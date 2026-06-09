package engram

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGitRemote writes a minimal .git/config under root declaring the given
// named remotes (name -> url), in the order provided via names.
func writeGitRemote(t *testing.T, root string, remotes [][2]string) {
	t.Helper()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("[core]\n\trepositoryformatversion = 0\n")
	for _, r := range remotes {
		b.WriteString("[remote \"" + r[0] + "\"]\n\turl = " + r[1] + "\n\tfetch = +refs/heads/*\n")
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func countProjects(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestProjectIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("git_remote_preferred", func(t *testing.T) {
		root := filepath.Join(home, "code", "proj")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitRemote(t, root, [][2]string{{"origin", "git@github.com:me/proj.git"}})
		if got := ProjectIdentity(root); got != "git@github.com:me/proj.git" {
			t.Errorf("identity = %q, want git remote", got)
		}
	})

	t.Run("origin_over_other_remotes", func(t *testing.T) {
		root := filepath.Join(home, "code", "multi")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitRemote(t, root, [][2]string{
			{"upstream", "git@github.com:them/proj.git"},
			{"origin", "git@github.com:me/proj.git"},
		})
		if got := ProjectIdentity(root); got != "git@github.com:me/proj.git" {
			t.Errorf("identity = %q, want origin url", got)
		}
	})

	t.Run("first_remote_when_no_origin", func(t *testing.T) {
		root := filepath.Join(home, "code", "noorigin")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitRemote(t, root, [][2]string{{"upstream", "git@github.com:them/proj.git"}})
		if got := ProjectIdentity(root); got != "git@github.com:them/proj.git" {
			t.Errorf("identity = %q, want fallback remote url", got)
		}
	})

	t.Run("path_fallback_when_no_remote", func(t *testing.T) {
		root := filepath.Join(home, "code", "plain")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := ProjectIdentity(root); got != filepath.Join("code", "plain") {
			t.Errorf("identity = %q, want home-relative path", got)
		}
	})

	t.Run("absolute_when_outside_home", func(t *testing.T) {
		outside := t.TempDir() // a sibling temp dir, not under HOME
		if got := ProjectIdentity(outside); got != outside {
			t.Errorf("identity = %q, want absolute path %q", got, outside)
		}
	})
}

func TestRegisterProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	t.Run("insert_then_idempotent", func(t *testing.T) {
		db := testDB(t)
		root := filepath.Join(home, "code", "alpha")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitRemote(t, root, [][2]string{{"origin", "git@github.com:me/alpha.git"}})

		if err := RegisterProject(ctx, db, root); err != nil {
			t.Fatal(err)
		}
		if err := RegisterProject(ctx, db, root); err != nil {
			t.Fatal(err)
		}
		if n := countProjects(t, db); n != 1 {
			t.Fatalf("project count = %d, want 1 (idempotent)", n)
		}
		var identity, path string
		if err := db.QueryRow(`SELECT identity, path FROM projects`).Scan(&identity, &path); err != nil {
			t.Fatal(err)
		}
		if identity != "git@github.com:me/alpha.git" {
			t.Errorf("identity = %q", identity)
		}
		if path != filepath.Join("code", "alpha") {
			t.Errorf("path = %q, want home-relative", path)
		}
	})

	t.Run("rekey_when_remote_changes", func(t *testing.T) {
		db := testDB(t)
		root := filepath.Join(home, "code", "beta")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitRemote(t, root, [][2]string{{"origin", "git@github.com:me/old.git"}})
		if err := RegisterProject(ctx, db, root); err != nil {
			t.Fatal(err)
		}
		// The repo's remote changes; re-registering must re-key in place.
		writeGitRemote(t, root, [][2]string{{"origin", "git@github.com:me/new.git"}})
		if err := RegisterProject(ctx, db, root); err != nil {
			t.Fatal(err)
		}
		if n := countProjects(t, db); n != 1 {
			t.Fatalf("project count = %d, want 1 (re-key in place)", n)
		}
		var identity string
		if err := db.QueryRow(`SELECT identity FROM projects`).Scan(&identity); err != nil {
			t.Fatal(err)
		}
		if identity != "git@github.com:me/new.git" {
			t.Errorf("identity = %q, want re-keyed to new remote", identity)
		}
	})

	t.Run("multiple_copies_share_identity", func(t *testing.T) {
		// Two working copies of one repo (same remote, different paths) -- e.g.
		// parallel branches in separate clones/worktrees -- must each get a row,
		// keyed by (identity, path), so neither evicts the other.
		db := testDB(t)
		shared := "git@github.com:me/parallel.git"
		rootA := filepath.Join(home, "code", "loc-a")
		rootB := filepath.Join(home, "code", "loc-b")
		for _, r := range []string{rootA, rootB} {
			// Both copies are live on disk (each has its own .engram), so the
			// second registration is a genuine copy, not a move.
			if err := os.MkdirAll(filepath.Join(r, ".engram"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeGitRemote(t, r, [][2]string{{"origin", shared}})
		}
		if err := RegisterProject(ctx, db, rootA); err != nil {
			t.Fatal(err)
		}
		if err := RegisterProject(ctx, db, rootB); err != nil {
			t.Fatal(err)
		}
		if n := countProjects(t, db); n != 2 {
			t.Fatalf("project count = %d, want 2 (one row per copy)", n)
		}
		// Re-registering an existing copy is idempotent: no third row.
		if err := RegisterProject(ctx, db, rootA); err != nil {
			t.Fatal(err)
		}
		if n := countProjects(t, db); n != 2 {
			t.Fatalf("project count after re-register = %d, want 2 (idempotent)", n)
		}
		paths := map[string]bool{}
		rows, err := db.Query(`SELECT path FROM projects WHERE identity = ?`, shared)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			paths[p] = true
		}
		if !paths[filepath.Join("code", "loc-a")] || !paths[filepath.Join("code", "loc-b")] {
			t.Errorf("paths = %v, want both loc-a and loc-b", paths)
		}
	})

	t.Run("moved_repo_relocates_row", func(t *testing.T) {
		// A single checkout that moves on disk (old .engram gone) relocates its
		// row in place rather than leaving a stale one for prune to reap.
		db := testDB(t)
		shared := "git@github.com:me/moved.git"
		oldRoot := filepath.Join(home, "old", "proj")
		newRoot := filepath.Join(home, "new", "proj")

		if err := os.MkdirAll(filepath.Join(oldRoot, ".engram"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitRemote(t, oldRoot, [][2]string{{"origin", shared}})
		if err := RegisterProject(ctx, db, oldRoot); err != nil {
			t.Fatal(err)
		}
		if n := countProjects(t, db); n != 1 {
			t.Fatalf("project count after first register = %d, want 1", n)
		}

		// Simulate the move: the old location is gone, the new one is live.
		if err := os.RemoveAll(oldRoot); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(newRoot, ".engram"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitRemote(t, newRoot, [][2]string{{"origin", shared}})
		if err := RegisterProject(ctx, db, newRoot); err != nil {
			t.Fatal(err)
		}

		if n := countProjects(t, db); n != 1 {
			t.Fatalf("project count after move = %d, want 1 (relocated, not duplicated)", n)
		}
		var path string
		if err := db.QueryRow(`SELECT path FROM projects WHERE identity = ?`, shared).Scan(&path); err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join("new", "proj") {
			t.Errorf("path = %q, want relocated to new/proj", path)
		}
	})
}

func TestOpenProjectDBRegistersOnCreation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	root := filepath.Join(home, "code", "fresh")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitRemote(t, root, [][2]string{{"origin", "git@github.com:me/fresh.git"}})

	// First open creates the project DB -> registers in the global manifest.
	db, err := OpenProjectDB(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()
	if n := countProjects(t, gdb); n != 1 {
		t.Fatalf("after creation, manifest count = %d, want 1", n)
	}

	// Re-opening an existing project DB is not a creation: no new manifest write.
	db2, err := OpenProjectDB(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	db2.Close()
	if n := countProjects(t, gdb); n != 1 {
		t.Fatalf("after re-open, manifest count = %d, want 1 (creation-only)", n)
	}
}
