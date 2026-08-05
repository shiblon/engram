package engram

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Nothing invoked ProbeSpec before these tests: coverage stopped at pure helpers
// like ApplyProbe, so the smoke run, the output extraction, and the invalid-model
// fallback were untested together -- the exact workflow in which five bugs were
// found by hand.

func TestProbeSpecVerifiesAModelTheProviderHonors(t *testing.T) {
	spec := fakeSpec(t, "ok")
	probe, err := ProbeSpec(context.Background(), spec, ProbeOptions{
		Model:          "fake-model-1",
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !probe.SmokeOK {
		t.Fatalf("smoke failed: exit=%d notes=%v stderr=%q", probe.ExitCode, probe.Notes, probe.Stderr)
	}
	if !probe.ModelVerified {
		t.Errorf("an honored model was not verified: reported %q, notes %v", probe.ReportedModel, probe.Notes)
	}
	if probe.FlagLiveness != "" {
		t.Errorf("liveness probe ran despite a positive report: %q", probe.FlagLiveness)
	}
}

func TestProbeSpecCatchesASilentModelSubstitution(t *testing.T) {
	// The failure this whole phase exists for: the flag is ignored, the run succeeds,
	// and the answer looks entirely plausible.
	spec := fakeSpec(t, "ok")
	spec.Model = &ArgvFragment{Argv: []string{"--profile", PlaceholderModel}} // the fake ignores --profile
	probe, err := ProbeSpec(context.Background(), spec, ProbeOptions{
		Model:          "fake-model-1",
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !probe.SmokeOK {
		t.Fatal("the run itself should succeed; that is what makes the substitution silent")
	}
	if probe.ModelVerified {
		t.Fatal("a substituted model was reported as verified")
	}
	if !strings.Contains(strings.Join(probe.Notes, " "), "untrusted") {
		t.Errorf("the finding should say the model field is untrusted: %v", probe.Notes)
	}
}

func TestProbeSpecSkipsLivenessWhenTheBaselineFailed(t *testing.T) {
	// With a broken login both the valid and the invalid model exit nonzero, so
	// reading the second failure as evidence about the model flag is how a bad
	// login got recorded as "the flag is live".
	spec := fakeSpec(t, "run-failure")
	probe, err := ProbeSpec(context.Background(), spec, ProbeOptions{
		Model:          "fake-model-1",
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if probe.SmokeOK {
		t.Fatal("this spec is configured to fail")
	}
	if probe.FlagLiveness != "inconclusive-baseline-failed" {
		t.Errorf("liveness = %q, want it reported inconclusive", probe.FlagLiveness)
	}
	if !strings.Contains(strings.Join(probe.Notes, " "), "prove nothing") {
		t.Errorf("notes should explain why liveness was skipped: %v", probe.Notes)
	}
}

func TestProbeSpecDoesNotRetireTheSeedFlagOnFailure(t *testing.T) {
	spec := fakeSpec(t, "run-failure")
	spec.Provenance.Seed = true
	probe, err := ProbeSpec(context.Background(), spec, ProbeOptions{TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if updated := ApplyProbe(spec, probe, timeZero()); !updated.Provenance.Seed {
		t.Error("a failed probe retired the seed flag, promoting a guess on no evidence")
	}
}

// timeZero is a fixed clock so provenance timestamps stay deterministic.
func timeZero() time.Time { return time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC) }
