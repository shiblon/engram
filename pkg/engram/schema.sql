-- events is the passive activity log: one row per recorded file touch. It feeds
-- only the "recently active files" breadcrumb at inject time, so it stores just
-- enough to answer "where was I working" -- no content, no diffs. (It once kept
-- a snippet column and an FTS index over it, but nothing ever searched events,
-- so both were dropped in schema v3.)
CREATE TABLE IF NOT EXISTS events (
    id          INTEGER PRIMARY KEY,
    session_id  TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    tool        TEXT    NOT NULL,
    file_path   TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_session ON events (session_id);
CREATE INDEX IF NOT EXISTS idx_events_ts      ON events (ts DESC);

CREATE TABLE IF NOT EXISTS memories (
    id         INTEGER PRIMARY KEY,
    ts         INTEGER NOT NULL,
    tier       TEXT    NOT NULL,
    key        TEXT    NOT NULL,
    content    TEXT    NOT NULL DEFAULT '',
    tldr       TEXT    NOT NULL DEFAULT '',
    trigger    TEXT    NOT NULL DEFAULT '',
    session_id TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_tier_key ON memories (tier, key);
CREATE INDEX IF NOT EXISTS idx_memories_tier_ts ON memories (tier, ts DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    key,
    content,
    tldr,
    trigger,
    content='memories',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, key, content, tldr, trigger)
    VALUES (new.id, new.key, new.content, new.tldr, new.trigger);
END;

CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, key, content, tldr, trigger)
    VALUES ('delete', old.id, old.key, old.content, old.tldr, old.trigger);
END;

CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, key, content, tldr, trigger)
    VALUES ('delete', old.id, old.key, old.content, old.tldr, old.trigger);
    INSERT INTO memories_fts(rowid, key, content, tldr, trigger)
    VALUES (new.id, new.key, new.content, new.tldr, new.trigger);
END;

-- curation_events is the append-only instrumentation log of human curation
-- actions on memory (see curation.go). The memories table is a last-write-wins
-- upsert keyed on (tier, key), so overwrites and deletes erase all prior state;
-- every mutating curation action writes one immutable row here instead, with a
-- content and tldr snapshot taken at event time. Rows are never updated or
-- deleted except by rotation (Prune). This is capture only -- no learning or
-- weighting reads it yet.
CREATE TABLE IF NOT EXISTS curation_events (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,
    session_id  TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL,
    tier        TEXT    NOT NULL DEFAULT '',
    key         TEXT    NOT NULL,
    db_scope    TEXT    NOT NULL DEFAULT '',
    source      TEXT    NOT NULL DEFAULT '',
    content     TEXT    NOT NULL DEFAULT '',
    tldr        TEXT    NOT NULL DEFAULT '',
    trigger     TEXT    NOT NULL DEFAULT '',
    from_tier   TEXT    NOT NULL DEFAULT '',
    to_tier     TEXT    NOT NULL DEFAULT '',
    from_db     TEXT    NOT NULL DEFAULT '',
    to_db       TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_curation_events_ts      ON curation_events (ts DESC);
CREATE INDEX IF NOT EXISTS idx_curation_events_session ON curation_events (session_id);

-- projects is the dump/restore manifest, populated only in the global DB. It is
-- the lazy index of every project that has a local engram DB, written once when
-- a project DB is born (see RegisterProject). identity is the cross-machine key
-- (git remote, else $HOME-relative path); path is where the .engram lives on
-- this machine ($HOME-relative, absolute if outside $HOME). The table exists in
-- every DB for schema uniformity but stays empty in project DBs.
--
-- The key is (identity, path), NOT identity alone: a single repo can have
-- several independent clones on one machine, all sharing one identity but
-- living at different paths. Each clone gets its own row so none silently evicts
-- the others from the manifest. Linked git worktrees are different: they share
-- the main checkout's path and project-local .engram state.
CREATE TABLE IF NOT EXISTS projects (
    id         INTEGER PRIMARY KEY,
    identity   TEXT    NOT NULL,
    path       TEXT    NOT NULL,
    last_seen  INTEGER NOT NULL,
    status     TEXT    NOT NULL DEFAULT 'live'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_identity_path ON projects (identity, path);

-- One durable classification per repository automation entry point. The
-- content digest is per candidate rather than one aggregate snapshot, so a
-- changed script retains its previous judgment and does not invalidate every
-- unrelated classification in the repository.
CREATE TABLE IF NOT EXISTS automation_catalog_entries (
    path           TEXT PRIMARY KEY,
    detected_kind  TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    classification TEXT NOT NULL,
    rationale      TEXT NOT NULL DEFAULT '',
    skill_key      TEXT NOT NULL DEFAULT '',
    invocation     TEXT NOT NULL DEFAULT '',
    reviewed_at INTEGER NOT NULL
);
