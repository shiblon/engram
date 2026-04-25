package main

import (
	"context"
	"os"

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

func init() {
	memCmd.PersistentFlags().BoolVar(&memGlobal, "global", false, "use global (~/.claude) database")
	memCmd.PersistentFlags().StringVar(&memTier, "tier", string(engram.TierShort), "memory tier (invariant, preference, long, short)")
}
