package engram

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Learning must probe, not believe. Reading help and trusting the inference has an
// expensive silent failure: a misread model flag means every child in a fan-out
// quietly runs the default model, and the output looks entirely plausible.

// DefaultProbePrompt is deliberately trivial. The probe is checking the
// invocation, not the model.
const DefaultProbePrompt = "Reply with the single word: ok"

// DefaultProbeTimeoutSeconds bounds a probe that would otherwise wait on an
// approval prompt in a process with no controlling terminal.
const DefaultProbeTimeoutSeconds = 120

// DefaultInvalidProbeModel is the deliberately nonexistent model used for the
// flag-liveness fallback.
const DefaultInvalidProbeModel = "engram-nonexistent-model-probe"

// ProbeOptions configures a probe pass.
type ProbeOptions struct {
	// Model is the model to ask for and then confirm. Required for model
	// verification; an empty model runs the smoke probe only.
	Model string
	// Prompt overrides the trivial probe prompt.
	Prompt string
	// TimeoutSeconds bounds each spawn.
	TimeoutSeconds int
	// SuppressContext runs the probe with the provider's context discovery off.
	// It defaults true and matters more than it sounds: without it the probe
	// costs a quarter in context loading rather than a fraction of a cent.
	SuppressContext *bool
	// Workdir is where the probe runs.
	Workdir string
	// SkipFlagLiveness omits the invalid-model fallback, which is free on some
	// providers and reaches the API on others.
	SkipFlagLiveness bool
	// Now is the clock, overridable in tests.
	Now func() time.Time
}

// ProbeResult is what a probe established about a spec on this machine.
type ProbeResult struct {
	Provider string
	Version  string
	// SmokeOK means the spec produced a clean exit and a readable result.
	SmokeOK  bool
	ExitCode int
	Result   string
	CostUSD  float64
	// RequestedModel and ReportedModel come from the run and the provider's own
	// output metadata respectively. ReportedModel is never the child's answer to
	// "what model are you": a model reporting its own identity is either reading
	// something its harness injected or guessing, and the two are
	// indistinguishable from outside.
	RequestedModel string
	ReportedModel  string
	ModelVerified  bool
	// FlagLiveness records what an invalid model name did, for a provider that
	// will not report its effective configuration. A provider that silently falls
	// back instead of erroring is itself the finding.
	FlagLiveness string
	Notes        []string
	Stderr       string
}

// ProbeSpec runs the two-phase probe: a smoke run, then positive model
// verification, with the flag-liveness probe as a fallback when the provider
// reports nothing about its effective configuration. The second phase is not
// optional, because a positive check is strictly better than inferring from a
// failure.
func ProbeSpec(ctx context.Context, spec *ProviderSpec, opts ProbeOptions) (ProbeResult, error) {
	if err := spec.Validate(); err != nil {
		return ProbeResult{}, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if opts.TimeoutSeconds <= 0 {
		opts.TimeoutSeconds = DefaultProbeTimeoutSeconds
	}
	if opts.Prompt == "" {
		opts.Prompt = DefaultProbePrompt
	}
	suppress := true
	if opts.SuppressContext != nil {
		suppress = *opts.SuppressContext
	}

	result := ProbeResult{Provider: spec.Provider, RequestedModel: opts.Model}

	if version, err := probeVersion(ctx, spec); err != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("could not read installed version: %v", err))
	} else {
		result.Version = version
	}
	if spec.SuppressContext == nil && suppress {
		result.Notes = append(result.Notes, "this spec declares no context-suppression flag, so the probe pays "+
			"full context load on spawn; that is dollars rather than cents, and it is the same cost every "+
			"dispatched child will pay")
	}

	tempDir, err := os.MkdirTemp("", "engram-probe-")
	if err != nil {
		return result, fmt.Errorf("allocate probe temp dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			dispatchLogf("engram dispatch probe: remove temp dir %s: %v", tempDir, err)
		}
	}()

	request := TaskRequest{
		ID:              "probe",
		Prompt:          opts.Prompt,
		Model:           opts.Model,
		Authority:       AuthorityReadOnly,
		Workdir:         opts.Workdir,
		SuppressContext: suppress,
	}
	run, err := runProbeOnce(ctx, spec, request, tempDir, opts.TimeoutSeconds)
	if err != nil {
		return result, err
	}
	result.ExitCode = run.exitCode
	result.Stderr = tailString(run.stderr, DefaultStderrTailBytes)

	extracted, extractErr := extractResult(spec, providerOutput{
		Stdout:     []byte(run.stdout),
		Stderr:     []byte(run.stderr),
		OutputFile: run.outputFile,
	})
	if extractErr != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("could not read the result the spec promised: %v", extractErr))
	}
	result.Result = extracted.Result
	result.CostUSD = extracted.CostUSD
	result.ReportedModel = extracted.ReportedModel
	result.Notes = append(result.Notes, extracted.Notes...)
	result.SmokeOK = run.exitCode == 0 && extractErr == nil && !extracted.ProviderError && result.Result != ""
	if isUsageError(spec, run.exitCode) {
		result.Notes = append(result.Notes, fmt.Sprintf("exit %d is a usage error for this provider, so the spec "+
			"itself was rejected: fix the argv rather than retrying", run.exitCode))
	}
	if !result.SmokeOK && suppress && spec.SuppressContext != nil {
		if hint := authSuppressionConflict(run.stdout + "\n" + run.stderr + "\n" + result.Result); hint != "" {
			result.Notes = append(result.Notes, hint)
		}
	}

	if opts.Model == "" {
		result.Notes = append(result.Notes, "no model was requested, so nothing was verified about model selection; "+
			"a misread model flag is silent, so probe with an explicit --model before trusting the spec")
		return result, nil
	}

	switch {
	case result.ReportedModel != "":
		want := run.resolvedModel
		if want == "" {
			want = opts.Model
		}
		result.ModelVerified = modelMatches(want, result.ReportedModel)
		if !result.ModelVerified {
			asked := fmt.Sprintf("%q", want)
			if want != opts.Model {
				asked = fmt.Sprintf("%q (role %q)", want, opts.Model)
			}
			result.Notes = append(result.Notes, fmt.Sprintf("asked for %s but the provider reported %q: "+
				"treat the model field as inferred and untrusted", asked, result.ReportedModel))
		}
	case opts.SkipFlagLiveness:
		result.Notes = append(result.Notes, "the provider reported no effective model and the flag-liveness probe "+
			"was skipped, so the model field stays inferred and untrusted")
	case !result.SmokeOK:
		// The liveness probe asks "does an invalid model name get rejected?" and
		// reads a nonzero exit as yes. That inference is only sound when the
		// VALID model exited cleanly. If the baseline already failed, a second
		// failure is the same failure, and reading it as evidence about the model
		// flag is how a broken login gets recorded as "the flag is live".
		result.FlagLiveness = "inconclusive-baseline-failed"
		result.Notes = append(result.Notes, "the smoke probe itself failed, so the flag-liveness check was skipped: "+
			"a second failing run would prove nothing about the model flag. Fix the smoke failure and probe again")
	default:
		// Fallback: pass a deliberately invalid model and confirm it errors. If a
		// provider silently falls back instead, that is the finding.
		liveness, err := runProbeOnce(ctx, spec, TaskRequest{
			ID:              "probe-liveness",
			Prompt:          opts.Prompt,
			Model:           DefaultInvalidProbeModel,
			Authority:       AuthorityReadOnly,
			Workdir:         opts.Workdir,
			SuppressContext: suppress,
		}, tempDir, opts.TimeoutSeconds)
		switch {
		case err != nil:
			result.FlagLiveness = "error"
			result.Notes = append(result.Notes, fmt.Sprintf("flag-liveness probe could not run: %v", err))
		case liveness.exitCode != 0:
			result.FlagLiveness = "rejected-invalid-model"
			result.Notes = append(result.Notes, fmt.Sprintf("an invalid model name was rejected (exit %d), so the "+
				"model flag is live -- though this confirms the flag is read, not that the requested model ran",
				liveness.exitCode))
		default:
			result.FlagLiveness = "silently-accepted-invalid-model"
			result.Notes = append(result.Notes, "an invalid model name was accepted with a clean exit: this provider "+
				"silently falls back, so the model field is inferred and untrusted and a fan-out may be running "+
				"the default model throughout")
		}
	}
	return result, nil
}

// authComplaintPattern matches the ways a CLI says it has no usable credentials.
// Deliberately narrow: this drives a diagnostic note, never a control decision, so
// a false negative costs a hint while a false positive would mislead.
var authComplaintPattern = regexp.MustCompile(`(?i)(not logged in|please run /login|no api key|unauthorized|authentication (failed|required)|invalid api key)`)

// authSuppressionConflict recognizes the specific trap where a context-suppression
// flag also disables the credential path, so the run dies on authentication rather
// than on anything to do with the spec's argv.
//
// This is not hypothetical. claude's --bare documents that "Anthropic auth is
// strictly ANTHROPIC_API_KEY or apiKeyHelper via --settings (OAuth and keychain are
// never read)", so an OAuth-authenticated user gets "Not logged in" from the very
// flag that was supposed to make dispatch cheap. Reporting that as a bare smoke
// failure sends someone hunting through their argv for a problem that is not there.
func authSuppressionConflict(output string) string {
	if !authComplaintPattern.MatchString(output) {
		return ""
	}
	return "the provider reports missing credentials, and this probe ran WITH context suppression: " +
		"a suppression flag may also be disabling the credential path it would otherwise read. " +
		"Re-probe with --keep-context to tell the two apart. If that succeeds, suppression and this " +
		"machine's auth mode are in conflict, so either supply the credentials the suppressed mode " +
		"accepts, or narrow the spec's suppress_context to flags that leave authentication alone -- " +
		"and note that every child then pays full context load"
}

// probeRun is one spawn's raw outcome.
type probeRun struct {
	exitCode      int
	stdout        string
	stderr        string
	outputFile    string
	argv          []string
	resolvedModel string
}

// runProbeOnce spawns a child under the same process hygiene a dispatched task
// gets, so a probe exercises the real path rather than a simplified one.
func runProbeOnce(ctx context.Context, spec *ProviderSpec, request TaskRequest,
	tempDir string, timeoutSeconds int) (probeRun, error) {

	invocation, err := spec.BuildInvocation(request, tempDir)
	if err != nil {
		return probeRun{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(runCtx, invocation.Argv[0], invocation.Argv[1:]...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Dir = invocation.Dir
	if len(invocation.Env) > 0 {
		cmd.Env = append(os.Environ(), invocation.Env...)
	}
	if invocation.Stdin != nil {
		cmd.Stdin = bytes.NewReader(invocation.Stdin)
	} else {
		cmd.Stdin = strings.NewReader("")
	}
	configureProcessGroup(cmd)
	grace := time.Duration(DefaultDispatchGraceSeconds) * time.Second
	var stopEscalation func()
	cmd.Cancel = func() error {
		stopEscalation = terminateProcessGroup(cmd, grace)
		return nil
	}
	cmd.WaitDelay = grace + time.Second

	if err := cmd.Start(); err != nil {
		return probeRun{argv: invocation.Argv, resolvedModel: invocation.ResolvedModel},
			fmt.Errorf("start %s: %w", invocation.Argv[0], err)
	}
	// A nonzero exit is data here, not an error: the flag-liveness probe is
	// specifically looking for one. Only a failure to spawn is an error, and
	// Start already reported that.
	waitErr := cmd.Wait()
	if stopEscalation != nil {
		stopEscalation()
	}
	if waitErr != nil {
		dispatchLogf("engram dispatch probe: %s exited with %v", spec.Provider, waitErr)
	}
	return probeRun{
		exitCode:      cmd.ProcessState.ExitCode(),
		stdout:        stdout.String(),
		stderr:        stderr.String(),
		outputFile:    invocation.OutputFile,
		argv:          invocation.Argv,
		resolvedModel: invocation.ResolvedModel,
	}, nil
}

// probeVersion asks the provider what version it is. Staleness checks belong at
// dispatch time rather than inject time: spawning every provider CLI at session
// start is not the light bookkeeping inject is limited to, and inject has to stay
// fast. Dispatch already spawns processes, so one --version is free there.
func probeVersion(ctx context.Context, spec *ProviderSpec) (string, error) {
	if spec.Version == nil || len(spec.Version.Argv) == 0 {
		return "", fmt.Errorf("spec declares no version argv")
	}
	versionCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	argv := append([]string{spec.Executable}, spec.Version.Argv...)
	cmd := exec.CommandContext(versionCtx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader("")
	configureProcessGroup(cmd)
	cmd.Cancel = func() error {
		terminateProcessGroup(cmd, time.Second)
		return nil
	}
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return normalizeVersion(string(output)), nil
}

var versionNumberPattern = regexp.MustCompile(`\d+\.\d+(\.\d+)?(-[0-9A-Za-z.]+)?`)

// normalizeVersion pulls the version number out of whatever a CLI prints, so a
// comparison is not defeated by a differing product-name prefix.
func normalizeVersion(output string) string {
	trimmed := strings.TrimSpace(output)
	if match := versionNumberPattern.FindString(trimmed); match != "" {
		return match
	}
	return firstLine(trimmed)
}

// ApplyProbe records a probe's findings in a spec's provenance, moving fields it
// positively verified out of the inferred list. Called on a clone so a concurrent
// reader never sees a half-updated spec.
func ApplyProbe(spec *ProviderSpec, probe ProbeResult, now time.Time) *ProviderSpec {
	updated := spec.Clone()
	if updated == nil {
		return spec
	}
	if probe.Version != "" {
		updated.Provenance.LearnedVersion = probe.Version
	}
	// Only a probe that actually ran may retire the seed flag. A failed probe
	// proved nothing, so clearing it would destroy the one signal that says "this
	// spec is a shipped guess", and would do so precisely when the guess is most
	// suspect.
	if probe.SmokeOK {
		updated.Provenance.Seed = false
	}
	updated.Provenance.Probe = &ProbeRecord{
		At:             now.UTC().Format(time.RFC3339),
		Version:        probe.Version,
		ExitCode:       probe.ExitCode,
		RequestedModel: probe.RequestedModel,
		ReportedModel:  probe.ReportedModel,
		ModelVerified:  probe.ModelVerified,
		CostUSD:        probe.CostUSD,
		FlagLiveness:   probe.FlagLiveness,
	}
	if probe.SmokeOK {
		updated.Provenance.VerifiedFields = addField(updated.Provenance.VerifiedFields, "prompt", "result")
		updated.Provenance.InferredFields = removeField(updated.Provenance.InferredFields, "prompt", "result")
	}
	if probe.ModelVerified {
		updated.Provenance.VerifiedFields = addField(updated.Provenance.VerifiedFields, "model")
		updated.Provenance.InferredFields = removeField(updated.Provenance.InferredFields, "model")
	} else if probe.RequestedModel != "" {
		updated.Provenance.InferredFields = addField(updated.Provenance.InferredFields, "model")
		updated.Provenance.VerifiedFields = removeField(updated.Provenance.VerifiedFields, "model")
	}
	return updated
}

func addField(fields []string, add ...string) []string {
	present := make(map[string]bool, len(fields))
	for _, field := range fields {
		present[field] = true
	}
	for _, field := range add {
		if !present[field] {
			fields = append(fields, field)
			present[field] = true
		}
	}
	sortStrings(fields)
	return fields
}

func removeField(fields []string, remove ...string) []string {
	drop := make(map[string]bool, len(remove))
	for _, field := range remove {
		drop[field] = true
	}
	kept := fields[:0]
	for _, field := range fields {
		if !drop[field] {
			kept = append(kept, field)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
