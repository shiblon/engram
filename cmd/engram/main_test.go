package main

import (
	"strings"
	"testing"
)

func TestRootCommandAlwaysCarriesVersion(t *testing.T) {
	want := engramVersion()
	if rootCmd.Version != want {
		t.Errorf("root version = %q, want %q", rootCmd.Version, want)
	}
	if !strings.Contains(rootCmd.Long, "Engram version "+want) {
		t.Errorf("root help does not show version %q", want)
	}
}
