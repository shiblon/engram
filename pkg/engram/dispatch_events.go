package engram

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// DispatchEventVersion rides on every emitted line. A parser that checks it fails
// loudly on schema drift instead of quietly misreading a renamed field.
const DispatchEventVersion = 1

// Event types. The set is deliberately minimal; adding one is a schema change a
// consumer must be told about.
const (
	EventBatchStart = "batch_start"
	EventTaskStart  = "task_start"
	EventStatus     = "status"
	EventTaskDone   = "task_done"
	EventBatchDone  = "batch_done"
)

// TaskState is a child's terminal state. Per-task progress is unavailable by
// construction, because the provider CLI will not report it, so state means
// running, finished, or failed -- never percent complete.
type TaskState string

const (
	// TaskStateOK means the child exited cleanly and the provider's own output
	// did not report an error.
	TaskStateOK TaskState = "ok"
	// TaskStateFailed means the spec ran and the work did not succeed.
	TaskStateFailed TaskState = "failed"
	// TaskStateTimeout means the child hit its wall-clock deadline and its
	// process group was torn down.
	TaskStateTimeout TaskState = "timeout"
	// TaskStateSpecError means the invocation itself was rejected, so the spec
	// is wrong rather than the work. This is the state that should trigger a
	// re-learn instead of a retry.
	TaskStateSpecError TaskState = "spec_error"
	// TaskStateStartError means the child never ran: executable missing, temp
	// file unwritable, spec invalid.
	TaskStateStartError TaskState = "start_error"
)

// BatchState summarizes a whole fan-out.
const (
	BatchStateOK      = "ok"
	BatchStatePartial = "partial"
	BatchStateFailed  = "failed"
)

// TokenUsage carries provider-reported token counts where they are available. A
// fan-out's cost is otherwise invisible until the bill arrives.
type TokenUsage struct {
	Input         int `json:"input,omitempty"`
	Output        int `json:"output,omitempty"`
	CacheCreation int `json:"cache_creation,omitempty"`
	CacheRead     int `json:"cache_read,omitempty"`
	// Reasoning counts codex's reasoning output tokens, which claude does not
	// report separately.
	Reasoning int `json:"reasoning,omitempty"`
}

// TaskResult is one child's outcome. It is repeated verbatim inside batch_done, so
// a caller that read nothing until exit still receives the whole answer.
type TaskResult struct {
	Task     string    `json:"task"`
	Provider string    `json:"provider"`
	State    TaskState `json:"state"`
	ExitCode int       `json:"exit_code"`

	RequestedModel string `json:"requested_model,omitempty"`
	// ReportedModel is what the provider's own output said it ran, read from
	// client-side metadata. It is never the child's answer to "what model are
	// you", because a model reporting its own identity is either reading
	// something its harness injected or guessing, and the two are
	// indistinguishable from outside.
	ReportedModel string `json:"reported_model,omitempty"`
	ModelVerified bool   `json:"model_verified,omitempty"`

	DurationSeconds float64     `json:"duration_seconds"`
	Result          string      `json:"result,omitempty"`
	TerminalReason  string      `json:"terminal_reason,omitempty"`
	CostUSD         float64     `json:"cost_usd,omitempty"`
	Tokens          *TokenUsage `json:"tokens,omitempty"`

	Error string `json:"error,omitempty"`
	// StderrTail is a bounded excerpt, present for diagnosis only.
	StderrTail string `json:"stderr_tail,omitempty"`
	// Repair is the instruction that turns a failure into a fix: which spec to
	// re-learn, which version drifted. Drift becomes an instruction rather than
	// a mystery.
	Repair   string   `json:"repair,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// DispatchEvent is one line of the status stream. The fields form a tagged union
// keyed by Type; a single struct keeps both emitting and parsing trivial.
type DispatchEvent struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	Time string `json:"time"`

	// batch_start
	TaskCount     int    `json:"task_count,omitempty"`
	MaxConcurrent int    `json:"max_concurrent,omitempty"`
	Deadline      string `json:"deadline,omitempty"`

	// task_start
	Task       string   `json:"task,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Model      string   `json:"model,omitempty"`
	SpecOrigin string   `json:"spec_origin,omitempty"`
	Argv       []string `json:"argv,omitempty"`
	PID        int      `json:"pid,omitempty"`

	// status
	ElapsedSeconds float64  `json:"elapsed_seconds,omitempty"`
	Running        []string `json:"running,omitempty"`
	Pending        int      `json:"pending,omitempty"`
	Completed      int      `json:"completed,omitempty"`
	Failed         int      `json:"failed,omitempty"`

	// task_done
	Result *TaskResult `json:"result,omitempty"`

	// batch_done
	State    string       `json:"state,omitempty"`
	Results  []TaskResult `json:"results,omitempty"`
	CostUSD  float64      `json:"cost_usd,omitempty"`
	Warnings []string     `json:"warnings,omitempty"`
}

// EventEmitter writes the JSON Lines status stream. One object per line, flushed
// per line: a single JSON document could not be read until it was closed, which
// would defeat watching a batch in flight. The stream is append-only -- a line is
// never rewritten or retracted, because harness capture of a backgrounded process
// is append-only text.
type EventEmitter struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// NewEventEmitter returns an emitter writing to w. Pass nil for now to use the
// wall clock; tests pass a fixed clock.
func NewEventEmitter(w io.Writer, now func() time.Time) *EventEmitter {
	if now == nil {
		now = time.Now
	}
	return &EventEmitter{w: w, now: now}
}

// Emit stamps and writes one event. Errors are returned rather than logged so the
// caller decides, but a broken status stream never aborts a running batch.
func (e *EventEmitter) Emit(event DispatchEvent) error {
	if e == nil || e.w == nil {
		return nil
	}
	event.V = DispatchEventVersion
	if event.Time == "" {
		event.Time = e.now().UTC().Format(time.RFC3339Nano)
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write(append(line, '\n')); err != nil {
		return err
	}
	if flusher, ok := e.w.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

// EmitWithin writes an event but gives up after d, reporting an error instead of
// waiting forever.
//
// This exists for the emits the supervisor makes on its own goroutine, batch_start
// and batch_done. A task goroutine that blocks in Emit is survivable, because the
// supervisor stops waiting for it -- but the supervisor blocking on its own final
// line means RunBatch never returns at all, and no amount of deadline elsewhere
// helps, because a deadline cannot interrupt a blocking io.Writer.Write.
//
// The abandoned write is left running: it holds the emitter's mutex, so every later
// Emit fails the same way, which is the correct outcome for a stream whose consumer
// has stopped reading. Losing status lines is a far smaller failure than a batch
// that never exits.
func (e *EventEmitter) EmitWithin(event DispatchEvent, d time.Duration) error {
	if e == nil || e.w == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- e.Emit(event) }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		return fmt.Errorf("gave up writing a %s event after %s: the status stream consumer is not reading",
			event.Type, d)
	}
}

// ParseDispatchEvents reads a JSON Lines stream, rejecting a line whose version
// this build does not understand. Watching code should fail loudly on drift.
func ParseDispatchEvents(r io.Reader) ([]DispatchEvent, error) {
	decoder := json.NewDecoder(r)
	var events []DispatchEvent
	for {
		var event DispatchEvent
		if err := decoder.Decode(&event); err == io.EOF {
			return events, nil
		} else if err != nil {
			return events, err
		}
		if event.V != DispatchEventVersion {
			return events, fmt.Errorf("dispatch event version %d is not %d: this stream was written by a different engram",
				event.V, DispatchEventVersion)
		}
		events = append(events, event)
	}
}
