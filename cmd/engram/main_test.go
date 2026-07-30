package main

import (
	"os"
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

func TestResolveEngramVersion(t *testing.T) {
	tests := []struct {
		name         string
		buildVersion string
		want         string
	}{
		{"released module", "v9.8.7", "v9.8.7"},
		{"development module", "(devel)", sourceVersion},
		{"missing build info", "", sourceVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveEngramVersion(tt.buildVersion); got != tt.want {
				t.Errorf("resolveEngramVersion(%q) = %q, want %q",
					tt.buildVersion, got, tt.want)
			}
		})
	}
}

func TestSourceVersionMatchesLatestChangelogRelease(t *testing.T) {
	data, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	var latest string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## [") && line != "## [Unreleased]" {
			latest = line
			break
		}
	}
	wantPrefix := "## [" + strings.TrimPrefix(sourceVersion, "v") + "]"
	if !strings.HasPrefix(latest, wantPrefix) {
		t.Errorf("sourceVersion = %q but latest changelog release is %q", sourceVersion, latest)
	}
}
