package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var migrateCleanup bool

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate legacy database (.claude/engram.db) to canonical location (.engram/mem.db)",
	Long: `Migrate copies events and memories from the legacy database location to the
canonical location. Run this once per project (and once globally with --global)
after upgrading to a version that uses the new .engram/ path.

Events are copied as-is. Memories are merged: the newer entry wins when the same
key exists in both databases. Run migrate only once -- events have no duplicate
protection and will be double-counted if copied twice.

Use --cleanup to remove the legacy files after a successful migration.`,
	RunE: runMigrate,
}

func runMigrate(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	var srcPath, dstPath string
	if migrateGlobal {
		src, err := engram.LegacyGlobalDBPath()
		if err != nil {
			return err
		}
		dst, err := engram.GlobalDBPath()
		if err != nil {
			return err
		}
		srcPath, dstPath = src, dst
	} else {
		root, err := engram.FindProjectRoot(effectiveCWD())
		if err != nil {
			return err
		}
		srcPath = engram.LegacyDBPath(root)
		dstPath = engram.DBPath(root)
	}

	if srcPath == dstPath {
		fmt.Println("source and destination are the same, nothing to do")
		return nil
	}
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		fmt.Printf("nothing to migrate: %s does not exist\n", srcPath)
		return nil
	}

	fmt.Printf("%s -> %s\n", srcPath, dstPath)

	src, err := engram.Open(ctx, srcPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		src.Close()
		return fmt.Errorf("create destination dir: %w", err)
	}
	dst, err := engram.Open(ctx, dstPath)
	if err != nil {
		src.Close()
		return fmt.Errorf("open destination: %w", err)
	}

	result, err := engram.Migrate(ctx, src, dst)
	src.Close()
	dst.Close()
	if err != nil {
		return err
	}

	fmt.Printf("copied %d events, %d memories\n", result.Events, result.Memories)
	for _, c := range result.Conflicts {
		fmt.Printf("  conflict: %s\n", c)
	}

	if migrateCleanup {
		if err := os.Remove(srcPath); err != nil {
			return fmt.Errorf("cleanup: %w", err)
		}
		for _, ext := range []string{"-shm", "-wal"} {
			os.Remove(srcPath + ext)
		}
		fmt.Printf("removed: %s\n", srcPath)
	}
	return nil
}

var migrateGlobal bool

func init() {
	migrateCmd.Flags().BoolVar(&migrateCleanup, "cleanup", false, "remove legacy files after successful migration")
	migrateCmd.Flags().BoolVarP(&migrateGlobal, "global", "g", false, "migrate global database")
	rootCmd.AddCommand(migrateCmd)
}
