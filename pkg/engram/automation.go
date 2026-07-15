package engram

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AutomationCandidate is a repository entry point worth classifying as a
// direct tool, part of a larger skill, internal plumbing, or intentionally
// ignored. Path is relative to the project root.
type AutomationCandidate struct {
	Path string
	Kind string
}

// AutomationReview is surfaced by inject when the repository's current set of
// automation entry points has not been reviewed. Digest identifies the exact
// candidate contents, not merely their paths.
type AutomationReview struct {
	Candidates []AutomationCandidate
	Digest     string
}

var automationFiles = map[string]string{
	"GNUmakefile":   "task runner",
	"Justfile":      "task runner",
	"Makefile":      "task runner",
	"Taskfile.yaml": "task runner",
	"Taskfile.yml":  "task runner",
	"justfile":      "task runner",
	"makefile":      "task runner",
	"package.json":  "package scripts",
}

var scriptExtensions = map[string]bool{
	".bash": true,
	".js":   true,
	".mjs":  true,
	".pl":   true,
	".py":   true,
	".rb":   true,
	".sh":   true,
	".ts":   true,
}

// DiscoverAutomation finds conventional automation entry points without
// executing them. It deliberately starts narrow: root task-runner manifests
// plus runnable-looking files below scripts/ and bin/. More sources can be
// added once their signal-to-noise ratio is understood.
func DiscoverAutomation(root string) ([]AutomationCandidate, string, error) {
	var candidates []AutomationCandidate
	for name, kind := range automationFiles {
		path := filepath.Join(root, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			candidates = append(candidates, AutomationCandidate{Path: filepath.ToSlash(name), Kind: kind})
		}
	}

	for _, dir := range []string{"bin", "scripts"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if path == base {
				return nil
			}
			if entry.IsDir() {
				if strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || !looksRunnable(path, info.Mode()) {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			candidates = append(candidates, AutomationCandidate{Path: filepath.ToSlash(rel), Kind: "script"})
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, "", fmt.Errorf("discover automation in %s: %w", dir, err)
		}
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
	digest, err := automationDigest(root, candidates)
	if err != nil {
		return nil, "", err
	}
	return candidates, digest, nil
}

func looksRunnable(path string, mode fs.FileMode) bool {
	if scriptExtensions[strings.ToLower(filepath.Ext(path))] || mode&0o111 != 0 {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var prefix [2]byte
	_, err = io.ReadFull(f, prefix[:])
	return err == nil && string(prefix[:]) == "#!"
}

func automationDigest(root string, candidates []AutomationCandidate) (string, error) {
	h := sha256.New()
	for _, candidate := range candidates {
		path := filepath.Join(root, filepath.FromSlash(candidate.Path))
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("hash automation candidate %s: %w", candidate.Path, err)
		}
		_, _ = io.WriteString(h, candidate.Kind+"\x00"+candidate.Path+"\x00")
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", fmt.Errorf("hash automation candidate %s: %w", candidate.Path, err)
		}
		f.Close()
		_, _ = io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// AutomationCatalogReviewed reports whether digest is the candidate snapshot
// most recently acknowledged as cataloged in this project database.
func AutomationCatalogReviewed(ctx context.Context, db *sql.DB, digest string) (bool, error) {
	var reviewed string
	err := db.QueryRowContext(ctx, `SELECT digest FROM automation_catalog_state WHERE id = 1`).Scan(&reviewed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read automation catalog state: %w", err)
	}
	return reviewed == digest, nil
}

// MarkAutomationCatalogReviewed acknowledges that every candidate in digest
// has been classified or deliberately ignored. It stores bookkeeping, not a
// memory: no prose from this row is injected.
func MarkAutomationCatalogReviewed(ctx context.Context, db *sql.DB, digest string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO automation_catalog_state (id, digest, reviewed_at)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET digest = excluded.digest, reviewed_at = excluded.reviewed_at`,
		digest, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("mark automation catalog reviewed: %w", err)
	}
	return nil
}
