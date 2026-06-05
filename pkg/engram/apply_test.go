package engram

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeStageSlot creates a stage slot under stageDir for the given identity,
// with a mem.db containing one long-term memory and a project.json sidecar.
func makeStageSlot(t *testing.T, ctx context.Context, stageDir, identity, originalPath string) string {
	t.Helper()
	slug := identitySlug(identity)
	slotDir := filepath.Join(stageDir, slug)
	if err := os.MkdirAll(slotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, filepath.Join(slotDir, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "restored-key", Content: "restored-value"})
	db.Close()

	sidecar, _ := json.Marshal(SaveProject{Identity: identity, Path: originalPath})
	if err := os.WriteFile(filepath.Join(slotDir, "project.json"), sidecar, 0o644); err != nil {
		t.Fatal(err)
	}
	return slug
}

// registerPending inserts a pending manifest row pointing at a stage slot.
func registerPending(t *testing.T, ctx context.Context, globalDB interface {
	ExecContext(context.Context, string, ...any) (interface{ RowsAffected() (int64, error) }, error)
}, identity, stagePath string) {
	t.Helper()
}

func TestApplyRestoreEmptyTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	stageDir := filepath.Join(home, ".engram", "project-stage")
	identity := "git@github.com:me/proj.git"
	makeStageSlot(t, ctx, stageDir, identity, "code/proj")

	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	// Register as pending.
	slug := identitySlug(identity)
	stagePath := filepath.Join(".engram", "project-stage", slug)
	_, err = gdb.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen, status) VALUES (?, ?, 1, 'pending')`,
		identity, stagePath)
	if err != nil {
		t.Fatal(err)
	}

	// Target is a fresh empty directory.
	projRoot := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitRemote(t, projRoot, [][2]string{{"origin", identity}})

	result, err := ApplyRestore(ctx, gdb, identity, projRoot)
	if err != nil {
		t.Fatalf("ApplyRestore: %v", err)
	}
	if !result.Applied {
		t.Error("Applied = false, want true")
	}
	if result.Conflicted {
		t.Error("Conflicted = true, want false")
	}

	// mem.db should now be at the project root.
	if _, err := os.Stat(DBPath(projRoot)); err != nil {
		t.Errorf("mem.db not placed: %v", err)
	}

	// Memories should be accessible.
	db, err := Open(ctx, DBPath(projRoot))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mems, _ := ListMemories(ctx, db, TierLong)
	if len(mems) != 1 || mems[0].Key != "restored-key" {
		t.Errorf("memories = %v, want [{restored-key restored-value}]", mems)
	}

	// Stage slot should be gone.
	if _, err := os.Stat(filepath.Join(stageDir, slug)); !os.IsNotExist(err) {
		t.Error("stage slot should have been removed after apply")
	}

	// Manifest entry should now be live.
	var status string
	_ = gdb.QueryRowContext(ctx, `SELECT status FROM projects WHERE identity = ?`, identity).Scan(&status)
	if status != "live" {
		t.Errorf("manifest status = %q, want 'live'", status)
	}
}

func TestApplyRestoreConflict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	stageDir := filepath.Join(home, ".engram", "project-stage")
	identity := "git@github.com:me/conflict.git"
	makeStageSlot(t, ctx, stageDir, identity, "code/conflict")

	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	slug := identitySlug(identity)
	stagePath := filepath.Join(".engram", "project-stage", slug)
	_, _ = gdb.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen, status) VALUES (?, ?, 1, 'pending')`,
		identity, stagePath)

	// Target has curated content already.
	projRoot := filepath.Join(home, "code", "conflict")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	pdb, err := OpenProjectDB(ctx, projRoot)
	if err != nil {
		t.Fatal(err)
	}
	_ = WriteMemory(ctx, pdb, Memory{Tier: TierLong, Key: "local-key", Content: "local-value"})
	pdb.Close()

	result, err := ApplyRestore(ctx, gdb, identity, projRoot)
	if err != nil {
		t.Fatalf("ApplyRestore conflict: %v", err)
	}
	if result.Applied {
		t.Error("Applied = true on conflict, want false")
	}
	if !result.Conflicted {
		t.Error("Conflicted = false, want true")
	}
	if result.NewStagePath == "" {
		t.Error("NewStagePath empty on conflict")
	}

	// Local memories must be untouched.
	pdb, _ = Open(ctx, DBPath(projRoot))
	defer pdb.Close()
	mems, _ := ListMemories(ctx, pdb, TierLong)
	if len(mems) != 1 || mems[0].Key != "local-key" {
		t.Errorf("local memories = %v, want [{local-key local-value}]", mems)
	}

	// Entry stays pending under the new slot.
	var status string
	_ = gdb.QueryRowContext(ctx, `SELECT status FROM projects WHERE identity = ?`, identity).Scan(&status)
	if status != "pending" {
		t.Errorf("manifest status = %q, want 'pending' after conflict", status)
	}
}

func TestDiscardRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	stageDir := filepath.Join(home, ".engram", "project-stage")
	identity := "git@github.com:me/todelete.git"
	slug := makeStageSlot(t, ctx, stageDir, identity, "code/todelete")

	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	stagePath := filepath.Join(".engram", "project-stage", slug)
	_, _ = gdb.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen, status) VALUES (?, ?, 1, 'pending')`,
		identity, stagePath)

	if err := DiscardRestore(ctx, gdb, identity); err != nil {
		t.Fatalf("DiscardRestore: %v", err)
	}

	// Stage slot should be gone.
	if _, err := os.Stat(filepath.Join(stageDir, slug)); !os.IsNotExist(err) {
		t.Error("stage slot should have been removed after discard")
	}

	// Manifest row should be gone.
	var n int
	_ = gdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE identity = ?`, identity).Scan(&n)
	if n != 0 {
		t.Errorf("manifest rows after discard = %d, want 0", n)
	}
}

func TestListPendingRestores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	stageDir := filepath.Join(home, ".engram", "project-stage")
	id1 := "git@github.com:me/a.git"
	id2 := "git@github.com:me/b.git"
	slug1 := makeStageSlot(t, ctx, stageDir, id1, "code/a")
	slug2 := makeStageSlot(t, ctx, stageDir, id2, "code/b")

	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	for _, pair := range [][2]string{{id1, slug1}, {id2, slug2}} {
		_, _ = gdb.ExecContext(ctx,
			`INSERT INTO projects (identity, path, last_seen, status) VALUES (?, ?, 1, 'pending')`,
			pair[0], filepath.Join(".engram", "project-stage", pair[1]))
	}

	pending, err := ListPendingRestores(ctx, gdb)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("len(pending) = %d, want 2", len(pending))
	}
	ids := map[string]bool{pending[0].Identity: true, pending[1].Identity: true}
	if !ids[id1] || !ids[id2] {
		t.Errorf("pending identities = %v, want %v and %v", ids, id1, id2)
	}
}

func TestInjectSurfacesPendingRestores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	stageDir := filepath.Join(home, ".engram", "project-stage")
	identity := "git@github.com:me/surface.git"
	makeStageSlot(t, ctx, stageDir, identity, "old/surface")

	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	slug := identitySlug(identity)
	stagePath := filepath.Join(".engram", "project-stage", slug)
	_, _ = gdb.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen, status) VALUES (?, ?, 1, 'pending')`,
		identity, stagePath)

	pending, err := ListPendingRestores(ctx, gdb)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListPendingRestores: err=%v len=%d", err, len(pending))
	}

	// Simulate inject setting MatchesCurrent for the current repo.
	currentIdentity := identity // pretend we're in the matching repo
	for i := range pending {
		pending[i].MatchesCurrent = pending[i].Identity == currentIdentity
	}

	global := InjectResult{PendingRestores: pending}
	project := InjectResult{}
	text := InjectContextText(global, project, 5)

	if text == "" {
		t.Fatal("InjectContextText returned empty with pending restore")
	}
	// Should surface the identity and the apply hint.
	for _, want := range []string{"Staged restores", identity, "--apply"} {
		if !strings.Contains(text, want) {
			t.Errorf("inject output missing %q", want)
		}
	}
}
