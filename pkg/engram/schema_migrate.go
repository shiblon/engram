package engram

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersion is the current schema version. Bump this and add an entry to
// schemaMigrations whenever the schema changes.
const schemaVersion = 5

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
	// 3 -> 4: add memories.tldr, the one-line summary inject surfaces in place of
	// full content for every tier but invariants (which stay full because they are
	// the agent's voice). Empty is valid: readers fall back to the first line of
	// content, so old rows and un-summarized writes keep working.
	//
	// We rebuild rather than ALTER ... ADD COLUMN because every migration replays
	// from version 0 even on a fresh DB whose schema.sql already has tldr, and
	// SQLite has no ADD COLUMN IF NOT EXISTS -- a plain ALTER would fail there with
	// "duplicate column". The canonical rebuild (copy the kept columns into a new
	// table that names tldr with a default, swap names) is correct whether the
	// column exists yet or not, so it is replay-safe. The three memories_fts sync
	// triggers are defined ON memories and so are dropped with it; we recreate them
	// and rebuild the external-content index from the swapped-in table. Existing
	// rows keep their tldr = '' and fall back to content's first line at inject.
	`DROP TRIGGER IF EXISTS memories_ai;
	 DROP TRIGGER IF EXISTS memories_ad;
	 DROP TRIGGER IF EXISTS memories_au;
	 CREATE TABLE memories_new (
	     id         INTEGER PRIMARY KEY,
	     ts         INTEGER NOT NULL,
	     tier       TEXT    NOT NULL,
	     key        TEXT    NOT NULL,
	     content    TEXT    NOT NULL DEFAULT '',
	     tldr       TEXT    NOT NULL DEFAULT '',
	     session_id TEXT
	 );
	 INSERT INTO memories_new (id, ts, tier, key, content, session_id)
	     SELECT id, ts, tier, key, content, session_id FROM memories;
	 DROP TABLE memories;
	 ALTER TABLE memories_new RENAME TO memories;
	 CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_tier_key ON memories (tier, key);
	 CREATE INDEX IF NOT EXISTS idx_memories_tier_ts ON memories (tier, ts DESC);
	 CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
	     INSERT INTO memories_fts(rowid, key, content)
	     VALUES (new.id, new.key, new.content);
	 END;
	 CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
	     INSERT INTO memories_fts(memories_fts, rowid, key, content)
	     VALUES ('delete', old.id, old.key, old.content);
	 END;
	 CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
	     INSERT INTO memories_fts(memories_fts, rowid, key, content)
	     VALUES ('delete', old.id, old.key, old.content);
	     INSERT INTO memories_fts(rowid, key, content)
	     VALUES (new.id, new.key, new.content);
	 END;
	 INSERT INTO memories_fts(memories_fts) VALUES ('rebuild');`,
	// 4 -> 5: remember the exact repository-automation snapshot most recently
	// reviewed. Fresh databases already have the table from schema.sql; IF NOT
	// EXISTS keeps the replay idempotent.
	`CREATE TABLE IF NOT EXISTS automation_catalog_state (
	     id          INTEGER PRIMARY KEY CHECK (id = 1),
	     digest      TEXT    NOT NULL,
	     reviewed_at INTEGER NOT NULL
	 );`,
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
