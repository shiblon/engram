package main

import (
	"context"
	"strings"
	"testing"

	"github.com/shiblon/engram/pkg/engram"
)

func TestMemoryEditFormatRoundTrip(t *testing.T) {
	original := engram.Memory{
		Tier:    engram.TierLong,
		Key:     "decision",
		Tldr:    "one-line summary",
		Content: "first line\n\n--- BODY ---\nlast line\n",
	}
	formatted := formatMemoryEdit("project", original)
	for _, want := range []string{"Scope: project", "Tier: long", "Key: decision", memEditTldrMarker, memEditBodyMarker} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatted edit file does not contain %q:\n%s", want, formatted)
		}
	}
	tldr, body, err := parseMemoryEdit(formatted)
	if err != nil {
		t.Fatal(err)
	}
	if tldr != original.Tldr {
		t.Errorf("round-trip tldr = %q, want %q", tldr, original.Tldr)
	}
	if body != original.Content {
		t.Errorf("round-trip body = %q, want %q", body, original.Content)
	}
}

func TestParseMemoryEditValidation(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"missing tldr marker", "body", "missing \"--- TLDR ---\" marker"},
		{"missing body marker", memEditTldrMarker + "\nsummary", "missing \"--- BODY ---\" marker"},
		{"multiline tldr", memEditTldrMarker + "\none\ntwo\n" + memEditBodyMarker + "\nbody\n", "tldr must be one line"},
		{"empty body", memEditTldrMarker + "\nsummary\n" + memEditBodyMarker + "\n \n", "memory body cannot be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseMemoryEdit(tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseMemoryEdit() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestFindEditableMemoryKeepsAgentLayerExplicit(t *testing.T) {
	ctx := context.Background()
	db, err := engram.OpenProjectDB(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	primary := engram.Memory{Tier: engram.TierPreference, Key: "style", Content: "primary"}
	layerKey, err := engram.AgentLayerKey("codex", "style")
	if err != nil {
		t.Fatal(err)
	}
	layer := engram.Memory{Tier: engram.TierPreference, Key: layerKey, Content: "codex"}
	for _, m := range []engram.Memory{primary, layer} {
		if err := engram.WriteMemory(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}

	tiers := []engram.Tier{engram.TierInvariant, engram.TierPreference}
	got, err := findEditableMemory(ctx, db, tiers, "", "style")
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != primary.Key {
		t.Errorf("primary edit resolved key %q, want %q", got.Key, primary.Key)
	}
	got, err = findEditableMemory(ctx, db, tiers, "codex", "style")
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != layer.Key {
		t.Errorf("agent edit resolved key %q, want %q", got.Key, layer.Key)
	}
	_, err = findEditableMemory(ctx, db, tiers, "", layerKey)
	if err == nil || !strings.Contains(err.Error(), "requires --agent codex") {
		t.Fatalf("stored layer key error = %v, want explicit --agent guidance", err)
	}
}

func TestFindEditableMemoryRequiresTierWhenAmbiguous(t *testing.T) {
	ctx := context.Background()
	db, err := engram.OpenProjectDB(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tier := range []engram.Tier{engram.TierLong, engram.TierShort} {
		if err := engram.WriteMemory(ctx, db, engram.Memory{Tier: tier, Key: "same", Content: string(tier)}); err != nil {
			t.Fatal(err)
		}
	}

	_, err = findEditableMemory(ctx, db, []engram.Tier{engram.TierLong, engram.TierShort}, "", "same")
	if err == nil || !strings.Contains(err.Error(), "specify --tier") {
		t.Fatalf("ambiguous edit error = %v, want --tier guidance", err)
	}
}

func TestApplyMemoryEditUsesSurgicalTldrUpdate(t *testing.T) {
	ctx := context.Background()
	db, err := engram.OpenProjectDB(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := engram.Memory{TS: 123, Tier: engram.TierLong, Key: "decision", Content: "body", Tldr: "old"}
	if err := engram.WriteMemory(ctx, db, original); err != nil {
		t.Fatal(err)
	}

	result, err := applyMemoryEdit(ctx, db, original, "new", original.Content, "project")
	if err != nil {
		t.Fatal(err)
	}
	if result != "updated tldr" {
		t.Errorf("result = %q, want updated tldr", result)
	}
	got, err := engram.ReadMemory(ctx, db, original.Tier, original.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.TS != original.TS {
		t.Errorf("tldr-only edit changed timestamp from %d to %d", original.TS, got.TS)
	}
	if got.Tldr != "new" || got.Content != original.Content {
		t.Errorf("edited memory = %+v, want new tldr and unchanged body", got)
	}
}

func TestApplyMemoryEditPreservesMetadata(t *testing.T) {
	ctx := context.Background()
	db, err := engram.OpenProjectDB(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := engram.Memory{
		TS: 123, Tier: engram.TierShort, Key: "working", Content: "old body",
		Tldr: "summary", SessionID: "session-1",
	}
	if err := engram.WriteMemory(ctx, db, original); err != nil {
		t.Fatal(err)
	}

	result, err := applyMemoryEdit(ctx, db, original, original.Tldr, "new body", "project")
	if err != nil {
		t.Fatal(err)
	}
	if result != "updated body" {
		t.Errorf("result = %q, want updated body", result)
	}
	got, err := engram.ReadMemory(ctx, db, original.Tier, original.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "new body" || got.Tldr != original.Tldr || got.SessionID != original.SessionID {
		t.Errorf("edited memory did not preserve metadata: %+v", got)
	}
	if got.TS == original.TS {
		t.Errorf("body edit did not refresh timestamp %d", got.TS)
	}
}

func TestApplyMemoryEditPreservesSkillTrigger(t *testing.T) {
	ctx := context.Background()
	db, err := engram.OpenProjectDB(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := engram.Memory{
		TS: 123, Tier: engram.TierLong, Key: "workflow", Content: "old instructions",
		Tldr: "outcome", Trigger: "when the workflow is requested",
	}
	if err := engram.WriteMemory(ctx, db, original); err != nil {
		t.Fatal(err)
	}

	if _, err := applyMemoryEdit(ctx, db, original, original.Tldr, "new instructions", "project"); err != nil {
		t.Fatal(err)
	}
	got, err := engram.ReadMemory(ctx, db, original.Tier, original.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Trigger != original.Trigger {
		t.Errorf("skill trigger = %q, want preserved %q", got.Trigger, original.Trigger)
	}
}

func TestApplyMemoryEditRejectsConcurrentChange(t *testing.T) {
	ctx := context.Background()
	db, err := engram.OpenProjectDB(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := engram.Memory{TS: 123, Tier: engram.TierLong, Key: "decision", Content: "original"}
	if err := engram.WriteMemory(ctx, db, original); err != nil {
		t.Fatal(err)
	}
	concurrent := original
	concurrent.TS = 456
	concurrent.Content = "changed elsewhere"
	if err := engram.WriteMemory(ctx, db, concurrent); err != nil {
		t.Fatal(err)
	}

	_, err = applyMemoryEdit(ctx, db, original, "", "edited locally", "project")
	if err == nil || !strings.Contains(err.Error(), "changed while the editor was open") {
		t.Fatalf("concurrent edit error = %v, want conflict guidance", err)
	}
	got, err := engram.ReadMemory(ctx, db, original.Tier, original.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != concurrent.Content {
		t.Errorf("concurrent content was overwritten: got %q, want %q", got.Content, concurrent.Content)
	}
}
