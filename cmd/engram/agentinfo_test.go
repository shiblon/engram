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
