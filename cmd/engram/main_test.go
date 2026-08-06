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

func TestRootCommandAlwaysCarriesVersion(t *testing.T) {
	want := engramVersion()
	if rootCmd.Version != want {
		t.Errorf("root version = %q, want %q", rootCmd.Version, want)
	}
	if !strings.Contains(rootCmd.Long, "Engram version "+want) {
		t.Errorf("root help does not show version %q", want)
	}
}

func TestResolveEngramVersion(t *testing.T) {
	// The rule: a local build never claims to BE the release. It used to, and that
	// both misled and silenced the guidance drift check, which compares the running
	// version against the version stamped into the shipped guidance.
	tests := []struct {
		name         string
		buildVersion string
		revision     string
		modified     bool
		want         string
	}{
		{"released module wins outright", "v9.8.7", "abc1234def", true, "v9.8.7"},
		{"clean local build names its commit", "(devel)", "abc1234def5678", false, sourceVersion + "+abc1234"},
		{"dirty local build says so", "(devel)", "abc1234def5678", true, sourceVersion + "+abc1234.dirty"},
		// Go does not stamp vcs.* from a linked git worktree, so this is the case
		// that produced a bare release number from a tree of unreleased work.
		{"no vcs stamp admits devel", "(devel)", "", false, sourceVersion + "+devel"},
		// Current toolchains stamp a pseudo-version instead of "(devel)", which is
		// why the old check silently stopped catching local builds. It also hides
		// which release the tree is based on, so it is reduced to release+commit.
		{"pseudo-version is a local build", "v0.0.0-20260806001334-0d9ca7e982a4", "", false,
			sourceVersion + "+0d9ca7e"},
		{"pseudo-version marked dirty by go", "v0.0.0-20260806001334-0d9ca7e982a4+dirty", "", false,
			sourceVersion + "+0d9ca7e.dirty"},
		{"vcs stamp wins over the pseudo-version hash", "v0.0.0-20260806001334-0d9ca7e982a4",
			"feedface1234", true, sourceVersion + "+feedfac.dirty"},
		{"missing build info admits devel", "", "", false, sourceVersion + "+devel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveEngramVersion(tt.buildVersion, tt.revision, tt.modified)
			if got != tt.want {
				t.Errorf("resolveEngramVersion(%q, %q, %v) = %q, want %q",
					tt.buildVersion, tt.revision, tt.modified, got, tt.want)
			}
			if isLocalBuildVersion(tt.buildVersion) && got == sourceVersion {
				t.Error("a local build reported the bare release version")
			}
		})
	}
}

func TestReleaseVersionStripsBuildMetadata(t *testing.T) {
	for input, want := range map[string]string{
		"v0.13.1":               "v0.13.1",
		"v0.13.1+devel":         "v0.13.1",
		"v0.13.1+abc1234.dirty": "v0.13.1",
	} {
		if got := releaseVersion(input); got != want {
			t.Errorf("releaseVersion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSourceVersionMatchesLatestChangelogRelease(t *testing.T) {
	data, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	var latest string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## [") && line != "## [Unreleased]" {
			latest = line
			break
		}
	}
	wantPrefix := "## [" + strings.TrimPrefix(releaseVersion(sourceVersion), "v") + "]"
	if !strings.HasPrefix(latest, wantPrefix) {
		t.Errorf("sourceVersion = %q but latest changelog release is %q", sourceVersion, latest)
	}
}

func TestFirstRecordableEventCreatesProjectDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "code", "fresh")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Inspection of a pristine repository stays side-effect-free.
	ro, err := engram.OpenProjectDBReadOnly(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	if engram.ProjectDBExists(root) {
		t.Fatal("read-only inspection created a project database")
	}
	if _, err := os.Stat(filepath.Join(root, ".engram")); !os.IsNotExist(err) {
		t.Fatalf("read-only inspection created .engram: %v", err)
	}

	touched := filepath.Join(root, "main.go")
	payload, err := json.Marshal(map[string]any{
		"session_id": "session-1",
		"cwd":        root,
		"tool_name":  "Edit",
		"tool_input": map[string]any{"file_path": touched},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		w.Close()
		r.Close()
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		r.Close()
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		_ = r.Close()
	}()

	if err := runRecord(recordCmd, nil); err != nil {
		t.Fatal(err)
	}
	if !engram.ProjectDBExists(root) {
		t.Fatal("first recordable event did not create the project database")
	}

	db, err := engram.OpenProjectDBReadOnly(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sessionID, tool, filePath string
	if err := db.QueryRow(`SELECT session_id, tool, file_path FROM events`).Scan(&sessionID, &tool, &filePath); err != nil {
		t.Fatal(err)
	}
	if sessionID != "session-1" || tool != "Edit" || filePath != "main.go" {
		t.Errorf("recorded event = (%q, %q, %q), want (%q, %q, %q)",
			sessionID, tool, filePath, "session-1", "Edit", "main.go")
	}

	gdb, err := engram.OpenGlobalDBReadOnly(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()
	var registeredPath string
	if err := gdb.QueryRow(`SELECT path FROM projects`).Scan(&registeredPath); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("code", "fresh"); registeredPath != want {
		t.Errorf("registered path = %q, want %q", registeredPath, want)
	}
}
