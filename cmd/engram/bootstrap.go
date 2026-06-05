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
  gemini       -- write ~/.gemini/GEMINI.md and SessionStart hook
  antigravity  -- write a Knowledge Item that instructs AntiGravity to call engram at session start
  copilot      -- write .github/copilot-instructions.md in the current project
  cursor       -- write .cursorrules in the current project
  codex        -- write AGENTS.md in the current project (or ~/.codex/AGENTS.md with -g)
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
	if err := bootstrapClaudeMd(); err != nil {
		return err
	}
	if err := bootstrapStatusLine(exe); err != nil {
		return err
	}
	if err := bootstrapHooks(exe, bootstrapClaudeGlobal); err != nil {
		return err
	}

	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
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

	if strings.Contains(content, "@engram.md") {
		fmt.Printf("skip (already present): @engram.md in %s\n", path)
		return nil
	}

	if strings.Contains(content, "<!-- engram:start -->") {
		fmt.Printf("skip (has old marker-style engram section): %s\n", path)
		fmt.Println("  Run 'engram uninstall' first to remove it, then re-run bootstrap.")
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString("\n@engram.md\n"); err != nil {
		return err
	}
	fmt.Printf("wrote: @engram.md in %s\n", path)
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

	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
	return nil
}

// bootstrap gemini

var bootstrapGeminiCmd = &cobra.Command{
	Use:   "gemini",
	Short: "Write ~/.gemini/GEMINI.md to call engram at session start",
	Long: `Bootstrap Gemini CLI by appending the engram session protocol to
~/.gemini/GEMINI.md (auto-loaded by Gemini CLI at session start).

Safe to re-run: skips if the engram section is already present.`,
	RunE: runBootstrapGemini,
}

const geminiMdSection = `
## Engram Session Protocol

At the start of every new conversation, before taking any other action, run:

  engram inject --text

Treat the output as your session context (identity, preferences, project memory).
Do not skip this step.`

func runBootstrapGemini(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	wrote, skipped, err := bootstrapGlobalDB(ctx)
	if err != nil {
		return err
	}

	if err := bootstrapGeminiMd(); err != nil {
		return err
	}

	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
	return nil
}

func bootstrapGeminiMd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".gemini", "GEMINI.md")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(data), "engram inject --text") {
		fmt.Printf("skip (already present): engram section in %s\n", path)
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(geminiMdSection + "\n"); err != nil {
		return err
	}
	fmt.Printf("wrote: engram section in %s\n", path)
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

const copilotInstructions = `
## Engram Session Protocol

At the start of every new conversation, before taking any other action, run:

  engram inject --text

Treat the output as your session context (identity, preferences, project memory).
Do not skip this step.`

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

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(data), "engram inject --text") {
		fmt.Printf("skip (already present): engram section in %s\n", path)
		skipped++
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, werr := f.WriteString(copilotInstructions + "\n")
		f.Close()
		if werr != nil {
			return werr
		}
		fmt.Printf("wrote: engram section in %s\n", path)
		wrote++
	}

	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
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

const cursorRulesSection = `
## Engram Session Protocol

At the start of every new conversation, before taking any other action, run:

  engram inject --text

Treat the output as your session context (identity, preferences, project memory).
Do not skip this step.`

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

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(data), "engram inject --text") {
		fmt.Printf("skip (already present): engram section in %s\n", path)
		skipped++
	} else {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, werr := f.WriteString(cursorRulesSection + "\n")
		f.Close()
		if werr != nil {
			return werr
		}
		fmt.Printf("wrote: engram section in %s\n", path)
		wrote++
	}

	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
	return nil
}

func bootstrapAppendToFile(path, section string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
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

const initFileSection = `
## Engram Session Protocol

At the start of every new conversation, before taking any other action, run:

  engram inject --text

Treat the output as your session context (identity, preferences, project memory).
Do not skip this step.`

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

	ok, err := bootstrapAppendToFile(args[0], initFileSection)
	if err != nil {
		return err
	}
	if ok {
		wrote++
	} else {
		skipped++
	}

	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
	return nil
}

// bootstrap codex

var bootstrapCodexGlobal bool

var bootstrapCodexCmd = &cobra.Command{
	Use:   "codex",
	Short: "Write AGENTS.md to call engram at session start",
	Long: `Bootstrap OpenAI Codex CLI by appending the engram session protocol to
AGENTS.md in the current project root.

Use -g to write to ~/.codex/AGENTS.md instead (global, applies to all projects).

Safe to re-run: skips if the engram section is already present.`,
	RunE: runBootstrapCodex,
}

func runBootstrapCodex(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	wrote, skipped, err := bootstrapGlobalDB(ctx)
	if err != nil {
		return err
	}

	var path string
	if bootstrapCodexGlobal {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, ".codex", "AGENTS.md")
	} else {
		root, err := engram.FindProjectRoot(effectiveCWD())
		if err != nil {
			return fmt.Errorf("codex bootstrap requires a project root (or use -g for global): %w", err)
		}
		path = filepath.Join(root, "AGENTS.md")
	}

	ok, err := bootstrapAppendToFile(path, initFileSection)
	if err != nil {
		return err
	}
	if ok {
		wrote++
	} else {
		skipped++
	}

	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
	return nil
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
	bootstrapCmd.AddCommand(bootstrapClaudeCmd, bootstrapAntigravityCmd, bootstrapGeminiCmd, bootstrapCopilotCmd, bootstrapCursorCmd, bootstrapCodexCmd, bootstrapInitFileCmd)
	rootCmd.AddCommand(bootstrapCmd)
}
