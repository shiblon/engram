package engram

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxActiveTopics         = 8
	MaxActiveSubtopics      = 16
	MaxTopicNameChars       = 64
	MaxTopicPurposeChars    = 120
	MaxTopicRetireChars     = 120
	MaxTopicMessageChars    = 4000
	MaxTopicCheckpointChars = 8000
	TopicTailSoftChars      = 12000
	TopicTailHardChars      = 24000
	TopicCompactionLease    = 15 * time.Minute
)

// SQLite owns topic timestamps. julianday('now') is available across the SQLite
// versions Engram supports; the per-subtopic MAX guard below makes its coarse
// clock strictly increasing without involving an agent or process clock.
const sqliteNowMicros = `CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)`

var (
	ErrTopicNotFound           = errors.New("topic not found")
	ErrSubtopicNotFound        = errors.New("subtopic not found")
	ErrNothingToCompact        = errors.New("nothing to compact")
	ErrCompactionStageNotFound = errors.New("compaction stage not found")
	ErrCompactionStageExpired  = errors.New("compaction stage expired")
	ErrCompactionStageStale    = errors.New("compaction stage is stale")
)

type Topic struct {
	Name       string `json:"name"`
	Purpose    string `json:"purpose"`
	RetireWhen string `json:"retire_when"`
	CreatedAt  int64  `json:"created_at"`
}

type Subtopic struct {
	Topic                string `json:"topic"`
	Name                 string `json:"name"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
	CheckpointThrough    int64  `json:"checkpoint_through"`
	CheckpointGeneration int64  `json:"checkpoint_generation"`
	HeadAt               int64  `json:"head_at"`
	TailMessages         int    `json:"tail_messages"`
	TailChars            int    `json:"tail_chars"`
}

type TopicMessage struct {
	TS   int64  `json:"ts"`
	Body string `json:"body"`
}

type TopicRead struct {
	Topic             string         `json:"topic"`
	Subtopic          string         `json:"subtopic"`
	Checkpoint        string         `json:"checkpoint,omitempty"`
	CheckpointThrough int64          `json:"checkpoint_through,omitempty"`
	Messages          []TopicMessage `json:"messages,omitempty"`
	HeadAt            int64          `json:"head_at,omitempty"`
	HistoryCompacted  bool           `json:"history_compacted,omitempty"`
}

type TopicPostResult struct {
	Message               TopicMessage `json:"message"`
	Unseen                *TopicRead   `json:"unseen,omitempty"`
	CompactionRecommended bool         `json:"compaction_recommended,omitempty"`
}

type TopicCompactionStage struct {
	Token              string         `json:"token"`
	Topic              string         `json:"topic"`
	Subtopic           string         `json:"subtopic"`
	BaseGeneration     int64          `json:"base_generation"`
	CutoffTS           int64          `json:"cutoff_ts"`
	ExpiresAt          int64          `json:"expires_at"`
	PreviousCheckpoint string         `json:"previous_checkpoint,omitempty"`
	CheckpointThrough  int64          `json:"checkpoint_through,omitempty"`
	Messages           []TopicMessage `json:"messages"`
}

type TopicCompactionResult struct {
	Topic      string `json:"topic"`
	Subtopic   string `json:"subtopic"`
	Through    int64  `json:"through"`
	Generation int64  `json:"generation"`
}

type CompactionInProgressError struct {
	ExpiresAt int64
}

func (e *CompactionInProgressError) Error() string {
	return fmt.Sprintf("compaction already in progress until %s", FormatTopicTimestamp(e.ExpiresAt))
}

type TopicEvent struct {
	Event    string `json:"event"`
	Topic    string `json:"topic"`
	Subtopic string `json:"subtopic,omitempty"`
	TS       int64  `json:"ts,omitempty"`
}

type TopicMonitorOptions struct {
	After        *int64
	Once         bool
	PollInterval time.Duration
}

type topicQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func FormatTopicTimestamp(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.UnixMicro(ts).UTC().Format(time.RFC3339Nano)
}

func ParseTopicTimestamp(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("timestamp is empty")
	}
	if raw, err := strconv.ParseInt(value, 10, 64); err == nil {
		if raw < 0 {
			return 0, fmt.Errorf("timestamp must be non-negative")
		}
		return raw, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, fmt.Errorf("parse timestamp %q: use RFC3339 or Unix microseconds: %w", value, err)
	}
	return t.UnixMicro(), nil
}

func validateTopicPart(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if utf8.RuneCountInString(value) > MaxTopicNameChars {
		return fmt.Errorf("%s is too long (max %d characters)", label, MaxTopicNameChars)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("%s %q contains %q; use letters, numbers, dot, dash, or underscore", label, value, r)
	}
	return nil
}

func validateBoundedText(label, value string, max int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is empty", label)
	}
	if n := utf8.RuneCountInString(value); n > max {
		return "", fmt.Errorf("%s is too long: %d characters (max %d)", label, n, max)
	}
	return value, nil
}

func withImmediateTopicWrite(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
				log.Printf("engram topic: rollback immediate transaction: %v", err)
			}
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func CreateTopic(ctx context.Context, db *sql.DB, topic Topic) error {
	if err := validateTopicPart("topic", topic.Name); err != nil {
		return err
	}
	var err error
	if topic.Purpose, err = validateBoundedText("purpose", topic.Purpose, MaxTopicPurposeChars); err != nil {
		return err
	}
	if topic.RetireWhen, err = validateBoundedText("retire condition", topic.RetireWhen, MaxTopicRetireChars); err != nil {
		return err
	}
	return withImmediateTopicWrite(ctx, db, func(conn *sql.Conn) error {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topics`).Scan(&count); err != nil {
			return fmt.Errorf("count topics: %w", err)
		}
		if count >= MaxActiveTopics {
			return fmt.Errorf("active topic limit reached (%d); retire a topic first", MaxActiveTopics)
		}
		_, err := conn.ExecContext(ctx, `
			INSERT INTO topics (name, purpose, retire_when, created_at)
			VALUES (?, ?, ?, `+sqliteNowMicros+`)`, topic.Name, topic.Purpose, topic.RetireWhen)
		if err != nil {
			return fmt.Errorf("create topic %q: %w", topic.Name, err)
		}
		return nil
	})
}

func ListTopics(ctx context.Context, db *sql.DB) ([]Topic, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name, purpose, retire_when, created_at
		FROM topics ORDER BY created_at, name`)
	if err != nil {
		return nil, fmt.Errorf("list topics: %w", err)
	}
	defer rows.Close()
	var topics []Topic
	for rows.Next() {
		var topic Topic
		if err := rows.Scan(&topic.Name, &topic.Purpose, &topic.RetireWhen, &topic.CreatedAt); err != nil {
			return nil, fmt.Errorf("list topics: %w", err)
		}
		topics = append(topics, topic)
	}
	return topics, rows.Err()
}

func RetireTopic(ctx context.Context, db *sql.DB, name string) (bool, error) {
	if err := validateTopicPart("topic", name); err != nil {
		return false, err
	}
	found := false
	err := withImmediateTopicWrite(ctx, db, func(conn *sql.Conn) error {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topics WHERE name = ?`, name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		found = true
		for _, stmt := range []string{
			`DELETE FROM topic_compaction_stages WHERE subtopic_id IN (SELECT id FROM topic_subtopics WHERE topic_name = ?)`,
			`DELETE FROM topic_messages WHERE subtopic_id IN (SELECT id FROM topic_subtopics WHERE topic_name = ?)`,
			`DELETE FROM topic_subtopics WHERE topic_name = ?`,
			`DELETE FROM topics WHERE name = ?`,
		} {
			if _, err := conn.ExecContext(ctx, stmt, name); err != nil {
				return fmt.Errorf("retire topic %q: %w", name, err)
			}
		}
		return nil
	})
	return found, err
}

func ListSubtopics(ctx context.Context, db *sql.DB, topic string) ([]Subtopic, error) {
	if err := validateTopicPart("topic", topic); err != nil {
		return nil, err
	}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM topics WHERE name = ?`, topic).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, fmt.Errorf("%w: %s", ErrTopicNotFound, topic)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT s.name, s.created_at, s.updated_at, s.checkpoint_through,
		       s.checkpoint_generation,
		       MAX(s.checkpoint_through, COALESCE(MAX(m.ts), 0)),
		       COUNT(m.id), COALESCE(SUM(length(m.body)), 0)
		FROM topic_subtopics s
		LEFT JOIN topic_messages m ON m.subtopic_id = s.id
		WHERE s.topic_name = ?
		GROUP BY s.id
		ORDER BY s.name`, topic)
	if err != nil {
		return nil, fmt.Errorf("list subtopics: %w", err)
	}
	defer rows.Close()
	var out []Subtopic
	for rows.Next() {
		var s Subtopic
		s.Topic = topic
		if err := rows.Scan(&s.Name, &s.CreatedAt, &s.UpdatedAt, &s.CheckpointThrough,
			&s.CheckpointGeneration, &s.HeadAt, &s.TailMessages, &s.TailChars); err != nil {
			return nil, fmt.Errorf("list subtopics: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type subtopicRow struct {
	ID                   int64
	Topic                string
	Name                 string
	Checkpoint           string
	CheckpointThrough    int64
	CheckpointGeneration int64
}

func lookupSubtopic(ctx context.Context, q topicQuerier, topic, subtopic string) (subtopicRow, error) {
	var s subtopicRow
	err := q.QueryRowContext(ctx, `
		SELECT s.id, s.topic_name, s.name, s.checkpoint,
		       s.checkpoint_through, s.checkpoint_generation
		FROM topic_subtopics s JOIN topics t ON t.name = s.topic_name
		WHERE s.topic_name = ? AND s.name = ?`, topic, subtopic).
		Scan(&s.ID, &s.Topic, &s.Name, &s.Checkpoint, &s.CheckpointThrough, &s.CheckpointGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("%w: %s/%s", ErrSubtopicNotFound, topic, subtopic)
	}
	if err != nil {
		return s, err
	}
	return s, nil
}

func ensureSubtopic(ctx context.Context, conn *sql.Conn, topic, subtopic string) (subtopicRow, error) {
	s, err := lookupSubtopic(ctx, conn, topic, subtopic)
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, ErrSubtopicNotFound) {
		return s, err
	}
	var topicExists int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topics WHERE name = ?`, topic).Scan(&topicExists); err != nil {
		return s, err
	}
	if topicExists == 0 {
		return s, fmt.Errorf("%w: %s", ErrTopicNotFound, topic)
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_subtopics WHERE topic_name = ?`, topic).Scan(&count); err != nil {
		return s, err
	}
	if count >= MaxActiveSubtopics {
		return s, fmt.Errorf("active subtopic limit reached for %s (%d)", topic, MaxActiveSubtopics)
	}
	_, err = conn.ExecContext(ctx, `
		INSERT INTO topic_subtopics (topic_name, name, created_at, updated_at)
		VALUES (?, ?, `+sqliteNowMicros+`, `+sqliteNowMicros+`)`, topic, subtopic)
	if err != nil {
		return s, fmt.Errorf("create subtopic %s/%s: %w", topic, subtopic, err)
	}
	return lookupSubtopic(ctx, conn, topic, subtopic)
}

func ReadSubtopic(ctx context.Context, db *sql.DB, topic, subtopic string, after *int64) (TopicRead, error) {
	if err := validateTopicPart("topic", topic); err != nil {
		return TopicRead{}, err
	}
	if err := validateTopicPart("subtopic", subtopic); err != nil {
		return TopicRead{}, err
	}
	s, err := lookupSubtopic(ctx, db, topic, subtopic)
	if err != nil {
		return TopicRead{}, err
	}
	return readSubtopicRow(ctx, db, s, after)
}

func readSubtopicRow(ctx context.Context, q topicQuerier, s subtopicRow, after *int64) (TopicRead, error) {
	result := TopicRead{Topic: s.Topic, Subtopic: s.Name}
	lower := int64(0)
	if after != nil {
		lower = *after
	}
	if s.Checkpoint != "" && (after == nil || *after < s.CheckpointThrough) {
		result.Checkpoint = s.Checkpoint
		result.CheckpointThrough = s.CheckpointThrough
		if after != nil {
			result.HistoryCompacted = true
		}
		if s.CheckpointThrough > lower {
			lower = s.CheckpointThrough
		}
	}
	rows, err := q.QueryContext(ctx, `
		SELECT ts, body FROM topic_messages
		WHERE subtopic_id = ? AND ts > ? ORDER BY ts`, s.ID, lower)
	if err != nil {
		return TopicRead{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var message TopicMessage
		if err := rows.Scan(&message.TS, &message.Body); err != nil {
			return TopicRead{}, err
		}
		result.Messages = append(result.Messages, message)
		result.HeadAt = message.TS
	}
	if err := rows.Err(); err != nil {
		return TopicRead{}, err
	}
	if result.HeadAt < s.CheckpointThrough {
		result.HeadAt = s.CheckpointThrough
	}
	return result, nil
}

func PostTopicMessage(ctx context.Context, db *sql.DB, topic, subtopic, body string, after *int64) (TopicPostResult, error) {
	if err := validateTopicPart("topic", topic); err != nil {
		return TopicPostResult{}, err
	}
	if err := validateTopicPart("subtopic", subtopic); err != nil {
		return TopicPostResult{}, err
	}
	body, err := validateBoundedText("message", body, MaxTopicMessageChars)
	if err != nil {
		return TopicPostResult{}, err
	}
	var result TopicPostResult
	err = withImmediateTopicWrite(ctx, db, func(conn *sql.Conn) error {
		s, err := ensureSubtopic(ctx, conn, topic, subtopic)
		if err != nil {
			return err
		}
		var tailChars int
		if err := conn.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(length(body)), 0) FROM topic_messages WHERE subtopic_id = ?`, s.ID).
			Scan(&tailChars); err != nil {
			return err
		}
		if tailChars+utf8.RuneCountInString(body) > TopicTailHardChars {
			return fmt.Errorf("subtopic tail reached %d characters; compact %s/%s before posting",
				TopicTailHardChars, topic, subtopic)
		}
		if after != nil {
			unseen, err := readSubtopicRow(ctx, conn, s, after)
			if err != nil {
				return err
			}
			result.Unseen = &unseen
		}
		if err := conn.QueryRowContext(ctx, `
			INSERT INTO topic_messages (subtopic_id, ts, body)
			SELECT s.id,
			       MAX(`+sqliteNowMicros+`,
			           MAX(s.checkpoint_through,
			               COALESCE((SELECT MAX(m.ts) FROM topic_messages m WHERE m.subtopic_id = s.id), 0)) + 1),
			       ?
			FROM topic_subtopics s WHERE s.id = ?
			RETURNING ts`, body, s.ID).Scan(&result.Message.TS); err != nil {
			return fmt.Errorf("post to %s/%s: %w", topic, subtopic, err)
		}
		result.Message.Body = body
		if _, err := conn.ExecContext(ctx,
			`UPDATE topic_subtopics SET updated_at = ? WHERE id = ?`, result.Message.TS, s.ID); err != nil {
			return err
		}
		result.CompactionRecommended = tailChars+utf8.RuneCountInString(body) >= TopicTailSoftChars
		return nil
	})
	return result, err
}

func newCompactionToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func BeginTopicCompaction(ctx context.Context, db *sql.DB, topic, subtopic string) (TopicCompactionStage, error) {
	if err := validateTopicPart("topic", topic); err != nil {
		return TopicCompactionStage{}, err
	}
	if err := validateTopicPart("subtopic", subtopic); err != nil {
		return TopicCompactionStage{}, err
	}
	token, err := newCompactionToken()
	if err != nil {
		return TopicCompactionStage{}, err
	}
	stage := TopicCompactionStage{Token: token, Topic: topic, Subtopic: subtopic}
	err = withImmediateTopicWrite(ctx, db, func(conn *sql.Conn) error {
		var now int64
		if err := conn.QueryRowContext(ctx, `SELECT `+sqliteNowMicros).Scan(&now); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx,
			`DELETE FROM topic_compaction_stages WHERE expires_at <= ?`, now); err != nil {
			return err
		}
		s, err := lookupSubtopic(ctx, conn, topic, subtopic)
		if err != nil {
			return err
		}
		var activeExpiry int64
		err = conn.QueryRowContext(ctx,
			`SELECT expires_at FROM topic_compaction_stages WHERE subtopic_id = ?`, s.ID).Scan(&activeExpiry)
		if err == nil {
			return &CompactionInProgressError{ExpiresAt: activeExpiry}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var cutoff sql.NullInt64
		if err := conn.QueryRowContext(ctx,
			`SELECT MAX(ts) FROM topic_messages WHERE subtopic_id = ?`, s.ID).Scan(&cutoff); err != nil {
			return err
		}
		if !cutoff.Valid {
			return fmt.Errorf("%w: %s/%s", ErrNothingToCompact, topic, subtopic)
		}
		stage.BaseGeneration = s.CheckpointGeneration
		stage.CutoffTS = cutoff.Int64
		stage.ExpiresAt = now + TopicCompactionLease.Microseconds()
		stage.PreviousCheckpoint = s.Checkpoint
		stage.CheckpointThrough = s.CheckpointThrough
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO topic_compaction_stages
			    (token, subtopic_id, base_generation, cutoff_ts, started_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)`, stage.Token, s.ID, stage.BaseGeneration,
			stage.CutoffTS, now, stage.ExpiresAt); err != nil {
			return fmt.Errorf("begin compaction: %w", err)
		}
		rows, err := conn.QueryContext(ctx, `
			SELECT ts, body FROM topic_messages
			WHERE subtopic_id = ? AND ts <= ? ORDER BY ts`, s.ID, stage.CutoffTS)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var message TopicMessage
			if err := rows.Scan(&message.TS, &message.Body); err != nil {
				return err
			}
			stage.Messages = append(stage.Messages, message)
		}
		return rows.Err()
	})
	return stage, err
}

func ApplyTopicCompaction(ctx context.Context, db *sql.DB, token, checkpoint string) (TopicCompactionResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return TopicCompactionResult{}, fmt.Errorf("stage token is empty")
	}
	checkpoint, err := validateBoundedText("checkpoint", checkpoint, MaxTopicCheckpointChars)
	if err != nil {
		return TopicCompactionResult{}, err
	}
	var result TopicCompactionResult
	var outcomeErr error
	err = withImmediateTopicWrite(ctx, db, func(conn *sql.Conn) error {
		var subtopicID, baseGeneration, currentGeneration, cutoff, expires, now int64
		err := conn.QueryRowContext(ctx, `
			SELECT s.id, s.topic_name, s.name, c.base_generation,
			       s.checkpoint_generation, c.cutoff_ts, c.expires_at, `+sqliteNowMicros+`
			FROM topic_compaction_stages c
			JOIN topic_subtopics s ON s.id = c.subtopic_id
			WHERE c.token = ?`, token).
			Scan(&subtopicID, &result.Topic, &result.Subtopic, &baseGeneration,
				&currentGeneration, &cutoff, &expires, &now)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCompactionStageNotFound
		}
		if err != nil {
			return err
		}
		if expires <= now {
			if _, err := conn.ExecContext(ctx, `DELETE FROM topic_compaction_stages WHERE token = ?`, token); err != nil {
				return err
			}
			outcomeErr = ErrCompactionStageExpired
			return nil
		}
		if currentGeneration != baseGeneration {
			if _, err := conn.ExecContext(ctx, `DELETE FROM topic_compaction_stages WHERE token = ?`, token); err != nil {
				return err
			}
			outcomeErr = ErrCompactionStageStale
			return nil
		}
		update, err := conn.ExecContext(ctx, `
			UPDATE topic_subtopics
			SET checkpoint = ?, checkpoint_through = ?,
			    checkpoint_generation = checkpoint_generation + 1
			WHERE id = ? AND checkpoint_generation = ?`,
			checkpoint, cutoff, subtopicID, baseGeneration)
		if err != nil {
			return err
		}
		rows, err := update.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			if _, err := conn.ExecContext(ctx, `DELETE FROM topic_compaction_stages WHERE token = ?`, token); err != nil {
				return err
			}
			outcomeErr = ErrCompactionStageStale
			return nil
		}
		if _, err := conn.ExecContext(ctx,
			`DELETE FROM topic_messages WHERE subtopic_id = ? AND ts <= ?`, subtopicID, cutoff); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx,
			`DELETE FROM topic_compaction_stages WHERE token = ?`, token); err != nil {
			return err
		}
		result.Through = cutoff
		result.Generation = baseGeneration + 1
		return nil
	})
	if err == nil && outcomeErr != nil {
		err = outcomeErr
	}
	return result, err
}

func CancelTopicCompaction(ctx context.Context, db *sql.DB, token string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, fmt.Errorf("stage token is empty")
	}
	cancelled := false
	err := withImmediateTopicWrite(ctx, db, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `DELETE FROM topic_compaction_stages WHERE token = ?`, token)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		cancelled = n == 1
		return err
	})
	return cancelled, err
}

func topicHeads(ctx context.Context, db *sql.DB, topic, subtopic string) (map[string]int64, error) {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM topics WHERE name = ?`, topic).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, fmt.Errorf("%w: %s", ErrTopicNotFound, topic)
	}
	query := `
		SELECT s.name, MAX(s.checkpoint_through, COALESCE(MAX(m.ts), 0))
		FROM topic_subtopics s LEFT JOIN topic_messages m ON m.subtopic_id = s.id
		WHERE s.topic_name = ?`
	args := []any{topic}
	if subtopic != "" {
		query += ` AND s.name = ?`
		args = append(args, subtopic)
	}
	query += ` GROUP BY s.id ORDER BY s.name`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	heads := make(map[string]int64)
	for rows.Next() {
		var name string
		var head int64
		if err := rows.Scan(&name, &head); err != nil {
			return nil, err
		}
		heads[name] = head
	}
	return heads, rows.Err()
}

func MonitorTopic(ctx context.Context, db *sql.DB, topic, subtopic string, opts TopicMonitorOptions, emit func(TopicEvent) error) error {
	if err := validateTopicPart("topic", topic); err != nil {
		return err
	}
	if subtopic != "" {
		if err := validateTopicPart("subtopic", subtopic); err != nil {
			return err
		}
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 750 * time.Millisecond
	}
	heads, err := topicHeads(ctx, db, topic, subtopic)
	if err != nil {
		return err
	}
	seen := make(map[string]int64, len(heads)+1)
	if opts.After == nil {
		for name, head := range heads {
			seen[name] = head
		}
	} else {
		for name := range heads {
			seen[name] = *opts.After
		}
		if subtopic != "" && len(heads) == 0 {
			seen[subtopic] = *opts.After
		}
	}

	check := func() (changed, stop bool, err error) {
		current, err := topicHeads(ctx, db, topic, subtopic)
		if errors.Is(err, ErrTopicNotFound) {
			return true, true, emit(TopicEvent{Event: "retired", Topic: topic})
		}
		if err != nil {
			return false, false, err
		}
		var events []TopicEvent
		for name, head := range current {
			if head > seen[name] {
				events = append(events, TopicEvent{Event: "message", Topic: topic, Subtopic: name, TS: head})
				seen[name] = head
			}
		}
		sort.Slice(events, func(i, j int) bool {
			if events[i].TS == events[j].TS {
				return events[i].Subtopic < events[j].Subtopic
			}
			return events[i].TS < events[j].TS
		})
		for _, event := range events {
			if err := emit(event); err != nil {
				return false, false, err
			}
		}
		return len(events) > 0, false, nil
	}

	if opts.After != nil {
		changed, stop, err := check()
		if err != nil || stop || (changed && opts.Once) {
			return err
		}
	}
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			changed, stop, err := check()
			if err != nil {
				return err
			}
			if stop || (changed && opts.Once) {
				return nil
			}
		}
	}
}
