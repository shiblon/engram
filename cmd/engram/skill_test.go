package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shiblon/engram/pkg/engram"
)

func TestAdoptedSkillPreservesUnspecifiedFields(t *testing.T) {
	existing := &engram.Memory{
		ID:        42,
		TS:        1234,
		Tier:      engram.TierLong,
		Key:       "standup",
		Content:   "the full verified procedure",
		Tldr:      "the existing outcome",
		SessionID: "preserve-even-if-unusual",
	}

	got, err := adoptedSkill(existing, "  When asked for a standup  ", false, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if got.Trigger != "When asked for a standup" {
		t.Errorf("trigger = %q", got.Trigger)
	}
	if got.Tldr != existing.Tldr || got.Content != existing.Content || got.TS != existing.TS || got.ID != existing.ID || got.SessionID != existing.SessionID {
		t.Errorf("adoption changed unspecified fields: got %+v, existing %+v", got, existing)
	}

	got, err = adoptedSkill(existing, "When asked for a standup", true, "  replacement outcome  ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tldr != "replacement outcome" {
		t.Errorf("explicit tldr = %q", got.Tldr)
	}
}

func TestAdoptedSkillRequiresExistingLongMemoryAndTrigger(t *testing.T) {
	if _, err := adoptedSkill(nil, "When needed", false, ""); err == nil {
		t.Error("nil memory should fail")
	}
	short := &engram.Memory{Tier: engram.TierShort, Key: "task"}
	if _, err := adoptedSkill(short, "When needed", false, ""); err == nil {
		t.Error("short memory should fail")
	}
	long := &engram.Memory{Tier: engram.TierLong, Key: "task"}
	if _, err := adoptedSkill(long, "  ", false, ""); err == nil {
		t.Error("empty trigger should fail")
	}
}

func TestClassifyAutomationInputInfersCommandAndResolvesRemoval(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "scripts", "check.sh")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidates, err := engram.DiscoverAutomation(root)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]engram.AutomationCandidate{candidates[0].Path: candidates[0]}
	db, err := engram.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	input := skillClassificationInput{
		Path: "scripts/check.sh", Classification: "direct-tool", Rationale: "validation gate",
	}
	if err := classifyAutomationInput(ctx, db, root, byPath, input); err != nil {
		t.Fatal(err)
	}
	entries, err := engram.ListAutomationCatalog(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Invocation != "bash scripts/check.sh" {
		t.Fatalf("classified entries = %+v", entries)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := classifyAutomationInput(ctx, db, root, map[string]engram.AutomationCandidate{}, skillClassificationInput{
		Path: "scripts/check.sh", Classification: "removed",
	}); err != nil {
		t.Fatal(err)
	}
	entries, err = engram.ListAutomationCatalog(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("removed classification survived: %+v", entries)
	}
}
