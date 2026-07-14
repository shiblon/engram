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
