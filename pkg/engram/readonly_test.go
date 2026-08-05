package engram

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenProjectDBReadOnlyWorksWithoutDirectoryWriteAccess(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, ".engram")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := DBPath(root)
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want := Memory{Tier: TierLong, Key: "decision", Content: "body"}
	if err := WriteMemory(ctx, db, want); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Persistent WAL keeps both coordination files available for a later
	// read-only process that cannot create files beside the database.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("persistent SQLite file %s: %v", p, err)
		}
		if err := os.Chmod(p, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o755)
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			_ = os.Chmod(p, 0o644)
		}
	})

	ro, err := OpenProjectDBReadOnly(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	got, err := ReadMemory(ctx, ro, TierLong, want.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Content != want.Content {
		t.Fatalf("read-only memory = %+v, want %+v", got, want)
	}
	if err := WriteMemory(ctx, ro, Memory{Tier: TierLong, Key: "nope", Content: "must fail"}); err == nil {
		t.Fatal("write through read-only database succeeded")
	}
}

func TestOpenProjectDBReadOnlyDoesNotCreateDatabase(t *testing.T) {
	root := t.TempDir()
	db, err := OpenProjectDBReadOnly(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	memories, err := ListMemories(context.Background(), db, TierLong)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 0 {
		t.Fatalf("empty read-only database returned memories: %+v", memories)
	}
	if err := WriteMemory(context.Background(), db, Memory{Tier: TierLong, Key: "nope", Content: "must fail"}); err == nil {
		t.Fatal("write to empty read-only database succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(root, ".engram")); !os.IsNotExist(statErr) {
		t.Fatalf("read-only open created .engram: %v", statErr)
	}
}

func TestOpenProjectDBReadOnlyExplainsLinkedWorktreeStorage(t *testing.T) {
	base := t.TempDir()
	mainRoot := filepath.Join(base, "main")
	worktreeRoot := filepath.Join(base, "worktree")
	writeLinkedWorktree(t, mainRoot, worktreeRoot, "feature")
	if err := os.MkdirAll(filepath.Join(mainRoot, ".engram"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainRoot, ".engram", "mem.db"), []byte("not sqlite"), 0o444); err != nil {
		t.Fatal(err)
	}

	_, err := OpenProjectDBReadOnly(context.Background(), worktreeRoot)
	if err == nil {
		t.Fatal("OpenProjectDBReadOnly unexpectedly succeeded")
	}
	for _, want := range []string{
		"linked worktree memory is stored in the main checkout",
		filepath.Join(mainRoot, ".engram", "mem.db"),
		"retry once with write access",
		"do not create a separate .engram directory",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}
