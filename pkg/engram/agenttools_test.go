package engram

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseToolHeader(t *testing.T) {
	cases := []struct {
		name             string
		content          string
		desc, usage, run string
	}{
		{
			name:    "bash hash comments with shebang on line 1",
			content: "#!/usr/bin/env bash\n# engram-desc: Render the diagram.\n# engram-usage: render OUTFILE\necho hi\n",
			desc:    "Render the diagram.",
			usage:   "render OUTFILE",
		},
		{
			name:    "slash comments",
			content: "// engram-desc: A node tool.\n// engram-usage: tool --flag\n",
			desc:    "A node tool.",
			usage:   "tool --flag",
		},
		{
			name:    "sql double-dash comments",
			content: "-- engram-desc: A sql tool.\n",
			desc:    "A sql tool.",
		},
		{
			name:    "no leading space after token",
			content: "#engram-desc:Tight.\n",
			desc:    "Tight.",
		},
		{
			name:    "explicit run header",
			content: "# engram-desc: thing\n# engram-run: python3.11\n",
			desc:    "thing",
			run:     "python3.11",
		},
		{
			name:    "desc optional usage absent",
			content: "# engram-desc: only desc\n",
			desc:    "only desc",
		},
		{
			name:    "first desc wins",
			content: "# engram-desc: first\n# engram-desc: second\n",
			desc:    "first",
		},
		{
			name:    "no header at all",
			content: "echo hello\n",
		},
		{
			name:    "shebang is not a desc",
			content: "#!/bin/bash\necho hi\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			desc, usage, run := parseToolHeader([]byte(c.content))
			if desc != c.desc || usage != c.usage || run != c.run {
				t.Errorf("parseToolHeader() = (%q,%q,%q), want (%q,%q,%q)",
					desc, usage, run, c.desc, c.usage, c.run)
			}
		})
	}
}

func TestResolveRunner(t *testing.T) {
	cases := []struct {
		name, file, content, runHeader, want string
	}{
		{name: "explicit header wins over extension", file: "x.py", content: "", runHeader: "python2", want: "python2"},
		{name: "extension sh", file: "foo.sh", want: "bash"},
		{name: "extension py", file: "foo.py", want: "python3"},
		{name: "extension js", file: "foo.js", want: "node"},
		{name: "shebang env", file: "foo", content: "#!/usr/bin/env ruby\n", want: "ruby"},
		{name: "shebang absolute", file: "foo", content: "#!/bin/bash\n", want: "bash"},
		{name: "unresolvable", file: "foo", content: "no shebang here\n", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveRunner(c.file, []byte(c.content), c.runHeader); got != c.want {
				t.Errorf("resolveRunner() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestToolDescCommand(t *testing.T) {
	td := ToolDesc{Run: "bash", Path: "context/agenttools/foo.sh"}
	if got, want := td.Command(), "bash context/agenttools/foo.sh"; got != want {
		t.Errorf("Command() = %q, want %q", got, want)
	}
	if got, want := (ToolDesc{Path: "p"}).Command(), "p"; got != want {
		t.Errorf("Command() with no runner = %q, want %q", got, want)
	}
}

func TestScanAgentTools(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("render.sh", "#!/usr/bin/env bash\n# engram-desc: Render it.\n# engram-usage: render OUT\n")
	write("report.py", "# engram-desc: Make a report.\n")
	write("notatool.txt", "just some text, no header\n")
	write("broken", "# engram-desc: no runner and no extension\nplain text\n")
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	tools, warnings := ScanAgentTools(dir)

	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2: %+v", len(tools), tools)
	}
	// Sorted by name: render.sh before report.py.
	if tools[0].Name != "render.sh" || tools[0].Run != "bash" || tools[0].Usage != "render OUT" {
		t.Errorf("tool[0] = %+v", tools[0])
	}
	if tools[1].Name != "report.py" || tools[1].Run != "python3" {
		t.Errorf("tool[1] = %+v", tools[1])
	}
	if len(warnings) != 1 {
		t.Errorf("got %d warnings, want 1 (the broken tool): %v", len(warnings), warnings)
	}
}

func TestScanAgentToolsAbsentDir(t *testing.T) {
	tools, warnings := ScanAgentTools(filepath.Join(t.TempDir(), "does-not-exist"))
	if tools != nil || warnings != nil {
		t.Errorf("absent dir should yield nil, nil; got %v, %v", tools, warnings)
	}
}

func TestMergeAgentTools(t *testing.T) {
	global := []ToolDesc{
		{Name: "shared.sh", Desc: "global", Run: "bash", Path: "/g/shared.sh"},
		{Name: "globalonly.sh", Desc: "g only", Run: "bash", Path: "/g/globalonly.sh"},
	}
	project := []ToolDesc{
		{Name: "shared.sh", Desc: "project", Run: "bash", Path: "context/agenttools/shared.sh"},
		{Name: "projonly.sh", Desc: "p only", Run: "bash", Path: "context/agenttools/projonly.sh"},
	}
	got := mergeAgentTools(global, project)

	if len(got) != 3 {
		t.Fatalf("got %d tools, want 3 (shared deduped): %+v", len(got), got)
	}
	// Sorted by name: globalonly.sh, projonly.sh, shared.sh.
	want := []string{"globalonly.sh", "projonly.sh", "shared.sh"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("order[%d] = %q, want %q", i, got[i].Name, w)
		}
	}
	// shared.sh must be the project version (shadowing).
	if got[2].Desc != "project" || got[2].Path != "context/agenttools/shared.sh" {
		t.Errorf("shared.sh not shadowed by project: %+v", got[2])
	}
}

func TestMergeAgentToolsEmpty(t *testing.T) {
	if got := mergeAgentTools(nil, nil); got != nil {
		t.Errorf("merge of nothing should be nil, got %v", got)
	}
}

func TestListToolCandidates(t *testing.T) {
	root := t.TempDir()
	cand := ProjectToolCandidatesDir(root)
	if err := os.MkdirAll(cand, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"bravo.sh", "alpha.sh"} {
		if err := os.WriteFile(filepath.Join(cand, n), []byte("# engram-desc: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(cand, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	cands, err := ListToolCandidates(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("got %d candidates, want 2 (subdir skipped): %+v", len(cands), cands)
	}
	if cands[0].Name != "alpha.sh" || cands[1].Name != "bravo.sh" {
		t.Errorf("not sorted by name: %+v", cands)
	}
	if cands[0].ModTime.IsZero() {
		t.Errorf("ModTime not populated: %+v", cands[0])
	}
	// Listing must not remove anything (candidates persist).
	if _, err := os.Stat(filepath.Join(cand, "alpha.sh")); err != nil {
		t.Errorf("listing should not delete candidates: %v", err)
	}
}

func TestListToolCandidatesAbsent(t *testing.T) {
	cands, err := ListToolCandidates(t.TempDir()) // no candidates dir
	if err != nil || cands != nil {
		t.Errorf("absent dir = (%v, %v), want (nil, nil)", cands, err)
	}
}

func TestFormatToolCandidate(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		age  time.Duration
		want string
	}{
		{"fresh.sh", 10 * time.Second, "fresh.sh (staged just now)"},
		{"min.sh", 1 * time.Minute, "min.sh (staged 1 minute ago)"},
		{"mins.sh", 5 * time.Minute, "mins.sh (staged 5 minutes ago)"},
		{"hour.sh", 1 * time.Hour, "hour.sh (staged 1 hour ago)"},
		{"hours.sh", 3 * time.Hour, "hours.sh (staged 3 hours ago)"},
		{"day.sh", 24 * time.Hour, "day.sh (staged 1 day ago)"},
		{"days.sh", 5 * 24 * time.Hour, "days.sh (staged 5 days ago)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FormatToolCandidate(ToolCandidate{Name: c.name, ModTime: now.Add(-c.age)}, now)
			if got != c.want {
				t.Errorf("FormatToolCandidate = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAgentToolsPaths(t *testing.T) {
	if got := ProjectAgentToolsDir("/repo"); got != "/repo/context/agenttools" {
		t.Errorf("ProjectAgentToolsDir = %q", got)
	}
	if got := ProjectToolCandidatesDir("/repo"); got != "/repo/.engram/toolcandidates" {
		t.Errorf("ProjectToolCandidatesDir = %q", got)
	}
}
