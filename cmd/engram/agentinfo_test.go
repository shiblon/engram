package main

import (
	"strings"
	"testing"
)

func TestRenderAgentInfoSubstitutesVersion(t *testing.T) {
	got := renderAgentInfo()
	if strings.Contains(got, "ENGRAM_GUIDANCE_VERSION") {
		t.Errorf("rendered guidance still contains the raw version token")
	}
	if !strings.Contains(got, engramVersion()) {
		t.Errorf("rendered guidance missing the substituted version %q", engramVersion())
	}
	// The drift instruction must survive rendering so agents know to act on a mismatch.
	if !strings.Contains(got, "predates the installed engram") {
		t.Errorf("rendered guidance missing the version-drift paragraph")
	}
}

func TestGuidanceDropsContextLongMd(t *testing.T) {
	got := renderAgentInfo()
	for _, gone := range []string{
		"auto-loads it when the file is newer",
		"NEVER edit context/long.md",
	} {
		if strings.Contains(got, gone) {
			t.Errorf("guidance still contains removed context/long.md prose: %q", gone)
		}
	}
	for _, want := range []string{
		"engram mem dump", // still the export helper
		"context/",        // the migration nudge references a stray context/ dir
		"no longer",       // auto-import no longer supported
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing expected phrase %q", want)
		}
	}
}
