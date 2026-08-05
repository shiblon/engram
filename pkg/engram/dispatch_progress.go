package engram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A running batch publishes a small progress file so a status line can show it.
//
// This deliberately does NOT become a table. Dispatch adds no database schema
// because the supervisor outlives every child and nothing survives the batch, and
// that decision holds here: this file's lifetime is exactly the supervisor's. Run it
// through the three questions in docs/design-notes.md -- would a row belong in
// `engram save`, take a place in the priority ladder, or ever be curated by a human?
// Three times no, so it is state rather than memory, and a transient file is weaker
// than a table, which is the right direction.
//
// One file per supervisor pid, for two reasons: concurrent batches do not collide,
// and a supervisor killed with SIGKILL leaves a file that a reader can detect as
// stale by checking whether the pid is still alive.
//
// The opposite failure rule from a provider spec applies here. A malformed spec must
// fail LOUDLY, because dispatch parses it at the point of action and a wrong
// invocation spends money. A malformed progress file must fail SILENTLY, because it
// is decoration and a broken status line is worse than an absent one. Nothing in
// this file may return an error that reaches a caller's exit code.

// DispatchProgress is one running batch's public progress, rewritten on every state
// change and heartbeat.
type DispatchProgress struct {
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
	Project   string `json:"project,omitempty"`
	Total     int    `json:"total"`
	Running   int    `json:"running"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
}

// progressDir is where running batches publish. XDG_RUNTIME_DIR is preferred
// because it is cleared on logout, which suits state that should not outlive a
// session; the temp dir is the portable fallback.
func progressDir() string {
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		return filepath.Join(runtime, "engram", "dispatch")
	}
	return filepath.Join(os.TempDir(), "engram-dispatch-progress")
}

// PublishDispatchProgress writes one batch's progress, best-effort. Errors are
// swallowed on purpose: failing a batch because a decorative file could not be
// written would be an absurd trade.
func PublishDispatchProgress(p DispatchProgress) {
	dir := progressDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	path := filepath.Join(dir, strconv.Itoa(p.PID)+".json")
	// Write then rename, so a reader never sees a half-written file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		if err := os.Remove(tmp); err != nil {
			dispatchLogf("engram dispatch: remove temp progress file: %v", err)
		}
	}
}

// ClearDispatchProgress removes a batch's progress file when it exits.
func ClearDispatchProgress(pid int) {
	if err := os.Remove(filepath.Join(progressDir(), strconv.Itoa(pid)+".json")); err != nil && !os.IsNotExist(err) {
		dispatchLogf("engram dispatch: remove progress file: %v", err)
	}
}

// ReadDispatchProgress returns the progress of every batch that is still running,
// oldest first, and cleans up files whose supervisor is gone.
//
// A stale file is the expected residue of a SIGKILLed supervisor, which cannot run
// its own cleanup by definition. Detecting it by liveness rather than by age is why
// the files are keyed by pid.
func ReadDispatchProgress() []DispatchProgress {
	entries, err := os.ReadDir(progressDir())
	if err != nil {
		return nil
	}
	var live []DispatchProgress
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(progressDir(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p DispatchProgress
		if err := json.Unmarshal(data, &p); err != nil {
			continue // decoration: a malformed file is skipped, never surfaced
		}
		if p.PID <= 0 || !processAlive(p.PID) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				dispatchLogf("engram dispatch: remove stale progress file %s: %v", path, err)
			}
			continue
		}
		live = append(live, p)
	}
	sort.Slice(live, func(i, j int) bool { return live[i].StartedAt < live[j].StartedAt })
	return live
}

// FormatDispatchProgress renders running batches as a status-line segment, or "" when
// nothing is running. A permanent "0/0" would be noise, so absence is the default.
func FormatDispatchProgress(batches []DispatchProgress, now time.Time) string {
	if len(batches) == 0 {
		return ""
	}
	segments := make([]string, 0, len(batches))
	for _, b := range batches {
		elapsed := now.Sub(time.UnixMilli(b.StartedAt))
		segment := fmt.Sprintf("⚡ %d/%d", b.Completed, b.Total)
		if b.Failed > 0 {
			segment += fmt.Sprintf(" %d✗", b.Failed)
		}
		segment += " " + compactDuration(elapsed)
		segments = append(segments, segment)
	}
	return strings.Join(segments, " · ")
}

// compactDuration renders an elapsed time in the least space that stays readable.
func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
