package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:          "engram",
	Short:        "Per-project tool-use memory for Claude Code",
	SilenceUsage: true,
}

// record

var recordCmd = &cobra.Command{
	Use:   "record",
	Short: "Record a tool-use event from stdin JSON",
	RunE:  runRecord,
}

func runRecord(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	input, err := engram.ParseHookInput(os.Stdin)
	if err != nil {
		return nil // malformed input is not our problem
	}
	root, err := engram.FindProjectRoot(input.CWD)
	if err != nil {
		return nil
	}

	var filePath string
	switch {
	case input.ToolName.Recordable():
		absPath, err := filepath.Abs(input.ToolInput.FilePath)
		if err != nil {
			return nil
		}
		rel, err := engram.RelPath(root, absPath)
		if err != nil {
			return nil
		}
		filePath = rel

	case input.ToolName == engram.ToolBash:
		if !engram.BashRecordable(input.ToolInput.Command) {
			return nil
		}
		if !engram.BashSucceeded(input.Response) {
			return nil
		}
		filePath = engram.NormalizeBashCommand(input.ToolInput.Command)

	default:
		return nil
	}

	db, err := engram.Open(ctx, engram.DBPath(root))
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram record: %v\n", err)
		return nil
	}
	defer db.Close()

	return engram.Record(ctx, db, engram.Event{
		SessionID: input.SessionID,
		Tool:      input.ToolName,
		FilePath:  filePath,
		Snippet:   engram.MakeSnippet(input.ToolName, input.Response),
	})
}

// inject

var (
	injectSessions int
	injectKeep     int
)

var injectCmd = &cobra.Command{
	Use:   "inject",
	Short: "Output session-start context JSON",
	RunE:  runInject,
}

func runInject(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	cwd, _ := os.Getwd()
	if input, err := engram.ParseHookInput(os.Stdin); err == nil && input.CWD != "" {
		cwd = input.CWD
	}

	root, err := engram.FindProjectRoot(cwd)
	if err != nil {
		fmt.Println("{}")
		return nil
	}

	dbPath := engram.DBPath(root)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("{}")
		return nil
	}

	db, err := engram.Open(ctx, dbPath)
	if err != nil {
		fmt.Println("{}")
		return nil
	}
	defer db.Close()

	projectResult, err := engram.Inject(ctx, db, injectSessions)
	if err != nil {
		fmt.Println("{}")
		return nil
	}

	// Prune old sessions while the DB is already open. Errors are non-fatal.
	if _, err := engram.Prune(ctx, db, injectKeep); err != nil {
		fmt.Fprintf(os.Stderr, "engram prune: %v\n", err)
	}

	// Read global memories (personality, preferences). Non-fatal if absent.
	var globalResult engram.InjectResult
	if globalPath, err := engram.GlobalDBPath(); err == nil {
		if _, err := os.Stat(globalPath); err == nil {
			if gdb, err := engram.Open(ctx, globalPath); err == nil {
				globalResult, _ = engram.Inject(ctx, gdb, injectSessions)
				gdb.Close()
			}
		}
	}

	fmt.Println(string(engram.FormatInjectOutput(globalResult, projectResult, injectSessions)))
	return nil
}

// prune

var pruneKeep int

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Delete events from old sessions, keeping the most recent N",
	RunE:  runPrune,
}

func runPrune(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := engram.FindProjectRoot(cwd)
	if err != nil {
		return err
	}

	db, err := engram.Open(ctx, engram.DBPath(root))
	if err != nil {
		return err
	}
	defer db.Close()

	n, err := engram.Prune(ctx, db, pruneKeep)
	if err != nil {
		return err
	}
	fmt.Printf("pruned %d events\n", n)
	return nil
}

func init() {
	injectCmd.Flags().IntVar(&injectSessions, "sessions", engram.DefaultInjectSessions, "number of recent sessions to include")
	injectCmd.Flags().IntVar(&injectKeep, "keep", engram.DefaultPruneSessions, "number of sessions to keep")
	pruneCmd.Flags().IntVar(&pruneKeep, "keep", engram.DefaultPruneSessions, "number of sessions to keep")
	rootCmd.AddCommand(recordCmd, injectCmd, pruneCmd, memCmd)
}
