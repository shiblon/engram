package engram

import (
	"context"
	"database/sql"
	"testing"
)

// Test setup must fail loudly. A discarded setup error does not vanish, it
// reappears later as a behavioral assertion failing for a reason that has nothing
// to do with the behavior -- which is the most expensive kind of test failure to
// diagnose. The package forbids silent discards in its own code; its tests should
// hold to it too.

func mustWriteMemory(t *testing.T, ctx context.Context, db *sql.DB, m Memory) {
	t.Helper()
	if err := WriteMemory(ctx, db, m); err != nil {
		t.Fatalf("setup: write %s memory %q: %v", m.Tier, m.Key, err)
	}
}

func mustOpenGlobalDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := OpenGlobalDB(ctx)
	if err != nil {
		t.Fatalf("setup: open global database: %v", err)
	}
	return db
}

func mustOpen(t *testing.T, ctx context.Context, path string) *sql.DB {
	t.Helper()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("setup: open database %s: %v", path, err)
	}
	return db
}

func mustRegisterProject(t *testing.T, ctx context.Context, db *sql.DB, root string) {
	t.Helper()
	if err := RegisterProject(ctx, db, root); err != nil {
		t.Fatalf("setup: register project %s: %v", root, err)
	}
}

func mustExec(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("setup: exec: %v", err)
	}
}

// mustScan reads a row that the test expects to exist. Where absence is the thing
// being asserted, check the error explicitly at the call site instead.
func mustScan(t *testing.T, row *sql.Row, dest ...any) {
	t.Helper()
	if err := row.Scan(dest...); err != nil {
		t.Fatalf("expected a row here: %v", err)
	}
}

func mustListPendingRestores(t *testing.T, ctx context.Context, db *sql.DB) []PendingRestore {
	t.Helper()
	pending, err := ListPendingRestores(ctx, db)
	if err != nil {
		t.Fatalf("list pending restores: %v", err)
	}
	return pending
}
