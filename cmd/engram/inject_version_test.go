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
}
