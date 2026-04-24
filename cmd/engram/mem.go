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

var memCmd = &cobra.Command{
	Use:   "mem",
	Short: "Manage agent memory (invariants, preferences, long-term, short-term)",
}

// shared flags
var memGlobal bool
var memTier string

func openMemDB(ctx context.Context) (*engram.DBHandle, error) {
	if memGlobal {
		path, err := engram.GlobalDBPath()
		if err != nil {
			return nil, err
		}
		db, err := engram.Open(ctx, path)
		if err != nil {
			return nil, err
		}
		return &engram.DBHandle{DB: db, Path: path}, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := engram.FindProjectRoot(cwd)
	if err != nil {
		return nil, err
	}
	path := engram.DBPath(root)
	db, err := engram.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return &engram.DBHandle{DB: db, Path: path}, nil
}

// write

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

// read

var memReadCmd = &cobra.Command{
	Use:   "read <key>",
	Short: "Read a memory entry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		m, err := engram.ReadMemory(ctx, h.DB, engram.Tier(memTier), args[0])
		if err != nil {
			return err
		}
		if m == nil {
			fmt.Printf("no %s memory found with key %q\n", memTier, args[0])
			return nil
		}
		fmt.Printf("[%s] %s\n%s\n", m.Tier, m.Key, m.Content)
		return nil
	},
}

// list

var memListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all memories in a tier",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		memories, err := engram.ListMemories(ctx, h.DB, engram.Tier(memTier))
		if err != nil {
			return err
		}
		if len(memories) == 0 {
			fmt.Printf("no %s memories\n", memTier)
			return nil
		}
		for i, m := range memories {
			fmt.Printf("%d. [%s] %s\n", i+1, m.Key, m.Content)
		}
		return nil
	},
}

// delete

var memDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Delete a memory entry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		return engram.DeleteMemory(ctx, h.DB, engram.Tier(memTier), args[0])
	},
}

// promote

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

		if err := engram.PromoteMemory(ctx, h.DB, args[0],
			engram.Tier(promoteFrom), engram.Tier(promoteTo)); err != nil {
			return err
		}
		fmt.Printf("promoted %q from %s to %s\n", args[0], promoteFrom, promoteTo)
		return nil
	},
}

// pop

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

// dump

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

		dir := dumpDir
		if dir == "" {
			if memGlobal {
				home, _ := os.UserHomeDir()
				dir = filepath.Join(home, ".claude", "memory")
			} else {
				cwd, _ := os.Getwd()
				root, err := engram.FindProjectRoot(cwd)
				if err != nil {
					return err
				}
				dir = filepath.Join(root, ".claude", "memory")
			}
		}
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

// load

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

		dir := dumpDir
		if dir == "" {
			if memGlobal {
				home, _ := os.UserHomeDir()
				dir = filepath.Join(home, ".claude", "memory")
			} else {
				cwd, _ := os.Getwd()
				root, err := engram.FindProjectRoot(cwd)
				if err != nil {
					return err
				}
				dir = filepath.Join(root, ".claude", "memory")
			}
		}

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

// parseMemoryMD parses the tier.md format: ## key\ncontent\n\n
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
	// shared flags
	memCmd.PersistentFlags().BoolVar(&memGlobal, "global", false, "use global (~/.claude) database")
	memCmd.PersistentFlags().StringVar(&memTier, "tier", string(engram.TierShort), "memory tier (invariant, preference, long, short)")

	// promote flags
	memPromoteCmd.Flags().StringVar(&promoteFrom, "from", string(engram.TierShort), "source tier")
	memPromoteCmd.Flags().StringVar(&promoteTo, "to", string(engram.TierLong), "destination tier")

	// dump/load flags
	memDumpCmd.Flags().StringVar(&dumpDir, "dir", "", "output directory (default: .claude/memory or ~/.claude/memory)")
	memLoadCmd.Flags().StringVar(&dumpDir, "dir", "", "input directory (default: .claude/memory or ~/.claude/memory)")

	memCmd.AddCommand(memWriteCmd, memReadCmd, memListCmd, memDeleteCmd,
		memPromoteCmd, memPopCmd, memDumpCmd, memLoadCmd)
}
