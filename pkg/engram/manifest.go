package engram

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProjectIdentity returns the cross-machine identity for the project rooted at
// root: its git remote URL when one can be read from root/.git/config, else the
// project's $HOME-relative path. Identity is the manifest key that lets a saved
// project be recognized on another machine, where absolute paths differ.
//
// The git remote is read straight from the config file (a plain FILE READ, no
// subprocess), so it works regardless of whether git is installed. When .git is
// a file rather than a directory (worktrees, submodules) or the config has no
// remote, the read fails cleanly and we fall back to the path.
func ProjectIdentity(root string) string {
	if url, ok := gitRemoteURL(filepath.Join(root, ".git", "config")); ok {
		return url
	}
	return homeRelPath(root)
}

// homeRelPath returns root expressed relative to $HOME, or the absolute path
// when root lies outside $HOME (or $HOME is unknown). This keeps stored paths
// portable across machines whose home directories differ.
func homeRelPath(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if rel, rerr := filepath.Rel(home, abs); rerr == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return abs
}

// gitRemoteURL parses a git config file and returns a remote URL, preferring
// "origin". It returns false when the file cannot be read or declares no remote
// URL. The parse is deliberately minimal -- enough for the [remote "name"] /
// url = ... shape git writes -- not a full git-config implementation.
func gitRemoteURL(configPath string) (string, bool) {
	f, err := os.Open(configPath)
	if err != nil {
		return "", false
	}
	defer f.Close()

	var section string
	var origin, fallback string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = remoteSubsection(line)
			continue
		}
		if section == "" {
			continue // not inside a [remote "..."] section
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "url" {
			continue
		}
		url := strings.TrimSpace(val)
		if url == "" {
			continue
		}
		if section == "origin" && origin == "" {
			origin = url
		}
		if fallback == "" {
			fallback = url
		}
	}
	if origin != "" {
		return origin, true
	}
	if fallback != "" {
		return fallback, true
	}
	return "", false
}

// remoteSubsection extracts the remote name from a section header line such as
// [remote "origin"], returning "" for any other section.
func remoteSubsection(line string) string {
	rest, ok := strings.CutPrefix(line, "[remote ")
	if !ok {
		return ""
	}
	i := strings.IndexByte(rest, '"')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(rest[i+1:], '"')
	if j < 0 {
		return ""
	}
	return rest[i+1 : i+1+j]
}

// RegisterProject records (or refreshes) root's entry in the global manifest.
// It is the single write behind dump/restore enumeration: a one-time structural
// footprint born with the project DB, never a per-command side effect.
//
// The write is idempotent and self-healing, resolving three cases without any
// human in the loop (it runs from DB-open and hooks):
//   - Re-key: a row exists for this path under a stale identity (the repo gained
//     or changed a git remote). The identity is updated in place.
//   - Move: this (identity, path) is new and the identity has exactly one row
//     whose .engram is gone from disk -- the project moved. That row is relocated
//     rather than left stale. See relocateMovedProject for why a surviving old
//     .engram (a genuine second copy) and multi-row identities are excluded.
//   - Otherwise: upsert keyed by (identity, path), so a repo with several
//     checkouts (clones / worktrees) keeps one row per copy instead of having a
//     later copy overwrite an earlier one.
//
// Callers treat the error as advisory: manifest bookkeeping must never fail an
// otherwise-successful DB open.
func RegisterProject(ctx context.Context, globalDB *sql.DB, root string) error {
	identity := ProjectIdentity(root)
	path := homeRelPath(root)
	now := time.Now().UnixMilli()

	// Re-key: an existing entry for this path whose identity has since drifted.
	res, err := globalDB.ExecContext(ctx,
		`UPDATE projects SET identity = ?, last_seen = ? WHERE path = ? AND identity <> ?`,
		identity, now, path, identity)
	if err != nil {
		return fmt.Errorf("register project (re-key): %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}

	// Move: relocate a single stale row for this identity instead of inserting.
	if relocated, err := relocateMovedProject(ctx, globalDB, identity, path, now); err != nil {
		return err
	} else if relocated {
		return nil
	}

	// Upsert keyed by (identity, path): one row per working copy.
	if _, err := globalDB.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen) VALUES (?, ?, ?)
		 ON CONFLICT(identity, path) DO UPDATE SET last_seen = excluded.last_seen`,
		identity, path, now); err != nil {
		return fmt.Errorf("register project: %w", err)
	}
	return nil
}

// relocateMovedProject handles RegisterProject's "the project moved on disk"
// case. It reports true (and updates the row in place) only when newPath is not
// yet recorded for identity AND the identity has exactly one existing row whose
// .engram no longer exists -- the unambiguous signature of a move. Every other
// shape reports false so the caller inserts a fresh row:
//   - the identity already has this path (a re-register),
//   - the lone existing row's .engram is still on disk (a genuine second copy),
//   - the identity has several rows (can't tell which, if any, moved -- let the
//     dead-row prune at save time reap stale ones).
//
// A stat error other than "not exist" (e.g. a permission issue) is treated as
// "still present", the safe default: relocate only on a confirmed absence.
func relocateMovedProject(ctx context.Context, globalDB *sql.DB, identity, newPath string, now int64) (bool, error) {
	home, _ := os.UserHomeDir()

	rows, err := globalDB.QueryContext(ctx, `SELECT path FROM projects WHERE identity = ?`, identity)
	if err != nil {
		return false, fmt.Errorf("register project (move check): %w", err)
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return false, err
		}
		paths = append(paths, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	if len(paths) != 1 || paths[0] == newPath {
		return false, nil // re-register, or ambiguous multi-row identity
	}
	oldAbs := absProjectRoot(paths[0], home)
	if _, err := os.Stat(filepath.Join(oldAbs, ".engram")); !os.IsNotExist(err) {
		return false, nil // old copy still present -> genuine second copy
	}

	if _, err := globalDB.ExecContext(ctx,
		`UPDATE projects SET path = ?, last_seen = ? WHERE identity = ? AND path = ?`,
		newPath, now, identity, paths[0]); err != nil {
		return false, fmt.Errorf("register project (relocate): %w", err)
	}
	return true, nil
}

// IsProjectRegistered reports whether the working copy at root is already
// recorded as live in the global manifest. It is used by inject to skip the
// registration side-effect for copies that are already known.
//
// The check is keyed by (identity, path), not identity alone: a second checkout
// of an already-registered repo is a distinct working copy that still needs its
// own row, so it must not be treated as already registered.
func IsProjectRegistered(ctx context.Context, globalDB *sql.DB, root string) bool {
	var n int
	_ = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects WHERE identity = ? AND path = ? AND status = 'live'`,
		ProjectIdentity(root), homeRelPath(root)).Scan(&n)
	return n > 0
}

// registerSelf records root in the global manifest, best-effort. It opens a
// short-lived global handle so it never contends with the project handle, and
// swallows all errors so manifest bookkeeping never fails a project DB open.
func registerSelf(ctx context.Context, root string) {
	gdb, err := OpenGlobalDB(ctx)
	if err != nil {
		return
	}
	defer gdb.Close()
	if err := RegisterProject(ctx, gdb, root); err != nil {
		log.Printf("engram: register project %s: %v", root, err)
	}
}
