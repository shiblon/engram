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

var rootCWD string

var rootCmd = &cobra.Command{
	Use:          "engram",
	Short:        "Per-project tool-use memory for Claude Code",
	SilenceUsage: true,
}

// effectiveCWD returns the user-supplied --cwd, or the process working directory.
func effectiveCWD() string {
	if rootCWD != "" {
		return rootCWD
	}
	cwd, _ := os.Getwd()
	return cwd
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

	db, err := engram.OpenProjectDB(ctx, root)
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
	injectText     bool
)

var injectCmd = &cobra.Command{
	Use:   "inject",
	Short: "Output session-start context JSON",
	RunE:  runInject,
}

func runInject(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	cwd, _ := os.Getwd()
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		if input, err := engram.ParseHookInput(os.Stdin); err == nil && input.CWD != "" {
			cwd = input.CWD
		}
	}

	// Read global memories (personality, preferences). Non-fatal if absent.
	var globalResult engram.InjectResult
	if engram.GlobalDBExists() {
		if gdb, err := engram.OpenGlobalDB(ctx); err == nil {
			globalResult, _ = engram.Inject(ctx, gdb, injectSessions)
			gdb.Close()
		}
	}

	// Read project memories. Non-fatal if no project root or DB exists.
	var projectResult engram.InjectResult
	if root, err := engram.FindProjectRoot(cwd); err == nil && engram.ProjectDBExists(root) {
		if db, err := engram.OpenProjectDB(ctx, root); err == nil {
			projectResult, _ = engram.Inject(ctx, db, injectSessions)
			if _, err := engram.Prune(ctx, db, injectKeep); err != nil {
				fmt.Fprintf(os.Stderr, "engram prune: %v\n", err)
			}
			db.Close()
		}
	}

	if injectText {
		text := engram.InjectContextText(globalResult, projectResult, injectSessions)
		if text != "" {
			fmt.Println(text)
		}
		return nil
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

	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		return err
	}

	db, err := engram.OpenProjectDB(ctx, root)
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
	rootCmd.PersistentFlags().StringVarP(&rootCWD, "cwd", "d", "", "working directory for project root resolution (default: current directory)")
	injectCmd.Flags().IntVar(&injectSessions, "sessions", engram.DefaultInjectSessions, "number of recent sessions to include")
	injectCmd.Flags().IntVar(&injectKeep, "keep", engram.DefaultPruneSessions, "number of sessions to keep")
	injectCmd.Flags().BoolVar(&injectText, "text", false, "output plain text instead of session-start hook JSON")
	pruneCmd.Flags().IntVar(&pruneKeep, "keep", engram.DefaultPruneSessions, "number of sessions to keep")
	rootCmd.AddCommand(recordCmd, injectCmd, pruneCmd, memCmd)
}
