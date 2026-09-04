package engram

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GuidanceRead records delivery of one lazy-loaded agentinfo body. It measures
// that the reference entered the agent's context, not whether the model attended
// to or followed it.
type GuidanceRead struct {
	Topic   string
	Version string
	TS      int64
}

// GuidanceReadStat is the per-topic, per-version histogram retained locally.
type GuidanceReadStat struct {
	Topic       string `json:"topic"`
	Version     string `json:"version"`
	Loads       int64  `json:"loads"`
	FirstLoaded int64  `json:"first_loaded"`
	LastLoaded  int64  `json:"last_loaded"`
}

// RecordGuidanceRead increments the compact histogram for one delivered body.
func RecordGuidanceRead(ctx context.Context, db *sql.DB, read GuidanceRead) error {
	read.Topic = strings.TrimSpace(read.Topic)
	read.Version = strings.TrimSpace(read.Version)
	if read.Topic == "" {
		return fmt.Errorf("record guidance read: topic is required")
	}
	if read.Version == "" {
		return fmt.Errorf("record guidance read: version is required")
	}
	if read.TS == 0 {
		read.TS = time.Now().UnixMilli()
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO guidance_reads
			(topic, engram_version, loads, first_loaded, last_loaded)
		VALUES (?, ?, 1, ?, ?)
		ON CONFLICT(topic, engram_version) DO UPDATE SET
			loads = guidance_reads.loads + 1,
			first_loaded = MIN(guidance_reads.first_loaded, excluded.first_loaded),
			last_loaded = MAX(guidance_reads.last_loaded, excluded.last_loaded)`,
		read.Topic, read.Version, read.TS, read.TS)
	if err != nil {
		return fmt.Errorf("record guidance read: %w", err)
	}
	return nil
}

// ListGuidanceReadStats returns observed body loads for one engram version.
// A pre-v10 read-only database has no guidance_reads table; that is equivalent
// to an empty histogram until the next writable open applies the migration.
func ListGuidanceReadStats(ctx context.Context, db *sql.DB, version string) ([]GuidanceReadStat, error) {
	var tableCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'guidance_reads'`).Scan(&tableCount); err != nil {
		return nil, fmt.Errorf("list guidance read stats: detect table: %w", err)
	}
	if tableCount == 0 {
		return nil, nil
	}

	rows, err := db.QueryContext(ctx, `
		SELECT topic, engram_version, loads, first_loaded, last_loaded
		FROM guidance_reads
		WHERE engram_version = ?
		ORDER BY topic`, version)
	if err != nil {
		return nil, fmt.Errorf("list guidance read stats: %w", err)
	}
	defer rows.Close()

	var stats []GuidanceReadStat
	for rows.Next() {
		var stat GuidanceReadStat
		if err := rows.Scan(&stat.Topic, &stat.Version, &stat.Loads, &stat.FirstLoaded, &stat.LastLoaded); err != nil {
			return nil, fmt.Errorf("list guidance read stats: scan: %w", err)
		}
		stats = append(stats, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list guidance read stats: rows: %w", err)
	}
	return stats, nil
}
