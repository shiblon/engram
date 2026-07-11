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
