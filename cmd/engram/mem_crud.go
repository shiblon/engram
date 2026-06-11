package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

// syncStandingIfTouched re-renders the per-platform standing-memory files when a
// global invariant or preference was just mutated -- the render-on-write half of
// the channel strategy that keeps both tiers on the authoritative always-loaded
// channel. Best-effort: a sync failure must never fail the mem operation itself.
func syncStandingIfTouched(ctx context.Context, h *engram.DBHandle, tiers ...engram.Tier) {
	if !memUsesGlobal() {
		return
	}
	for _, t := range tiers {
		if t == engram.TierInvariant || t == engram.TierPreference {
			if err := engram.SyncStandingMemory(ctx, h.DB); err != nil {
				fmt.Fprintf(os.Stderr, "engram: sync standing memory: %v\n", err)
			}
			return
		}
	}
}

func memAgentName() (string, error) {
	return engram.NormalizeAgent(memAgent)
}

func memStoredKey(key string, tier engram.Tier) (string, error) {
	agent, err := memAgentName()
	if err != nil {
		return "", err
	}
	if agent == "" {
		return key, nil
	}
	if !engram.IsStandingTier(tier) {
		return "", fmt.Errorf("--agent only applies to global invariant/preference memory; specify --tier invariant or --tier preference")
	}
	return engram.AgentLayerKey(agent, key)
}

func memDefaultTiers(cmd *cobra.Command) []engram.Tier {
	if cmd.Flag("tier").Changed {
		return []engram.Tier{engram.Tier(memTier)}
	}
	if memAgent != "" {
		return engram.StandingTiers
	}
	return []engram.Tier{engram.TierInvariant, engram.TierPreference, engram.TierLong, engram.TierShort}
}

func memViewTiers(cmd *cobra.Command) ([]engram.Tier, error) {
	tiers := memDefaultTiers(cmd)
	if memAgent == "" {
		return tiers, nil
	}
	for _, t := range tiers {
		if !engram.IsStandingTier(t) {
			return nil, fmt.Errorf("--agent only applies to global invariant/preference memory")
		}
	}
	return tiers, nil
}

func printMemories(memories []engram.Memory) {
	for i, m := range memories {
		fmt.Printf("%d. %s %s\n", i+1, engram.MemoryLabel(m), m.Content)
	}
}

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
		tier := engram.Tier(memTier)
		key, err := memStoredKey(args[0], tier)
		if err != nil {
			return err
		}
		m := engram.Memory{
			TS:      time.Now().UnixMilli(),
			Tier:    tier,
			Key:     key,
			Content: content,
		}
		if err := engram.WriteMemory(ctx, h.DB, m); err != nil {
			return err
		}
		syncStandingIfTouched(ctx, h, engram.Tier(memTier))
		scope := "project"
		if memGlobal {
			scope = "global"
		}
		if agent, _ := memAgentName(); agent != "" {
			fmt.Printf("stored in global %s %s layer: %s\n", agent, memTier, args[0])
			return nil
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

		if !cmd.Flag("tier").Changed || memAgent != "" || memUsesGlobal() {
			tiers, err := memViewTiers(cmd)
			if err != nil {
				return err
			}
			matches, err := engram.ListMemoriesForView(ctx, h.DB, tiers, memAgent, args[0])
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", args[0])
			}
			for _, m := range matches {
				fmt.Printf("%s\n%s\n\n", engram.MemoryLabel(m), m.Content)
			}
			return nil
		}

		key, err := memStoredKey(args[0], engram.Tier(memTier))
		if err != nil {
			return err
		}
		m, err := engram.ReadMemory(ctx, h.DB, engram.Tier(memTier), key)
		if err != nil {
			return err
		}
		if m == nil {
			return fmt.Errorf("not found: %s/%s", memTier, args[0])
		}
		fmt.Printf("%s\n%s\n", engram.MemoryLabel(*m), m.Content)
		return nil
	},
}

var memListJSON bool

var memListCmd = &cobra.Command{
	Use:   "list [key]",
	Short: "List memories. Omit --tier to list all tiers (cold excluded; use --tier cold).",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		var key string
		if len(args) > 0 {
			key = args[0]
		}
		tiers, err := memViewTiers(cmd)
		if err != nil {
			return err
		}
		memories, err := engram.ListMemoriesForView(ctx, h.DB, tiers, memAgent, key)
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
		printMemories(memories)
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

		tier := engram.Tier(memTier)
		key := args[0]
		if !cmd.Flag("tier").Changed {
			tiers, err := memViewTiers(cmd)
			if err != nil {
				return err
			}
			matches, err := engram.ListMemoriesForView(ctx, h.DB, tiers, memAgent, args[0])
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", args[0])
			}
			if len(matches) > 1 {
				fmt.Printf("ambiguous: %q found in multiple tiers, specify --tier:\n", args[0])
				for _, m := range matches {
					fmt.Printf("  %s\n", engram.MemoryLabel(m))
				}
				return fmt.Errorf("ambiguous key")
			}
			tier = matches[0].Tier
			key = matches[0].Key
		} else {
			key, err = memStoredKey(args[0], tier)
			if err != nil {
				return err
			}
		}

		if err := engram.DeleteMemory(ctx, h.DB, tier, key); err != nil {
			return err
		}
		syncStandingIfTouched(ctx, h, tier)
		return nil
	},
}

var (
	moveFrom string
	moveTo   string
)

var memMoveCmd = &cobra.Command{
	Use:   "move <key>",
	Short: "Move a memory to a different tier",
	Long: `Move a memory from one tier to another within the same database.

The source tier is inferred automatically unless --from is specified.
Use --to to specify the destination tier (required).

Tiers: invariant, preference, long, short, cold`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		if moveTo == "" {
			return fmt.Errorf("--to is required")
		}
		if memAgent != "" && !engram.IsStandingTier(engram.Tier(moveTo)) {
			return fmt.Errorf("--agent only applies to global invariant/preference memory; --to must be invariant or preference")
		}

		from := engram.Tier(moveFrom)
		key := args[0]
		if !cmd.Flag("from").Changed {
			tiers, err := memViewTiers(cmd)
			if err != nil {
				return err
			}
			matches, err := engram.ListMemoriesForView(ctx, h.DB, tiers, memAgent, args[0])
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", args[0])
			}
			if len(matches) > 1 {
				fmt.Printf("ambiguous: %q found in multiple tiers, specify --from:\n", args[0])
				for _, m := range matches {
					fmt.Printf("  %s\n", engram.MemoryLabel(m))
				}
				return fmt.Errorf("ambiguous key")
			}
			from = matches[0].Tier
			key = matches[0].Key
		} else {
			var err error
			key, err = memStoredKey(args[0], from)
			if err != nil {
				return err
			}
		}

		if err := engram.MoveMemory(ctx, h.DB, key,
			from, engram.Tier(moveTo)); err != nil {
			return err
		}
		syncStandingIfTouched(ctx, h, from, engram.Tier(moveTo))
		fmt.Printf("moved %q from %s to %s\n", args[0], from, moveTo)
		return nil
	},
}

var memPopCmd = &cobra.Command{
	Use:   "pop",
	Short: "Read and remove the most recent short-term memory",
	RunE: func(cmd *cobra.Command, args []string) error {
		if memAgent != "" {
			return fmt.Errorf("--agent only applies to global invariant/preference memory")
		}
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
	memMoveCmd.Flags().StringVar(&moveFrom, "from", "", "source tier (inferred if omitted)")
	memMoveCmd.Flags().StringVar(&moveTo, "to", "", "destination tier (required)")

	memCmd.AddCommand(memWriteCmd, memReadCmd, memListCmd, memDeleteCmd, memMoveCmd, memPopCmd)
}
