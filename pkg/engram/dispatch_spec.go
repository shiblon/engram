package engram

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DispatchSpecVersion is the schema version of a provider invocation spec. It is
// the one contract that must stay stable while everything behind it churns, so a
// spec carries it explicitly and a reader refuses an unrecognized value rather
// than guessing at a differently-shaped document.
const DispatchSpecVersion = 1

// DispatchSpecKeyPrefix is the memory key prefix for provider specs. Specs are
// ordinary long-term memories rather than a new table or file location, so
// repairing a broken invocation uses the one tool the agent already reaches for.
const DispatchSpecKeyPrefix = "dispatch-spec-"

// Placeholder tokens a spec may reference. Substitution happens into
// already-split argv elements and never into a string that is split afterward,
// which is what keeps a prompt containing spaces, semicolons, or newlines a
// single argument. There is no shell, so there is nothing to quote.
const (
	PlaceholderPrompt       = "{{prompt}}"
	PlaceholderPromptFile   = "{{prompt_file}}"
	PlaceholderModel        = "{{model}}"
	PlaceholderSystemPrompt = "{{system_prompt}}"
	PlaceholderAuthority    = "{{authority}}"
	PlaceholderBudgetUSD    = "{{budget_usd}}"
	PlaceholderWorkdir      = "{{workdir}}"
	PlaceholderOutputFile   = "{{output_file}}"
)

var placeholderPattern = regexp.MustCompile(`\{\{[a-z_]+\}\}`)

// Argv transport has a hard ceiling, and hitting it produces E2BIG from execve
// rather than anything that explains itself. Measured on Linux 6.12: a single
// element tops out at 131071 bytes (MAX_ARG_STRLEN is 32 pages, less the
// terminating NUL) and the whole vector at about 2 MiB. macOS has no per-element
// limit but a smaller total, so checking both is right on every platform.
//
// The practical consequence: a diff slice larger than about 128 KB must travel by
// stdin or by file, and dispatch says so instead of letting the child fail
// obscurely.
const (
	MaxArgvElementBytes = 131071
	MaxArgvTotalBytes   = 1 << 20
)

// Prompt transports. A provider either takes the prompt as an argv element, on
// stdin, or from a file it is told to read.
const (
	PromptTransportArgv  = "argv"
	PromptTransportStdin = "stdin"
	PromptTransportFile  = "file"
)

// Result formats. Every one of these reads a documented field or file the
// provider itself produces; none of them parses the provider's prose.
const (
	ResultFormatJSON            = "json"
	ResultFormatJSONL           = "jsonl"
	ResultFormatLastMessageFile = "last-message-file"
	ResultFormatText            = "text"
)

// System-prompt delivery modes. A provider with no system-prompt flag folds the
// parent's context into the prompt instead, and says so in the spec rather than
// having dispatch do it invisibly.
const (
	SystemPromptModeArgv    = "argv"
	SystemPromptModePrepend = "prepend"
)

// Working-directory modes.
const (
	WorkdirModeInherit = "inherit"
	WorkdirModeArgv    = "argv"
	WorkdirModeChdir   = "chdir"
)

// Authority levels a caller asks for by role. The spec maps each to whatever the
// provider spells it, so a batch config never names a provider-specific value.
const (
	AuthorityReadOnly = "read-only"
	AuthorityEdit     = "edit"
	AuthorityDefault  = "default"
)

// ArgvFragment is an optional group of argv elements contributed when its value
// is supplied. Values maps a role-level name (a model alias, an authority level)
// to the provider's own spelling, so a portable batch config can say "read-only"
// without knowing whether this CLI calls that a sandbox or a permission mode.
//
// Mapping a role to the empty string means "omit this fragment entirely", which is
// how a spec expresses "let the CLI use its own default". That is not a nicety:
// claude's --permission-mode has no "default" among its choices, so emitting one
// would be a usage error rather than a no-op.
type ArgvFragment struct {
	Argv   []string          `json:"argv"`
	Values map[string]string `json:"values,omitempty"`
}

// resolve maps a role-level value through Values, passing it through unchanged
// when no mapping exists. An empty result means the fragment is omitted.
func (f *ArgvFragment) resolve(value string) string {
	if f == nil || f.Values == nil {
		return value
	}
	if mapped, ok := f.Values[value]; ok {
		return mapped
	}
	return value
}

// PromptSpec describes how the prompt reaches the child.
type PromptSpec struct {
	Transport string   `json:"transport"`
	Argv      []string `json:"argv,omitempty"`
}

// SystemPromptSpec describes how the parent's composed context reaches the
// child. Prepend mode is explicit in the spec because folding a system prompt
// into a user prompt changes what the child sees, and a silent fold would be
// indistinguishable from a working flag.
type SystemPromptSpec struct {
	Mode string   `json:"mode"`
	Argv []string `json:"argv,omitempty"`
}

// WorkdirSpec describes working-directory semantics: inherited, passed as a
// flag, or set on the child process.
type WorkdirSpec struct {
	Mode string   `json:"mode"`
	Argv []string `json:"argv,omitempty"`
}

// ResultSpec names where the final answer and the run's reported metadata
// appear. Paths are dot-separated with bracketed indices ("a.b[0].c").
type ResultSpec struct {
	Format             string   `json:"format"`
	JSONPath           string   `json:"json_path,omitempty"`
	ErrorPath          string   `json:"error_path,omitempty"`
	TerminalReasonPath string   `json:"terminal_reason_path,omitempty"`
	CostUSDPath        string   `json:"cost_usd_path,omitempty"`
	ModelPath          string   `json:"model_path,omitempty"`
	ModelRegex         string   `json:"model_regex,omitempty"`
	OutputFileArgv     []string `json:"output_file_argv,omitempty"`
}

// ExitCodes records what a provider's exit statuses mean, to whatever extent
// they are discoverable. Zero means unknown, which is unambiguous because a
// successful exit can never signal either failure class. The distinction is what
// lets a self-healing loop know whether to re-learn the spec or report the work
// as failed: a usage error means the spec is wrong, a run failure means the spec
// ran and the work did not succeed.
type ExitCodes struct {
	UsageError int `json:"usage_error,omitempty"`
	RunFailure int `json:"run_failure,omitempty"`
}

// ProbeRecord is what a probe established, kept in-band so a spec that travels
// to another machine is detectably unverified there rather than quietly trusted.
type ProbeRecord struct {
	At             string  `json:"at,omitempty"`
	Version        string  `json:"version,omitempty"`
	ExitCode       int     `json:"exit_code"`
	RequestedModel string  `json:"requested_model,omitempty"`
	ReportedModel  string  `json:"reported_model,omitempty"`
	ModelVerified  bool    `json:"model_verified"`
	CostUSD        float64 `json:"cost_usd,omitempty"`
	FlagLiveness   string  `json:"flag_liveness,omitempty"`
}

// Provenance records what the spec was learned against and which fields were
// verified rather than inferred. It is what dissolves the objection that a spec
// must never travel: a spec arriving on a machine with different versions is
// detected as stale at dispatch and re-learned, so it is a seed, not a lie.
type Provenance struct {
	LearnedVersion string       `json:"learned_version,omitempty"`
	LearnedAt      string       `json:"learned_at,omitempty"`
	HelpDigest     string       `json:"help_digest,omitempty"`
	VerifiedFields []string     `json:"verified_fields,omitempty"`
	InferredFields []string     `json:"inferred_fields,omitempty"`
	Probe          *ProbeRecord `json:"probe,omitempty"`
	Seed           bool         `json:"seed,omitempty"`
	Notes          string       `json:"notes,omitempty"`
}

// ProviderSpec is the declarative invocation recipe for one provider CLI: an
// argv template with placeholders, not a shell command string and not a script.
// Docker's exec form and Kubernetes command/args are the prior art, chosen for
// the reason that applies here -- an array has no quoting exposure to begin with,
// works identically on Windows, and can be validated before spending tokens.
type ProviderSpec struct {
	V               int               `json:"v"`
	Provider        string            `json:"provider"`
	Executable      string            `json:"executable"`
	BaseArgv        []string          `json:"base_argv,omitempty"`
	Prompt          PromptSpec        `json:"prompt"`
	Model           *ArgvFragment     `json:"model,omitempty"`
	SystemPrompt    *SystemPromptSpec `json:"system_prompt,omitempty"`
	Authority       *ArgvFragment     `json:"authority,omitempty"`
	Budget          *ArgvFragment     `json:"budget,omitempty"`
	SuppressContext *ArgvFragment     `json:"suppress_context,omitempty"`
	Workdir         WorkdirSpec       `json:"workdir"`
	Env             map[string]string `json:"env,omitempty"`
	Result          ResultSpec        `json:"result"`
	Version         *ArgvFragment     `json:"version,omitempty"`
	ExitCodes       ExitCodes         `json:"exit_codes,omitempty"`
	Provenance      Provenance        `json:"provenance,omitempty"`
}

// ParseProviderSpec decodes and validates a spec document. Validation happens
// when dispatch parses the block rather than when the memory is written, which is
// the right trade for something meant to be hand-edited at 11pm.
func ParseProviderSpec(data []byte) (*ProviderSpec, error) {
	var spec ProviderSpec
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return nil, fmt.Errorf("parse provider spec: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return &spec, nil
}

// Validate checks the properties dispatch relies on before it spawns anything.
func (s *ProviderSpec) Validate() error {
	if s.V != DispatchSpecVersion {
		return fmt.Errorf("provider spec: unsupported version %d (this engram understands %d)", s.V, DispatchSpecVersion)
	}
	if strings.TrimSpace(s.Provider) == "" {
		return fmt.Errorf("provider spec: missing provider name")
	}
	if strings.ContainsAny(s.Provider, " \t\n/") {
		return fmt.Errorf("provider spec: provider %q must be a bare name", s.Provider)
	}
	if strings.TrimSpace(s.Executable) == "" {
		return fmt.Errorf("provider spec %s: missing executable", s.Provider)
	}
	if err := s.checkArgv("base_argv", s.BaseArgv, nil); err != nil {
		return err
	}

	switch s.Prompt.Transport {
	case PromptTransportStdin:
		if len(s.Prompt.Argv) > 0 {
			return fmt.Errorf("provider spec %s: stdin prompt transport takes no argv", s.Provider)
		}
	case PromptTransportArgv:
		if err := s.requirePlaceholder("prompt.argv", s.Prompt.Argv, PlaceholderPrompt); err != nil {
			return err
		}
	case PromptTransportFile:
		if err := s.requirePlaceholder("prompt.argv", s.Prompt.Argv, PlaceholderPromptFile); err != nil {
			return err
		}
	case "":
		return fmt.Errorf("provider spec %s: missing prompt transport (%s, %s, or %s)",
			s.Provider, PromptTransportArgv, PromptTransportStdin, PromptTransportFile)
	default:
		return fmt.Errorf("provider spec %s: unknown prompt transport %q", s.Provider, s.Prompt.Transport)
	}

	if s.Model != nil {
		if err := s.requirePlaceholder("model.argv", s.Model.Argv, PlaceholderModel); err != nil {
			return err
		}
	}
	if s.SystemPrompt != nil {
		switch s.SystemPrompt.Mode {
		case SystemPromptModeArgv:
			if err := s.requirePlaceholder("system_prompt.argv", s.SystemPrompt.Argv, PlaceholderSystemPrompt); err != nil {
				return err
			}
		case SystemPromptModePrepend:
			if len(s.SystemPrompt.Argv) > 0 {
				return fmt.Errorf("provider spec %s: system_prompt prepend mode takes no argv", s.Provider)
			}
		default:
			return fmt.Errorf("provider spec %s: unknown system_prompt mode %q", s.Provider, s.SystemPrompt.Mode)
		}
	}
	if s.Authority != nil {
		if err := s.requirePlaceholder("authority.argv", s.Authority.Argv, PlaceholderAuthority); err != nil {
			return err
		}
	}
	if s.Budget != nil {
		if err := s.requirePlaceholder("budget.argv", s.Budget.Argv, PlaceholderBudgetUSD); err != nil {
			return err
		}
	}
	if s.SuppressContext != nil {
		if err := s.checkArgv("suppress_context.argv", s.SuppressContext.Argv, nil); err != nil {
			return err
		}
		if len(s.SuppressContext.Argv) == 0 {
			return fmt.Errorf("provider spec %s: suppress_context declared with no argv", s.Provider)
		}
	}
	switch s.Workdir.Mode {
	case "", WorkdirModeInherit, WorkdirModeChdir:
		if len(s.Workdir.Argv) > 0 {
			return fmt.Errorf("provider spec %s: workdir mode %q takes no argv", s.Provider, s.Workdir.Mode)
		}
	case WorkdirModeArgv:
		if err := s.requirePlaceholder("workdir.argv", s.Workdir.Argv, PlaceholderWorkdir); err != nil {
			return err
		}
	default:
		return fmt.Errorf("provider spec %s: unknown workdir mode %q", s.Provider, s.Workdir.Mode)
	}
	if s.Version != nil {
		if err := s.checkArgv("version.argv", s.Version.Argv, nil); err != nil {
			return err
		}
		if len(s.Version.Argv) == 0 {
			return fmt.Errorf("provider spec %s: version declared with no argv", s.Provider)
		}
	}

	switch s.Result.Format {
	case ResultFormatJSON, ResultFormatJSONL:
		if s.Result.JSONPath == "" {
			return fmt.Errorf("provider spec %s: result format %q needs json_path", s.Provider, s.Result.Format)
		}
	case ResultFormatLastMessageFile:
		if err := s.requirePlaceholder("result.output_file_argv", s.Result.OutputFileArgv, PlaceholderOutputFile); err != nil {
			return err
		}
	case ResultFormatText:
	case "":
		return fmt.Errorf("provider spec %s: missing result format", s.Provider)
	default:
		return fmt.Errorf("provider spec %s: unknown result format %q", s.Provider, s.Result.Format)
	}
	if s.Result.ModelRegex != "" {
		if _, err := regexp.Compile(s.Result.ModelRegex); err != nil {
			return fmt.Errorf("provider spec %s: result.model_regex does not compile: %w", s.Provider, err)
		}
	}
	if s.ExitCodes.UsageError != 0 && s.ExitCodes.UsageError == s.ExitCodes.RunFailure {
		return fmt.Errorf("provider spec %s: usage_error and run_failure cannot share exit code %d, "+
			"since the whole point is telling a wrong spec from failed work", s.Provider, s.ExitCodes.UsageError)
	}
	return nil
}

// checkArgv rejects placeholders a fragment has no business referencing, so a
// typo fails validation instead of reaching a child as a literal "{{modle}}".
func (s *ProviderSpec) checkArgv(field string, argv []string, allowed []string) error {
	permitted := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		permitted[a] = true
	}
	for _, element := range argv {
		for _, found := range placeholderPattern.FindAllString(element, -1) {
			if !permitted[found] {
				return fmt.Errorf("provider spec %s: %s references placeholder %s, which is not substituted there",
					s.Provider, field, found)
			}
		}
	}
	return nil
}

// requirePlaceholder enforces that a fragment both mentions its own placeholder
// and mentions nothing else. A fragment that forgets its placeholder would pass
// a flag with no value, which fails in a way that looks like provider trouble.
func (s *ProviderSpec) requirePlaceholder(field string, argv []string, placeholder string) error {
	if len(argv) == 0 {
		return fmt.Errorf("provider spec %s: %s is empty but must contain %s", s.Provider, field, placeholder)
	}
	if err := s.checkArgv(field, argv, []string{placeholder}); err != nil {
		return err
	}
	for _, element := range argv {
		if strings.Contains(element, placeholder) {
			return nil
		}
	}
	return fmt.Errorf("provider spec %s: %s must contain %s", s.Provider, field, placeholder)
}

// Marshal renders the spec as indented JSON. Indented on purpose: this document
// is meant to be read and hand-edited when a provider breaks.
func (s *ProviderSpec) Marshal() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// Clone returns a deep copy, so probing can annotate provenance without mutating
// a spec another goroutine is reading.
func (s *ProviderSpec) Clone() *ProviderSpec {
	if s == nil {
		return nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	var out ProviderSpec
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return &out
}

// TaskRequest is one child's worth of work, expressed in role-level terms. It
// names no provider flags, which is what keeps a dispatch plan portable.
type TaskRequest struct {
	ID              string
	Prompt          string
	SystemPrompt    string
	Model           string
	Authority       string
	BudgetUSD       float64
	Workdir         string
	SuppressContext bool
}

// Invocation is a fully resolved child process: argv, stdin bytes, environment
// additions, and the temp files the spec asked for.
type Invocation struct {
	Argv       []string
	Stdin      []byte
	Env        []string
	Dir        string
	OutputFile string
	PromptFile string
	// Warnings records deliberate accommodations the caller should see, such as
	// a system prompt folded into the user prompt because the provider has no
	// flag for it, or a requested guardrail this provider cannot express.
	Warnings []string
}

// BuildInvocation resolves a spec plus a request into a concrete argv.
//
// Element order is fixed and load-bearing: executable, base argv, then the
// optional fragments, then the prompt last. A prompt delivered as a positional
// argument has to follow the flags, and pinning the order here means a spec
// author never has to think about it.
//
// tempDir receives any files the spec requires (a prompt file, a last-message
// output file); pass an empty string only when the spec needs neither.
func (s *ProviderSpec) BuildInvocation(request TaskRequest, tempDir string) (*Invocation, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, fmt.Errorf("task %s: empty prompt", request.ID)
	}

	inv := &Invocation{Argv: []string{s.Executable}}
	prompt := request.Prompt

	if request.SystemPrompt != "" {
		switch {
		case s.SystemPrompt == nil:
			inv.Warnings = append(inv.Warnings, fmt.Sprintf(
				"provider %s spec declares no system-prompt delivery; the composed context was dropped", s.Provider))
		case s.SystemPrompt.Mode == SystemPromptModePrepend:
			prompt = request.SystemPrompt + "\n\n---\n\n" + prompt
			inv.Warnings = append(inv.Warnings, fmt.Sprintf(
				"provider %s has no system-prompt flag; the composed context was prepended to the prompt", s.Provider))
		}
	}

	inv.Argv = append(inv.Argv, s.BaseArgv...)

	if request.Model != "" {
		if s.Model == nil {
			return nil, fmt.Errorf("task %s: provider %s spec has no model flag, so model %q cannot be honored; "+
				"omit the model or re-learn the spec", request.ID, s.Provider, request.Model)
		}
		if resolved := s.Model.resolve(request.Model); resolved != "" {
			inv.Argv = append(inv.Argv, substituteArgv(s.Model.Argv, map[string]string{
				PlaceholderModel: resolved,
			})...)
		}
	}
	if request.Authority != "" {
		// Authority is the one guardrail a dispatched child cannot negotiate, so
		// every way it fails to apply is reported rather than absorbed. A human
		// reading the stream should never have to infer what a child could do.
		switch {
		case s.Authority == nil:
			inv.Warnings = append(inv.Warnings, fmt.Sprintf(
				"provider %s spec has no authority flag, so %q is NOT enforced and this child runs with "+
					"whatever authority the CLI defaults to", s.Provider, request.Authority))
		default:
			resolved := s.Authority.resolve(request.Authority)
			if resolved == "" && request.Authority != AuthorityDefault {
				inv.Warnings = append(inv.Warnings, fmt.Sprintf(
					"provider %s spec maps authority %q to nothing, so this child runs with the CLI's default "+
						"authority rather than the level that was asked for", s.Provider, request.Authority))
			}
			if resolved != "" {
				inv.Argv = append(inv.Argv, substituteArgv(s.Authority.Argv, map[string]string{
					PlaceholderAuthority: resolved,
				})...)
			}
		}
		if request.Authority != AuthorityReadOnly {
			// Not a spec problem, so not an error -- but a write-capable child is
			// worth saying out loud on the stream. Nobody can interrupt it, N of
			// them share one working tree with no coordination, and the edits are
			// observed only after the join.
			inv.Warnings = append(inv.Warnings, fmt.Sprintf(
				"task runs with %q authority, not read-only: no human can intervene mid-run, and concurrent "+
					"children writing the same tree are not coordinated", request.Authority))
		}
	}
	if request.BudgetUSD > 0 {
		if s.Budget == nil {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf(
				"provider %s spec has no budget flag; the batch deadline is the only guardrail", s.Provider))
		} else {
			inv.Argv = append(inv.Argv, substituteArgv(s.Budget.Argv, map[string]string{
				PlaceholderBudgetUSD: strconv.FormatFloat(request.BudgetUSD, 'f', -1, 64),
			})...)
		}
	}
	if request.SuppressContext {
		if s.SuppressContext == nil {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf(
				"provider %s spec has no context-suppression flag; this child pays full context load on spawn", s.Provider))
		} else {
			inv.Argv = append(inv.Argv, s.SuppressContext.Argv...)
		}
	}
	if request.SystemPrompt != "" && s.SystemPrompt != nil && s.SystemPrompt.Mode == SystemPromptModeArgv {
		inv.Argv = append(inv.Argv, substituteArgv(s.SystemPrompt.Argv, map[string]string{
			PlaceholderSystemPrompt: request.SystemPrompt,
		})...)
	}

	switch s.Workdir.Mode {
	case WorkdirModeArgv:
		if request.Workdir != "" {
			inv.Argv = append(inv.Argv, substituteArgv(s.Workdir.Argv, map[string]string{
				PlaceholderWorkdir: request.Workdir,
			})...)
		}
	case WorkdirModeChdir:
		inv.Dir = request.Workdir
	default:
		if request.Workdir != "" {
			inv.Dir = request.Workdir
		}
	}

	if s.Result.Format == ResultFormatLastMessageFile {
		if tempDir == "" {
			return nil, fmt.Errorf("task %s: provider %s reports its result through a file, but no temp dir was provided",
				request.ID, s.Provider)
		}
		inv.OutputFile = filepath.Join(tempDir, sanitizeFileComponent(request.ID)+".last-message.txt")
		inv.Argv = append(inv.Argv, substituteArgv(s.Result.OutputFileArgv, map[string]string{
			PlaceholderOutputFile: inv.OutputFile,
		})...)
	}

	switch s.Prompt.Transport {
	case PromptTransportStdin:
		inv.Stdin = []byte(prompt)
	case PromptTransportArgv:
		inv.Argv = append(inv.Argv, substituteArgv(s.Prompt.Argv, map[string]string{
			PlaceholderPrompt: prompt,
		})...)
	case PromptTransportFile:
		if tempDir == "" {
			return nil, fmt.Errorf("task %s: provider %s takes its prompt from a file, but no temp dir was provided",
				request.ID, s.Provider)
		}
		inv.PromptFile = filepath.Join(tempDir, sanitizeFileComponent(request.ID)+".prompt.txt")
		if err := os.WriteFile(inv.PromptFile, []byte(prompt), 0o600); err != nil {
			return nil, fmt.Errorf("task %s: write prompt file: %w", request.ID, err)
		}
		inv.Argv = append(inv.Argv, substituteArgv(s.Prompt.Argv, map[string]string{
			PlaceholderPromptFile: inv.PromptFile,
		})...)
	}

	for _, key := range sortedKeys(s.Env) {
		inv.Env = append(inv.Env, key+"="+s.Env[key])
	}
	if err := checkArgvSize(request.ID, s, inv.Argv); err != nil {
		return nil, err
	}
	return inv, nil
}

// checkArgvSize refuses an argv the kernel would reject, naming the transport that
// fixes it. E2BIG at spawn time is exactly as fatal and far less legible.
func checkArgvSize(taskID string, spec *ProviderSpec, argv []string) error {
	total := 0
	for _, element := range argv {
		total += len(element) + 1
		if len(element) > MaxArgvElementBytes {
			return fmt.Errorf("task %s: one argument is %d bytes, over the %d-byte per-argument limit; "+
				"switch the %s spec's prompt transport to %q or %q, or slice the input smaller",
				taskID, len(element), MaxArgvElementBytes, spec.Provider, PromptTransportStdin, PromptTransportFile)
		}
	}
	if total > MaxArgvTotalBytes {
		return fmt.Errorf("task %s: the whole argument vector is %d bytes, over the %d-byte ceiling; "+
			"switch the %s spec's prompt transport to %q or %q",
			taskID, total, MaxArgvTotalBytes, spec.Provider, PromptTransportStdin, PromptTransportFile)
	}
	return nil
}

// substituteArgv replaces placeholders inside each element of an already-split
// template. The substituted value is never re-split, so a prompt containing
// spaces stays one argument -- the rule that keeps this free of the whole class
// of bugs shell injection belongs to.
func substituteArgv(template []string, values map[string]string) []string {
	out := make([]string, 0, len(template))
	for _, element := range template {
		for placeholder, value := range values {
			element = strings.ReplaceAll(element, placeholder, value)
		}
		out = append(out, element)
	}
	return out
}

// sanitizeFileComponent turns a task id into something safe to use as a file
// name without becoming a path traversal or colliding with a sibling.
func sanitizeFileComponent(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		return "task"
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- spec storage as ordinary memory ----

// specFencePattern finds the first fenced JSON block in a memory body.
var specFencePattern = regexp.MustCompile("(?s)```json\\s*\\n(.*?)\\n```")

// DispatchSpecKey is the memory key holding the spec for a provider.
func DispatchSpecKey(provider string) string {
	return DispatchSpecKeyPrefix + provider
}

// ProviderFromSpecKey reports the provider a spec key names.
func ProviderFromSpecKey(key string) (string, bool) {
	if !strings.HasPrefix(key, DispatchSpecKeyPrefix) {
		return "", false
	}
	provider := strings.TrimPrefix(key, DispatchSpecKeyPrefix)
	if provider == "" {
		return "", false
	}
	return provider, true
}

// ExtractSpecJSON pulls the fenced JSON block out of a memory body. The body is
// prose plus one fenced block so a human reading the memory finds an explanation
// first, and so the block can be edited without disturbing the explanation.
func ExtractSpecJSON(content string) ([]byte, error) {
	match := specFencePattern.FindStringSubmatch(content)
	if match == nil {
		trimmed := strings.TrimSpace(content)
		if strings.HasPrefix(trimmed, "{") {
			return []byte(trimmed), nil
		}
		return nil, fmt.Errorf("no fenced ```json block found in spec memory")
	}
	return []byte(match[1]), nil
}

// FormatSpecMemory renders a spec as a long-term memory: a short explanation of
// what the reader is looking at and how to repair it, then the spec itself in a
// fenced block.
func FormatSpecMemory(spec *ProviderSpec) (Memory, error) {
	body, err := spec.Marshal()
	if err != nil {
		return Memory{}, err
	}
	verified := "none recorded"
	if len(spec.Provenance.VerifiedFields) > 0 {
		verified = strings.Join(spec.Provenance.VerifiedFields, ", ")
	}
	learned := spec.Provenance.LearnedVersion
	if learned == "" {
		learned = "unrecorded version"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Learned invocation spec for the %s CLI, parsed by `engram dispatch` at the point of\n", spec.Provider)
	fmt.Fprintf(&b, "action. Learned against %s. Verified fields: %s.\n\n", learned, verified)
	b.WriteString("This is a config store, not guidance: dispatch reads it, so it cannot be quietly\n")
	b.WriteString("disregarded, only malformed. When a run fails because a flag moved, edit the JSON\n")
	b.WriteString("below and write it back -- that beats waiting for a release or re-running a learner.\n")
	b.WriteString("Check your edit with `engram dispatch spec validate --provider " + spec.Provider + "`.\n\n")
	b.WriteString("```json\n")
	b.Write(body)
	b.WriteString("\n```\n")

	tldr := fmt.Sprintf("Invocation spec for the %s CLI (learned against %s); dispatch parses it, edit the JSON to repair a moved flag.",
		spec.Provider, learned)
	return Memory{
		Tier:    TierLong,
		Key:     DispatchSpecKey(spec.Provider),
		Content: b.String(),
		Tldr:    truncateRunes(tldr, MaxTldrLen),
	}, nil
}

// HelpDigest is the content digest of the help text a spec was learned from, so
// drift in the provider's documented surface is detectable rather than assumed
// absent. Same mechanism the automation catalog uses: persist the judgment with a
// digest, re-derive on drift.
func HelpDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + fmt.Sprintf("%x", sum[:])
}
