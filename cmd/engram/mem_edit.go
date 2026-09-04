package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

const (
	memEditTldrMarker = "--- TLDR ---"
	memEditBodyMarker = "--- BODY ---"
)

var memEditEditor string

func formatMemoryEdit(scope string, m engram.Memory) string {
	return fmt.Sprintf(`# Engram memory editor
# Scope, tier, and key are read-only context. Use mem move to change scope or tier.
# Scope: %s
# Tier: %s
# Key: %s
# Keep the TLDR on one line (maximum %d characters). Save and close to apply.
%s
%s
%s
%s
`, scope, m.Tier, visibleMemoryKey(m), engram.MaxTldrLen,
		memEditTldrMarker, m.Tldr, memEditBodyMarker, m.Content)
}

func parseMemoryEdit(data string) (tldr, body string, err error) {
	_, afterTldr, ok := strings.Cut(data, memEditTldrMarker+"\n")
	if !ok {
		return "", "", fmt.Errorf("missing %q marker", memEditTldrMarker)
	}
	tldrText, bodyText, ok := strings.Cut(afterTldr, "\n"+memEditBodyMarker+"\n")
	if !ok {
		return "", "", fmt.Errorf("missing %q marker", memEditBodyMarker)
	}
	tldr = strings.TrimSpace(tldrText)
	if strings.ContainsAny(tldr, "\r\n") {
		return "", "", fmt.Errorf("tldr must be one line")
	}
	if n := len([]rune(tldr)); n > engram.MaxTldrLen {
		return "", "", fmt.Errorf("tldr too long: %d characters (max %d)", n, engram.MaxTldrLen)
	}

	// formatMemoryEdit adds exactly one envelope newline after the body so the
	// temporary file is pleasant in every editor. Remove only that newline;
	// any newline already belonging to the memory remains intact.
	body = strings.TrimSuffix(bodyText, "\n")
	if strings.TrimSpace(body) == "" {
		return "", "", fmt.Errorf("memory body cannot be empty; use `engram mem delete` to remove the entry")
	}
	return tldr, body, nil
}

// findEditableMemory resolves only the explicitly selected layer. Without
// --agent, agent-layer entries are not candidates; with --agent, the primary
// entry is not a candidate. That keeps an edit from crossing an authority-like
// scope just because two visible keys happen to match.
func findEditableMemory(ctx context.Context, db *sql.DB, tiers []engram.Tier, agent, key string) (*engram.Memory, error) {
	agent, err := engram.NormalizeAgent(agent)
	if err != nil {
		return nil, err
	}
	storedKey := key
	if agent != "" {
		storedKey, err = engram.AgentLayerKey(agent, key)
		if err != nil {
			return nil, err
		}
	} else if layerAgent, base, ok := engram.ParseAgentLayerKey(key); ok {
		return nil, fmt.Errorf("agent-layer key %q requires --agent %s with base key %q", key, layerAgent, base)
	}

	var matches []engram.Memory
	for _, tier := range tiers {
		m, err := engram.ReadMemory(ctx, db, tier, storedKey)
		if err != nil {
			return nil, err
		}
		if m != nil {
			matches = append(matches, *m)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("not found: %s", key)
	}
	if len(matches) > 1 {
		var labels []string
		for _, m := range matches {
			labels = append(labels, engram.MemoryLabel(m))
		}
		return nil, fmt.Errorf("ambiguous key %q; specify --tier (matches: %s)", key, strings.Join(labels, ", "))
	}
	return &matches[0], nil
}

func selectedEditor() string {
	if strings.TrimSpace(memEditEditor) != "" {
		return strings.TrimSpace(memEditEditor)
	}
	if editor := strings.TrimSpace(os.Getenv("VISUAL")); editor != "" {
		return editor
	}
	if editor := strings.TrimSpace(os.Getenv("EDITOR")); editor != "" {
		return editor
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

func runEditor(editor, path string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// Windows editor paths containing spaces should be supplied through a
		// wrapper or --editor. The common executable-plus-flags form still works.
		parts := strings.Fields(editor)
		if len(parts) == 0 {
			return fmt.Errorf("empty editor command")
		}
		cmd = exec.Command(parts[0], append(parts[1:], path)...)
	} else {
		// VISUAL and EDITOR conventionally contain a shell command (for example,
		// "code --wait"). Pass the generated path separately as $1 so its value
		// is never interpreted as shell syntax.
		cmd = exec.Command("sh", "-c", "exec "+editor+` "$1"`, "engram-editor", path)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q failed: %w", editor, err)
	}
	return nil
}

func applyMemoryEdit(ctx context.Context, db *sql.DB, original engram.Memory, tldr, body, scope string) (string, error) {
	contentChanged := body != original.Content
	tldrChanged := tldr != original.Tldr
	if !contentChanged && !tldrChanged {
		return "unchanged", nil
	}

	current, err := engram.ReadMemory(ctx, db, original.Tier, original.Key)
	if err != nil {
		return "", err
	}
	if current == nil {
		return "", fmt.Errorf("memory no longer exists: %s", engram.MemoryLabel(original))
	}
	if current.TS != original.TS || current.Content != original.Content ||
		current.Tldr != original.Tldr || current.Trigger != original.Trigger ||
		current.SessionID != original.SessionID {
		return "", fmt.Errorf("memory changed while the editor was open: %s; review the retained edit and retry", engram.MemoryLabel(original))
	}

	if !contentChanged {
		ok, err := engram.SetMemoryTldr(ctx, db, original.Tier, original.Key, tldr,
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scope))
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("memory no longer exists: %s", engram.MemoryLabel(original))
		}
		return "updated tldr", nil
	}

	updated := original
	updated.TS = time.Now().UnixMilli()
	updated.Content = body
	updated.Tldr = tldr
	if err := engram.WriteMemory(ctx, db, updated,
		engram.WithCurationSource(engram.SourceInteractive),
		engram.WithCurationScope(scope)); err != nil {
		return "", err
	}
	if tldrChanged {
		return "updated body and tldr", nil
	}
	return "updated body", nil
}

var memEditCmd = &cobra.Command{
	Use:   "edit <key-or-address>",
	Short: "Edit an existing memory's body and tldr in your editor",
	Long: `Open an existing memory in VISUAL or EDITOR (falling back to vi, or
notepad on Windows). Scope, tier, key, skill trigger, and short-term session
metadata are preserved. Use --editor to override the editor for one invocation.

The tier is inferred when the key is unambiguous. Use --tier when the same key
exists in multiple tiers, --global for global memory, and --agent to edit one
agent-specific invariant or preference layer.

If parsing, the editor, or the database write fails, the edited temporary file
is retained and its path is reported so your work is not lost.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (runErr error) {
		target, err := resolveMemoryTarget(cmd, args[0], "tier")
		if err != nil {
			return err
		}
		ctx := context.Background()
		h, err := target.openDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		tiers, err := target.viewTiers(cmd)
		if err != nil {
			return err
		}
		original, err := findEditableMemory(ctx, h.DB, tiers, target.Agent, target.Key)
		if err != nil {
			return err
		}

		f, err := os.CreateTemp("", "engram-edit-*.md")
		if err != nil {
			return fmt.Errorf("create edit file: %w", err)
		}
		path := f.Name()
		removeFile := false
		defer func() {
			if removeFile {
				_ = os.Remove(path)
				return
			}
			if runErr != nil {
				runErr = fmt.Errorf("%w; edited file kept at %s", runErr, path)
			}
		}()

		initial := formatMemoryEdit(scopeName(target.Global), *original)
		if _, err := f.WriteString(initial); err != nil {
			_ = f.Close()
			return fmt.Errorf("write edit file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close edit file: %w", err)
		}

		if err := runEditor(selectedEditor(), path); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read edited memory: %w", err)
		}
		tldr, body, err := parseMemoryEdit(string(data))
		if err != nil {
			return fmt.Errorf("parse edited memory: %w", err)
		}
		result, err := applyMemoryEdit(ctx, h.DB, *original, tldr, body, scopeName(target.Global))
		if err != nil {
			return fmt.Errorf("memory was not changed: %w", err)
		}
		removeFile = true
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", result, engram.MemoryLabel(*original))
		return nil
	},
}

func init() {
	memEditCmd.Flags().StringVar(&memEditEditor, "editor", "", "editor command for this invocation (default: VISUAL, EDITOR, or platform editor)")
	memCmd.AddCommand(memEditCmd)
}
