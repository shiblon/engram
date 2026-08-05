package engram

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// curation_events is an append-only instrumentation log of human curation
// actions on memory. It exists to preserve a signal the working store discards:
// the memories table is a last-write-wins upsert keyed on (tier, key), so an
// overwrite or a delete erases all prior history. Every mutating curation action
// writes one immutable row here instead, snapshotting the content and tldr at
// event time so the sign/valence of the action can be classified later from the
// text. Rows are never updated or deleted except by rotation (see Prune).
//
// This is capture only. No learning, weighting, or feature logic lives here or
// consumes this table yet; it is the raw substrate a future learning layer will
// read.

// CurationAction names one mutating curation action. Every memory-mutation path
// maps to exactly one of these.
type CurationAction string

const (
	// CurationCreate is the first write of a (tier, key) that did not exist.
	CurationCreate CurationAction = "create"
	// CurationUpdate overwrites an existing (tier, key).
	CurationUpdate CurationAction = "update"
	// CurationDelete removes a memory; the row snapshots the removed content.
	CurationDelete CurationAction = "delete"
	// CurationMove reclassifies a memory across tiers and/or databases. It is one
	// event, not a delete+create pair, because a demotion (e.g. long -> cold) is a
	// distinct signal from either a deletion or a fresh write.
	CurationMove CurationAction = "move"
	// CurationTldrSet curates a memory's one-line summary without rewriting content.
	CurationTldrSet CurationAction = "tldr-set"
	// CurationSkillAdopt promotes an existing long-term memory into a skill by
	// attaching a retrieval trigger, without rewriting its body.
	CurationSkillAdopt CurationAction = "skill-adopt"
	// CurationSkillClassify records a verdict on a repository automation entry
	// point. Its key is the automation path; content holds the classification and
	// tldr holds the rationale.
	CurationSkillClassify CurationAction = "skill-classify"
)

// CurationSource tags how a mutation was triggered so administrative bulk writes
// can be filtered out of the reward signal later. Empty means unknown.
type CurationSource string

const (
	// SourceInteractive is a direct, one-at-a-time curation command (mem/skill).
	SourceInteractive CurationSource = "interactive"
	// SourceLoad is a bulk write from `mem load` restoring dumped markdown.
	SourceLoad CurationSource = "load"
	// SourceImport is a bulk write from an import path.
	SourceImport CurationSource = "import"
	// SourceMigrate is a bulk write from a legacy-database migration.
	SourceMigrate CurationSource = "migrate"
	// SourceBootstrap is a seed write performed while wiring engram into a platform.
	SourceBootstrap CurationSource = "bootstrap"
)

// CurationEvent is one append-only row in the curation log. Content and Tldr are
// snapshots taken at event time (for a delete, the removed values), sized without
// concern because curation events are sparse.
type CurationEvent struct {
	ID        int64
	TS        int64 // unix epoch ms; zero means use current time
	SessionID string
	Action    CurationAction
	Tier      Tier
	Key       string
	// DBScope is "project" or "global": which database the mutated memory lives
	// in. It duplicates information already implied by which database this row was
	// written to, but recording it explicitly keeps a single merged read of both
	// logs self-describing. Empty when the caller did not supply it.
	DBScope string
	Source  CurationSource
	Content string
	Tldr    string
	Trigger string
	// FromTier/ToTier and FromDB/ToDB are populated only for CurationMove, naming
	// the tier and database the memory moved between. A cross-database move records
	// one event in each database, each carrying the same from/to values.
	FromTier Tier
	ToTier   Tier
	FromDB   string
	ToDB     string
}

// curationExecer is the minimal write surface RecordCuration needs. Both *sql.DB
// and *sql.Tx satisfy it, so a curation event can be recorded standalone or
// inside a caller's transaction (e.g. the batched skill-classify path), making
// the capture atomic with the mutation it describes.
type curationExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// curationOptions carries the context the primitive data-access functions cannot
// derive on their own (acting session, database scope, trigger source, and an
// explicit action override), plus an internal suppression flag used by the move
// composition so a move does not also emit its constituent create/delete events.
type curationOptions struct {
	action   CurationAction // "" means derive (create vs update, or the primitive's default)
	source   CurationSource
	session  string
	scope    string
	toScope  string // move only: destination database scope
	suppress bool
}

// CurationOption enriches a mutating call with curation metadata. Passing none
// leaves capture on with derived defaults, so no mutation path is silently
// missed; callers that know more (the CLI knows scope and source) supply it.
type CurationOption func(*curationOptions)

// WithCurationSource tags the trigger source of a mutation.
func WithCurationSource(s CurationSource) CurationOption {
	return func(o *curationOptions) { o.source = s }
}

// WithCurationSession sets the acting session id for a mutation.
func WithCurationSession(id string) CurationOption {
	return func(o *curationOptions) { o.session = id }
}

// WithCurationScope sets the database scope ("project"/"global") of the mutated
// memory. For a move it is the source scope.
func WithCurationScope(scope string) CurationOption {
	return func(o *curationOptions) { o.scope = scope }
}

// WithCurationToScope sets the destination database scope of a cross-database
// move.
func WithCurationToScope(scope string) CurationOption {
	return func(o *curationOptions) { o.toScope = scope }
}

// WithCurationAction overrides the derived action. It is how the skill-adopt path
// labels a write that would otherwise read as an ordinary update.
func WithCurationAction(a CurationAction) CurationOption {
	return func(o *curationOptions) { o.action = a }
}

// suppressCuration turns off capture for a single call. It is unexported because
// suppression is only ever correct internally, where a higher-level action (a
// move) records a single event in place of the primitives it is built from.
func suppressCuration() CurationOption {
	return func(o *curationOptions) { o.suppress = true }
}

func resolveCurationOptions(opts []CurationOption) curationOptions {
	var o curationOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// RecordCuration appends one immutable curation event. It never mutates or
// deletes an existing row.
func RecordCuration(ctx context.Context, db curationExecer, ev CurationEvent) error {
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO curation_events
		    (ts, session_id, action, tier, key, db_scope, source, content, tldr, trigger, from_tier, to_tier, from_db, to_db)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.TS, ev.SessionID, ev.Action, ev.Tier, ev.Key, ev.DBScope, ev.Source,
		ev.Content, ev.Tldr, ev.Trigger, ev.FromTier, ev.ToTier, ev.FromDB, ev.ToDB,
	)
	if err != nil {
		return fmt.Errorf("record curation event: %w", err)
	}
	return nil
}

// captureCuration records a curation event best-effort: a logging failure must
// never fail the mutation it instruments. The failure is logged, never silently
// discarded, so a broken capture is visible rather than a hidden data loss.
func captureCuration(ctx context.Context, db curationExecer, ev CurationEvent) {
	// The log is append-only and exists to be a learning signal, so a value that is
	// not part of the vocabulary corrupts that signal permanently and silently. Every
	// one of these fields is set by engram's own code, so an unknown value is a
	// developer mistake rather than untrusted input -- which is exactly the kind that
	// survives review unnoticed. Complain loudly, then write the row anyway: an event
	// with a flagged field is more useful than no event, and capture must never fail
	// the mutation it is recording.
	for field, problem := range map[string]string{
		"action": validCurationAction(ev.Action),
		"source": validCurationSource(ev.Source),
		"scope":  validCurationScope(ev.DBScope),
	} {
		if problem != "" {
			log.Printf("engram: curation event for %s/%s has an out-of-vocabulary %s: %s",
				ev.Tier, ev.Key, field, problem)
		}
	}
	if err := RecordCuration(ctx, db, ev); err != nil {
		log.Printf("engram: capture curation event (%s %s/%s): %v", ev.Action, ev.Tier, ev.Key, err)
	}
}

// validCurationAction returns an empty string when action is in the vocabulary,
// otherwise a description of the problem.
func validCurationAction(action CurationAction) string {
	switch action {
	case CurationCreate, CurationUpdate, CurationDelete, CurationMove,
		CurationTldrSet, CurationSkillAdopt, CurationSkillClassify:
		return ""
	case "":
		return "empty"
	}
	return fmt.Sprintf("%q", action)
}

func validCurationSource(source CurationSource) string {
	switch source {
	case SourceInteractive, SourceLoad, SourceImport, SourceMigrate, SourceBootstrap, "":
		return ""
	}
	return fmt.Sprintf("%q", source)
}

// validCurationScope allows empty: DBScope is documented as optional, since which
// database a row lives in is already implied by which log it was written to.
func validCurationScope(scope string) string {
	switch scope {
	case "project", "global", "":
		return ""
	}
	return fmt.Sprintf("%q", scope)
}

// CurationFilter narrows a ListCurationEvents query. Zero values match all.
type CurationFilter struct {
	Action    CurationAction
	SessionID string
	Key       string
	Limit     int
}

// ListCurationEvents returns curation events most-recent-first, optionally
// filtered. It is the read path behind `engram curation`.
func ListCurationEvents(ctx context.Context, db *sql.DB, f CurationFilter) ([]CurationEvent, error) {
	q := `SELECT id, ts, session_id, action, tier, key, db_scope, source, content, tldr, trigger, from_tier, to_tier, from_db, to_db
		FROM curation_events WHERE true`
	var args []any
	if f.Action != "" {
		q += ` AND action = ?`
		args = append(args, f.Action)
	}
	if f.SessionID != "" {
		q += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	if f.Key != "" {
		q += ` AND key = ?`
		args = append(args, f.Key)
	}
	q += ` ORDER BY ts DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list curation events: %w", err)
	}
	defer rows.Close()
	var out []CurationEvent
	for rows.Next() {
		var ev CurationEvent
		if err := rows.Scan(&ev.ID, &ev.TS, &ev.SessionID, &ev.Action, &ev.Tier, &ev.Key,
			&ev.DBScope, &ev.Source, &ev.Content, &ev.Tldr, &ev.Trigger,
			&ev.FromTier, &ev.ToTier, &ev.FromDB, &ev.ToDB); err != nil {
			return nil, fmt.Errorf("scan curation event: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
