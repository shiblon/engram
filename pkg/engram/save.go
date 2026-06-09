package engram

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// SaveMeta is written as meta.json at the root of the archive.
type SaveMeta struct {
	EngramVersion string        `json:"engram_version"`
	SaveHost      string        `json:"save_host"`
	SaveTime      int64         `json:"save_time"` // unix epoch ms
	Projects      []SaveProject `json:"projects"`
}

// SaveProject records the identity and machine-local path of one project entry.
type SaveProject struct {
	Identity string `json:"identity"`
	Path     string `json:"path"` // $HOME-relative (or absolute when outside $HOME)
}

// SaveResult summarises what was written.
type SaveResult struct {
	ProjectCount    int      // projects actually snapshotted into the archive
	PrunedCount     int
	Skipped         []string // projects dropped from the archive (open/vacuum failure), with reason
	ContextWarnings []string // non-empty when context/ exists but --include-context was false
}

// SaveOptions controls optional behaviour of Save.
type SaveOptions struct {
	// IncludeContext, when true, bundles each project's context/ directory
	// (long.md + agenttools) into the archive. Defaults to false because these
	// files are version-controlled and already survive across machines.
	IncludeContext bool
	// EngramVersion is the binary version string embedded in meta.json.
	// Pass runtime/debug.ReadBuildInfo() result; empty string is fine.
	EngramVersion string
}

// Save snapshots all machine-local engram state into a gzipped tar written to
// w. It walks the global manifest, prunes dead entries, and for each surviving
// project takes a WAL-safe SQLite snapshot via VACUUM INTO. The global DB,
// agenttools, toolcandidates, and the project-stage directory are also included.
//
// Save never modifies any source database; the VACUUM INTO target is always a
// fresh temp file.
func Save(ctx context.Context, w io.Writer, opts SaveOptions) (SaveResult, error) {
	var res SaveResult

	globalDB, err := OpenGlobalDB(ctx)
	if err != nil {
		return res, fmt.Errorf("save: open global db: %w", err)
	}
	defer globalDB.Close()

	// Walk + prune manifest.
	projects, pruned, err := manifestEntries(ctx, globalDB)
	if err != nil {
		return res, fmt.Errorf("save: read manifest: %w", err)
	}
	res.PrunedCount = pruned

	// Resolve well-known global paths.
	globalDBPath, err := GlobalDBPath()
	if err != nil {
		return res, fmt.Errorf("save: global db path: %w", err)
	}
	globalToolsDir, err := GlobalAgentToolsDir()
	if err != nil {
		return res, fmt.Errorf("save: global agenttools dir: %w", err)
	}
	home, _ := os.UserHomeDir()
	stageDir := filepath.Join(filepath.Dir(globalDBPath), "project-stage")

	// Build meta.
	host, _ := os.Hostname()
	meta := SaveMeta{
		EngramVersion: opts.EngramVersion,
		SaveHost:      host,
		SaveTime:      time.Now().UnixMilli(),
	}
	// meta.Projects is populated after the snapshot loop below, from the projects
	// that actually made it into the archive -- so meta never lists a project the
	// archive doesn't contain.

	// All SQLite snapshots land in a temp dir; cleaned up after the tar is written.
	tmpDir, err := os.MkdirTemp("", "engram-save-*")
	if err != nil {
		return res, fmt.Errorf("save: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Global DB snapshot.
	globalSnap := filepath.Join(tmpDir, "global.db")
	if err := vacuumInto(ctx, globalDB, globalSnap); err != nil {
		return res, fmt.Errorf("save: snapshot global db: %w", err)
	}

	// Project DB snapshots.
	type projectSnap struct {
		entry   manifestEntry
		snapPath string
	}
	var snaps []projectSnap
	for i, p := range projects {
		absRoot := absProjectRoot(p.path, home)
		dbPath := DBPath(absRoot)
		snapPath := filepath.Join(tmpDir, fmt.Sprintf("project-%d.db", i))

		pdb, err := openRaw(ctx, dbPath)
		if err != nil {
			// Record the skip rather than dropping it silently: a backup that
			// omits a project must say so, not report success.
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%s): open: %v", p.identity, p.path, err))
			continue
		}
		vacErr := vacuumInto(ctx, pdb, snapPath)
		pdb.Close()
		if vacErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%s): snapshot: %v", p.identity, p.path, vacErr))
			continue
		}
		snaps = append(snaps, projectSnap{entry: p, snapPath: snapPath})

		// Context warning: exists but not included.
		if !opts.IncludeContext {
			contextDir := filepath.Join(absRoot, "context")
			if _, err := os.Stat(contextDir); err == nil {
				res.ContextWarnings = append(res.ContextWarnings,
					fmt.Sprintf("%s has context/ — use --include-context to bundle it", p.path))
			}
		}
	}

	// Count and describe only what actually made it into the archive, so
	// ProjectCount never overstates and meta.Projects matches the projects/<n>/
	// entries restore will read.
	res.ProjectCount = len(snaps)
	for _, ps := range snaps {
		meta.Projects = append(meta.Projects, SaveProject{Identity: ps.entry.identity, Path: ps.entry.path})
	}

	// Write archive.
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	// meta.json
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return res, fmt.Errorf("save: marshal meta: %w", err)
	}
	if err := tarWriteBytes(tw, "meta.json", metaBytes); err != nil {
		return res, err
	}

	// global/mem.db (snapshot)
	if err := tarWriteFile(tw, "global/mem.db", globalSnap); err != nil {
		return res, err
	}
	// global/agenttools/
	if err := tarWriteDir(tw, "global/agenttools", globalToolsDir); err != nil {
		return res, err
	}
	// project-stage/ (pending restores must hop A->B->C)
	if err := tarWriteDir(tw, "project-stage", stageDir); err != nil {
		return res, err
	}

	// projects/<n>/
	for i, ps := range snaps {
		prefix := fmt.Sprintf("projects/%d", i)
		absRoot := absProjectRoot(ps.entry.path, home)

		if err := tarWriteFile(tw, prefix+"/mem.db", ps.snapPath); err != nil {
			return res, err
		}
		// toolcandidates/
		if err := tarWriteDir(tw, prefix+"/toolcandidates", ProjectToolCandidatesDir(absRoot)); err != nil {
			return res, err
		}
		// context/ (opt-in)
		if opts.IncludeContext {
			if err := tarWriteDir(tw, prefix+"/context", filepath.Join(absRoot, "context")); err != nil {
				return res, err
			}
		}
		// identity sidecar so restore knows which project this is
		sidecar, _ := json.Marshal(SaveProject{Identity: ps.entry.identity, Path: ps.entry.path})
		if err := tarWriteBytes(tw, prefix+"/project.json", sidecar); err != nil {
			return res, err
		}
	}

	if err := tw.Close(); err != nil {
		return res, fmt.Errorf("save: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return res, fmt.Errorf("save: close gzip: %w", err)
	}
	return res, nil
}

// manifestEntry is an unexported view of a projects row.
type manifestEntry struct {
	identity string
	path     string // $HOME-relative or absolute
}

// manifestEntries reads all surviving projects from the global manifest,
// deletes rows whose .engram directory no longer exists, and returns the live
// entries plus the count of pruned rows.
func manifestEntries(ctx context.Context, globalDB *sql.DB) ([]manifestEntry, int, error) {
	home, _ := os.UserHomeDir()

	rows, err := globalDB.QueryContext(ctx,
		`SELECT identity, path FROM projects ORDER BY last_seen DESC`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var live, dead []manifestEntry
	for rows.Next() {
		var e manifestEntry
		if err := rows.Scan(&e.identity, &e.path); err != nil {
			return nil, 0, err
		}
		absRoot := absProjectRoot(e.path, home)
		if _, err := os.Stat(filepath.Join(absRoot, ".engram")); os.IsNotExist(err) {
			dead = append(dead, e)
		} else {
			live = append(live, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Prune by (identity, path): a repo may have several copies sharing one
	// identity, so deleting by identity alone would evict live siblings too.
	for _, e := range dead {
		if _, err := globalDB.ExecContext(ctx,
			`DELETE FROM projects WHERE identity = ? AND path = ?`, e.identity, e.path); err != nil {
			log.Printf("engram: prune dead manifest entry %q (%s): %v", e.identity, e.path, err)
		}
	}
	return live, len(dead), nil
}

// absProjectRoot resolves a manifest path (either $HOME-relative or already
// absolute) back to an absolute filesystem path.
func absProjectRoot(manifestPath, home string) string {
	if filepath.IsAbs(manifestPath) {
		return manifestPath
	}
	if home == "" {
		return manifestPath
	}
	return filepath.Join(home, manifestPath)
}

// openRaw opens a SQLite file without applying schema migrations (read-only
// snapshot source). The WAL-safe copy happens via VACUUM INTO; we never write
// to source DBs from save.
func openRaw(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// vacuumInto creates a WAL-safe consistent snapshot of src at destPath using
// VACUUM INTO. destPath must not already exist.
func vacuumInto(ctx context.Context, src *sql.DB, destPath string) error {
	_, err := src.ExecContext(ctx, `VACUUM INTO ?`, destPath)
	if err != nil {
		return fmt.Errorf("VACUUM INTO %s: %w", destPath, err)
	}
	return nil
}

// tarWriteBytes adds a regular file entry with the given content to tw.
func tarWriteBytes(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(data)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("save: tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("save: tar write %s: %w", name, err)
	}
	return nil
}

// tarWriteFile adds a single regular file from srcPath into the archive as name.
func tarWriteFile(tw *tar.Writer, name, srcPath string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("save: open %s: %w", srcPath, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("save: stat %s: %w", srcPath, err)
	}
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     fi.Size(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("save: tar header %s: %w", name, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("save: tar write %s: %w", name, err)
	}
	return nil
}

// tarWriteDir recursively adds all regular files under srcDir into the archive
// under prefix. An absent or empty srcDir is silently skipped.
func tarWriteDir(tw *tar.Writer, prefix, srcDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil // absent directory is fine
	}
	for _, e := range entries {
		if e.IsDir() {
			if err := tarWriteDir(tw, prefix+"/"+e.Name(), filepath.Join(srcDir, e.Name())); err != nil {
				return err
			}
			continue
		}
		if err := tarWriteFile(tw, prefix+"/"+e.Name(), filepath.Join(srcDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
