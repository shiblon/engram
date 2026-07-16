package engram

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// samePath compares two paths after resolving symlinks, so macOS /var → /private/var
// differences don't cause spurious failures.
func samePath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}

// --- pure functions ---

func TestPatchedFiles(t *testing.T) {
	// A full V4A envelope exercising every file-naming header, including a
	// rename (Update File + Move to). The patch may sit under any tool_input
	// field name, so the field key here ("input") is deliberately not "command".
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: pkg/new.go",
		"+package pkg",
		"*** Update File: cmd/old.go",
		"*** Move to: cmd/renamed.go",
		"@@",
		"-gone",
		"+kept",
		"*** Delete File: docs/stale.md",
		"*** End Patch",
	}, "\n")

	cases := []struct {
		name      string
		toolInput string
		want      []string
	}{
		{
			name:      "all headers, patch under input field",
			toolInput: mustJSONObj(t, map[string]string{"input": patch}),
			want:      []string{"pkg/new.go", "cmd/old.go", "cmd/renamed.go", "docs/stale.md"},
		},
		{
			name:      "patch arriving as a shell heredoc under command",
			toolInput: mustJSONObj(t, map[string]string{"command": "apply_patch <<'EOF'\n" + patch + "\nEOF"}),
			want:      []string{"pkg/new.go", "cmd/old.go", "cmd/renamed.go", "docs/stale.md"},
		},
		{
			name:      "no patch present",
			toolInput: mustJSONObj(t, map[string]string{"command": "go test ./..."}),
			want:      nil,
		},
		{
			name:      "not a JSON object",
			toolInput: `"bare string"`,
			want:      nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PatchedFiles(json.RawMessage(c.toolInput))
			if !equalStrings(got, c.want) {
				t.Errorf("PatchedFiles() = %v, want %v", got, c.want)
			}
		})
	}
}

func mustJSONObj(t *testing.T, m map[string]string) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRelPath(t *testing.T) {
	cases := []struct {
		root    string
		absPath string
		want    string
		wantErr bool
	}{
		{"/proj", "/proj/src/main.go", "src/main.go", false},
		{"/proj", "/proj", ".", false},
		{"/proj", "/other/file.go", "", true},
	}
	for _, c := range cases {
		got, err := RelPath(c.root, c.absPath)
		if (err != nil) != c.wantErr {
			t.Errorf("RelPath(%q, %q) error = %v, wantErr %v", c.root, c.absPath, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("RelPath(%q, %q) = %q, want %q", c.root, c.absPath, got, c.want)
		}
	}
}

func TestRecordable(t *testing.T) {
	// Claude (Read/Edit/Write) and Gemini (read_file/write_file/replace) file
	// tools all record through the file_path branch; apply_patch and non-file
	// tools do not.
	recordable := []Tool{ToolRead, ToolEdit, ToolWrite, ToolReadFile, ToolWriteFile, ToolReplace}
	for _, tool := range recordable {
		if !tool.Recordable() {
			t.Errorf("%s.Recordable() = false, want true", tool)
		}
	}
	for _, tool := range []Tool{ToolApplyPatch, "Bash", "run_shell_command", "glob"} {
		if tool.Recordable() {
			t.Errorf("%s.Recordable() = true, want false", tool)
		}
	}
}

func TestParseHookInput(t *testing.T) {
	t.Run("file_tool", func(t *testing.T) {
		raw := `{"session_id":"s1","cwd":"/proj","tool_name":"Edit","tool_input":{"file_path":"/proj/main.go"}}`
		h, err := ParseHookInput(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if h.SessionID != "s1" || h.CWD != "/proj" || h.ToolName != ToolEdit {
			t.Errorf("unexpected HookInput: %+v", h)
		}
		if got := h.FilePath(); got != "/proj/main.go" {
			t.Errorf("FilePath() = %q, want /proj/main.go", got)
		}
	})

	t.Run("apply_patch_tool", func(t *testing.T) {
		raw := `{"session_id":"s2","cwd":"/proj","tool_name":"apply_patch",` +
			`"tool_input":{"input":"*** Begin Patch\n*** Update File: a.go\n*** End Patch"}}`
		h, err := ParseHookInput(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if h.ToolName != ToolApplyPatch {
			t.Errorf("tool_name = %q, want apply_patch", h.ToolName)
		}
		if h.FilePath() != "" {
			t.Errorf("FilePath() = %q, want empty (apply_patch carries no file_path)", h.FilePath())
		}
		if got := PatchedFiles(h.ToolInput); !equalStrings(got, []string{"a.go"}) {
			t.Errorf("PatchedFiles() = %v, want [a.go]", got)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader("not json"))
		if err == nil {
			t.Error("expected error for malformed JSON")
		}
	})
}

func TestInjectContextText(t *testing.T) {
	t.Run("empty_both", func(t *testing.T) {
		got := InjectContextText(InjectResult{}, InjectResult{}, 5)
		if !strings.Contains(got, "Engram is active") {
			t.Errorf("expected setup message, got %q", got)
		}
	})

	t.Run("identity_section", func(t *testing.T) {
		global := InjectResult{
			Invariants: []Memory{{Key: "name", Content: "Axiom"}},
		}
		got := InjectContextText(global, InjectResult{}, 5)
		if !strings.Contains(got, "## Identity") {
			t.Errorf("missing Identity section in %q", got)
		}
		if !strings.Contains(got, "**name**: Axiom") {
			t.Errorf("missing invariant entry in %q", got)
		}
	})

	t.Run("orientation_header_present_and_first", func(t *testing.T) {
		global := InjectResult{
			Invariants: []Memory{{Key: "codename", Content: "Cadence."}, {Key: "personality", Content: "upbeat"}},
		}
		project := InjectResult{LongTerm: []Memory{{Key: "a", Content: "x"}}}
		got := InjectContextText(global, project, 5)
		if !strings.Contains(got, "## Orientation") {
			t.Errorf("missing orientation header in %q", got)
		}
		if !strings.Contains(got, "Oriented as Cadence.") {
			t.Errorf("codename not surfaced cleanly in orientation: %q", got)
		}
		if !strings.Contains(got, "1 long-term") {
			t.Errorf("orientation missing memory counts: %q", got)
		}
		if oi, ii := strings.Index(got, "## Orientation"), strings.Index(got, "## Identity"); oi < 0 || oi > ii {
			t.Errorf("orientation header should precede identity (orientation=%d identity=%d)", oi, ii)
		}
	})

	t.Run("no_orientation_when_empty", func(t *testing.T) {
		got := InjectContextText(InjectResult{}, InjectResult{}, 5)
		if strings.Contains(got, "## Orientation") {
			t.Errorf("should not emit orientation header when nothing loaded: %q", got)
		}
	})

	t.Run("preferences_section", func(t *testing.T) {
		global := InjectResult{
			Preferences: []Memory{{Key: "style", Content: "no comments"}},
		}
		got := InjectContextText(global, InjectResult{}, 5)
		if !strings.Contains(got, "## Preferences") {
			t.Errorf("missing Preferences section in %q", got)
		}
	})

	t.Run("long_term_over_budget_caps_and_reports_honestly", func(t *testing.T) {
		var lt []Memory
		for i := 0; i < 300; i++ {
			lt = append(lt, Memory{Key: fmt.Sprintf("k%03d", i), Tldr: strings.Repeat("x", 90)})
		}
		got := InjectContextText(InjectResult{LongTerm: lt}, InjectResult{}, 5)
		if !strings.Contains(got, "## Long-term memory (showing ") {
			t.Errorf("expected capped long-term header with a showing-note")
		}
		if !strings.Contains(got, "of 300;") {
			t.Errorf("expected section note to report 'of 300'")
		}
		if !strings.Contains(got, "of 300 long-term") {
			t.Errorf("expected orientation header to say 'N of 300 long-term'")
		}
	})

	t.Run("orientation_includes_culling_nudge_with_read_the_rest", func(t *testing.T) {
		global := InjectResult{LongTerm: []Memory{{Key: "x", Content: "y"}}}
		got := InjectContextText(global, InjectResult{}, 5)
		for _, want := range []string{"fewer entries than", "truncated", "engram mem list", "engram mem read"} {
			if !strings.Contains(got, want) {
				t.Errorf("orientation missing culling-nudge phrase %q", want)
			}
		}
	})

	t.Run("long_term_under_budget_has_no_note", func(t *testing.T) {
		global := InjectResult{LongTerm: []Memory{{Key: "infra", Content: "small"}}}
		got := InjectContextText(global, InjectResult{}, 5)
		if !strings.Contains(got, "## Long-term memory\n") {
			t.Errorf("small long-term should have a plain header, no note: %q", got)
		}
		if strings.Contains(got, "showing") {
			t.Errorf("small long-term should not carry a budget note")
		}
	})

	t.Run("active_areas_over_budget_is_capped_and_noted", func(t *testing.T) {
		var files []string
		for i := 0; i < 250; i++ {
			files = append(files, fmt.Sprintf("dir%03d/f.go", i))
		}
		got := InjectContextText(InjectResult{}, InjectResult{Files: files}, 5)
		if !strings.Contains(got, "## Recently active areas (last 5 sessions) (showing ") {
			t.Errorf("expected a capped active-areas header with a showing-note")
		}
		if !strings.Contains(got, "older activity omitted") {
			t.Errorf("expected the areas note to flag omitted activity")
		}
	})

	t.Run("active_areas_section_rolls_up_directories", func(t *testing.T) {
		project := InjectResult{
			Files: []string{"pkg/a.go", "pkg/b.go", "pkg/c.go", "pkg/d.go", "pkg/e.go"},
		}
		got := InjectContextText(InjectResult{}, project, 3)
		if !strings.Contains(got, "## Recently active areas (last 3 sessions)") {
			t.Errorf("missing active-areas section in %q", got)
		}
		if !strings.Contains(got, "pkg/ ×5") {
			t.Errorf("directory rollup missing in %q", got)
		}
	})

	t.Run("short_term_section", func(t *testing.T) {
		project := InjectResult{
			ShortTerm: []Memory{{Key: "task", Content: "refactor auth"}},
		}
		got := InjectContextText(InjectResult{}, project, 5)
		if !strings.Contains(got, "## Short-term stack") {
			t.Errorf("missing short-term section in %q", got)
		}
	})

	t.Run("automation_review_is_explicitly_a_user_prompt", func(t *testing.T) {
		project := InjectResult{AutomationReview: &AutomationReview{
			Items: []AutomationReviewItem{
				{Candidate: AutomationCandidate{Path: "Makefile", Kind: "task runner"}, State: AutomationNew},
				{Candidate: AutomationCandidate{Path: "scripts/check.sh", Kind: "script"}, State: AutomationChanged,
					Previous: &AutomationCatalogEntry{Classification: AutomationDirectTool, Rationale: "validation gate"}},
			},
		}}
		got := InjectContextText(InjectResult{}, project, 5)
		for _, want := range []string{
			"## Automation catalog review",
			"2 automation catalog entries",
			"changed: scripts/check.sh (script)",
			"previous: direct-tool — validation gate",
			"Run `engram skill discover`",
			"do not execute candidates merely to inspect them",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("automation review missing %q in %q", want, got)
			}
		}
	})

	t.Run("classified_project_tools_and_skill_members_are_scoped", func(t *testing.T) {
		project := InjectResult{
			ProjectTools: []ToolDesc{{Name: "scripts/check.sh", Run: "bash", Path: "scripts/check.sh", Desc: "validation gate"}},
			SkillCandidates: []AutomationCatalogEntry{{
				Path: "scripts/release.sh", Classification: AutomationSkillMember,
				SkillKey: "release", Rationale: "publishes the artifact",
			}},
		}
		got := InjectContextText(InjectResult{}, project, 5)
		for _, want := range []string{
			"## Project tools", "bash scripts/check.sh: validation gate",
			"## Skill candidates", "**release**: scripts/release.sh — publishes the artifact",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("classified automation missing %q in %q", want, got)
			}
		}
	})

	t.Run("global_long_term_surfaced", func(t *testing.T) {
		global := InjectResult{LongTerm: []Memory{{Key: "infra", Content: "global-long-fact"}}}
		got := InjectContextText(global, InjectResult{}, 5)
		if !strings.Contains(got, "## Long-term memory") {
			t.Errorf("global long-term should surface a long-term section: %q", got)
		}
		if !strings.Contains(got, "global-long-fact") {
			t.Errorf("global long-term entry missing: %q", got)
		}
	})

	t.Run("global_short_term_surfaced", func(t *testing.T) {
		global := InjectResult{ShortTerm: []Memory{{Key: "wip", Content: "global-short-state"}}}
		got := InjectContextText(global, InjectResult{}, 5)
		if !strings.Contains(got, "## Short-term stack") {
			t.Errorf("global short-term should surface a short-term section: %q", got)
		}
		if !strings.Contains(got, "global-short-state") {
			t.Errorf("global short-term entry missing: %q", got)
		}
	})

	t.Run("long_term_merges_global_then_project", func(t *testing.T) {
		global := InjectResult{LongTerm: []Memory{{Key: "g", Content: "global-fact"}}}
		project := InjectResult{LongTerm: []Memory{{Key: "p", Content: "project-fact"}}}
		got := InjectContextText(global, project, 5)
		gi, pi := strings.Index(got, "global-fact"), strings.Index(got, "project-fact")
		if gi < 0 || pi < 0 {
			t.Fatalf("both long-term entries should surface: %q", got)
		}
		if gi > pi {
			t.Errorf("global long-term should precede project (global=%d project=%d)", gi, pi)
		}
		if !strings.Contains(got, "2 long-term") {
			t.Errorf("orientation count should sum global+project long-term: %q", got)
		}
	})

	t.Run("agent_tools_section_ignores_project_tools", func(t *testing.T) {
		// Plan B: engram no longer owns project-scoped tools, so even if a caller
		// populates project.AgentTools (e.g. a stale caller not yet updated),
		// InjectContextText must render global tools only.
		global := InjectResult{AgentTools: []ToolDesc{
			{Name: "g.sh", Desc: "global tool", Run: "bash", Path: "/home/u/.engram/agenttools/g.sh"},
		}}
		project := InjectResult{AgentTools: []ToolDesc{
			{Name: "render.sh", Desc: "Render it.", Run: "bash", Path: "context/agenttools/render.sh"},
		}}
		got := InjectContextText(global, project, 5)
		if !strings.Contains(got, "## Agent tools") {
			t.Errorf("missing agent tools section in %q", got)
		}
		if !strings.Contains(got, "- bash /home/u/.engram/agenttools/g.sh: global tool") {
			t.Errorf("missing global tool command in %q", got)
		}
		if strings.Contains(got, "render.sh") {
			t.Errorf("project.AgentTools must not surface any longer (project tool staging is removed): %q", got)
		}
	})

	t.Run("no_staged_tool_candidates_section", func(t *testing.T) {
		global := InjectResult{AgentTools: []ToolDesc{
			{Name: "g.sh", Desc: "global tool", Run: "bash", Path: "/home/u/.engram/agenttools/g.sh"},
		}}
		got := InjectContextText(global, InjectResult{}, 5)
		if strings.Contains(got, "## Staged tool candidates") {
			t.Errorf("staged tool candidates section should no longer be emitted (project tool staging is removed): %q", got)
		}
		if !strings.Contains(got, "## Agent tools") {
			t.Errorf("global tools should still surface under Agent tools: %q", got)
		}
	})
}

func TestAgentLayerKeys(t *testing.T) {
	key, err := AgentLayerKey("Codex", "personality")
	if err != nil {
		t.Fatal(err)
	}
	if key != "agent/codex/personality" {
		t.Fatalf("AgentLayerKey = %q, want agent/codex/personality", key)
	}
	agent, base, ok := ParseAgentLayerKey(key)
	if !ok || agent != "codex" || base != "personality" {
		t.Fatalf("ParseAgentLayerKey = %q, %q, %v", agent, base, ok)
	}
	if got := MemoryLabel(Memory{Tier: TierInvariant, Key: key}); got != "[invariant/personality @codex]" {
		t.Fatalf("MemoryLabel = %q", got)
	}
}

func TestInjectWithAgentLayers(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, m := range []Memory{
		{Tier: TierInvariant, Key: "personality", Content: "base personality"},
		{Tier: TierPreference, Key: "style", Content: "base style"},
		{Tier: TierInvariant, Key: "agent/codex/personality", Content: "codex personality"},
		{Tier: TierPreference, Key: "agent/codex/style", Content: "codex style"},
		{Tier: TierPreference, Key: "agent/gemini/style", Content: "gemini style"},
	} {
		if err := WriteMemory(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}

	primary, err := InjectWithAgent(ctx, db, 5, "")
	if err != nil {
		t.Fatal(err)
	}
	got := InjectContextText(primary, InjectResult{}, 5)
	if strings.Contains(got, "codex style") || strings.Contains(got, "gemini style") {
		t.Fatalf("unscoped inject leaked agent layer:\n%s", got)
	}
	if !strings.Contains(got, "base personality") || !strings.Contains(got, "base style") {
		t.Fatalf("unscoped inject lost primary guidance:\n%s", got)
	}

	codex, err := InjectWithAgent(ctx, db, 5, "codex")
	if err != nil {
		t.Fatal(err)
	}
	got = InjectContextText(codex, InjectResult{}, 5)
	if !strings.Contains(got, "## Agent layer (codex)") || !strings.Contains(got, "codex personality") || !strings.Contains(got, "codex style") {
		t.Fatalf("codex layer missing:\n%s", got)
	}
	if strings.Contains(got, "gemini style") {
		t.Fatalf("codex inject leaked gemini layer:\n%s", got)
	}
}

func TestListMemoriesForViewAgentLayer(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	for _, m := range []Memory{
		{Tier: TierInvariant, Key: "personality", Content: "base personality"},
		{Tier: TierInvariant, Key: "agent/codex/personality", Content: "codex personality"},
		{Tier: TierInvariant, Key: "agent/gemini/personality", Content: "gemini personality"},
	} {
		if err := WriteMemory(ctx, db, m); err != nil {
			t.Fatal(err)
		}
	}

	all, err := ListMemoriesForView(ctx, db, []Tier{TierInvariant}, "", "personality")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all visible memories = %d, want 3: %+v", len(all), all)
	}

	codex, err := ListMemoriesForView(ctx, db, []Tier{TierInvariant}, "codex", "personality")
	if err != nil {
		t.Fatal(err)
	}
	if len(codex) != 2 {
		t.Fatalf("codex visible memories = %d, want primary + codex layer: %+v", len(codex), codex)
	}
	for _, m := range codex {
		if strings.Contains(m.Key, "gemini") {
			t.Fatalf("codex view leaked gemini layer: %+v", codex)
		}
	}
}

func TestFormatStatusLine(t *testing.T) {
	cases := []struct {
		name        string
		codename    string
		project     string
		long, short int
		want        string
	}{
		{"in_project", "Cadence.", "engram", 9, 0, "Cadence · engram · 9 long · 0 short"},
		{"in_project_with_short", "Cadence", "engram", 2, 3, "Cadence · engram · 2 long · 3 short"},
		{"no_codename_falls_back", "", "engram", 1, 0, "engram · engram · 1 long · 0 short"},
		{"outside_project_with_short", "Cadence", "", 0, 3, "Cadence · 3 short"},
		{"outside_project_clean", "Cadence", "", 0, 0, "Cadence"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FormatStatusLine(c.codename, c.project, c.long, c.short)
			if got != c.want {
				t.Errorf("FormatStatusLine(%q, %q, %d, %d) = %q, want %q",
					c.codename, c.project, c.long, c.short, got, c.want)
			}
		})
	}
}

func TestMemoryMDRoundTrip(t *testing.T) {
	original := []Memory{
		{Tier: TierLong, Key: "alpha", Content: "content alpha", Tldr: "the alpha summary", Trigger: "When alpha work is requested"},
		{Tier: TierLong, Key: "beta", Content: "multi\nline\ncontent"},
		{Tier: TierLong, Key: "gamma", Content: "  trimmed  "},
	}
	formatted := FormatMemoryMD(TierLong, original)
	parsed, err := ParseMemoryMD(TierLong, formatted)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(original) {
		t.Fatalf("got %d memories, want %d", len(parsed), len(original))
	}
	for i, m := range parsed {
		if m.Tier != TierLong {
			t.Errorf("[%d] tier %q, want long", i, m.Tier)
		}
		if m.Key != original[i].Key {
			t.Errorf("[%d] key %q, want %q", i, m.Key, original[i].Key)
		}
		wantContent := strings.TrimSpace(original[i].Content)
		if m.Content != wantContent {
			t.Errorf("[%d] content %q, want %q", i, m.Content, wantContent)
		}
		if m.Tldr != original[i].Tldr {
			t.Errorf("[%d] tldr %q, want %q", i, m.Tldr, original[i].Tldr)
		}
		if m.Trigger != original[i].Trigger {
			t.Errorf("[%d] trigger %q, want %q", i, m.Trigger, original[i].Trigger)
		}
	}
}

// A markdown file predating the tldr column (no tldr comment) must still parse,
// leaving Tldr empty rather than swallowing the first content line.
func TestParseMemoryMDWithoutTldrIsBackwardCompatible(t *testing.T) {
	legacy := "# Long\n\n## alpha\ncontent alpha\n\n## beta\nfirst line\nsecond line\n"
	parsed, err := ParseMemoryMD(TierLong, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("got %d memories, want 2", len(parsed))
	}
	for _, m := range parsed {
		if m.Tldr != "" {
			t.Errorf("entry %q got tldr %q, want empty for a legacy file", m.Key, m.Tldr)
		}
	}
	if parsed[0].Content != "content alpha" {
		t.Errorf("alpha content = %q, want %q", parsed[0].Content, "content alpha")
	}
	if parsed[1].Content != "first line\nsecond line" {
		t.Errorf("beta content = %q, want full body", parsed[1].Content)
	}
}

func TestParseMemoryMDEmpty(t *testing.T) {
	parsed, err := ParseMemoryMD(TierShort, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 0 {
		t.Errorf("got %d memories from empty input, want 0", len(parsed))
	}
}

func TestRollupFiles(t *testing.T) {
	t.Run("directory_under_threshold_is_one_counted_line", func(t *testing.T) {
		files := []string{"pkg/a.go", "pkg/b.go", "pkg/c.go"}
		got := rollupFiles(files, 10, 0)
		want := []string{"pkg/ ×3"}
		if !equalStrings(got, want) {
			t.Errorf("rollupFiles = %v, want %v", got, want)
		}
	})

	t.Run("over_threshold_splits_one_level_deeper_in_recency_order", func(t *testing.T) {
		// pkg holds 11 files (>10), so it must not render as "pkg/ ×11"; it
		// splits into its subdirs. The most-recent file is under pkg/y, so the
		// pkg/y bucket sorts ahead of pkg/x.
		files := []string{
			"pkg/y/1.go", // most recent
			"pkg/x/1.go", "pkg/x/2.go", "pkg/x/3.go",
			"pkg/x/4.go", "pkg/x/5.go", "pkg/x/6.go",
			"pkg/y/2.go", "pkg/y/3.go", "pkg/y/4.go", "pkg/y/5.go",
		}
		got := rollupFiles(files, 10, 0)
		want := []string{"pkg/y/ ×5", "pkg/x/ ×6"}
		if !equalStrings(got, want) {
			t.Errorf("rollupFiles = %v, want %v", got, want)
		}
	})

	t.Run("direct_files_of_a_split_dir_form_a_residual_line", func(t *testing.T) {
		// pkg has 12 touched files: 4 sit directly in pkg/, 8 under pkg/sub/. The
		// direct ones can't be pushed deeper, so they aggregate into one residual
		// "pkg/ ×4" line rather than being enumerated.
		files := []string{
			"pkg/root1.go", // most recent -> residual sorts first
			"pkg/sub/a.go", "pkg/sub/b.go", "pkg/sub/c.go", "pkg/sub/d.go",
			"pkg/sub/e.go", "pkg/sub/f.go", "pkg/sub/g.go", "pkg/sub/h.go",
			"pkg/root2.go", "pkg/root3.go", "pkg/root4.go",
		}
		got := rollupFiles(files, 10, 0)
		want := []string{"pkg/ ×4", "pkg/sub/ ×8"}
		if !equalStrings(got, want) {
			t.Errorf("rollupFiles = %v, want %v", got, want)
		}
	})

	t.Run("recurses_past_one_level_when_a_subdir_is_still_over_threshold", func(t *testing.T) {
		// Everything is under a/b/, and a/b/ itself holds 11 files, so resolution
		// has to keep descending to a/b/c and a/b/d.
		files := []string{
			"a/b/c/1.go", "a/b/c/2.go", "a/b/c/3.go", "a/b/c/4.go",
			"a/b/c/5.go", "a/b/c/6.go",
			"a/b/d/1.go", "a/b/d/2.go", "a/b/d/3.go", "a/b/d/4.go", "a/b/d/5.go",
		}
		got := rollupFiles(files, 10, 0)
		want := []string{"a/b/c/ ×6", "a/b/d/ ×5"}
		if !equalStrings(got, want) {
			t.Errorf("rollupFiles = %v, want %v", got, want)
		}
	})

	t.Run("small_bucket_expands_to_filenames", func(t *testing.T) {
		// With expandThreshold 3, a directory at or below 3 touched files lists
		// them by name; a larger one keeps the count.
		files := []string{"x/a.go", "x/b.go", "y/1.go", "y/2.go", "y/3.go", "y/4.go"}
		got := rollupFiles(files, 10, 3)
		want := []string{"x/a.go", "x/b.go", "y/ ×4"}
		if !equalStrings(got, want) {
			t.Errorf("rollupFiles = %v, want %v", got, want)
		}
	})

	t.Run("root_level_files_do_not_render_a_bare_slash", func(t *testing.T) {
		files := []string{"README.md", "LICENSE"}
		got := rollupFiles(files, 10, 0)
		want := []string{"./ ×2"}
		if !equalStrings(got, want) {
			t.Errorf("rollupFiles = %v, want %v", got, want)
		}
	})
}

func TestBudgetLines(t *testing.T) {
	lines := []string{"aaaa", "bbbb", "cccc"} // joined as "aaaa\nbbbb\ncccc" = 14 chars

	t.Run("all_fit_under_budget", func(t *testing.T) {
		kept, shown := budgetLines(lines, 100)
		if shown != 3 || !equalStrings(kept, lines) {
			t.Errorf("budgetLines = %v (shown %d), want all 3", kept, shown)
		}
	})

	t.Run("non_positive_budget_means_unlimited", func(t *testing.T) {
		kept, shown := budgetLines(lines, 0)
		if shown != 3 || !equalStrings(kept, lines) {
			t.Errorf("budgetLines = %v (shown %d), want all 3 unbounded", kept, shown)
		}
	})

	t.Run("keeps_the_prefix_that_fits_and_drops_the_stale_tail", func(t *testing.T) {
		// budget 9: "aaaa"(4) + "\nbbbb"(5) = 9 fits; adding "\ncccc" (5) would hit 14.
		kept, shown := budgetLines(lines, 9)
		want := []string{"aaaa", "bbbb"}
		if shown != 2 || !equalStrings(kept, want) {
			t.Errorf("budgetLines = %v (shown %d), want %v", kept, shown, want)
		}
	})
}

func TestCountPhrase(t *testing.T) {
	t.Run("plain_count_when_nothing_dropped", func(t *testing.T) {
		if got := countPhrase(91, 91, "long-term"); got != "91 long-term" {
			t.Errorf("countPhrase = %q, want %q", got, "91 long-term")
		}
	})
	t.Run("shows_shown_of_total_when_capped", func(t *testing.T) {
		if got := countPhrase(42, 91, "long-term"); got != "42 of 91 long-term" {
			t.Errorf("countPhrase = %q, want %q", got, "42 of 91 long-term")
		}
	})
}

func TestBudgetNote(t *testing.T) {
	t.Run("empty_when_nothing_dropped", func(t *testing.T) {
		if got := budgetNote(91, 91, "prune it"); got != "" {
			t.Errorf("budgetNote = %q, want empty", got)
		}
	})
	t.Run("wraps_shown_of_total_around_the_remedy", func(t *testing.T) {
		got := budgetNote(42, 91, "prune with `engram mem list`")
		if !strings.Contains(got, "showing 42 of 91") || !strings.Contains(got, "prune with `engram mem list`") {
			t.Errorf("budgetNote = %q, want showing-of-total plus remedy", got)
		}
	})
}

// --- DB tests ---

func TestRecord(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	err := Record(ctx, db, Event{
		SessionID: "s1",
		TS:        1000,
		Tool:      ToolRead,
		FilePath:  "main.go",
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Inject(ctx, db, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0] != "main.go" {
		t.Errorf("Files = %v, want [main.go]", result.Files)
	}
}

func TestInjectRecentFiles(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// File touches from every tool that records -- Claude's Read/Edit/Write and
	// Codex's apply_patch -- all surface as recently active files, ordered most
	// recent first.
	Record(ctx, db, Event{SessionID: "s1", TS: 1000, Tool: ToolRead, FilePath: "foo.go"})
	Record(ctx, db, Event{SessionID: "s1", TS: 2000, Tool: ToolEdit, FilePath: "bar.go"})
	Record(ctx, db, Event{SessionID: "s1", TS: 3000, Tool: ToolApplyPatch, FilePath: "baz.go"})

	result, err := Inject(ctx, db, 5)
	if err != nil {
		t.Fatal(err)
	}

	if !equalStrings(result.Files, []string{"baz.go", "bar.go", "foo.go"}) {
		t.Errorf("Files = %v, want [baz.go bar.go foo.go]", result.Files)
	}
}

func TestInjectSessionLimit(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	for i, sess := range []string{"s1", "s2", "s3"} {
		Record(ctx, db, Event{
			SessionID: sess,
			TS:        int64(i+1) * 1000,
			Tool:      ToolRead,
			FilePath:  sess + ".go",
		})
	}

	// Ask for only 2 sessions — s2 and s3 are most recent
	result, err := Inject(ctx, db, 2)
	if err != nil {
		t.Fatal(err)
	}
	fileSet := map[string]bool{}
	for _, f := range result.Files {
		fileSet[f] = true
	}
	if fileSet["s1.go"] {
		t.Error("s1.go should be outside the 2-session window")
	}
	if !fileSet["s2.go"] || !fileSet["s3.go"] {
		t.Errorf("Files = %v, want s2.go and s3.go", result.Files)
	}
}

func TestPrune(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	for i, sess := range []string{"s1", "s2", "s3"} {
		Record(ctx, db, Event{
			SessionID: sess,
			TS:        int64(i+1) * 1000,
			Tool:      ToolRead,
			FilePath:  sess + ".go",
		})
	}

	n, err := Prune(ctx, db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Prune deleted %d rows, want 1", n)
	}

	result, err := Inject(ctx, db, 10)
	if err != nil {
		t.Fatal(err)
	}
	fileSet := map[string]bool{}
	for _, f := range result.Files {
		fileSet[f] = true
	}
	if fileSet["s1.go"] {
		t.Error("s1.go should have been pruned")
	}
	if !fileSet["s2.go"] || !fileSet["s3.go"] {
		t.Errorf("Files = %v, want s2.go and s3.go after pruning", result.Files)
	}
}

func TestPruneKeepAll(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	Record(ctx, db, Event{SessionID: "s1", TS: 1000, Tool: ToolRead, FilePath: "f.go"})

	n, err := Prune(ctx, db, 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("Prune deleted %d rows, want 0", n)
	}
}

func TestWriteMemoryUpsert(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "foo", Content: "original"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "foo", Content: "updated"}); err != nil {
		t.Fatal(err)
	}

	m, err := ReadMemory(ctx, db, TierShort, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("memory not found")
	}
	if m.Content != "updated" {
		t.Errorf("Content = %q, want %q", m.Content, "updated")
	}

	all, err := ListMemories(ctx, db, TierShort)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("got %d memories, want 1 (upsert must not duplicate)", len(all))
	}
}

func TestReadMemoryNotFound(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	m, err := ReadMemory(ctx, db, TierShort, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Error("expected nil for missing key")
	}
}

func TestDeleteMemory(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "k", Content: "v"})

	if err := DeleteMemory(ctx, db, TierLong, "k"); err != nil {
		t.Fatal(err)
	}
	m, _ := ReadMemory(ctx, db, TierLong, "k")
	if m != nil {
		t.Error("memory should be deleted")
	}
}

func TestDeleteMemoryNotFound(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	err := DeleteMemory(ctx, db, TierShort, "nonexistent")
	if err == nil {
		t.Error("expected error deleting nonexistent memory")
	}
}

func TestFindMemoryByKey(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "shared", Content: "long value"})
	WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "shared", Content: "short value"})
	WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "other", Content: "other"})

	matches, err := FindMemoryByKey(ctx, db, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Errorf("got %d matches, want 2", len(matches))
	}
	tiers := map[Tier]bool{}
	for _, m := range matches {
		tiers[m.Tier] = true
	}
	if !tiers[TierLong] || !tiers[TierShort] {
		t.Errorf("expected both long and short tiers, got %v", tiers)
	}
}

func TestMoveMemory(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "decision", Content: "use SQLite"})

	if err := MoveMemory(ctx, db, "decision", TierShort, TierLong); err != nil {
		t.Fatal(err)
	}

	m, err := ReadMemory(ctx, db, TierLong, "decision")
	if err != nil || m == nil {
		t.Fatalf("not found in long tier: %v", err)
	}
	if m.Content != "use SQLite" {
		t.Errorf("Content = %q, want %q", m.Content, "use SQLite")
	}

	gone, _ := ReadMemory(ctx, db, TierShort, "decision")
	if gone != nil {
		t.Error("memory should be gone from short tier after move")
	}
}

func TestMoveMemoryNotFound(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	err := MoveMemory(ctx, db, "ghost", TierShort, TierLong)
	if err == nil {
		t.Error("expected error moving nonexistent memory")
	}
}

func TestPopMemory(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "first", Content: "c1", TS: 1000})
	WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "second", Content: "c2", TS: 2000})

	m, err := PopMemory(ctx, db, TierShort)
	if err != nil || m == nil {
		t.Fatalf("first pop failed: %v", err)
	}
	if m.Key != "second" {
		t.Errorf("first pop key = %q, want second (most recent)", m.Key)
	}

	m, err = PopMemory(ctx, db, TierShort)
	if err != nil || m == nil {
		t.Fatalf("second pop failed: %v", err)
	}
	if m.Key != "first" {
		t.Errorf("second pop key = %q, want first", m.Key)
	}

	m, err = PopMemory(ctx, db, TierShort)
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Error("pop on empty tier should return nil")
	}
}

func TestListMemories(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "a", Content: "1"})
	WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "b", Content: "2"})
	WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "c", Content: "3"})

	long, err := ListMemories(ctx, db, TierLong)
	if err != nil {
		t.Fatal(err)
	}
	if len(long) != 2 {
		t.Errorf("long tier: got %d memories, want 2", len(long))
	}

	short, err := ListMemories(ctx, db, TierShort)
	if err != nil {
		t.Fatal(err)
	}
	if len(short) != 1 {
		t.Errorf("short tier: got %d memories, want 1", len(short))
	}
}

func TestSearchMemories(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "auth", Content: "authentication uses JWT tokens"})
	WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "db", Content: "database uses PostgreSQL"})
	WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "task", Content: "working on JWT refresh logic"})

	results, err := SearchMemories(ctx, db, "JWT", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}

	results, err = SearchMemories(ctx, db, "JWT", TierLong)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results filtered to long, want 1", len(results))
	}
	if results[0].Key != "auth" {
		t.Errorf("got key %q, want auth", results[0].Key)
	}

	results, err = SearchMemories(ctx, db, "PostgreSQL", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Key != "db" {
		t.Errorf("got %v, want [db]", results)
	}
}

func TestSkillsAreTriggerBearingLongTermMemories(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	skill := Memory{
		Tier:    TierLong,
		Key:     "standup-report",
		Content: "Gather work, draft the narrative, then post it.",
		Tldr:    "Prepare and post a concise standup",
		Trigger: "When the user asks to prepare or post a standup report",
	}
	if err := WriteMemory(ctx, db, skill); err != nil {
		t.Fatal(err)
	}
	if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "standup-format", Content: "A settled formatting decision"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "bad", Content: "no", Trigger: "When bad"}); err == nil {
		t.Fatal("trigger-bearing short memory should be rejected")
	}

	skills, err := ListSkills(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Key != skill.Key {
		t.Fatalf("ListSkills = %+v, want only %q", skills, skill.Key)
	}
	for _, query := range []string{"standup", "concise"} {
		hits, err := SearchSkills(ctx, db, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].Key != skill.Key {
			t.Errorf("SearchSkills(%q) = %+v, want %q", query, hits, skill.Key)
		}
	}
}

func TestInjectRendersScopedSkillTriggerIndex(t *testing.T) {
	global := InjectResult{LongTerm: []Memory{{
		Key: "standup", Content: "full global instructions", Tldr: "Prepare the report", Trigger: "When asked for a standup",
	}}}
	project := InjectResult{LongTerm: []Memory{{
		Key: "release", Content: "full project instructions", Tldr: "Ship safely", Trigger: "When asked to release",
	}}}
	got := InjectContextText(global, project, 5)
	for _, want := range []string{
		"## Skills (trigger index",
		"When asked for a standup",
		"engram skill read -g standup",
		"When asked to release",
		"engram skill read release",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill index missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "full global instructions") || strings.Contains(got, "full project instructions") {
		t.Errorf("skill index leaked full instructions: %q", got)
	}
}

func TestInjectIncludesMemories(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "arch", Content: "use SQLite"})
	WriteMemory(ctx, db, Memory{Tier: TierShort, Key: "task", Content: "in progress"})

	result, err := Inject(ctx, db, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.LongTerm) != 1 || result.LongTerm[0].Key != "arch" {
		t.Errorf("LongTerm = %v, want [{arch}]", result.LongTerm)
	}
	if len(result.ShortTerm) != 1 || result.ShortTerm[0].Key != "task" {
		t.Errorf("ShortTerm = %v, want [{task}]", result.ShortTerm)
	}
}

// --- filesystem tests ---

func TestFindProjectRoot(t *testing.T) {
	t.Run("engram_marker", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".engram"), 0755); err != nil {
			t.Fatal(err)
		}
		got, err := FindProjectRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !samePath(got, dir) {
			t.Errorf("got %q, want %q", got, dir)
		}
	})

	t.Run("git_marker", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
		got, err := FindProjectRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !samePath(got, dir) {
			t.Errorf("got %q, want %q", got, dir)
		}
	})

	t.Run("walks_up", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(dir, "a", "b", "c")
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		got, err := FindProjectRoot(sub)
		if err != nil {
			t.Fatal(err)
		}
		if !samePath(got, dir) {
			t.Errorf("got %q, want %q", got, dir)
		}
	})

	t.Run("engram_beats_git", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(dir, "inner")
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(sub, ".engram"), 0755); err != nil {
			t.Fatal(err)
		}
		got, err := FindProjectRoot(sub)
		if err != nil {
			t.Fatal(err)
		}
		if !samePath(got, sub) {
			t.Errorf("got %q, want %q (inner .engram should win)", got, sub)
		}
	})

	t.Run("no_marker", func(t *testing.T) {
		// Use a fresh subdirectory with no markers anywhere up the chain
		// that we control. We can't prevent the test runner's own markers
		// from being found, so instead we verify the function returns either
		// an error or a path that exists.
		dir := t.TempDir()
		sub := filepath.Join(dir, "deep", "sub")
		os.MkdirAll(sub, 0755)
		_, err := FindProjectRoot(sub)
		// This may or may not error depending on whether /tmp has a VCS root.
		// We just verify it doesn't panic and the error case is exercised elsewhere.
		_ = err
	})
}

func TestFindProjectRootClaudeMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	got, err := FindProjectRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(got, dir) {
		t.Errorf("got %q, want %q", got, dir)
	}
}

// --- tldr ---

func TestWriteMemoryTldr(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	t.Run("round_trip", func(t *testing.T) {
		if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "k", Content: "full text", Tldr: "the summary"}); err != nil {
			t.Fatal(err)
		}
		m, err := ReadMemory(ctx, db, TierLong, "k")
		if err != nil || m == nil {
			t.Fatalf("read: %v", err)
		}
		if m.Tldr != "the summary" {
			t.Errorf("Tldr = %q, want %q", m.Tldr, "the summary")
		}
	})

	t.Run("limit_counts_characters_not_bytes", func(t *testing.T) {
		// "é" is two bytes but one character. MaxTldrLen of them is at the ceiling
		// (400 bytes, MaxTldrLen runes) and must be accepted; one more is rejected.
		// A byte limit would wrongly reject the first case.
		atMax := strings.Repeat("é", MaxTldrLen)
		if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "atmax", Tldr: atMax}); err != nil {
			t.Errorf("tldr at exactly MaxTldrLen characters should be accepted, got %v", err)
		}
		overMax := strings.Repeat("é", MaxTldrLen+1)
		if err := WriteMemory(ctx, db, Memory{Tier: TierLong, Key: "over", Tldr: overMax}); err == nil {
			t.Error("tldr over MaxTldrLen characters should be rejected")
		}
	})
}

func TestInjectSummary(t *testing.T) {
	t.Run("prefers_tldr_over_content", func(t *testing.T) {
		m := Memory{Content: "the whole long content", Tldr: "short summary"}
		if got := m.InjectSummary(); got != "short summary" {
			t.Errorf("InjectSummary = %q, want tldr %q", got, "short summary")
		}
	})
	t.Run("falls_back_to_first_line", func(t *testing.T) {
		m := Memory{Content: "first line\nsecond line\nthird"}
		if got := m.InjectSummary(); got != "first line" {
			t.Errorf("InjectSummary = %q, want first line only", got)
		}
	})
	t.Run("truncates_to_budget", func(t *testing.T) {
		m := Memory{Content: strings.Repeat("x", MaxTldrLen+50)}
		got := m.InjectSummary()
		if n := utf8.RuneCountInString(got); n > MaxTldrLen {
			t.Errorf("summary is %d chars, want <= %d", n, MaxTldrLen)
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("truncated summary should end with an ellipsis, got %q", got)
		}
	})
}

func TestMoveMemoryAcrossDB(t *testing.T) {
	ctx := context.Background()
	src := testDB(t)
	dst := testDB(t)

	// A global claude-layer preference relocating into a project, de-scoped to its
	// base key (projects have no layers). Content and tldr must survive.
	if err := WriteMemory(ctx, src, Memory{Tier: TierPreference, Key: "agent/claude/worktree", Content: "the rule", Tldr: "worktree-only edits"}); err != nil {
		t.Fatal(err)
	}
	if err := MoveMemoryAcrossDB(ctx, src, dst, "agent/claude/worktree", "worktree", TierPreference, TierPreference); err != nil {
		t.Fatal(err)
	}

	if gone, _ := ReadMemory(ctx, src, TierPreference, "agent/claude/worktree"); gone != nil {
		t.Error("source entry should be gone after cross-DB move")
	}
	got, err := ReadMemory(ctx, dst, TierPreference, "worktree")
	if err != nil || got == nil {
		t.Fatalf("destination entry missing: %v", err)
	}
	if got.Content != "the rule" || got.Tldr != "worktree-only edits" {
		t.Errorf("moved entry = {content:%q tldr:%q}, want content+tldr preserved", got.Content, got.Tldr)
	}
}

func TestMoveMemoryAcrossDBNotFound(t *testing.T) {
	ctx := context.Background()
	src := testDB(t)
	dst := testDB(t)
	if err := MoveMemoryAcrossDB(ctx, src, dst, "ghost", "ghost", TierShort, TierLong); err == nil {
		t.Error("expected error moving a nonexistent memory across databases")
	}
}

// A v3 DB has a memories table without a tldr column. Migrating to v4 must add it,
// preserve every row (with tldr defaulting to empty), and keep full-text search
// working over the rebuilt table.
func TestSchemaMigrationV3ToV4AddsTldr(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Recreate the v3 memories shape (no tldr): rebuild the table without the
	// column, restore its indexes and FTS triggers, then stamp the DB back to v3.
	v3setup := []string{
		`DROP TRIGGER IF EXISTS memories_ai`,
		`DROP TRIGGER IF EXISTS memories_ad`,
		`DROP TRIGGER IF EXISTS memories_au`,
		`DROP TABLE IF EXISTS memories_fts`,
		`CREATE TABLE memories_v3 (
		    id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, tier TEXT NOT NULL,
		    key TEXT NOT NULL, content TEXT NOT NULL DEFAULT '', session_id TEXT)`,
		`INSERT INTO memories_v3 (id, ts, tier, key, content, session_id)
		    SELECT id, ts, tier, key, content, session_id FROM memories`,
		`DROP TABLE memories`,
		`ALTER TABLE memories_v3 RENAME TO memories`,
		`CREATE UNIQUE INDEX idx_memories_tier_key ON memories (tier, key)`,
		`CREATE INDEX idx_memories_tier_ts ON memories (tier, ts DESC)`,
		`CREATE VIRTUAL TABLE memories_fts USING fts5(
		    key, content, content='memories', content_rowid='id')`,
		`CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
		    INSERT INTO memories_fts(rowid, key, content) VALUES (new.id, new.key, new.content);
		 END`,
		`CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
		    INSERT INTO memories_fts(memories_fts, rowid, key, content) VALUES ('delete', old.id, old.key, old.content);
		 END`,
		`CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
		    INSERT INTO memories_fts(memories_fts, rowid, key, content) VALUES ('delete', old.id, old.key, old.content);
		    INSERT INTO memories_fts(rowid, key, content) VALUES (new.id, new.key, new.content);
		 END`,
		`INSERT INTO memories (id, ts, tier, key, content, session_id) VALUES (1, 100, 'long', 'infra', 'stateless decomposition wins', NULL)`,
		`INSERT INTO memories_fts(memories_fts) VALUES ('rebuild')`,
		`PRAGMA user_version = 3`,
	}
	for _, stmt := range v3setup {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("set up v3 state (%q): %v", stmt, err)
		}
	}

	if err := applyMigrations(ctx, db); err != nil {
		t.Fatalf("applyMigrations v3->: %v", err)
	}

	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d, want %d", v, schemaVersion)
	}

	var tldrCols int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name = 'tldr'`).Scan(&tldrCols); err != nil {
		t.Fatal(err)
	}
	if tldrCols != 1 {
		t.Error("tldr column missing after migration")
	}
	var triggerCols int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name = 'trigger'`).Scan(&triggerCols); err != nil {
		t.Fatal(err)
	}
	if triggerCols != 1 {
		t.Error("trigger column missing after migration")
	}

	// The row survives, with tldr defaulting to empty.
	m, err := ReadMemory(ctx, db, TierLong, "infra")
	if err != nil || m == nil {
		t.Fatalf("row lost across migration: %v", err)
	}
	if m.Content != "stateless decomposition wins" || m.Tldr != "" || m.Trigger != "" {
		t.Errorf("migrated row = {content:%q tldr:%q trigger:%q}, want content preserved and metadata empty", m.Content, m.Tldr, m.Trigger)
	}

	// Full-text search still works over the rebuilt table.
	hits, err := SearchMemories(ctx, db, "decomposition", TierLong)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Key != "infra" {
		t.Errorf("FTS after migration returned %v, want the single 'infra' row", hits)
	}
}

func TestInjectContextTextTldrAndProjectPreferences(t *testing.T) {
	t.Run("preferences_merge_global_then_project", func(t *testing.T) {
		global := InjectResult{Preferences: []Memory{{Key: "g", Content: "global pref"}}}
		project := InjectResult{Preferences: []Memory{{Key: "p", Content: "project pref"}}}
		got := InjectContextText(global, project, 5)
		gi, pi := strings.Index(got, "global pref"), strings.Index(got, "project pref")
		if gi < 0 || pi < 0 {
			t.Fatalf("both preferences should surface: %q", got)
		}
		if gi > pi {
			t.Errorf("global preference should precede project (global=%d project=%d)", gi, pi)
		}
		if !strings.Contains(got, "2 preferences") {
			t.Errorf("orientation count should sum global+project preferences: %q", got)
		}
	})

	t.Run("non_identity_tiers_show_tldr_not_full_content", func(t *testing.T) {
		global := InjectResult{
			LongTerm: []Memory{{Key: "k", Content: "THE ENTIRE LONG BODY", Tldr: "the one-liner"}},
		}
		got := InjectContextText(global, InjectResult{}, 5)
		if !strings.Contains(got, "the one-liner") {
			t.Errorf("long-term should surface its tldr: %q", got)
		}
		if strings.Contains(got, "THE ENTIRE LONG BODY") {
			t.Errorf("long-term should surface only the tldr, not full content: %q", got)
		}
	})

	t.Run("identity_renders_full_even_with_tldr", func(t *testing.T) {
		global := InjectResult{
			Invariants: []Memory{{Key: "personality", Content: "THE FULL PERSONALITY PROSE", Tldr: "brief"}},
		}
		got := InjectContextText(global, InjectResult{}, 5)
		if !strings.Contains(got, "THE FULL PERSONALITY PROSE") {
			t.Errorf("identity must render in full regardless of tldr: %q", got)
		}
	})
}

func TestSetMemoryTldr(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	if err := WriteMemory(ctx, db, Memory{Tier: TierPreference, Key: "k", Content: "line one\nline two", TS: 100}); err != nil {
		t.Fatal(err)
	}

	// Before: no tldr -> InjectSummary falls back to the first line of content.
	m, err := ReadMemory(ctx, db, TierPreference, "k")
	if err != nil || m == nil {
		t.Fatalf("read: %v", err)
	}
	if got := m.InjectSummary(); got != "line one" {
		t.Fatalf("pre-tldr summary = %q, want first line", got)
	}

	// Set a tldr: content and ts untouched, summary switches to the tldr.
	ok, err := SetMemoryTldr(ctx, db, TierPreference, "k", "the gist")
	if err != nil || !ok {
		t.Fatalf("set tldr: ok=%v err=%v", ok, err)
	}
	m, _ = ReadMemory(ctx, db, TierPreference, "k")
	if m.Tldr != "the gist" {
		t.Errorf("tldr = %q, want %q", m.Tldr, "the gist")
	}
	if m.Content != "line one\nline two" {
		t.Errorf("content changed: %q", m.Content)
	}
	if m.TS != 100 {
		t.Errorf("ts = %d, want 100 (a tldr edit must not touch the timestamp)", m.TS)
	}
	if got := m.InjectSummary(); got != "the gist" {
		t.Errorf("summary = %q, want the tldr", got)
	}

	// Clearing falls back to the first line again.
	if ok, err := SetMemoryTldr(ctx, db, TierPreference, "k", ""); err != nil || !ok {
		t.Fatalf("clear tldr: ok=%v err=%v", ok, err)
	}
	m, _ = ReadMemory(ctx, db, TierPreference, "k")
	if m.Tldr != "" {
		t.Errorf("tldr not cleared: %q", m.Tldr)
	}

	// A missing key reports ok=false with no error (never creates a row).
	if ok, err := SetMemoryTldr(ctx, db, TierPreference, "nope", "x"); err != nil || ok {
		t.Errorf("missing key: ok=%v err=%v, want false/nil", ok, err)
	}

	// An over-length tldr is rejected by validation.
	if _, err := SetMemoryTldr(ctx, db, TierPreference, "k", strings.Repeat("x", MaxTldrLen+1)); err == nil {
		t.Error("expected validation error for over-length tldr")
	}
}
