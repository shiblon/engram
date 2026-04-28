package engram

import (
	"context"
	"database/sql"
	"fmt"
)

// MigrateResult summarizes a completed migration.
type MigrateResult struct {
	Events    int64
	Memories  int64
	Conflicts []string // tier/key entries where both DBs had a value; describes which was kept
}

// Migrate copies all events and memories from src to dst.
//
// Events are copied unconditionally (append-only log; run migrate only once).
// Memories are merged: the newer entry by ts wins; conflicts are reported in the result.
func Migrate(ctx context.Context, src, dst *sql.DB) (MigrateResult, error) {
	var result MigrateResult

	if err := migrateEvents(ctx, src, dst, &result); err != nil {
		return result, err
	}
	if err := migrateMemories(ctx, src, dst, &result); err != nil {
		return result, err
	}
	return result, nil
}

func migrateEvents(ctx context.Context, src, dst *sql.DB, result *MigrateResult) error {
	rows, err := src.QueryContext(ctx, `SELECT session_id, ts, tool, file_path, snippet FROM events ORDER BY ts`)
	if err != nil {
		return fmt.Errorf("migrate events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.SessionID, &e.TS, &e.Tool, &e.FilePath, &e.Snippet); err != nil {
			return fmt.Errorf("migrate events scan: %w", err)
		}
		if err := Record(ctx, dst, e); err != nil {
			return fmt.Errorf("migrate events record: %w", err)
		}
		result.Events++
	}
	return rows.Err()
}

func migrateMemories(ctx context.Context, src, dst *sql.DB, result *MigrateResult) error {
	srcMems, err := queryMemories(ctx, src, "", "", 0)
	if err != nil {
		return fmt.Errorf("migrate memories: %w", err)
	}
	for _, m := range srcMems {
		existing, err := ReadMemory(ctx, dst, m.Tier, m.Key)
		if err != nil {
			return fmt.Errorf("migrate memories check: %w", err)
		}
		if existing != nil {
			if existing.TS >= m.TS {
				result.Conflicts = append(result.Conflicts, fmt.Sprintf("%s/%s: destination kept (newer)", m.Tier, m.Key))
				continue
			}
			result.Conflicts = append(result.Conflicts, fmt.Sprintf("%s/%s: source kept (newer)", m.Tier, m.Key))
		}
		if err := WriteMemory(ctx, dst, m); err != nil {
			return fmt.Errorf("migrate memories write: %w", err)
		}
		result.Memories++
	}
	return nil
}
