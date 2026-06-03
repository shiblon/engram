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
		if db, err := engram.OpenGlobalDB(ctx); err == nil {
			if m, err := engram.ReadMemory(ctx, db, engram.TierInvariant, "codename"); err == nil && m != nil {
				codename = m.Content
			}
			if items, err := engram.ListMemories(ctx, db, engram.TierShort); err == nil {
				shortCount += len(items)
			}
			db.Close()
		}
	}

	project := ""
	longCount := 0
	if cwd, err := os.Getwd(); err == nil {
		if root, err := engram.FindProjectRoot(cwd); err == nil {
			project = filepath.Base(root)
			if db, err := engram.OpenProjectDB(ctx, root); err == nil {
				if items, err := engram.ListMemories(ctx, db, engram.TierLong); err == nil {
					longCount = len(items)
				}
				if items, err := engram.ListMemories(ctx, db, engram.TierShort); err == nil {
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
