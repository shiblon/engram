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
