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

var uninstallDropDB bool

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove engram hooks, statusLine, and gitignore entries",
	Long: `Uninstall reverses what bootstrap did:
  - Removes engram hooks from ~/.claude/settings.json
  - Removes the statusLine from ~/.claude/settings.json
  - Removes the engram section from ~/.claude/CLAUDE.md (if markers present)
  - Removes engram entries from .gitignore in the current project

Memories are NOT deleted. Use 'engram mem' commands to manage them manually.
Use --drop-db to also delete the project database.`,
	RunE: runUninstall,
}

func runUninstall(cmd *cobra.Command, _ []string) error {
	if err := uninstallSettings(); err != nil {
		return err
	}
	if err := uninstallClaudeMd(); err != nil {
		return err
	}
	if err := uninstallGitignore(); err != nil {
		return err
	}
	if uninstallDropDB {
		if err := uninstallDB(); err != nil {
			return err
		}
	}
	fmt.Println("\nDone. Global memories (personality, preferences) were not touched.")
	fmt.Println("To remove them: engram mem --global --tier invariant list  (then delete as needed)")
	return nil
}

func uninstallSettings() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "settings.json")

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
	if !strings.Contains(content, "<!-- engram:start -->") {
		if strings.Contains(content, "engram") {
			fmt.Printf("skip (has engram content but no markers): %s\n", path)
			fmt.Println("  Edit manually to remove engram content.")
		} else {
			fmt.Printf("skip (no engram content): %s\n", path)
		}
		return nil
	}

	updated := engramBlock.ReplaceAllString(content, "")
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		return err
	}
	fmt.Printf("removed: engram section from %s\n", path)
	return nil
}

func uninstallGitignore() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := engram.FindProjectRoot(cwd)
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
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := engram.FindProjectRoot(cwd)
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

func init() {
	uninstallCmd.Flags().BoolVar(&uninstallDropDB, "drop-db", false, "also delete the project database")
	rootCmd.AddCommand(uninstallCmd)
}
