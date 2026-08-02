package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var dumpDir string

// dumpMemories renders each non-empty tier in tiers to out. When dir is
// empty, it writes engram.FormatMemoryMD output straight to out (a blank line
// between tiers). When dir is non-empty, it creates dir and writes
// <dir>/<tier>.md files instead, reporting each write on stderr.
func dumpMemories(ctx context.Context, db *sql.DB, tiers []engram.Tier, dir string, out io.Writer) error {
	toStdout := dir == ""
	if !toStdout {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	wroteAny := false
	for _, tier := range tiers {
		memories, err := engram.ListMemories(ctx, db, tier)
		if err != nil {
			return err
		}
		if len(memories) == 0 {
			continue
		}
		md := engram.FormatMemoryMD(tier, memories)
		if toStdout {
			if wroteAny {
				fmt.Fprintln(out)
			}
			fmt.Fprint(out, md)
			if !strings.HasSuffix(md, "\n") {
				fmt.Fprintln(out)
			}
			wroteAny = true
			continue
		}
		path := filepath.Join(dir, string(tier)+".md")
		if err := os.WriteFile(path, []byte(md), 0644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	}
	return nil
}

var memDumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Export memories to markdown files",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDBReadOnly(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		tiers := []engram.Tier{engram.TierInvariant, engram.TierPreference, engram.TierLong, engram.TierShort, engram.TierCold}
		if memTier != "" {
			tiers = []engram.Tier{engram.Tier(memTier)}
		}

		return dumpMemories(ctx, h.DB, tiers, dumpDir, os.Stdout)
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
		if dir == "" {
			return fmt.Errorf("engram mem load: specify --dir (there is no default location)")
		}

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
				if err := engram.WriteMemory(ctx, h.DB, m,
					engram.WithCurationSource(engram.SourceLoad),
					engram.WithCurationScope(scopeName(memUsesGlobal()))); err != nil {
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
	// Project scope has no default location; `mem load` requires --dir.
	return ""
}

func init() {
	memDumpCmd.Flags().StringVar(&dumpDir, "dir", "", "write files to this directory instead of stdout")
	memLoadCmd.Flags().StringVar(&dumpDir, "dir", "", "directory to read <tier>.md files from (required; ~/.claude/memory with --global)")

	memCmd.AddCommand(memDumpCmd, memLoadCmd)
}
