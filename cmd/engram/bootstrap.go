package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

const bootstrapWorkflow = `When asked to remember something: infer the right tier from context, write it with engram mem, and tell the user where it was stored (global or project, which tier) and why.

Memory tiers:
- invariant (--global): identity, codename, personality -- rarely changed
- preference (--global): code and behavior rules -- add and remove over time
- long: settled project decisions and facts
- short: in-flight context, conversation stack, backlog items

Global memories (invariant, preference) apply to all projects. Before writing or
updating any global memory, always ask the user for confirmation -- global changes
affect every project and session. Check what is already there first:
engram mem --global --tier invariant list

When starting a digression: save current context to short-term memory first, confirm it is there, then proceed. When done, re-read short-term and resume.

When a task finishes: check short-term for anything worth promoting to long-term, and delete what is no longer relevant.

To manage memory:
  engram mem --help                          -- all subcommands
  engram mem search <query>                  -- full-text search across all tiers
  engram mem search --tier long <query>      -- search within a specific tier
If engram is not in PATH, find the full path with: go env GOBIN

If session start context appears twice or seems duplicated, engram hooks are
probably configured in both ~/.claude/settings.json and .claude/settings.json.
Ask the user which they prefer and help remove the duplicate set.`

const bootstrapCanary = `If your identity or instructions feel unfamiliar, run:
  engram mem --global --tier invariant list
That is the signal to re-bootstrap from the inject context at session start.`

// claudeMDContent returns the content to write into CLAUDE.md, wrapping
// agentInfoText in engram markers so it can be cleanly found and replaced.
func claudeMDContent() string {
	return "<!-- engram:start -->\n" + agentInfoText + "<!-- engram:end -->\n"
}

var bootstrapGlobalOnly bool

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Set up engram workflow instructions in global memory and CLAUDE.md",
	Long: `Bootstrap writes the standard engram workflow instructions to the global
database as invariants, and optionally writes a minimal CLAUDE.md.

Existing keys are never overwritten -- bootstrap is safe to re-run.`,
	RunE: runBootstrap,
}

func runBootstrap(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	db, err := engram.OpenGlobalDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	wrote, skipped := 0, 0

	invariants := map[string]string{
		"engram-workflow": bootstrapWorkflow,
		"engram-canary":   bootstrapCanary,
	}
	for key, content := range invariants {
		existing, err := engram.ReadMemory(ctx, db, engram.TierInvariant, key)
		if err != nil {
			return err
		}
		if existing != nil {
			fmt.Printf("skip (exists): invariant/%s\n", key)
			skipped++
			continue
		}
		if err := engram.WriteMemory(ctx, db, engram.Memory{
			Tier:    engram.TierInvariant,
			Key:     key,
			Content: content,
		}); err != nil {
			return err
		}
		fmt.Printf("wrote: invariant/%s\n", key)
		wrote++
	}

	// Write the personality setup todo as a short-term memory only if no
	// personality invariant exists yet -- it's self-deleting once done.
	personality, err := engram.ReadMemory(ctx, db, engram.TierInvariant, "personality")
	if err != nil {
		return err
	}
	if personality == nil {
		setupKey := "setup-personality"
		existing, err := engram.ReadMemory(ctx, db, engram.TierShort, setupKey)
		if err != nil {
			return err
		}
		if existing != nil {
			fmt.Printf("skip (exists): short/%s\n", setupKey)
			skipped++
		} else {
			if err := engram.WriteMemory(ctx, db, engram.Memory{
				Tier:    engram.TierShort,
				Key:     setupKey,
				Content: "Set up personality and preferences. FIRST run: engram mem --global --tier invariant list -- if personality and codename are already configured from another project, skip to preferences or just delete this entry. Otherwise: work with the user to choose a codename and define a personality, store both as global invariants, add code preferences as global preferences. Delete this entry when done.",
			}); err != nil {
				return err
			}
			fmt.Printf("wrote: short/%s\n", setupKey)
			wrote++
		}
	}

	// Write the memory migration todo -- self-deleting once done.
	migrateKey := "migrate-existing-memory"
	existing, err := engram.ReadMemory(ctx, db, engram.TierShort, migrateKey)
	if err != nil {
		return err
	}
	if existing != nil {
		fmt.Printf("skip (exists): short/%s\n", migrateKey)
		skipped++
	} else {
		if err := engram.WriteMemory(ctx, db, engram.Memory{
			Tier:    engram.TierShort,
			Key:     migrateKey,
			Content: "Migrate existing memory into engram. First check whether global memories are already configured: engram mem --global --tier invariant list. Then follow the appropriate path:\n\nIf global memories are NOT yet set up: also migrate any global context you have been maintaining (personality, preferences, coding rules from CLAUDE.md or similar files) into the global engram DB as invariants and preferences. Ask the user before writing anything global.\n\nIf global memories ARE already set up: leave them alone entirely.\n\nIn both cases: look for project-specific memory or context for THIS project -- markdown files, notes, project-level context files -- and migrate relevant content into the project engram tiers (not global): settled decisions to long-term, in-flight work to short-term. Delete or archive source files once migrated. If nothing is found, delete this entry.",
		}); err != nil {
			return err
		}
		fmt.Printf("wrote: short/%s\n", migrateKey)
		wrote++
	}

	if !bootstrapGlobalOnly {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := bootstrapClaudeMd(); err != nil {
			return err
		}
		if err := bootstrapStatusLine(exe); err != nil {
			return err
		}
		if err := bootstrapHooks(exe); err != nil {
			return err
		}
	}

	fmt.Printf("\n%d written, %d skipped\n", wrote, skipped)
	if skipped > 0 {
		fmt.Println("(use engram mem --global --tier invariant write <key> <content> to update existing entries)")
	}
	return nil
}

func bootstrapClaudeMd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if strings.Contains(string(data), "<!-- engram:start -->") {
		fmt.Printf("skip (already has engram section): %s\n", path)
		return nil
	}

	if strings.Contains(string(data), "engram") {
		fmt.Printf("skip (has engram content but no markers -- not modifying): %s\n", path)
		fmt.Println("  Add <!-- engram:start --> and <!-- engram:end --> markers manually, or move the file and re-run bootstrap.")
		return nil
	}

	if len(data) > 0 {
		fmt.Printf("skip (exists without engram content -- not modifying): %s\n", path)
		fmt.Println("  Add engram content manually, or move the file and re-run bootstrap.")
		return nil
	}

	if err := os.WriteFile(path, []byte(claudeMDContent()), 0644); err != nil {
		return err
	}
	fmt.Printf("wrote: %s\n", path)
	return nil
}


func bootstrapStatusLine(exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "settings.json")

	settings, err := readSettingsJSON(path)
	if err != nil {
		return err
	}

	if _, exists := settings["statusLine"]; exists {
		fmt.Printf("skip (exists): statusLine in %s\n", path)
		return nil
	}

	settings["statusLine"] = map[string]any{
		"type":            "command",
		"command":         exe + " status",
		"refreshInterval": 30,
	}

	if err := writeSettingsJSON(path, settings); err != nil {
		return err
	}
	fmt.Printf("wrote: statusLine in %s\n", path)
	return nil
}

func bootstrapHooks(exe string) error {
	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		fmt.Println("skip (no project root found): hooks")
		return nil
	}
	projectPath := filepath.Join(root, ".claude", "settings.json")

	data, _ := os.ReadFile(projectPath)
	if strings.Contains(string(data), "engram record") {
		fmt.Printf("skip (hooks already in project settings): %s\n", projectPath)
		return nil
	}

	if err := addEngramHooks(projectPath, exe); err != nil {
		return err
	}
	fmt.Printf("wrote: engram hooks in %s\n", projectPath)
	return nil
}

func addEngramHooks(path string, exe string) error {
	settings, err := readSettingsJSON(path)
	if err != nil {
		return err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	hooks["PostToolUse"] = append(
		asSlice(hooks["PostToolUse"]),
		map[string]any{
			"matcher": "Read|Edit|Write|Bash",
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": exe + " record",
			}},
		},
	)
	hooks["SessionStart"] = append(
		asSlice(hooks["SessionStart"]),
		map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": exe + " inject",
			}},
		},
	)
	settings["hooks"] = hooks

	return writeSettingsJSON(path, settings)
}

func readSettingsJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func writeSettingsJSON(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0644)
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func init() {
	bootstrapCmd.Flags().BoolVar(&bootstrapGlobalOnly, "global-only", false, "skip CLAUDE.md, only write global DB invariants")
	rootCmd.AddCommand(bootstrapCmd)
}
