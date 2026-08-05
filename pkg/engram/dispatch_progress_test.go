package engram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDispatchProgressRoundTripAndStaleReaping(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	live := DispatchProgress{
		PID: os.Getpid(), StartedAt: time.Now().Add(-90 * time.Second).UnixMilli(),
		Total: 8, Running: 3, Completed: 4, Failed: 1,
	}
	PublishDispatchProgress(live)

	// A SIGKILLed supervisor cannot clean up after itself by definition, so a file
	// for a dead pid is expected residue. Detecting it by liveness rather than age is
	// why these are keyed by pid.
	PublishDispatchProgress(DispatchProgress{PID: 999999, StartedAt: 1, Total: 3})

	batches := ReadDispatchProgress()
	if len(batches) != 1 || batches[0].PID != live.PID {
		t.Fatalf("expected only the live batch, got %+v", batches)
	}
	if _, err := os.Stat(filepath.Join(progressDir(), "999999.json")); !os.IsNotExist(err) {
		t.Error("a progress file for a dead pid was not reaped")
	}

	segment := FormatDispatchProgress(batches, time.Now())
	for _, want := range []string{"4/8", "1✗", "1m3"} {
		if !strings.Contains(segment, want) {
			t.Errorf("segment %q missing %q", segment, want)
		}
	}

	ClearDispatchProgress(live.PID)
	if got := ReadDispatchProgress(); len(got) != 0 {
		t.Errorf("progress survived Clear: %+v", got)
	}
	// Nothing running means no segment: a permanent "0/0" would be noise.
	if segment := FormatDispatchProgress(nil, time.Now()); segment != "" {
		t.Errorf("expected no segment when idle, got %q", segment)
	}
}

func TestDispatchProgressIgnoresAMalformedFile(t *testing.T) {
	// Opposite failure rule from a provider spec: a malformed spec must fail loudly
	// because dispatch parses it at the point of action, while a malformed progress
	// file must fail silently because it is decoration and a broken status line is
	// worse than an absent one.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if err := os.MkdirAll(progressDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(progressDir(), "garbage.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadDispatchProgress(); len(got) != 0 {
		t.Errorf("a malformed file produced output: %+v", got)
	}
	if segment := FormatDispatchProgress(ReadDispatchProgress(), time.Now()); segment != "" {
		t.Errorf("a malformed file reached the status line: %q", segment)
	}
}
