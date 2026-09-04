package main

import (
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
  claude       -- install the policy kernel plus Claude Code hooks
  codex        -- install the policy kernel plus Codex CLI hooks
  gemini       -- install the policy kernel in GEMINI.md
  antigravity  -- write a Knowledge Item that instructs AntiGravity to call engram at session start
  copilot      -- write .github/copilot-instructions.md in the current project
  cursor       -- write .cursorrules in the current project
  initfile     -- append the engram protocol to any init file (generic escape hatch)

Preview every target with annotated unified patches by adding --dry-run. Add
--diff instead to preview the same complete plan and accept or reject it as one
coherent installation. Unchanged files appear with empty patch headers.`,
}

// bootstrap claude

var bootstrapClaudeGlobal bool
var bootstrapClaudeProject bool

var bootstrapClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Set up Claude Code hooks and CLAUDE.md",
	Long: `Bootstrap Claude Code by patching ~/.claude/CLAUDE.md
and adding engram hooks to settings.json.

Hooks are global by default and written to ~/.claude/settings.json. Use
--project to write hooks to the current project's .claude/settings.json instead;
the generated kernel and status line remain global.

Unrelated settings and user-authored instructions are preserved. Generated
Engram entries are updated or retired as needed, so the command is safe to re-run.`,
	RunE: runBootstrapClaude,
}

func runBootstrapClaude(cmd *cobra.Command, _ []string) error {
	global, err := bootstrapGlobalScope(bootstrapClaudeGlobal, bootstrapClaudeProject)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return runBootstrapPlan(cmd, func(plan *bootstrapPlan) error {
		if err := bootstrapEngramMd(plan); err != nil {
			return err
		}
		if err := bootstrapClaudeMd(plan); err != nil {
			return err
		}
		if err := retireClaudeStandingMd(plan); err != nil {
			return err
		}
		if err := bootstrapStatusLine(plan, exe); err != nil {
			return err
		}
		return bootstrapHooks(plan, exe, global)
	})
}

// bootstrapGlobalScope keeps --global as a compatibility spelling while making
// global installation the default. Project-local installation is always an
// explicit choice so a forgotten flag cannot dirty the current repository.
func bootstrapGlobalScope(globalFlag, projectFlag bool) (bool, error) {
	if globalFlag && projectFlag {
		return false, fmt.Errorf("--global and --project select different bootstrap scopes; choose one")
	}
	return !projectFlag, nil
}

func bootstrapEngramMd(plan *bootstrapPlan) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "engram.md")
	content := "<!-- GENERATED FILE -- do not edit directly; re-run `engram bootstrap claude`. -->\n" + renderGuidanceKernel("claude") + "\n"
	return plan.writeFile(path, []byte(content), 0o644, "install the generated Claude policy kernel")
}

func bootstrapClaudeMd(plan *bootstrapPlan) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")

	data, err := plan.readFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)

	if engramBlock.MatchString(content) {
		content = engramBlock.ReplaceAllString(content, "")
	}

	// The generated kernel is the sole static import. Identity and preferences
	// arrive through SessionStart injection, so importing their generated files as
	// well would duplicate session context and create two delivery semantics.
	const include = "@engram.md"
	if hasExactLine(content, include) {
		return plan.writeFile(path, []byte(content), 0o644, "import the generated Engram policy kernel")
	}
	content += "\n" + include + "\n"
	return plan.writeFile(path, []byte(content), 0o644, "import the generated Engram policy kernel")
}

func hasExactLine(content, want string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

// retireClaudeStandingMd removes the old generated identity/preference imports
// and files after the common kernel has been installed. User-authored content is
// untouched; only exact @generated-file lines and Engram's known generated files
// are removed.
func retireClaudeStandingMd(plan *bootstrapPlan) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	claudeMd := filepath.Join(home, ".claude", "CLAUDE.md")
	data, err := plan.readFile(claudeMd)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)
	legacyImports := make(map[string]bool, len(engram.StandingFileBases()))
	for _, base := range engram.StandingFileBases() {
		legacyImports["@"+base] = true
	}
	lines := strings.Split(content, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !legacyImports[strings.TrimSpace(line)] {
			kept = append(kept, line)
		}
	}
	updated := strings.Join(kept, "\n")
	if err := plan.writeFile(claudeMd, []byte(updated), 0o644, "remove legacy standing-memory imports"); err != nil {
		return err
	}
	for _, base := range engram.StandingFileBases() {
		path := filepath.Join(home, ".claude", base)
		if err := plan.removeFile(path, "retire legacy generated standing-memory file"); err != nil {
			return err
		}
	}
	return nil
}

func bootstrapStatusLine(plan *bootstrapPlan, exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "settings.json")

	settings, err := readSettingsJSON(plan, path)
	if err != nil {
		return err
	}

	if _, exists := settings["statusLine"]; exists {
		return writeSettingsJSON(plan, path, settings, "install the Claude status line")
	}

	settings["statusLine"] = map[string]any{
		"type":            "command",
		"command":         exe + " status",
		"refreshInterval": 30,
	}

	return writeSettingsJSON(plan, path, settings, "install the Claude status line")
}

func bootstrapHooks(plan *bootstrapPlan, exe string, global bool) error {
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
			return nil
		}
		path = filepath.Join(root, ".claude", "settings.json")
	}

	data, _ := plan.readFile(path)
	if strings.Contains(string(data), "engram record") {
	} else {
		if err := addEngramHooks(plan, path, exe); err != nil {
			return err
		}
	}
	if err := ensureClaudeInjectAgent(plan, path); err != nil {
		return err
	}

	// Ensure the engram allowlist independently of the hooks check above, so
	// re-running bootstrap repairs older installs that predate it.
	if err := ensureEngramAllowlist(plan, path); err != nil {
		return err
	}
	return nil
}

func ensureClaudeInjectAgent(plan *bootstrapPlan, path string) error {
	settings, err := readSettingsJSON(plan, path)
	if err != nil {
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	if !updateEngramHookCommand(hooks, "SessionStart", "engram inject", "engram inject --agent claude") {
		return writeSettingsJSON(plan, path, settings, "install Claude record and session-start hooks")
	}
	settings["hooks"] = hooks
	return writeSettingsJSON(plan, path, settings, "install Claude record and session-start hooks")
}

// engramAllowlist is the set of engram command families an agent invokes
// directly. bootstrap pre-approves exactly these so the memory workflow never
// trips a per-call permission prompt. Hooks (record/inject/status) run via the
// harness and need no allowlisting; uninstall/bootstrap are deliberately NOT
// auto-granted.
var engramAllowlist = []string{
	"Bash(engram mem:*)",
}

// ensureEngramAllowlist adds the engramAllowlist patterns to
// settings.permissions.allow if absent. Idempotent: a no-op when all present.
func ensureEngramAllowlist(plan *bootstrapPlan, path string) error {
	settings, err := readSettingsJSON(plan, path)
	if err != nil {
		return err
	}
	for _, pattern := range engramAllowlist {
		addAllowedTool(settings, pattern)
	}
	return writeSettingsJSON(plan, path, settings, "allow direct Engram memory commands")
}

func addEngramHooks(plan *bootstrapPlan, path string, exe string) error {
	settings, err := readSettingsJSON(plan, path)
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
				"command": exe + " inject --agent claude",
			}},
		},
	)
	settings["hooks"] = hooks

	return writeSettingsJSON(plan, path, settings, "install Claude record and session-start hooks")
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

The KI instructs AntiGravity to run "engram inject --text --agent antigravity"
at the start of every session and treat the output as session context.

Safe to re-run: skips if the KI already exists.`,
	RunE: runBootstrapAntigravity,
}

var antigravityInstructions = strings.TrimPrefix(renderGuidanceKernel("antigravity"), "\n")

const antigravitySummary = `REQUIRED: At the start of every new conversation, before taking any other action, run "engram inject --text --agent antigravity" and treat the output as your session context (identity, preferences, project memory, and the AntiGravity layer).`

func runBootstrapAntigravity(cmd *cobra.Command, _ []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	kiDir := filepath.Join(home, ".gemini", "antigravity", "knowledge", "engram_protocol")
	artifactsDir := filepath.Join(kiDir, "artifacts")

	metaPath := filepath.Join(kiDir, "metadata.json")
	return runBootstrapPlan(cmd, func(plan *bootstrapPlan) error {
		if meta, err := plan.readFile(metaPath); err == nil {
			if err := plan.writeFile(metaPath, meta, 0o644, "preserve the existing AntiGravity Knowledge Item"); err != nil {
				return err
			}
			for _, path := range []string{filepath.Join(kiDir, "timestamps.json"), filepath.Join(artifactsDir, "instructions.md")} {
				if data, err := plan.readFile(path); err == nil {
					if err := plan.writeFile(path, data, 0o644, "preserve the existing AntiGravity Knowledge Item"); err != nil {
						return err
					}
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339)

		meta, _ := json.MarshalIndent(map[string]any{
			"title":     "Engram Session Protocol",
			"summary":   antigravitySummary,
			"timestamp": now,
		}, "", "  ")
		if err := plan.writeFile(metaPath, append(meta, '\n'), 0o644, "create the AntiGravity Knowledge Item metadata"); err != nil {
			return err
		}

		ts, _ := json.MarshalIndent(map[string]any{
			"created":  now,
			"modified": now,
			"accessed": now,
		}, "", "  ")
		if err := plan.writeFile(filepath.Join(kiDir, "timestamps.json"), append(ts, '\n'), 0o644, "create the AntiGravity Knowledge Item timestamps"); err != nil {
			return err
		}

		if err := plan.writeFile(filepath.Join(artifactsDir, "instructions.md"), []byte(antigravityInstructions+"\n"), 0o644, "install the Engram policy kernel for AntiGravity"); err != nil {
			return err
		}
		return nil
	})
}

// bootstrap gemini

var bootstrapGeminiCmd = &cobra.Command{
	Use:   "gemini",
	Short: "Install the Engram policy kernel in GEMINI.md",
	Long: `Bootstrap Gemini CLI for engram by installing the common policy kernel
in ~/.gemini/GEMINI.md. The kernel's first-interaction rule performs injection
when no lifecycle hook supplied context. Engram installs hooks only for provider
lifecycles covered by its compatibility tests; re-bootstrap removes legacy
Engram Gemini hooks installed by earlier releases.

Safe to re-run: skips pieces that are already present.`,
	RunE: runBootstrapGemini,
}

// engramProtocolSection is retained as the installation-facing name for the
// common policy kernel. Provider installers supply only their normalized agent
// name; all policy prose and reference routing come from the topic registry.
func engramProtocolSection(agent string) string {
	return renderGuidanceKernel(agent)
}

// policyInstallAdapter describes only provider delivery mechanics. Policy prose
// is deliberately absent: every adapter installs renderGuidanceKernel(agent).
// Lifecycle configuration is optional: tested providers install hooks, while a
// demoted provider can remove legacy hooks from the same adapter boundary.
type policyInstallAdapter struct {
	agent              string
	resolvePath        func() (string, error)
	configureLifecycle func(plan *bootstrapPlan, exe string) error
}

func runPolicyInstall(cmd *cobra.Command, adapter policyInstallAdapter) error {
	path, err := adapter.resolvePath()
	if err != nil {
		return err
	}
	return runBootstrapPlan(cmd, func(plan *bootstrapPlan) error {
		if _, err := bootstrapAppendToFile(plan, path, renderGuidanceKernel(adapter.agent)); err != nil {
			return err
		}
		if adapter.configureLifecycle == nil {
			return nil
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return adapter.configureLifecycle(plan, exe)
	})
}

func homePolicyPath(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}

func projectPolicyPath(name string, parts ...string) (string, error) {
	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		return "", fmt.Errorf("%s bootstrap requires a project root: %w", name, err)
	}
	return filepath.Join(append([]string{root}, parts...)...), nil
}

func runBootstrapGemini(cmd *cobra.Command, _ []string) error {
	var settingsPath string
	return runPolicyInstall(cmd, policyInstallAdapter{
		agent: "gemini",
		resolvePath: func() (string, error) {
			path, err := homePolicyPath(".gemini", "GEMINI.md")
			if err != nil {
				return "", err
			}
			settingsPath, err = homePolicyPath(".gemini", "settings.json")
			return path, err
		},
		configureLifecycle: func(plan *bootstrapPlan, _ string) error {
			return planStripEngramHooks(plan, settingsPath,
				hookSpec{event: "AfterTool", subcommand: "record"},
				hookSpec{event: "SessionStart", subcommand: "inject"},
			)
		},
	})
}

func planStripEngramHooks(plan *bootstrapPlan, path string, specs ...hookSpec) error {
	data, err := plan.readFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return plan.writeFile(path, data, 0o644, "remove legacy Engram lifecycle hooks")
	}
	changed := false
	for _, spec := range specs {
		if removeEngramHookQuiet(hooks, spec.event, "engram "+spec.subcommand) {
			changed = true
		}
	}
	if !changed {
		return plan.writeFile(path, data, 0o644, "remove legacy Engram lifecycle hooks")
	}
	settings["hooks"] = hooks
	return writeSettingsJSON(plan, path, settings, "remove legacy Engram lifecycle hooks")
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
	return runPolicyInstall(cmd, policyInstallAdapter{
		agent: "copilot",
		resolvePath: func() (string, error) {
			return projectPolicyPath("copilot", ".github", "copilot-instructions.md")
		},
	})
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
	return runPolicyInstall(cmd, policyInstallAdapter{
		agent: "cursor",
		resolvePath: func() (string, error) {
			return projectPolicyPath("cursor", ".cursorrules")
		},
	})
}

func bootstrapAppendToFile(plan *bootstrapPlan, path, section string) (bool, error) {
	data, err := plan.readFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if engramSectionRE.Match(data) {
		updated := engramSectionRE.ReplaceAll(data, []byte(section+"\n"))
		if string(updated) == string(data) {
			if err := plan.writeFile(path, data, 0o644, "install or update the Engram policy kernel section"); err != nil {
				return false, err
			}
			return false, nil
		}
		if err := plan.writeFile(path, updated, 0o644, "install or update the Engram policy kernel section"); err != nil {
			return false, err
		}
		return true, nil
	}
	updated := append(append([]byte(nil), data...), []byte(section+"\n")...)
	if err := plan.writeFile(path, updated, 0o644, "install or update the Engram policy kernel section"); err != nil {
		return false, err
	}
	return true, nil
}

// bootstrap initfile

var bootstrapInitFileAgent string

var bootstrapInitFileCmd = &cobra.Command{
	Use:   "initfile <path>",
	Short: "Append the engram session protocol to any agent init file",
	Long: `Append the engram session protocol to the specified file.

Use this for any AI agent that reads a markdown init file at session start,
such as AGENTS.md (Codex), .windsurfrules, or any custom file.

Use --agent NAME to render that agent's layer into the fallback inject command.

Safe to re-run: skips if the engram section is already present.`,
	Args: cobra.ExactArgs(1),
	RunE: runBootstrapInitFile,
}

func runBootstrapInitFile(cmd *cobra.Command, args []string) error {
	agent, err := engram.NormalizeAgent(bootstrapInitFileAgent)
	if err != nil {
		return err
	}
	return runPolicyInstall(cmd, policyInstallAdapter{
		agent: agent,
		resolvePath: func() (string, error) {
			return args[0], nil
		},
	})
}

// bootstrap codex

var bootstrapCodexGlobal bool
var bootstrapCodexProject bool
var bootstrapCodexNoSessionHook bool

var bootstrapCodexCmd = &cobra.Command{
	Use:   "codex",
	Short: "Set up Codex CLI hooks and AGENTS.md",
	Long: `Bootstrap OpenAI Codex CLI for engram. Two pieces are installed:

  - hooks.json: a SessionStart hook that runs "engram inject --agent codex"
    (loading memory plus the Codex layer into the session) and a PostToolUse hook
    on apply_patch that runs "engram record" (logging touched files). Codex's
    hook protocol matches Claude Code's, so record/inject work unchanged.
  - AGENTS.md: the human-readable engram session protocol, as a fallback.

By default both go in ~/.codex (global, applies to all projects). Use --project
to write .codex/hooks.json and AGENTS.md in the current project instead. Codex
only honors project-local .codex/ config in trusted projects.

Use --no-session-hook to skip the SessionStart inject hook and rely on AGENTS.md
for startup context while keeping apply_patch file tracking. Re-running with
--no-session-hook removes an existing engram SessionStart hook.

Safe to re-run: skips pieces that are already present.`,
	RunE: runBootstrapCodex,
}

func runBootstrapCodex(cmd *cobra.Command, _ []string) error {
	global, err := bootstrapGlobalScope(bootstrapCodexGlobal, bootstrapCodexProject)
	if err != nil {
		return err
	}
	var hooksPath string
	return runPolicyInstall(cmd, policyInstallAdapter{
		agent: "codex",
		resolvePath: func() (string, error) {
			if global {
				path, err := homePolicyPath(".codex", "AGENTS.md")
				if err != nil {
					return "", err
				}
				hooksPath, err = homePolicyPath(".codex", "hooks.json")
				return path, err
			}
			root, err := engram.FindProjectRoot(effectiveCWD())
			if err != nil {
				return "", fmt.Errorf("codex bootstrap requires a project root (or use -g for global): %w", err)
			}
			hooksPath = filepath.Join(root, ".codex", "hooks.json")
			return filepath.Join(root, "AGENTS.md"), nil
		},
		configureLifecycle: func(plan *bootstrapPlan, exe string) error {
			return bootstrapCodexHooks(plan, hooksPath, exe, !bootstrapCodexNoSessionHook)
		},
	})
}

// hookSpec describes one engram hook to install: the settings event key, an
// optional tool/lifecycle matcher (empty means fire on every occurrence of the
// event), and the engram subcommand the handler runs.
type hookSpec struct {
	event      string
	matcher    string
	subcommand string
}

// installEngramHooks merges the given engram hooks into a compatible hook-config
// JSON file without disturbing existing hooks. Idempotent: a
// spec whose command is already registered under its event is skipped, so
// partial installs repair cleanly on re-run.
func installEngramHooks(plan *bootstrapPlan, path, exe string, specs []hookSpec) error {
	settings, err := readSettingsJSON(plan, path)
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
		if strings.HasPrefix(s.subcommand, "inject") {
			marker = "engram inject"
		}
		if dedupeEngramHooks(hooks, s.event, marker) {
			changed = true
		}
		if updateEngramHookCommand(hooks, s.event, marker, "engram "+s.subcommand) {
			changed = true
		}
		if engramHookPresent(hooks, s.event, marker) {
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
		changed = true
	}
	if !changed {
		return writeSettingsJSON(plan, path, settings, "install Engram lifecycle hooks")
	}

	settings["hooks"] = hooks
	return writeSettingsJSON(plan, path, settings, "install Engram lifecycle hooks")
}

// updateEngramHookCommand upgrades an existing semantic hook in place while
// preserving the executable path that was already installed. This lets a stable
// /usr/local/bin/engram hook gain new arguments without being replaced by a
// transient development-binary path from go test/go run.
func updateEngramHookCommand(hooks map[string]any, event, marker, want string) bool {
	changed := false
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
			cmd, _ := hm["command"].(string)
			idx := strings.Index(cmd, marker)
			if idx < 0 {
				continue
			}
			next := cmd[:idx] + want
			if cmd != next {
				hm["command"] = next
				changed = true
			}
		}
	}
	return changed
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
func dedupeEngramHooks(hooks map[string]any, event, marker string) bool {
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
	}
	return changed
}

// bootstrapCodexHooks installs engram's Codex hooks. The record hook always
// tracks apply_patch file edits; the SessionStart inject hook is optional
// because Codex currently surfaces hook additionalContext visibly. When the
// session hook is omitted, any existing engram SessionStart hook is removed so
// re-running bootstrap repairs earlier installs.
func bootstrapCodexHooks(plan *bootstrapPlan, path, exe string, includeSessionHook bool) error {
	specs := []hookSpec{
		{event: "PostToolUse", matcher: "^apply_patch$", subcommand: "record"},
	}
	if includeSessionHook {
		specs = append([]hookSpec{
			{event: "SessionStart", matcher: "startup|resume|clear|compact", subcommand: "inject --agent codex"},
		}, specs...)
	}
	if err := installEngramHooks(plan, path, exe, specs); err != nil {
		return err
	}
	if includeSessionHook {
		return nil
	}

	settings, err := readSettingsJSON(plan, path)
	if err != nil {
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	if !removeEngramHookQuiet(hooks, "SessionStart", "engram inject") {
		return writeSettingsJSON(plan, path, settings, "install Codex lifecycle hooks")
	}
	settings["hooks"] = hooks
	return writeSettingsJSON(plan, path, settings, "install Codex lifecycle hooks")
}

func readSettingsJSON(plan *bootstrapPlan, path string) (map[string]any, error) {
	data, err := plan.readFile(path)
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

func writeSettingsJSON(plan *bootstrapPlan, path string, settings map[string]any, reason string) error {
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return plan.writeFile(path, append(out, '\n'), 0o644, reason)
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func init() {
	bootstrapCmd.PersistentFlags().BoolVar(&bootstrapDryRun, "dry-run", false, "print annotated unified diffs without changing files or memories")
	bootstrapCmd.PersistentFlags().BoolVar(&bootstrapDiff, "diff", false, "print annotated unified diffs and ask whether to apply the complete plan")
	bootstrapClaudeCmd.Flags().BoolVarP(&bootstrapClaudeGlobal, "global", "g", false, "install hooks globally (default; retained for compatibility)")
	bootstrapClaudeCmd.Flags().BoolVarP(&bootstrapClaudeProject, "project", "p", false, "install hooks in the current project; kernel and status line remain global")
	bootstrapCodexCmd.Flags().BoolVarP(&bootstrapCodexGlobal, "global", "g", false, "install in ~/.codex (default; retained for compatibility)")
	bootstrapCodexCmd.Flags().BoolVarP(&bootstrapCodexProject, "project", "p", false, "install AGENTS.md and hooks in the current project")
	bootstrapCodexCmd.Flags().BoolVar(&bootstrapCodexNoSessionHook, "no-session-hook", false, "do not install Codex SessionStart inject hook; rely on AGENTS.md fallback")
	bootstrapInitFileCmd.Flags().StringVar(&bootstrapInitFileAgent, "agent", "", "agent layer named by the fallback inject command")
	bootstrapCmd.AddCommand(bootstrapClaudeCmd, bootstrapAntigravityCmd, bootstrapGeminiCmd, bootstrapCopilotCmd, bootstrapCursorCmd, bootstrapCodexCmd, bootstrapInitFileCmd)
	rootCmd.AddCommand(bootstrapCmd)
}
