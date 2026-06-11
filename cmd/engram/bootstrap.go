package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Set up engram for a specific AI agent",
	Long: `Bootstrap configures engram for a given AI agent.

Subcommands:
  claude       -- set up Claude Code hooks, CLAUDE.md, and global DB invariants
  codex        -- set up Codex CLI hooks (record/inject) plus AGENTS.md
  gemini       -- set up Gemini CLI hooks (record/inject) plus GEMINI.md
  antigravity  -- write a Knowledge Item that instructs AntiGravity to call engram at session start
  copilot      -- write .github/copilot-instructions.md in the current project
  cursor       -- write .cursorrules in the current project
  initfile     -- append the engram protocol to any init file (generic escape hatch)`,
}

// bootstrapGlobalDB writes the shared global DB invariants used by all agents.
// Returns (wrote, skipped, error).
func bootstrapGlobalDB(ctx context.Context) (int, int, error) {
	db, err := engram.OpenGlobalDB(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()

	wrote, skipped := 0, 0

	personality, err := engram.ReadMemory(ctx, db, engram.TierInvariant, "personality")
	if err != nil {
		return wrote, skipped, err
	}
	if personality == nil {
		setupKey := "setup-personality"
		existing, err := engram.ReadMemory(ctx, db, engram.TierShort, setupKey)
		if err != nil {
			return wrote, skipped, err
		}
		if existing != nil {
			fmt.Printf("skip (exists): short/%s\n", setupKey)
			skipped++
		} else {
			if err := engram.WriteMemory(ctx, db, engram.Memory{
				Tier:    engram.TierShort,
				Key:     setupKey,
				Content: "Set up personality and preferences. FIRST run: engram mem --global --tier invariant list -- if personality and codename are already configured from another project, skip to preferences or just delete this entry. Otherwise: work with the user to choose a codename and define a personality, store both as global invariants, add code preferences as global preferences. Delete this entry when done.",
			}); err != nil {
				return wrote, skipped, err
			}
			fmt.Printf("wrote: short/%s\n", setupKey)
			wrote++
		}
	}

	migrateKey := "migrate-existing-memory"
	existing, err := engram.ReadMemory(ctx, db, engram.TierShort, migrateKey)
	if err != nil {
		return wrote, skipped, err
	}
	if existing != nil {
		fmt.Printf("skip (exists): short/%s\n", migrateKey)
		skipped++
	} else {
		if err := engram.WriteMemory(ctx, db, engram.Memory{
			Tier:    engram.TierShort,
			Key:     migrateKey,
			Content: "Migrate existing memory into engram. First check whether global memories are already configured: engram mem --global --tier invariant list. Then follow the appropriate path:\n\nIf global memories are NOT yet set up: also migrate any global context you have been maintaining (personality, preferences, coding rules from CLAUDE.md or similar files) into the global engram DB as invariants and preferences. Ask the user before writing anything global.\n\nIf global memories ARE already set up: leave them alone entirely.\n\nIn both cases: look for project-specific memory or context for THIS project -- markdown files, notes, project-level context files -- and migrate relevant content into the project engram tiers (not global): settled decisions to long-term, in-flight work to short-term. Delete or archive source files once migrated. If nothing is found, delete this entry.",
		}); err != nil {
			return wrote, skipped, err
		}
		fmt.Printf("wrote: short/%s\n", migrateKey)
		wrote++
	}

	return wrote, skipped, nil
}

// bootstrap claude

var bootstrapClaudeGlobal bool

var bootstrapClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Set up Claude Code hooks and CLAUDE.md",
	Long: `Bootstrap Claude Code by patching ~/.claude/CLAUDE.md
and adding engram hooks to settings.json.

By default hooks are written to the project's .claude/settings.json.
Use -g to write hooks to ~/.claude/settings.json instead (for personal machines).

Existing keys and entries are never overwritten -- safe to re-run.`,
	RunE: runBootstrapClaude,
}

func runBootstrapClaude(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	wrote, skipped, err := bootstrapGlobalDB(ctx)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := bootstrapEngramMd(); err != nil {
		return err
	}
	if err := bootstrapStandingMd(ctx); err != nil {
		return err
	}
	if err := bootstrapClaudeMd(); err != nil {
		return err
	}
	if err := bootstrapStatusLine(exe); err != nil {
		return err
	}
	if err := bootstrapHooks(exe, bootstrapClaudeGlobal); err != nil {
		return err
	}

	printBootstrapSummary(wrote, skipped)
	return nil
}

func bootstrapEngramMd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "engram.md")
	if err := os.WriteFile(path, []byte(agentInfoText), 0644); err != nil {
		return err
	}
	fmt.Printf("wrote: %s\n", path)
	return nil
}

// bootstrapStandingMd renders the initial standing-memory files (invariants and
// preferences) the CLAUDE.md imports. It must run after bootstrapEngramMd, since
// SyncStandingMemory treats the presence of engram.md as the signal that this
// platform is bootstrapped. On a fresh install with empty tiers it writes
// placeholders; render-on-write fills them in later.
func bootstrapStandingMd(ctx context.Context) error {
	gdb, err := engram.OpenGlobalDB(ctx)
	if err != nil {
		return err
	}
	defer gdb.Close()
	if err := engram.SyncStandingMemory(ctx, gdb); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	for _, base := range engram.StandingFileBases() {
		fmt.Printf("wrote: %s\n", filepath.Join(home, ".claude", base))
	}
	return nil
}

func bootstrapClaudeMd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)

	if strings.Contains(content, "<!-- engram:start -->") {
		fmt.Printf("skip (has old marker-style engram section): %s\n", path)
		fmt.Println("  Run 'engram uninstall' first to remove it, then re-run bootstrap.")
		return nil
	}

	// Import the static instructions (@engram.md) and the dynamic standing-memory
	// files (@engram-invariants.md, @engram-preferences.md). Each is added
	// independently so an existing install that predates a given import gains it
	// on re-bootstrap.
	includes := []string{"@engram.md"}
	for _, base := range engram.StandingFileBases() {
		includes = append(includes, "@"+base)
	}
	var toAdd []string
	for _, inc := range includes {
		if strings.Contains(content, inc) {
			fmt.Printf("skip (already present): %s in %s\n", inc, path)
			continue
		}
		toAdd = append(toAdd, inc)
	}
	if len(toAdd) == 0 {
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, inc := range toAdd {
		if _, err := f.WriteString("\n" + inc + "\n"); err != nil {
			return err
		}
		fmt.Printf("wrote: %s in %s\n", inc, path)
	}
	return nil
}

func bootstrapStatusLine(exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "settings.json")

	settings, err := readSettingsJSON(path)
	if err != nil {
		return err
	}

	if _, exists := settings["statusLine"]; exists {
		fmt.Printf("skip (exists): statusLine in %s\n", path)
		return nil
	}

	settings["statusLine"] = map[string]any{
		"type":            "command",
		"command":         exe + " status",
		"refreshInterval": 30,
	}

	if err := writeSettingsJSON(path, settings); err != nil {
		return err
	}
	fmt.Printf("wrote: statusLine in %s\n", path)
	return nil
}

func bootstrapHooks(exe string, global bool) error {
	var path string
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, ".claude", "settings.json")
	} else {
		root, err := engram.FindProjectRoot(effectiveCWD())
		if err != nil {
			fmt.Println("skip (no project root found): hooks")
			return nil
		}
		path = filepath.Join(root, ".claude", "settings.json")
	}

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "engram record") {
		fmt.Printf("skip (hooks already present): %s\n", path)
	} else {
		if err := addEngramHooks(path, exe); err != nil {
			return err
		}
		fmt.Printf("wrote: engram hooks in %s\n", path)
	}

	// Ensure the engram allowlist independently of the hooks check above, so
	// re-running bootstrap repairs older installs that predate it.
	if err := ensureEngramAllowlist(path); err != nil {
		return err
	}
	return nil
}

// engramAllowlist is the set of engram command families an agent invokes
// directly. bootstrap pre-approves exactly these so the memory and tool
// workflows never trip a per-call permission prompt. Hooks (record/inject/
// status) run via the harness and need no allowlisting; uninstall/bootstrap are
// deliberately NOT auto-granted.
var engramAllowlist = []string{
	"Bash(engram mem:*)",
	"Bash(engram tool:*)",
}

// ensureEngramAllowlist adds the engramAllowlist patterns to
// settings.permissions.allow if absent. Idempotent: a no-op when all present.
func ensureEngramAllowlist(path string) error {
	settings, err := readSettingsJSON(path)
	if err != nil {
		return err
	}
	changed := false
	for _, pattern := range engramAllowlist {
		if addAllowedTool(settings, pattern) {
			changed = true
		}
	}
	if changed {
		if err := writeSettingsJSON(path, settings); err != nil {
			return err
		}
		fmt.Printf("wrote: engram allowlist in %s\n", path)
	} else {
		fmt.Printf("skip (allowlist already present): %s\n", path)
	}
	return nil
}

func addEngramHooks(path string, exe string) error {
	settings, err := readSettingsJSON(path)
	if err != nil {
		return err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	hooks["PostToolUse"] = append(
		asSlice(hooks["PostToolUse"]),
		map[string]any{
			"matcher": "Read|Edit|Write|Bash",
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": exe + " record",
			}},
		},
	)
	hooks["SessionStart"] = append(
		asSlice(hooks["SessionStart"]),
		map[string]any{
			// Explicit matcher (vs. omitting it) so the hook unambiguously fires on
			// "compact" -- re-injecting memory/personality after compaction, the
			// case most prone to silently losing context. The other sources are
			// listed to preserve matcher-less behavior (fire on every source).
			"matcher": "startup|resume|clear|compact",
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": exe + " inject",
			}},
		},
	)
	settings["hooks"] = hooks

	return writeSettingsJSON(path, settings)
}

// addAllowedTool appends a permission pattern to settings.permissions.allow if it
// is not already present, reporting whether it made a change.
func addAllowedTool(settings map[string]any, pattern string) bool {
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow := asSlice(perms["allow"])
	for _, a := range allow {
		if s, ok := a.(string); ok && s == pattern {
			return false
		}
	}
	perms["allow"] = append(allow, pattern)
	settings["permissions"] = perms
	return true
}

// bootstrap antigravity

var bootstrapAntigravityCmd = &cobra.Command{
	Use:   "antigravity",
	Short: "Write an AntiGravity Knowledge Item that calls engram at session start",
	Long: `Bootstrap AntiGravity by writing a Knowledge Item to
~/.gemini/antigravity/knowledge/engram_protocol/.

The KI instructs AntiGravity to run "engram inject --text" at the start of
every session and treat the output as session context.

Safe to re-run: skips if the KI already exists.`,
	RunE: runBootstrapAntigravity,
}

const antigravityInstructions = `# Engram Session Protocol

At the start of every new conversation, before taking any other actions, run:

  engram inject --text

Read the output and treat it as your session context. It contains identity,
preferences, and project-specific memory loaded from the engram database.
Do not skip this step.`

const antigravitySummary = `REQUIRED: At the start of every new conversation, before taking any other action, run "engram inject --text" and treat the output as your session context (identity, preferences, project memory).`

func runBootstrapAntigravity(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	wrote, skipped, err := bootstrapGlobalDB(ctx)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	kiDir := filepath.Join(home, ".gemini", "antigravity", "knowledge", "engram_protocol")
	artifactsDir := filepath.Join(kiDir, "artifacts")

	metaPath := filepath.Join(kiDir, "metadata.json")
	if _, err := os.Stat(metaPath); err == nil {
		fmt.Printf("skip (exists): %s\n", kiDir)
		skipped++
	} else {
		if err := os.MkdirAll(artifactsDir, 0755); err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339)

		meta, _ := json.MarshalIndent(map[string]any{
			"title":     "Engram Session Protocol",
			"summary":   antigravitySummary,
			"timestamp": now,
		}, "", "  ")
		if err := os.WriteFile(metaPath, append(meta, '\n'), 0644); err != nil {
			return err
		}

		ts, _ := json.MarshalIndent(map[string]any{
			"created":  now,
			"modified": now,
			"accessed": now,
		}, "", "  ")
		if err := os.WriteFile(filepath.Join(kiDir, "timestamps.json"), append(ts, '\n'), 0644); err != nil {
			return err
		}

		if err := os.WriteFile(filepath.Join(artifactsDir, "instructions.md"), []byte(antigravityInstructions+"\n"), 0644); err != nil {
			return err
		}
		fmt.Printf("wrote: %s\n", kiDir)
		wrote++
	}

	printBootstrapSummary(wrote, skipped)
	return nil
}

// bootstrap gemini

var bootstrapGeminiCmd = &cobra.Command{
	Use:   "gemini",
	Short: "Set up Gemini CLI hooks and GEMINI.md",
	Long: `Bootstrap Gemini CLI for engram. Two pieces are installed in ~/.gemini:

  - settings.json: a SessionStart hook that runs "engram inject" (loading memory
    into the session) and an AfterTool hook on read_file/write_file/replace that
    runs "engram record" (logging touched files). Gemini's hook protocol matches
    Claude Code's, so record/inject work unchanged.
  - GEMINI.md: the human-readable engram session protocol, as a fallback.

Safe to re-run: skips pieces that are already present.`,
	RunE: runBootstrapGemini,
}

// engramProtocolSection is the session-protocol block appended to every markdown
// init file (Gemini, Copilot, Cursor, AGENTS.md/Codex, custom initfiles). It is
// the single source of truth for that block; uninstall's engramSectionRE removes
// exactly this (a test asserts the two stay in sync). AntiGravity uses a distinct
// single-# variant -- see antigravityInstructions.
const engramProtocolSection = `
## Engram Session Protocol

At the start of a new conversation, first check whether engram context is already
present in this session. Look for sections such as "## Orientation",
"## Identity", "## Preferences", or "## Long-term memory".

If that context is already present, do not run another inject command.

If it is absent, before taking any other action, run:

  engram inject --text

Treat the output as your session context (identity, preferences, project memory).
Do not skip this step.`

func runBootstrapGemini(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	wrote, skipped, err := bootstrapGlobalDB(ctx)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	ok, err := bootstrapAppendToFile(filepath.Join(home, ".gemini", "GEMINI.md"), engramProtocolSection)
	if err != nil {
		return err
	}
	countWroteSkipped(ok, &wrote, &skipped)

	if err := bootstrapGeminiHooks(filepath.Join(home, ".gemini", "settings.json"), exe); err != nil {
		return err
	}

	printBootstrapSummary(wrote, skipped)
	return nil
}

// bootstrap copilot

var bootstrapCopilotCmd = &cobra.Command{
	Use:   "copilot",
	Short: "Write .github/copilot-instructions.md to call engram at session start",
	Long: `Bootstrap GitHub Copilot by appending the engram session protocol to
.github/copilot-instructions.md in the current project.

Safe to re-run: skips if the engram section is already present.`,
	RunE: runBootstrapCopilot,
}

func runBootstrapCopilot(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	wrote, skipped, err := bootstrapGlobalDB(ctx)
	if err != nil {
		return err
	}

	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		return fmt.Errorf("copilot bootstrap requires a project root: %w", err)
	}
	path := filepath.Join(root, ".github", "copilot-instructions.md")

	ok, err := bootstrapAppendToFile(path, engramProtocolSection)
	if err != nil {
		return err
	}
	countWroteSkipped(ok, &wrote, &skipped)

	printBootstrapSummary(wrote, skipped)
	return nil
}

// bootstrap cursor

var bootstrapCursorCmd = &cobra.Command{
	Use:   "cursor",
	Short: "Write .cursorrules to call engram at session start",
	Long: `Bootstrap Cursor by appending the engram session protocol to
.cursorrules in the current project.

Safe to re-run: skips if the engram section is already present.`,
	RunE: runBootstrapCursor,
}

func runBootstrapCursor(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	wrote, skipped, err := bootstrapGlobalDB(ctx)
	if err != nil {
		return err
	}

	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		return fmt.Errorf("cursor bootstrap requires a project root: %w", err)
	}
	path := filepath.Join(root, ".cursorrules")

	ok, err := bootstrapAppendToFile(path, engramProtocolSection)
	if err != nil {
		return err
	}
	countWroteSkipped(ok, &wrote, &skipped)

	printBootstrapSummary(wrote, skipped)
	return nil
}

// countWroteSkipped bumps wrote or skipped based on whether an append happened.
func countWroteSkipped(appended bool, wrote, skipped *int) {
	if appended {
		*wrote++
	} else {
		*skipped++
	}
}

// printBootstrapSummary prints the shared "N written, M skipped" footer plus the
// hint about updating existing global entries.
func printBootstrapSummary(wrote, skipped int) {
	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
}

func bootstrapAppendToFile(path, section string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if engramSectionRE.Match(data) {
		updated := engramSectionRE.ReplaceAll(data, []byte(section+"\n"))
		if string(updated) == string(data) {
			fmt.Printf("skip (already present): engram section in %s\n", path)
			return false, nil
		}
		if err := os.WriteFile(path, updated, 0644); err != nil {
			return false, err
		}
		fmt.Printf("updated: engram section in %s\n", path)
		return true, nil
	}
	if strings.Contains(string(data), "engram inject --text") {
		fmt.Printf("skip (already present): engram section in %s\n", path)
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	_, werr := f.WriteString(section + "\n")
	f.Close()
	if werr != nil {
		return false, werr
	}
	fmt.Printf("wrote: engram section in %s\n", path)
	return true, nil
}

// bootstrap initfile

var bootstrapInitFileCmd = &cobra.Command{
	Use:   "initfile <path>",
	Short: "Append the engram session protocol to any agent init file",
	Long: `Append the engram session protocol to the specified file.

Use this for any AI agent that reads a markdown init file at session start,
such as AGENTS.md (Codex), .windsurfrules, or any custom file.

Safe to re-run: skips if the engram section is already present.`,
	Args: cobra.ExactArgs(1),
	RunE: runBootstrapInitFile,
}

func runBootstrapInitFile(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	wrote, skipped, err := bootstrapGlobalDB(ctx)
	if err != nil {
		return err
	}

	ok, err := bootstrapAppendToFile(args[0], engramProtocolSection)
	if err != nil {
		return err
	}
	if ok {
		wrote++
	} else {
		skipped++
	}

	printBootstrapSummary(wrote, skipped)
	return nil
}

// bootstrap codex

var bootstrapCodexGlobal bool
var bootstrapCodexNoSessionHook bool

var bootstrapCodexCmd = &cobra.Command{
	Use:   "codex",
	Short: "Set up Codex CLI hooks and AGENTS.md",
	Long: `Bootstrap OpenAI Codex CLI for engram. Two pieces are installed:

  - hooks.json: a SessionStart hook that runs "engram inject" (loading memory
    into the session) and a PostToolUse hook on apply_patch that runs
    "engram record" (logging touched files). Codex's hook protocol matches
    Claude Code's, so record/inject work unchanged.
  - AGENTS.md: the human-readable engram session protocol, as a fallback.

By default both go in the project (.codex/hooks.json and ./AGENTS.md). Use -g to
write to ~/.codex instead (global, applies to all projects). Codex only honors
project-local .codex/ config in trusted projects, so prefer -g on machines where
you have not trusted the project.

Use --no-session-hook to skip the SessionStart inject hook and rely on AGENTS.md
for startup context while keeping apply_patch file tracking. Re-running with
--no-session-hook removes an existing engram SessionStart hook.

Safe to re-run: skips pieces that are already present.`,
	RunE: runBootstrapCodex,
}

func runBootstrapCodex(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	wrote, skipped, err := bootstrapGlobalDB(ctx)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	// Resolve where AGENTS.md and the Codex hooks.json live. AGENTS.md sits at
	// the project root (or ~/.codex with -g); hooks always live under a .codex
	// dir (the project's, or the home one).
	var agentsPath, hooksPath string
	if bootstrapCodexGlobal {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		agentsPath = filepath.Join(home, ".codex", "AGENTS.md")
		hooksPath = filepath.Join(home, ".codex", "hooks.json")
	} else {
		root, err := engram.FindProjectRoot(effectiveCWD())
		if err != nil {
			return fmt.Errorf("codex bootstrap requires a project root (or use -g for global): %w", err)
		}
		agentsPath = filepath.Join(root, "AGENTS.md")
		hooksPath = filepath.Join(root, ".codex", "hooks.json")
	}

	ok, err := bootstrapAppendToFile(agentsPath, engramProtocolSection)
	if err != nil {
		return err
	}
	countWroteSkipped(ok, &wrote, &skipped)

	if err := bootstrapCodexHooks(hooksPath, exe, !bootstrapCodexNoSessionHook); err != nil {
		return err
	}

	printBootstrapSummary(wrote, skipped)
	return nil
}

// hookSpec describes one engram hook to install: the settings event key, an
// optional tool/lifecycle matcher (empty means fire on every occurrence of the
// event), and the engram subcommand the handler runs.
type hookSpec struct {
	event      string
	matcher    string
	subcommand string
}

// installEngramHooks merges the given engram hooks into a hook-config JSON file
// (a Codex/Gemini hooks.json or settings.json) without disturbing existing
// hooks. It shares Claude Code's hook JSON shape, so the same record/inject
// commands work; only event names and matchers vary per agent. Idempotent: a
// spec whose command is already registered under its event is skipped, so
// partial installs repair cleanly on re-run.
func installEngramHooks(path, exe string, specs []hookSpec) error {
	settings, err := readSettingsJSON(path)
	if err != nil {
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	changed := false
	for _, s := range specs {
		cmd := exe + " " + s.subcommand
		marker := "engram " + s.subcommand
		if dedupeEngramHooks(hooks, s.event, marker, path) {
			changed = true
		}
		if engramHookPresent(hooks, s.event, marker) {
			fmt.Printf("skip (present): %s %s hook in %s\n", s.event, s.subcommand, path)
			continue
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": cmd,
			}},
		}
		if s.matcher != "" {
			entry["matcher"] = s.matcher
		}
		hooks[s.event] = append(asSlice(hooks[s.event]), entry)
		fmt.Printf("wrote: %s %s hook in %s\n", s.event, s.subcommand, path)
		changed = true
	}
	if !changed {
		return nil
	}

	settings["hooks"] = hooks
	return writeSettingsJSON(path, settings)
}

// engramHookPresent reports whether any handler under the given event already
// runs the engram subcommand named by marker (for example, "engram record").
func engramHookPresent(hooks map[string]any, event, marker string) bool {
	for _, group := range asSlice(hooks[event]) {
		gm, ok := group.(map[string]any)
		if !ok {
			continue
		}
		for _, h := range asSlice(gm["hooks"]) {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if c, _ := hm["command"].(string); strings.Contains(c, marker) {
				return true
			}
		}
	}
	return false
}

// dedupeEngramHooks keeps the first hook for marker and removes later duplicate
// handlers. It matters when bootstrap is run from a development binary: the
// executable path may be a Go build-cache path, but the semantic hook is still
// the same "engram record" or "engram inject" action.
func dedupeEngramHooks(hooks map[string]any, event, marker, path string) bool {
	arr, ok := hooks[event].([]any)
	if !ok {
		return false
	}
	kept := false
	changed := false
	filtered := make([]any, 0, len(arr))
	for _, group := range arr {
		gm, ok := group.(map[string]any)
		if !ok {
			filtered = append(filtered, group)
			continue
		}
		hookList, _ := gm["hooks"].([]any)
		keptHooks := make([]any, 0, len(hookList))
		for _, h := range hookList {
			hm, ok := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if ok && strings.Contains(cmd, marker) {
				if kept {
					changed = true
					continue
				}
				kept = true
			}
			keptHooks = append(keptHooks, h)
		}
		if len(keptHooks) == 0 {
			changed = true
			continue
		}
		gm["hooks"] = keptHooks
		filtered = append(filtered, gm)
	}
	if changed {
		hooks[event] = filtered
		fmt.Printf("removed duplicate: %s hook from %s\n", marker, path)
	}
	return changed
}

// bootstrapCodexHooks installs engram's Codex hooks. The record hook always
// tracks apply_patch file edits; the SessionStart inject hook is optional
// because Codex currently surfaces hook additionalContext visibly. When the
// session hook is omitted, any existing engram SessionStart hook is removed so
// re-running bootstrap repairs earlier installs.
func bootstrapCodexHooks(path, exe string, includeSessionHook bool) error {
	specs := []hookSpec{
		{event: "PostToolUse", matcher: "^apply_patch$", subcommand: "record"},
	}
	if includeSessionHook {
		specs = append([]hookSpec{
			{event: "SessionStart", matcher: "startup|resume|clear|compact", subcommand: "inject"},
		}, specs...)
	}
	if err := installEngramHooks(path, exe, specs); err != nil {
		return err
	}
	if includeSessionHook {
		return nil
	}

	settings, err := readSettingsJSON(path)
	if err != nil {
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	if !removeEngramHook(hooks, "SessionStart", "engram inject", "engram inject hook", path) {
		return nil
	}
	settings["hooks"] = hooks
	return writeSettingsJSON(path, settings)
}

// bootstrapGeminiHooks installs engram's SessionStart (inject) and AfterTool
// (record) hooks into a Gemini settings.json. Gemini names the post-tool event
// AfterTool and its file tools read_file/write_file/replace; its SessionStart
// matcher is an exact lifecycle string, so we omit it to fire on every source.
func bootstrapGeminiHooks(path, exe string) error {
	return installEngramHooks(path, exe, []hookSpec{
		{event: "SessionStart", subcommand: "inject"},
		{event: "AfterTool", matcher: "read_file|write_file|replace", subcommand: "record"},
	})
}

func readSettingsJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func writeSettingsJSON(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0644)
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func init() {
	bootstrapClaudeCmd.Flags().BoolVarP(&bootstrapClaudeGlobal, "global", "g", false, "write hooks to ~/.claude/settings.json instead of the project's .claude/settings.json")
	bootstrapCodexCmd.Flags().BoolVarP(&bootstrapCodexGlobal, "global", "g", false, "write to ~/.codex/AGENTS.md instead of the project's AGENTS.md")
	bootstrapCodexCmd.Flags().BoolVar(&bootstrapCodexNoSessionHook, "no-session-hook", false, "do not install Codex SessionStart inject hook; rely on AGENTS.md fallback")
	bootstrapCmd.AddCommand(bootstrapClaudeCmd, bootstrapAntigravityCmd, bootstrapGeminiCmd, bootstrapCopilotCmd, bootstrapCursorCmd, bootstrapCodexCmd, bootstrapInitFileCmd)
	rootCmd.AddCommand(bootstrapCmd)
}
