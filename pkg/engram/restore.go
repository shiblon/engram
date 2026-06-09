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
	"strings"
	"time"
)

// RestoreResult summarises what was applied.
type RestoreResult struct {
	GlobalApplied bool // true when the global DB + agenttools were restored
	GlobalSkipped bool // true when the global DB already had curated content
	StagedCount   int  // number of project snapshots added to project-stage
}

// Restore reads a save archive from r and applies it to the current machine:
//
//   - Global DB + agenttools: applied only when the current global DB contains
//     no curated memories (invariant/preference/long/cold tiers). If it already
//     has curated content, global restore is skipped and GlobalSkipped is set.
//
//   - Each project snapshot (from projects/<n>/ and any project-stage/ carry-
//     forward entries): copied to $HOME/.engram/project-stage/<slug>/ and
//     registered as a pending row in the global manifest. Duplicates by identity
//     are skipped so a project appearing in both sections is staged once.
//
// Phase 4 adds --apply to place a staged project into a working tree.
func Restore(ctx context.Context, r io.Reader) (RestoreResult, error) {
	var res RestoreResult

	globalDBPath, err := GlobalDBPath()
	if err != nil {
		return res, fmt.Errorf("restore: global db path: %w", err)
	}
	home, _ := os.UserHomeDir()
	stageDir := filepath.Join(filepath.Dir(globalDBPath), "project-stage")

	// Read the full archive into a name→data index so we can process sections
	// without re-reading the reader.
	type entry struct{ name string; data []byte }
	var all []entry
	gz, err := gzip.NewReader(r)
	if err != nil {
		return res, fmt.Errorf("restore: open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("restore: read archive: %w", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return res, fmt.Errorf("restore: read entry %s: %w", hdr.Name, err)
		}
		all = append(all, entry{hdr.Name, data})
	}
	byName := make(map[string][]byte, len(all))
	for _, e := range all {
		byName[e.name] = e.data
	}

	// Open (or create) the global DB before any filesystem writes so we can
	// check curated content and register pending entries transactionally.
	globalDB, err := OpenGlobalDB(ctx)
	if err != nil {
		return res, fmt.Errorf("restore: open global db: %w", err)
	}
	defer globalDB.Close()

	// Apply global state when the current global DB has no curated memories.
	if globalDBData, ok := byName["global/mem.db"]; ok {
		empty, err := dbHasNoCuratedContent(ctx, globalDB)
		if err != nil {
			return res, fmt.Errorf("restore: check global db: %w", err)
		}
		if empty {
			tmp := globalDBPath + ".restore-tmp"
			if err := os.WriteFile(tmp, globalDBData, 0o600); err != nil {
				return res, fmt.Errorf("restore: write global db: %w", err)
			}
			globalDB.Close()
			if err := os.Rename(tmp, globalDBPath); err != nil {
				if rerr := os.Remove(tmp); rerr != nil {
					log.Printf("engram: restore cleanup temp db %s: %v", tmp, rerr)
				}
				return res, fmt.Errorf("restore: install global db: %w", err)
			}
			globalDB, err = OpenGlobalDB(ctx)
			if err != nil {
				return res, fmt.Errorf("restore: reopen global db: %w", err)
			}
			res.GlobalApplied = true

			// Restore agenttools alongside the DB.
			globalToolsDir, _ := GlobalAgentToolsDir()
			if err := os.MkdirAll(globalToolsDir, 0o755); err != nil {
				return res, fmt.Errorf("restore: create agenttools dir: %w", err)
			}
			const toolPrefix = "global/agenttools/"
			for _, e := range all {
				if !strings.HasPrefix(e.name, toolPrefix) {
					continue
				}
				rel := strings.TrimPrefix(e.name, toolPrefix)
				if rel == "" {
					continue
				}
				dest := filepath.Join(globalToolsDir, rel)
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					return res, err
				}
				if err := os.WriteFile(dest, e.data, 0o755); err != nil {
					return res, fmt.Errorf("restore: write agenttool %s: %w", rel, err)
				}
			}
		} else {
			res.GlobalSkipped = true
		}
	}

	// Stage project snapshots. Process projects/<n>/ (live) first, then
	// project-stage/<slug>/ carry-forwards (pending from source). Deduplicate
	// by (identity, source path) so a copy appearing in both sections is staged
	// once -- but distinct working copies of one repo (same identity, different
	// source paths) are each staged, so the user can later choose which to apply.
	// A struct key (not a delimiter-joined string) keeps this free of separator
	// hazards if the dedup set ever moves out of memory into a store.
	type copyKey struct{ identity, path string }
	seen := map[copyKey]bool{}

	stage := func(memData []byte, sp SaveProject) error {
		key := copyKey{sp.Identity, sp.Path}
		if seen[key] {
			return nil
		}
		seen[key] = true

		slug := uniqueStageSlug(stageDir, identitySlug(sp.Identity))
		slotDir := filepath.Join(stageDir, slug)
		if err := os.MkdirAll(slotDir, 0o755); err != nil {
			return fmt.Errorf("restore: create stage slot: %w", err)
		}
		if err := os.WriteFile(filepath.Join(slotDir, "mem.db"), memData, 0o600); err != nil {
			return fmt.Errorf("restore: write staged mem.db: %w", err)
		}
		sidecar, _ := json.Marshal(sp)
		if err := os.WriteFile(filepath.Join(slotDir, "project.json"), sidecar, 0o644); err != nil {
			return fmt.Errorf("restore: write staged project.json: %w", err)
		}

		stagePath := slotDir
		if home != "" {
			stagePath = homeRelPath(slotDir)
		}
		// Conflict target is (identity, path): each staged copy occupies its own
		// unique stage slot, so distinct copies of one repo land in distinct rows.
		_, err := globalDB.ExecContext(ctx,
			`INSERT INTO projects (identity, path, last_seen, status)
			 VALUES (?, ?, ?, 'pending')
			 ON CONFLICT(identity, path) DO UPDATE SET
			   last_seen = excluded.last_seen,
			   status    = 'pending'`,
			sp.Identity, stagePath, time.Now().UnixMilli())
		if err != nil {
			return fmt.Errorf("restore: register pending %s: %w", sp.Identity, err)
		}
		res.StagedCount++
		return nil
	}

	// Live projects from the source archive.
	for i := 0; ; i++ {
		prefix := fmt.Sprintf("projects/%d/", i)
		sidecarData, ok := byName[prefix+"project.json"]
		if !ok {
			break
		}
		memData, ok := byName[prefix+"mem.db"]
		if !ok {
			continue
		}
		var sp SaveProject
		if err := json.Unmarshal(sidecarData, &sp); err != nil {
			continue
		}
		if err := stage(memData, sp); err != nil {
			return res, err
		}
	}

	// Carry-forward pending entries from the source machine's stage.
	for _, e := range all {
		if !strings.HasPrefix(e.name, "project-stage/") {
			continue
		}
		if filepath.Base(e.name) != "project.json" {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(e.name, "project-stage/"), "/", 2)
		if len(parts) < 1 {
			continue
		}
		slug := parts[0]
		memData, ok := byName["project-stage/"+slug+"/mem.db"]
		if !ok {
			continue
		}
		var sp SaveProject
		if err := json.Unmarshal(e.data, &sp); err != nil {
			continue
		}
		if err := stage(memData, sp); err != nil {
			return res, err
		}
	}

	return res, nil
}

// dbHasNoCuratedContent returns true when the DB contains no memories in the
// invariant, preference, long, or cold tiers. Short-tier events are session-
// ephemeral and do not count as curated content.
func dbHasNoCuratedContent(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories
		 WHERE tier IN ('invariant','preference','long','cold')`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// identitySlug derives a short, human-readable directory name from a project
// identity. For git remote URLs it uses the repo basename (sans .git); for
// path-based identities it uses the last path component.
func identitySlug(identity string) string {
	// Strip common git suffixes and path prefixes.
	s := strings.TrimSuffix(identity, ".git")
	// For URLs like git@host:user/repo or https://host/user/repo
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		s = "project"
	}
	// Sanitize for filesystem use: replace characters that are awkward in paths.
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '-'
		}
		return r
	}, s)
	return s
}

// uniqueStageSlug returns a slot name under dir that does not yet exist,
// appending " (1)", " (2)", … (the download-model convention from the design)
// when the base name is taken.
func uniqueStageSlug(dir, base string) string {
	candidate := base
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s (%d)", base, i)
	}
}
