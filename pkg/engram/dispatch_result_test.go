package engram

import (
	"os"
	"path/filepath"
	"testing"
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
