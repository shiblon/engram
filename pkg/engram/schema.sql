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