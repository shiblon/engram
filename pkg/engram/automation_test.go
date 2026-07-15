package engram

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAutomation(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}

	write("Makefile", "check:\n\tgo test ./...\n", 0o644)
	write("scripts/check-go.sh", "#!/bin/sh\ngo test ./...\n", 0o644)
	write("scripts/release", "#!/usr/bin/env bash\n", 0o644)
	write("bin/render", "render\n", 0o755)
	write("scripts/README.md", "documentation\n", 0o644)
	write("scripts/.hidden.sh", "#!/bin/sh\n", 0o755)

	got, digest, err := DiscoverAutomation(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []AutomationCandidate{
		{Path: "Makefile", Kind: "task runner"},
		{Path: "bin/render", Kind: "script"},
		{Path: "scripts/check-go.sh", Kind: "script"},
		{Path: "scripts/release", Kind: "script"},
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	if digest == "" {
		t.Fatal("digest is empty")
	}

	write("scripts/check-go.sh", "#!/bin/sh\ngo test -race ./...\n", 0o644)
	_, changed, err := DiscoverAutomation(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed == digest {
		t.Error("digest did not change when candidate content changed")
	}
}

func TestAutomationCatalogReviewed(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	reviewed, err := AutomationCatalogReviewed(ctx, db, "first")
	if err != nil {
		t.Fatal(err)
	}
	if reviewed {
		t.Fatal("unseen digest reported as reviewed")
	}
	if err := MarkAutomationCatalogReviewed(ctx, db, "first"); err != nil {
		t.Fatal(err)
	}
	reviewed, err = AutomationCatalogReviewed(ctx, db, "first")
	if err != nil {
		t.Fatal(err)
	}
	if !reviewed {
		t.Fatal("acknowledged digest reported as unreviewed")
	}
	reviewed, err = AutomationCatalogReviewed(ctx, db, "second")
	if err != nil {
		t.Fatal(err)
	}
	if reviewed {
		t.Fatal("changed digest reported as reviewed")
	}
}
