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
