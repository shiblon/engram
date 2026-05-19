package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var dumpDir string

var memDumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Export memories to markdown files",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		dir := resolveMemDir()
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}

		tiers := []engram.Tier{engram.TierInvariant, engram.TierPreference, engram.TierLong, engram.TierShort, engram.TierCold}
		if memTier != "" {
			tiers = []engram.Tier{engram.Tier(memTier)}
		}

		for _, tier := range tiers {
			memories, err := engram.ListMemories(ctx, h.DB, tier)
			if err != nil {
				return err
			}
			if len(memories) == 0 {
				continue
			}
			path := filepath.Join(dir, string(tier)+".md")
			if err := os.WriteFile(path, []byte(engram.FormatMemoryMD(tier, memories)), 0644); err != nil {
				return err
			}
			fmt.Printf("wrote %s\n", path)
		}
		return nil
	},
}

var memLoadCmd = &cobra.Command{
	Use:   "load",
	Short: "Import memories from markdown files",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		dir := resolveMemDir()

		tiers := []engram.Tier{engram.TierInvariant, engram.TierPreference, engram.TierLong, engram.TierShort, engram.TierCold}
		if memTier != "" {
			tiers = []engram.Tier{engram.Tier(memTier)}
		}

		for _, tier := range tiers {
			path := filepath.Join(dir, string(tier)+".md")
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			memories, err := engram.ParseMemoryMD(tier, string(data))
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			for _, m := range memories {
				if err := engram.WriteMemory(ctx, h.DB, m); err != nil {
					return err
				}
			}
			fmt.Printf("loaded %d entries from %s\n", len(memories), path)
		}
		return nil
	},
}

func resolveMemDir() string {
	if dumpDir != "" {
		return dumpDir
	}
	if memGlobal {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".claude", "memory")
	}
	cwd := effectiveCWD()
	root, err := engram.FindProjectRoot(cwd)
	if err != nil {
		return "context"
	}
	return filepath.Join(root, "context")
}

func init() {
	memDumpCmd.Flags().StringVar(&dumpDir, "dir", "", "output directory (default: context/ for project, ~/.claude/memory with --global)")
	memLoadCmd.Flags().StringVar(&dumpDir, "dir", "", "input directory (default: context/ for project, ~/.claude/memory with --global)")

	memCmd.AddCommand(memDumpCmd, memLoadCmd)
}
