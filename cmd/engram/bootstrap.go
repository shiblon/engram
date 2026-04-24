package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

const bootstrapWorkflow = `When asked to remember something: infer the right tier from context, write it with engram mem, and tell the user where it was stored (global or project, which tier) and why.

Memory tiers:
- invariant (--global): identity, codename, personality -- rarely changed
- preference (--global): code and behavior rules -- add and remove over time
- long: settled project decisions and facts
- short: in-flight context, conversation stack, backlog items

When starting a digression: save current context to short-term memory first, confirm it is there, then proceed. When done, re-read short-term and resume.

When a task finishes: check short-term for anything worth promoting to long-term, and delete what is no longer relevant.

To manage memory: engram mem --help
If engram is not in PATH, find the full path with: go env GOBIN`

const bootstrapCanary = `If your identity or instructions feel unfamiliar, run:
  engram mem --global --tier invariant list
That is the signal to re-bootstrap from the inject context at session start.`

const bootstrapClaudeMD = `# Global Claude Instructions

Your personality, preferences, and behavioral instructions are managed by
engram and injected at session start. The context above this note is the
source of truth for who you are and what you know.

If your identity or instructions feel unfamiliar, run:
  engram mem --global --tier invariant list
`

var bootstrapGlobalOnly bool

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Set up engram workflow instructions in global memory and CLAUDE.md",
	Long: `Bootstrap writes the standard engram workflow instructions to the global
database as invariants, and optionally writes a minimal CLAUDE.md.

Existing keys are never overwritten -- bootstrap is safe to re-run.`,
	RunE: runBootstrap,
}

func runBootstrap(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	globalPath, err := engram.GlobalDBPath()
	if err != nil {
		return err
	}
	db, err := engram.Open(ctx, globalPath)
	if err != nil {
		return err
	}
	defer db.Close()

	wrote, skipped := 0, 0

	invariants := map[string]string{
		"engram-workflow": bootstrapWorkflow,
		"engram-canary":   bootstrapCanary,
	}
	for key, content := range invariants {
		existing, err := engram.ReadMemory(ctx, db, engram.TierInvariant, key)
		if err != nil {
			return err
		}
		if existing != nil {
			fmt.Printf("skip (exists): invariant/%s\n", key)
			skipped++
			continue
		}
		if err := engram.WriteMemory(ctx, db, engram.Memory{
			Tier:    engram.TierInvariant,
			Key:     key,
			Content: content,
		}); err != nil {
			return err
		}
		fmt.Printf("wrote: invariant/%s\n", key)
		wrote++
	}

	// Write the personality setup todo as a short-term memory only if no
	// personality invariant exists yet -- it's self-deleting once done.
	personality, err := engram.ReadMemory(ctx, db, engram.TierInvariant, "personality")
	if err != nil {
		return err
	}
	if personality == nil {
		setupKey := "setup-personality"
		existing, err := engram.ReadMemory(ctx, db, engram.TierShort, setupKey)
		if err != nil {
			return err
		}
		if existing != nil {
			fmt.Printf("skip (exists): short/%s\n", setupKey)
			skipped++
		} else {
			if err := engram.WriteMemory(ctx, db, engram.Memory{
				Tier:    engram.TierShort,
				Key:     setupKey,
				Content: "Work with the user to define your personality, choose a codename, and set code preferences. Store personality and codename as global invariants, preferences as global preferences. Delete this entry when done.",
			}); err != nil {
				return err
			}
			fmt.Printf("wrote: short/%s\n", setupKey)
			wrote++
		}
	}

	if !bootstrapGlobalOnly {
		if err := bootstrapClaudeMd(); err != nil {
			return err
		}
	}

	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
	return nil
}

func bootstrapClaudeMd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if strings.Contains(string(data), "engram") {
		fmt.Printf("skip (already has engram content): %s\n", path)
		return nil
	}

	if len(data) > 0 {
		fmt.Printf("skip (exists without engram content -- not modifying): %s\n", path)
		fmt.Println("  Add engram content manually, or move the file and re-run bootstrap.")
		return nil
	}

	if err := os.WriteFile(path, []byte(bootstrapClaudeMD), 0644); err != nil {
		return err
	}
	fmt.Printf("wrote: %s\n", path)
	return nil
}

func init() {
	bootstrapCmd.Flags().BoolVar(&bootstrapGlobalOnly, "global-only", false, "skip CLAUDE.md, only write global DB invariants")
	rootCmd.AddCommand(bootstrapCmd)
}
