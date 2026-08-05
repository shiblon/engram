package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiblon/engram/pkg/engram"
)

// runDispatchCLI executes the root command with args, returning stdout and stderr
// separately. Keeping them separate is the point of the test: status is a machine
// stream and human output must never interleave with it.
func runDispatchCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

// isolatedHome points HOME at a temp dir so a test never reads or writes the
// developer's real global memory.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestDispatchDryRunResolvesArgvWithoutSpawning(t *testing.T) {
	isolatedHome(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "batch.json")

	// The seed spec for claude resolves even with no memory database, so a dry run
	// is the zero-cost way to see exactly what would be spawned.
	config := `{
		"v": 1,
		"max_concurrent": 2,
		"defaults": {"provider": "claude", "authority": "read-only"},
		"tasks": [
			{"id": "slice-1", "prompt": "review pkg/a", "model": "cheap"},
			{"id": "whole", "prompt": "review the whole change at a higher altitude", "model": "strong"}
		]
	}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runDispatchCLI(t, "dispatch", "run", "--config", configPath, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	events, err := engram.ParseDispatchEvents(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("stdout was not a clean JSON Lines stream: %v\n%s", err, stdout)
	}

	byTask := map[string][]string{}
	var sawBatchDone bool
	for _, event := range events {
		switch event.Type {
		case engram.EventTaskStart:
			byTask[event.Task] = event.Argv
		case engram.EventBatchDone:
			sawBatchDone = true
			if event.State != engram.BatchStateOK {
				t.Errorf("dry run reported state %q", event.State)
			}
		}
	}
	if !sawBatchDone {
		t.Fatal("stream did not end with an authoritative batch_done")
	}
	if len(byTask) != 2 {
		t.Fatalf("expected argv for both tasks, got %v", byTask)
	}
	slice := strings.Join(byTask["slice-1"], " ")
	for _, want := range []string{
		"claude",
		"--output-format json",
		// A role name must resolve to a FULL model id. Measured 2026-08-05: asking
		// claude for the alias "haiku" silently ran claude-sonnet-5 with a clean
		// exit, so a config that names a raw alias is a silent-substitution hazard.
		"--model claude-haiku-4-5-20251001",
		// read-only is dontAsk plus a write-tool denylist, NOT plan mode. Canaried
		// 2026-08-05: plan mode does not withhold writes, it redirects the child into
		// writing a plan file and returning a planning stub instead of the work.
		"--permission-mode dontAsk",
		"--disallowedTools Edit Write NotebookEdit",
	} {
		if !strings.Contains(slice, want) {
			t.Errorf("resolved argv missing %q:\n%s", want, slice)
		}
	}
	// Suppression is --setting-sources with an EMPTY value, not --bare. Measured
	// 2026-08-05: --bare refuses OAuth credentials outright, while emptying the
	// sources keeps them and cuts cache-creation tokens from 36,888 to 3,685. The
	// empty value matters -- "local" still loads .claude/settings.local.json, so it
	// reduces the child's config where the empty form determines it. Asserted
	// against the argv slice because an empty element vanishes in a joined string.
	if !hasFlagWithValue(byTask["slice-1"], "--setting-sources", "") {
		t.Errorf("argv does not empty the setting sources:\n%#v", byTask["slice-1"])
	}
	if strings.Contains(slice, "--permission-mode plan") {
		t.Errorf("plan mode writes plan files and returns a stub; it is not read-only:\n%s", slice)
	}
	if strings.Contains(slice, "--bare") {
		t.Errorf("--bare is auth-hostile on an OAuth machine and must not be the suppression flag:\n%s", slice)
	}
	if whole := strings.Join(byTask["whole"], " "); !strings.Contains(whole, "--model claude-opus-5") {
		t.Errorf("the strong role did not resolve to a full model id:\n%s", whole)
	}
	// The prompt rides stdin on this spec, so it must not appear in argv.
	if strings.Contains(slice, "review pkg/a") {
		t.Errorf("prompt leaked into argv on a stdin-transport spec:\n%s", slice)
	}
}

func TestDispatchRunRejectsAnInvalidConfigBeforeSpendingAnything(t *testing.T) {
	isolatedHome(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "batch.json")
	if err := os.WriteFile(configPath, []byte(`{"v":1,"tasks":[{"id":"a","prompt":"p"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := runDispatchCLI(t, "dispatch", "run", "--config", configPath, "--dry-run")
	if err == nil {
		t.Fatal("expected a task with no provider to be refused")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Fatalf("error should name the missing field; got %v", err)
	}
}

func TestDispatchSpecShowFallsBackToTheShippedSeed(t *testing.T) {
	isolatedHome(t)
	stdout, stderr, err := runDispatchCLI(t, "dispatch", "spec", "show", "claude")
	if err != nil {
		t.Fatal(err)
	}
	var spec engram.ProviderSpec
	if err := json.Unmarshal([]byte(stdout), &spec); err != nil {
		t.Fatalf("stdout was not the bare spec JSON: %v\n%s", err, stdout)
	}
	if spec.Provider != "claude" {
		t.Errorf("provider = %q", spec.Provider)
	}
	// The origin belongs on stderr: a caller piping stdout into jq should get only
	// the document.
	if !strings.Contains(stderr, "seed") {
		t.Errorf("stderr should say the spec came from the shipped seed; got %q", stderr)
	}
}

func TestDispatchSpecPutValidatesAndStores(t *testing.T) {
	isolatedHome(t)

	spec := `{
		"v": 2,
		"provider": "toy",
		"executable": "toy-cli",
		"prompt": {"transport": "stdin"},
		"model": {"argv": ["--model", "{{model}}"]},
		"result": {"format": "json", "json_path": "answer"},
		"version": {"argv": ["--version"]}
	}`
	stdin := strings.NewReader(spec)
	rootCmd.SetIn(stdin)
	t.Cleanup(func() { rootCmd.SetIn(nil) })

	stdout, _, err := runDispatchCLI(t, "dispatch", "spec", "put", "toy", "--from", "-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "dispatch-spec-toy") {
		t.Fatalf("put did not report where the spec landed: %q", stdout)
	}

	// It must come back through the normal memory read path, since repair is
	// supposed to be an ordinary `engram mem edit`.
	shown, _, err := runDispatchCLI(t, "dispatch", "spec", "show", "toy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown, `"toy-cli"`) {
		t.Fatalf("stored spec did not come back: %q", shown)
	}
}

func TestDispatchSpecPutRefusesAMismatchedProvider(t *testing.T) {
	isolatedHome(t)
	rootCmd.SetIn(strings.NewReader(
		`{"v":2,"provider":"toy","executable":"t","prompt":{"transport":"stdin"},"result":{"format":"text"}}`))
	t.Cleanup(func() { rootCmd.SetIn(nil) })

	_, _, err := runDispatchCLI(t, "dispatch", "spec", "put", "other", "--from", "-")
	if err == nil {
		t.Fatal("expected a spec whose provider disagrees with the command to be refused")
	}
}

func TestDispatchSpecValidateReportsABrokenDocument(t *testing.T) {
	isolatedHome(t)
	rootCmd.SetIn(strings.NewReader(
		`{"v":2,"provider":"toy","executable":"t","prompt":{"transport":"stdin"},
		  "model":{"argv":["--model"]},"result":{"format":"text"}}`))
	t.Cleanup(func() { rootCmd.SetIn(nil) })

	_, _, err := runDispatchCLI(t, "dispatch", "spec", "validate", "--from", "-")
	if err == nil {
		t.Fatal("expected a model fragment with no placeholder to fail validation")
	}
	if !strings.Contains(err.Error(), "{{model}}") {
		t.Fatalf("the error should name the missing placeholder; got %v", err)
	}
}

func TestDispatchSpecListNamesSeedsAsUnprobed(t *testing.T) {
	isolatedHome(t)
	stdout, _, err := runDispatchCLI(t, "dispatch", "spec", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"claude", "codex", "seed", "unprobed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("spec list missing %q:\n%s", want, stdout)
		}
	}
}

func TestDispatchStaysOutOfTheBootstrapAllowlist(t *testing.T) {
	// Dispatch spends tokens and spawns processes, so it belongs with save and
	// restore rather than with the routine curation verb. A permission prompt on a
	// consequential, infrequent operation is appropriate friction.
	data, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "engram dispatch") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "allow") || strings.Contains(lower, "permission") {
			t.Errorf("dispatch appears to be pre-approved in bootstrap: %s", strings.TrimSpace(line))
		}
	}
}

// hasFlagWithValue reports whether argv contains flag immediately followed by
// value. Needed because an empty argv element disappears when the vector is
// joined for display, and "the value is empty" is exactly what some assertions
// are about.
func hasFlagWithValue(argv []string, flag, value string) bool {
	for i, element := range argv {
		if element == flag && i+1 < len(argv) && argv[i+1] == value {
			return true
		}
	}
	return false
}
