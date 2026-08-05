package engram

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGuessSubcommandsOnlyReadsTheCommandSection(t *testing.T) {
	// Walking subcommand help is mandatory for a learner, because top-level help
	// misleads: codex documents -a/--ask-for-approval and `codex exec` rejects it.
	// The guess is a convenience, so it must not turn flag prose into spawns.
	help := `Usage: codex [OPTIONS] [PROMPT]

Commands:
  exec       Run non-interactively
  login      Manage login
  mcp        Model Context Protocol
  help       Print this message

Options:
  -m, --model <MODEL>   Model to use
      --sandbox <MODE>  read-only, workspace-write
  -p, --profile <NAME>  Configuration profile
`
	got := guessSubcommands(help)
	want := []string{"exec", "login", "mcp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("guessed %#v, want %#v", got, want)
	}
}

func TestGuessSubcommandsHandlesHelpWithNoCommands(t *testing.T) {
	help := `Usage: claude [options] [prompt]

Options:
  -p, --print   Print response and exit
      --model   Model for the session
`
	if got := guessSubcommands(help); len(got) != 0 {
		t.Fatalf("a flags-only help page should yield no subcommands, got %#v", got)
	}
}

func TestHelpDigestChangesWithTheText(t *testing.T) {
	first := HelpDigest("usage: tool --model M")
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("digest is not labeled with its algorithm: %q", first)
	}
	if first == HelpDigest("usage: tool -m M") {
		t.Fatal("a moved flag must change the digest, or drift is undetectable")
	}
	if first != HelpDigest("usage: tool --model M") {
		t.Fatal("the digest must be stable for identical text")
	}
}

func TestApplyProbeMovesVerifiedFieldsOutOfInferred(t *testing.T) {
	spec := minimalSpec()
	spec.Provenance.InferredFields = []string{"model", "prompt", "result", "budget"}
	spec.Provenance.Seed = true

	at := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	updated := ApplyProbe(spec, ProbeResult{
		Version:        "3.0.0",
		SmokeOK:        true,
		RequestedModel: "cheap",
		ReportedModel:  "cheap-1",
		ModelVerified:  true,
		ExitCode:       0,
	}, at)

	if updated.Provenance.Seed {
		t.Error("a probed spec is no longer a shipped guess")
	}
	if updated.Provenance.LearnedVersion != "3.0.0" {
		t.Errorf("learned version = %q", updated.Provenance.LearnedVersion)
	}
	if updated.Provenance.Probe == nil || !updated.Provenance.Probe.ModelVerified {
		t.Fatalf("probe record missing or not recorded as verified: %+v", updated.Provenance.Probe)
	}
	for _, field := range []string{"model", "prompt", "result"} {
		if !containsString(updated.Provenance.VerifiedFields, field) {
			t.Errorf("%s should be verified after a successful probe", field)
		}
		if containsString(updated.Provenance.InferredFields, field) {
			t.Errorf("%s should no longer be inferred", field)
		}
	}
	// A field the probe said nothing about stays inferred.
	if !containsString(updated.Provenance.InferredFields, "budget") {
		t.Error("budget was never probed, so it must remain inferred")
	}
	// The original must be untouched: a probe annotates a clone so a concurrent
	// reader never sees a half-updated spec.
	if !spec.Provenance.Seed {
		t.Error("ApplyProbe mutated the spec it was given")
	}
}

func TestApplyProbeMarksAnUnhonoredModelUntrusted(t *testing.T) {
	spec := minimalSpec()
	spec.Provenance.VerifiedFields = []string{"model"}
	updated := ApplyProbe(spec, ProbeResult{
		RequestedModel: "strong",
		ReportedModel:  "default-model",
		ModelVerified:  false,
		FlagLiveness:   "silently-accepted-invalid-model",
	}, time.Now())

	if containsString(updated.Provenance.VerifiedFields, "model") {
		t.Error("a model that was not honored must lose its verified claim")
	}
	if !containsString(updated.Provenance.InferredFields, "model") {
		t.Error("an unhonored model must be recorded as inferred and untrusted")
	}
	if updated.Provenance.Probe.FlagLiveness != "silently-accepted-invalid-model" {
		t.Errorf("flag liveness finding was dropped: %+v", updated.Provenance.Probe)
	}
}
