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
	"unicode/utf8"
)

type AutomationClassification string

const (
	AutomationDirectTool  AutomationClassification = "direct-tool"
	AutomationSkillMember AutomationClassification = "skill-member"
	AutomationInternal    AutomationClassification = "internal"
	AutomationNeedsReview AutomationClassification = "review"
	AutomationIgnore      AutomationClassification = "ignore"
)

const MaxAutomationRationaleLen = 200

func ValidAutomationClassification(v AutomationClassification) bool {
	switch v {
	case AutomationDirectTool, AutomationSkillMember, AutomationInternal, AutomationNeedsReview, AutomationIgnore:
		return true
	}
	return false
}

// AutomationCandidate is a conventional repository automation entry point.
// Digest fingerprints this candidate alone, so one changed file does not
// invalidate the judgments attached to every other entry point.
type AutomationCandidate struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
}

// AutomationCatalogEntry is the durable judgment attached to one candidate at
// one content version. Invocation is required for direct tools when it cannot
// be inferred from the script extension or shebang. SkillKey groups several
// skill members into one proposed workflow.
type AutomationCatalogEntry struct {
	Path           string                   `json:"path"`
	DetectedKind   string                   `json:"detected_kind"`
	ContentDigest  string                   `json:"content_digest"`
	Classification AutomationClassification `json:"classification"`
	Rationale      string                   `json:"rationale,omitempty"`
	SkillKey       string                   `json:"skill_key,omitempty"`
	Invocation     string                   `json:"invocation,omitempty"`
	ReviewedAt     int64                    `json:"reviewed_at"`
}

type AutomationReviewState string

const (
	AutomationNew       AutomationReviewState = "new"
	AutomationChanged   AutomationReviewState = "changed"
	AutomationRemoved   AutomationReviewState = "removed"
	AutomationUnchanged AutomationReviewState = "unchanged"
)

// AutomationReviewItem combines the current filesystem observation with the
// previous durable verdict. For removed entries Candidate contains the last
// known path/kind/digest from the catalog.
type AutomationReviewItem struct {
	Candidate AutomationCandidate     `json:"candidate"`
	State     AutomationReviewState   `json:"state"`
	Previous  *AutomationCatalogEntry `json:"previous,omitempty"`
}

type AutomationReview struct {
	Items []AutomationReviewItem `json:"items"`
}

type automationExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
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

// DiscoverAutomation finds conventional entry points without executing them.
func DiscoverAutomation(root string) ([]AutomationCandidate, error) {
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
			return nil, fmt.Errorf("discover automation in %s: %w", dir, err)
		}
	}

	for i := range candidates {
		digest, err := automationFileDigest(filepath.Join(root, filepath.FromSlash(candidates[i].Path)))
		if err != nil {
			return nil, fmt.Errorf("hash automation candidate %s: %w", candidates[i].Path, err)
		}
		candidates[i].Digest = digest
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
	return candidates, nil
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

func automationFileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ListAutomationCatalog(ctx context.Context, db *sql.DB) ([]AutomationCatalogEntry, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT path, detected_kind, content_digest, classification, rationale,
		       skill_key, invocation, reviewed_at
		FROM automation_catalog_entries ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("list automation catalog: %w", err)
	}
	defer rows.Close()
	var out []AutomationCatalogEntry
	for rows.Next() {
		var e AutomationCatalogEntry
		if err := rows.Scan(&e.Path, &e.DetectedKind, &e.ContentDigest, &e.Classification,
			&e.Rationale, &e.SkillKey, &e.Invocation, &e.ReviewedAt); err != nil {
			return nil, fmt.Errorf("scan automation catalog: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReconcileAutomationCatalog compares current candidates to durable judgments.
// When includeUnchanged is false it returns only work requiring attention.
func ReconcileAutomationCatalog(ctx context.Context, db *sql.DB, candidates []AutomationCandidate, includeUnchanged bool) (AutomationReview, error) {
	entries, err := ListAutomationCatalog(ctx, db)
	if err != nil {
		return AutomationReview{}, err
	}
	previous := make(map[string]AutomationCatalogEntry, len(entries))
	for _, e := range entries {
		previous[e.Path] = e
	}
	seen := make(map[string]bool, len(candidates))
	var items []AutomationReviewItem
	for _, candidate := range candidates {
		seen[candidate.Path] = true
		entry, ok := previous[candidate.Path]
		state := AutomationNew
		var prior *AutomationCatalogEntry
		if ok {
			copy := entry
			prior = &copy
			state = AutomationUnchanged
			if entry.ContentDigest != candidate.Digest || entry.DetectedKind != candidate.Kind {
				state = AutomationChanged
			}
		}
		if includeUnchanged || state != AutomationUnchanged {
			items = append(items, AutomationReviewItem{Candidate: candidate, State: state, Previous: prior})
		}
	}
	for _, entry := range entries {
		if seen[entry.Path] {
			continue
		}
		copy := entry
		items = append(items, AutomationReviewItem{
			Candidate: AutomationCandidate{Path: entry.Path, Kind: entry.DetectedKind, Digest: entry.ContentDigest},
			State:     AutomationRemoved, Previous: &copy,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Candidate.Path < items[j].Candidate.Path })
	return AutomationReview{Items: items}, nil
}

func ClassifyAutomation(ctx context.Context, db automationExecer, candidate AutomationCandidate, classification AutomationClassification, rationale, skillKey, invocation string) error {
	if !ValidAutomationClassification(classification) {
		return fmt.Errorf("unsupported automation classification %q", classification)
	}
	if classification != AutomationSkillMember && skillKey != "" {
		return fmt.Errorf("skill key only applies to skill-member classification")
	}
	if classification != AutomationDirectTool && strings.TrimSpace(invocation) != "" {
		return fmt.Errorf("invocation only applies to direct-tool classification")
	}
	rationale = strings.TrimSpace(rationale)
	if strings.ContainsAny(rationale, "\r\n") {
		return fmt.Errorf("automation rationale must be one line")
	}
	if n := utf8.RuneCountInString(rationale); n > MaxAutomationRationaleLen {
		return fmt.Errorf("automation rationale too long: %d characters (max %d)", n, MaxAutomationRationaleLen)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO automation_catalog_entries
		    (path, detected_kind, content_digest, classification, rationale, skill_key, invocation, reviewed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
		    detected_kind = excluded.detected_kind,
		    content_digest = excluded.content_digest,
		    classification = excluded.classification,
		    rationale = excluded.rationale,
		    skill_key = excluded.skill_key,
		    invocation = excluded.invocation,
		    reviewed_at = excluded.reviewed_at`,
		candidate.Path, candidate.Kind, candidate.Digest, classification,
		rationale, strings.TrimSpace(skillKey), strings.TrimSpace(invocation), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("classify automation %s: %w", candidate.Path, err)
	}
	// Capture the verdict in the append-only curation log. The classification is
	// the valence-bearing text (a reader can weigh "direct-tool" against "ignore"
	// later); rationale rides the tldr. The automation catalog is project-scoped.
	// This shares the caller's execer, so when classify runs inside a batch
	// transaction the event commits atomically with the verdict.
	captureCuration(ctx, db, CurationEvent{
		Action:  CurationSkillClassify,
		Key:     candidate.Path,
		DBScope: scopeName(false),
		Content: string(classification),
		Tldr:    rationale,
	})
	return nil
}

// scopeName is the package-internal name for a database scope, matching the CLI's
// vocabulary. The automation catalog only ever lives in the project database.
func scopeName(global bool) string {
	if global {
		return "global"
	}
	return "project"
}

// InferAutomationInvocation returns a stable runner-based command for a script.
// Manifests and other non-script entry points return an empty command so the
// classifier can require an explicit --command rather than inventing one.
func InferAutomationInvocation(root string, candidate AutomationCandidate) (string, error) {
	path := filepath.Join(root, filepath.FromSlash(candidate.Path))
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	run := resolveRunner(candidate.Path, content, "")
	if run == "" {
		return "", nil
	}
	return run + " " + candidate.Path, nil
}

func RemoveAutomationClassification(ctx context.Context, db automationExecer, path string) (bool, error) {
	result, err := db.ExecContext(ctx, `DELETE FROM automation_catalog_entries WHERE path = ?`, path)
	if err != nil {
		return false, fmt.Errorf("remove automation classification %s: %w", path, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		captureCuration(ctx, db, CurationEvent{
			Action:  CurationSkillClassify,
			Key:     path,
			DBScope: scopeName(false),
			Content: "removed",
		})
	}
	return n > 0, nil
}

// ActiveAutomationCatalogEntries returns only judgments matching a currently
// present candidate at the exact classified kind and digest. Changed or removed
// code remains available for review through reconciliation but is not offered
// as an executable tool or live skill member until reclassified.
func ActiveAutomationCatalogEntries(entries []AutomationCatalogEntry, candidates []AutomationCandidate) []AutomationCatalogEntry {
	current := make(map[string]AutomationCandidate, len(candidates))
	for _, candidate := range candidates {
		current[candidate.Path] = candidate
	}
	out := make([]AutomationCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		candidate, ok := current[entry.Path]
		if ok && candidate.Kind == entry.DetectedKind && candidate.Digest == entry.ContentDigest {
			out = append(out, entry)
		}
	}
	return out
}

// ProjectToolsFromCatalog turns classified direct tools into scoped tool
// descriptors. A stored invocation wins; otherwise script runner inference is
// used. Missing invocations are warnings, never invented commands.
func ProjectToolsFromCatalog(root string, entries []AutomationCatalogEntry) (tools []ToolDesc, warnings []string) {
	for _, entry := range entries {
		if entry.Classification != AutomationDirectTool {
			continue
		}
		desc := entry.Rationale
		if desc == "" {
			desc = entry.Path
		}
		if entry.Invocation != "" {
			tools = append(tools, ToolDesc{Name: entry.Path, Desc: desc, Usage: entry.Invocation, Path: entry.Invocation})
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(entry.Path))
		content, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: classified direct tool is unavailable", entry.Path))
			continue
		}
		run := resolveRunner(entry.Path, content, "")
		if run == "" {
			warnings = append(warnings, fmt.Sprintf("%s: classified direct tool has no invocation; reclassify with --command", entry.Path))
			continue
		}
		tools = append(tools, ToolDesc{Name: entry.Path, Desc: desc, Run: run, Path: entry.Path})
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, warnings
}
