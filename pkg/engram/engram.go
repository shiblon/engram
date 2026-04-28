// Package engram provides a simple way to maintain a database of session
// actions for Claude Code. Actions taken result in storage to SQLite, and
// opening a session or understanding context of a repo is a quick summary using
// this tool.
//
// Inspired by https://github.com/dezgit2025/auto-memory, which works for
// copilot but not for Claude. With Claude a database is not available by
// default, so this helps create and manage one as well as using for
// summarization.
package engram

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "embed"

	_ "modernc.org/sqlite"
)

// Tool identifies a Claude Code tool that produces recordable events.
type Tool string

const (
	ToolRead  Tool = "Read"
	ToolEdit  Tool = "Edit"
	ToolWrite Tool = "Write"
	ToolBash  Tool = "Bash"
)

// Recordable reports whether this tool unconditionally produces events worth
// recording. Bash is conditionally recordable; use BashRecordable to check.
func (t Tool) Recordable() bool {
	switch t {
	case ToolRead, ToolEdit, ToolWrite:
		return true
	}
	return false
}

// NormalizeBashCommand strips rtk-style prefixes, returning the canonical
// form of the command (e.g. "rtk grep -r foo" -> "grep -r foo").
func NormalizeBashCommand(command string) string {
	parts := strings.Fields(command)
	if len(parts) >= 2 && filepath.Base(parts[0]) == "rtk" {
		return strings.Join(parts[1:], " ")
	}
	return command
}

// BashRecordable reports whether a Bash command is worth recording. It
// recognises grep and find, including rtk-prefixed variants (e.g. "rtk grep").
func BashRecordable(command string) bool {
	parts := strings.Fields(NormalizeBashCommand(command))
	if len(parts) == 0 {
		return false
	}
	switch filepath.Base(parts[0]) {
	case "grep", "find":
		return true
	}
	return false
}

// BashSucceeded reports whether a Bash tool_response indicates success.
// A command is considered failed if it was interrupted or produced no stdout
// and non-empty stderr.
func BashSucceeded(raw json.RawMessage) bool {
	var r BashResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return false
	}
	if r.Interrupted {
		return false
	}
	return r.Stdout != "" || r.Stderr == ""
}

// Tier identifies the memory tier for a Memory entry.
type Tier string

const (
	TierInvariant  Tier = "invariant"
	TierPreference Tier = "preference"
	TierLong       Tier = "long"
	TierShort      Tier = "short"
)

// Memory holds a single intentional memory entry.
type Memory struct {
	ID        int64
	TS        int64
	Tier      Tier
	Key       string
	Content   string
	SessionID string // non-empty for short-tier auto-expiry
}

const (
	// DefaultInjectSessions is the default number of recent sessions to
	// include in session-start context.
	DefaultInjectSessions = 5
	// DefaultPruneSessions is the default number of sessions to keep when
	// pruning old events.
	DefaultPruneSessions = 100

	snippetHeadLines = 50
	snippetDiffChars = 2000
)

//go:embed schema.sql
var schema string

// HookInput is the JSON payload received on stdin from Claude Code PostToolUse hooks.
type HookInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	ToolName  Tool   `json:"tool_name"`
	ToolInput struct {
		FilePath string `json:"file_path"` // Read, Edit, Write
		Command  string `json:"command"`   // Bash
	} `json:"tool_input"`
	// Response holds the raw tool_response JSON. Use the typed response structs
	// (ReadResponse, EditResponse, BashResponse) to unmarshal as needed.
	// TODO: consider a typed union here once all tool response shapes are probed.
	Response json.RawMessage `json:"tool_response"`
}

// ReadResponse is the tool_response shape for Read events.
type ReadResponse struct {
	File struct {
		Content    string `json:"content"`
		NumLines   int    `json:"numLines"`
		StartLine  int    `json:"startLine"`
		TotalLines int    `json:"totalLines"`
	} `json:"file"`
}

// EditHunk is one hunk in an EditResponse.StructuredPatch.
type EditHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// EditResponse is the tool_response shape for Edit events.
type EditResponse struct {
	NewString       string     `json:"newString"`
	StructuredPatch []EditHunk `json:"structuredPatch"`
}

// WriteResponse is the tool_response shape for Write events.
type WriteResponse struct {
	Content string `json:"content"`
}

// BashResponse is the tool_response shape for Bash events.
type BashResponse struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Interrupted bool   `json:"interrupted"`
}

// ParseHookInput decodes a HookInput from r.
func ParseHookInput(r io.Reader) (*HookInput, error) {
	var h HookInput
	if err := json.NewDecoder(r).Decode(&h); err != nil {
		return nil, fmt.Errorf("parse hook input: %w", err)
	}
	return &h, nil
}

// RelPath returns the path of absPath relative to root, or an error if absPath
// is outside root.
func RelPath(root, absPath string) (string, error) {
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s is outside project root %s", absPath, root)
	}
	return rel, nil
}

// Event holds a single recorded tool-use event.
type Event struct {
	SessionID string
	TS        int64 // unix epoch ms; zero means use current time
	Tool      Tool
	// FilePath is the file path relative to project root for file tool events,
	// or the normalized command string for Bash events.
	FilePath string
	Snippet  string
}

// FindProjectRoot walks up from start to find the nearest project root,
// returning the deepest directory that contains a .claude/ directory or a VCS
// root (.git, .hg, .svn). "Deepest" means closest to start, so nested repos
// (submodules, monorepo sub-packages) resolve to the innermost boundary.
//
// To have a sub-repo managed as part of an outer engram project rather than
// its own, initialize engram in the outer project and not the inner one. The
// first .claude/ dir found walking up wins, so the outer project takes over
// once the inner VCS root is passed.
//
// $HOME/.claude is always skipped -- it is the Claude Code global config
// directory, not a project root. VCS roots at $HOME are still recognized
// (e.g. a dotfiles repo). The walk never goes above $HOME.
func FindProjectRoot(start string) (string, error) {
	home, _ := os.UserHomeDir()

	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		// .engram/ takes priority -- canonical engram project marker.
		if info, err := os.Stat(filepath.Join(current, ".engram")); err == nil && info.IsDir() {
			return current, nil
		}
		// .claude/ is the legacy marker (skip at $HOME).
		if current != home {
			if info, err := os.Stat(filepath.Join(current, ".claude")); err == nil && info.IsDir() {
				return current, nil
			}
		}
		for _, marker := range []string{".git", ".hg", ".svn"} {
			if _, err := os.Stat(filepath.Join(current, marker)); err == nil {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current || current == home {
			break
		}
		current = parent
	}
	return "", fmt.Errorf("no project root found from %s", start)
}

// DBPath returns the canonical project database path for the given root.
func DBPath(root string) string {
	return filepath.Join(root, ".engram", "mem.db")
}

// LegacyDBPath returns the old project database path, used for read fallback.
func LegacyDBPath(root string) string {
	return filepath.Join(root, ".claude", "engram.db")
}

// DBHandle bundles an open database with its path.
type DBHandle struct {
	DB   *sql.DB
	Path string
}

// GlobalDBPath returns the canonical global database path in $HOME/.engram.
func GlobalDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("global db path: %w", err)
	}
	return filepath.Join(home, ".engram", "mem.db"), nil
}

// LegacyGlobalDBPath returns the old global database path, used for read fallback.
func LegacyGlobalDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("legacy global db path: %w", err)
	}
	return filepath.Join(home, ".claude", "engram.db"), nil
}

// dbExists reports whether any of the given paths refer to an existing file.
// Empty paths are silently skipped.
func dbExists(paths ...string) bool {
	for _, p := range paths {
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}
	return false
}

// openWithFallback opens canonical if it exists (or neither exists), falling
// back to legacy if canonical is absent. Creates canonical's directory when
// opening canonical.
func openWithFallback(ctx context.Context, canonical, legacy string) (*sql.DB, error) {
	if _, err := os.Stat(canonical); os.IsNotExist(err) {
		if legacy != "" {
			if _, err := os.Stat(legacy); err == nil {
				return Open(ctx, legacy)
			}
		}
	}
	dir := filepath.Dir(canonical)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	gi := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gi); os.IsNotExist(err) {
		_ = os.WriteFile(gi, []byte("*\n"), 0644)
	}
	return Open(ctx, canonical)
}

// ProjectDBExists reports whether any project database exists at root.
func ProjectDBExists(root string) bool {
	return dbExists(DBPath(root), LegacyDBPath(root))
}

// GlobalDBExists reports whether any global database exists.
func GlobalDBExists() bool {
	path, _ := GlobalDBPath()
	legacy, _ := LegacyGlobalDBPath()
	return dbExists(path, legacy)
}

// OpenProjectDB opens the project database, falling back to the legacy path if
// the canonical path does not yet exist.
func OpenProjectDB(ctx context.Context, root string) (*sql.DB, error) {
	return openWithFallback(ctx, DBPath(root), LegacyDBPath(root))
}

// OpenGlobalDB opens the global database, falling back to the legacy path if
// the canonical path does not yet exist.
func OpenGlobalDB(ctx context.Context) (*sql.DB, error) {
	path, err := GlobalDBPath()
	if err != nil {
		return nil, err
	}
	legacy, _ := LegacyGlobalDBPath()
	return openWithFallback(ctx, path, legacy)
}

// Open opens (and initializes) the engram database at path. The caller is
// responsible for calling db.Close.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if err := dbInit(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// dbInit applies the schema to the database. Idempotent and non-destructive.
func dbInit(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("db init: %w", err)
	}
	return nil
}

// Record inserts an event into the database.
func Record(ctx context.Context, db *sql.DB, event Event) error {
	if event.TS == 0 {
		event.TS = time.Now().UnixMilli()
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO events (session_id, ts, tool, file_path, snippet) VALUES (?, ?, ?, ?, ?)`,
		event.SessionID, event.TS, event.Tool, event.FilePath, event.Snippet,
	)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	return nil
}

// InjectResult holds the context gathered for a session-start injection.
type InjectResult struct {
	// From events table
	Files    []string
	Searches []string
	// From memories table
	Invariants  []Memory
	Preferences []Memory
	LongTerm    []Memory
	ShortTerm   []Memory
}

// Inject returns files and bash searches from the last nSessions sessions,
// plus all memories from the given database.
func Inject(ctx context.Context, db *sql.DB, nSessions int) (InjectResult, error) {
	recentSessions := `
		SELECT session_id FROM (
			SELECT session_id, MAX(ts) AS last_ts
			FROM events
			GROUP BY session_id
			ORDER BY last_ts DESC
			LIMIT ?
		)`

	files, err := queryStrings(ctx, db, `
		SELECT file_path
		FROM events
		WHERE tool != ? AND session_id IN (`+recentSessions+`)
		GROUP BY file_path
		ORDER BY MAX(ts) DESC
	`, ToolBash, nSessions)
	if err != nil {
		return InjectResult{}, fmt.Errorf("inject files: %w", err)
	}

	searches, err := queryStrings(ctx, db, `
		SELECT file_path
		FROM events
		WHERE tool = ? AND session_id IN (`+recentSessions+`)
		GROUP BY file_path
		ORDER BY MAX(ts) DESC
	`, ToolBash, nSessions)
	if err != nil {
		return InjectResult{}, fmt.Errorf("inject searches: %w", err)
	}

	invariants, err := ListMemories(ctx, db, TierInvariant)
	if err != nil {
		return InjectResult{}, fmt.Errorf("inject invariants: %w", err)
	}
	preferences, err := ListMemories(ctx, db, TierPreference)
	if err != nil {
		return InjectResult{}, fmt.Errorf("inject preferences: %w", err)
	}
	longTerm, err := ListMemories(ctx, db, TierLong)
	if err != nil {
		return InjectResult{}, fmt.Errorf("inject long-term: %w", err)
	}
	shortTerm, err := ListMemories(ctx, db, TierShort)
	if err != nil {
		return InjectResult{}, fmt.Errorf("inject short-term: %w", err)
	}

	return InjectResult{
		Files:       files,
		Searches:    searches,
		Invariants:  invariants,
		Preferences: preferences,
		LongTerm:    longTerm,
		ShortTerm:   shortTerm,
	}, nil
}

func queryStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Prune deletes events from sessions older than the keepSessions most recent,
// returning the number of rows deleted.
func Prune(ctx context.Context, db *sql.DB, keepSessions int) (int64, error) {
	result, err := db.ExecContext(ctx, `
		DELETE FROM events
		WHERE session_id NOT IN (
			SELECT session_id FROM (
				SELECT session_id, MAX(ts) AS last_ts
				FROM events
				GROUP BY session_id
				ORDER BY last_ts DESC
				LIMIT ?
			)
		)
	`, keepSessions)
	if err != nil {
		return 0, fmt.Errorf("prune: %w", err)
	}
	return result.RowsAffected()
}

// WriteMemory upserts a memory entry. If a memory with the same tier and key
// exists it is replaced.
func WriteMemory(ctx context.Context, db *sql.DB, m Memory) error {
	if m.TS == 0 {
		m.TS = time.Now().UnixMilli()
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO memories (ts, tier, key, content, session_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tier, key) DO UPDATE SET
			ts = excluded.ts,
			content = excluded.content,
			session_id = excluded.session_id
	`, m.TS, m.Tier, m.Key, m.Content, m.SessionID)
	if err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	return nil
}

// queryMemories is the shared implementation for ReadMemory, ReadMemoryTop, ListMemories, and FindMemoryByKey.
// An empty tier matches all tiers.
func queryMemories(ctx context.Context, db *sql.DB, tier Tier, key string, limit int) ([]Memory, error) {
	q := `SELECT id, ts, tier, key, content, COALESCE(session_id, '') FROM memories WHERE true`
	var args []any
	if tier != "" {
		q += ` AND tier = ?`
		args = append(args, tier)
	}
	if key != "" {
		q += ` AND key = ?`
		args = append(args, key)
	}
	q += ` ORDER BY ts DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.TS, &m.Tier, &m.Key, &m.Content, &m.SessionID); err != nil {
			return nil, fmt.Errorf("query memories scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReadMemory returns the memory with the given tier and key, or nil if not found.
func ReadMemory(ctx context.Context, db *sql.DB, tier Tier, key string) (*Memory, error) {
	ms, err := queryMemories(ctx, db, tier, key, 1)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, nil
	}
	return &ms[0], nil
}

// ListMemories returns all memories for a tier, ordered by ts descending.
func ListMemories(ctx context.Context, db *sql.DB, tier Tier) ([]Memory, error) {
	return queryMemories(ctx, db, tier, "", 0)
}

// DeleteMemory removes the memory with the given tier and key, returning an
// error if nothing was found.
func DeleteMemory(ctx context.Context, db *sql.DB, tier Tier, key string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM memories WHERE tier = ? AND key = ?`, tier, key)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("not found: %s/%s", tier, key)
	}
	return nil
}

// FindMemoryByKey searches all tiers for memories with the exact key.
func FindMemoryByKey(ctx context.Context, db *sql.DB, key string) ([]Memory, error) {
	return queryMemories(ctx, db, "", key, 0)
}

// PromoteMemory moves a memory from one tier to another within the same database.
func PromoteMemory(ctx context.Context, db *sql.DB, key string, from, to Tier) error {
	m, err := ReadMemory(ctx, db, from, key)
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("memory %q not found in tier %q", key, from)
	}
	m.Tier = to
	m.TS = time.Now().UnixMilli()
	if err := WriteMemory(ctx, db, *m); err != nil {
		return err
	}
	return DeleteMemory(ctx, db, from, key)
}

// PopMemory reads and deletes the most recent short-tier memory. Returns nil
// if the tier is empty.
func PopMemory(ctx context.Context, db *sql.DB, tier Tier) (*Memory, error) {
	m, err := ReadMemoryTop(ctx, db, tier)
	if err != nil || m == nil {
		return m, err
	}
	return m, DeleteMemory(ctx, db, tier, m.Key)
}

// ReadMemoryTop returns the most recent memory for a tier without deleting it.
func ReadMemoryTop(ctx context.Context, db *sql.DB, tier Tier) (*Memory, error) {
	ms, err := queryMemories(ctx, db, tier, "", 1)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, nil
	}
	return &ms[0], nil
}

// SearchMemories performs a full-text search over memories. If tier is
// non-empty, results are filtered to that tier.
func SearchMemories(ctx context.Context, db *sql.DB, query string, tier Tier) ([]Memory, error) {
	q := `SELECT m.id, m.ts, m.tier, m.key, m.content, COALESCE(m.session_id, '')
		FROM memories_fts f
		JOIN memories m ON m.id = f.rowid
		WHERE memories_fts MATCH ?`
	args := []any{query}
	if tier != "" {
		q += ` AND m.tier = ?`
		args = append(args, tier)
	}
	q += ` ORDER BY rank`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search memories: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.TS, &m.Tier, &m.Key, &m.Content, &m.SessionID); err != nil {
			return nil, fmt.Errorf("search scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MakeSnippet extracts a snippet from a raw tool_response JSON payload.
func MakeSnippet(tool Tool, raw json.RawMessage) string {
	switch tool {
	case ToolRead:
		var r ReadResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return ""
		}
		return headLines(r.File.Content, snippetHeadLines)

	case ToolEdit:
		var r EditResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return ""
		}
		if len(r.StructuredPatch) == 0 {
			if len(r.NewString) > snippetDiffChars {
				return r.NewString[:snippetDiffChars]
			}
			return r.NewString
		}
		var b strings.Builder
		for _, hunk := range r.StructuredPatch {
			fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
				hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines)
			for _, line := range hunk.Lines {
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
		result := b.String()
		if len(result) > snippetDiffChars {
			return result[:snippetDiffChars] + "\n... (truncated)"
		}
		return result

	case ToolWrite:
		var r WriteResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return ""
		}
		return headLines(r.Content, snippetHeadLines)

	case ToolBash:
		var r BashResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return ""
		}
		return headLines(r.Stdout, snippetHeadLines)

	default:
		return ""
	}
}

// injectOutput is the JSON structure returned by the SessionStart hook.
type injectOutput struct {
	HookSpecificOutput injectHookOutput `json:"hookSpecificOutput"`
}

type injectHookOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// FormatInjectOutput formats the session-start hook output JSON.
// InjectContextText formats global and project inject results as plain text.
func InjectContextText(global, project InjectResult, nSessions int) string {
	var parts []string

	if len(global.Invariants) > 0 {
		lines := make([]string, len(global.Invariants))
		for i, m := range global.Invariants {
			lines[i] = fmt.Sprintf("**%s**: %s", m.Key, m.Content)
		}
		parts = append(parts, "## Identity\n"+strings.Join(lines, "\n"))
	}

	if len(global.Preferences) > 0 {
		lines := make([]string, len(global.Preferences))
		for i, m := range global.Preferences {
			lines[i] = "- " + m.Content
		}
		parts = append(parts, "## Preferences\n"+strings.Join(lines, "\n"))
	}

	if len(project.LongTerm) > 0 {
		lines := make([]string, len(project.LongTerm))
		for i, m := range project.LongTerm {
			lines[i] = fmt.Sprintf("- **%s**: %s", m.Key, m.Content)
		}
		parts = append(parts, "## Long-term memory\n"+strings.Join(lines, "\n"))
	}

	if len(project.ShortTerm) > 0 {
		lines := make([]string, len(project.ShortTerm))
		for i, m := range project.ShortTerm {
			lines[i] = fmt.Sprintf("%d. [%s] %s", i+1, m.Key, m.Content)
		}
		parts = append(parts, "## Short-term stack\n"+strings.Join(lines, "\n"))
	}

	if len(project.Files) > 0 {
		parts = append(parts,
			fmt.Sprintf("## Recently active files (last %d sessions)\n  %s",
				nSessions, strings.Join(project.Files, "\n  ")))
	}

	if len(project.Searches) > 0 {
		parts = append(parts,
			"## Recent searches\n  "+strings.Join(project.Searches, "\n  "))
	}

	if len(parts) == 0 {
		parts = append(parts,
			"Engram is active but not yet set up. "+
				"Ask your agent to set a personality, codename, and preferences with `engram mem write`.")
	}

	return strings.Join(parts, "\n\n")
}

// FormatInjectOutput wraps InjectContextText in the SessionStart hook JSON envelope.
func FormatInjectOutput(global, project InjectResult, nSessions int) []byte {
	out, _ := json.Marshal(injectOutput{
		HookSpecificOutput: injectHookOutput{
			HookEventName:     "SessionStart",
			AdditionalContext: InjectContextText(global, project, nSessions),
		},
	})
	return out
}

func headLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
