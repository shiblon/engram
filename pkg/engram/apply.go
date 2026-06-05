package engram

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// PendingRestore describes a staged project snapshot awaiting placement.
type PendingRestore struct {
	Identity       string // cross-machine key (git remote or $HOME-rel path)
	OriginalPath   string // where it lived on the source machine
	StagePath      string // local stage slot path ($HOME-relative)
	MatchesCurrent bool   // true when identity matches the current repo's identity
}

// ListPendingRestores returns all pending restore entries from the global manifest.
func ListPendingRestores(ctx context.Context, globalDB *sql.DB) ([]PendingRestore, error) {
	rows, err := globalDB.QueryContext(ctx,
		`SELECT identity, path FROM projects WHERE status = 'pending' ORDER BY last_seen DESC`)
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer rows.Close()

	home, _ := os.UserHomeDir()
	var out []PendingRestore
	for rows.Next() {
		var p PendingRestore
		var stagePath string
		if err := rows.Scan(&p.Identity, &stagePath); err != nil {
			return nil, err
		}
		// The stage path in the manifest is $HOME-relative; resolve to absolute
		// so we can read the project.json sidecar for the original path.
		absStage := absProjectRoot(stagePath, home)
		p.StagePath = stagePath

		// Read original path from the sidecar if present.
		if data, err := os.ReadFile(filepath.Join(absStage, "project.json")); err == nil {
			var sp SaveProject
			if json.Unmarshal(data, &sp) == nil {
				p.OriginalPath = sp.Path
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ApplyRestore places the staged project snapshot identified by identity into
// root (the target working tree).
//
// Empty target (no curated memories): the staged mem.db is moved into
// root/.engram/mem.db, the pending manifest entry is updated to live.
//
// Populated target (has curated memories): the staged snapshot is re-identified
// to a new slot ("slug (1)", etc.) and kept pending; the caller is informed via
// ApplyResult.Conflicted.
//
// In both cases the staged slot directory on disk is cleaned up on success.
func ApplyRestore(ctx context.Context, globalDB *sql.DB, identity, root string) (ApplyResult, error) {
	var res ApplyResult

	home, _ := os.UserHomeDir()
	stageDir := globalStageDir(home)

	// Find the pending entry.
	var stagePath string
	err := globalDB.QueryRowContext(ctx,
		`SELECT path FROM projects WHERE identity = ? AND status = 'pending'`,
		identity).Scan(&stagePath)
	if err == sql.ErrNoRows {
		return res, fmt.Errorf("apply: no pending entry for identity %q", identity)
	}
	if err != nil {
		return res, fmt.Errorf("apply: look up pending entry: %w", err)
	}
	absStage := absProjectRoot(stagePath, home)
	stagedMemDB := filepath.Join(absStage, "mem.db")

	if _, err := os.Stat(stagedMemDB); err != nil {
		return res, fmt.Errorf("apply: staged mem.db not found at %s", stagedMemDB)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return res, fmt.Errorf("apply: resolve root: %w", err)
	}

	// Check whether the target already has curated content.
	engDir := filepath.Join(absRoot, ".engram")
	targetDBPath := DBPath(absRoot)
	populated := false
	if _, statErr := os.Stat(targetDBPath); statErr == nil {
		tdb, openErr := openRaw(ctx, targetDBPath)
		if openErr == nil {
			empty, _ := dbHasNoCuratedContent(ctx, tdb)
			populated = !empty
			tdb.Close()
		}
	}

	if populated {
		// Re-id: park the snapshot under a new slot name, keep it pending.
		newSlug := uniqueStageSlug(stageDir, identitySlug(identity))
		newSlot := filepath.Join(stageDir, newSlug)
		if err := os.Rename(absStage, newSlot); err != nil {
			return res, fmt.Errorf("apply: re-id stage slot: %w", err)
		}
		newPath := homeRelPath(newSlot)
		_, err := globalDB.ExecContext(ctx,
			`UPDATE projects SET path = ?, last_seen = ? WHERE identity = ?`,
			newPath, time.Now().UnixMilli(), identity)
		if err != nil {
			return res, fmt.Errorf("apply: update re-id path: %w", err)
		}
		res.Conflicted = true
		res.NewStagePath = newPath
		return res, nil
	}

	// Empty target: place the snapshot.
	if err := os.MkdirAll(engDir, 0o755); err != nil {
		return res, fmt.Errorf("apply: create .engram dir: %w", err)
	}

	destDB := targetDBPath
	data, err := os.ReadFile(stagedMemDB)
	if err != nil {
		return res, fmt.Errorf("apply: read staged mem.db: %w", err)
	}
	if err := os.WriteFile(destDB, data, 0o600); err != nil {
		return res, fmt.Errorf("apply: write mem.db: %w", err)
	}

	// Write the .gitignore for the .engram dir if absent (mirrors openWithFallback).
	gi := filepath.Join(engDir, ".gitignore")
	if _, err := os.Stat(gi); os.IsNotExist(err) {
		if err := os.WriteFile(gi, []byte("*\n"), 0o644); err != nil {
			log.Printf("engram: write .gitignore for %s: %v", engDir, err)
		}
	}

	// Update the manifest: this entry is now live at its real root.
	liveIdentity := ProjectIdentity(absRoot)
	livePath := homeRelPath(absRoot)
	_, err = globalDB.ExecContext(ctx,
		`UPDATE projects SET identity = ?, path = ?, last_seen = ?, status = 'live'
		 WHERE identity = ?`,
		liveIdentity, livePath, time.Now().UnixMilli(), identity)
	if err != nil {
		return res, fmt.Errorf("apply: update manifest: %w", err)
	}

	// Remove the stage slot now that placement succeeded.
	if err := os.RemoveAll(absStage); err != nil {
		log.Printf("engram: remove stage slot %s: %v", absStage, err)
	}

	res.Applied = true
	return res, nil
}

// ApplyResult reports what ApplyRestore did.
type ApplyResult struct {
	Applied      bool   // snapshot was placed into the working tree
	Conflicted   bool   // target had curated content; snapshot re-id'd to new slot
	NewStagePath string // set when Conflicted; the new pending slot path
}

// DiscardRestore removes the staged snapshot for identity and deletes its
// manifest row. It is a no-op (no error) when no pending entry exists.
func DiscardRestore(ctx context.Context, globalDB *sql.DB, identity string) error {
	home, _ := os.UserHomeDir()

	var stagePath string
	err := globalDB.QueryRowContext(ctx,
		`SELECT path FROM projects WHERE identity = ? AND status = 'pending'`,
		identity).Scan(&stagePath)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("discard: look up entry: %w", err)
	}

	absStage := absProjectRoot(stagePath, home)
	if err := os.RemoveAll(absStage); err != nil {
		log.Printf("engram: discard stage slot %s: %v", absStage, err)
	}

	_, err = globalDB.ExecContext(ctx,
		`DELETE FROM projects WHERE identity = ? AND status = 'pending'`, identity)
	return err
}

// globalStageDir returns the project-stage directory path under $HOME/.engram.
func globalStageDir(home string) string {
	return filepath.Join(home, ".engram", "project-stage")
}
