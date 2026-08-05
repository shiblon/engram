package engram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fake provider is this test binary re-invoked with an env marker, which keeps
// the tests hermetic: no provider CLI is installed, nothing reaches a network, and
// the real spawn path (process group, explicit stdin, deadline, teardown) is still
// the code under test.

const fakeProviderEnv = "ENGRAM_FAKE_PROVIDER"

// TestFakeProviderProcess is not a test. It is the fake provider CLI, and it exits
// early unless the marker env var is set.
func TestFakeProviderProcess(t *testing.T) {
	mode := os.Getenv(fakeProviderEnv)
	if mode == "" {
		return
	}
	// --version answers regardless of mode, the way a real CLI does: the version
	// probe has to succeed even when the run itself is set up to fail.
	if containsString(os.Args, "--version") {
		fmt.Println("fake-cli 9.9.9")
		os.Exit(0)
	}

	// Mirror back what the caller passed, so a test can assert on argv and stdin.
	prompt, _ := readAllStdin()
	model := flagValue(os.Args, "--model")

	switch mode {
	case "ok":
		emitFakeJSON(prompt, model, false)
	case "provider-error":
		// Exits zero while reporting failure in its own structured output, the
		// way a real CLI does after refusing a task.
		emitFakeJSON(prompt, model, true)
		os.Exit(0)
	case "usage-error":
		fmt.Fprintln(os.Stderr, "error: unexpected argument '--model' found")
		os.Exit(2)
	case "run-failure":
		fmt.Fprintln(os.Stderr, "error: the work failed")
		os.Exit(1)
	case "hang":
		// Sleep past any test deadline; the point is that teardown reaches it.
		time.Sleep(60 * time.Second)
	}
	os.Exit(0)
}

func emitFakeJSON(prompt, model string, isError bool) {
	reported := model
	if reported == "" {
		reported = "fake-default-model"
	}
	payload := map[string]any{
		"result":          "echo:" + prompt,
		"is_error":        isError,
		"terminal_reason": "end_turn",
		"total_cost_usd":  0.0125,
		"modelUsage":      map[string]any{reported: map[string]any{"inputTokens": 3}},
		"usage":           map[string]any{"input_tokens": 5, "output_tokens": 2},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(body))
}

func readAllStdin() (string, error) {
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(os.Stdin); err != nil {
		return "", err
	}
	return strings.TrimSpace(buffer.String()), nil
}

func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// fakeSpec builds a spec that invokes this test binary in the given mode.
func fakeSpec(t *testing.T, mode string) *ProviderSpec {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &ProviderSpec{
		V:          DispatchSpecVersion,
		Provider:   "fake",
		Executable: executable,
		BaseArgv:   []string{"-test.run=TestFakeProviderProcess", "--"},
		Prompt:     PromptSpec{Transport: PromptTransportStdin},
		Model:      &ArgvFragment{Argv: []string{"--model", PlaceholderModel}},
		Env:        map[string]string{fakeProviderEnv: mode},
		Result: ResultSpec{
			Format:             ResultFormatJSON,
			JSONPath:           "result",
			ErrorPath:          "is_error",
			TerminalReasonPath: "terminal_reason",
			CostUSDPath:        "total_cost_usd",
			ModelPath:          "modelUsage",
		},
		Version:    &ArgvFragment{Argv: []string{"-test.run=TestFakeProviderProcess", "--", "--version"}},
		ExitCodes:  ExitCodes{UsageError: 2, RunFailure: 1},
		Provenance: Provenance{LearnedVersion: "9.9.9"},
	}
}

func fakeOptions(spec *ProviderSpec, out *bytes.Buffer) DispatchOptions {
	return DispatchOptions{
		Specs:            map[string]ResolvedSpec{"fake": {Spec: spec, Origin: SpecOriginMemory}},
		Emitter:          NewEventEmitter(out, nil),
		SkipVersionCheck: true,
		GraceSeconds:     1,
	}
}

func TestRunBatchHappyPath(t *testing.T) {
	var stream bytes.Buffer
	spec := fakeSpec(t, "ok")
	config := BatchConfig{
		V:             DispatchConfigVersion,
		MaxConcurrent: 2,
		Tasks: []TaskConfig{
			{ID: "slice-1", Prompt: "first slice", Provider: "fake", Model: "cheap"},
			{ID: "slice-2", Prompt: "second slice", Provider: "fake", Model: "cheap"},
			{ID: "whole", Prompt: "the whole change", Provider: "fake", Model: "strong"},
		},
	}
	outcome, err := RunBatch(context.Background(), config, fakeOptions(spec, &stream))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != BatchStateOK {
		t.Fatalf("batch state = %q, results %+v", outcome.State, outcome.Results)
	}
	if len(outcome.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(outcome.Results))
	}
	for _, result := range outcome.Results {
		if result.State != TaskStateOK {
			t.Errorf("task %s: state %q error %q", result.Task, result.State, result.Error)
		}
		if !strings.HasPrefix(result.Result, "echo:") {
			t.Errorf("task %s: result did not come from the provider's own output channel: %q", result.Task, result.Result)
		}
		if !result.ModelVerified {
			t.Errorf("task %s: model %q was not confirmed against the reported %q",
				result.Task, result.RequestedModel, result.ReportedModel)
		}
		if result.CostUSD == 0 {
			t.Errorf("task %s: provider-reported cost was dropped", result.Task)
		}
	}
	// Each child gets its own prompt: a shared prompt across slices would be a
	// decomposition that did not decompose.
	if outcome.Results[0].Result == outcome.Results[1].Result {
		t.Error("two slices produced identical results, so the prompts were not per-task")
	}
	if outcome.CostUSD == 0 {
		t.Error("batch cost was not accumulated")
	}

	events, err := ParseDispatchEvents(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, event := range events {
		seen[event.Type]++
		if event.V != DispatchEventVersion {
			t.Fatalf("event %s carried version %d", event.Type, event.V)
		}
	}
	if seen[EventBatchStart] != 1 || seen[EventBatchDone] != 1 {
		t.Fatalf("expected exactly one batch_start and one batch_done: %v", seen)
	}
	if seen[EventTaskStart] != 3 || seen[EventTaskDone] != 3 {
		t.Fatalf("expected three task_start and three task_done: %v", seen)
	}
	if events[0].Type != EventBatchStart {
		t.Fatalf("stream did not open with batch_start: %q", events[0].Type)
	}

	// batch_done must be authoritative and self-contained, so a caller that read
	// nothing until exit still receives the whole answer.
	last := events[len(events)-1]
	if last.Type != EventBatchDone {
		t.Fatalf("stream did not close with batch_done: %q", last.Type)
	}
	if len(last.Results) != 3 {
		t.Fatalf("batch_done carried %d results, not the whole answer", len(last.Results))
	}
	for _, result := range last.Results {
		if result.Result == "" {
			t.Errorf("batch_done result for %s was empty", result.Task)
		}
	}
}

func TestRunBatchReportsUsageErrorAsASpecProblem(t *testing.T) {
	// A usage error means the spec is wrong, not that the work failed. That
	// distinction is what lets a self-healing loop know whether to re-learn.
	var stream bytes.Buffer
	spec := fakeSpec(t, "usage-error")
	config := BatchConfig{
		V:     DispatchConfigVersion,
		Tasks: []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake"}},
	}
	outcome, err := RunBatch(context.Background(), config, fakeOptions(spec, &stream))
	if err != nil {
		t.Fatal(err)
	}
	result := outcome.Results[0]
	if result.State != TaskStateSpecError {
		t.Fatalf("state = %q, want %q", result.State, TaskStateSpecError)
	}
	if !strings.Contains(result.Repair, "dispatch survey") {
		t.Fatalf("a spec error must carry the repair instruction; got %q", result.Repair)
	}
	if outcome.State != BatchStateFailed {
		t.Fatalf("batch state = %q", outcome.State)
	}
}

func TestRunBatchDistinguishesRunFailureFromSpecError(t *testing.T) {
	var stream bytes.Buffer
	config := BatchConfig{
		V:     DispatchConfigVersion,
		Tasks: []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake"}},
	}
	outcome, err := RunBatch(context.Background(), config, fakeOptions(fakeSpec(t, "run-failure"), &stream))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcome.Results[0].State; got != TaskStateFailed {
		t.Fatalf("state = %q, want %q: exit 1 means the spec ran and the work failed", got, TaskStateFailed)
	}
}

func TestRunBatchBelievesAProviderThatExitsZeroAndReportsFailure(t *testing.T) {
	var stream bytes.Buffer
	config := BatchConfig{
		V:     DispatchConfigVersion,
		Tasks: []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake"}},
	}
	outcome, err := RunBatch(context.Background(), config, fakeOptions(fakeSpec(t, "provider-error"), &stream))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcome.Results[0].State; got != TaskStateFailed {
		t.Fatalf("state = %q: a zero exit with is_error true must not read as success", got)
	}
}

func TestRunBatchEnforcesAPerTaskDeadline(t *testing.T) {
	// An approval prompt in a process with no controlling terminal can block
	// indefinitely, so every child needs a wall-clock deadline.
	var stream bytes.Buffer
	config := BatchConfig{
		V:               DispatchConfigVersion,
		DeadlineSeconds: 30,
		Tasks:           []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake", DeadlineSeconds: 1}},
	}
	start := time.Now()
	outcome, err := RunBatch(context.Background(), config, fakeOptions(fakeSpec(t, "hang"), &stream))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcome.Results[0].State; got != TaskStateTimeout {
		t.Fatalf("state = %q, want %q", got, TaskStateTimeout)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("teardown took %v, so the deadline did not actually reach the child", elapsed)
	}
}

func TestRunBatchDetectsAModelThatWasNotHonored(t *testing.T) {
	// The failure this catches is silent: a misread model flag means every child
	// runs the default model while the output looks entirely plausible.
	var stream bytes.Buffer
	spec := fakeSpec(t, "ok")
	// The fake reports whatever --model carried; point the flag at something the
	// fake ignores, so the reported model is the default.
	spec.Model = &ArgvFragment{Argv: []string{"--profile", PlaceholderModel}}
	config := BatchConfig{
		V:     DispatchConfigVersion,
		Tasks: []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake", Model: "strong-model"}},
	}
	outcome, err := RunBatch(context.Background(), config, fakeOptions(spec, &stream))
	if err != nil {
		t.Fatal(err)
	}
	result := outcome.Results[0]
	if result.ModelVerified {
		t.Fatal("a child that ran the default model was reported as verified")
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "default model") {
		t.Fatalf("expected a warning that the requested model may not have run; got %v", result.Warnings)
	}
}

func TestRunBatchDryRunSpawnsNothing(t *testing.T) {
	var stream bytes.Buffer
	spec := fakeSpec(t, "usage-error") // would fail if it actually ran
	options := fakeOptions(spec, &stream)
	options.DryRun = true
	config := BatchConfig{
		V:     DispatchConfigVersion,
		Tasks: []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake", Model: "cheap"}},
	}
	outcome, err := RunBatch(context.Background(), config, options)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != BatchStateOK {
		t.Fatalf("dry run state = %q", outcome.State)
	}
	events, err := ParseDispatchEvents(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	for _, event := range events {
		if event.Type == EventTaskStart {
			argv = event.Argv
		}
	}
	// The whole point of a dry run is seeing the resolved argv for free.
	if !containsString(argv, "--model") || !containsString(argv, "cheap") {
		t.Fatalf("dry run did not report the resolved argv: %#v", argv)
	}
}

func TestRunBatchRespectsTheConcurrencyCap(t *testing.T) {
	var stream bytes.Buffer
	spec := fakeSpec(t, "ok")
	tasks := make([]TaskConfig, 6)
	for i := range tasks {
		tasks[i] = TaskConfig{ID: "t" + strconv.Itoa(i), Prompt: "p" + strconv.Itoa(i), Provider: "fake"}
	}
	config := BatchConfig{V: DispatchConfigVersion, MaxConcurrent: 2, Tasks: tasks}
	outcome, err := RunBatch(context.Background(), config, fakeOptions(spec, &stream))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != BatchStateOK {
		t.Fatalf("state = %q, results %+v", outcome.State, outcome.Results)
	}

	// Walk the stream and confirm no more than the cap were ever in flight.
	events, err := ParseDispatchEvents(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	inFlight, peak := 0, 0
	for _, event := range events {
		switch event.Type {
		case EventTaskStart:
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
		case EventTaskDone:
			inFlight--
		}
	}
	if peak > config.MaxConcurrent {
		t.Fatalf("peak in-flight tasks was %d, over the cap of %d", peak, config.MaxConcurrent)
	}
}

func TestRunBatchRefusesAProviderWithNoSpec(t *testing.T) {
	config := BatchConfig{
		V:     DispatchConfigVersion,
		Tasks: []TaskConfig{{ID: "t", Prompt: "p", Provider: "unlearned"}},
	}
	_, err := RunBatch(context.Background(), config, DispatchOptions{SkipVersionCheck: true})
	if err == nil {
		t.Fatal("expected a batch naming an unknown provider to be refused before it spends anything")
	}
	if !strings.Contains(err.Error(), "spec put") {
		t.Fatalf("the error should name the fix; got %v", err)
	}
}

func TestRunBatchAttachesVersionDriftToTheFirstFailure(t *testing.T) {
	// A version mismatch does not refuse the run, because most bumps do not touch
	// flags. It annotates, and the first actual failure carries the repair.
	var stream bytes.Buffer
	spec := fakeSpec(t, "usage-error")
	spec.Provenance.LearnedVersion = "1.0.0" // the fake reports 9.9.9
	options := fakeOptions(spec, &stream)
	options.SkipVersionCheck = false

	config := BatchConfig{
		V:     DispatchConfigVersion,
		Tasks: []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake"}},
	}
	outcome, err := RunBatch(context.Background(), config, options)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(outcome.Warnings, " ")
	if !strings.Contains(joined, "learned against") {
		t.Fatalf("expected a drift warning on the batch; got %q", joined)
	}
	if !strings.Contains(outcome.Results[0].Repair, "1.0.0") {
		t.Fatalf("drift should become an instruction on the failure; got %q", outcome.Results[0].Repair)
	}
}

func TestParseBatchConfig(t *testing.T) {
	t.Run("folds defaults into every task", func(t *testing.T) {
		config, err := ParseBatchConfig([]byte(`{
			"v": 1,
			"defaults": {"provider": "claude", "model": "haiku", "authority": "read-only"},
			"tasks": [{"id": "a", "prompt": "one"}, {"id": "b", "prompt": "two", "model": "opus"}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		if config.Tasks[0].Provider != "claude" || config.Tasks[0].Model != "haiku" {
			t.Fatalf("defaults not applied: %+v", config.Tasks[0])
		}
		if config.Tasks[1].Model != "opus" {
			t.Fatalf("a task's own model must win over the default: %+v", config.Tasks[1])
		}
		if config.Tasks[0].Authority != AuthorityReadOnly {
			t.Fatalf("authority default not applied: %+v", config.Tasks[0])
		}
		if config.MaxConcurrent != DefaultDispatchConcurrency || config.DeadlineSeconds != DefaultDispatchDeadlineSeconds {
			t.Fatalf("standing limits not applied: %+v", config)
		}
	})

	t.Run("reads a prompt from a file", func(t *testing.T) {
		path := t.TempDir() + "/slice.txt"
		if err := os.WriteFile(path, []byte("a very large diff slice"), 0o600); err != nil {
			t.Fatal(err)
		}
		config, err := ParseBatchConfig([]byte(fmt.Sprintf(
			`{"v":1,"defaults":{"provider":"claude"},"tasks":[{"id":"a","prompt_file":%q}]}`, path)))
		if err != nil {
			t.Fatal(err)
		}
		if config.Tasks[0].Prompt != "a very large diff slice" {
			t.Fatalf("prompt_file was not read: %q", config.Tasks[0].Prompt)
		}
	})

	t.Run("rejects configs that cannot run", func(t *testing.T) {
		cases := map[string]string{
			"unsupported version": `{"v":99,"tasks":[{"id":"a","prompt":"p","provider":"claude"}]}`,
			"no tasks":            `{"v":1,"tasks":[]}`,
			"duplicate id":        `{"v":1,"tasks":[{"id":"a","prompt":"p","provider":"c"},{"id":"a","prompt":"q","provider":"c"}]}`,
			"no provider":         `{"v":1,"tasks":[{"id":"a","prompt":"p"}]}`,
			"no prompt":           `{"v":1,"tasks":[{"id":"a","provider":"c"}]}`,
			"empty prompt":        `{"v":1,"tasks":[{"id":"a","prompt":"   ","provider":"c"}]}`,
			"both prompt forms":   `{"v":1,"tasks":[{"id":"a","prompt":"p","prompt_file":"f","provider":"c"}]}`,
			"unknown field":       `{"v":1,"tasks":[{"id":"a","prompt":"p","provider":"c","modle":"x"}]}`,
		}
		for name, body := range cases {
			if _, err := ParseBatchConfig([]byte(body)); err == nil {
				t.Errorf("%s: expected an error", name)
			}
		}
	})
}

func TestEventEmitterWritesOneFlushedLinePerEvent(t *testing.T) {
	var out bytes.Buffer
	fixed := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	emitter := NewEventEmitter(&out, func() time.Time { return fixed })
	for i := 0; i < 3; i++ {
		if err := emitter.Emit(DispatchEvent{Type: EventStatus, Completed: i}); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), out.String())
	}
	for _, line := range lines {
		var event DispatchEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line is not standalone JSON: %v", err)
		}
		if event.V != DispatchEventVersion || event.Time == "" {
			t.Fatalf("every line must carry v and a timestamp: %+v", event)
		}
	}
}

func TestParseDispatchEventsRejectsAForeignVersion(t *testing.T) {
	_, err := ParseDispatchEvents(strings.NewReader(`{"v":42,"type":"status"}` + "\n"))
	if err == nil {
		t.Fatal("a parser must fail loudly on schema drift rather than quietly misread")
	}
}

func TestNormalizeDefaultsAuthorityToReadOnly(t *testing.T) {
	// A config silent on authority must not inherit the provider's ambient default:
	// the child has no human attached, and the config should record what it could do.
	config, err := ParseBatchConfig([]byte(
		`{"v":1,"defaults":{"provider":"claude"},"tasks":[{"id":"a","prompt":"p"},{"id":"b","prompt":"p","authority":"edit"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Tasks[0].Authority != AuthorityReadOnly {
		t.Errorf("silent task got authority %q, want %q", config.Tasks[0].Authority, AuthorityReadOnly)
	}
	if config.Tasks[1].Authority != AuthorityEdit {
		t.Errorf("an explicit authority must survive: %q", config.Tasks[1].Authority)
	}
}

func TestSeedSpecsEnforceReadOnlyForASilentTask(t *testing.T) {
	// The end-to-end property that matters: a config that says nothing about
	// authority produces argv that actually constrains the child.
	seeds, err := SeedProviderSpecs()
	if err != nil {
		t.Fatal(err)
	}
	for provider, spec := range seeds {
		request := TaskConfig{ID: "t", Prompt: "p", Provider: provider, Authority: AuthorityReadOnly}.request()
		inv, err := spec.BuildInvocation(request, t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		joined := strings.Join(inv.Argv, " ")
		if !strings.Contains(joined, "read-only") && !strings.Contains(joined, "plan") {
			t.Errorf("%s: read-only produced no constraining flag: %s", provider, joined)
		}
		for _, warning := range inv.Warnings {
			if strings.Contains(warning, "authority") {
				t.Errorf("%s: read-only should be enforceable, but got %q", provider, warning)
			}
		}
	}
}

func TestWriteCapableAndUnenforceableAuthorityAreBothWarned(t *testing.T) {
	spec := minimalSpec()
	spec.Authority = &ArgvFragment{
		Argv:   []string{"--mode", PlaceholderAuthority},
		Values: map[string]string{AuthorityReadOnly: "ro", AuthorityEdit: ""},
	}

	// Write-capable: not an error, but it must be said out loud on the stream.
	inv, err := spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "p", Authority: AuthorityEdit}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(inv.Warnings, " | ")
	if !strings.Contains(joined, "no human can intervene") {
		t.Errorf("a write-capable child was not flagged: %q", joined)
	}
	// And a level the spec cannot express must not pass silently.
	if !strings.Contains(joined, "maps authority") {
		t.Errorf("an unenforceable authority level was absorbed: %q", joined)
	}

	// Read-only is the quiet path, since it is the expected posture.
	inv, err = spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "p", Authority: AuthorityReadOnly}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range inv.Warnings {
		if strings.Contains(warning, "authority") || strings.Contains(warning, "intervene") {
			t.Errorf("read-only should not warn: %q", warning)
		}
	}

	// The provider-default role is the one deliberate way to hand off to the CLI,
	// so it does not get the "maps to nothing" complaint -- but it is still not
	// read-only, so it is still flagged.
	spec.Authority.Values[AuthorityDefault] = ""
	inv, err = spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "p", Authority: AuthorityDefault}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(inv.Warnings, " | ")
	if strings.Contains(joined, "maps authority") {
		t.Errorf("the default role opts into the CLI default on purpose: %q", joined)
	}
	if !strings.Contains(joined, "not read-only") {
		t.Errorf("the default role is still not read-only and should say so: %q", joined)
	}
}

func TestModelVerificationComparesResolvedSpellingNotRoleName(t *testing.T) {
	// The bug this pins: a config saying model "cheap" resolved correctly to
	// claude-haiku-4-5-20251001, claude ran exactly that and reported it, and
	// verification still failed because it compared the ROLE NAME against the
	// reported id. Verification that cries wolf on correct portable configs trains
	// everyone to ignore it, which then conceals the real silent substitution it
	// exists to catch.
	var stream bytes.Buffer
	spec := fakeSpec(t, "ok")
	spec.Model = &ArgvFragment{
		Argv:   []string{"--model", PlaceholderModel},
		Values: map[string]string{"cheap": "fake-cheap-model-1"},
	}
	config := BatchConfig{
		V:     DispatchConfigVersion,
		Tasks: []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake", Model: "cheap"}},
	}
	outcome, err := RunBatch(context.Background(), config, fakeOptions(spec, &stream))
	if err != nil {
		t.Fatal(err)
	}
	result := outcome.Results[0]
	if result.ReportedModel != "fake-cheap-model-1" {
		t.Fatalf("the fake provider did not receive the resolved model: %q", result.ReportedModel)
	}
	if !result.ModelVerified {
		t.Fatalf("a correctly honored role name was reported unverified; warnings: %v", result.Warnings)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "default model") {
			t.Errorf("false alarm on a correct config: %q", warning)
		}
	}
}

func TestModelVerificationStillCatchesASubstitutionBehindARole(t *testing.T) {
	// The role indirection must not hide a real substitution: the warning has to
	// name both the resolved id and the role, so the chain is legible.
	var stream bytes.Buffer
	spec := fakeSpec(t, "ok")
	spec.Model = &ArgvFragment{
		Argv:   []string{"--profile", PlaceholderModel}, // the fake ignores --profile
		Values: map[string]string{"cheap": "fake-cheap-model-1"},
	}
	config := BatchConfig{
		V:     DispatchConfigVersion,
		Tasks: []TaskConfig{{ID: "t", Prompt: "p", Provider: "fake", Model: "cheap"}},
	}
	outcome, err := RunBatch(context.Background(), config, fakeOptions(spec, &stream))
	if err != nil {
		t.Fatal(err)
	}
	result := outcome.Results[0]
	if result.ModelVerified {
		t.Fatal("a child that ran the default model was reported as verified")
	}
	joined := strings.Join(result.Warnings, " | ")
	if !strings.Contains(joined, "fake-cheap-model-1") || !strings.Contains(joined, `role "cheap"`) {
		t.Errorf("the warning should show the resolution chain: %q", joined)
	}
}
