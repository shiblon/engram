package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/shiblon/engram/pkg/engram"
)

func TestDumpMemoriesToWriter(t *testing.T) {
	ctx := context.Background()
	db, err := engram.OpenProjectDB(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := engram.WriteMemory(ctx, db, engram.Memory{Tier: engram.TierLong, Key: "alpha", Content: "beta"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := dumpMemories(ctx, db, []engram.Tier{engram.TierLong}, "", &buf); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("dumpMemories(dir=\"\") wrote %q, want the entry rendered to the writer", out)
	}
}

func TestEffectiveWriteTldr(t *testing.T) {
	withTldr := &engram.Memory{Tldr: "kept summary"}
	emptyTldr := &engram.Memory{Tldr: ""}
	tests := []struct {
		name        string
		existing    *engram.Memory
		flagChanged bool
		flagVal     string
		want        string
	}{
		{"no flag preserves existing tldr", withTldr, false, "", "kept summary"},
		{"no flag on a new memory stays empty", nil, false, "", ""},
		{"no flag preserves an empty tldr", emptyTldr, false, "", ""},
		{"flag sets the value", withTldr, true, "new summary", "new summary"},
		{"flag empty clears deliberately", withTldr, true, "", ""},
		{"flag value is trimmed", nil, true, "  spaced  ", "spaced"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveWriteTldr(tt.existing, tt.flagChanged, tt.flagVal); got != tt.want {
				t.Errorf("effectiveWriteTldr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateMemWriteContent(t *testing.T) {
	ctx := context.Background()
	db, err := engram.OpenProjectDB(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	original := engram.Memory{
		Tier: engram.TierPreference, Key: "style", Content: "original body",
	}
	if err := engram.WriteMemory(ctx, db, original); err != nil {
		t.Fatal(err)
	}
	failedUpdate := original
	failedUpdate.Content = "intended replacement"
	failedUpdate.Tldr = strings.Repeat("x", engram.MaxTldrLen+1)
	if err := engram.WriteMemory(ctx, db, failedUpdate); err == nil {
		t.Fatal("oversized tldr update succeeded, want failure")
	}
	existing, err := engram.ReadMemory(ctx, db, original.Tier, original.Key)
	if err != nil {
		t.Fatal(err)
	}
	if existing == nil {
		t.Fatal("memory missing after failed update")
	}
	if existing.Content != original.Content {
		t.Fatalf("content after failed update = %q, want %q", existing.Content, original.Content)
	}
	readback := engram.MemoryLabel(*existing) + "\n" + existing.Content

	if err := validateMemWriteContent(existing, readback); err == nil {
		t.Fatal("formatted read-back content was accepted, want rejection")
	} else {
		for _, want := range []string{"refusing to overwrite", "engram mem tldr", "intended body"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err, want)
			}
		}
	}

	for name, content := range map[string]string{
		"new memory has no read-back source": readback,
		"ordinary replacement body":          "updated body",
		"old raw body remains valid":         existing.Content,
		"unrelated bracketed content":        "[preference/other]\nbody",
	} {
		t.Run(name, func(t *testing.T) {
			candidate := existing
			if name == "new memory has no read-back source" {
				candidate = nil
			}
			if err := validateMemWriteContent(candidate, content); err != nil {
				t.Errorf("validateMemWriteContent() = %v, want nil", err)
			}
		})
	}
}

func TestMemWriteIsTldrOnly(t *testing.T) {
	existing := &engram.Memory{Content: "body"}
	tests := []struct {
		name        string
		existing    *engram.Memory
		content     string
		flagChanged bool
		want        bool
	}{
		{"same body with tldr flag", existing, "body", true, true},
		{"same body without tldr flag", existing, "body", false, false},
		{"different body with tldr flag", existing, "new body", true, false},
		{"new memory with tldr flag", nil, "body", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := memWriteIsTldrOnly(tt.existing, tt.content, tt.flagChanged); got != tt.want {
				t.Errorf("memWriteIsTldrOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemPersistentPreRunCanonicalizesTierAliases(t *testing.T) {
	oldTier := memTier
	t.Cleanup(func() { memTier = oldTier })

	memTier = "short-term"
	if err := memCmd.PersistentPreRunE(memWriteCmd, nil); err != nil {
		t.Fatal(err)
	}
	if memTier != string(engram.TierShort) {
		t.Errorf("memTier = %q, want %q", memTier, engram.TierShort)
	}

	memTier = "shortish"
	if err := memCmd.PersistentPreRunE(memWriteCmd, nil); err == nil {
		t.Fatal("persistent pre-run accepted an unknown tier")
	}
}
