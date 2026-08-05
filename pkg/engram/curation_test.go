package engram

import (
	"context"
	"database/sql"
	"testing"
)

func curationEvents(t *testing.T, db *sql.DB, f CurationFilter) []CurationEvent {
	t.Helper()
	evs, err := ListCurationEvents(context.Background(), db, f)
	if err != nil {
		t.Fatalf("ListCurationEvents: %v", err)
	}
	return evs
}

// TestCurationCapturesOverwriteLosslessly is the core losslessness guarantee: an
// overwrite of an existing (tier, key) -- which the last-write-wins memories table
// erases -- must leave both the original and the replacement recoverable from the
// append-only curation log.
func TestCurationCapturesOverwriteLosslessly(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "k", Content: "first", Tldr: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "k", Content: "second", Tldr: "two"}); err != nil {
		t.Fatal(err)
	}

	// The working store keeps only the last write.
	m, err := ReadMemory(ctx, db, TierLong, "k")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Content != "second" {
		t.Fatalf("memories table = %+v, want content=second", m)
	}

	// The curation log kept both, tagged create then update, with each snapshot.
	evs := curationEvents(t, db, CurationFilter{Key: "k"})
	if len(evs) != 2 {
		t.Fatalf("got %d curation events, want 2 (create + update): %+v", len(evs), evs)
	}
	// Newest first.
	if evs[0].Action != CurationUpdate || evs[0].Content != "second" || evs[0].Tldr != "two" {
		t.Errorf("newest event = %+v, want update/second/two", evs[0])
	}
	if evs[1].Action != CurationCreate || evs[1].Content != "first" || evs[1].Tldr != "one" {
		t.Errorf("oldest event = %+v, want create/first/one", evs[1])
	}
}

// TestCurationCapturesDelete asserts a delete snapshots the removed content, which
// the memories table drops entirely.
func TestCurationCapturesDelete(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "task", Content: "in flight", Tldr: "wip"}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteMemory(ctx, db, TierShort, "task"); err != nil {
		t.Fatal(err)
	}

	if m, err := ReadMemory(ctx, db, TierShort, "task"); err != nil {
		t.Fatal(err)
	} else if m != nil {
		t.Fatalf("memory not deleted: %+v", m)
	}

	evs := curationEvents(t, db, CurationFilter{Action: CurationDelete})
	if len(evs) != 1 {
		t.Fatalf("got %d delete events, want 1: %+v", len(evs), evs)
	}
	if evs[0].Content != "in flight" || evs[0].Tier != TierShort || evs[0].Key != "task" {
		t.Errorf("delete event = %+v, want short/task/'in flight'", evs[0])
	}
}

// TestCurationFailedDeleteDoesNotCapture ensures a delete that matched nothing
// records no event.
func TestCurationFailedDeleteDoesNotCapture(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := DeleteMemory(ctx, db, TierShort, "absent"); err == nil {
		t.Fatal("expected not-found error deleting absent key")
	}
	if evs := curationEvents(t, db, CurationFilter{}); len(evs) != 0 {
		t.Fatalf("got %d events for a failed delete, want 0: %+v", len(evs), evs)
	}
}

// TestCurationMoveIsSingleEvent asserts a same-database move records exactly one
// move event (not the create+delete pair it is built from) carrying the tier
// transition.
func TestCurationMoveIsSingleEvent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "note", Content: "body"}); err != nil {
		t.Fatal(err)
	}
	if err := MoveMemory(ctx, db, "note", TierShort, TierLong); err != nil {
		t.Fatal(err)
	}

	moves := curationEvents(t, db, CurationFilter{Action: CurationMove})
	if len(moves) != 1 {
		t.Fatalf("got %d move events, want 1: %+v", len(moves), moves)
	}
	if moves[0].FromTier != TierShort || moves[0].ToTier != TierLong {
		t.Errorf("move event = %+v, want short->long", moves[0])
	}
	// The move must not leak stray create/update/delete events.
	all := curationEvents(t, db, CurationFilter{})
	if len(all) != 2 { // the initial create + the move
		t.Fatalf("got %d total events, want 2 (create + move): %+v", len(all), all)
	}
}

// TestCurationTldrSet asserts a tldr-set records the new summary alongside a
// snapshot of the untouched content.
func TestCurationTldrSet(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "k", Content: "keep me"}); err != nil {
		t.Fatal(err)
	}
	ok, err := SetMemoryTldr(ctx, db, TierLong, "k", "a summary")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("SetMemoryTldr reported no match")
	}

	evs := curationEvents(t, db, CurationFilter{Action: CurationTldrSet})
	if len(evs) != 1 {
		t.Fatalf("got %d tldr-set events, want 1: %+v", len(evs), evs)
	}
	if evs[0].Tldr != "a summary" || evs[0].Content != "keep me" {
		t.Errorf("tldr-set event = %+v, want tldr='a summary' content='keep me'", evs[0])
	}
}

// TestCurationSkillAdoptOverridesAction confirms the skill-adopt override wins
// over the derived create/update action.
func TestCurationSkillAdoptOverridesAction(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "proc", Content: "steps"}); err != nil {
		t.Fatal(err)
	}
	adopted := Memory{Tier: TierLong, Key: "proc", Content: "steps", Trigger: "when asked to do the thing"}
	if err := WriteMemory(ctx, db, adopted, WithCurationAction(CurationSkillAdopt)); err != nil {
		t.Fatal(err)
	}

	if evs := curationEvents(t, db, CurationFilter{Action: CurationSkillAdopt}); len(evs) != 1 {
		t.Fatalf("got %d skill-adopt events, want 1: %+v", len(evs), evs)
	} else if evs[0].Trigger != "when asked to do the thing" {
		t.Errorf("skill-adopt event = %+v, want trigger snapshot", evs[0])
	}
}

// TestCurationSourceTag confirms the trigger-source tag is captured.
func TestCurationSourceTag(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "k", Content: "c"},
		WithCurationSource(SourceLoad), WithCurationScope("project")); err != nil {
		t.Fatal(err)
	}
	evs := curationEvents(t, db, CurationFilter{})
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if evs[0].Source != SourceLoad || evs[0].DBScope != "project" {
		t.Errorf("event = %+v, want source=load scope=project", evs[0])
	}
}

// TestPruneKeepsCurationEvents guards the losslessness-vs-rotation boundary:
// session-less curation rows (the norm today) survive pruning even when their
// file-touch sessions are aged out.
func TestPruneKeepsCurationEvents(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// Three file-touch sessions and a session-less curation write.
	for i, sess := range []string{"s1", "s2", "s3"} {
		if err := Record(ctx, db, Event{SessionID: sess, TS: int64(i+1) * 1000, Tool: ToolRead, FilePath: sess + ".go"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "k", Content: "c"}); err != nil {
		t.Fatal(err)
	}

	if _, err := Prune(ctx, db, 2); err != nil {
		t.Fatal(err)
	}

	if evs := curationEvents(t, db, CurationFilter{}); len(evs) != 1 {
		t.Fatalf("session-less curation event was pruned: got %d, want 1", len(evs))
	}
}

func TestCurationVocabularyIsChecked(t *testing.T) {
	// Every constant must pass its own validator, or the check is worse than none:
	// it would cry wolf on correct code and get muted.
	for _, action := range []CurationAction{
		CurationCreate, CurationUpdate, CurationDelete, CurationMove,
		CurationTldrSet, CurationSkillAdopt, CurationSkillClassify,
	} {
		if problem := validCurationAction(action); problem != "" {
			t.Errorf("declared action %q reported as out-of-vocabulary: %s", action, problem)
		}
	}
	for _, source := range []CurationSource{
		SourceInteractive, SourceLoad, SourceImport, SourceMigrate, SourceBootstrap,
	} {
		if problem := validCurationSource(source); problem != "" {
			t.Errorf("declared source %q reported as out-of-vocabulary: %s", source, problem)
		}
	}
	for _, scope := range []string{"project", "global", ""} {
		if problem := validCurationScope(scope); problem != "" {
			t.Errorf("scope %q reported as out-of-vocabulary: %s", scope, problem)
		}
	}
	// And a typo must be caught, which is the whole point.
	if validCurationAction("crete") == "" {
		t.Error("a misspelled action was accepted into the append-only log")
	}
	if validCurationScope("Global") == "" {
		t.Error("a wrong-case scope was accepted")
	}
}
