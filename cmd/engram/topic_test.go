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
	for _, want := range []string{"[experimental: topics]", "topic list", "read", "post", "compact", "monitor"} {
		if !strings.Contains(got, want) {
			t.Errorf("topic help missing %q in %q", want, got)
		}
	}
	if strings.Count(got, "\n") > 40 {
		t.Errorf("topic help grew beyond lightweight routing (%d lines)", strings.Count(got, "\n"))
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
