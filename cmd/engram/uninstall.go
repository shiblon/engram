package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove engram configuration for a specific AI agent",
	Long: `Uninstall removes engram configuration for a given AI agent.

Subcommands:
  claude       -- remove Claude Code hooks, statusLine, and CLAUDE.md entries
  gemini       -- remove the Gemini CLI GEMINI.md section and SessionStart hook
  antigravity  -- remove the AntiGravity Knowledge Item
  copilot      -- remove the engram section from .github/copilot-instructions.md

Memories are NOT deleted by any subcommand. Use 'engram mem' to manage them.`,
}

// uninstall claude

var uninstallClaudeDropDB bool
var uninstallClaudeGlobal bool

var uninstallClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Remove Claude Code hooks, statusLine, and CLAUDE.md entries",
	Long: `Uninstall Claude Code integration:
  - Removes engram hooks from settings.json (project or global with -g)
  - Removes the statusLine from ~/.claude/settings.json
  - Removes the engram section from ~/.claude/CLAUDE.md (if markers present)
  - Removes engram entries from .gitignore in the current project

Memories are NOT deleted. Use --drop-db to also delete the project database.`,
	RunE: runUninstallClaude,
}

func runUninstallClaude(cmd *cobra.Command, _ []string) error {
	if err := uninstallSettings(uninstallClaudeGlobal); err != nil {
		return err
	}
	if err := uninstallClaudeMd(); err != nil {
		return err
	}
	if err := uninstallGitignore(); err != nil {
		return err
	}
	if uninstallClaudeDropDB {
		if err := uninstallDB(); err != nil {
			return err
		}
	}
	fmt.Println("\nDone. Global memories (personality, preferences) were not touched.")
	fmt.Println("To remove them: engram mem --global --tier invariant list  (then delete as needed)")
	return nil
}

func uninstallSettings(global bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var path string
	if global {
		path = filepath.Join(home, ".claude", "settings.json")
	} else {
		root, err := engram.FindProjectRoot(effectiveCWD())
		if err != nil {
			fmt.Println("skip (no project root found): hooks")
			return nil
		}
		path = filepath.Join(root, ".claude", "settings.json")
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Printf("skip (not found): %s\n", path)
		return nil
	}
	if err != nil {
		return err
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	changed := false

	if _, ok := settings["statusLine"]; ok {
		delete(settings, "statusLine")
		fmt.Printf("removed: statusLine from %s\n", path)
		changed = true
	}

	if hooks, ok := settings["hooks"].(map[string]any); ok {
		if postToolUse, ok := hooks["PostToolUse"].([]any); ok {
			filtered := make([]any, 0, len(postToolUse))
			for _, entry := range postToolUse {
				m, ok := entry.(map[string]any)
				if !ok {
					filtered = append(filtered, entry)
					continue
				}
				hookList, _ := m["hooks"].([]any)
				nonEngram := make([]any, 0, len(hookList))
				for _, h := range hookList {
					hm, ok := h.(map[string]any)
					if !ok {
						nonEngram = append(nonEngram, h)
						continue
					}
					cmd, _ := hm["command"].(string)
					if !strings.Contains(cmd, "engram record") {
						nonEngram = append(nonEngram, h)
					}
				}
				if len(nonEngram) > 0 {
					m["hooks"] = nonEngram
					filtered = append(filtered, m)
				}
			}
			if len(filtered) != len(postToolUse) {
				hooks["PostToolUse"] = filtered
				fmt.Printf("removed: engram record hook from %s\n", path)
				changed = true
			}
		}

		if sessionStart, ok := hooks["SessionStart"].([]any); ok {
			filtered := make([]any, 0, len(sessionStart))
			for _, entry := range sessionStart {
				m, ok := entry.(map[string]any)
				if !ok {
					filtered = append(filtered, entry)
					continue
				}
				hookList, _ := m["hooks"].([]any)
				nonEngram := make([]any, 0, len(hookList))
				for _, h := range hookList {
					hm, ok := h.(map[string]any)
					if !ok {
						nonEngram = append(nonEngram, h)
						continue
					}
					cmd, _ := hm["command"].(string)
					if !strings.Contains(cmd, "engram inject") {
						nonEngram = append(nonEngram, h)
					}
				}
				if len(nonEngram) > 0 {
					m["hooks"] = nonEngram
					filtered = append(filtered, m)
				}
			}
			if len(filtered) != len(sessionStart) {
				hooks["SessionStart"] = filtered
				fmt.Printf("removed: engram inject hook from %s\n", path)
				changed = true
			}
		}
	}

	if !changed {
		fmt.Printf("skip (no engram entries found): %s\n", path)
		return nil
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0644)
}

var engramBlock = regexp.MustCompile(`(?s)<!-- engram:start -->.*?<!-- engram:end -->\n?`)
var engramMdLine = regexp.MustCompile(`(?m)^@engram\.md\n?`)

func uninstallClaudeMd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Printf("skip (not found): %s\n", path)
		return nil
	}
	if err != nil {
		return err
	}

	content := string(data)
	updated := engramBlock.ReplaceAllString(content, "")
	updated = engramMdLine.ReplaceAllString(updated, "")

	if updated == content {
		fmt.Printf("skip (no engram content): %s\n", path)
	} else {
		if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
			return err
		}
		fmt.Printf("removed: engram reference from %s\n", path)
	}

	engramMd := filepath.Join(home, ".claude", "engram.md")
	if err := os.Remove(engramMd); err == nil {
		fmt.Printf("removed: %s\n", engramMd)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func uninstallGitignore() error {
	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		fmt.Println("skip (no project root found): .gitignore")
		return nil
	}
	path := filepath.Join(root, ".gitignore")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Printf("skip (not found): %s\n", path)
		return nil
	}
	if err != nil {
		return err
	}

	block := regexp.MustCompile(`(?m)\n# engram database\n\.claude/engram\.db\n\.claude/engram\.db-shm\n\.claude/engram\.db-wal\n?`)
	updated := block.ReplaceAllString(string(data), "")
	if updated == string(data) {
		fmt.Printf("skip (no engram entries): %s\n", path)
		return nil
	}
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		return err
	}
	fmt.Printf("removed: engram entries from %s\n", path)
	return nil
}

func uninstallDB() error {
	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		fmt.Println("skip (no project root found): database")
		return nil
	}
	for _, name := range []string{"engram.db", "engram.db-shm", "engram.db-wal"} {
		path := filepath.Join(root, ".claude", name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Printf("deleted: %s\n", path)
	}
	return nil
}

// uninstall gemini

var uninstallGeminiCmd = &cobra.Command{
	Use:   "gemini",
	Short: "Remove the Gemini CLI knowledge file written by bootstrap gemini",
	RunE:  runUninstallGemini,
}

var engramGeminiSection = regexp.MustCompile(`(?m)\n## Engram Session Protocol\n[\s\S]*?Do not skip this step\.\n?`)

func runUninstallGemini(_ *cobra.Command, _ []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	mdPath := filepath.Join(home, ".gemini", "GEMINI.md")
	data, err := os.ReadFile(mdPath)
	if os.IsNotExist(err) {
		fmt.Printf("skip (not found): %s\n", mdPath)
	} else if err != nil {
		return err
	} else {
		updated := engramGeminiSection.ReplaceAllString(string(data), "")
		if updated == string(data) {
			fmt.Printf("skip (no engram section): %s\n", mdPath)
		} else {
			if err := os.WriteFile(mdPath, []byte(updated), 0644); err != nil {
				return err
			}
			fmt.Printf("removed: engram section from %s\n", mdPath)
		}
	}

	fmt.Println("\nDone. Global memories (personality, preferences) were not touched.")
	fmt.Println("To remove them: engram mem --global --tier invariant list  (then delete as needed)")
	return nil
}

// uninstall copilot

var uninstallCopilotCmd = &cobra.Command{
	Use:   "copilot",
	Short: "Remove the engram section from .github/copilot-instructions.md",
	RunE:  runUninstallCopilot,
}

var engramCopilotSection = regexp.MustCompile(`(?m)\n## Engram Session Protocol\n[\s\S]*?Do not skip this step\.\n?`)

func runUninstallCopilot(_ *cobra.Command, _ []string) error {
	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		fmt.Println("skip (no project root found): copilot-instructions.md")
		return nil
	}
	path := filepath.Join(root, ".github", "copilot-instructions.md")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Printf("skip (not found): %s\n", path)
	} else if err != nil {
		return err
	} else {
		updated := engramCopilotSection.ReplaceAllString(string(data), "")
		if updated == string(data) {
			fmt.Printf("skip (no engram section): %s\n", path)
		} else {
			if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
				return err
			}
			fmt.Printf("removed: engram section from %s\n", path)
		}
	}

	fmt.Println("\nDone. Global memories (personality, preferences) were not touched.")
	fmt.Println("To remove them: engram mem --global --tier invariant list  (then delete as needed)")
	return nil
}

// uninstall antigravity

var uninstallAntigravityCmd = &cobra.Command{
	Use:   "antigravity",
	Short: "Remove the AntiGravity Knowledge Item written by bootstrap antigravity",
	RunE:  runUninstallAntigravity,
}

func runUninstallAntigravity(_ *cobra.Command, _ []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	kiDir := filepath.Join(home, ".gemini", "antigravity", "knowledge", "engram_protocol")

	if _, err := os.Stat(kiDir); os.IsNotExist(err) {
		fmt.Printf("skip (not found): %s\n", kiDir)
	} else {
		if err := os.RemoveAll(kiDir); err != nil {
			return err
		}
		fmt.Printf("removed: %s\n", kiDir)
	}

	fmt.Println("\nDone. Global memories (personality, preferences) were not touched.")
	fmt.Println("To remove them: engram mem --global --tier invariant list  (then delete as needed)")
	return nil
}

func init() {
	uninstallClaudeCmd.Flags().BoolVar(&uninstallClaudeDropDB, "drop-db", false, "also delete the project database")
	uninstallClaudeCmd.Flags().BoolVarP(&uninstallClaudeGlobal, "global", "g", false, "remove hooks from ~/.claude/settings.json instead of the project's .claude/settings.json")
	uninstallCmd.AddCommand(uninstallClaudeCmd, uninstallGeminiCmd, uninstallAntigravityCmd, uninstallCopilotCmd)
	rootCmd.AddCommand(uninstallCmd)
}
