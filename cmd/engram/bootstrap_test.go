package main

import "testing"

// The session-protocol block lives in one constant (engramProtocolSection,
// written by every markdown-init-file bootstrap) and is removed by one regex
// (engramSectionRE, used by every corresponding uninstall). They live in
// separate files and must not drift: a regex that no longer matches what
// bootstrap wrote would silently leave the section behind on uninstall.
func TestUninstallRegexMatchesBootstrapSection(t *testing.T) {
	// bootstrap appends the section followed by a newline.
	written := engramProtocolSection + "\n"

	if !engramSectionRE.MatchString(written) {
		t.Fatalf("engramSectionRE does not match what bootstrap writes:\n%q", written)
	}

	// Removing the matched section from a realistic file must leave the rest
	// intact and drop the whole block.
	file := "# My init file\n\nsome existing content.\n" + written
	got := engramSectionRE.ReplaceAllString(file, "")
	if got != "# My init file\n\nsome existing content.\n" {
		t.Errorf("uninstall removal left unexpected residue:\n%q", got)
	}
}
