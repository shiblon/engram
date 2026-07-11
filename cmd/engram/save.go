package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var saveOutput string

var saveCmd = &cobra.Command{
	Use:   "save",
	Short: "Save all engram state to a portable archive",
	Long: `Save snapshots all machine-local engram state into a single gzipped tar:
global memory + agenttools, every registered project's memory, and any
in-flight project-stage entries (pending restores).

The archive can be moved to another machine and unpacked with: engram restore <file>`,
	RunE: runSave,
}

func runSave(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	outPath := saveOutput
	if outPath == "" {
		ts := time.Now().Format("20060102-150405")
		outPath = fmt.Sprintf("engram-save-%s.tgz", ts)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("save: create output file: %w", err)
	}

	version := engramVersion()

	result, saveErr := engram.Save(ctx, f, engram.SaveOptions{
		EngramVersion: version,
	})

	// Always close the file; if save failed, remove the partial output.
	closeErr := f.Close()
	if saveErr != nil {
		if err := os.Remove(outPath); err != nil {
			log.Printf("engram: remove partial save %s: %v", outPath, err)
		}
		return saveErr
	}
	if closeErr != nil {
		if err := os.Remove(outPath); err != nil {
			log.Printf("engram: remove partial save %s: %v", outPath, err)
		}
		return fmt.Errorf("save: write output: %w", closeErr)
	}

	fmt.Fprintf(os.Stderr, "wrote %s (%d project(s)", outPath, result.ProjectCount)
	if result.PrunedCount > 0 {
		fmt.Fprintf(os.Stderr, ", %d pruned", result.PrunedCount)
	}
	fmt.Fprintln(os.Stderr, ")")

	for _, s := range result.Skipped {
		fmt.Fprintf(os.Stderr, "warning: skipped (not in archive): %s\n", s)
	}
	return nil
}

func init() {
	saveCmd.Flags().StringVarP(&saveOutput, "output", "o", "", "output path (default: engram-save-<timestamp>.tgz in current directory)")
	rootCmd.AddCommand(saveCmd)
}
