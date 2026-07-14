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

func writeLinkedWorktree(t *testing.T, mainRoot, worktreeRoot, name string) {
	t.Helper()
	gitDir := filepath.Join(mainRoot, ".git", "worktrees", name)
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProjectStorageRootLinkedWorktree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mainRoot := filepath.Join(home, "code", "repo")
	worktreeRoot := filepath.Join(home, "worktrees", "repo-feature")
	remote := "git@github.com:me/repo.git"
	writeGitRemote(t, mainRoot, [][2]string{{"origin", remote}})
	writeLinkedWorktree(t, mainRoot, worktreeRoot, "repo-feature")

	if got := ProjectStorageRoot(worktreeRoot); !samePath(got, mainRoot) {
		t.Errorf("ProjectStorageRoot = %q, want main root %q", got, mainRoot)
	}
	if got := DBPath(worktreeRoot); got != filepath.Join(mainRoot, ".engram", "mem.db") {
		t.Errorf("DBPath = %q, want main checkout DB", got)
	}
	if got := ProjectIdentity(worktreeRoot); got != remote {
		t.Errorf("ProjectIdentity = %q, want common git remote", got)
	}
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
		// parallel branches in separate clones -- must each get a row, keyed by
		// (identity, path), so neither evicts the other. Linked git worktrees are
		// covered separately below and deliberately share one storage row.
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

	t.Run("linked_worktree_shares_main_checkout_row", func(t *testing.T) {
		db := testDB(t)
		shared := "git@github.com:me/worktree.git"
		mainRoot := filepath.Join(home, "code", "repo")
		worktreeRoot := filepath.Join(home, "worktrees", "repo-feature")

		if err := os.MkdirAll(filepath.Join(mainRoot, ".engram"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitRemote(t, mainRoot, [][2]string{{"origin", shared}})
		writeLinkedWorktree(t, mainRoot, worktreeRoot, "repo-feature")

		if err := RegisterProject(ctx, db, mainRoot); err != nil {
			t.Fatal(err)
		}
		if err := RegisterProject(ctx, db, worktreeRoot); err != nil {
			t.Fatal(err)
		}
		if n := countProjects(t, db); n != 1 {
			t.Fatalf("project count = %d, want 1 (linked worktree shares main storage)", n)
		}
		if !IsProjectRegistered(ctx, db, worktreeRoot) {
			t.Fatal("linked worktree should see main checkout manifest row as registered")
		}
		var path string
		if err := db.QueryRow(`SELECT path FROM projects WHERE identity = ?`, shared).Scan(&path); err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join("code", "repo") {
			t.Errorf("path = %q, want main checkout path", path)
		}
	})

	t.Run("linked_worktree_registration_removes_stale_worktree_row", func(t *testing.T) {
		db := testDB(t)
		shared := "git@github.com:me/stale-worktree.git"
		mainRoot := filepath.Join(home, "code", "stale-main")
		worktreeRoot := filepath.Join(home, "worktrees", "stale-feature")

		if err := os.MkdirAll(filepath.Join(mainRoot, ".engram"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(worktreeRoot, ".engram"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitRemote(t, mainRoot, [][2]string{{"origin", shared}})
		writeLinkedWorktree(t, mainRoot, worktreeRoot, "stale-feature")

		_, err := db.ExecContext(ctx,
			`INSERT INTO projects (identity, path, last_seen) VALUES (?, ?, 1)`,
			shared, filepath.Join("worktrees", "stale-feature"))
		if err != nil {
			t.Fatal(err)
		}
		if err := RegisterProject(ctx, db, worktreeRoot); err != nil {
			t.Fatal(err)
		}
		if n := countProjects(t, db); n != 1 {
			t.Fatalf("project count = %d, want 1 (stale worktree row collapsed)", n)
		}
		var path string
		if err := db.QueryRow(`SELECT path FROM projects WHERE identity = ?`, shared).Scan(&path); err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join("code", "stale-main") {
			t.Errorf("path = %q, want main checkout path", path)
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

func TestOpenProjectDBFromLinkedWorktreeCreatesOnlyMainDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	mainRoot := filepath.Join(home, "code", "repo")
	worktreeRoot := filepath.Join(home, "worktrees", "repo-feature")
	remote := "git@github.com:me/repo.git"
	writeGitRemote(t, mainRoot, [][2]string{{"origin", remote}})
	writeLinkedWorktree(t, mainRoot, worktreeRoot, "repo-feature")

	db, err := OpenProjectDB(ctx, worktreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := os.Stat(filepath.Join(mainRoot, ".engram", "mem.db")); err != nil {
		t.Fatalf("main checkout DB missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreeRoot, ".engram")); !os.IsNotExist(err) {
		t.Fatalf("linked worktree should not get .engram dir, stat err = %v", err)
	}

	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	var identity, path string
	if err := gdb.QueryRow(`SELECT identity, path FROM projects`).Scan(&identity, &path); err != nil {
		t.Fatal(err)
	}
	if identity != remote {
		t.Errorf("identity = %q, want %q", identity, remote)
	}
	if path != filepath.Join("code", "repo") {
		t.Errorf("path = %q, want main checkout path", path)
	}
}

func TestForgetProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()

	insert := func(db *sql.DB, identity, path string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			`INSERT INTO projects (identity, path, last_seen) VALUES (?, ?, 1)`, identity, path); err != nil {
			t.Fatal(err)
		}
	}
	remainingPaths := func(db *sql.DB) []string {
		t.Helper()
		rows, err := db.Query(`SELECT path FROM projects ORDER BY path`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ps []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			ps = append(ps, p)
		}
		return ps
	}

	t.Run("path_match_leaves_sibling_clones", func(t *testing.T) {
		// Two clones share one identity; naming a path is single-row surgery and
		// must not evict the sibling clone.
		db := testDB(t)
		insert(db, "git@github.com:me/proj.git", filepath.Join("code", "a"))
		insert(db, "git@github.com:me/proj.git", filepath.Join("code", "b"))

		removed, err := ForgetProject(ctx, db, filepath.Join("code", "a"), false)
		if err != nil {
			t.Fatal(err)
		}
		if len(removed) != 1 || removed[0].Path != filepath.Join("code", "a") {
			t.Fatalf("removed = %+v, want just code/a", removed)
		}
		if got := remainingPaths(db); len(got) != 1 || got[0] != filepath.Join("code", "b") {
			t.Fatalf("remaining = %v, want [code/b]", got)
		}
	})

	t.Run("identity_fallback_forgets_all_clones", func(t *testing.T) {
		db := testDB(t)
		insert(db, "git@github.com:me/proj.git", filepath.Join("code", "a"))
		insert(db, "git@github.com:me/proj.git", filepath.Join("code", "b"))
		insert(db, "git@github.com:me/other.git", filepath.Join("code", "c"))

		removed, err := ForgetProject(ctx, db, "git@github.com:me/proj.git", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(removed) != 2 {
			t.Fatalf("removed %d rows, want 2 (both clones of the identity)", len(removed))
		}
		if got := remainingPaths(db); len(got) != 1 || got[0] != filepath.Join("code", "c") {
			t.Fatalf("remaining = %v, want [code/c]", got)
		}
	})

	t.Run("path_wins_over_identity", func(t *testing.T) {
		// A target that could match a path OR an identity resolves to the path,
		// so an unlucky name collision can never fan out to identity siblings.
		db := testDB(t)
		insert(db, "code/x", filepath.Join("code", "y")) // identity literally equals another row's target
		insert(db, "other", "code/x")                    // path == the target string

		removed, err := ForgetProject(ctx, db, "code/x", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(removed) != 1 || removed[0].Path != "code/x" {
			t.Fatalf("removed = %+v, want the path row only", removed)
		}
	})

	t.Run("abs_path_normalizes_to_stored_relative", func(t *testing.T) {
		db := testDB(t)
		insert(db, "id-x", filepath.Join("code", "a"))

		removed, err := ForgetProject(ctx, db, filepath.Join(home, "code", "a"), false)
		if err != nil {
			t.Fatal(err)
		}
		if len(removed) != 1 {
			t.Fatalf("removed = %+v, want 1 via abs-path normalization", removed)
		}
	})

	t.Run("no_match_is_benign", func(t *testing.T) {
		db := testDB(t)
		insert(db, "id-x", filepath.Join("code", "a"))

		removed, err := ForgetProject(ctx, db, "code/nope", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(removed) != 0 {
			t.Fatalf("removed = %+v, want none", removed)
		}
		if got := remainingPaths(db); len(got) != 1 {
			t.Fatalf("row wrongly removed: %v", got)
		}
	})

	t.Run("purge_deletes_engram_dir", func(t *testing.T) {
		// A stray project outside $HOME (stored absolute, like /tmp): --purge
		// removes the row AND the .engram on disk.
		db := testDB(t)
		stray := t.TempDir()
		engramDir := filepath.Join(stray, ".engram")
		if err := os.MkdirAll(engramDir, 0o755); err != nil {
			t.Fatal(err)
		}
		insert(db, "id-stray", stray)

		removed, err := ForgetProject(ctx, db, stray, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(removed) != 1 || removed[0].Purged != engramDir {
			t.Fatalf("removed = %+v, want purged %s", removed, engramDir)
		}
		if _, err := os.Stat(engramDir); !os.IsNotExist(err) {
			t.Fatalf(".engram dir still present after purge (err = %v)", err)
		}
	})

	t.Run("purge_refuses_global_engram", func(t *testing.T) {
		// A corrupt row resolving to $HOME would point .engram at the global DB.
		// Purge must refuse and leave every global memory intact, while still
		// removing the bogus manifest row.
		db := testDB(t)
		globalEngram := filepath.Join(home, ".engram")
		if err := os.MkdirAll(globalEngram, 0o755); err != nil {
			t.Fatal(err)
		}
		insert(db, "id-danger", "") // absProjectRoot("", home) -> $HOME

		removed, err := ForgetProject(ctx, db, "id-danger", true)
		if err == nil {
			t.Fatal("expected purge guard to error, got nil")
		}
		if len(removed) != 1 || removed[0].Purged != "" {
			t.Fatalf("removed = %+v, want row removed with no purge", removed)
		}
		if _, err := os.Stat(globalEngram); err != nil {
			t.Fatalf("global engram dir was wrongly touched: %v", err)
		}
		if got := remainingPaths(db); len(got) != 0 {
			t.Fatalf("bogus row not removed: %v", got)
		}
	})
}
