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
    session_id TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_tier_key ON memories (tier, key);
CREATE INDEX IF NOT EXISTS idx_memories_tier_ts ON memories (tier, ts DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    key,
    content,
    content='memories',
    content_rowid='id'
);

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

-- projects is the dump/restore manifest, populated only in the global DB. It is
-- the lazy index of every project that has a local engram DB, written once when
-- a project DB is born (see RegisterProject). identity is the cross-machine key
-- (git remote, else $HOME-relative path); path is where the .engram lives on
-- this machine ($HOME-relative, absolute if outside $HOME). The table exists in
-- every DB for schema uniformity but stays empty in project DBs.
--
-- The key is (identity, path), NOT identity alone: a single repo can have
-- several working copies on one machine (separate clones or git worktrees on
-- parallel branches), all sharing one identity but living at different paths.
-- Each gets its own row so none silently evicts the others from the manifest.
CREATE TABLE IF NOT EXISTS projects (
    id         INTEGER PRIMARY KEY,
    identity   TEXT    NOT NULL,
    path       TEXT    NOT NULL,
    last_seen  INTEGER NOT NULL,
    status     TEXT    NOT NULL DEFAULT 'live'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_identity_path ON projects (identity, path);