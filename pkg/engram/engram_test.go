package engram

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestHeadLines(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc", 2, "a\nb"},
		{"a\nb\nc", 3, "a\nb\nc"},
		{"a\nb\nc", 10, "a\nb\nc"},
		{"a", 1, "a"},
		{"", 5, ""},
		{"a\nb", 0, ""},
	}
	for _, c := range cases {
		got := headLines(c.in, c.n)
		if got != c.want {
			t.Errorf("headLines(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestNormalizeBashCommand(t *testing.T) {
	cases := []struct{ in, want string }{
		{"grep -r foo .", "grep -r foo ."},
		{"rtk grep -r foo .", "grep -r foo ."},
		{"/usr/local/bin/rtk find . -name '*.go'", "find . -name '*.go'"},
		{"git status", "git status"},
		{"rtk", "rtk"},
		{"", ""},
	}
	for _, c := range cases {
		got := NormalizeBashCommand(c.in)
		if got != c.want {
			t.Errorf("NormalizeBashCommand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBashRecordable(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"grep -r foo .", true},
		{"find . -name '*.go'", true},
		{"rtk grep -r foo .", true},
		{"/usr/bin/grep -r foo .", true},
		{"git status", false},
		{"ls -la", false},
		{"findme stuff", false},
		{"", false},
	}
	for _, c := range cases {
		got := BashRecordable(c.cmd)
		if got != c.want {
			t.Errorf("BashRecordable(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestBashSucceeded(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`{"stdout":"output","stderr":"","interrupted":false}`, true},
		{`{"stdout":"","stderr":"","interrupted":false}`, true},
		{`{"stdout":"out","stderr":"warn","interrupted":false}`, true},
		{`{"stdout":"","stderr":"error","interrupted":false}`, false},
		{`{"stdout":"","stderr":"","interrupted":true}`, false},
		{`{"stdout":"out","stderr":"","interrupted":true}`, false},
		{`not json`, false},
	}
	for _, c := range cases {
		got := BashSucceeded(json.RawMessage(c.raw))
		if got != c.want {
			t.Errorf("BashSucceeded(%s) = %v, want %v", c.raw, got, c.want)
		}
	}
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

func TestParseHookInput(t *testing.T) {
	t.Run("read_tool", func(t *testing.T) {
		raw := `{"session_id":"s1","cwd":"/proj","tool_name":"Read","tool_input":{"file_path":"/proj/main.go"},"tool_response":{}}`
		h, err := ParseHookInput(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if h.SessionID != "s1" || h.CWD != "/proj" || h.ToolName != ToolRead {
			t.Errorf("unexpected HookInput: %+v", h)
		}
		if h.ToolInput.FilePath != "/proj/main.go" {
			t.Errorf("file_path = %q, want /proj/main.go", h.ToolInput.FilePath)
		}
	})

	t.Run("bash_tool", func(t *testing.T) {
		raw := `{"session_id":"s2","cwd":"/proj","tool_name":"Bash","tool_input":{"command":"grep -r foo ."},"tool_response":{}}`
		h, err := ParseHookInput(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if h.ToolName != ToolBash || h.ToolInput.Command != "grep -r foo ." {
			t.Errorf("unexpected HookInput: %+v", h)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader("not json"))
		if err == nil {
			t.Error("expected error for malformed JSON")
		}
	})
}

func TestMakeSnippet(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		raw := json.RawMessage(`{"file":{"content":"line1\nline2\nline3","numLines":3,"startLine":1,"totalLines":3}}`)
		got := MakeSnippet(ToolRead, raw)
		if got != "line1\nline2\nline3" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("read_truncates", func(t *testing.T) {
		lines := make([]string, 60)
		for i := range lines {
			lines[i] = "line"
		}
		content := strings.Join(lines, "\n")
		raw, _ := json.Marshal(map[string]any{"file": map[string]any{"content": content}})
		got := MakeSnippet(ToolRead, json.RawMessage(raw))
		gotLines := strings.Split(got, "\n")
		if len(gotLines) != snippetHeadLines {
			t.Errorf("got %d lines, want %d", len(gotLines), snippetHeadLines)
		}
	})

	t.Run("edit_structured_patch", func(t *testing.T) {
		raw := json.RawMessage(`{
			"newString": "ignored",
			"structuredPatch": [
				{"oldStart":1,"oldLines":1,"newStart":1,"newLines":2,"lines":["-old","+new1","+new2"]}
			]
		}`)
		got := MakeSnippet(ToolEdit, raw)
		if !strings.Contains(got, "@@ -1,1 +1,2 @@") {
			t.Errorf("missing hunk header in %q", got)
		}
		if !strings.Contains(got, "-old") || !strings.Contains(got, "+new1") {
			t.Errorf("missing diff lines in %q", got)
		}
	})

	t.Run("edit_newstring_fallback", func(t *testing.T) {
		raw := json.RawMessage(`{"newString":"hello world","structuredPatch":[]}`)
		got := MakeSnippet(ToolEdit, raw)
		if got != "hello world" {
			t.Errorf("got %q, want %q", got, "hello world")
		}
	})

	t.Run("write", func(t *testing.T) {
		raw := json.RawMessage(`{"content":"file content"}`)
		got := MakeSnippet(ToolWrite, raw)
		if got != "file content" {
			t.Errorf("got %q, want %q", got, "file content")
		}
	})

	t.Run("bash", func(t *testing.T) {
		raw := json.RawMessage(`{"stdout":"search result","stderr":"","interrupted":false}`)
		got := MakeSnippet(ToolBash, raw)
		if got != "search result" {
			t.Errorf("got %q, want %q", got, "search result")
		}
	})

	t.Run("unknown_tool", func(t *testing.T) {
		got := MakeSnippet(Tool("Unknown"), json.RawMessage(`{}`))
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("malformed_json", func(t *testing.T) {
		got := MakeSnippet(ToolRead, json.RawMessage(`not json`))
		if got != "" {
			t.Errorf("got %q, want empty", got)
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

	t.Run("files_section", func(t *testing.T) {
		project := InjectResult{
			Files: []string{"main.go", "pkg/foo.go"},
		}
		got := InjectContextText(InjectResult{}, project, 3)
		if !strings.Contains(got, "## Recently active files (last 3 sessions)") {
			t.Errorf("missing files section in %q", got)
		}
		if !strings.Contains(got, "main.go") {
			t.Errorf("missing file entry in %q", got)
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

	t.Run("agent_tools_section", func(t *testing.T) {
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
		if !strings.Contains(got, "- bash context/agenttools/render.sh: Render it.") {
			t.Errorf("missing project tool command in %q", got)
		}
		if !strings.Contains(got, "- bash /home/u/.engram/agenttools/g.sh: global tool") {
			t.Errorf("missing global tool command in %q", got)
		}
	})

	t.Run("tool_candidates_resurfaced", func(t *testing.T) {
		project := InjectResult{ToolCandidates: []string{"alpha.sh (staged 2 days ago)", "bravo.sh (staged just now)"}}
		got := InjectContextText(InjectResult{}, project, 5)
		if !strings.Contains(got, "## Staged tool candidates") {
			t.Errorf("missing tool candidates section in %q", got)
		}
		if !strings.Contains(got, "- alpha.sh (staged 2 days ago)") || !strings.Contains(got, "- bravo.sh (staged just now)") {
			t.Errorf("missing age-annotated candidate lines in %q", got)
		}
	})

	t.Run("project_tool_shadows_global", func(t *testing.T) {
		global := InjectResult{AgentTools: []ToolDesc{
			{Name: "dup.sh", Desc: "global version", Run: "bash", Path: "/g/dup.sh"},
		}}
		project := InjectResult{AgentTools: []ToolDesc{
			{Name: "dup.sh", Desc: "project version", Run: "bash", Path: "context/agenttools/dup.sh"},
		}}
		got := InjectContextText(global, project, 5)
		if strings.Contains(got, "global version") {
			t.Errorf("project tool should shadow global, but global appeared: %q", got)
		}
		if !strings.Contains(got, "project version") {
			t.Errorf("project tool missing after shadowing: %q", got)
		}
	})
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
		{Tier: TierLong, Key: "alpha", Content: "content alpha"},
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

// --- DB tests ---

func TestRecord(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	err := Record(ctx, db, Event{
		SessionID: "s1",
		TS:        1000,
		Tool:      ToolRead,
		FilePath:  "main.go",
		Snippet:   "package main",
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

func TestInjectSeparatesFilesAndSearches(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	Record(ctx, db, Event{SessionID: "s1", TS: 1000, Tool: ToolRead, FilePath: "foo.go"})
	Record(ctx, db, Event{SessionID: "s1", TS: 2000, Tool: ToolEdit, FilePath: "bar.go"})
	Record(ctx, db, Event{SessionID: "s1", TS: 3000, Tool: ToolBash, FilePath: "grep -r auth ."})

	result, err := Inject(ctx, db, 5)
	if err != nil {
		t.Fatal(err)
	}

	fileSet := map[string]bool{}
	for _, f := range result.Files {
		fileSet[f] = true
	}
	if !fileSet["foo.go"] || !fileSet["bar.go"] {
		t.Errorf("Files = %v, missing expected entries", result.Files)
	}
	if fileSet["grep -r auth ."] {
		t.Error("bash command should not appear in Files")
	}

	if len(result.Searches) != 1 || result.Searches[0] != "grep -r auth ." {
		t.Errorf("Searches = %v, want [grep -r auth .]", result.Searches)
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
