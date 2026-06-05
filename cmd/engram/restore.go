package main

import (
	"context"
	"fmt"
	"os"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var restoreApply string
var restoreDiscard string
var restoreStatus bool

var restoreCmd = &cobra.Command{
	Use:   "restore [file]",
	Short: "Restore engram state from a save archive",
	Long: `Restore applies a save archive produced by 'engram save', or manages
pending staged projects.

  engram restore <file>
      Apply an archive: restore global memory + agenttools (if the current
      machine has none), and stage every project snapshot for later placement.

  engram restore --status
      List all pending staged projects.

  engram restore --apply <identity>
      Place the staged snapshot matching <identity> into the current working
      tree. Runs from inside the project directory. If the target already has
      curated memories the snapshot is re-staged under a new slot name.

  engram restore --discard <identity>
      Drop a staged snapshot without applying it.`,
	RunE: runRestore,
}

func runRestore(_ *cobra.Command, args []string) error {
	ctx := context.Background()

	// --status: list pending entries.
	if restoreStatus {
		gdb, err := engram.OpenGlobalDB(ctx)
		if err != nil {
			return err
		}
		defer gdb.Close()
		pending, err := engram.ListPendingRestores(ctx, gdb)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			fmt.Fprintln(os.Stderr, "no pending restores")
			return nil
		}
		for _, p := range pending {
			fmt.Fprintf(os.Stderr, "  identity: %s\n  original: %s\n  stage:    %s\n\n",
				p.Identity, p.OriginalPath, p.StagePath)
		}
		return nil
	}

	// --discard: drop a staged entry.
	if restoreDiscard != "" {
		gdb, err := engram.OpenGlobalDB(ctx)
		if err != nil {
			return err
		}
		defer gdb.Close()
		if err := engram.DiscardRestore(ctx, gdb, restoreDiscard); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "discarded: %s\n", restoreDiscard)
		return nil
	}

	// --apply: place a staged project into the current working tree.
	if restoreApply != "" {
		root, err := engram.FindProjectRoot(effectiveCWD())
		if err != nil {
			return fmt.Errorf("restore --apply: %w (run from inside the project directory)", err)
		}
		gdb, err := engram.OpenGlobalDB(ctx)
		if err != nil {
			return err
		}
		defer gdb.Close()
		result, err := engram.ApplyRestore(ctx, gdb, restoreApply, root)
		if err != nil {
			return err
		}
		if result.Applied {
			fmt.Fprintf(os.Stderr, "applied: %s -> %s\n", restoreApply, root)
		} else if result.Conflicted {
			fmt.Fprintf(os.Stderr, "conflict: %s has curated content; snapshot re-staged as %s\n",
				root, result.NewStagePath)
		}
		return nil
	}

	// Default: restore from archive file.
	if len(args) == 0 {
		return fmt.Errorf("restore: provide an archive file, or use --apply / --discard / --status")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return fmt.Errorf("restore: open archive: %w", err)
	}
	defer f.Close()

	result, err := engram.Restore(ctx, f)
	if err != nil {
		return err
	}

	if result.GlobalApplied {
		fmt.Fprintln(os.Stderr, "global: restored")
	} else if result.GlobalSkipped {
		fmt.Fprintln(os.Stderr, "global: skipped (existing curated content)")
	}
	if result.StagedCount > 0 {
		fmt.Fprintf(os.Stderr, "staged: %d project(s) pending\n", result.StagedCount)
	} else {
		fmt.Fprintln(os.Stderr, "staged: no projects")
	}
	return nil
}

func init() {
	restoreCmd.Flags().StringVar(&restoreApply, "apply", "", "place the staged snapshot with this identity into the current project")
	restoreCmd.Flags().StringVar(&restoreDiscard, "discard", "", "drop the staged snapshot with this identity")
	restoreCmd.Flags().BoolVar(&restoreStatus, "status", false, "list all pending staged projects")
	rootCmd.AddCommand(restoreCmd)
}
