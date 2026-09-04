package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestUnifiedFileDiffUsesStandardPatchFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.md")
	f := &plannedBootstrapFile{
		path:         path,
		before:       []byte("alpha\nbeta\ngamma\n"),
		beforeExists: true,
		after:        []byte("alpha\nBETA\ngamma\n"),
		afterExists:  true,
	}

	got := unifiedFileDiff(f)
	for _, want := range []string{
		"--- " + path + "\n",
		"+++ " + path + "\n",
		"@@ -1,3 +1,3 @@\n",
		"-beta\n",
		"+BETA\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unified diff missing %q:\n%s", want, got)
		}
	}
}

func TestUnifiedFileDiffShowsUnchangedTarget(t *testing.T) {
	f := &plannedBootstrapFile{
		path:         "policy.md",
		before:       []byte("same\n"),
		beforeExists: true,
		after:        []byte("same\n"),
		afterExists:  true,
	}
	if got, want := unifiedFileDiff(f), "--- policy.md\n+++ policy.md\n"; got != want {
		t.Fatalf("unchanged diff = %q, want %q", got, want)
	}
}

func TestBootstrapDryRunPrintsDiffWithoutWriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "AGENTS.md")
	cmd, out := bootstrapPlanTestCommand("")
	setBootstrapPreviewMode(t, true, false)

	err := runBootstrapPlan(cmd, func(plan *bootstrapPlan) error {
		return plan.writeFile(path, []byte("new policy\n"), 0o644, "install policy")
	})
	if err != nil {
		t.Fatalf("runBootstrapPlan: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".engram", "mem.db")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote global DB: %v", err)
	}
	got := out.String()
	for _, want := range []string{"[create] file " + path + ": install policy", "--- /dev/null", "+++ " + path, "+new policy", "Dry run: no changes applied."} {
		if !strings.Contains(got, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, got)
		}
	}
}

func TestBootstrapDiffRejectsAndAcceptsCompletePlan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		answer    string
		wantWrite bool
		message   string
	}{
		{name: "reject", answer: "n\n", message: "Rejected: no changes applied."},
		{name: "accept", answer: "yes\n", wantWrite: true, message: "applied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(home, "policy.md")
			cmd, out := bootstrapPlanTestCommand(tc.answer)
			setBootstrapPreviewMode(t, false, true)

			err := runBootstrapPlan(cmd, func(plan *bootstrapPlan) error {
				return plan.writeFile(path, []byte("accepted\n"), 0o644, "install policy")
			})
			if err != nil {
				t.Fatalf("runBootstrapPlan: %v", err)
			}
			_, err = os.Stat(path)
			if tc.wantWrite && err != nil {
				t.Fatalf("accepted plan did not write target: %v", err)
			}
			if !tc.wantWrite && !os.IsNotExist(err) {
				t.Fatalf("rejected plan wrote target: %v", err)
			}
			if !strings.Contains(out.String(), tc.message) {
				t.Errorf("output missing %q:\n%s", tc.message, out.String())
			}
		})
	}
}

func TestBootstrapPlanRefusesStaleReviewedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.md")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := newBootstrapPlan()
	if err := plan.writeFile(path, []byte("planned\n"), 0o644, "update policy"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed meanwhile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := plan.apply(context.Background()); err == nil || !strings.Contains(err.Error(), "changed after preview") {
		t.Fatalf("apply stale plan error = %v", err)
	}
}

func TestBootstrapGlobalScope(t *testing.T) {
	for _, tc := range []struct {
		name        string
		globalFlag  bool
		projectFlag bool
		wantGlobal  bool
		wantErr     bool
	}{
		{name: "default is global", wantGlobal: true},
		{name: "compatibility global flag", globalFlag: true, wantGlobal: true},
		{name: "explicit project", projectFlag: true},
		{name: "conflicting flags", globalFlag: true, projectFlag: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bootstrapGlobalScope(tc.globalFlag, tc.projectFlag)
			if (err != nil) != tc.wantErr {
				t.Fatalf("bootstrapGlobalScope error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.wantGlobal {
				t.Errorf("bootstrapGlobalScope = %v, want %v", got, tc.wantGlobal)
			}
		})
	}
}

func TestCodexBootstrapScopeTargets(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	oldCWD := rootCWD
	oldGlobal, oldProject := bootstrapCodexGlobal, bootstrapCodexProject
	rootCWD = project
	setBootstrapPreviewMode(t, true, false)
	t.Cleanup(func() {
		rootCWD = oldCWD
		bootstrapCodexGlobal, bootstrapCodexProject = oldGlobal, oldProject
	})

	for _, tc := range []struct {
		name        string
		projectFlag bool
		wantPath    string
		notPath     string
	}{
		{
			name:     "global by default",
			wantPath: filepath.Join(home, ".codex", "AGENTS.md"),
			notPath:  filepath.Join(project, "AGENTS.md"),
		},
		{
			name:        "project when explicit",
			projectFlag: true,
			wantPath:    filepath.Join(project, "AGENTS.md"),
			notPath:     filepath.Join(home, ".codex", "AGENTS.md"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bootstrapCodexGlobal = false
			bootstrapCodexProject = tc.projectFlag
			cmd, out := bootstrapPlanTestCommand("")
			if err := runBootstrapCodex(cmd, nil); err != nil {
				t.Fatalf("runBootstrapCodex: %v", err)
			}
			if !strings.Contains(out.String(), "file "+tc.wantPath) {
				t.Errorf("preview does not target %s:\n%s", tc.wantPath, out.String())
			}
			if strings.Contains(out.String(), "file "+tc.notPath) {
				t.Errorf("preview unexpectedly targets %s:\n%s", tc.notPath, out.String())
			}
			if _, err := os.Stat(tc.wantPath); !os.IsNotExist(err) {
				t.Errorf("dry-run wrote %s: %v", tc.wantPath, err)
			}
		})
	}
}

func bootstrapPlanTestCommand(input string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(input))
	return cmd, &out
}

func setBootstrapPreviewMode(t *testing.T, dryRun, diff bool) {
	t.Helper()
	oldDryRun, oldDiff := bootstrapDryRun, bootstrapDiff
	bootstrapDryRun, bootstrapDiff = dryRun, diff
	t.Cleanup(func() {
		bootstrapDryRun, bootstrapDiff = oldDryRun, oldDiff
	})
}
