package main

import (
	"context"
	"fmt"
	"os"

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

	name := "engram"
	if globalPath, err := engram.GlobalDBPath(); err == nil {
		if db, err := engram.Open(ctx, globalPath); err == nil {
			if m, err := engram.ReadMemory(ctx, db, engram.TierInvariant, "codename"); err == nil && m != nil {
				name = m.Content
			}
			db.Close()
		}
	}

	shortCount := 0
	if globalPath, err := engram.GlobalDBPath(); err == nil {
		if db, err := engram.Open(ctx, globalPath); err == nil {
			if items, err := engram.ListMemories(ctx, db, engram.TierShort); err == nil {
				shortCount += len(items)
			}
			db.Close()
		}
	}
	cwd, err := os.Getwd()
	if err == nil {
		if root, err := engram.FindProjectRoot(cwd); err == nil {
			if db, err := engram.Open(ctx, engram.DBPath(root)); err == nil {
				if items, err := engram.ListMemories(ctx, db, engram.TierShort); err == nil {
					shortCount += len(items)
				}
				db.Close()
			}
		}
	}

	if shortCount > 0 {
		fmt.Printf("%s · %d short", name, shortCount)
	} else {
		fmt.Print(name)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
