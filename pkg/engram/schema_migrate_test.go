package engram

import (
	"context"
	"testing"
)

func TestSchemaMigrationFreshDB(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d, want %d", v, schemaVersion)
	}
}

func TestSchemaMigrationIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Opening again (re-running dbInit) must not fail or change the version.
	if err := dbInit(ctx, db); err != nil {
		t.Fatalf("second dbInit: %v", err)
	}

	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Errorf("user_version after second init = %d, want %d", v, schemaVersion)
	}
}

func TestSchemaMigrationFromZero(t *testing.T) {
	// Simulate a pre-versioning DB: schema applied but user_version = 0.
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Reset to version 0 to simulate a legacy DB.
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}

	// applyMigrations should advance it to schemaVersion without error.
	if err := applyMigrations(ctx, db); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}

	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d after migration from 0, want %d", v, schemaVersion)
	}
}

// A v2 DB stored a snippet column plus an events_fts table and triggers over it.
// Migrating to v3 must drop all of that, purge the old Bash "search" rows, and
// preserve the file rows (ids included).
func TestSchemaMigrationV2ToV3DropsSnippetAndSearches(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Recreate the v2 events shape: drop the slim v3 table and rebuild the old
	// one with snippet, the external-content FTS, and its sync triggers; then
	// stamp the DB back to version 2.
	v2setup := []string{
		`DROP TABLE events`,
		`CREATE TABLE events (
		    id INTEGER PRIMARY KEY, session_id TEXT NOT NULL, ts INTEGER NOT NULL,
		    tool TEXT NOT NULL, file_path TEXT NOT NULL, snippet TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX idx_events_session ON events (session_id)`,
		`CREATE INDEX idx_events_ts ON events (ts DESC)`,
		`CREATE VIRTUAL TABLE events_fts USING fts5(file_path, snippet, content='events', content_rowid='id')`,
		`CREATE TRIGGER events_ai AFTER INSERT ON events BEGIN
		    INSERT INTO events_fts(rowid, file_path, snippet) VALUES (new.id, new.file_path, new.snippet);
		 END`,
		`CREATE TRIGGER events_ad AFTER DELETE ON events BEGIN
		    INSERT INTO events_fts(events_fts, rowid, file_path, snippet) VALUES ('delete', old.id, old.file_path, old.snippet);
		 END`,
		`CREATE TRIGGER events_au AFTER UPDATE ON events BEGIN
		    INSERT INTO events_fts(events_fts, rowid, file_path, snippet) VALUES ('delete', old.id, old.file_path, old.snippet);
		    INSERT INTO events_fts(rowid, file_path, snippet) VALUES (new.id, new.file_path, new.snippet);
		 END`,
		`PRAGMA user_version = 2`,
	}
	for _, stmt := range v2setup {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("set up v2 state (%q): %v", stmt, err)
		}
	}

	// Two file touches and one Bash "search", each carrying a snippet.
	rows := []struct {
		id               int
		tool, path, snip string
	}{
		{1, "Read", "main.go", "package main"},
		{2, "Edit", "util.go", "func helper() {}"},
		{3, "Bash", "grep -r auth .", "auth.go:42"},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO events (id, session_id, ts, tool, file_path, snippet) VALUES (?, 's1', ?, ?, ?, ?)`,
			r.id, r.id*1000, r.tool, r.path, r.snip); err != nil {
			t.Fatalf("insert v2 row %d: %v", r.id, err)
		}
	}

	if err := applyMigrations(ctx, db); err != nil {
		t.Fatalf("applyMigrations v2->: %v", err)
	}

	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d, want %d", v, schemaVersion)
	}

	var snippetCols int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('events') WHERE name = 'snippet'`).Scan(&snippetCols); err != nil {
		t.Fatal(err)
	}
	if snippetCols != 0 {
		t.Error("snippet column still present after migration")
	}

	var ftsTables int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'events_fts'`).Scan(&ftsTables); err != nil {
		t.Fatal(err)
	}
	if ftsTables != 0 {
		t.Error("events_fts still present after migration")
	}

	// The Bash row is gone; the two file rows survive with their ids.
	got, err := queryStrings(ctx, db,
		`SELECT file_path FROM events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(got, []string{"main.go", "util.go"}) {
		t.Errorf("surviving file_paths = %v, want [main.go util.go]", got)
	}
}

// A v1 DB keyed UNIQUE(identity); migrating to v2 must drop that index and key
// on (identity, path) so two copies of one repo can coexist in the manifest.
func TestSchemaMigrationV1ToV2RelaxesProjectsKey(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Recreate the v1 shape: replace the (identity, path) index with UNIQUE(identity)
	// and stamp the DB back to version 1.
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_projects_identity_path`,
		`CREATE UNIQUE INDEX idx_projects_identity ON projects (identity)`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("set up v1 state (%q): %v", stmt, err)
		}
	}

	// Under v1, a second path for one identity is rejected by UNIQUE(identity).
	id := "git@github.com:me/parallel.git"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen) VALUES (?, 'code/a', 1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen) VALUES (?, 'code/b', 1)`, id); err == nil {
		t.Fatal("v1 should reject a second path for one identity, but it succeeded")
	}

	// Migrate to current.
	if err := applyMigrations(ctx, db); err != nil {
		t.Fatalf("applyMigrations v1->: %v", err)
	}

	// Now the second copy is accepted; both rows coexist.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen) VALUES (?, 'code/b', 1)`, id); err != nil {
		t.Fatalf("post-migration second path insert: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE identity = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("rows for identity = %d, want 2 after migration", n)
	}

	// The pair is still unique: a duplicate (identity, path) is rejected.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen) VALUES (?, 'code/a', 1)`, id); err == nil {
		t.Error("duplicate (identity, path) should be rejected after migration")
	}
}
