package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

		tiers := []engram.Tier{engram.TierInvariant, engram.TierPreference, engram.TierLong, engram.TierShort}
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
			f, err := os.Create(path)
			if err != nil {
				return err
			}
			t := string(tier)
			fmt.Fprintf(f, "# %s%s\n\n", strings.ToUpper(t[:1]), t[1:])
			for _, m := range memories {
				fmt.Fprintf(f, "## %s\n%s\n\n", m.Key, m.Content)
			}
			f.Close()
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

		tiers := []engram.Tier{engram.TierInvariant, engram.TierPreference, engram.TierLong, engram.TierShort}
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
			memories, err := parseMemoryMD(tier, string(data))
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
		return ".claude/memory"
	}
	return filepath.Join(root, ".claude", "memory")
}

func parseMemoryMD(tier engram.Tier, data string) ([]engram.Memory, error) {
	var out []engram.Memory
	var key string
	var contentLines []string

	flush := func() {
		if key == "" {
			return
		}
		out = append(out, engram.Memory{
			TS:      time.Now().UnixMilli(),
			Tier:    tier,
			Key:     key,
			Content: strings.TrimSpace(strings.Join(contentLines, "\n")),
		})
		key = ""
		contentLines = nil
	}

	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") {
			flush()
			key = strings.TrimPrefix(line, "## ")
		} else if strings.HasPrefix(line, "# ") {
			// tier header, skip
		} else if key != "" {
			contentLines = append(contentLines, line)
		}
	}
	flush()
	return out, scanner.Err()
}

func init() {
	memDumpCmd.Flags().StringVar(&dumpDir, "dir", "", "output directory (default: .claude/memory or ~/.claude/memory)")
	memLoadCmd.Flags().StringVar(&dumpDir, "dir", "", "input directory (default: .claude/memory or ~/.claude/memory)")

	memCmd.AddCommand(memDumpCmd, memLoadCmd)
}
