package engram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DispatchConfigVersion is the schema version of a batch config document.
const DispatchConfigVersion = 1

// Dispatch defaults. A batch is a batch, not a daemon: one invocation, N children,
// exit when the work is done. Every child gets a wall-clock deadline because an
// approval prompt in a process with no controlling terminal can block indefinitely.
const (
	DefaultDispatchConcurrency      = 4
	DefaultDispatchDeadlineSeconds  = 900
	DefaultDispatchHeartbeatSeconds = 15
	DefaultDispatchGraceSeconds     = 5
	// DefaultStderrTailBytes bounds the diagnostic excerpt kept per task, so one
	// chatty provider cannot make batch_done unreadable.
	DefaultStderrTailBytes = 4000
	// DefaultMaxChildOutputBytes caps how much of a child's stdout, stderr, or
	// result file is read into memory. Generous enough for a real review, small
	// enough that N children cannot exhaust the supervisor.
	DefaultMaxChildOutputBytes = 8 << 20
)

// TaskConfig is one slice of a fan-out, expressed in role-level terms. It names a
// provider and a model but never a flag, which is what lets the same config run
// against whatever specs this machine has learned.
type TaskConfig struct {
	ID               string  `json:"id"`
	Prompt           string  `json:"prompt,omitempty"`
	PromptFile       string  `json:"prompt_file,omitempty"`
	SystemPrompt     string  `json:"system_prompt,omitempty"`
	SystemPromptFile string  `json:"system_prompt_file,omitempty"`
	Provider         string  `json:"provider,omitempty"`
	Model            string  `json:"model,omitempty"`
	Authority        string  `json:"authority,omitempty"`
	BudgetUSD        float64 `json:"budget_usd,omitempty"`
	Workdir          string  `json:"workdir,omitempty"`
	SuppressContext  *bool   `json:"suppress_context,omitempty"`
	DeadlineSeconds  int     `json:"deadline_seconds,omitempty"`
}

// BatchConfig is the whole fan-out in one artifact. N tasks with per-task prompt,
// provider, model, and deadline is genuinely structured data, past the point where
// flags should carry it -- and a file is re-runnable, diffable, and reviewable
// before it spends tokens.
type BatchConfig struct {
	V                int          `json:"v"`
	MaxConcurrent    int          `json:"max_concurrent,omitempty"`
	DeadlineSeconds  int          `json:"deadline_seconds,omitempty"`
	HeartbeatSeconds int          `json:"heartbeat_seconds,omitempty"`
	Defaults         TaskConfig   `json:"defaults,omitempty"`
	Tasks            []TaskConfig `json:"tasks"`
}

// ParseBatchConfig decodes and validates a batch config.
func ParseBatchConfig(data []byte) (*BatchConfig, error) {
	var config BatchConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("parse batch config: %w", err)
	}
	if err := config.normalize(); err != nil {
		return nil, err
	}
	return &config, nil
}

// normalize folds defaults into every task, reads any prompt files, applies the
// standing limits, and rejects a config that cannot run.
func (c *BatchConfig) normalize() error {
	if c.V != DispatchConfigVersion {
		return fmt.Errorf("batch config: unsupported version %d (this engram understands %d)", c.V, DispatchConfigVersion)
	}
	if len(c.Tasks) == 0 {
		return fmt.Errorf("batch config: no tasks")
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = DefaultDispatchConcurrency
	}
	if c.DeadlineSeconds <= 0 {
		c.DeadlineSeconds = DefaultDispatchDeadlineSeconds
	}
	if c.HeartbeatSeconds <= 0 {
		c.HeartbeatSeconds = DefaultDispatchHeartbeatSeconds
	}

	seen := make(map[string]bool, len(c.Tasks))
	for i := range c.Tasks {
		task := &c.Tasks[i]
		if task.ID == "" {
			task.ID = fmt.Sprintf("task-%d", i+1)
		}
		if seen[task.ID] {
			return fmt.Errorf("batch config: duplicate task id %q", task.ID)
		}
		seen[task.ID] = true

		applyTaskDefaults(task, c.Defaults)

		if task.Prompt == "" && task.PromptFile == "" {
			return fmt.Errorf("batch config: task %q has neither prompt nor prompt_file", task.ID)
		}
		if task.Prompt != "" && task.PromptFile != "" {
			return fmt.Errorf("batch config: task %q sets both prompt and prompt_file", task.ID)
		}
		if task.PromptFile != "" {
			data, err := os.ReadFile(task.PromptFile)
			if err != nil {
				return fmt.Errorf("batch config: task %q prompt_file: %w", task.ID, err)
			}
			task.Prompt = string(data)
			task.PromptFile = ""
		}
		if task.SystemPrompt != "" && task.SystemPromptFile != "" {
			return fmt.Errorf("batch config: task %q sets both system_prompt and system_prompt_file", task.ID)
		}
		if task.SystemPromptFile != "" {
			data, err := os.ReadFile(task.SystemPromptFile)
			if err != nil {
				return fmt.Errorf("batch config: task %q system_prompt_file: %w", task.ID, err)
			}
			task.SystemPrompt = string(data)
			task.SystemPromptFile = ""
		}
		if strings.TrimSpace(task.Prompt) == "" {
			return fmt.Errorf("batch config: task %q has an empty prompt", task.ID)
		}
		if task.Provider == "" {
			return fmt.Errorf("batch config: task %q names no provider (set it on the task or in defaults)", task.ID)
		}
		if task.DeadlineSeconds <= 0 {
			task.DeadlineSeconds = c.DeadlineSeconds
		}
		// A child that says nothing about authority gets read-only, so writing
		// requires naming it. Inheriting the provider's ambient default would be
		// the wrong direction twice over: a dispatched child has no human attached
		// to answer an approval prompt, and the batch config should be the
		// auditable record of what each child was allowed to do rather than a
		// document that is silent on the question.
		if task.Authority == "" {
			task.Authority = AuthorityReadOnly
		}
		// Reject a provider-specific authority string here, at config parse, before
		// anything is spawned. This is the hole that let `danger-full-access` reach
		// codex's --sandbox straight from an unreviewed batch file.
		if !ValidAuthority(task.Authority) {
			return fmt.Errorf("batch config: task %q requests authority %q, which is not one of %s; "+
				"a provider-specific value must be mapped inside a reviewed spec, not named in a config",
				task.ID, task.Authority, strings.Join(CanonicalAuthorities(), ", "))
		}
	}
	return nil
}

// applyTaskDefaults fills a task's unset fields from the batch defaults. Prompt
// and id are deliberately never defaulted: a shared prompt across every slice
// would be a decomposition that did not decompose.
func applyTaskDefaults(task *TaskConfig, defaults TaskConfig) {
	if task.Provider == "" {
		task.Provider = defaults.Provider
	}
	if task.Model == "" {
		task.Model = defaults.Model
	}
	if task.Authority == "" {
		task.Authority = defaults.Authority
	}
	if task.BudgetUSD == 0 {
		task.BudgetUSD = defaults.BudgetUSD
	}
	if task.Workdir == "" {
		task.Workdir = defaults.Workdir
	}
	if task.SuppressContext == nil {
		task.SuppressContext = defaults.SuppressContext
	}
	if task.DeadlineSeconds == 0 {
		task.DeadlineSeconds = defaults.DeadlineSeconds
	}
	if task.SystemPrompt == "" && task.SystemPromptFile == "" {
		task.SystemPrompt = defaults.SystemPrompt
		task.SystemPromptFile = defaults.SystemPromptFile
	}
}

// request converts a normalized task config into a provider-neutral request.
func (t TaskConfig) request() TaskRequest {
	suppress := true // composing the child's context deliberately is the default
	if t.SuppressContext != nil {
		suppress = *t.SuppressContext
	}
	return TaskRequest{
		ID:              t.ID,
		Prompt:          t.Prompt,
		SystemPrompt:    t.SystemPrompt,
		Model:           t.Model,
		Authority:       t.Authority,
		BudgetUSD:       t.BudgetUSD,
		Workdir:         t.Workdir,
		SuppressContext: suppress,
	}
}

// DispatchOptions carries everything the supervisor needs that is not in the
// config: resolved specs, where the status stream goes, and the knobs tests move.
type DispatchOptions struct {
	// Specs maps provider name to a resolved spec and its origin. Every provider
	// named by a task must be present.
	Specs map[string]ResolvedSpec
	// Emitter receives the JSON Lines status stream. Nil discards it.
	Emitter *EventEmitter
	// TempDir holds prompt and last-message files. Empty allocates one.
	TempDir string
	// Now is the clock, overridable in tests.
	Now func() time.Time
	// GraceSeconds is how long a SIGTERM has before SIGKILL follows.
	GraceSeconds int
	// StderrTailBytes bounds the per-task diagnostic excerpt.
	StderrTailBytes int
	// MaxChildOutputBytes caps how much of a child's stdout, stderr, and result
	// file is read at all. Zero uses DefaultMaxChildOutputBytes.
	MaxChildOutputBytes int
	// SkipVersionCheck omits the one `--version` spawn per provider. Dispatch
	// already spawns processes, so the check is free here in a way it never
	// would be at session start.
	SkipVersionCheck bool
	// DryRun resolves every invocation and emits task_start for each without
	// spawning anything, which validates a whole batch for zero tokens.
	DryRun bool
}

// BatchOutcome is the supervisor's return value, identical in content to the
// batch_done event so a Go caller and a stream consumer see the same answer.
type BatchOutcome struct {
	State    string
	Results  []TaskResult
	CostUSD  float64
	Warnings []string
}

// RunBatch supervises one fan-out: N children, a global concurrency cap, a batch
// deadline, and a join, then exit. There is no run table and no mailbox, because
// the supervisor outlives every child and nothing survives the batch. A run dies
// with its session, which is a feature: detachment is already solved one layer
// down by tmux, and the failure mode being refused is a token-spending agent loop
// that outlives the session with no pane to observe it.
func RunBatch(ctx context.Context, config BatchConfig, opts DispatchOptions) (BatchOutcome, error) {
	if err := config.normalize(); err != nil {
		return BatchOutcome{}, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if opts.GraceSeconds <= 0 {
		opts.GraceSeconds = DefaultDispatchGraceSeconds
	}
	if opts.StderrTailBytes <= 0 {
		opts.StderrTailBytes = DefaultStderrTailBytes
	}
	if opts.MaxChildOutputBytes <= 0 {
		opts.MaxChildOutputBytes = DefaultMaxChildOutputBytes
	}
	emitDeadline := time.Duration(opts.GraceSeconds) * time.Second

	tempDir := opts.TempDir
	if tempDir == "" {
		created, err := os.MkdirTemp("", "engram-dispatch-")
		if err != nil {
			return BatchOutcome{}, fmt.Errorf("allocate dispatch temp dir: %w", err)
		}
		tempDir = created
		defer func() {
			if err := os.RemoveAll(created); err != nil {
				dispatchLogf("engram dispatch: remove temp dir %s: %v", created, err)
			}
		}()
	}

	// Confirm every provider has a spec before spending anything.
	warnings := make([]string, 0, 4)
	for _, task := range config.Tasks {
		if _, ok := opts.Specs[task.Provider]; !ok {
			return BatchOutcome{}, fmt.Errorf("task %q names provider %q, which has no invocation spec; "+
				"learn one with `engram dispatch spec put %s`", task.ID, task.Provider, task.Provider)
		}
	}

	// One --version per provider. A mismatch annotates rather than refuses,
	// because most bumps do not touch flags -- but the note is then attached to
	// the first actual failure, which turns drift into an instruction instead of
	// a mystery.
	drift := map[string]string{}
	if !opts.SkipVersionCheck && !opts.DryRun {
		for _, provider := range providersOf(config) {
			resolved := opts.Specs[provider]
			installed, err := probeVersion(ctx, resolved.Spec)
			switch {
			case err != nil:
				warnings = append(warnings, fmt.Sprintf("provider %s: could not read installed version: %v", provider, err))
			case resolved.Spec.Provenance.LearnedVersion == "":
				drift[provider] = fmt.Sprintf("spec for %s records no learned version; installed is %s. Re-learn with "+
					"`engram dispatch probe %s` so drift becomes detectable.", provider, installed, provider)
				warnings = append(warnings, drift[provider])
			case installed != resolved.Spec.Provenance.LearnedVersion:
				drift[provider] = fmt.Sprintf("spec for %s was learned against %s, installed is %s. If this failure is a "+
					"moved flag, re-learn with `engram dispatch survey %s` then `engram dispatch spec put %s`.",
					provider, resolved.Spec.Provenance.LearnedVersion, installed, provider, provider)
				warnings = append(warnings, drift[provider])
			}
			if resolved.Origin == SpecOriginSeed {
				warnings = append(warnings, fmt.Sprintf("provider %s is using the seed spec shipped with engram, "+
					"unverified on this machine; probe it with `engram dispatch probe %s`", provider, provider))
			}
		}
	}

	batchCtx, cancelBatch := context.WithTimeout(ctx, time.Duration(config.DeadlineSeconds)*time.Second)
	defer cancelBatch()

	start := now()
	if err := opts.Emitter.EmitWithin(DispatchEvent{
		Type:          EventBatchStart,
		TaskCount:     len(config.Tasks),
		MaxConcurrent: config.MaxConcurrent,
		Deadline:      start.Add(time.Duration(config.DeadlineSeconds) * time.Second).UTC().Format(time.RFC3339),
		Warnings:      warnings,
	}, emitDeadline); err != nil {
		dispatchLogf("engram dispatch: emit batch_start: %v", err)
	}

	pid := os.Getpid()
	project := ""
	if root, err := FindProjectRoot("."); err == nil {
		project = filepath.Base(root)
	}
	defer ClearDispatchProgress(pid)

	tracker := &progressTracker{pending: len(config.Tasks), running: map[string]bool{}}
	// Publish progress for a status line. Best-effort decoration: see
	// dispatch_progress.go for why this is a transient file rather than a table, and
	// why it must never fail a batch.
	publish := func() {
		running, _, completed, failed := tracker.snapshot()
		PublishDispatchProgress(DispatchProgress{
			PID: pid, StartedAt: start.UnixMilli(), UpdatedAt: now().UnixMilli(),
			Project: project, Total: len(config.Tasks),
			Running: len(running), Completed: completed, Failed: failed,
		})
	}
	publish()
	heartbeatDone := make(chan struct{})
	heartbeatStopped := make(chan struct{})
	go heartbeat(batchCtx, opts.Emitter, tracker, start, now,
		time.Duration(config.HeartbeatSeconds)*time.Second, heartbeatDone, heartbeatStopped, publish)

	// Results are collected under a mutex rather than by indexing a shared slice,
	// because the barrier that used to make indexing safe is no longer absolute:
	// the wait below can be abandoned at the batch deadline, and a task goroutine
	// may still be alive when that happens.
	collected := &resultSet{results: make(map[int]TaskResult, len(config.Tasks))}
	semaphore := make(chan struct{}, config.MaxConcurrent)
	var wg sync.WaitGroup
	for i, task := range config.Tasks {
		wg.Add(1)
		go func(index int, task TaskConfig) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-batchCtx.Done():
				result := TaskResult{
					Task:     task.ID,
					Provider: task.Provider,
					State:    TaskStateTimeout,
					Error:    "batch deadline expired before this task started",
				}
				collected.store(index, result)
				// neverStarted, not finish: this task never took a running slot,
				// so only the pending count should move.
				tracker.neverStarted(task.ID)
				emitTaskDone(opts.Emitter, result)
				return
			}
			result := runTask(batchCtx, task, opts.Specs[task.Provider], opts, tempDir, tracker, now)
			if result.State != TaskStateOK {
				if note, ok := drift[task.Provider]; ok {
					result.Repair = joinRepair(result.Repair, note)
				}
			}
			collected.store(index, result)
			publish()
			emitTaskDone(opts.Emitter, result)
		}(i, task)
	}

	// A batch must exit at its deadline, and waiting on the WaitGroup alone did not
	// guarantee that. Emit holds a mutex across a raw io.Writer.Write with no
	// deadline, and every task goroutine emits before its wg.Done() runs, so one
	// stalled stream consumer -- a pipe nobody drains, a full buffer -- blocked
	// wg.Wait() forever. Every other blocking point here has a deadline attached;
	// this one now does too. Abandoning the wait cannot unblock the stuck write, but
	// it stops the whole batch from being held hostage by it.
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()
	abandoned := false
	select {
	case <-waitDone:
	case <-batchCtx.Done():
		// Give in-flight children the grace period they would get anyway, then
		// proceed with whatever has been collected.
		select {
		case <-waitDone:
		case <-time.After(time.Duration(opts.GraceSeconds) * time.Second):
			abandoned = true
		}
	}

	// Join the heartbeat before batch_done so the terminal line is provably last.
	// Closing the channel and racing ahead let an in-flight status emission win the
	// writer, which would put a line after the event documented as authoritative and
	// break any consumer that stops reading there.
	close(heartbeatDone)
	select {
	case <-heartbeatStopped:
	case <-time.After(2 * time.Second):
		dispatchLogf("engram dispatch: heartbeat did not stop; a status line may follow batch_done")
	}

	results := collected.ordered(len(config.Tasks), config.Tasks)
	if abandoned {
		warnings = append(warnings, "the batch deadline expired while tasks were still reporting, so some results "+
			"below are incomplete; a stalled status-stream consumer is the usual cause")
	}
	outcome := BatchOutcome{Results: results, Warnings: warnings}
	failures := 0
	for _, result := range results {
		outcome.CostUSD += result.CostUSD
		if result.State != TaskStateOK {
			failures++
		}
	}
	switch {
	case failures == 0:
		outcome.State = BatchStateOK
	case failures == len(results):
		outcome.State = BatchStateFailed
	default:
		outcome.State = BatchStatePartial
	}

	// batch_done is authoritative and self-contained, so a caller that read
	// nothing until exit still receives the whole answer.
	if err := opts.Emitter.EmitWithin(DispatchEvent{
		Type:           EventBatchDone,
		State:          outcome.State,
		Results:        outcome.Results,
		CostUSD:        outcome.CostUSD,
		TaskCount:      len(results),
		Warnings:       outcome.Warnings,
		ElapsedSeconds: now().Sub(start).Seconds(),
	}, emitDeadline); err != nil {
		dispatchLogf("engram dispatch: emit batch_done: %v", err)
		outcome.Warnings = append(outcome.Warnings, "the final batch_done line could not be written: "+
			"the status stream consumer stopped reading, so this outcome exists only as a return value")
	}
	return outcome, nil
}

// runTask spawns and joins one child.
func runTask(ctx context.Context, task TaskConfig, resolved ResolvedSpec, opts DispatchOptions,
	tempDir string, tracker *progressTracker, now func() time.Time) TaskResult {

	spec := resolved.Spec
	result := TaskResult{
		Task:           task.ID,
		Provider:       task.Provider,
		RequestedModel: task.Model,
	}

	invocation, err := spec.BuildInvocation(task.request(), tempDir)
	if err != nil {
		result.State = TaskStateStartError
		result.Error = err.Error()
		result.Repair = fmt.Sprintf("the spec for %s could not produce an invocation; inspect it with "+
			"`engram dispatch spec show %s`", task.Provider, task.Provider)
		tracker.finish(task.ID, false)
		return result
	}
	result.Warnings = invocation.Warnings

	tracker.start(task.ID)
	if err := opts.Emitter.Emit(DispatchEvent{
		Type:       EventTaskStart,
		Task:       task.ID,
		Provider:   task.Provider,
		Model:      task.Model,
		SpecOrigin: string(resolved.Origin),
		// Redacted, because a provider that carries its prompt in argv would
		// otherwise publish the caller's content -- including a prompt supplied by
		// file specifically to keep it out of view -- into every captured stream.
		Argv: RedactArgv(invocation.Argv, invocation.Secrets),
	}); err != nil {
		dispatchLogf("engram dispatch: emit task_start: %v", err)
	}

	if opts.DryRun {
		result.State = TaskStateOK
		result.Result = "(dry run: nothing was spawned)"
		tracker.finish(task.ID, true)
		return result
	}

	taskCtx, cancelTask := context.WithTimeout(ctx, time.Duration(task.DeadlineSeconds)*time.Second)
	defer cancelTask()

	// Bound the capture, not just the excerpt kept afterwards. Unbounded buffers let
	// a runaway or malicious child write until the deadline and exhaust the
	// supervisor's memory, taking the whole batch with it.
	stdout := &boundedBuffer{limit: opts.MaxChildOutputBytes}
	stderr := &boundedBuffer{limit: opts.MaxChildOutputBytes}
	cmd := exec.CommandContext(taskCtx, invocation.Argv[0], invocation.Argv[1:]...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Dir = invocation.Dir
	if len(invocation.Env) > 0 {
		cmd.Env = append(os.Environ(), invocation.Env...)
	}
	// Set stdin explicitly, to the prompt or to an empty reader, and never
	// inherit it. codex reads stdin IN ADDITION to an argv prompt and appends it
	// as a <stdin> block, so a child inheriting an open-but-idle pipe blocks
	// there forever with no TTY to reveal why.
	if invocation.Stdin != nil {
		cmd.Stdin = bytes.NewReader(invocation.Stdin)
	} else {
		cmd.Stdin = strings.NewReader("")
	}
	configureProcessGroup(cmd)
	grace := time.Duration(opts.GraceSeconds) * time.Second
	// exec.CommandContext would kill only the direct child, orphaning the
	// provider's grandchildren; killing the group inverts that.
	// stopEscalation cancels the deferred SIGKILL once Wait has confirmed the child
	// is gone, so the timer can never fire at a recycled pid.
	var stopEscalation func()
	cmd.Cancel = func() error {
		stopEscalation = terminateProcessGroup(cmd, grace)
		return nil
	}
	cmd.WaitDelay = grace + time.Second

	started := now()
	if err := cmd.Start(); err != nil {
		result.State = TaskStateStartError
		result.Error = err.Error()
		result.DurationSeconds = now().Sub(started).Seconds()
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			result.Repair = fmt.Sprintf("executable %q from the %s spec was not found on PATH",
				spec.Executable, task.Provider)
		}
		tracker.finish(task.ID, false)
		return result
	}
	waitErr := cmd.Wait()
	if stopEscalation != nil {
		stopEscalation()
	}
	result.DurationSeconds = now().Sub(started).Seconds()
	result.ExitCode = cmd.ProcessState.ExitCode()
	result.StderrTail = tailString(stderr.String(), opts.StderrTailBytes)

	timedOut := errors.Is(taskCtx.Err(), context.DeadlineExceeded)

	extracted, extractErr := extractResult(spec, providerOutput{
		Stdout:       stdout.Bytes(),
		Stderr:       stderr.Bytes(),
		OutputFile:   invocation.OutputFile,
		MaxFileBytes: opts.MaxChildOutputBytes,
	})
	if stdout.Truncated() || stderr.Truncated() {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"this child produced more than the %d-byte capture limit, so its output was truncated",
			opts.MaxChildOutputBytes))
	}
	result.Result = extracted.Result
	result.TerminalReason = extracted.TerminalReason
	result.CostUSD = extracted.CostUSD
	result.ReportedModel = extracted.ReportedModel
	result.Tokens = extracted.Tokens
	result.Warnings = append(result.Warnings, extracted.Notes...)
	if task.Model != "" && extracted.ReportedModel != "" {
		// Compare the provider's own spelling, not the role name the config used.
		want := invocation.ResolvedModel
		if want == "" {
			want = task.Model
		}
		result.ModelVerified = modelMatches(want, extracted.ReportedModel)
		if !result.ModelVerified {
			asked := want
			if want != task.Model {
				asked = fmt.Sprintf("%s (role %q)", want, task.Model)
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"requested model %s but %s reported %q: this child may have run the default model",
				asked, task.Provider, extracted.ReportedModel))
		}
	}

	switch {
	case timedOut:
		result.State = TaskStateTimeout
		result.Error = fmt.Sprintf("wall-clock deadline of %ds expired; the process group was torn down", task.DeadlineSeconds)
	case isUsageError(spec, result.ExitCode):
		// A usage error means the spec is wrong, not that the work failed. That
		// distinction is the whole point of recording exit-code semantics.
		result.State = TaskStateSpecError
		result.Error = firstNonEmpty(result.StderrTail, waitErrorString(waitErr))
		result.Repair = fmt.Sprintf("exit %d is a usage error for %s, so the invocation spec is wrong rather than the "+
			"work: survey the CLI with `engram dispatch survey %s`, fix the argv, and write it back with "+
			"`engram dispatch spec put %s`", result.ExitCode, task.Provider, task.Provider, task.Provider)
	case waitErr != nil:
		result.State = TaskStateFailed
		result.Error = firstNonEmpty(waitErrorString(waitErr), result.StderrTail)
	case extractErr != nil:
		result.State = TaskStateFailed
		result.Error = fmt.Sprintf("could not read the result the spec promised: %v", extractErr)
		result.Repair = fmt.Sprintf("the %s spec's result section no longer matches the CLI's output; "+
			"check it with `engram dispatch spec show %s`", task.Provider, task.Provider)
	case extracted.ProviderError:
		// Exit code is a fallback, because these CLIs commonly exit zero after
		// refusing a task or exhausting a budget.
		result.State = TaskStateFailed
		result.Error = firstNonEmpty(extracted.TerminalReason, "provider reported an error in its own structured output")
	default:
		result.State = TaskStateOK
	}
	tracker.finish(task.ID, result.State == TaskStateOK)
	return result
}

func emitTaskDone(emitter *EventEmitter, result TaskResult) {
	copied := result
	if err := emitter.Emit(DispatchEvent{
		Type:   EventTaskDone,
		Task:   result.Task,
		Result: &copied,
	}); err != nil {
		dispatchLogf("engram dispatch: emit task_done: %v", err)
	}
}

// isUsageError reports whether an exit code means the spec was rejected. Zero is
// never a usage error, which is what makes an unset ExitCodes field unambiguous.
func isUsageError(spec *ProviderSpec, code int) bool {
	return spec.ExitCodes.UsageError != 0 && code == spec.ExitCodes.UsageError
}

func waitErrorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func joinRepair(existing, note string) string {
	if existing == "" {
		return note
	}
	return existing + " " + note
}

// progressTracker is the supervisor's whole notion of run status: in memory for
// the few minutes it exists, reported on the event stream, durable nowhere.
type progressTracker struct {
	mu        sync.Mutex
	running   map[string]bool
	pending   int
	completed int
	failed    int
}

func (t *progressTracker) start(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running[id] = true
	if t.pending > 0 {
		t.pending--
	}
}

// neverStarted records a task that was abandoned before it took a running slot.
// finish alone would leave it counted in pending forever while also folding it into
// completed, so pending + running + completed could exceed the task count in every
// subsequent status event.
func (t *progressTracker) neverStarted(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.running, id)
	if t.pending > 0 {
		t.pending--
	}
	t.completed++
	t.failed++
}

func (t *progressTracker) finish(id string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.running, id)
	t.completed++
	if !ok {
		t.failed++
	}
}

func (t *progressTracker) snapshot() (running []string, pending, completed, failed int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.running {
		running = append(running, id)
	}
	sort.Strings(running)
	return running, t.pending, t.completed, t.failed
}

// heartbeat emits status on an interval as well as on state change. Change-only
// would make a slow task indistinguishable from a hang, and heartbeat-only would
// report too late; the heartbeat is the only liveness signal a watching agent
// gets, so it is load-bearing rather than decorative.
func heartbeat(ctx context.Context, emitter *EventEmitter, tracker *progressTracker,
	start time.Time, now func() time.Time, interval time.Duration,
	done <-chan struct{}, stopped chan<- struct{}, publish func()) {

	// stopped lets RunBatch join this goroutine, so batch_done is provably the last
	// line rather than merely the last line requested.
	defer close(stopped)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			publish()
			running, pending, completed, failed := tracker.snapshot()
			if err := emitter.Emit(DispatchEvent{
				Type:           EventStatus,
				ElapsedSeconds: now().Sub(start).Seconds(),
				Running:        running,
				Pending:        pending,
				Completed:      completed,
				Failed:         failed,
			}); err != nil {
				dispatchLogf("engram dispatch: emit status: %v", err)
			}
		}
	}
}

func providersOf(config BatchConfig) []string {
	seen := map[string]bool{}
	var providers []string
	for _, task := range config.Tasks {
		if !seen[task.Provider] {
			seen[task.Provider] = true
			providers = append(providers, task.Provider)
		}
	}
	sort.Strings(providers)
	return providers
}

// tailString keeps the last n bytes, which is where a CLI's actual complaint is.
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...(truncated)...\n" + s[len(s)-n:]
}

// dispatchLogf routes best-effort diagnostics to the standard logger. A failed
// status-line write must not abort a running batch, but it must not vanish
// either: a silent discard would hide exactly the bug worth finding.
func dispatchLogf(format string, args ...any) {
	log.Printf(format, args...)
}

// resultSet collects task results without indexing a shared slice, so the wait for
// them can be abandoned at the batch deadline without racing a live goroutine.
type resultSet struct {
	mu      sync.Mutex
	results map[int]TaskResult
}

func (r *resultSet) store(index int, result TaskResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[index] = result
}

// ordered returns one result per task in config order, synthesizing an entry for
// any task that never reported. A missing result is itself a finding, so it is
// reported as such rather than appearing as a zero-valued success.
func (r *resultSet) ordered(count int, tasks []TaskConfig) []TaskResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TaskResult, 0, count)
	for i := 0; i < count; i++ {
		if result, ok := r.results[i]; ok {
			out = append(out, result)
			continue
		}
		out = append(out, TaskResult{
			Task:     tasks[i].ID,
			Provider: tasks[i].Provider,
			State:    TaskStateTimeout,
			Error:    "the batch stopped waiting before this task reported a result",
		})
	}
	return out
}

// boundedBuffer accumulates up to limit bytes and discards the rest, reporting that
// it did so. An io.Writer handed to exec must never grow without bound: the child
// on the other end may be runaway or hostile, and "we ran out of memory" is a much
// worse failure than "that child said too much".
type boundedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) <= room {
			b.buf.Write(p)
		} else {
			b.buf.Write(p[:room])
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	// Always report a full write: a short write would make exec treat this as an
	// error and kill the child, when discarding excess output is the intent.
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes()
}

func (b *boundedBuffer) String() string { return string(b.Bytes()) }

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
