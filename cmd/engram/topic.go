package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var topicCmd = &cobra.Command{
	Use:   "topic",
	Short: "Share short-lived findings through project pubsub topics",
	Long: `Share short-lived findings through project pubsub topics.

Inject lists topic names, purposes, and retirement conditions. Use 'topic list
<topic>' to discover subtopics, then read or post only where relevant. Subtopic
history is lossy by design; promote durable findings to memory separately.`,
	Args: cobra.NoArgs,
}

func openTopicDB(ctx context.Context, readOnly bool) (*engram.DBHandle, error) {
	if readOnly {
		return openScopeDBReadOnly(ctx, false)
	}
	return openScopeDB(ctx, false)
}

func parseTopicTarget(value string) (string, string, error) {
	topic, subtopic, ok := strings.Cut(value, "/")
	if !ok || topic == "" || subtopic == "" || strings.Contains(subtopic, "/") {
		return "", "", fmt.Errorf("target must be <topic>/<subtopic>")
	}
	return topic, subtopic, nil
}

func parseAfter(value string) (*int64, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	ts, err := engram.ParseTopicTimestamp(value)
	if err != nil {
		return nil, err
	}
	return &ts, nil
}

var (
	topicPurpose    string
	topicRetireWhen string
)

var topicCreateCmd = &cobra.Command{
	Use:   "create <topic>",
	Short: "Create a topic with its purpose and retirement condition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openTopicDB(ctx, false)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		if err := engram.CreateTopic(ctx, h.DB, engram.Topic{
			Name: args[0], Purpose: topicPurpose, RetireWhen: topicRetireWhen,
		}); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created topic %s\n", args[0])
		return nil
	},
}

var topicListCmd = &cobra.Command{
	Use:   "list [topic]",
	Short: "List topics, or the subtopics under one topic",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openTopicDB(ctx, true)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		if len(args) == 0 {
			topics, err := engram.ListTopics(ctx, h.DB)
			if err != nil {
				return err
			}
			if len(topics) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no active topics")
				return nil
			}
			for _, topic := range topics {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tretire when: %s\n",
					topic.Name, topic.Purpose, topic.RetireWhen)
			}
			return nil
		}
		subtopics, err := engram.ListSubtopics(ctx, h.DB, args[0])
		if err != nil {
			return err
		}
		if len(subtopics) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no subtopics")
			return nil
		}
		for _, subtopic := range subtopics {
			fmt.Fprintln(cmd.OutOrStdout(), subtopic.Name)
		}
		return nil
	},
}

var topicReadAfter string

var topicReadCmd = &cobra.Command{
	Use:   "read <topic>/<subtopic>",
	Short: "Read a compacted subtopic, optionally after a timestamp",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		topic, subtopic, err := parseTopicTarget(args[0])
		if err != nil {
			return err
		}
		after, err := parseAfter(topicReadAfter)
		if err != nil {
			return err
		}
		ctx := context.Background()
		h, err := openTopicDB(ctx, true)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		result, err := engram.ReadSubtopic(ctx, h.DB, topic, subtopic, after)
		if err != nil {
			return err
		}
		printTopicRead(cmd.OutOrStdout(), result)
		return nil
	},
}

func printTopicRead(w io.Writer, result engram.TopicRead) {
	if result.HistoryCompacted {
		fmt.Fprintf(w, "history compacted through %s\n", engram.FormatTopicTimestamp(result.CheckpointThrough))
	}
	if result.Checkpoint != "" {
		fmt.Fprintf(w, "checkpoint through %s\n%s\n",
			engram.FormatTopicTimestamp(result.CheckpointThrough), result.Checkpoint)
	}
	for _, message := range result.Messages {
		fmt.Fprintf(w, "%s\t%s\n", engram.FormatTopicTimestamp(message.TS), message.Body)
	}
	if result.Checkpoint == "" && len(result.Messages) == 0 {
		fmt.Fprintln(w, "no messages")
	}
}

var topicPostAfter string

var topicPostCmd = &cobra.Command{
	Use:   "post <topic>/<subtopic> <message>",
	Short: "Post a message and optionally return intervening messages",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		topic, subtopic, err := parseTopicTarget(args[0])
		if err != nil {
			return err
		}
		after, err := parseAfter(topicPostAfter)
		if err != nil {
			return err
		}
		ctx := context.Background()
		h, err := openTopicDB(ctx, false)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		result, err := engram.PostTopicMessage(ctx, h.DB, topic, subtopic,
			strings.Join(args[1:], " "), after)
		if err != nil {
			return err
		}
		if result.Unseen != nil && (result.Unseen.Checkpoint != "" || len(result.Unseen.Messages) > 0) {
			fmt.Fprintln(cmd.OutOrStdout(), "unseen before post:")
			printTopicRead(cmd.OutOrStdout(), *result.Unseen)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "posted at %s\n", engram.FormatTopicTimestamp(result.Message.TS))
		if result.CompactionRecommended {
			fmt.Fprintf(cmd.ErrOrStderr(), "compaction recommended: engram topic compact begin %s/%s\n", topic, subtopic)
		}
		return nil
	},
}

var topicRetireCmd = &cobra.Command{
	Use:   "retire <topic>",
	Short: "Retire a topic and discard its ephemeral contents",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openTopicDB(ctx, false)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		found, err := engram.RetireTopic(ctx, h.DB, args[0])
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: %s", engram.ErrTopicNotFound, args[0])
		}
		fmt.Fprintf(cmd.OutOrStdout(), "retired topic %s\n", args[0])
		return nil
	},
}

var topicCompactCmd = &cobra.Command{
	Use:   "compact",
	Short: "Stage and apply a lossy subtopic checkpoint",
	Long: `Compact in two guarded phases. 'begin' claims one immutable prefix and
returns a stage token plus its checkpoint and messages. Summarize that material,
then 'apply' the token and replacement checkpoint. Concurrent posts remain in the
tail; concurrent compactors cannot overwrite one another.`,
	Args: cobra.NoArgs,
}

var topicCompactBeginCmd = &cobra.Command{
	Use:   "begin <topic>/<subtopic>",
	Short: "Claim a subtopic prefix and print the material to compact",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		topic, subtopic, err := parseTopicTarget(args[0])
		if err != nil {
			return err
		}
		ctx := context.Background()
		h, err := openTopicDB(ctx, false)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		stage, err := engram.BeginTopicCompaction(ctx, h.DB, topic, subtopic)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "stage: %s\nexpires: %s\ncutoff: %s\n",
			stage.Token, engram.FormatTopicTimestamp(stage.ExpiresAt),
			engram.FormatTopicTimestamp(stage.CutoffTS))
		if stage.PreviousCheckpoint != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "checkpoint through %s\n%s\n",
				engram.FormatTopicTimestamp(stage.CheckpointThrough), stage.PreviousCheckpoint)
		}
		for _, message := range stage.Messages {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n",
				engram.FormatTopicTimestamp(message.TS), message.Body)
		}
		return nil
	},
}

var topicCompactApplyCmd = &cobra.Command{
	Use:   "apply <stage-token> <checkpoint>",
	Short: "Apply a staged checkpoint if its generation is still current",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openTopicDB(ctx, false)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		result, err := engram.ApplyTopicCompaction(ctx, h.DB, args[0], strings.Join(args[1:], " "))
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "compacted %s/%s through %s (generation %d)\n",
			result.Topic, result.Subtopic, engram.FormatTopicTimestamp(result.Through), result.Generation)
		return nil
	},
}

var topicCompactCancelCmd = &cobra.Command{
	Use:   "cancel <stage-token>",
	Short: "Release a compaction stage before its lease expires",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openTopicDB(ctx, false)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		cancelled, err := engram.CancelTopicCompaction(ctx, h.DB, args[0])
		if err != nil {
			return err
		}
		if !cancelled {
			return engram.ErrCompactionStageNotFound
		}
		fmt.Fprintln(cmd.OutOrStdout(), "compaction stage cancelled")
		return nil
	},
}

var (
	topicMonitorAfter string
	topicMonitorOnce  bool
)

var topicMonitorCmd = &cobra.Command{
	Use:   "monitor <topic>[/<subtopic>]",
	Short: "Stream edge-triggered topic changes without storing a cursor",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		topic, subtopic := args[0], ""
		if strings.Contains(args[0], "/") {
			var err error
			topic, subtopic, err = parseTopicTarget(args[0])
			if err != nil {
				return err
			}
		}
		after, err := parseAfter(topicMonitorAfter)
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		h, err := openTopicDB(ctx, true)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		encoder := json.NewEncoder(cmd.OutOrStdout())
		return engram.MonitorTopic(ctx, h.DB, topic, subtopic, engram.TopicMonitorOptions{
			After: after, Once: topicMonitorOnce,
		}, func(event engram.TopicEvent) error {
			out := struct {
				Event    string `json:"event"`
				Topic    string `json:"topic"`
				Subtopic string `json:"subtopic,omitempty"`
				At       string `json:"at,omitempty"`
			}{Event: event.Event, Topic: event.Topic, Subtopic: event.Subtopic}
			if event.TS > 0 {
				out.At = engram.FormatTopicTimestamp(event.TS)
			}
			return encoder.Encode(out)
		})
	},
}

func init() {
	topicCreateCmd.Flags().StringVar(&topicPurpose, "purpose", "", "short reason the topic exists")
	topicCreateCmd.Flags().StringVar(&topicRetireWhen, "retire-when", "", "event that retires the topic")
	topicReadCmd.Flags().StringVar(&topicReadAfter, "after", "", "return messages after this RFC3339 timestamp")
	topicPostCmd.Flags().StringVar(&topicPostAfter, "after", "", "also return intervening messages after this RFC3339 timestamp")
	topicMonitorCmd.Flags().StringVar(&topicMonitorAfter, "after", "", "emit changes after this RFC3339 timestamp")
	topicMonitorCmd.Flags().BoolVar(&topicMonitorOnce, "once", false, "exit after the first change")

	topicCompactCmd.AddCommand(topicCompactBeginCmd, topicCompactApplyCmd, topicCompactCancelCmd)
	topicCmd.AddCommand(topicCreateCmd, topicListCmd, topicReadCmd, topicPostCmd,
		topicCompactCmd, topicMonitorCmd, topicRetireCmd)
	markExperimental(topicCmd, "topics")
	rootCmd.AddCommand(topicCmd)
}
