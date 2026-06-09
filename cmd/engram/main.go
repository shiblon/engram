package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

func main() {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		rootCmd.Version = info.Main.Version
	}
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCWD string

var rootCmd = &cobra.Command{
	Use:   "engram",
	Short: "Per-session memory and personality for AI agents",
	Long: `Per-session memory and personality for AI agents -- works with Claude Code,
Cursor, GitHub Copilot, Codex, and any agent with a markdown init file.

Get started:  engram bootstrap <platform>
Or ask your agent to run: engram agentinfo`,
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

// loadContextFile syncs contextFile into db's long-term memories if the file
// is newer than the DB. Returns the number of memories loaded, or 0 if the
// file is absent or already up to date.
func loadContextFile(ctx context.Context, db *sql.DB, contextFile string) int {
	fi, err := os.Stat(contextFile)
	if err != nil {
		return 0
	}
	existing, _ := engram.ListMemories(ctx, db, engram.TierLong)
	if len(existing) > 0 && !fi.ModTime().After(time.UnixMilli(existing[0].TS)) {
		return 0
	}
	data, err := os.ReadFile(contextFile)
	if err != nil {
		return 0
	}
	memories, err := engram.ParseMemoryMD(engram.TierLong, string(data))
	if err != nil {
		return 0
	}
	// Count only memories that actually persisted; a swallowed write here would
	// otherwise report context as loaded when it was lost (e.g. disk/corruption).
	loaded := 0
	for _, m := range memories {
		if err := engram.WriteMemory(ctx, db, m); err != nil {
			fmt.Fprintf(os.Stderr, "engram: load context/long.md %q: %v\n", m.Key, err)
			continue
		}
		loaded++
	}
	return loaded
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
			// Surface pending restores; mark any that match the current repo.
			if pending, err := engram.ListPendingRestores(ctx, gdb); err == nil && len(pending) > 0 {
				currentIdentity := engram.ProjectIdentity(cwd)
				for i := range pending {
					pending[i].MatchesCurrent = pending[i].Identity == currentIdentity
				}
				globalResult.PendingRestores = pending
			}
			// Register the current project if it isn't already in the manifest.
			// One-time write per project; becomes a no-op once registered.
			if root, err := engram.FindProjectRoot(cwd); err == nil {
				if !engram.IsProjectRegistered(ctx, gdb, root) {
					if err := engram.RegisterProject(ctx, gdb, root); err != nil {
						log.Printf("engram: inject register %s: %v", root, err)
					}
				}
			}
			gdb.Close()
		}
	}
	// Global agent tools live on the filesystem, independent of the global DB.
	if gdir, err := engram.GlobalAgentToolsDir(); err == nil {
		var warnings []string
		globalResult.AgentTools, warnings = engram.ScanAgentTools(gdir)
		reportToolWarnings(warnings)
	}

	// Read project memories. Non-fatal if no project root or DB exists.
	var projectResult engram.InjectResult
	var bootstrapped int
	if root, err := engram.FindProjectRoot(cwd); err == nil {
		contextFile := filepath.Join(root, "context", "long.md")
		_, contextErr := os.Stat(contextFile)
		if engram.ProjectDBExists(root) || contextErr == nil {
			if db, err := engram.OpenProjectDB(ctx, root); err == nil {
				bootstrapped = loadContextFile(ctx, db, contextFile)
				projectResult, _ = engram.Inject(ctx, db, injectSessions)
				if _, err := engram.Prune(ctx, db, injectKeep); err != nil {
					fmt.Fprintf(os.Stderr, "engram prune: %v\n", err)
				}
				db.Close()
			}
		}
		// Agent tools and staged candidates are independent of the project DB.
		projectResult.AgentTools = scanProjectTools(root)
		// Surface staged candidates annotated with age. Candidates persist (no
		// auto-eviction); the agent judges maturity from age, a portable signal
		// across every platform, unlike a Claude-Code-only session-start source.
		if cands, err := engram.ListToolCandidates(root); err != nil {
			fmt.Fprintf(os.Stderr, "engram agenttools: %v\n", err)
		} else {
			now := time.Now()
			for _, c := range cands {
				projectResult.ToolCandidates = append(projectResult.ToolCandidates, engram.FormatToolCandidate(c, now))
			}
		}
	}

	contextText := engram.InjectContextText(globalResult, projectResult, injectSessions)
	if bootstrapped > 0 {
		contextText = fmt.Sprintf("(loaded %d long-term memories from context/long.md)\n\n", bootstrapped) + contextText
	}

	if injectText {
		if contextText != "" {
			fmt.Println(contextText)
		}
		return nil
	}
	fmt.Println(string(engram.FormatInjectOutputText(contextText)))
	return nil
}

// scanProjectTools scans the project's committed agenttools dir and rewrites each
// tool's path to be relative to root, so the injected command reads as
// "bash context/agenttools/foo.sh" (what the agent types from the repo root)
// rather than an absolute path. Global tools keep their absolute paths.
func scanProjectTools(root string) []engram.ToolDesc {
	tools, warnings := engram.ScanAgentTools(engram.ProjectAgentToolsDir(root))
	reportToolWarnings(warnings)
	for i := range tools {
		if rel, err := filepath.Rel(root, tools[i].Path); err == nil {
			tools[i].Path = rel
		}
	}
	return tools
}

// reportToolWarnings surfaces misconfigured-tool warnings on stderr so they do
// not pollute the injected context but remain visible to the user.
func reportToolWarnings(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "engram agenttools: %s\n", w)
	}
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
