package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print a brief status line for the current session",
	RunE:  runStatus,
}

func runStatus(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	codename := ""
	shortCount := 0
	if engram.GlobalDBExists() {
		db, err := engram.OpenGlobalDBReadOnly(ctx)
		if err != nil {
			return fmt.Errorf("status: %w", err)
		}
		if m, err := engram.ReadMemory(ctx, db, engram.TierInvariant, "codename"); err != nil {
			db.Close()
			return fmt.Errorf("status: read global codename: %w", err)
		} else if m != nil {
			codename = m.Content
		}
		if items, err := engram.ListMemories(ctx, db, engram.TierShort); err != nil {
			db.Close()
			return fmt.Errorf("status: list global short memory: %w", err)
		} else {
			shortCount += len(items)
		}
		db.Close()
	}

	project := ""
	longCount := 0
	if cwd, err := os.Getwd(); err == nil {
		if root, err := engram.FindProjectRoot(cwd); err == nil {
			project = filepath.Base(root)
			if engram.ProjectDBExists(root) {
				db, err := engram.OpenProjectDBReadOnly(ctx, root)
				if err != nil {
					return fmt.Errorf("status: %w", err)
				}
				if items, err := engram.ListMemories(ctx, db, engram.TierLong); err != nil {
					db.Close()
					return fmt.Errorf("status: list project long memory: %w", err)
				} else {
					longCount = len(items)
				}
				if items, err := engram.ListMemories(ctx, db, engram.TierShort); err != nil {
					db.Close()
					return fmt.Errorf("status: list project short memory: %w", err)
				} else {
					shortCount += len(items)
				}
				db.Close()
			}
		}
	}

	fmt.Print(engram.FormatStatusLine(codename, project, longCount, shortCount))
	return nil
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
