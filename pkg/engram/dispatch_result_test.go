package engram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveJSONPath(t *testing.T) {
	document := map[string]any{
		"result": "the answer",
		"nested": map[string]any{"deep": map[string]any{"value": 7.0}},
		"items":  []any{map[string]any{"name": "first"}, map[string]any{"name": "second"}},
	}
	cases := []struct {
		path  string
		want  string
		found bool
	}{
		{"result", "the answer", true},
		{"nested.deep.value", "7", true},
		{"items[1].name", "second", true},
		{"items[9].name", "", false},
		{"missing", "", false},
		{"nested.deep.value.further", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		value, ok := resolveJSONPath(document, c.path)
		if ok != c.found {
			t.Errorf("path %q found=%v, want %v", c.path, ok, c.found)
			continue
		}
		if ok && jsonScalarString(value) != c.want {
			t.Errorf("path %q = %q, want %q", c.path, jsonScalarString(value), c.want)
		}
	}
}

func TestExtractResultJSON(t *testing.T) {
	spec := minimalSpec()
	spec.Result = ResultSpec{
		Format:             ResultFormatJSON,
		JSONPath:           "result",
		ErrorPath:          "is_error",
		TerminalReasonPath: "terminal_reason",
		CostUSDPath:        "total_cost_usd",
		ModelPath:          "modelUsage",
	}
	stdout := []byte(`{
		"result": "  ok  ",
		"is_error": false,
		"terminal_reason": "end_turn",
		"total_cost_usd": 0.265428,
		"modelUsage": {"claude-haiku-4-5-20251001": {"inputTokens": 3}},
		"usage": {"input_tokens": 11, "cache_creation_input_tokens": 44227}
	}`)
	extracted, err := extractResult(spec, providerOutput{Stdout: stdout})
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Result != "ok" {
		t.Errorf("result = %q, want %q", extracted.Result, "ok")
	}
	if extracted.TerminalReason != "end_turn" {
		t.Errorf("terminal reason = %q", extracted.TerminalReason)
	}
	if extracted.CostUSD != 0.265428 {
		t.Errorf("cost = %v", extracted.CostUSD)
	}
	if extracted.ReportedModel != "claude-haiku-4-5-20251001" {
		t.Errorf("reported model = %q", extracted.ReportedModel)
	}
	if extracted.ProviderError {
		t.Error("is_error false was read as an error")
	}
	// Context loading dominates a child's cost, so the cache-creation counter is
	// the number worth surfacing.
	if extracted.Tokens == nil || extracted.Tokens.CacheCreation != 44227 {
		t.Errorf("token usage not captured: %+v", extracted.Tokens)
	}
}

func TestExtractResultReadsProviderReportedError(t *testing.T) {
	// The exit code is a fallback, because these CLIs commonly exit zero after
	// refusing a task or exhausting a budget.
	spec := minimalSpec()
	spec.Result = ResultSpec{Format: ResultFormatJSON, JSONPath: "result", ErrorPath: "is_error"}
	extracted, err := extractResult(spec, providerOutput{
		Stdout: []byte(`{"result":"refused","is_error":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !extracted.ProviderError {
		t.Fatal("a provider that reports its own error while exiting zero must be believed")
	}
}

func TestExtractResultNotesAnAbsentPath(t *testing.T) {
	spec := minimalSpec()
	spec.Result = ResultSpec{Format: ResultFormatJSON, JSONPath: "result"}
	extracted, err := extractResult(spec, providerOutput{Stdout: []byte(`{"other":"value"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(extracted.Notes) == 0 {
		t.Fatal("a configured path missing from valid JSON should be noted, since it means the spec drifted")
	}
}

func TestExtractResultJSONL(t *testing.T) {
	spec := minimalSpec()
	spec.Result = ResultSpec{Format: ResultFormatJSONL, JSONPath: "result"}
	stdout := []byte("{\"type\":\"progress\"}\nnot json at all\n{\"result\":\"first\"}\n{\"result\":\"final\"}\n")
	extracted, err := extractResult(spec, providerOutput{Stdout: stdout})
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Result != "final" {
		t.Fatalf("JSONL scan picked %q, want the last line carrying the result path", extracted.Result)
	}
}

func TestExtractResultLastMessageFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last.txt")
	if err := os.WriteFile(path, []byte("the final message\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := minimalSpec()
	spec.Result = ResultSpec{
		Format:         ResultFormatLastMessageFile,
		OutputFileArgv: []string{"-o", PlaceholderOutputFile},
		ModelRegex:     `(?im)^\s*model:\s*(\S+)`,
	}
	extracted, err := extractResult(spec, providerOutput{
		Stdout:     []byte("  model: gpt-5-codex\n  sandbox: read-only\n"),
		OutputFile: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Result != "the final message" {
		t.Errorf("result = %q", extracted.Result)
	}
	// A preamble echoing resolved configuration is client code, which is what
	// makes it trustworthy for model verification.
	if extracted.ReportedModel != "gpt-5-codex" {
		t.Errorf("reported model = %q", extracted.ReportedModel)
	}
}

func TestExtractResultMissingFileIsAnError(t *testing.T) {
	spec := minimalSpec()
	spec.Result = ResultSpec{
		Format:         ResultFormatLastMessageFile,
		OutputFileArgv: []string{"-o", PlaceholderOutputFile},
	}
	_, err := extractResult(spec, providerOutput{OutputFile: filepath.Join(t.TempDir(), "absent")})
	if err == nil {
		t.Fatal("expected a missing last-message file to be an error")
	}
}

func TestModelMatches(t *testing.T) {
	cases := []struct {
		requested, reported string
		want                bool
	}{
		{"haiku", "claude-haiku-4-5-20251001", true},
		{"claude-haiku-4-5-20251001", "haiku", true},
		{"opus", "claude-haiku-4-5-20251001", false},
		{"haiku", "claude-opus-5,claude-haiku-4-5", true},
		{"", "anything", false},
		{"haiku", "", false},
		{"GPT-5-Codex", "gpt-5-codex", true},
	}
	for _, c := range cases {
		if got := modelMatches(c.requested, c.reported); got != c.want {
			t.Errorf("modelMatches(%q, %q) = %v, want %v", c.requested, c.reported, got, c.want)
		}
	}
}

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"2.1.222\n":                 "2.1.222",
		"codex-cli 0.146.0":         "0.146.0",
		"claude version 1.2.3-beta": "1.2.3-beta",
		"weird output":              "weird output",
	}
	for input, want := range cases {
		if got := normalizeVersion(input); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExtractResultKeepsModelWhenTheResultReadFails(t *testing.T) {
	// The bug this pins: codex printed "model: gpt-5-codex" in its preamble on a run
	// that failed before writing its last-message file, and the early return threw
	// that away -- reporting "model reported: unknown" while the answer sat in the
	// captured output. A failed result read must not discard metadata already read.
	spec := minimalSpec()
	spec.Result = ResultSpec{
		Format:         ResultFormatLastMessageFile,
		OutputFileArgv: []string{"-o", PlaceholderOutputFile},
		ModelRegex:     `(?im)^\s*model:\s*(\S+)`,
	}
	extracted, err := extractResult(spec, providerOutput{
		Stderr:     []byte("workdir: /tmp\nmodel: gpt-5-codex\nsandbox: read-only\nERROR: request failed\n"),
		OutputFile: filepath.Join(t.TempDir(), "never-written.txt"),
	})
	if err == nil {
		t.Fatal("the missing result file must still be reported as an error")
	}
	if extracted.ReportedModel != "gpt-5-codex" {
		t.Fatalf("model identity was discarded along with the result: %q", extracted.ReportedModel)
	}
}

func TestExtractResultKeepsMetadataWhenJSONIsUnparseable(t *testing.T) {
	spec := minimalSpec()
	spec.Result = ResultSpec{Format: ResultFormatJSON, JSONPath: "result", ModelRegex: `(?im)^model:\s*(\S+)`}
	extracted, err := extractResult(spec, providerOutput{
		Stdout: []byte("model: some-model-1\nthis is not JSON at all"),
	})
	if err == nil {
		t.Fatal("unparseable JSON must still be reported")
	}
	if extracted.ReportedModel != "some-model-1" {
		t.Fatalf("metadata was discarded on a parse failure: %q", extracted.ReportedModel)
	}
}

func TestAuthSuppressionConflictRecognizesCredentialComplaints(t *testing.T) {
	// The real string claude emits under --bare with OAuth credentials.
	for _, output := range []string{
		"Not logged in · Please run /login",
		"Error: no API key found",
		"authentication required",
	} {
		if authSuppressionConflict(output) == "" {
			t.Errorf("failed to recognize a credential complaint: %q", output)
		}
	}
	// It must stay quiet on unrelated failures, or the hint becomes noise that
	// sends people chasing an auth problem they do not have.
	for _, output := range []string{
		"ERROR: the 'gpt-5-codex' model is not supported",
		"error: unexpected argument '--model' found",
		"",
	} {
		if hint := authSuppressionConflict(output); hint != "" {
			t.Errorf("false positive on %q: %s", output, hint)
		}
	}
}

func TestApplyProbeKeepsSeedFlagWhenTheProbeFailed(t *testing.T) {
	// A failed probe proved nothing, so it must not retire the "this is a shipped
	// guess" signal -- least of all when the guess is most suspect.
	spec := minimalSpec()
	spec.Provenance.Seed = true

	failed := ApplyProbe(spec, ProbeResult{SmokeOK: false, ExitCode: 1, Version: "9.9.9"}, time.Now())
	if !failed.Provenance.Seed {
		t.Error("a failed probe cleared the seed flag")
	}
	if failed.Provenance.Probe == nil {
		t.Error("the failed attempt should still be recorded")
	}

	succeeded := ApplyProbe(spec, ProbeResult{SmokeOK: true, ExitCode: 0, Version: "9.9.9"}, time.Now())
	if succeeded.Provenance.Seed {
		t.Error("a successful probe should retire the seed flag")
	}
}

func TestReadResultFileRefusesASymlink(t *testing.T) {
	// A child is told its result path but does not own it. Replacing that path with
	// a symlink used to make dispatch read any file the user could read and publish
	// the contents as that task's result.
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("private material"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "result.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readResultFile(link, 1<<20); err == nil {
		t.Fatal("a symlinked result file must be refused, not followed")
	}
}

func TestReadResultFileIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 5000)), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readResultFile(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 100 {
		t.Fatalf("read %d bytes despite a 100-byte cap", len(data))
	}
}

func TestBoundedBufferDiscardsWithoutFailingTheChild(t *testing.T) {
	// A short write would make exec treat this as an error and kill the child, when
	// discarding excess output is precisely the intent.
	buffer := &boundedBuffer{limit: 10}
	n, err := buffer.Write([]byte(strings.Repeat("y", 5000)))
	if err != nil || n != 5000 {
		t.Fatalf("Write reported (%d, %v); it must claim the full write", n, err)
	}
	if len(buffer.Bytes()) != 10 {
		t.Fatalf("buffer kept %d bytes, want 10", len(buffer.Bytes()))
	}
	if !buffer.Truncated() {
		t.Error("truncation must be reported, not silent")
	}
}

func TestUsageIsReadFromASeparateChannelThanTheResult(t *testing.T) {
	// codex writes its final message to a file and reports token usage only as a
	// JSONL event, so looking for usage where the result was found accounts for
	// neither. It also spells two of the counters differently from claude.
	dir := t.TempDir()
	path := filepath.Join(dir, "last.txt")
	if err := os.WriteFile(path, []byte("the answer"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := minimalSpec()
	spec.Result = ResultSpec{
		Format:         ResultFormatLastMessageFile,
		OutputFileArgv: []string{"-o", PlaceholderOutputFile},
		UsageJSONLPath: "usage",
	}
	stdout := `{"type":"thread.started"}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":17892,"cached_input_tokens":9984,` +
		`"cache_write_input_tokens":0,"output_tokens":144,"reasoning_output_tokens":130}}` + "\n"

	extracted, err := extractResult(spec, providerOutput{
		Stdout: []byte(stdout), OutputFile: path, MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Result != "the answer" {
		t.Errorf("result = %q", extracted.Result)
	}
	if extracted.Tokens == nil {
		t.Fatal("token usage was not read from the JSONL stream")
	}
	if extracted.Tokens.Input != 17892 || extracted.Tokens.Output != 144 {
		t.Errorf("tokens = %+v", extracted.Tokens)
	}
	// codex's spellings must map onto the same fields claude's do.
	if extracted.Tokens.CacheRead != 9984 {
		t.Errorf("cached_input_tokens did not map to CacheRead: %+v", extracted.Tokens)
	}
	if extracted.Tokens.Reasoning != 130 {
		t.Errorf("reasoning tokens dropped: %+v", extracted.Tokens)
	}
}

func TestProbeUsesItsOwnBaseArgvWhenTheSpecDeclaresOne(t *testing.T) {
	// A probe and a run want different things from codex: --json reports tokens but
	// suppresses the model preamble, and the probe exists to verify the model.
	spec := minimalSpec()
	spec.BaseArgv = []string{"exec", "--json"}
	spec.Probe = &ProbeOverride{BaseArgv: []string{"exec"}}

	run, err := spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "p"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(run.Argv, "--json") {
		t.Errorf("a normal run should keep --json for its token counts: %#v", run.Argv)
	}
	probe, err := spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "p", ForProbe: true}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if containsString(probe.Argv, "--json") {
		t.Errorf("a probe must drop --json so the model preamble survives: %#v", probe.Argv)
	}
}
