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

func TestRenderAgentInfoWarnsAgainstMemoryReadbackWrites(t *testing.T) {
	got := renderAgentInfo()
	for _, want := range []string{
		"engram mem ... tldr <key>",
		"Do not feed `engram mem read` output back into `engram mem write`",
		"after a failed write",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered guidance missing %q", want)
		}
	}
}

func TestRenderAgentInfoExplainsCopyableMemoryAddresses(t *testing.T) {
	got := renderAgentInfo()
	for _, want := range []string{
		"engram:tier/key",
		"engram:/tier/key is global",
		"engram:/preference/@codex/key",
		"Bare keys plus --tier/--global/--agent remain supported",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered guidance missing %q", want)
		}
	}
}

func TestRenderAgentInfoExplainsLinkedWorktreeReadAccess(t *testing.T) {
	got := renderAgentInfo()
	for _, want := range []string{
		"Linked Git worktrees share the main checkout's Engram database",
		"request narrowly scoped filesystem",
		"retry once",
		"separate .engram directory in the linked worktree",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered guidance missing %q", want)
		}
	}
}

func TestProtocolGuidanceCarriesVersionAndSkillMetaTrigger(t *testing.T) {
	got := engramProtocolSection("codex")
	for _, want := range []string{
		"Guidance version: " + engramVersion(),
		"CLI tier tokens are fixed",
		"`--tier short`",
		"engram mem ... tldr <key>",
		"Do not feed `engram mem read` output back into `engram mem write`",
		"CAPTURE ON FIRST USE",
		"The injected Skills index is the retrieval mechanism",
		"trust the index over the search",
		"after removing",
		"Ask before writing it globally",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("protocol guidance missing %q", want)
		}
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
		"engram mem dump",           // still the export helper
		"Do not dump automatically", // only in the project memory and sharing section
		"no longer",                 // auto-import no longer supported
		"Do not recreate",           // pins the migration-nudge paragraph uniquely
	} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing expected phrase %q", want)
		}
	}
}
