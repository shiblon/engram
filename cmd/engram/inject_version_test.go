package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectVersionLineCurrentInstallRefreshesOnlyLiveContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("> Guidance version: v1.2.3.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := injectVersionLine("v1.2.3", "codex", home)
	for _, want := range []string{
		"bootstrap status: current",
		"Guidance version v1.2.3",
		"engram agentinfo --kernel --agent codex",
		"retained snapshot",
		"Do not warn",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("injectVersionLine = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "tell the user") {
		t.Errorf("current install prompted an unnecessary user warning: %q", got)
	}
}

func TestInjectVersionLineFindsCurrentProjectInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("> Guidance version: v2.0.0.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := injectVersionLine("v2.0.0", "codex", filepath.Join(root, "subdir"))
	if !strings.Contains(got, "bootstrap status: current") {
		t.Errorf("project bootstrap was not detected: %q", got)
	}
}

func TestInjectVersionLineWarnsOnlyForStaleInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := injectVersionLine("v1.2.3", "codex", t.TempDir())
	for _, want := range []string{
		"engram version: v1.2.3",
		"bootstrap status: stale or absent",
		"engram agentinfo --kernel --agent codex",
		"offer to run `engram bootstrap`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("injectVersionLine = %q, want %q", got, want)
		}
	}
}

func TestInjectVersionLineDoesNotMistakeVersionPrefixForCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("> Guidance version: v1.2.30.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := injectVersionLine("v1.2.3", "codex", home)
	if !strings.Contains(got, "stale or absent") {
		t.Errorf("version prefix produced a false current result: %q", got)
	}
}

func TestInstalledGuidanceCurrentRecognizesEveryBootstrapProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := map[string]string{
		"claude":      filepath.Join(home, ".claude", "engram.md"),
		"codex":       filepath.Join(home, ".codex", "AGENTS.md"),
		"gemini":      filepath.Join(home, ".gemini", "GEMINI.md"),
		"antigravity": filepath.Join(home, ".gemini", "antigravity", "knowledge", "engram_protocol", "artifacts", "instructions.md"),
		"copilot":     filepath.Join(root, ".github", "copilot-instructions.md"),
		"cursor":      filepath.Join(root, ".cursorrules"),
	}
	for agent, path := range tests {
		t.Run(agent, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("> Guidance version: v9.9.9.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			current, known := installedGuidanceCurrent("v9.9.9", agent, root)
			if !known || !current {
				t.Errorf("installedGuidanceCurrent(%q) = (%v, %v), want (true, true)", agent, current, known)
			}
		})
	}

	if current, known := installedGuidanceCurrent("v9.9.9", "custom-agent", root); current || known {
		t.Errorf("custom initfile unexpectedly had a conventional install path: (%v, %v)", current, known)
	}
}
