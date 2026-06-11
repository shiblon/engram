package engram

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersion is the current schema version. Bump this and add an entry to
// schemaMigrations whenever the schema changes.
const schemaVersion = 3

// schemaMigrations maps from-version to the SQL that advances to from+1.
// Version 0 means "newly created or pre-versioning DB with the baseline schema
// already applied by schema.sql"; migration 0->1 is a no-op sentinel.
var schemaMigrations = []string{
	// 0 -> 1: baseline schema (applied by schema.sql on Open; nothing extra needed)
	``,
	// 1 -> 2: the projects manifest key becomes (identity, path) so a repo with
	// multiple independent clones keeps one row per copy instead of having later
	// copies overwrite earlier ones. Safe on existing data: v1's UNIQUE(identity)
	// guaranteed no duplicate identities, so every row already satisfies the
	// stricter (identity, path) uniqueness. IF [NOT] EXISTS keeps it idempotent
	// for fresh DBs, where schema.sql already created the new index.
	`DROP INDEX IF EXISTS idx_projects_identity;
	 CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_identity_path ON projects (identity, path);`,
	// 2 -> 3: drop the events snippet column and the events_fts apparatus.
	// Nothing ever searched events (only memories_fts is queried), so the FTS
	// table and its triggers indexed data no reader consumed; record now stores
	// only file-touch breadcrumbs.
	//
	// We rebuild events rather than ALTER ... DROP COLUMN: every migration replays
	// from version 0 even on a fresh DB whose schema.sql already lacks snippet, and
	// SQLite has no DROP COLUMN IF EXISTS, so a literal drop would fail there. The
	// canonical rebuild (copy the kept columns into a new table, swap names) names
	// snippet nowhere, so it is correct whether the column exists or not. Historical
	// Bash rows -- the old grep/find "searches" -- are dropped in the copy: with
	// search injection gone they would only pollute the files list. The FTS triggers
	// reference snippet, so they and their external-content table go first.
	`DROP TRIGGER IF EXISTS events_ai;
	 DROP TRIGGER IF EXISTS events_ad;
	 DROP TRIGGER IF EXISTS events_au;
	 DROP TABLE IF EXISTS events_fts;
	 CREATE TABLE events_new (
	     id          INTEGER PRIMARY KEY,
	     session_id  TEXT    NOT NULL,
	     ts          INTEGER NOT NULL,
	     tool        TEXT    NOT NULL,
	     file_path   TEXT    NOT NULL
	 );
	 INSERT INTO events_new (id, session_id, ts, tool, file_path)
	     SELECT id, session_id, ts, tool, file_path FROM events WHERE tool != 'Bash';
	 DROP TABLE events;
	 ALTER TABLE events_new RENAME TO events;
	 CREATE INDEX IF NOT EXISTS idx_events_session ON events (session_id);
	 CREATE INDEX IF NOT EXISTS idx_events_ts      ON events (ts DESC);`,
}

// applyMigrations reads PRAGMA user_version, runs any pending migration steps
// in order inside individual transactions, and updates user_version on success.
func applyMigrations(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("schema migration: read user_version: %w", err)
	}

	for v := current; v < schemaVersion; v++ {
		sql := schemaMigrations[v]
		if err := runMigrationStep(ctx, db, v, sql); err != nil {
			return err
		}
	}
	return nil
}

func runMigrationStep(ctx context.Context, db *sql.DB, fromVersion int, stmt string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("schema migration %d->%d: begin: %w", fromVersion, fromVersion+1, err)
	}
	defer tx.Rollback()

	if stmt != "" {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("schema migration %d->%d: %w", fromVersion, fromVersion+1, err)
		}
	}

	// PRAGMA user_version cannot be set inside a transaction via a parameter,
	// so we set it after commit on the main connection.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("schema migration %d->%d: commit: %w", fromVersion, fromVersion+1, err)
	}

	next := fromVersion + 1
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, next)); err != nil {
		return fmt.Errorf("schema migration %d->%d: set user_version: %w", fromVersion, next, err)
	}
	return nil
}
