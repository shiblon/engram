package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var bootstrapDryRun bool
var bootstrapDiff bool

const bootstrapSetupPersonality = "Set up personality and preferences. FIRST run: engram mem --global --tier invariant list -- if personality and codename are already configured from another project, skip to preferences or just delete this entry. Otherwise: work with the user to choose a codename and define a personality, store both as global invariants, add code preferences as global preferences. Delete this entry when done."

const bootstrapMigrateMemory = "Migrate existing memory into engram. First check whether global memories are already configured: engram mem --global --tier invariant list. Then follow the appropriate path:\n\nIf global memories are NOT yet set up: also migrate any global context you have been maintaining (personality, preferences, coding rules from CLAUDE.md or similar files) into the global engram DB as invariants and preferences. Ask the user before writing anything global.\n\nIf global memories ARE already set up: leave them alone entirely.\n\nIn both cases: look for project-specific memory or context for THIS project -- markdown files, notes, project-level context files -- and migrate relevant content into the project engram tiers (not global): settled decisions to long-term, in-flight work to short-term. Delete or archive source files once migrated. If nothing is found, delete this entry."

type plannedBootstrapFile struct {
	path         string
	before       []byte
	beforeExists bool
	after        []byte
	afterExists  bool
	mode         os.FileMode
	reasons      []string
}

func (f *plannedBootstrapFile) changed() bool {
	return f.beforeExists != f.afterExists || !bytes.Equal(f.before, f.after)
}

func (f *plannedBootstrapFile) status() string {
	if !f.changed() {
		return "unchanged"
	}
	if !f.beforeExists {
		return "create"
	}
	if !f.afterExists {
		return "remove"
	}
	return "update"
}

type plannedBootstrapMemory struct {
	tier    engram.Tier
	key     string
	content string
	changed bool
	reason  string
}

type bootstrapPlan struct {
	files      []*plannedBootstrapFile
	fileByPath map[string]*plannedBootstrapFile
	memories   []plannedBootstrapMemory
}

func newBootstrapPlan() *bootstrapPlan {
	return &bootstrapPlan{fileByPath: make(map[string]*plannedBootstrapFile)}
}

func (p *bootstrapPlan) readFile(path string) ([]byte, error) {
	if f := p.fileByPath[path]; f != nil {
		if !f.afterExists {
			return nil, os.ErrNotExist
		}
		return append([]byte(nil), f.after...), nil
	}
	return os.ReadFile(path)
}

func (p *bootstrapPlan) file(path string) (*plannedBootstrapFile, error) {
	if f := p.fileByPath[path]; f != nil {
		return f, nil
	}
	data, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f := &plannedBootstrapFile{
		path:         path,
		before:       append([]byte(nil), data...),
		beforeExists: exists,
		after:        append([]byte(nil), data...),
		afterExists:  exists,
		mode:         0o644,
	}
	p.fileByPath[path] = f
	p.files = append(p.files, f)
	return f, nil
}

func (p *bootstrapPlan) writeFile(path string, data []byte, mode os.FileMode, reason string) error {
	f, err := p.file(path)
	if err != nil {
		return err
	}
	f.after = append(f.after[:0], data...)
	f.afterExists = true
	f.mode = mode
	f.reasons = appendReason(f.reasons, reason)
	return nil
}

func (p *bootstrapPlan) removeFile(path, reason string) error {
	f, err := p.file(path)
	if err != nil {
		return err
	}
	f.after = nil
	f.afterExists = false
	f.reasons = appendReason(f.reasons, reason)
	return nil
}

func appendReason(reasons []string, reason string) []string {
	if reason == "" {
		return reasons
	}
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func (p *bootstrapPlan) addMemory(tier engram.Tier, key, content, reason string, changed bool) {
	p.memories = append(p.memories, plannedBootstrapMemory{
		tier: tier, key: key, content: content, reason: reason, changed: changed,
	})
}

func (p *bootstrapPlan) counts() (changed, unchanged int) {
	for _, m := range p.memories {
		if m.changed {
			changed++
		} else {
			unchanged++
		}
	}
	for _, f := range p.files {
		if f.changed() {
			changed++
		} else {
			unchanged++
		}
	}
	return changed, unchanged
}

func (p *bootstrapPlan) validateFiles() error {
	for _, f := range p.files {
		data, err := os.ReadFile(f.path)
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if exists != f.beforeExists || !bytes.Equal(data, f.before) {
			return fmt.Errorf("bootstrap target changed after preview: %s; re-run bootstrap to review a fresh diff", f.path)
		}
	}
	return nil
}

func (p *bootstrapPlan) apply(ctx context.Context) error {
	if err := p.validateFiles(); err != nil {
		return err
	}
	if p.hasMemoryWrites() {
		db, err := engram.OpenGlobalDB(ctx)
		if err != nil {
			return err
		}
		defer db.Close()
		for _, m := range p.memories {
			if !m.changed {
				continue
			}
			if err := engram.WriteMemory(ctx, db, engram.Memory{
				Tier: m.tier, Key: m.key, Content: m.content,
			}, engram.WithCurationSource(engram.SourceBootstrap), engram.WithCurationScope(scopeName(false))); err != nil {
				return err
			}
		}
	}
	for _, f := range p.files {
		if !f.changed() {
			continue
		}
		if !f.afterExists {
			if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.path, f.after, f.mode); err != nil {
			return err
		}
	}
	return nil
}

func (p *bootstrapPlan) hasMemoryWrites() bool {
	for _, m := range p.memories {
		if m.changed {
			return true
		}
	}
	return false
}

func (p *bootstrapPlan) render(w io.Writer, withDiff bool) {
	for _, m := range p.memories {
		status := "unchanged"
		if m.changed {
			status = "create"
		}
		fmt.Fprintf(w, "[%s] memory engram:/%s/%s: %s\n", status, m.tier, m.key, m.reason)
		if m.changed {
			fmt.Fprintln(w, "  content:")
			for _, line := range strings.Split(m.content, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
	}
	for _, f := range p.files {
		fmt.Fprintf(w, "[%s] file %s", f.status(), f.path)
		if len(f.reasons) > 0 {
			fmt.Fprintf(w, ": %s", strings.Join(f.reasons, "; "))
		}
		fmt.Fprintln(w)
		if withDiff {
			fmt.Fprint(w, unifiedFileDiff(f))
		}
	}
	changed, unchanged := p.counts()
	fmt.Fprintf(w, "\n%d changes, %d unchanged\n", changed, unchanged)
}

func (p *bootstrapPlan) renderApplied(w io.Writer) {
	for _, m := range p.memories {
		verb := "skip (unchanged)"
		if m.changed {
			verb = "wrote"
		}
		fmt.Fprintf(w, "%s: global %s/%s\n", verb, m.tier, m.key)
	}
	for _, f := range p.files {
		verb := f.status()
		if verb == "unchanged" {
			verb = "skip (unchanged)"
		}
		fmt.Fprintf(w, "%s: %s\n", verb, f.path)
	}
	changed, unchanged := p.counts()
	fmt.Fprintf(w, "\n%d applied, %d unchanged\n", changed, unchanged)
}

func unifiedFileDiff(f *plannedBootstrapFile) string {
	oldName, newName := f.path, f.path
	if !f.beforeExists {
		oldName = "/dev/null"
	}
	if !f.afterExists {
		newName = "/dev/null"
	}
	if !f.changed() {
		// Unified diff format has no representation for an unchanged file. Keep
		// the standard headers so --dry-run can still account for every target.
		return fmt.Sprintf("--- %s\n+++ %s\n", oldName, newName)
	}
	before, after := string(f.before), string(f.after)
	edits := myers.ComputeEdits(span.URIFromPath(f.path), before, after)
	return fmt.Sprint(gotextdiff.ToUnified(oldName, newName, before, edits))
}

func confirmBootstrap(cmd *cobra.Command) (bool, error) {
	fmt.Fprint(cmd.OutOrStdout(), "\nApply this complete bootstrap plan? [y/N] ")
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func commandOut(cmd *cobra.Command) io.Writer {
	if cmd == nil {
		return os.Stdout
	}
	return cmd.OutOrStdout()
}

func runBootstrapPlan(cmd *cobra.Command, build func(*bootstrapPlan) error) error {
	if bootstrapDryRun && bootstrapDiff {
		return fmt.Errorf("--dry-run and --diff are alternative preview modes; choose one")
	}
	ctx := context.Background()
	plan := newBootstrapPlan()
	if err := planBootstrapGlobalDB(ctx, plan); err != nil {
		return err
	}
	if err := build(plan); err != nil {
		return err
	}
	out := commandOut(cmd)
	if bootstrapDryRun || bootstrapDiff {
		plan.render(out, true)
		changed, _ := plan.counts()
		if bootstrapDryRun {
			fmt.Fprintln(out, "Dry run: no changes applied.")
			return nil
		}
		if changed == 0 {
			fmt.Fprintln(out, "Nothing to apply.")
			return nil
		}
		accepted, err := confirmBootstrap(cmd)
		if err != nil {
			return err
		}
		if !accepted {
			fmt.Fprintln(out, "Rejected: no changes applied.")
			return nil
		}
	}
	if err := plan.apply(ctx); err != nil {
		return err
	}
	plan.renderApplied(out)
	return nil
}

func planBootstrapGlobalDB(ctx context.Context, plan *bootstrapPlan) error {
	var personality, setup, migrate *engram.Memory
	if engram.GlobalDBExists() {
		db, err := engram.OpenGlobalDBReadOnly(ctx)
		if err != nil {
			return err
		}
		defer db.Close()
		personality, err = engram.ReadMemory(ctx, db, engram.TierInvariant, "personality")
		if err != nil {
			return err
		}
		setup, err = engram.ReadMemory(ctx, db, engram.TierShort, "setup-personality")
		if err != nil {
			return err
		}
		migrate, err = engram.ReadMemory(ctx, db, engram.TierShort, "migrate-existing-memory")
		if err != nil {
			return err
		}
	}
	if personality != nil {
		plan.addMemory(engram.TierShort, "setup-personality", "", "global personality already exists", false)
	} else {
		plan.addMemory(engram.TierShort, "setup-personality", bootstrapSetupPersonality,
			"queue first-session personality setup", setup == nil)
	}
	plan.addMemory(engram.TierShort, "migrate-existing-memory", bootstrapMigrateMemory,
		"queue migration of existing agent memory", migrate == nil)
	return nil
}
