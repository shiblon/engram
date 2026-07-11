package engram

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ToolDesc describes a single agent tool discovered in an agenttools directory.
// The script's header is the single source of truth for Desc, Usage, and Run.
type ToolDesc struct {
	Name  string // file name, e.g. "render-dataflow.sh"
	Desc  string // from "engram-desc:" (required)
	Usage string // from "engram-usage:" (optional)
	Run   string // resolved interpreter command, e.g. "bash"
	Path  string // path passed to the runner, e.g. "$HOME/.engram/agenttools/render-dataflow.sh"
}

// Command returns the full invocation string, e.g.
// "bash $HOME/.engram/agenttools/render-dataflow.sh". Tools are never run
// directly (no reliance on the executable bit); they are invoked through
// their runner.
func (t ToolDesc) Command() string {
	if t.Run == "" {
		return t.Path
	}
	return t.Run + " " + t.Path
}

// GlobalAgentToolsDir returns the personal, cross-project tool directory under
// $HOME/.engram, alongside the global memory DB. Keeping all global engram state
// under one root lets it move as a unit -- backup, sync to a new machine, and a
// future dump/reload-all.
func GlobalAgentToolsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("global agenttools dir: %w", err)
	}
	return filepath.Join(home, ".engram", "agenttools"), nil
}

// extRunners maps a file extension to the command used to run it.
var extRunners = map[string]string{
	".sh":   "bash",
	".bash": "bash",
	".py":   "python3",
	".js":   "node",
	".mjs":  "node",
	".rb":   "ruby",
	".pl":   "perl",
}

// parseToolHeader extracts the engram-desc/usage/run fields from a script's
// content. An empty returned desc means the file is not a valid agent tool.
// Matching is comment-token-agnostic: a leading run of comment punctuation
// (#, //, --) and whitespace is stripped before the key is matched, so the same
// convention works across bash, python, node, and sql. First hit per key wins.
func parseToolHeader(content []byte) (desc, usage, run string) {
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		line := strings.TrimLeft(sc.Text(), " \t#/-")
		switch {
		case desc == "" && strings.HasPrefix(line, "engram-desc:"):
			desc = strings.TrimSpace(strings.TrimPrefix(line, "engram-desc:"))
		case usage == "" && strings.HasPrefix(line, "engram-usage:"):
			usage = strings.TrimSpace(strings.TrimPrefix(line, "engram-usage:"))
		case run == "" && strings.HasPrefix(line, "engram-run:"):
			run = strings.TrimSpace(strings.TrimPrefix(line, "engram-run:"))
		}
	}
	return desc, usage, run
}

// resolveRunner determines the command used to invoke a script. An explicit
// engram-run header wins, then a known extension, then the shebang interpreter.
// Returns "" when none apply, signalling the caller to skip the tool.
func resolveRunner(name string, content []byte, runHeader string) string {
	if runHeader != "" {
		return runHeader
	}
	if r, ok := extRunners[strings.ToLower(filepath.Ext(name))]; ok {
		return r
	}
	return shebangInterp(content)
}

// shebangInterp returns the interpreter name from a leading "#!" line, or "".
// "#!/usr/bin/env bash" -> "bash"; "#!/bin/bash" -> "bash".
func shebangInterp(content []byte) string {
	first := content
	if nl := bytes.IndexByte(content, '\n'); nl >= 0 {
		first = content[:nl]
	}
	s := strings.TrimSpace(string(first))
	if !strings.HasPrefix(s, "#!") {
		return ""
	}
	fields := strings.Fields(s[2:])
	if len(fields) == 0 {
		return ""
	}
	// "/usr/bin/env bash" -> "bash"; "/bin/bash" -> "bash".
	if filepath.Base(fields[0]) == "env" && len(fields) > 1 {
		return fields[1]
	}
	return filepath.Base(fields[0])
}

// mergeAgentTools combines global and project tools into a single list sorted by
// name. A project tool shadows a global tool with the same name, so a repo can
// pin its own version of a globally-installed tool. The result is deterministic.
func mergeAgentTools(global, project []ToolDesc) []ToolDesc {
	byName := make(map[string]ToolDesc, len(global)+len(project))
	for _, t := range global {
		byName[t.Name] = t
	}
	for _, t := range project {
		byName[t.Name] = t // project shadows global
	}
	if len(byName) == 0 {
		return nil
	}
	merged := make([]ToolDesc, 0, len(byName))
	for _, t := range byName {
		merged = append(merged, t)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	return merged
}

// ScanAgentTools returns the agent tools defined in dir, sorted by name. A file
// qualifies if it has an engram-desc header and a resolvable runner. An absent
// dir is normal and yields no tools and no error. Files without a desc are
// silently skipped (not tools); files with a desc but no resolvable runner are
// surfaced via warnings so a misconfigured tool is visible rather than dropped.
func ScanAgentTools(dir string) (tools []ToolDesc, warnings []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		desc, usage, runHeader := parseToolHeader(content)
		if desc == "" {
			continue
		}
		run := resolveRunner(name, content, runHeader)
		if run == "" {
			warnings = append(warnings, fmt.Sprintf(
				"%s: has engram-desc but no resolvable runner (add an engram-run header, a known extension, or a shebang)", path))
			continue
		}
		tools = append(tools, ToolDesc{Name: name, Desc: desc, Usage: usage, Run: run, Path: path})
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, warnings
}
