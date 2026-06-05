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
// The write is idempotent and self-healing. When a row already exists for this
// path under a stale identity -- the repo gained or changed a git remote since
// it was first registered -- the entry is re-keyed in place. Otherwise it is
// upserted on identity (the manifest key), refreshing path and last_seen so a
// project that moved on disk under a stable identity stays correct.
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

	// Upsert keyed by identity, refreshing the on-disk path if it moved.
	if _, err := globalDB.ExecContext(ctx,
		`INSERT INTO projects (identity, path, last_seen) VALUES (?, ?, ?)
		 ON CONFLICT(identity) DO UPDATE SET path = excluded.path, last_seen = excluded.last_seen`,
		identity, path, now); err != nil {
		return fmt.Errorf("register project: %w", err)
	}
	return nil
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
