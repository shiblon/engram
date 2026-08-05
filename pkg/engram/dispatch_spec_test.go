package engram

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// minimalSpec is a valid spec used as a base for mutation in tests.
func minimalSpec() *ProviderSpec {
	return &ProviderSpec{
		V:          DispatchSpecVersion,
		Provider:   "fake",
		Executable: "fake-cli",
		BaseArgv:   []string{"exec", "--json"},
		Prompt:     PromptSpec{Transport: PromptTransportStdin},
		Model:      &ArgvFragment{Argv: []string{"--model", PlaceholderModel}},
		Result:     ResultSpec{Format: ResultFormatJSON, JSONPath: "result"},
		Version:    &ArgvFragment{Argv: []string{"--version"}},
	}
}

func TestProviderSpecValidate(t *testing.T) {
	t.Run("minimal spec is valid", func(t *testing.T) {
		if err := minimalSpec().Validate(); err != nil {
			t.Fatalf("valid spec rejected: %v", err)
		}
	})

	t.Run("rejects an unrecognized schema version", func(t *testing.T) {
		spec := minimalSpec()
		spec.V = 99
		if err := spec.Validate(); err == nil {
			t.Fatal("expected a version mismatch to be refused rather than guessed at")
		}
	})

	t.Run("rejects a fragment missing its own placeholder", func(t *testing.T) {
		spec := minimalSpec()
		spec.Model = &ArgvFragment{Argv: []string{"--model"}}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), PlaceholderModel) {
			t.Fatalf("a model fragment with no placeholder would pass a flag with no value; got %v", err)
		}
	})

	t.Run("rejects a placeholder that is not substituted where it appears", func(t *testing.T) {
		spec := minimalSpec()
		spec.BaseArgv = []string{"exec", PlaceholderModel}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "not substituted") {
			t.Fatalf("a typo'd placeholder must fail validation, not reach the child literally; got %v", err)
		}
	})

	t.Run("rejects stdin transport carrying argv", func(t *testing.T) {
		spec := minimalSpec()
		spec.Prompt = PromptSpec{Transport: PromptTransportStdin, Argv: []string{PlaceholderPrompt}}
		if err := spec.Validate(); err == nil {
			t.Fatal("expected stdin transport with argv to be refused")
		}
	})

	t.Run("rejects an unknown prompt transport", func(t *testing.T) {
		spec := minimalSpec()
		spec.Prompt = PromptSpec{Transport: "telepathy"}
		if err := spec.Validate(); err == nil {
			t.Fatal("expected an unknown transport to be refused")
		}
	})

	t.Run("rejects a last-message result with no output file flag", func(t *testing.T) {
		spec := minimalSpec()
		spec.Result = ResultSpec{Format: ResultFormatLastMessageFile}
		if err := spec.Validate(); err == nil {
			t.Fatal("expected a file-reported result with no file flag to be refused")
		}
	})

	t.Run("rejects exit codes that cannot tell a wrong spec from failed work", func(t *testing.T) {
		spec := minimalSpec()
		spec.ExitCodes = ExitCodes{UsageError: 1, RunFailure: 1}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "cannot share exit code") {
			t.Fatalf("expected shared exit codes to be refused; got %v", err)
		}
	})

	t.Run("rejects a model regex that does not compile", func(t *testing.T) {
		spec := minimalSpec()
		spec.Result.ModelRegex = "([unterminated"
		if err := spec.Validate(); err == nil {
			t.Fatal("expected an uncompilable regex to be caught at validation, not at dispatch")
		}
	})
}

func TestParseProviderSpecRejectsUnknownFields(t *testing.T) {
	// A hand-edited spec with a misspelled key must fail loudly. The failure mode
	// being prevented is dispatching N children with a silently dropped flag.
	data := []byte(`{"v":1,"provider":"fake","executable":"f","prompt":{"transport":"stdin"},
		"result":{"format":"text"},"modle":{"argv":["-m","{{model}}"]}}`)
	if _, err := ParseProviderSpec(data); err == nil {
		t.Fatal("expected a misspelled key to be refused rather than ignored")
	}
}

func TestBuildInvocationArgvOrderAndQuoting(t *testing.T) {
	spec := minimalSpec()
	spec.Prompt = PromptSpec{Transport: PromptTransportArgv, Argv: []string{PlaceholderPrompt}}
	spec.Authority = &RoleFragment{Roles: map[string][]string{
		AuthorityReadOnly: {"--sandbox", "read-only"},
	}}
	spec.SuppressContext = &ArgvFragment{Argv: []string{"--bare"}}

	// A prompt full of shell metacharacters, spaces, and newlines. There is no
	// shell, so all of this is just bytes inside one argv element.
	prompt := "review this; rm -rf /\n$(whoami) `id` \"quoted\" 'also quoted'"
	inv, err := spec.BuildInvocation(TaskRequest{
		ID:              "slice-1",
		Prompt:          prompt,
		Model:           "cheap-model",
		Authority:       AuthorityReadOnly,
		SuppressContext: true,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"fake-cli", "exec", "--json", "--model", "cheap-model",
		"--sandbox", "read-only", "--bare", prompt}
	if !reflect.DeepEqual(inv.Argv, want) {
		t.Fatalf("argv order or substitution wrong:\n got %#v\nwant %#v", inv.Argv, want)
	}
	// The prompt must be exactly one element. If substitution ever happened into
	// a string that was split afterward, this is where it would show up as
	// several arguments.
	if inv.Argv[len(inv.Argv)-1] != prompt {
		t.Fatal("the prompt was not delivered as a single argv element")
	}
}

func TestBuildInvocationStdinTransport(t *testing.T) {
	spec := minimalSpec()
	inv, err := spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "hello"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if string(inv.Stdin) != "hello" {
		t.Fatalf("stdin transport did not carry the prompt: %q", inv.Stdin)
	}
	for _, element := range inv.Argv {
		if element == "hello" {
			t.Fatal("prompt leaked into argv on a stdin-transport spec")
		}
	}
}

func TestBuildInvocationAllocatesLastMessageFile(t *testing.T) {
	spec := minimalSpec()
	spec.Result = ResultSpec{
		Format:         ResultFormatLastMessageFile,
		OutputFileArgv: []string{"-o", PlaceholderOutputFile},
	}
	inv, err := spec.BuildInvocation(TaskRequest{ID: "slice/one", Prompt: "p"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if inv.OutputFile == "" {
		t.Fatal("no output file was allocated for a file-reported result")
	}
	if !containsString(inv.Argv, inv.OutputFile) {
		t.Fatalf("the allocated output file never reached argv: %#v", inv.Argv)
	}
	// A task id with a path separator must not become a path.
	if strings.Contains(strings.TrimPrefix(inv.OutputFile, t.TempDir()), "/slice/one") {
		t.Fatalf("task id was not sanitized into a file name: %s", inv.OutputFile)
	}
}

func TestBuildInvocationPromptFileTransport(t *testing.T) {
	spec := minimalSpec()
	spec.Prompt = PromptSpec{Transport: PromptTransportFile, Argv: []string{"--prompt-file", PlaceholderPromptFile}}
	inv, err := spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "a very large slice"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if inv.PromptFile == "" || !containsString(inv.Argv, inv.PromptFile) {
		t.Fatalf("prompt file was not written and passed: %#v", inv)
	}
}

func TestBuildInvocationSystemPromptPrependIsVisible(t *testing.T) {
	// Folding a system prompt into the user prompt changes what the child sees, so
	// it must be declared in the spec and reported as a warning -- never silent.
	spec := minimalSpec()
	spec.Prompt = PromptSpec{Transport: PromptTransportArgv, Argv: []string{PlaceholderPrompt}}
	spec.SystemPrompt = &SystemPromptSpec{Mode: SystemPromptModePrepend}

	inv, err := spec.BuildInvocation(TaskRequest{
		ID:           "t",
		Prompt:       "review the diff",
		SystemPrompt: "You are reviewing for error handling only.",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	last := inv.Argv[len(inv.Argv)-1]
	if !strings.HasPrefix(last, "You are reviewing") || !strings.Contains(last, "review the diff") {
		t.Fatalf("system prompt was not prepended: %q", last)
	}
	if len(inv.Warnings) == 0 {
		t.Fatal("a prepended system prompt must be reported, not applied invisibly")
	}
}

func TestBuildInvocationRefusesModelWithNoFlag(t *testing.T) {
	// The silent failure this prevents: every child in a fan-out quietly running
	// the default model while the output looks entirely plausible.
	spec := minimalSpec()
	spec.Model = nil
	_, err := spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "p", Model: "opus"}, t.TempDir())
	if err == nil {
		t.Fatal("expected a requested model with no model flag to be an error, not a silent default")
	}
}

func TestBuildInvocationWarnsWhenGuardrailsAreUnavailable(t *testing.T) {
	spec := minimalSpec()
	inv, err := spec.BuildInvocation(TaskRequest{
		ID:              "t",
		Prompt:          "p",
		Authority:       AuthorityReadOnly,
		BudgetUSD:       2.5,
		SuppressContext: true,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(inv.Warnings, " | ")
	for _, want := range []string{"authority", "budget", "context-suppression"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning about unavailable %s guardrail; got %q", want, joined)
		}
	}
}

func TestSpecMemoryRoundTrip(t *testing.T) {
	spec := minimalSpec()
	spec.Provenance.LearnedVersion = "1.2.3"
	spec.Provenance.VerifiedFields = []string{"prompt", "result"}

	memory, err := FormatSpecMemory(spec)
	if err != nil {
		t.Fatal(err)
	}
	if memory.Tier != TierLong {
		t.Fatalf("spec memory landed in tier %q, not long-term", memory.Tier)
	}
	if memory.Key != "dispatch-spec-fake" {
		t.Fatalf("unexpected spec memory key %q", memory.Key)
	}
	if memory.Tldr == "" || len([]rune(memory.Tldr)) > MaxTldrLen {
		t.Fatalf("tldr missing or over the ceiling: %q", memory.Tldr)
	}
	// The body leads with prose so a human reading the memory finds the
	// explanation before the JSON.
	if strings.HasPrefix(strings.TrimSpace(memory.Content), "```") {
		t.Fatal("spec memory body should explain itself before the fenced block")
	}

	body, err := ExtractSpecJSON(memory.Content)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseProviderSpec(body)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Provider != spec.Provider || parsed.Provenance.LearnedVersion != "1.2.3" {
		t.Fatalf("spec did not survive the memory round trip: %+v", parsed)
	}
}

func TestExtractSpecJSONAcceptsBareDocument(t *testing.T) {
	if _, err := ExtractSpecJSON(`  {"v":1}  `); err != nil {
		t.Fatalf("a bare JSON document should be accepted: %v", err)
	}
	if _, err := ExtractSpecJSON("no json here"); err == nil {
		t.Fatal("expected prose with no JSON block to be an error")
	}
}

func TestProviderSpecStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	spec := minimalSpec()

	if err := WriteProviderSpec(ctx, db, spec); err != nil {
		t.Fatal(err)
	}
	read, err := ReadProviderSpec(ctx, db, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if read == nil || read.Executable != "fake-cli" {
		t.Fatalf("stored spec did not come back: %+v", read)
	}

	// A learned spec must win over a shipped seed.
	resolved, err := ResolveProviderSpec(ctx, db, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Origin != SpecOriginMemory {
		t.Fatalf("expected the learned spec to win, got origin %q", resolved.Origin)
	}

	specs, problems, err := ListProviderSpecs(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(specs) != 1 || specs[0].Provider != "fake" {
		t.Fatalf("unexpected spec list: %+v", specs)
	}
}

func TestReadProviderSpecReportsMalformedBlockWithRepairPath(t *testing.T) {
	// A malformed block must fail loudly with a clear error rather than silently
	// changing behavior. That is the whole reliability argument for a spec that
	// dispatch parses at the point of action.
	ctx := context.Background()
	db := testDB(t)
	if err := WriteMemory(ctx, db, Memory{
		Tier:    TierLong,
		Key:     DispatchSpecKey("fake"),
		Content: "explanation\n\n```json\n{\"v\": 1, \"provider\": \"fake\",\n```\n",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := ReadProviderSpec(ctx, db, "fake")
	if err == nil {
		t.Fatal("expected a malformed spec block to be an error")
	}
	if !strings.Contains(err.Error(), "engram mem edit") {
		t.Fatalf("the error should name the repair path; got %v", err)
	}
}

func TestResolveProviderSpecFallsBackToSeed(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	resolved, err := ResolveProviderSpec(ctx, db, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Origin != SpecOriginSeed {
		t.Fatalf("expected the shipped seed, got origin %q", resolved.Origin)
	}
}

func TestResolveProviderSpecNamesTheFixForAnUnknownProvider(t *testing.T) {
	ctx := context.Background()
	_, err := ResolveProviderSpec(ctx, testDB(t), "gemini")
	if err == nil {
		t.Fatal("expected an unknown provider to be an error")
	}
	if !strings.Contains(err.Error(), "dispatch survey") {
		t.Fatalf("the error should point at how to learn a spec; got %v", err)
	}
}

func TestSeedSpecsAreValidAndMarkedUnverified(t *testing.T) {
	seeds, err := SeedProviderSpecs()
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) == 0 {
		t.Fatal("no seed specs shipped")
	}
	for provider, spec := range seeds {
		if err := spec.Validate(); err != nil {
			t.Errorf("seed spec %s is invalid: %v", provider, err)
		}
		if !spec.Provenance.Seed {
			t.Errorf("seed spec %s does not mark itself as a seed, so a reader cannot tell a guess from a probed truth", provider)
		}
		if spec.Provenance.Probe != nil {
			t.Errorf("seed spec %s claims a probe it cannot have run on this machine", provider)
		}
		if spec.Provenance.LearnedVersion == "" {
			t.Errorf("seed spec %s records no learned version, so drift against it is undetectable", provider)
		}
		// A seed must round-trip through JSON unchanged, since it is the starting
		// point for a hand edit.
		encoded, err := spec.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var reparsed ProviderSpec
		if err := json.Unmarshal(encoded, &reparsed); err != nil {
			t.Errorf("seed spec %s does not round-trip: %v", provider, err)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBuildInvocationRefusesAnOversizeArgvWithTheFix(t *testing.T) {
	// Measured on Linux: a single argv element tops out at 131071 bytes. Above
	// that, execve returns E2BIG, which explains nothing; dispatch names the
	// transport that fixes it instead.
	spec := minimalSpec()
	spec.Prompt = PromptSpec{Transport: PromptTransportArgv, Argv: []string{PlaceholderPrompt}}
	huge := strings.Repeat("x", MaxArgvElementBytes+1)

	_, err := spec.BuildInvocation(TaskRequest{ID: "big-slice", Prompt: huge}, t.TempDir())
	if err == nil {
		t.Fatal("expected an oversize argv element to be refused before the kernel does it")
	}
	if !strings.Contains(err.Error(), PromptTransportStdin) || !strings.Contains(err.Error(), PromptTransportFile) {
		t.Fatalf("the error should name the transports that fix it; got %v", err)
	}
}

func TestBuildInvocationAllowsALargeSliceOnStdin(t *testing.T) {
	// The same slice that will not fit in argv rides stdin without complaint,
	// which is why stdin is the better default where a provider accepts it.
	spec := minimalSpec()
	huge := strings.Repeat("x", MaxArgvElementBytes*2)
	inv, err := spec.BuildInvocation(TaskRequest{ID: "big-slice", Prompt: huge}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Stdin) != len(huge) {
		t.Fatalf("stdin carried %d bytes, want %d", len(inv.Stdin), len(huge))
	}
}

func TestBuildInvocationOmitsAFragmentMappedToTheEmptyString(t *testing.T) {
	// claude's --permission-mode has no "default" choice, so the default authority
	// role must omit the flag rather than pass an invalid value that a CLI would
	// reject as a usage error.
	spec := minimalSpec()
	spec.Authority = &RoleFragment{Roles: map[string][]string{
		AuthorityReadOnly: {"--permission-mode", "plan"},
		AuthorityDefault:  {},
	}}
	inv, err := spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "p", Authority: AuthorityDefault}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if containsString(inv.Argv, "--permission-mode") {
		t.Fatalf("a role mapped to no flags must omit the whole fragment: %#v", inv.Argv)
	}

	// The mapped roles still resolve normally.
	inv, err = spec.BuildInvocation(TaskRequest{ID: "t", Prompt: "p", Authority: AuthorityReadOnly}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(inv.Argv, "plan") {
		t.Fatalf("read-only did not map to the provider's spelling: %#v", inv.Argv)
	}
}

func TestSeedSpecsOnlyMapAuthorityToValuesTheProviderAccepts(t *testing.T) {
	// Verified against the installed help on 2026-08-05: claude 2.1.222 accepts
	// acceptEdits, auto, bypassPermissions, manual, dontAsk, plan; codex-cli
	// 0.146.0 accepts read-only, workspace-write, danger-full-access. A value
	// outside those sets is a usage error, which reads as provider trouble.
	accepted := map[string]map[string]bool{
		"claude": {"acceptEdits": true, "auto": true, "bypassPermissions": true, "manual": true, "dontAsk": true, "plan": true},
		"codex":  {"read-only": true, "workspace-write": true, "danger-full-access": true},
	}
	seeds, err := SeedProviderSpecs()
	if err != nil {
		t.Fatal(err)
	}
	for provider, spec := range seeds {
		valid, known := accepted[provider]
		if !known || spec.Authority == nil {
			continue
		}
		for role, argv := range spec.Authority.Roles {
			for i, element := range argv {
				// Only check values, which follow a flag; flags themselves start with -.
				if i == 0 || strings.HasPrefix(element, "-") || strings.HasPrefix(argv[i-1], "--disallowedTools") {
					continue
				}
				if !valid[element] {
					t.Errorf("seed %s authority role %q passes %q, which the CLI does not accept", provider, role, element)
				}
			}
		}
	}
}

func TestArgvIsRedactedBeforeItReachesTheStream(t *testing.T) {
	// task_start emits resolved argv, which is what makes --dry-run useful. But a
	// provider carrying its prompt in argv would publish the caller's content into
	// every captured stream -- including a prompt supplied via prompt_file
	// specifically to keep it out of view.
	spec := minimalSpec()
	spec.Prompt = PromptSpec{Transport: PromptTransportArgv, Argv: []string{PlaceholderPrompt}}
	spec.SystemPrompt = &SystemPromptSpec{Mode: SystemPromptModeArgv, Argv: []string{"--sys", PlaceholderSystemPrompt}}

	secret := "the confidential diff nobody should see in a log"
	sysSecret := "composed reviewer context"
	inv, err := spec.BuildInvocation(TaskRequest{
		ID: "t", Prompt: secret, SystemPrompt: sysSecret, Model: "m",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The real argv must still carry the real prompt, or the child gets nothing.
	if !containsString(inv.Argv, secret) {
		t.Fatal("the child must still receive the actual prompt")
	}
	redacted := strings.Join(RedactArgv(inv.Argv, inv.Secrets), " ")
	for _, leaked := range []string{secret, sysSecret} {
		if strings.Contains(redacted, leaked) {
			t.Errorf("redaction missed %q:\n%s", leaked, redacted)
		}
	}
	if !strings.Contains(redacted, "<redacted") {
		t.Errorf("redaction should say something was removed, not silently drop it:\n%s", redacted)
	}
	// Flags must survive, or the redacted argv is useless for diagnosis.
	if !strings.Contains(redacted, "--model m") || !strings.Contains(redacted, "--sys") {
		t.Errorf("redaction destroyed the diagnostic value of the argv:\n%s", redacted)
	}
}

func TestTaskIDsThatSanitizeAlikeGetDistinctFiles(t *testing.T) {
	// Duplicate-id rejection compares RAW ids, so "a/b" and "a?b" both passed it and
	// then both sanitized to "a-b" -- sharing one result file, so two tasks could
	// read each other's results.
	spec := minimalSpec()
	spec.Result = ResultSpec{
		Format:         ResultFormatLastMessageFile,
		OutputFileArgv: []string{"-o", PlaceholderOutputFile},
	}
	dir := t.TempDir()
	first, err := spec.BuildInvocation(TaskRequest{ID: "a/b", Prompt: "p"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := spec.BuildInvocation(TaskRequest{ID: "a?b", Prompt: "p"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.OutputFile == second.OutputFile {
		t.Fatalf("two distinct task ids share a result file: %s", first.OutputFile)
	}
	// And the name should still be recognizable to a human debugging a temp dir.
	if !strings.Contains(filepath.Base(first.OutputFile), "a-b") {
		t.Errorf("the file name lost all trace of the task id: %s", first.OutputFile)
	}
}
