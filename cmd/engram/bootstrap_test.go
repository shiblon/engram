package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiblon/engram/pkg/engram"
)

func applyBootstrapTestPlan(t *testing.T, build func(*bootstrapPlan) error) {
	t.Helper()
	plan := newBootstrapPlan()
	if err := build(plan); err != nil {
		t.Fatalf("build bootstrap plan: %v", err)
	}
	if err := plan.apply(context.Background()); err != nil {
		t.Fatalf("apply bootstrap plan: %v", err)
	}
}

func applyBootstrapCodexHooks(t *testing.T, path, exe string, includeSessionHook bool) {
	t.Helper()
	applyBootstrapTestPlan(t, func(plan *bootstrapPlan) error {
		return bootstrapCodexHooks(plan, path, exe, includeSessionHook)
	})
}

// The session-protocol block lives in one renderer (engramProtocolSection,
// written by every markdown-init-file bootstrap) and is removed by one regex
// (engramSectionRE, used by every corresponding uninstall). They live in
// separate files and must not drift: a regex that no longer matches what
// bootstrap wrote would silently leave the section behind on uninstall.
func TestUninstallRegexMatchesBootstrapSection(t *testing.T) {
	// bootstrap appends the section followed by a newline.
	written := engramProtocolSection("codex") + "\n"

	if !engramSectionRE.MatchString(written) {
		t.Fatalf("engramSectionRE does not match what bootstrap writes:\n%q", written)
	}

	// Removing the matched section from a realistic file must leave the rest
	// intact and drop the whole block.
	file := "# My init file\n\nsome existing content.\n" + written
	got := engramSectionRE.ReplaceAllString(file, "")
	if got != "# My init file\n\nsome existing content.\n" {
		t.Errorf("uninstall removal left unexpected residue:\n%q", got)
	}
}

func TestBootstrapAppendToFileUpdatesOldProtocolSection(t *testing.T) {
	oldSection := `
## Engram Session Protocol

At the start of every new conversation, before taking any other action, run:

  engram inject --text

Treat the output as your session context (identity, preferences, project memory).
Do not skip this step.`
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	before := "# My init file\n" + oldSection + "\n\nkeep me\n"
	if err := os.WriteFile(path, []byte(before), 0644); err != nil {
		t.Fatalf("write init file: %v", err)
	}

	plan := newBootstrapPlan()
	updated, err := bootstrapAppendToFile(plan, path, engramProtocolSection("codex"))
	if err != nil {
		t.Fatalf("bootstrapAppendToFile: %v", err)
	}
	if !updated {
		t.Fatalf("bootstrapAppendToFile reported no update")
	}
	if err := plan.apply(context.Background()); err != nil {
		t.Fatalf("apply bootstrap plan: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read init file: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "At the start of every new conversation, before taking any other action, run:") {
		t.Errorf("old unconditional startup instruction survived:\n%s", got)
	}
	if !strings.Contains(got, "use them without injecting again") {
		t.Errorf("common context-presence rule missing:\n%s", got)
	}
	if !strings.Contains(got, "engram inject --text --agent codex") {
		t.Errorf("agent-specific inject command missing:\n%s", got)
	}
	if !strings.Contains(got, "\n\nkeep me\n") {
		t.Errorf("existing file content was not preserved:\n%s", got)
	}
}

func TestBootstrapAppendToFileUpdatesUnlayeredProtocolSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	before := "# My init file\n" + engramProtocolSection("") + "\n\nkeep me\n"
	if err := os.WriteFile(path, []byte(before), 0644); err != nil {
		t.Fatalf("write init file: %v", err)
	}

	plan := newBootstrapPlan()
	updated, err := bootstrapAppendToFile(plan, path, engramProtocolSection("codex"))
	if err != nil {
		t.Fatalf("bootstrapAppendToFile: %v", err)
	}
	if !updated {
		t.Fatalf("bootstrapAppendToFile reported no update")
	}
	if err := plan.apply(context.Background()); err != nil {
		t.Fatalf("apply bootstrap plan: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read init file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "engram inject --text --agent codex") {
		t.Errorf("agent-specific inject command missing:\n%s", got)
	}
	if strings.Contains(got, "\n  engram inject --text\n") {
		t.Errorf("old unlayered inject command survived:\n%s", got)
	}
	if !strings.Contains(got, "\n\nkeep me\n") {
		t.Errorf("existing file content was not preserved:\n%s", got)
	}
}

func TestRetireClaudeStandingMdPreservesUserContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "CLAUDE.md")
	content := "# My instructions\n@engram-invariants.md\nkeep me\n@engram-preferences.md"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	for _, base := range engram.StandingFileBases() {
		if err := os.WriteFile(filepath.Join(dir, base), []byte("generated"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	applyBootstrapTestPlan(t, retireClaudeStandingMd)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "# My instructions\nkeep me" {
		t.Errorf("CLAUDE.md after retirement = %q", got)
	}
	for _, base := range engram.StandingFileBases() {
		if _, err := os.Stat(filepath.Join(dir, base)); !os.IsNotExist(err) {
			t.Errorf("legacy file %s survived retirement", base)
		}
	}
}

func TestBootstrapEngramMdWritesKernelOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	applyBootstrapTestPlan(t, bootstrapEngramMd)
	data, err := os.ReadFile(filepath.Join(home, ".claude", "engram.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "engram inject --text --agent claude") {
		t.Errorf("generated kernel missing Claude inject command")
	}
	if strings.Contains(got, "Slicing destroys the seams") {
		t.Errorf("generated kernel contains operational dispatch manual")
	}
}

func TestGeminiBootstrapRetiresLegacyHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, settingsPath, map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": "/usr/bin/engram inject --agent gemini"}},
			}},
			"AfterTool": []any{map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": "/usr/bin/engram record"}},
			}},
		},
	})
	if err := runBootstrapGemini(nil, nil); err != nil {
		t.Fatalf("runBootstrapGemini: %v", err)
	}
	settings := readSettings(t, settingsPath)
	if settings["theme"] != "dark" {
		t.Errorf("unrelated Gemini setting was not preserved")
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hookCommand(t, hooks, "SessionStart") != "" || hookCommand(t, hooks, "AfterTool") != "" {
		t.Errorf("legacy Gemini hooks survived re-bootstrap: %+v", hooks)
	}
	data, err := os.ReadFile(filepath.Join(home, ".gemini", "GEMINI.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "engram inject --text --agent gemini") {
		t.Errorf("Gemini policy kernel missing agent-specific fallback")
	}
}

// hookCommand digs the command string out of the first handler of a named hook
// event in a parsed hooks JSON, or "" if absent.
func hookCommand(t *testing.T, hooks map[string]any, event string) string {
	t.Helper()
	arr, ok := hooks[event].([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	group, _ := arr[0].(map[string]any)
	list, _ := group["hooks"].([]any)
	if len(list) == 0 {
		return ""
	}
	handler, _ := list[0].(map[string]any)
	cmd, _ := handler["command"].(string)
	return cmd
}

// hookMatcher returns the matcher of the first group under event, or "".
func hookMatcher(t *testing.T, hooks map[string]any, event string) string {
	t.Helper()
	arr, ok := hooks[event].([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	group, _ := arr[0].(map[string]any)
	m, _ := group["matcher"].(string)
	return m
}

func TestRemoveEngramHookQuietPreservesMixedHookGroup(t *testing.T) {
	hooks := map[string]any{
		"SessionStart": []any{map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": "/usr/bin/engram inject --agent gemini"},
				map[string]any{"type": "command", "command": "/usr/bin/keep-me"},
			},
		}},
	}
	if !removeEngramHookQuiet(hooks, "SessionStart", "engram inject") {
		t.Fatal("removeEngramHookQuiet reported no change")
	}
	if got := hookCommand(t, hooks, "SessionStart"); got != "/usr/bin/keep-me" {
		t.Fatalf("remaining hook command = %q, want /usr/bin/keep-me", got)
	}
}

func readHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	settings := readSettings(t, path)
	hooks, _ := settings["hooks"].(map[string]any)
	return hooks
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings file: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings file: %v", err)
	}
	return settings
}

func writeSettings(t *testing.T, path string, settings map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatalf("write settings file: %v", err)
	}
}

// Codex's tested provider adapter installs both hooks, re-runs as a no-op, and
// fully uninstalls. Providers without proven lifecycle behavior do not call this
// helper merely because their JSON happens to resemble another provider's.
func TestCodexHooksRoundTrip(t *testing.T) {
	const exe = "/usr/local/bin/engram"
	path := filepath.Join(t.TempDir(), "codex", "hooks.json")
	applyBootstrapCodexHooks(t, path, exe, true)
	hooks := readHooks(t, path)
	if got := hookCommand(t, hooks, "PostToolUse"); !strings.Contains(got, "engram record") {
		t.Errorf("PostToolUse command = %q, want 'engram record'", got)
	}
	if got := hookCommand(t, hooks, "SessionStart"); !strings.Contains(got, "engram inject --agent codex") {
		t.Errorf("SessionStart command = %q, want agent-specific inject", got)
	}
	if got := hookMatcher(t, hooks, "PostToolUse"); got != "^apply_patch$" {
		t.Errorf("PostToolUse matcher = %q, want %q", got, "^apply_patch$")
	}
	applyBootstrapCodexHooks(t, path, exe, true)
	hooks = readHooks(t, path)
	if n := len(hooks["PostToolUse"].([]any)); n != 1 {
		t.Errorf("PostToolUse entries after re-bootstrap = %d, want 1", n)
	}
	if err := stripEngramHooks(path, "PostToolUse", "SessionStart"); err != nil {
		t.Fatalf("stripEngramHooks: %v", err)
	}
	hooks = readHooks(t, path)
	if hookCommand(t, hooks, "PostToolUse") != "" || hookCommand(t, hooks, "SessionStart") != "" {
		t.Errorf("engram hooks survived uninstall: %+v", hooks)
	}
}

func TestAgentHooksUpgradePlainInjectCommand(t *testing.T) {
	const exe = "/usr/local/bin/engram"
	path := filepath.Join(t.TempDir(), "codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{
				"matcher": "startup|resume|clear|compact",
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": "/opt/homebrew/bin/engram inject",
				}},
			}},
		},
	}
	writeSettings(t, path, settings)

	applyBootstrapCodexHooks(t, path, exe, true)
	hooks := readHooks(t, path)
	if got := hookCommand(t, hooks, "SessionStart"); got != "/opt/homebrew/bin/engram inject --agent codex" {
		t.Errorf("SessionStart command = %q, want upgraded stable path", got)
	}
}

func TestCodexNoSessionHookKeepsRecordAndRemovesInject(t *testing.T) {
	const exe = "/usr/local/bin/engram"
	path := filepath.Join(t.TempDir(), "codex", "hooks.json")

	applyBootstrapCodexHooks(t, path, exe, true)
	hooks := readHooks(t, path)
	if got := hookCommand(t, hooks, "SessionStart"); !strings.Contains(got, "engram inject --agent codex") {
		t.Fatalf("initial SessionStart command = %q, want agent-specific engram inject", got)
	}

	applyBootstrapCodexHooks(t, path, exe, false)
	hooks = readHooks(t, path)
	if got := hookCommand(t, hooks, "SessionStart"); got != "" {
		t.Errorf("SessionStart command after no-session bootstrap = %q, want none", got)
	}
	if got := hookCommand(t, hooks, "PostToolUse"); !strings.Contains(got, "engram record") {
		t.Errorf("PostToolUse command after no-session bootstrap = %q, want engram record", got)
	}

	// Idempotent: a second no-session bootstrap still keeps only the record hook.
	applyBootstrapCodexHooks(t, path, exe, false)
	hooks = readHooks(t, path)
	if got := hookCommand(t, hooks, "SessionStart"); got != "" {
		t.Errorf("SessionStart command after second no-session bootstrap = %q, want none", got)
	}
	if n := len(hooks["PostToolUse"].([]any)); n != 1 {
		t.Errorf("PostToolUse entries after second no-session bootstrap = %d, want 1", n)
	}
}

func TestCodexBootstrapDoesNotDuplicateRecordHookWhenExePathChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex", "hooks.json")

	applyBootstrapCodexHooks(t, path, "/opt/homebrew/bin/engram", false)
	applyBootstrapCodexHooks(t, path, "/var/folders/tmp/go-build/b001/exe/engram", false)

	hooks := readHooks(t, path)
	if n := len(hooks["PostToolUse"].([]any)); n != 1 {
		t.Fatalf("PostToolUse entries after exe path changed = %d, want 1", n)
	}
	if got := hookCommand(t, hooks, "PostToolUse"); got != "/opt/homebrew/bin/engram record" {
		t.Errorf("PostToolUse command = %q, want original stable command", got)
	}
}

func TestCodexBootstrapDedupesExistingRecordHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex", "hooks.json")

	applyBootstrapCodexHooks(t, path, "/opt/homebrew/bin/engram", false)
	settings := readSettings(t, path)
	hooks := settings["hooks"].(map[string]any)
	hooks["PostToolUse"] = append(hooks["PostToolUse"].([]any), map[string]any{
		"matcher": "^apply_patch$",
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": "/var/folders/tmp/go-build/b001/exe/engram record",
		}},
	})
	writeSettings(t, path, settings)

	applyBootstrapCodexHooks(t, path, "/usr/local/bin/engram", false)
	hooks = readHooks(t, path)
	if n := len(hooks["PostToolUse"].([]any)); n != 1 {
		t.Fatalf("PostToolUse entries after repair = %d, want 1", n)
	}
	if got := hookCommand(t, hooks, "PostToolUse"); got != "/opt/homebrew/bin/engram record" {
		t.Errorf("PostToolUse command after repair = %q, want original stable command", got)
	}
}
