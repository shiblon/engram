package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

// tool: manage agent tools (stage candidates, promote, list, discard). These are
// single, allowlistable commands so the staging/promotion workflow never trips a
// per-action permission prompt, and so it works identically across agent platforms.

var toolCmd = &cobra.Command{
	Use:   "tool",
	Short: "Manage agent tools: stage candidates, promote them, list, discard",
}

var toolListCmd = &cobra.Command{
	Use:   "list",
	Short: "List staged tool candidates (with age) and promoted tools",
	RunE:  runToolList,
}

var toolStageCmd = &cobra.Command{
	Use:   "stage <name>",
	Short: "Stage a tool candidate, reading the script body from stdin",
	Args:  cobra.ExactArgs(1),
	RunE:  runToolStage,
}

var toolPromoteTo string

var toolPromoteCmd = &cobra.Command{
	Use:   "promote <name>",
	Short: "Promote a candidate (or project tool) into the project or global tool dir",
	Args:  cobra.ExactArgs(1),
	RunE:  runToolPromote,
}

var toolPromoteAs string

var toolDiscardCmd = &cobra.Command{
	Use:   "discard <name>",
	Short: "Delete a staged tool candidate",
	Args:  cobra.ExactArgs(1),
	RunE:  runToolDiscard,
}

func toolRoot() (string, error) {
	return engram.FindProjectRoot(effectiveCWD())
}

// validToolName rejects anything but a bare filename, so a name can never escape
// the intended directory via a path separator or "..".
func validToolName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid tool name %q: must be a bare filename", name)
	}
	return nil
}

func runToolStage(_ *cobra.Command, args []string) error {
	name := args[0]
	if err := validToolName(name); err != nil {
		return err
	}
	root, err := toolRoot()
	if err != nil {
		return err
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read script from stdin: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return fmt.Errorf("empty script on stdin; pipe the tool body to 'engram tool stage %s'", name)
	}
	dir := engram.ProjectToolCandidatesDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create candidates dir: %w", err)
	}
	dest := filepath.Join(dir, name)
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return fmt.Errorf("write candidate: %w", err)
	}
	rel, err := filepath.Rel(root, dest)
	if err != nil {
		rel = dest
	}
	fmt.Printf("staged candidate: %s\n", rel)
	return nil
}

func runToolPromote(_ *cobra.Command, args []string) error {
	name := args[0]
	if err := validToolName(name); err != nil {
		return err
	}
	destName := name
	if toolPromoteAs != "" {
		if err := validToolName(toolPromoteAs); err != nil {
			return fmt.Errorf("--as: %w", err)
		}
		destName = toolPromoteAs
	}
	root, err := toolRoot()
	if err != nil {
		return err
	}
	candidate := filepath.Join(engram.ProjectToolCandidatesDir(root), name)
	projectTool := filepath.Join(engram.ProjectAgentToolsDir(root), name)

	switch toolPromoteTo {
	case "project":
		if !fileExists(candidate) {
			return fmt.Errorf("no staged candidate %q to promote (stage it first)", name)
		}
		if err := os.MkdirAll(engram.ProjectAgentToolsDir(root), 0o755); err != nil {
			return err
		}
		dest := filepath.Join(engram.ProjectAgentToolsDir(root), destName)
		if err := moveFile(candidate, dest); err != nil {
			return err
		}
		fmt.Printf("promoted %s -> context/agenttools/%s (commit it to share with the repo)\n", name, destName)
		return nil

	case "global":
		gdir, err := engram.GlobalAgentToolsDir()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(gdir, 0o755); err != nil {
			return err
		}
		dest := filepath.Join(gdir, destName)
		switch {
		case fileExists(projectTool):
			// Project -> global is a COPY: the committed project copy stays so the
			// repo and teammates keep the tool they depend on.
			if err := copyFile(projectTool, dest); err != nil {
				return err
			}
			fmt.Printf("copied %s -> $HOME/.engram/agenttools/%s (project copy left in place)\n", name, destName)
		case fileExists(candidate):
			if err := moveFile(candidate, dest); err != nil {
				return err
			}
			fmt.Printf("promoted %s -> $HOME/.engram/agenttools/%s\n", name, destName)
		default:
			return fmt.Errorf("no candidate or project tool named %q to promote", name)
		}
		return nil

	default:
		return fmt.Errorf("--to must be 'project' or 'global', got %q", toolPromoteTo)
	}
}

func runToolDiscard(_ *cobra.Command, args []string) error {
	name := args[0]
	if err := validToolName(name); err != nil {
		return err
	}
	root, err := toolRoot()
	if err != nil {
		return err
	}
	candidate := filepath.Join(engram.ProjectToolCandidatesDir(root), name)
	if !fileExists(candidate) {
		return fmt.Errorf("no staged candidate %q", name)
	}
	if err := os.Remove(candidate); err != nil {
		return fmt.Errorf("discard candidate: %w", err)
	}
	fmt.Printf("discarded candidate: %s\n", name)
	return nil
}

func runToolList(_ *cobra.Command, _ []string) error {
	root, err := toolRoot()
	if err != nil {
		return err
	}
	now := time.Now()
	cands, _ := engram.ListToolCandidates(root)
	if len(cands) == 0 {
		fmt.Println("Staged candidates: (none)")
	} else {
		fmt.Println("Staged candidates:")
		for _, c := range cands {
			fmt.Printf("  %s\n", engram.FormatToolCandidate(c, now))
		}
	}
	proj, _ := engram.ScanAgentTools(engram.ProjectAgentToolsDir(root))
	printTools("Project tools (context/agenttools/):", proj, root)
	if gdir, err := engram.GlobalAgentToolsDir(); err == nil {
		glob, _ := engram.ScanAgentTools(gdir)
		printTools("Global tools ($HOME/.engram/agenttools/):", glob, "")
	}
	return nil
}

// printTools lists scanned tools under a heading, showing the run command. When
// relTo is non-empty, paths are shown relative to it.
func printTools(heading string, tools []engram.ToolDesc, relTo string) {
	if len(tools) == 0 {
		fmt.Printf("%s (none)\n", heading)
		return
	}
	fmt.Println(heading)
	for _, t := range tools {
		if relTo != "" {
			if rel, err := filepath.Rel(relTo, t.Path); err == nil {
				t.Path = rel
			}
		}
		fmt.Printf("  %s: %s\n", t.Command(), t.Desc)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// moveFile renames src to dst, falling back to copy+remove across filesystems
// (rename fails with EXDEV when src and dst live on different mounts, e.g. a repo
// under one mount and ~/.local under another).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// copyFile copies src to dst, preserving the source file mode.
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode().Perm())
}

func init() {
	toolPromoteCmd.Flags().StringVar(&toolPromoteTo, "to", "project", "promotion target: 'project' (context/agenttools) or 'global' ($HOME/.engram/agenttools)")
	toolPromoteCmd.Flags().StringVar(&toolPromoteAs, "as", "", "rename the tool at its destination (default: keep original name)")
	toolCmd.AddCommand(toolListCmd, toolStageCmd, toolPromoteCmd, toolDiscardCmd)
	rootCmd.AddCommand(toolCmd)
}
