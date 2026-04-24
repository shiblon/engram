CREATE TABLE IF NOT EXISTS events (
    id          INTEGER PRIMARY KEY,
    session_id  TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    tool        TEXT    NOT NULL,
    file_path   TEXT    NOT NULL,
    snippet     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_events_session ON events (session_id);
CREATE INDEX IF NOT EXISTS idx_events_ts      ON events (ts DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
    file_path,
    snippet,
    content='events',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS events_ai AFTER INSERT ON events BEGIN
    INSERT INTO events_fts(rowid, file_path, snippet)
    VALUES (new.id, new.file_path, new.snippet);
END;

CREATE TRIGGER IF NOT EXISTS events_ad AFTER DELETE ON events BEGIN
    INSERT INTO events_fts(events_fts, rowid, file_path, snippet)
    VALUES ('delete', old.id, old.file_path, old.snippet);
END;

CREATE TRIGGER IF NOT EXISTS events_au AFTER UPDATE ON events BEGIN
    INSERT INTO events_fts(events_fts, rowid, file_path, snippet)
    VALUES ('delete', old.id, old.file_path, old.snippet);
    INSERT INTO events_fts(rowid, file_path, snippet)
    VALUES (new.id, new.file_path, new.snippet);
END;

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