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
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "embed"

	_ "modernc.org/sqlite"
)

// Tool identifies an agent tool whose use produces a recordable file event.
// The names are the on-the-wire tool_name values across the agents engram
// supports: Claude Code edits through Read/Edit/Write, Gemini CLI through
// read_file/write_file/replace, and Codex CLI through apply_patch.
type Tool string

const (
	// Claude Code file tools.
	ToolRead  Tool = "Read"
	ToolEdit  Tool = "Edit"
	ToolWrite Tool = "Write"
	// Gemini CLI file tools (all carry the path in tool_input.file_path, just
	// like Claude's, so they record through the same FilePath() path).
	ToolReadFile  Tool = "read_file"
	ToolWriteFile Tool = "write_file"
	ToolReplace   Tool = "replace"
	// Codex CLI file tool. Unlike the others it names paths inside the patch
	// body rather than a file_path field -- see PatchedFiles.
	ToolApplyPatch Tool = "apply_patch"
)

// Recordable reports whether this tool carries its file path in
// tool_input.file_path (Claude Code's Read/Edit/Write and Gemini CLI's
// read_file/write_file/replace). apply_patch is also recordable but names its
// paths inside the patch body, so callers extract those with PatchedFiles
// rather than reading a single file_path.
func (t Tool) Recordable() bool {
	switch t {
	case ToolRead, ToolEdit, ToolWrite,
		ToolReadFile, ToolWriteFile, ToolReplace:
		return true
	}
	return false
}

// Tier identifies the memory tier for a Memory entry.
type Tier string

const (
	TierInvariant  Tier = "invariant"
	TierPreference Tier = "preference"
	TierLong       Tier = "long"
	TierShort      Tier = "short"
	TierCold       Tier = "cold"
)

// ParseTier turns a user- or caller-supplied tier name into one of engram's five
// canonical tiers. The human-facing names "long-term" and "short-term" are
// accepted as aliases because the documentation and injected context naturally
// use them in prose; storage always uses the canonical short tokens.
func ParseTier(value string) (Tier, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(TierInvariant):
		return TierInvariant, nil
	case string(TierPreference):
		return TierPreference, nil
	case string(TierLong), "long-term":
		return TierLong, nil
	case string(TierShort), "short-term":
		return TierShort, nil
	case string(TierCold):
		return TierCold, nil
	default:
		return "", fmt.Errorf(
			"invalid memory tier %q: use invariant, preference, long, short, or cold",
			value,
		)
	}
}

// Memory holds a single intentional memory entry.
type Memory struct {
	ID      int64
	TS      int64
	Tier    Tier
	Key     string
	Content string
	// Tldr is the one-line summary inject surfaces in place of Content for every
	// tier but invariants. Empty is valid; InjectSummary falls back to the first
	// line of Content. Capped at MaxTldrLen runes by WriteMemory.
	Tldr string
	// Trigger is the compact task condition used to retrieve a skill. A
	// long-term memory with a nonempty Trigger is a skill; Content remains its
	// full instructions. Capped at MaxTriggerLen runes by WriteMemory.
	Trigger   string
	SessionID string // non-empty for short-tier auto-expiry
}

// MaxTldrLen is the hard character (rune) ceiling on a memory's tldr. It is a
// character count, not a word count, on purpose: a word limit is trivially gamed
// with run-on compounds, whereas characters force genuine compression. Inject
// leans on this bound to keep session-start context small and predictable.
const MaxTldrLen = 200

// MaxTriggerLen keeps the always-loaded skill index compact and predictable.
const MaxTriggerLen = 200

// InjectSummary returns the one-line form of a memory for session-start context:
// its tldr when set, else the first line of its content, truncated to MaxTldrLen
// runes either way. Inject surfaces this for every tier except invariants; the
// agent fetches full content on demand with `engram mem read <key>`.
func (m Memory) InjectSummary() string {
	s := m.Tldr
	if s == "" {
		s = firstLine(m.Content)
	}
	return truncateRunes(s, MaxTldrLen)
}

// validateTldr rejects a tldr longer than MaxTldrLen runes. Enforced in
// WriteMemory so every write path (CLI, import, restore) shares one ceiling.
func validateTldr(tldr string) error {
	if n := utf8.RuneCountInString(tldr); n > MaxTldrLen {
		return fmt.Errorf("tldr too long: %d characters (max %d)", n, MaxTldrLen)
	}
	return nil
}

func validateTrigger(trigger string) error {
	if n := utf8.RuneCountInString(trigger); n > MaxTriggerLen {
		return fmt.Errorf("trigger too long: %d characters (max %d)", n, MaxTriggerLen)
	}
	return nil
}

// truncateRunes shortens s to at most n runes, appending an ellipsis when it cut
// anything, so a stray un-summarized firstLine can never blow the inject budget.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

const (
	// DefaultInjectSessions is the default number of recent sessions to
	// include in session-start context.
	DefaultInjectSessions = 5
	// DefaultPruneSessions is the default number of sessions to keep when
	// pruning old events.
	DefaultPruneSessions = 100
	// InjectSplitThreshold is the touched-file count above which a directory in
	// the recently-active rollup is broken into its subdirectories, so hot areas
	// resolve while quiet ones stay a single line.
	InjectSplitThreshold = 10
	// InjectExpandThreshold is the touched-file count at or below which a rollup
	// directory is expanded to its individual file paths instead of a `dir/ ×count`
	// line. The count is touched files (Read/Edit/Write/apply_patch events), not
	// files on disk, so naming them tells the agent exactly which files it worked
	// on in that directory -- high-value signal. Kept small so the rollup still
	// collapses busy directories; InjectAreasBudgetChars backstops the spread-thin
	// case where many low-touch directories would each name their files.
	InjectExpandThreshold = 3
	// InjectLongBudgetChars, InjectShortBudgetChars, and InjectAreasBudgetChars
	// bound how many characters the long-term, short-term, and recently-active
	// sections may spend at session start. Entries arrive most-recent-first, so a
	// budget keeps recent items and drops the stalest, then the section reports
	// "showing N of M" rather than letting the harness silently truncate the
	// middle. Identity, preferences, and cold stay uncapped: identity is the
	// agent's voice, preferences are always-on rules, and cold is already an
	// index. These are the tunable policy knobs. The areas budget is small on
	// purpose -- it rarely bites, catching only runaway spread-thin repos.
	InjectLongBudgetChars  = 10000
	InjectSkillBudgetChars = 5000
	InjectShortBudgetChars = 3000
	InjectAreasBudgetChars = 2000
)

//go:embed schema.sql
var schema string

// HookInput is the JSON payload an agent delivers on stdin to the record
// (PostToolUse) and inject (SessionStart) hooks. The schema is shared across
// Claude Code and Codex CLI, which use the same snake_case field names. Only
// the fields engram acts on are decoded; tool_input is kept raw because its
// shape varies by tool (and by agent), and the typed accessors below pull what
// each record path needs.
type HookInput struct {
	SessionID string          `json:"session_id"`
	CWD       string          `json:"cwd"`
	ToolName  Tool            `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// FilePath returns tool_input.file_path -- the single edited file reported by
// Claude Code's Read/Edit/Write tools. It is empty for tools that carry no such
// field, including Codex's apply_patch, whose paths live in the patch body and
// are extracted with PatchedFiles instead.
func (h *HookInput) FilePath() string {
	var ti struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(h.ToolInput, &ti); err != nil {
		return ""
	}
	return ti.FilePath
}

// patchFileHeaders are the V4A apply_patch envelope markers that name a file.
// PatchedFiles records the destination path for each. "*** Move to:" is the new
// location of a rename and so also counts as a touched file.
var patchFileHeaders = []string{
	"*** Add File: ",
	"*** Update File: ",
	"*** Delete File: ",
	"*** Move to: ",
}

// PatchedFiles extracts the file paths named in a Codex apply_patch V4A envelope
// (*** Begin Patch ... *** End Patch) found inside the tool_input. Codex delivers
// the patch as a string whose field name varies (and a shell-heredoc invocation
// puts it under "command"), so rather than bind to one field we scan every
// string value in the tool_input object for the envelope's "*** <op> File:"
// headers. Returns the touched paths in the order encountered, deduplicated;
// nil if tool_input holds no patch.
func PatchedFiles(toolInput json.RawMessage) []string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(toolInput, &fields); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range fields {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue // not a string field
		}
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			for _, header := range patchFileHeaders {
				if path, ok := strings.CutPrefix(line, header); ok {
					path = strings.TrimSpace(path)
					if path != "" && !seen[path] {
						seen[path] = true
						out = append(out, path)
					}
				}
			}
		}
	}
	return out
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

// Event holds a single recorded file-touch event.
type Event struct {
	SessionID string
	TS        int64 // unix epoch ms; zero means use current time
	Tool      Tool
	// FilePath is the touched file's path relative to the project root.
	FilePath string
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

// DBPath returns the canonical project database path for the given root. Linked
// git worktrees store project memory in the main worktree so every branch
// checkout shares the same project-level memories.
func DBPath(root string) string {
	root = ProjectStorageRoot(root)
	return filepath.Join(root, ".engram", "mem.db")
}

// LegacyDBPath returns the old project database path, used for read fallback.
func LegacyDBPath(root string) string {
	root = ProjectStorageRoot(root)
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
	_, canonErr := os.Stat(canonical)
	if os.IsNotExist(canonErr) && legacy != "" {
		if _, err := os.Stat(legacy); err == nil {
			return Open(ctx, legacy)
		}
	}
	dir := filepath.Dir(canonical)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	gi := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gi); os.IsNotExist(err) {
		if err := os.WriteFile(gi, []byte("*\n"), 0644); err != nil {
			// Best-effort: without this the SQLite DB could be committed. Mirror
			// apply.go's handling (log) rather than swallowing it entirely.
			log.Printf("engram: write .gitignore for %s: %v", dir, err)
		}
	}
	return Open(ctx, canonical)
}

// openReadOnlyWithFallback opens an existing database without creating files,
// applying schema, or running migrations. The canonical path wins when both
// exist, matching openWithFallback.
func openReadOnlyWithFallback(ctx context.Context, canonical, legacy string) (*sql.DB, error) {
	path := canonical
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat database %s: %w", path, err)
		}
		if legacy == "" {
			return openEmptyReadOnly(ctx)
		}
		if _, legacyErr := os.Stat(legacy); legacyErr != nil {
			if os.IsNotExist(legacyErr) {
				return openEmptyReadOnly(ctx)
			}
			return nil, fmt.Errorf("stat database %s: %w", legacy, legacyErr)
		}
		path = legacy
	}
	db, err := openReadOnly(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open database read-only %s: %w", path, err)
	}
	return db, nil
}

func openEmptyReadOnly(ctx context.Context) (*sql.DB, error) {
	db, err := Open(ctx, ":memory:")
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
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
//
// When this call brings a project DB into existence for the first time (neither
// the canonical nor the legacy path existed), it registers the project in the
// global manifest -- a one-time structural footprint at DB birth, never a
// per-open side effect. Registration is best-effort and never blocks the open.
func OpenProjectDB(ctx context.Context, root string) (*sql.DB, error) {
	creating := !dbExists(DBPath(root), LegacyDBPath(root))
	db, err := openWithFallback(ctx, DBPath(root), LegacyDBPath(root))
	if err != nil {
		return nil, err
	}
	if creating {
		registerSelf(ctx, root)
	}
	return db, nil
}

// OpenProjectDBReadOnly opens existing project memory without creating files or
// applying schema changes. Linked worktrees resolve to the main checkout's
// shared database just like OpenProjectDB.
func OpenProjectDBReadOnly(ctx context.Context, root string) (*sql.DB, error) {
	db, err := openReadOnlyWithFallback(ctx, DBPath(root), LegacyDBPath(root))
	if err == nil {
		return db, nil
	}
	storageRoot := ProjectStorageRoot(root)
	if filepath.Clean(storageRoot) == filepath.Clean(root) {
		return nil, err
	}
	return nil, fmt.Errorf(
		"linked worktree memory is stored in the main checkout at %s: %w; "+
			"retry once with write access to %s so SQLite can create WAL coordination files; "+
			"do not create a separate .engram directory in this worktree",
		DBPath(root), err, filepath.Dir(DBPath(root)),
	)
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

// OpenGlobalDBReadOnly opens existing global memory without creating files or
// applying schema changes.
func OpenGlobalDBReadOnly(ctx context.Context) (*sql.DB, error) {
	path, err := GlobalDBPath()
	if err != nil {
		return nil, err
	}
	legacy, _ := LegacyGlobalDBPath()
	db, err := openReadOnlyWithFallback(ctx, path, legacy)
	if err == nil {
		return db, nil
	}
	return nil, fmt.Errorf(
		"open global memory read-only: %w; if this is a sandboxed filesystem, "+
			"retry once with write access to %s so SQLite can create WAL coordination files",
		err, filepath.Dir(path),
	)
}

// Open opens (and initializes) the engram database at path. The caller is
// responsible for calling db.Close.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := openRaw(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := dbInit(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// dbInit applies the baseline schema and any pending migrations.
func dbInit(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("db init: %w", err)
	}
	if err := applyMigrations(ctx, db); err != nil {
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
		`INSERT INTO events (session_id, ts, tool, file_path) VALUES (?, ?, ?, ?)`,
		event.SessionID, event.TS, event.Tool, event.FilePath,
	)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	return nil
}

// InjectResult holds the context gathered for a session-start injection.
type InjectResult struct {
	// From events table
	Files []string
	// From memories table
	Invariants       []Memory
	Preferences      []Memory
	Agent            string
	AgentInvariants  []Memory
	AgentPreferences []Memory
	LongTerm         []Memory
	ShortTerm        []Memory
	Cold             []Memory // keys+content injected as index only; content not expanded
	// From the filesystem (the global agenttools dir), not the DB. Populated by
	// the caller after Inject, since scanning is I/O outside the memory
	// database. Only ever set on the global InjectResult; repository-scoped tools
	// come from explicit catalog classifications below.
	AgentTools []ToolDesc
	// ProjectTools are repository-scoped direct tools backed by durable
	// automation catalog classifications.
	ProjectTools []ToolDesc
	// SkillCandidates are cataloged workflow members grouped by SkillKey. They
	// are offered for skill authoring but are not themselves executable skills.
	SkillCandidates []AutomationCatalogEntry
	// PendingRestores is the list of staged project snapshots awaiting placement.
	// Populated by the caller from the global DB after Inject. The renderer
	// surfaces these so the agent can decide whether to run --apply.
	PendingRestores []PendingRestore
	// AutomationReview is project-local, filesystem-derived maintenance context.
	// It contains only new, changed, or explicitly removed catalog entries.
	AutomationReview *AutomationReview
}

// Inject returns the recently active files from the last nSessions sessions,
// plus all memories from the given database.
func Inject(ctx context.Context, db *sql.DB, nSessions int) (InjectResult, error) {
	return InjectWithAgent(ctx, db, nSessions, "")
}

// InjectWithAgent returns Inject plus the requested agent-specific global layer.
// Agent layers live in the same global invariant/preference tiers as primary
// standing guidance, but are hidden unless agent is non-empty.
func InjectWithAgent(ctx context.Context, db *sql.DB, nSessions int, agent string) (InjectResult, error) {
	agent, err := NormalizeAgent(agent)
	if err != nil {
		return InjectResult{}, err
	}
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
		WHERE session_id IN (`+recentSessions+`)
		GROUP BY file_path
		ORDER BY MAX(ts) DESC
	`, nSessions)
	if err != nil {
		return InjectResult{}, fmt.Errorf("inject files: %w", err)
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
	cold, err := ListMemories(ctx, db, TierCold)
	if err != nil {
		return InjectResult{}, fmt.Errorf("inject cold: %w", err)
	}

	return InjectResult{
		Files:            files,
		Invariants:       PrimaryMemories(invariants),
		Preferences:      PrimaryMemories(preferences),
		Agent:            agent,
		AgentInvariants:  AgentLayerMemories(invariants, agent),
		AgentPreferences: AgentLayerMemories(preferences, agent),
		LongTerm:         longTerm,
		ShortTerm:        shortTerm,
		Cold:             cold,
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

// Prune deletes file-touch events from sessions older than the keepSessions most
// recent, returning the number of event rows deleted. It rotates the append-only
// curation log by the same session model in the same call, so the two logs share
// one retention policy and neither grows without bound.
//
// Two deliberate departures protect the curation log's losslessness. The retained
// session set is computed over both tables' timestamps, so a session that only
// curated memory (and touched no files) is not aged out early. And a curation row
// with no session id is never pruned: it has no session to age against, and
// dropping it would discard reward signal. In practice curation rows carry an
// empty session id today (the CLI is invoked without one), so this call rotates
// only the file-touch log until acting sessions are threaded through.
func Prune(ctx context.Context, db *sql.DB, keepSessions int) (int64, error) {
	recent := `
		SELECT session_id FROM (
			SELECT session_id, MAX(ts) AS last_ts FROM (
				SELECT session_id, ts FROM events
				UNION ALL
				SELECT session_id, ts FROM curation_events WHERE session_id != ''
			)
			GROUP BY session_id
			ORDER BY last_ts DESC
			LIMIT ?
		)`
	result, err := db.ExecContext(ctx,
		`DELETE FROM events WHERE session_id NOT IN (`+recent+`)`, keepSessions)
	if err != nil {
		return 0, fmt.Errorf("prune: %w", err)
	}
	eventsDeleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM curation_events WHERE session_id != '' AND session_id NOT IN (`+recent+`)`,
		keepSessions); err != nil {
		return 0, fmt.Errorf("prune curation events: %w", err)
	}
	return eventsDeleted, nil
}

// WriteMemory upserts a memory entry. If a memory with the same tier and key
// exists it is replaced.
//
// Every write is captured in the append-only curation log (see curation.go),
// which is why the primitive rather than each caller carries the instrumentation:
// no write path can be silently missed. The recorded action is derived as create
// or update from whether the (tier, key) already existed, unless a caller
// overrides it (WithCurationAction) or suppresses capture (used internally by the
// move composition). Capture is best-effort and never fails the write.
func WriteMemory(ctx context.Context, db *sql.DB, m Memory, opts ...CurationOption) error {
	tier, err := ParseTier(string(m.Tier))
	if err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	m.Tier = tier
	if err := validateTldr(m.Tldr); err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	if err := validateTrigger(m.Trigger); err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	if m.Trigger != "" && m.Tier != TierLong {
		return fmt.Errorf("write memory: trigger requires long tier")
	}
	if m.TS == 0 {
		m.TS = time.Now().UnixMilli()
	}

	// Determine create vs update before the upsert so the captured action is
	// accurate. Only needed when capture is on and the caller did not fix the
	// action itself, so ordinary suppressed/overridden writes skip the extra read.
	co := resolveCurationOptions(opts)
	action := co.action
	if !co.suppress && action == "" {
		existing, err := ReadMemory(ctx, db, m.Tier, m.Key)
		if err != nil {
			return fmt.Errorf("write memory: %w", err)
		}
		if existing == nil {
			action = CurationCreate
		} else {
			action = CurationUpdate
		}
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO memories (ts, tier, key, content, tldr, trigger, session_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tier, key) DO UPDATE SET
			ts = excluded.ts,
			content = excluded.content,
			tldr = excluded.tldr,
			trigger = excluded.trigger,
			session_id = excluded.session_id
	`, m.TS, m.Tier, m.Key, m.Content, m.Tldr, m.Trigger, m.SessionID)
	if err != nil {
		return fmt.Errorf("write memory: %w", err)
	}

	if !co.suppress {
		captureCuration(ctx, db, CurationEvent{
			TS:        m.TS,
			SessionID: firstNonEmpty(co.session, m.SessionID),
			Action:    action,
			Tier:      m.Tier,
			Key:       m.Key,
			DBScope:   co.scope,
			Source:    co.source,
			Content:   m.Content,
			Tldr:      m.Tldr,
			Trigger:   m.Trigger,
		})
	}
	return nil
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// SetMemoryTldr updates only the tldr of an existing memory, leaving its content
// and timestamp untouched. It is the surgical alternative to WriteMemory for
// curating summaries: WriteMemory is an upsert that demands the full content
// back (and silently creates a row on a typo'd key), whereas this never creates
// a row and reports whether the key matched. Timestamp is deliberately left
// alone -- a tldr edit is metadata, not a touch, so it must not reorder the
// memory in inject.
//
// The change needs no standing-file resync: the standing files render full
// content, not the tldr, and inject reads the tldr live from the DB.
func SetMemoryTldr(ctx context.Context, db *sql.DB, tier Tier, key, tldr string, opts ...CurationOption) (bool, error) {
	if err := validateTldr(tldr); err != nil {
		return false, fmt.Errorf("set tldr: %w", err)
	}
	res, err := db.ExecContext(ctx,
		`UPDATE memories SET tldr = ? WHERE tier = ? AND key = ?`, tldr, tier, key)
	if err != nil {
		return false, fmt.Errorf("set tldr: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	co := resolveCurationOptions(opts)
	if !co.suppress {
		// Snapshot the (unchanged) content alongside the new tldr so a later reader
		// sees both what the summary now says and the body it summarizes.
		var content string
		if m, err := ReadMemory(ctx, db, tier, key); err == nil && m != nil {
			content = m.Content
		}
		captureCuration(ctx, db, CurationEvent{
			SessionID: co.session,
			Action:    CurationTldrSet,
			Tier:      tier,
			Key:       key,
			DBScope:   co.scope,
			Source:    co.source,
			Content:   content,
			Tldr:      tldr,
		})
	}
	return true, nil
}

// queryMemories is the shared implementation for ReadMemory, ReadMemoryTop, ListMemories, and FindMemoryByKey.
// An empty tier matches all tiers.
func queryMemories(ctx context.Context, db *sql.DB, tier Tier, key string, limit int) ([]Memory, error) {
	q := `SELECT id, ts, tier, key, content, tldr, trigger, COALESCE(session_id, '') FROM memories WHERE true`
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
	return scanMemories(rows)
}

// scanMemories collects every row of the canonical memories projection
// (id, ts, tier, key, content, tldr, trigger, session_id) and closes the rows. Shared by
// queryMemories and SearchMemories, which return identically-shaped rows.
func scanMemories(rows *sql.Rows) ([]Memory, error) {
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.TS, &m.Tier, &m.Key, &m.Content, &m.Tldr, &m.Trigger, &m.SessionID); err != nil {
			return nil, fmt.Errorf("scan memory row: %w", err)
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

// ListSkills returns long-term memories with a retrieval trigger. Skills share
// memory storage deliberately: trigger is their task index, tldr their outcome,
// and content their full instructions.
func ListSkills(ctx context.Context, db *sql.DB) ([]Memory, error) {
	memories, err := ListMemories(ctx, db, TierLong)
	if err != nil {
		return nil, err
	}
	return skillMemories(memories), nil
}

func skillMemories(memories []Memory) []Memory {
	out := make([]Memory, 0, len(memories))
	for _, m := range memories {
		if m.Trigger != "" {
			out = append(out, m)
		}
	}
	return out
}

// DeleteMemory removes the memory with the given tier and key, returning an
// error if nothing was found. The removed content and tldr are snapshotted into
// the curation log before deletion, so a delete -- the most destructive curation
// action -- is fully recoverable from the append-only log. Capture is best-effort
// and only happens on a successful delete.
func DeleteMemory(ctx context.Context, db *sql.DB, tier Tier, key string, opts ...CurationOption) error {
	co := resolveCurationOptions(opts)
	var snapshot *Memory
	if !co.suppress {
		snapshot, _ = ReadMemory(ctx, db, tier, key)
	}
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
	if !co.suppress && snapshot != nil {
		action := co.action
		if action == "" {
			action = CurationDelete
		}
		captureCuration(ctx, db, CurationEvent{
			SessionID: firstNonEmpty(co.session, snapshot.SessionID),
			Action:    action,
			Tier:      tier,
			Key:       key,
			DBScope:   co.scope,
			Source:    co.source,
			Content:   snapshot.Content,
			Tldr:      snapshot.Tldr,
			Trigger:   snapshot.Trigger,
		})
	}
	return nil
}

// FindMemoryByKey searches all tiers for memories with the exact key.
func FindMemoryByKey(ctx context.Context, db *sql.DB, key string) ([]Memory, error) {
	return queryMemories(ctx, db, "", key, 0)
}

// MoveMemory moves a memory from one tier to another within the same database.
// It records a single curation event with action "move" rather than the
// create+delete pair its implementation is built from, because a reclassification
// (e.g. a demotion long -> cold) is a distinct reward signal from either writing
// or deleting. The constituent Write and Delete suppress their own capture.
func MoveMemory(ctx context.Context, db *sql.DB, key string, from, to Tier, opts ...CurationOption) error {
	canonicalTo, err := ParseTier(string(to))
	if err != nil {
		return fmt.Errorf("move memory: destination %w", err)
	}
	to = canonicalTo
	m, err := ReadMemory(ctx, db, from, key)
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("memory %q not found in tier %q", key, from)
	}
	m.Tier = to
	m.TS = time.Now().UnixMilli()
	if err := WriteMemory(ctx, db, *m, suppressCuration()); err != nil {
		return err
	}
	if err := DeleteMemory(ctx, db, from, key, suppressCuration()); err != nil {
		return err
	}
	co := resolveCurationOptions(opts)
	captureCuration(ctx, db, CurationEvent{
		TS:        m.TS,
		SessionID: firstNonEmpty(co.session, m.SessionID),
		Action:    CurationMove,
		Tier:      to,
		Key:       key,
		DBScope:   co.scope,
		Source:    co.source,
		Content:   m.Content,
		Tldr:      m.Tldr,
		Trigger:   m.Trigger,
		FromTier:  from,
		ToTier:    to,
		FromDB:    co.scope,
		ToDB:      co.scope,
	})
	return nil
}

// MoveMemoryAcrossDB relocates a memory from the src database to the dst
// database, reading it from tier `from` under srcKey and writing it to tier `to`
// under dstKey, then deleting the source. It is how a memory crosses the
// global<->project boundary (a same-database move is MoveMemory); dstKey lets the
// caller de-scope an agent-layer key when it lands in a project, which has no
// layers. The write happens before the delete so a failure leaves the source
// intact rather than losing the memory.
// A cross-database move records the move in both databases -- one event in the
// source (the memory departed) and one in the destination (it arrived) -- so
// neither log loses the fact of the move when the two are read independently.
// Both carry identical from/to tier and database fields; they differ only in the
// db_scope naming which log they live in.
func MoveMemoryAcrossDB(ctx context.Context, src, dst *sql.DB, srcKey, dstKey string, from, to Tier, opts ...CurationOption) error {
	canonicalTo, err := ParseTier(string(to))
	if err != nil {
		return fmt.Errorf("move memory across databases: destination %w", err)
	}
	to = canonicalTo
	m, err := ReadMemory(ctx, src, from, srcKey)
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("memory %q not found in tier %q", srcKey, from)
	}
	m.ID = 0 // dst assigns its own rowid
	m.Key = dstKey
	m.Tier = to
	m.TS = time.Now().UnixMilli()
	if err := WriteMemory(ctx, dst, *m, suppressCuration()); err != nil {
		return err
	}
	if err := DeleteMemory(ctx, src, from, srcKey, suppressCuration()); err != nil {
		return err
	}
	co := resolveCurationOptions(opts)
	event := CurationEvent{
		TS:        m.TS,
		SessionID: firstNonEmpty(co.session, m.SessionID),
		Action:    CurationMove,
		Tier:      to,
		Key:       dstKey,
		Source:    co.source,
		Content:   m.Content,
		Tldr:      m.Tldr,
		Trigger:   m.Trigger,
		FromTier:  from,
		ToTier:    to,
		FromDB:    co.scope,
		ToDB:      co.toScope,
	}
	srcEvent := event
	srcEvent.DBScope = co.scope
	dstEvent := event
	dstEvent.DBScope = co.toScope
	captureCuration(ctx, src, srcEvent)
	captureCuration(ctx, dst, dstEvent)
	return nil
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

// ftsMatchQuery turns free-text into a forgiving FTS5 MATCH expression: every
// alphanumeric token is double-quoted (which neutralizes FTS operator characters
// like ", -, *, and :, so arbitrary caller text can never provoke a MATCH syntax
// error) and the tokens are OR-ed together.
//
// OR, not the FTS5 bareword default of implicit AND, is the whole point. Under
// AND, "standup morning routine" requires a memory to contain all three tokens,
// so every extra word the caller adds can only ever drop hits -- recall shrinks
// as the query grows, exactly backwards for discovery. Under OR, more words widen
// the net and bm25 rank floats the best match to the top. It returns "" when the
// query holds no searchable token; callers treat that as "no results" rather than
// feeding an empty string to MATCH (which errors).
func ftsMatchQuery(raw string) string {
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for i, tok := range tokens {
		tokens[i] = `"` + tok + `"`
	}
	return strings.Join(tokens, " OR ")
}

// SearchMemories performs a full-text search over memories. If tier is
// non-empty, results are filtered to that tier.
func SearchMemories(ctx context.Context, db *sql.DB, query string, tier Tier) ([]Memory, error) {
	match := ftsMatchQuery(query)
	if match == "" {
		return nil, nil
	}
	q := `SELECT m.id, m.ts, m.tier, m.key, m.content, m.tldr, m.trigger, COALESCE(m.session_id, '')
		FROM memories_fts f
		JOIN memories m ON m.id = f.rowid
		WHERE memories_fts MATCH ?`
	args := []any{match}
	if tier != "" {
		q += ` AND m.tier = ?`
		args = append(args, tier)
	}
	q += ` ORDER BY rank`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search memories: %w", err)
	}
	return scanMemories(rows)
}

// SearchSkills searches every indexed skill field, then excludes ordinary
// long-term memory. The FTS index covers key, content, tldr, and trigger.
func SearchSkills(ctx context.Context, db *sql.DB, query string) ([]Memory, error) {
	memories, err := SearchMemories(ctx, db, query, TierLong)
	if err != nil {
		return nil, err
	}
	return skillMemories(memories), nil
}

// injectOutput is the JSON structure returned by the SessionStart hook.
type injectOutput struct {
	HookSpecificOutput injectHookOutput `json:"hookSpecificOutput"`
}

type injectHookOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// mergeMemories concatenates global entries ahead of project entries into a new
// slice, leaving both inputs' backing arrays untouched. Inject merges the two
// databases for every tier it surfaces from both (long, short, cold): a global
// long-term fact then loads in every project, while project facts stay local.
func mergeMemories(global, project []Memory) []Memory {
	merged := make([]Memory, 0, len(global)+len(project))
	merged = append(merged, global...)
	merged = append(merged, project...)
	return merged
}

// countPhrase renders "N of M label" when a budget dropped some entries
// (shown < total), else "M label". The orientation header uses it so its stated
// counts never overstate what actually rendered in the sections below.
func countPhrase(shown, total int, label string) string {
	if shown < total {
		return fmt.Sprintf("%d of %d %s", shown, total, label)
	}
	return fmt.Sprintf("%d %s", total, label)
}

// budgetNote returns the parenthetical appended to a capped section's header,
// or "" when the budget dropped nothing. It reports what rendered versus the
// total and appends a section-specific remedy, turning a silent truncation into
// a visible "showing N of M" with a hint about the rest.
func budgetNote(shown, total int, remedy string) string {
	if shown >= total {
		return ""
	}
	return fmt.Sprintf(" (showing %d of %d; %s)", shown, total, remedy)
}

// budgetLines keeps the leading lines whose cumulative length (including the
// newline separators that join them) fits within budget, returning the kept
// prefix and its length. A non-positive budget means unlimited. Callers pass
// lines most-recent-first, so the dropped remainder is always the stalest.
func budgetLines(lines []string, budget int) (kept []string, shown int) {
	if budget <= 0 {
		return lines, len(lines)
	}
	total := 0
	for i, ln := range lines {
		// Count RUNES, not bytes. Every caller passes an Inject*BudgetChars
		// constant, and MaxTldrLen measures the same kind of limit with
		// RuneCountInString -- so counting bytes here made the package disagree with
		// itself and silently truncated non-ASCII content earlier than the budget
		// promised. An em dash or an arrow in a tldr should cost one character, not
		// three.
		add := utf8.RuneCountInString(ln)
		if i > 0 {
			add++ // newline separator between lines
		}
		if total+add > budget {
			break
		}
		total += add
		kept = append(kept, ln)
	}
	return kept, len(kept)
}

// rollupFiles collapses a recency-ordered list of touched file paths into a
// compact directory activity summary. It never enumerates files by default:
// each directory renders as one `dir/ ×count` line, so a folder holding hundreds
// of generated files costs one line, not hundreds. A directory whose touched-file
// count exceeds splitThreshold is broken one level deeper and the rule reapplied,
// so resolution follows churn -- quiet corners stay coarse, hot areas resolve to
// their subdirectories. A leaf bucket at or below expandThreshold expands to its
// actual file paths, because at that size the names are cheap and carry more
// signal than a count. Buckets are ordered by the recency of their most-recent
// file, which the input ordering already encodes.
func rollupFiles(files []string, splitThreshold, expandThreshold int) []string {
	return rollupBucket(files, 0, splitThreshold, expandThreshold)
}

// rollupBucket renders one directory bucket: the files sharing the first `depth`
// path components. It is the recursive core of rollupFiles. Inputs stay in
// recency order (most-recent first) throughout, so emitting groups in
// first-appearance order preserves the recency sort for free.
func rollupBucket(files []string, depth, splitThreshold, expandThreshold int) []string {
	// A file is "deeper" than this bucket when it has a subdirectory below the
	// shared prefix; only then can splitting reduce anything. A bucket of files
	// that all sit directly in the prefix dir cannot be split, so an over-count
	// leaf just reports its count rather than enumerating filenames.
	deeperExists := false
	for _, f := range files {
		if len(strings.Split(f, "/")) > depth+1 {
			deeperExists = true
			break
		}
	}
	// depth 0 is the repo root, not a real directory bucket: always partition by
	// top-level component so a single-directory set recurses to depth 1 and leafs
	// with its true prefix rather than a bare ".".
	if depth > 0 && (len(files) <= splitThreshold || !deeperExists) {
		return rollupLeaf(files, depth, expandThreshold)
	}

	// Split: group deeper files by their next path component (the subdirectory),
	// and collect files sitting directly in this dir as a residual. Track each
	// group's first-appearance index so the emitted blocks keep recency order.
	type group struct {
		firstIdx int
		files    []string
		subdir   string // "" for the direct-file residual
	}
	groups := []*group{}
	index := map[string]int{}
	for i, f := range files {
		comps := strings.Split(f, "/")
		key, subdir := "", ""
		if len(comps) > depth+1 {
			subdir = comps[depth]
			key = "d:" + subdir
		} else {
			key = "residual"
		}
		gi, ok := index[key]
		if !ok {
			gi = len(groups)
			index[key] = gi
			groups = append(groups, &group{firstIdx: i, subdir: subdir})
		}
		groups[gi].files = append(groups[gi].files, f)
	}
	sort.SliceStable(groups, func(a, b int) bool {
		return groups[a].firstIdx < groups[b].firstIdx
	})

	var out []string
	for _, g := range groups {
		if g.subdir == "" {
			out = append(out, rollupLeaf(g.files, depth, expandThreshold)...)
		} else {
			out = append(out, rollupBucket(g.files, depth+1, splitThreshold, expandThreshold)...)
		}
	}
	return out
}

// rollupLeaf renders a terminal bucket: the file paths themselves when the count
// is small enough to be worth naming (at or below expandThreshold), otherwise a
// single `dir/ ×count` line keyed on the shared prefix.
func rollupLeaf(files []string, depth, expandThreshold int) []string {
	if len(files) <= expandThreshold {
		return append([]string(nil), files...)
	}
	prefix := strings.Join(strings.Split(files[0], "/")[:depth], "/")
	if prefix == "" {
		prefix = "."
	}
	return []string{fmt.Sprintf("%s/ ×%d", prefix, len(files))}
}

// InjectContextText formats global and project inject results as the plain-text
// session context (the markdown body injected at session start).
func InjectContextText(global, project InjectResult, nSessions int) string {
	var parts []string

	if len(global.Invariants) > 0 {
		lines := make([]string, len(global.Invariants))
		for i, m := range global.Invariants {
			lines[i] = fmt.Sprintf("**%s**: %s", m.Key, m.Content)
		}
		parts = append(parts, "## Identity\n"+strings.Join(lines, "\n"))
	}

	// Preferences merge global and project entries (global first), the same way
	// long/short/cold already do: a global preference applies everywhere, a project
	// preference only in its own repo. Identity stays global-only above -- who the
	// agent is does not change per project. Each line is a one-line summary, not the
	// full rule; the full text rides the @-imported standing file and `engram mem
	// read <key>`.
	prefs := mergeMemories(global.Preferences, project.Preferences)
	if len(prefs) > 0 {
		lines := make([]string, len(prefs))
		for i, m := range prefs {
			lines[i] = fmt.Sprintf("- **%s**: %s", m.Key, m.InjectSummary())
		}
		parts = append(parts, "## Preferences\n"+strings.Join(lines, "\n"))
	}

	if global.Agent != "" && (len(global.AgentInvariants) > 0 || len(global.AgentPreferences) > 0) {
		var lines []string
		lines = append(lines, "Agent-specific global guidance layered on top of the primary identity and preferences.")
		for _, m := range global.AgentInvariants {
			lines = append(lines, fmt.Sprintf("**%s**: %s", m.Key, m.Content))
		}
		for _, m := range global.AgentPreferences {
			lines = append(lines, fmt.Sprintf("- **%s**: %s", m.Key, m.InjectSummary()))
		}
		parts = append(parts, fmt.Sprintf("## Agent layer (%s)\n%s", global.Agent, strings.Join(lines, "\n")))
	}

	coldEntries := mergeMemories(global.Cold, project.Cold)
	if len(coldEntries) > 0 {
		lines := make([]string, len(coldEntries))
		for i, m := range coldEntries {
			lines[i] = fmt.Sprintf("- %s: %s", m.Key, m.InjectSummary())
		}
		parts = append(parts, "## Cold storage (index only -- fetch with: engram mem --tier cold read <key>)\n"+strings.Join(lines, "\n"))
	}

	if tools := global.AgentTools; len(tools) > 0 {
		lines := make([]string, len(tools))
		for i, t := range tools {
			lines[i] = fmt.Sprintf("- %s: %s", t.Command(), t.Desc)
		}
		parts = append(parts, "## Agent tools (invoke with the command shown; details in the script header)\n"+strings.Join(lines, "\n"))
	}
	if tools := project.ProjectTools; len(tools) > 0 {
		lines := make([]string, len(tools))
		for i, t := range tools {
			lines[i] = fmt.Sprintf("- %s: %s", t.Command(), t.Desc)
		}
		parts = append(parts, "## Project tools (repository-scoped; invoke with the command shown)\n"+strings.Join(lines, "\n"))
	}
	if members := project.SkillCandidates; len(members) > 0 {
		lines := make([]string, len(members))
		for i, member := range members {
			group := member.SkillKey
			if group == "" {
				group = "unclustered"
			}
			line := fmt.Sprintf("- **%s**: %s", group, member.Path)
			if member.Rationale != "" {
				line += " — " + member.Rationale
			}
			lines[i] = line
		}
		parts = append(parts, "## Skill candidates (classified workflow members; offer to create or update the grouped skill)\n"+strings.Join(lines, "\n"))
	}

	if len(global.PendingRestores) > 0 {
		lines := make([]string, len(global.PendingRestores))
		for i, p := range global.PendingRestores {
			line := fmt.Sprintf("- identity: %s | slot: %s | original: %s | stage: %s", p.Identity, p.Slot, p.OriginalPath, p.StagePath)
			if p.MatchesCurrent {
				line += " [MATCHES CURRENT REPO -- consider: engram restore --apply " + p.Identity + "]"
			}
			lines[i] = line
		}
		parts = append(parts, "## Staged restores (pending project snapshots -- agent: check for identity or near-miss match with current repo, prompt user to apply or discard)\n"+strings.Join(lines, "\n"))
	}

	if review := project.AutomationReview; review != nil && len(review.Items) > 0 {
		const shownLimit = 8
		limit := len(review.Items)
		if limit > shownLimit {
			limit = shownLimit
		}
		lines := make([]string, 0, limit+2)
		lines = append(lines, fmt.Sprintf(
			"%d automation catalog entries require review.", len(review.Items)))
		for _, item := range review.Items[:limit] {
			line := fmt.Sprintf("- %s: %s (%s)", item.State, item.Candidate.Path, item.Candidate.Kind)
			if item.Previous != nil {
				line += fmt.Sprintf("; previous: %s", item.Previous.Classification)
				if item.Previous.Rationale != "" {
					line += " — " + item.Previous.Rationale
				}
			}
			lines = append(lines, line)
		}
		if omitted := len(review.Items) - limit; omitted > 0 {
			lines = append(lines, fmt.Sprintf("- ... and %d more", omitted))
		}
		lines = append(lines,
			"Run `engram skill discover` to review only these entries. Preserve prior judgments when still valid; do not execute candidates merely to inspect them.")
		parts = append(parts, "## Automation catalog review\n"+strings.Join(lines, "\n"))
	}

	var skillLines []string
	for _, scoped := range []struct {
		memories []Memory
		scope    string
		readFlag string
	}{
		{global.LongTerm, "global", "read -g "},
		{project.LongTerm, "project", "read "},
	} {
		for _, m := range scoped.memories {
			if m.Trigger == "" {
				continue
			}
			skillLines = append(skillLines, fmt.Sprintf(
				"- **%s** (%s): %s — %s [read: `engram skill %s%s`]",
				m.Key, scoped.scope, m.Trigger, m.InjectSummary(), scoped.readFlag, m.Key))
		}
	}
	shownSkills := 0
	if len(skillLines) > 0 {
		kept, shown := budgetLines(skillLines, InjectSkillBudgetChars)
		shownSkills = shown
		remedy := fmt.Sprintf("%d over budget, prune with `engram skill list`", len(skillLines)-shown)
		parts = append(parts, "## Skills (trigger index — retrieve full instructions when the task matches)"+
			budgetNote(shown, len(skillLines), remedy)+"\n"+strings.Join(kept, "\n"))
	}

	allLongTerm := mergeMemories(global.LongTerm, project.LongTerm)
	longTerm := make([]Memory, 0, len(allLongTerm))
	for _, m := range allLongTerm {
		if m.Trigger == "" {
			longTerm = append(longTerm, m)
		}
	}
	shownLong, totalLong := shownSkills, len(allLongTerm)
	if len(longTerm) > 0 {
		lines := make([]string, len(longTerm))
		for i, m := range longTerm {
			lines[i] = fmt.Sprintf("- **%s**: %s", m.Key, m.InjectSummary())
		}
		kept, shown := budgetLines(lines, InjectLongBudgetChars)
		shownLong += shown
		remedy := fmt.Sprintf("%d over budget, prune with `engram mem list`", len(longTerm)-shown)
		parts = append(parts, "## Long-term memory"+budgetNote(shown, len(longTerm), remedy)+"\n"+strings.Join(kept, "\n"))
	}

	shortTerm := mergeMemories(global.ShortTerm, project.ShortTerm)
	shownShort, totalShort := 0, len(shortTerm)
	if len(shortTerm) > 0 {
		lines := make([]string, len(shortTerm))
		for i, m := range shortTerm {
			lines[i] = fmt.Sprintf("%d. [%s] %s", i+1, m.Key, m.InjectSummary())
		}
		kept, shown := budgetLines(lines, InjectShortBudgetChars)
		shownShort = shown
		remedy := fmt.Sprintf("%d over budget, prune with `engram mem list`", totalShort-shown)
		parts = append(parts, "## Short-term stack"+budgetNote(shown, totalShort, remedy)+"\n"+strings.Join(kept, "\n"))
	}

	if len(project.Files) > 0 {
		rollup := rollupFiles(project.Files, InjectSplitThreshold, InjectExpandThreshold)
		kept, shown := budgetLines(rollup, InjectAreasBudgetChars)
		header := fmt.Sprintf("## Recently active areas (last %d sessions)", nSessions) +
			budgetNote(shown, len(rollup), "older activity omitted")
		parts = append(parts, header+"\n  "+strings.Join(kept, "\n  "))
	}

	if len(parts) == 0 {
		return "Engram is active but not yet set up. " +
			"Ask your agent to set a personality, codename, and preferences with `engram mem write`."
	}

	// Lead with an explicit orientation header so the agent knows, without
	// parsing the personality prose below, that it arrived oriented and how to
	// open its first reply.
	return orientationHeader(global, project, shownLong, totalLong, shownShort, totalShort) + "\n\n" + strings.Join(parts, "\n\n")
}

// orientationHeader renders the leading "## Orientation" block: who the agent is
// (codename), what memory loaded, and how to open the first reply. It exists so
// orientation is a stated fact in the injected context rather than something the
// agent must infer. The shown/total long and short counts come from the budgeted
// sections so the header never claims more memory than actually rendered below.
func orientationHeader(global, project InjectResult, shownLong, totalLong, shownShort, totalShort int) string {
	who := "Oriented (no codename set)."
	if codename := displayCodename(invariantValue(global.AgentInvariants, "codename")); codename != "" {
		who = fmt.Sprintf("Oriented as %s.", codename)
	} else if codename := displayCodename(invariantValue(global.Invariants, "codename")); codename != "" {
		who = fmt.Sprintf("Oriented as %s.", codename)
	}
	counts := fmt.Sprintf("Memory loaded: %d identity, %d preferences, %s, %s.",
		len(global.Invariants), len(global.Preferences)+len(project.Preferences),
		countPhrase(shownLong, totalLong, "long-term"), countPhrase(shownShort, totalShort, "short-term"))
	if global.Agent != "" {
		counts += fmt.Sprintf(" Agent layer %s: %d identity, %d preferences.",
			global.Agent, len(global.AgentInvariants), len(global.AgentPreferences))
	}
	return "## Orientation\n" + who + " " + counts + "\n" +
		"First reply this session: open with a brief, in-character orientation sentence that " +
		"names your codename and confirms what loaded, then answer. Keep your codename present " +
		"in your voice throughout the session, not just at the start.\n" +
		"Preferences, long-term, short-term, and cold entries below are one-line summaries; " +
		"run `engram mem read <key>` for an entry's full text when it looks relevant to what " +
		"you are doing. Identity is shown in full.\n" +
		"If a section shows fewer entries than its header states, the rest were truncated " +
		"upstream: tell the user, suggest pruning that tier, and read what is missing with " +
		"`engram mem list` / `engram mem read <key>` so you are not acting on a partial memory."
}

// invariantValue returns the content of the invariant with the given key, or "".
func invariantValue(ms []Memory, key string) string {
	for _, m := range ms {
		if m.Key == key {
			return m.Content
		}
	}
	return ""
}

// displayCodename trims trailing punctuation and space so a stored "Cadence."
// renders cleanly inline as "Cadence".
func displayCodename(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ". ")
}

// FormatStatusLine renders the persistent status-line string used by `engram
// status`: the codename, then (inside a project) the project name and memory
// counts. Outside a project it shows only the codename, plus a short-tier count
// when there is pending in-flight context worth surfacing.
func FormatStatusLine(codename, project string, long, short int) string {
	name := displayCodename(codename)
	if name == "" {
		name = "engram"
	}
	parts := []string{name}
	switch {
	case project != "":
		parts = append(parts, project,
			fmt.Sprintf("%d long", long), fmt.Sprintf("%d short", short))
	case short > 0:
		parts = append(parts, fmt.Sprintf("%d short", short))
	}
	return strings.Join(parts, " · ")
}

// FormatInjectOutput wraps InjectContextText in the SessionStart hook JSON envelope.
func FormatInjectOutput(global, project InjectResult, nSessions int) []byte {
	return FormatInjectOutputText(InjectContextText(global, project, nSessions))
}

// FormatInjectOutputText wraps pre-assembled context text in the SessionStart hook JSON envelope.
func FormatInjectOutputText(text string) []byte {
	out, _ := json.Marshal(injectOutput{
		HookSpecificOutput: injectHookOutput{
			HookEventName:     "SessionStart",
			AdditionalContext: text,
		},
	})
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// FormatMemoryMD formats a slice of memories as a markdown document for the given tier.
// The output is parseable by ParseMemoryMD.
func FormatMemoryMD(tier Tier, memories []Memory) string {
	t := string(tier)
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- GENERATED by engram -- do not edit directly; use `engram mem` commands -->\n\n")
	fmt.Fprintf(&b, "# %s%s\n\n", strings.ToUpper(t[:1]), t[1:])
	for _, m := range memories {
		fmt.Fprintf(&b, "## %s\n", m.Key)
		// The tldr rides an HTML comment: invisible in rendered markdown, but
		// round-trips through ParseMemoryMD so a dump/load does not drop it.
		if m.Tldr != "" {
			fmt.Fprintf(&b, "<!-- tldr: %s -->\n", m.Tldr)
		}
		if m.Trigger != "" {
			fmt.Fprintf(&b, "<!-- trigger: %s -->\n", m.Trigger)
		}
		fmt.Fprintf(&b, "%s\n\n", m.Content)
	}
	return b.String()
}

// ParseMemoryMD parses a markdown document produced by FormatMemoryMD into
// Memory entries for the given tier.
func ParseMemoryMD(tier Tier, data string) ([]Memory, error) {
	var out []Memory
	var key, tldr, trigger string
	var contentLines []string
	now := time.Now().UnixMilli()

	flush := func() {
		if key == "" {
			return
		}
		out = append(out, Memory{
			TS:      now,
			Tier:    tier,
			Key:     key,
			Content: strings.TrimSpace(strings.Join(contentLines, "\n")),
			Tldr:    tldr,
			Trigger: trigger,
		})
		key = ""
		tldr = ""
		trigger = ""
		contentLines = nil
	}

	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			key = strings.TrimPrefix(line, "## ")
		case strings.HasPrefix(line, "# "):
			// tier header, skip
		case key != "" && len(contentLines) == 0 &&
			strings.HasPrefix(trimmed, "<!-- tldr:") && strings.HasSuffix(trimmed, "-->"):
			// The tldr comment sits between an entry's heading and its content.
			tldr = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "<!-- tldr:"), "-->"))
		case key != "" && len(contentLines) == 0 &&
			strings.HasPrefix(trimmed, "<!-- trigger:") && strings.HasSuffix(trimmed, "-->"):
			trigger = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "<!-- trigger:"), "-->"))
		case key != "":
			contentLines = append(contentLines, line)
		}
	}
	flush()
	return out, scanner.Err()
}
