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

func TestPolicyKernelCarriesVersionTriggersAndReferenceRoutes(t *testing.T) {
	got := engramProtocolSection("codex")
	for _, want := range []string{
		"Guidance version: " + engramVersion(),
		guidanceStartMarker,
		guidanceEndMarker,
		"WHEN: At task start",
		"DO: Scan the injected Skills index",
		"READ: Read `engram agentinfo skills`",
		"engram inject --text --agent codex",
		"`invariant`, `preference`, `long`, `short`, and `cold`",
		"never feed display-formatted `engram mem read` output",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("protocol guidance missing %q", want)
		}
	}
}

func TestPolicyKernelUsesTightFieldLists(t *testing.T) {
	got := engramProtocolSection("codex")
	for _, want := range []string{
		"- WHEN: At task start",
		"\n- DO: Scan the injected Skills index",
		"\n- READ: Read `engram agentinfo skills`",
		"\n- BOUNDARY: A no-results search",
		"Provider-specific inject command: `engram inject --text --agent codex`.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("compact policy kernel missing %q", want)
		}
	}
	for _, tooLoose := range []string{"\n\n- DO:", "\n\n- READ:", "\n\n- BOUNDARY:"} {
		if strings.Contains(got, tooLoose) {
			t.Errorf("policy kernel contains loose field spacing %q", tooLoose)
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

func TestGuidanceCarriesDispatchJudgmentInBothSurfaces(t *testing.T) {
	// The kernel recognizes the trigger and points to the topic; detailed judgment
	// lives once in topic-addressable reference rather than every init file.
	kernel := engramProtocolSection("codex")
	for _, want := range []string{"including `engram dispatch`", "engram agentinfo experiments"} {
		if !strings.Contains(kernel, want) {
			t.Errorf("kernel guidance missing %q", want)
		}
	}
	topic, _ := guidanceTopicByName("experiments")
	reference := renderAgentInfoTopic(topic, false, "")
	for _, want := range []string{
		"experimental: dispatch",
		"Slicing destroys the seams",
		"Fan-out amplifies false positives",
		"license silence",
		"higher altitude",
		"does not self-orient",
		"GET CONSENT FOR THE COST",
		"dispatch-spec-<provider>",
		"a misread model flag is silent",
	} {
		if !strings.Contains(reference, want) {
			t.Errorf("experiments reference missing %q", want)
		}
	}
}

func TestGuidanceWarnsAgainstWriteCapableDispatch(t *testing.T) {
	// The hang-or-bypass trap is the non-obvious part: a guardrail doing its job
	// looks like a broken child, so the pressure runs toward disabling it. That has
	// to reach the agent in words, not just as a default in the code.
	topic, _ := guidanceTopicByName("experiments")
	got := renderAgentInfoTopic(topic, false, "")
	for _, want := range []string{
		"READ-ONLY BY DEFAULT",
		"looks exactly like a hang",
		"BECAUSE the safety worked",
		"Nobody can interrupt",
		"lost update",
		"observe the side effects last",
		"produce a PATCH",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("experiments reference missing %q", want)
		}
	}
}

func TestAgentInfoIndexAndTopicViews(t *testing.T) {
	index := renderAgentInfoIndex()
	for _, want := range []string{"memory-workflow", "safe-memory-updates", "staged-restores"} {
		if !strings.Contains(index, want) {
			t.Errorf("agentinfo index missing %q", want)
		}
	}
	topic, ok := guidanceTopicByName("skills")
	if !ok {
		t.Fatal("skills topic not registered")
	}
	body := renderAgentInfoTopic(topic, false, "")
	if strings.Contains(body, "WHEN: At task start") {
		t.Errorf("plain topic unexpectedly includes kernel policy")
	}
	if !strings.Contains(body, "CAPTURE ON FIRST USE") {
		t.Errorf("plain topic missing operational reference")
	}
	full := renderAgentInfoTopic(topic, true, "codex")
	if !strings.Contains(full, "WHEN: At task start") || !strings.Contains(full, "CAPTURE ON FIRST USE") {
		t.Errorf("full topic does not combine kernel and reference")
	}
}

func TestEveryReferenceSectionIsRegisteredExactlyOnce(t *testing.T) {
	sections := referenceSections()
	for _, topic := range guidanceTopics() {
		body, ok := sections[topic.BodyHeading]
		if !ok || strings.TrimSpace(body) == "" {
			t.Errorf("topic %q has no reference body at heading %q", topic.Name, topic.BodyHeading)
		}
		delete(sections, topic.BodyHeading)
	}
	if len(sections) != 0 {
		t.Errorf("reference source has unregistered sections: %v", sections)
	}
}
