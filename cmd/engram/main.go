package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

func main() {
	if v := engramVersion(); v != "(devel)" {
		rootCmd.Version = v
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

	// Collect the file touches this tool use produced. Claude Code's
	// Read/Edit/Write each report one path in tool_input.file_path; Codex's
	// apply_patch names one or more paths inside the patch body (and may arrive
	// as a shell heredoc), which PatchedFiles teases out. Anything else is not a
	// file touch worth recording.
	type fileEvent struct {
		tool engram.Tool
		path string
	}
	var events []fileEvent
	if input.ToolName.Recordable() {
		if rel, ok := recordableRel(root, input.CWD, input.FilePath()); ok {
			events = append(events, fileEvent{input.ToolName, rel})
		}
	} else {
		for _, p := range engram.PatchedFiles(input.ToolInput) {
			if rel, ok := recordableRel(root, input.CWD, p); ok {
				events = append(events, fileEvent{engram.ToolApplyPatch, rel})
			}
		}
	}
	if len(events) == 0 {
		return nil
	}

	db, err := engram.OpenProjectDB(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram record: %v\n", err)
		return nil
	}
	defer db.Close()

	for _, e := range events {
		if err := engram.Record(ctx, db, engram.Event{
			SessionID: input.SessionID,
			Tool:      e.tool,
			FilePath:  e.path,
		}); err != nil {
			return err
		}
	}
	return nil
}

// recordableRel resolves p -- absolute, or relative to cwd -- to a path relative
// to the project root, reporting false if p is empty, unresolvable, or falls
// outside the root.
func recordableRel(root, cwd, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, p)
	}
	rel, err := engram.RelPath(root, abs)
	if err != nil {
		return "", false
	}
	return rel, true
}

// inject

var (
	injectSessions int
	injectKeep     int
	injectText     bool
	injectAgent    string
)

var injectCmd = &cobra.Command{
	Use:   "inject",
	Short: "Output session-start context JSON",
	RunE:  runInject,
}

// injectVersionLine is the version-drift check inject leads with. It carries the
// instruction itself, not just the number, because the check must fire even when
// the loaded engram.md is old and version-less (it predates this feature) -- in
// that case the guidance file cannot carry the instruction, but inject always
// can. It covers both a version mismatch and a missing version line.
func injectVersionLine(version string) string {
	return fmt.Sprintf("engram version: %s. Check the engram guidance loaded in your "+
		"context: if its \"Guidance version\" line differs from this, or there is no "+
		"version line at all, that guidance predates this engram; tell the user and offer "+
		"to run `engram bootstrap` to refresh it.", version)
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
			globalResult, err = engram.InjectWithAgent(ctx, gdb, injectSessions, injectAgent)
			if err != nil {
				log.Printf("engram: inject global memory: %v", err)
			}
			// Surface pending restores; mark any that match the current repo.
			if pending, err := engram.ListPendingRestores(ctx, gdb); err == nil && len(pending) > 0 {
				currentIdentity := engram.ProjectIdentity(cwd)
				if root, err := engram.FindProjectRoot(cwd); err == nil {
					currentIdentity = engram.ProjectIdentity(root)
				}
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
	if root, err := engram.FindProjectRoot(cwd); err == nil {
		if engram.ProjectDBExists(root) {
			if db, err := engram.OpenProjectDB(ctx, root); err == nil {
				projectResult, err = engram.Inject(ctx, db, injectSessions)
				if err != nil {
					log.Printf("engram: inject project memory: %v", err)
				}
				if _, err := engram.Prune(ctx, db, injectKeep); err != nil {
					fmt.Fprintf(os.Stderr, "engram prune: %v\n", err)
				}
				if candidates, err := engram.DiscoverAutomation(root); err != nil {
					log.Printf("engram: discover project automation: %v", err)
				} else {
					review, err := engram.ReconcileAutomationCatalog(ctx, db, candidates, false)
					if err != nil {
						log.Printf("engram: reconcile automation catalog: %v", err)
					} else if len(review.Items) > 0 {
						projectResult.AutomationReview = &review
					}
					entries, err := engram.ListAutomationCatalog(ctx, db)
					if err != nil {
						log.Printf("engram: list automation catalog: %v", err)
					} else {
						entries = engram.ActiveAutomationCatalogEntries(entries, candidates)
						var warnings []string
						projectResult.ProjectTools, warnings = engram.ProjectToolsFromCatalog(root, entries)
						for _, warning := range warnings {
							fmt.Fprintf(os.Stderr, "engram project tools: %s\n", warning)
						}
						for _, entry := range entries {
							if entry.Classification == engram.AutomationSkillMember {
								projectResult.SkillCandidates = append(projectResult.SkillCandidates, entry)
							}
						}
					}
				}
				db.Close()
			}
		}
	}

	contextText := engram.InjectContextText(globalResult, projectResult, injectSessions)
	if contextText != "" {
		contextText = injectVersionLine(engramVersion()) + "\n\n" + contextText
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
	injectCmd.Flags().StringVar(&injectAgent, "agent", "", "agent layer to inject on top of primary global identity/preferences (empty injects no layer)")
	pruneCmd.Flags().IntVar(&pruneKeep, "keep", engram.DefaultPruneSessions, "number of sessions to keep")
	rootCmd.AddCommand(recordCmd, injectCmd, pruneCmd, memCmd)
}
