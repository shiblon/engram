package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The session-protocol block lives in one constant (engramProtocolSection,
// written by every markdown-init-file bootstrap) and is removed by one regex
// (engramSectionRE, used by every corresponding uninstall). They live in
// separate files and must not drift: a regex that no longer matches what
// bootstrap wrote would silently leave the section behind on uninstall.
func TestUninstallRegexMatchesBootstrapSection(t *testing.T) {
	// bootstrap appends the section followed by a newline.
	written := engramProtocolSection + "\n"

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

	updated, err := bootstrapAppendToFile(path, engramProtocolSection)
	if err != nil {
		t.Fatalf("bootstrapAppendToFile: %v", err)
	}
	if !updated {
		t.Fatalf("bootstrapAppendToFile reported no update")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read init file: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "At the start of every new conversation, before taking any other action, run:") {
		t.Errorf("old unconditional startup instruction survived:\n%s", got)
	}
	if !strings.Contains(got, "If that context is already present, do not run another inject command.") {
		t.Errorf("new duplicate guard missing:\n%s", got)
	}
	if !strings.Contains(got, "\n\nkeep me\n") {
		t.Errorf("existing file content was not preserved:\n%s", got)
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

func readHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks file: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse hooks file: %v", err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	return hooks
}

// Both Codex and Gemini install record + inject hooks through the shared
// installEngramHooks helper, differing only in event names and matchers. Each
// must register both hooks, re-run as a no-op, and fully uninstall.
func TestAgentHooksRoundTrip(t *testing.T) {
	const exe = "/usr/local/bin/engram"
	cases := []struct {
		name         string
		bootstrap    func(path, exe string) error
		recordEvent  string
		recordMatch  string
		sessionEvent string
	}{
		{"codex", bootstrapCodexHooks, "PostToolUse", "^apply_patch$", "SessionStart"},
		{"gemini", bootstrapGeminiHooks, "AfterTool", "read_file|write_file|replace", "SessionStart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), c.name, "hooks.json")

			if err := c.bootstrap(path, exe); err != nil {
				t.Fatalf("bootstrap: %v", err)
			}
			hooks := readHooks(t, path)
			if got := hookCommand(t, hooks, c.recordEvent); !strings.Contains(got, "engram record") {
				t.Errorf("%s command = %q, want 'engram record'", c.recordEvent, got)
			}
			if got := hookCommand(t, hooks, c.sessionEvent); !strings.Contains(got, "engram inject") {
				t.Errorf("%s command = %q, want 'engram inject'", c.sessionEvent, got)
			}
			if got := hookMatcher(t, hooks, c.recordEvent); got != c.recordMatch {
				t.Errorf("%s matcher = %q, want %q", c.recordEvent, got, c.recordMatch)
			}

			// Idempotent: a second bootstrap adds no duplicate entries.
			if err := c.bootstrap(path, exe); err != nil {
				t.Fatalf("second bootstrap: %v", err)
			}
			hooks = readHooks(t, path)
			if n := len(hooks[c.recordEvent].([]any)); n != 1 {
				t.Errorf("%s entries after re-bootstrap = %d, want 1", c.recordEvent, n)
			}

			// Uninstall removes both engram hooks.
			if err := stripEngramHooks(path, c.recordEvent, c.sessionEvent); err != nil {
				t.Fatalf("stripEngramHooks: %v", err)
			}
			hooks = readHooks(t, path)
			if hookCommand(t, hooks, c.recordEvent) != "" || hookCommand(t, hooks, c.sessionEvent) != "" {
				t.Errorf("engram hooks survived uninstall: %+v", hooks)
			}
		})
	}
}
