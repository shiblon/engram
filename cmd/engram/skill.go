package main

import (
	"context"
	"fmt"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var skillDiscoverAcknowledge bool

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Discover and manage task-triggered project workflows",
	Long: `Skills are focused workflows loaded when their trigger matches the current task.

This initial implementation sketches repository automation discovery. It does
not yet persist full skill definitions: scripts remain in the repository, and
the discovery command helps classify which are direct tools, parts of skills,
internal plumbing, stale, or intentionally ignored.`,
}

var skillDiscoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Find repository automation entry points that may need cataloging",
	Long: `Find conventional automation entry points without executing them.

Review each candidate's documentation and call sites, then classify it as:

  direct tool    one operation with a recognizable trigger
  skill member   part of a workflow requiring instructions or judgment
  internal       implementation detail called by another entry point
  review         unclear, stale, or requiring user input
  ignore         generated, vendored, example, or deliberately uncataloged

After every candidate is classified or deliberately ignored, pass
--acknowledge to record the current content snapshot. Inject will prompt again
when a discovered entry point is added, removed, or changed. Acknowledgment is
bookkeeping, not long-term memory.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := context.Background()
		root, err := engram.FindProjectRoot(effectiveCWD())
		if err != nil {
			return fmt.Errorf("skill discover: %w", err)
		}
		candidates, digest, err := engram.DiscoverAutomation(root)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No conventional automation entry points found.")
			return nil
		}

		for _, candidate := range candidates {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", candidate.Kind, candidate.Path)
		}
		if !skillDiscoverAcknowledge {
			fmt.Fprintln(cmd.OutOrStdout(), "\nReview and classify these candidates; rerun with --acknowledge when complete.")
			return nil
		}

		db, err := engram.OpenProjectDB(ctx, root)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := engram.MarkAutomationCatalogReviewed(ctx, db, digest); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nAcknowledged %d automation entry points at their current versions.\n", len(candidates))
		return nil
	},
}

func init() {
	skillDiscoverCmd.Flags().BoolVar(&skillDiscoverAcknowledge, "acknowledge", false, "mark the current candidate versions as reviewed")
	markExperimental(skillDiscoverCmd, "skill-discovery")
	skillCmd.AddCommand(skillDiscoverCmd)
	rootCmd.AddCommand(skillCmd)
}
