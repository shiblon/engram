package engram

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PendingRestore describes a staged project snapshot awaiting placement. When a
// repo has several saved working copies they share an Identity but differ in
// Slot (their unique stage-slot name) and OriginalPath -- the two handles a
// caller uses to choose which copy to apply.
type PendingRestore struct {
	Identity       string // cross-machine key (git remote or $HOME-rel path)
	Slot           string // unique stage-slot name; disambiguates copies of one identity
	OriginalPath   string // where it lived on the source machine
	StagePath      string // local stage slot path ($HOME-relative)
	LastSeen       int64  // unix millis, for recency ordering
	MatchesCurrent bool   // true when identity matches the current repo's identity
}

// RestoreSelector picks one staged copy when an identity has several. Either
// field (or neither, when the identity is unambiguous) may be set; Slot is the
// exact stage-slot name, OriginalPath the copy's path on the source machine.
type RestoreSelector struct {
	Slot         string
	OriginalPath string
}

func (s RestoreSelector) empty() bool { return s.Slot == "" && s.OriginalPath == "" }

// ListPendingRestores returns all pending restore entries from the global manifest.
func ListPendingRestores(ctx context.Context, globalDB *sql.DB) ([]PendingRestore, error) {
	rows, err := globalDB.QueryContext(ctx,
		`SELECT identity, path, last_seen FROM projects WHERE status = 'pending' ORDER BY last_seen DESC`)
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer rows.Close()

	home, _ := os.UserHomeDir()
	var out []PendingRestore
	for rows.Next() {
		var p PendingRestore
		var stagePath string
		if err := rows.Scan(&p.Identity, &stagePath, &p.LastSeen); err != nil {
			return nil, err
		}
		// The stage path in the manifest is $HOME-relative; resolve to absolute
		// so we can read the project.json sidecar for the original path.
		absStage := absProjectRoot(stagePath, home)
		p.StagePath = stagePath
		p.Slot = filepath.Base(stagePath)

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

// resolvePending finds the single pending stage path for identity, narrowed by
// sel. It returns a descriptive error when nothing matches, or when the identity
// has multiple copies and sel does not single one out -- the error lists the
// candidates (slot + original path) so the caller can re-issue with a selector.
func resolvePending(ctx context.Context, globalDB *sql.DB, identity string, sel RestoreSelector) (stagePath string, err error) {
	all, err := ListPendingRestores(ctx, globalDB)
	if err != nil {
		return "", err
	}
	var matches []PendingRestore
	for _, p := range all {
		if p.Identity != identity {
			continue
		}
		if sel.Slot != "" && p.Slot != sel.Slot {
			continue
		}
		if sel.OriginalPath != "" && p.OriginalPath != sel.OriginalPath {
			continue
		}
		matches = append(matches, p)
	}

	switch len(matches) {
	case 0:
		if sel.empty() {
			return "", fmt.Errorf("apply: no pending entry for identity %q", identity)
		}
		return "", fmt.Errorf("apply: no pending entry for identity %q matching the given selector", identity)
	case 1:
		return matches[0].StagePath, nil
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "apply: identity %q has %d staged copies; select one with --slot or --from:", identity, len(matches))
		for _, m := range matches {
			fmt.Fprintf(&b, "\n  --slot %s   (--from %s)", m.Slot, m.OriginalPath)
		}
		return "", fmt.Errorf("%s", b.String())
	}
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
func ApplyRestore(ctx context.Context, globalDB *sql.DB, identity string, sel RestoreSelector, root string) (ApplyResult, error) {
	var res ApplyResult

	home, _ := os.UserHomeDir()
	stageDir := globalStageDir(home)

	// Find the pending entry, narrowed by sel when the identity has several copies.
	stagePath, err := resolvePending(ctx, globalDB, identity, sel)
	if err != nil {
		return res, err
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
	storageRoot := ProjectStorageRoot(absRoot)

	// Check whether the target already has curated content.
	engDir := filepath.Join(storageRoot, ".engram")
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
			`UPDATE projects SET path = ?, last_seen = ? WHERE identity = ? AND path = ?`,
			newPath, time.Now().UnixMilli(), identity, stagePath)
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
	livePath := homeRelPath(storageRoot)
	_, err = globalDB.ExecContext(ctx,
		`UPDATE projects SET identity = ?, path = ?, last_seen = ?, status = 'live'
		 WHERE identity = ? AND path = ?`,
		liveIdentity, livePath, time.Now().UnixMilli(), identity, stagePath)
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
// manifest row, narrowed by sel when the identity has several staged copies. It
// is a no-op (no error) when no pending entry matches; it errors (listing the
// candidates) when sel fails to single one out among multiple.
func DiscardRestore(ctx context.Context, globalDB *sql.DB, identity string, sel RestoreSelector) error {
	home, _ := os.UserHomeDir()

	stagePath, err := resolvePending(ctx, globalDB, identity, sel)
	if err != nil {
		// No match at all is a no-op; ambiguity (and other errors) propagate.
		if sel.empty() && strings.Contains(err.Error(), "no pending entry") {
			return nil
		}
		return err
	}

	absStage := absProjectRoot(stagePath, home)
	if err := os.RemoveAll(absStage); err != nil {
		log.Printf("engram: discard stage slot %s: %v", absStage, err)
	}

	_, err = globalDB.ExecContext(ctx,
		`DELETE FROM projects WHERE identity = ? AND path = ? AND status = 'pending'`, identity, stagePath)
	return err
}

// globalStageDir returns the project-stage directory path under $HOME/.engram.
func globalStageDir(home string) string {
	return filepath.Join(home, ".engram", "project-stage")
}
