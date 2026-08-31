package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

func TestLimitSearchResults(t *testing.T) {
	results := []engram.Memory{{Key: "first"}, {Key: "second"}, {Key: "third"}}

	got, omitted, err := limitSearchResults(results, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "first" || got[1].Key != "second" {
		t.Fatalf("limited results = %+v, want first two ranked matches", got)
	}
	if omitted != 1 {
		t.Errorf("omitted = %d, want 1", omitted)
	}

	got, omitted, err = limitSearchResults(results, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(results) || omitted != 0 {
		t.Errorf("zero limit returned %d results with %d omitted, want all %d", len(got), omitted, len(results))
	}

	if _, _, err := limitSearchResults(results, -1); err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("negative limit error = %v, want a non-negative validation error", err)
	}
}

func TestSearchOutputIsCompactUnlessFull(t *testing.T) {
	results := []engram.Memory{{
		Tier: engram.TierLong, Key: "deployment", Tldr: "Rollback procedure", Content: "complete body that should stay hidden",
	}}

	var compact bytes.Buffer
	printMemorySummaries(&compact, results, false)
	if got := compact.String(); !strings.Contains(got, "engram:long/deployment") || !strings.Contains(got, "Rollback procedure") {
		t.Errorf("compact search output = %q, want address and tldr", got)
	} else if strings.Contains(got, "complete body") {
		t.Errorf("compact search output leaked the full body: %q", got)
	}

	var full bytes.Buffer
	printFullMemorySearchResults(&full, results, false)
	if got := full.String(); !strings.Contains(got, "complete body that should stay hidden") {
		t.Errorf("full search output = %q, want complete body", got)
	}
}

func TestSearchOmissionNoticeStaysOffStdout(t *testing.T) {
	var stderr bytes.Buffer
	reportOmittedSearchResults(&stderr, 10, 3)
	got := stderr.String()
	for _, want := range []string{"showing 10", "3 omitted", "--limit 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("omission notice = %q, want %q", got, want)
		}
	}

	stderr.Reset()
	reportOmittedSearchResults(&stderr, 3, 0)
	if stderr.Len() != 0 {
		t.Errorf("complete result set produced an omission notice: %q", stderr.String())
	}
}

func TestSearchFlagDefaults(t *testing.T) {
	for name, command := range map[string]*cobra.Command{
		"mem search":   memSearchCmd,
		"skill search": skillSearchCmd,
	} {
		if got := command.Flag("limit").DefValue; got != "0" {
			t.Errorf("%s --limit default = %q, want 0 (all results)", name, got)
		}
		if command.Flag("full") == nil {
			t.Errorf("%s has no --full flag", name)
		}
	}
}
