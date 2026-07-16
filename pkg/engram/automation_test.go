package engram

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAutomationFixture(t *testing.T, root, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func candidateByPath(t *testing.T, candidates []AutomationCandidate, path string) AutomationCandidate {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.Path == path {
			return candidate
		}
	}
	t.Fatalf("candidate not found: %s", path)
	return AutomationCandidate{}
}

func TestDiscoverAutomationFingerprintsEachCandidate(t *testing.T) {
	root := t.TempDir()
	writeAutomationFixture(t, root, "Makefile", "check:\n\tgo test ./...\n", 0o644)
	writeAutomationFixture(t, root, "scripts/check-go.sh", "#!/bin/sh\ngo test ./...\n", 0o644)
	writeAutomationFixture(t, root, "scripts/release", "#!/usr/bin/env bash\n", 0o644)
	writeAutomationFixture(t, root, "bin/render", "render\n", 0o755)
	writeAutomationFixture(t, root, "scripts/README.md", "documentation\n", 0o644)
	writeAutomationFixture(t, root, "scripts/.hidden.sh", "#!/bin/sh\n", 0o755)

	got, err := DiscoverAutomation(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ path, kind string }{
		{"Makefile", "task runner"},
		{"bin/render", "script"},
		{"scripts/check-go.sh", "script"},
		{"scripts/release", "script"},
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %#v, want %d", got, len(want))
	}
	for i, candidate := range got {
		if candidate.Path != want[i].path || candidate.Kind != want[i].kind || candidate.Digest == "" {
			t.Errorf("candidate %d = %#v, want path=%q kind=%q and digest", i, candidate, want[i].path, want[i].kind)
		}
	}
	before := candidateByPath(t, got, "scripts/check-go.sh").Digest
	writeAutomationFixture(t, root, "scripts/check-go.sh", "#!/bin/sh\ngo test -race ./...\n", 0o644)
	changed, err := DiscoverAutomation(root)
	if err != nil {
		t.Fatal(err)
	}
	if candidateByPath(t, changed, "scripts/check-go.sh").Digest == before {
		t.Error("candidate digest did not change with its content")
	}
	if candidateByPath(t, changed, "Makefile").Digest != candidateByPath(t, got, "Makefile").Digest {
		t.Error("unrelated candidate digest changed")
	}
}

func TestAutomationCatalogReconcilesPerCandidate(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	candidates := []AutomationCandidate{
		{Path: "scripts/a.sh", Kind: "script", Digest: "a1"},
		{Path: "scripts/b.sh", Kind: "script", Digest: "b1"},
	}

	review, err := ReconcileAutomationCatalog(ctx, db, candidates, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Items) != 2 || review.Items[0].State != AutomationNew || review.Items[1].State != AutomationNew {
		t.Fatalf("initial review = %+v", review.Items)
	}
	if err := ClassifyAutomation(ctx, db, candidates[0], AutomationDirectTool, "validation gate", "", "bash scripts/a.sh"); err != nil {
		t.Fatal(err)
	}
	if err := ClassifyAutomation(ctx, db, candidates[1], AutomationSkillMember, "release helper", "release", ""); err != nil {
		t.Fatal(err)
	}
	review, err = ReconcileAutomationCatalog(ctx, db, candidates, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Items) != 0 {
		t.Fatalf("unchanged classified candidates should be silent: %+v", review.Items)
	}

	changed := []AutomationCandidate{
		{Path: "scripts/a.sh", Kind: "script", Digest: "a2"},
		candidates[1],
	}
	review, err = ReconcileAutomationCatalog(ctx, db, changed, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Items) != 1 || review.Items[0].State != AutomationChanged {
		t.Fatalf("changed review = %+v", review.Items)
	}
	previous := review.Items[0].Previous
	if previous == nil || previous.Classification != AutomationDirectTool || previous.Rationale != "validation gate" {
		t.Fatalf("changed entry lost prior judgment: %+v", previous)
	}

	review, err = ReconcileAutomationCatalog(ctx, db, changed[:1], false)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Items) != 2 || review.Items[1].State != AutomationRemoved || review.Items[1].Candidate.Path != "scripts/b.sh" {
		t.Fatalf("removed review = %+v", review.Items)
	}
}

func TestAutomationClassificationValidationAndProjectTools(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	root := t.TempDir()
	writeAutomationFixture(t, root, "scripts/check.sh", "#!/bin/sh\n", 0o644)
	candidates, err := DiscoverAutomation(root)
	if err != nil {
		t.Fatal(err)
	}
	candidate := candidateByPath(t, candidates, "scripts/check.sh")
	command, err := InferAutomationInvocation(root, candidate)
	if err != nil || command != "bash scripts/check.sh" {
		t.Fatalf("inferred command = %q, err=%v", command, err)
	}
	if err := ClassifyAutomation(ctx, db, candidate, AutomationDirectTool, "run checks", "not-allowed", command); err == nil {
		t.Error("non-skill classification accepted a skill key")
	}
	if err := ClassifyAutomation(ctx, db, candidate, AutomationDirectTool, "run checks", "", command); err != nil {
		t.Fatal(err)
	}
	entries, err := ListAutomationCatalog(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	tools, warnings := ProjectToolsFromCatalog(root, entries)
	if len(warnings) != 0 || len(tools) != 1 {
		t.Fatalf("tools=%+v warnings=%v", tools, warnings)
	}
	if tools[0].Command() != command || !strings.Contains(tools[0].Desc, "checks") {
		t.Errorf("project tool = %+v", tools[0])
	}
	if active := ActiveAutomationCatalogEntries(entries, []AutomationCandidate{{Path: candidate.Path, Kind: candidate.Kind, Digest: "changed"}}); len(active) != 0 {
		t.Errorf("changed candidate remained active: %+v", active)
	}
	removed, err := RemoveAutomationClassification(ctx, db, candidate.Path)
	if err != nil || !removed {
		t.Fatalf("remove classification: removed=%v err=%v", removed, err)
	}
}
