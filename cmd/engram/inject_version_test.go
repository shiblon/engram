package main

import (
	"strings"
	"testing"
)

func TestInjectVersionLine(t *testing.T) {
	got := injectVersionLine("v1.2.3")
	if !strings.Contains(got, "v1.2.3") {
		t.Errorf("injectVersionLine = %q, want it to contain the version", got)
	}
	if !strings.Contains(got, "engram version") {
		t.Errorf("injectVersionLine = %q, want a labeled version line", got)
	}
	// The line must carry the drift-check instruction itself, so it fires even
	// when the loaded policy kernel is old and version-less (predates this feature).
	if !strings.Contains(got, "Guidance version") {
		t.Errorf("injectVersionLine = %q, want it to reference the guidance's Guidance version line", got)
	}
	if !strings.Contains(got, "no version") {
		t.Errorf("injectVersionLine = %q, want it to handle a version-less (old) guidance file", got)
	}
	if !strings.Contains(got, "engram bootstrap") {
		t.Errorf("injectVersionLine = %q, want it to recommend engram bootstrap on drift", got)
	}
	if !strings.Contains(got, "engram agentinfo") {
		t.Errorf("injectVersionLine = %q, want it to route absent guidance to agentinfo", got)
	}
}
