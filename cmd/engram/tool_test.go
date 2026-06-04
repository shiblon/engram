package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shiblon/engram/pkg/engram"
)

func TestValidToolName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"foo.sh", true},
		{"fuzzy-find.py", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{`a\b`, false},
		{"../escape.sh", false},
	}
	for _, c := range cases {
		if err := validToolName(c.name); (err == nil) != c.ok {
			t.Errorf("validToolName(%q): ok=%v, want %v (err=%v)", c.name, err == nil, c.ok, err)
		}
	}
}

// setupToolProject creates a temp project root (with a .engram marker) and a temp
// HOME, points the command layer at them via rootCWD, and restores globals after.
func setupToolProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".engram"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	prevCWD, prevTo := rootCWD, toolPromoteTo
	rootCWD = root
	t.Cleanup(func() { rootCWD, toolPromoteTo = prevCWD, prevTo })
	return root
}

func stageFixture(t *testing.T, root, name, body string) {
	t.Helper()
	dir := engram.ProjectToolCandidatesDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestToolPromoteProjectMoves(t *testing.T) {
	root := setupToolProject(t)
	stageFixture(t, root, "greet.sh", "# engram-desc: hi\n")
	toolPromoteTo = "project"

	if err := runToolPromote(nil, []string{"greet.sh"}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(engram.ProjectAgentToolsDir(root), "greet.sh")) {
		t.Error("tool missing from project dir after promote")
	}
	if fileExists(filepath.Join(engram.ProjectToolCandidatesDir(root), "greet.sh")) {
		t.Error("candidate should be moved out of staging on project promote")
	}
}

func TestToolPromoteGlobalCopiesProjectTool(t *testing.T) {
	root := setupToolProject(t)
	pdir := engram.ProjectAgentToolsDir(root)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "greet.sh"), []byte("# engram-desc: hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	toolPromoteTo = "global"

	if err := runToolPromote(nil, []string{"greet.sh"}); err != nil {
		t.Fatal(err)
	}
	gdir, _ := engram.GlobalAgentToolsDir()
	if !fileExists(filepath.Join(gdir, "greet.sh")) {
		t.Error("tool not copied to global")
	}
	if !fileExists(filepath.Join(pdir, "greet.sh")) {
		t.Error("project copy must remain (project->global is a copy, not a move)")
	}
}

func TestToolPromoteCandidateToGlobalMoves(t *testing.T) {
	root := setupToolProject(t)
	stageFixture(t, root, "g.sh", "# engram-desc: hi\n")
	toolPromoteTo = "global"

	if err := runToolPromote(nil, []string{"g.sh"}); err != nil {
		t.Fatal(err)
	}
	gdir, _ := engram.GlobalAgentToolsDir()
	if !fileExists(filepath.Join(gdir, "g.sh")) {
		t.Error("candidate not promoted to global")
	}
	if fileExists(filepath.Join(engram.ProjectToolCandidatesDir(root), "g.sh")) {
		t.Error("candidate should be moved out of staging")
	}
}

func TestToolPromoteMissing(t *testing.T) {
	setupToolProject(t)
	toolPromoteTo = "project"
	if err := runToolPromote(nil, []string{"ghost.sh"}); err == nil {
		t.Error("expected error promoting a nonexistent candidate")
	}
}

func TestToolDiscard(t *testing.T) {
	root := setupToolProject(t)
	stageFixture(t, root, "toss.sh", "x")

	if err := runToolDiscard(nil, []string{"toss.sh"}); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(engram.ProjectToolCandidatesDir(root), "toss.sh")) {
		t.Error("candidate not discarded")
	}
	if err := runToolDiscard(nil, []string{"nope.sh"}); err == nil {
		t.Error("expected error discarding a missing candidate")
	}
}

func TestCopyFilePreservesMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("hello"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Errorf("mode = %o, want 750", info.Mode().Perm())
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "hello" {
		t.Errorf("content = %q, want hello", data)
	}
}
