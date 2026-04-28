package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var memWriteCmd = &cobra.Command{
	Use:   "write <key> <content>",
	Short: "Write (upsert) a memory entry",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		content := strings.Join(args[1:], " ")
		m := engram.Memory{
			TS:      time.Now().UnixMilli(),
			Tier:    engram.Tier(memTier),
			Key:     args[0],
			Content: content,
		}
		if err := engram.WriteMemory(ctx, h.DB, m); err != nil {
			return err
		}
		scope := "project"
		if memGlobal {
			scope = "global"
		}
		fmt.Printf("stored in %s %s memory: %s\n", scope, memTier, args[0])
		return nil
	},
}

var memReadCmd = &cobra.Command{
	Use:   "read <key>",
	Short: "Read a memory entry. Omit --tier to search all tiers.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		if !cmd.Flag("tier").Changed {
			matches, err := engram.FindMemoryByKey(ctx, h.DB, args[0])
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", args[0])
			}
			for _, m := range matches {
				fmt.Printf("[%s/%s]\n%s\n\n", m.Tier, m.Key, m.Content)
			}
			return nil
		}

		m, err := engram.ReadMemory(ctx, h.DB, engram.Tier(memTier), args[0])
		if err != nil {
			return err
		}
		if m == nil {
			return fmt.Errorf("not found: %s/%s", memTier, args[0])
		}
		fmt.Printf("[%s/%s]\n%s\n", m.Tier, m.Key, m.Content)
		return nil
	},
}

var memListJSON bool

var memListCmd = &cobra.Command{
	Use:   "list",
	Short: "List memories. Omit --tier to list all tiers.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		var memories []engram.Memory
		if cmd.Flag("tier").Changed {
			memories, err = engram.ListMemories(ctx, h.DB, engram.Tier(memTier))
		} else {
			for _, t := range []engram.Tier{engram.TierInvariant, engram.TierPreference, engram.TierLong, engram.TierShort} {
				ms, err := engram.ListMemories(ctx, h.DB, t)
				if err != nil {
					return err
				}
				memories = append(memories, ms...)
			}
		}
		if err != nil {
			return err
		}

		if memListJSON {
			out, err := json.Marshal(memories)
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		}

		if len(memories) == 0 {
			fmt.Println("no memories")
			return nil
		}
		for i, m := range memories {
			fmt.Printf("%d. [%s/%s] %s\n", i+1, m.Tier, m.Key, m.Content)
		}
		return nil
	},
}

var memDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Delete a memory entry. Omit --tier to delete if unambiguous.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		if !cmd.Flag("tier").Changed {
			matches, err := engram.FindMemoryByKey(ctx, h.DB, args[0])
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", args[0])
			}
			if len(matches) > 1 {
				fmt.Printf("ambiguous: %q found in multiple tiers, specify --tier:\n", args[0])
				for _, m := range matches {
					fmt.Printf("  %s/%s\n", m.Tier, m.Key)
				}
				return fmt.Errorf("ambiguous key")
			}
			return engram.DeleteMemory(ctx, h.DB, matches[0].Tier, args[0])
		}

		return engram.DeleteMemory(ctx, h.DB, engram.Tier(memTier), args[0])
	},
}

var (
	promoteFrom string
	promoteTo   string
)

var memPromoteCmd = &cobra.Command{
	Use:   "promote <key>",
	Short: "Move a memory from one tier to another",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		// If --from not explicitly set, find the tier automatically.
		from := engram.Tier(promoteFrom)
		if !cmd.Flag("from").Changed {
			matches, err := engram.FindMemoryByKey(ctx, h.DB, args[0])
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", args[0])
			}
			if len(matches) > 1 {
				fmt.Printf("ambiguous: %q found in multiple tiers, specify --from:\n", args[0])
				for _, m := range matches {
					fmt.Printf("  %s/%s\n", m.Tier, m.Key)
				}
				return fmt.Errorf("ambiguous key")
			}
			from = matches[0].Tier
		}

		if err := engram.PromoteMemory(ctx, h.DB, args[0],
			from, engram.Tier(promoteTo)); err != nil {
			return err
		}
		fmt.Printf("promoted %q from %s to %s\n", args[0], from, promoteTo)
		return nil
	},
}

var memPopCmd = &cobra.Command{
	Use:   "pop",
	Short: "Read and remove the most recent short-term memory",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		m, err := engram.PopMemory(ctx, h.DB, engram.Tier(memTier))
		if err != nil {
			return err
		}
		if m == nil {
			fmt.Printf("no %s memories\n", memTier)
			return nil
		}
		fmt.Printf("[%s] %s\n%s\n", m.Tier, m.Key, m.Content)
		return nil
	},
}

func init() {
	memListCmd.Flags().BoolVar(&memListJSON, "json", false, "output as JSON array")
	memPromoteCmd.Flags().StringVar(&promoteFrom, "from", string(engram.TierShort), "source tier")
	memPromoteCmd.Flags().StringVar(&promoteTo, "to", string(engram.TierLong), "destination tier")

	memCmd.AddCommand(memWriteCmd, memReadCmd, memListCmd, memDeleteCmd, memPromoteCmd, memPopCmd)
}
