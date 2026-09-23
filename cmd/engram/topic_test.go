package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTopicHelpKeepsUsageOnDemand(t *testing.T) {
	var out bytes.Buffer
	topicCmd.SetOut(&out)
	t.Cleanup(func() { topicCmd.SetOut(nil) })
	if err := topicCmd.Help(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"[experimental: topics]", "topic list", "read", "post", "compact", "check", "monitor"} {
		if !strings.Contains(got, want) {
			t.Errorf("topic help missing %q in %q", want, got)
		}
	}
	if strings.Count(got, "\n") > 40 {
		t.Errorf("topic help grew beyond lightweight routing (%d lines)", strings.Count(got, "\n"))
	}
}

func TestTopicCheckHelpIsImmediateAndNonBlocking(t *testing.T) {
	var out bytes.Buffer
	topicCheckCmd.SetOut(&out)
	t.Cleanup(func() { topicCheckCmd.SetOut(nil) })
	if err := topicCheckCmd.Help(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"once and exit immediately", "No output means", "never waits for a future change",
		"topic monitor", "--after",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("topic check help missing %q in %q", want, got)
		}
	}
}

func TestTopicMonitorHelpRequiresPersistentBackgroundUse(t *testing.T) {
	var out bytes.Buffer
	topicMonitorCmd.SetOut(&out)
	t.Cleanup(func() { topicMonitorCmd.SetOut(nil) })
	if err := topicMonitorCmd.Help(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"Continuously stream", "background process", "keep it running across turns",
		"Do not stop it after an event", "without waiting for another user prompt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("topic monitor help missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "--once") {
		t.Errorf("topic monitor help still advertises one-shot operation: %q", got)
	}
}

func TestParseTopicTarget(t *testing.T) {
	topic, subtopic, err := parseTopicTarget("graph-memory/allocator")
	if err != nil || topic != "graph-memory" || subtopic != "allocator" {
		t.Fatalf("parse = (%q, %q, %v)", topic, subtopic, err)
	}
	for _, bad := range []string{"graph-memory", "/allocator", "graph-memory/", "a/b/c"} {
		if _, _, err := parseTopicTarget(bad); err == nil {
			t.Errorf("parseTopicTarget(%q) succeeded", bad)
		}
	}
}
