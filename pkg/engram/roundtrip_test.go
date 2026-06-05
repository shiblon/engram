package engram

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveRestoreRoundTrip is the end-to-end integration test: populate a
// source HOME, Save to a buffer, point HOME at a fresh destination, Restore,
// and verify that global memories and project snapshots arrived intact.
func TestSaveRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()

	// --- SOURCE MACHINE (HOME_A) ---
	homeA := t.TempDir()
	t.Setenv("HOME", homeA)

	// Global memories.
	globalDB, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = WriteMemory(ctx, globalDB, Memory{Tier: TierInvariant, Key: "codename", Content: "Cadence"})
	_ = WriteMemory(ctx, globalDB, Memory{Tier: TierPreference, Key: "style", Content: "terse"})
	globalDB.Close()

	// A project with memories.
	projRoot := filepath.Join(homeA, "code", "myproject")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitRemote(t, projRoot, [][2]string{{"origin", "git@github.com:me/myproject.git"}})
	projDB, err := OpenProjectDB(ctx, projRoot)
	if err != nil {
		t.Fatal(err)
	}
	_ = WriteMemory(ctx, projDB, Memory{Tier: TierLong, Key: "design", Content: "manifest-based enumeration"})
	projDB.Close()

	// Register in global manifest (normally happens at creation; the test DB
	// is created by OpenProjectDB above which calls registerSelf, but HOME is
	// set so it should have run; register explicitly to be certain).
	globalDB, _ = OpenGlobalDB(ctx)
	_ = RegisterProject(ctx, globalDB, projRoot)
	globalDB.Close()

	// Save.
	var buf bytes.Buffer
	saveResult, err := Save(ctx, &buf, SaveOptions{EngramVersion: "test"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saveResult.ProjectCount != 1 {
		t.Fatalf("Save: ProjectCount = %d, want 1", saveResult.ProjectCount)
	}

	// --- DESTINATION MACHINE (HOME_B) ---
	homeB := t.TempDir()
	t.Setenv("HOME", homeB)

	restoreResult, err := Restore(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Global should have been applied (homeB was empty).
	if !restoreResult.GlobalApplied {
		t.Error("Restore: GlobalApplied = false, want true")
	}
	if restoreResult.GlobalSkipped {
		t.Error("Restore: GlobalSkipped = true, want false")
	}

	// One project should be staged.
	if restoreResult.StagedCount != 1 {
		t.Errorf("Restore: StagedCount = %d, want 1", restoreResult.StagedCount)
	}

	// Global memories should be intact in HOME_B.
	destGlobalDB, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer destGlobalDB.Close()

	mems, err := ListMemories(ctx, destGlobalDB, TierInvariant)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 || mems[0].Key != "codename" || mems[0].Content != "Cadence" {
		t.Errorf("global invariant memories = %v, want [{codename Cadence}]", mems)
	}

	prefMems, err := ListMemories(ctx, destGlobalDB, TierPreference)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefMems) != 1 || prefMems[0].Key != "style" {
		t.Errorf("global preference memories = %v, want [{style terse}]", prefMems)
	}

	// The project should be registered as pending in the manifest.
	var identity, status string
	err = destGlobalDB.QueryRowContext(ctx,
		`SELECT identity, status FROM projects WHERE identity = ?`,
		"git@github.com:me/myproject.git").Scan(&identity, &status)
	if err != nil {
		t.Fatalf("pending manifest row: %v", err)
	}
	if status != "pending" {
		t.Errorf("manifest status = %q, want 'pending'", status)
	}

	// The staged mem.db should contain the project memory.
	stageDir := filepath.Join(homeB, ".engram", "project-stage")
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		t.Fatalf("read stage dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stage dir entries = %d, want 1", len(entries))
	}
	stagedDBPath := filepath.Join(stageDir, entries[0].Name(), "mem.db")
	stagedDB, err := Open(ctx, stagedDBPath)
	if err != nil {
		t.Fatalf("open staged db: %v", err)
	}
	defer stagedDB.Close()

	projMems, err := ListMemories(ctx, stagedDB, TierLong)
	if err != nil {
		t.Fatal(err)
	}
	if len(projMems) != 1 || projMems[0].Key != "design" || projMems[0].Content != "manifest-based enumeration" {
		t.Errorf("staged project memories = %v, want [{design manifest-based enumeration}]", projMems)
	}
}

// TestRestoreSkipsPopulatedGlobal verifies that a destination with existing
// curated memories does not get overwritten.
func TestRestoreSkipsPopulatedGlobal(t *testing.T) {
	ctx := context.Background()

	// Source.
	homeA := t.TempDir()
	t.Setenv("HOME", homeA)
	gdb, _ := OpenGlobalDB(ctx)
	_ = WriteMemory(ctx, gdb, Memory{Tier: TierPreference, Key: "k", Content: "source"})
	gdb.Close()

	var buf bytes.Buffer
	if _, err := Save(ctx, &buf, SaveOptions{}); err != nil {
		t.Fatal(err)
	}

	// Destination already has curated content.
	homeB := t.TempDir()
	t.Setenv("HOME", homeB)
	gdb, _ = OpenGlobalDB(ctx)
	_ = WriteMemory(ctx, gdb, Memory{Tier: TierPreference, Key: "k", Content: "local"})
	gdb.Close()

	result, err := Restore(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if result.GlobalApplied {
		t.Error("GlobalApplied = true, want false (destination was populated)")
	}
	if !result.GlobalSkipped {
		t.Error("GlobalSkipped = false, want true")
	}

	// Local content must be untouched.
	gdb, _ = OpenGlobalDB(ctx)
	defer gdb.Close()
	mems, _ := ListMemories(ctx, gdb, TierPreference)
	if len(mems) != 1 || mems[0].Content != "local" {
		t.Errorf("destination memories = %v, want [{k local}]", mems)
	}
}
