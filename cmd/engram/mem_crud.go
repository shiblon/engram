package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

// syncStandingIfTouched re-renders the per-platform standing-memory files when a
// global invariant or preference was just mutated -- the render-on-write half of
// the channel strategy that keeps both tiers on the authoritative always-loaded
// channel. Best-effort: a sync failure must never fail the mem operation itself.
func syncStandingIfTouched(ctx context.Context, h *engram.DBHandle, global bool, tiers ...engram.Tier) {
	if !global {
		return
	}
	for _, t := range tiers {
		if t == engram.TierInvariant || t == engram.TierPreference {
			if _, err := engram.SyncStandingMemory(ctx, h.DB); err != nil {
				fmt.Fprintf(os.Stderr, "engram: sync standing memory: %v\n", err)
			}
			return
		}
	}
}

func memAgentName() (string, error) {
	return engram.NormalizeAgent(memAgent)
}

func memDefaultTiers(cmd *cobra.Command) []engram.Tier {
	if cmd.Flag("tier").Changed {
		return []engram.Tier{engram.Tier(memTier)}
	}
	if memAgent != "" {
		return engram.StandingTiers
	}
	return []engram.Tier{engram.TierInvariant, engram.TierPreference, engram.TierLong, engram.TierShort}
}

func memViewTiers(cmd *cobra.Command) ([]engram.Tier, error) {
	tiers := memDefaultTiers(cmd)
	if memAgent == "" {
		return tiers, nil
	}
	for _, t := range tiers {
		if !engram.IsStandingTier(t) {
			return nil, fmt.Errorf("--agent only applies to global invariant/preference memory")
		}
	}
	return tiers, nil
}

func printMemories(w io.Writer, memories []engram.Memory) {
	for i, m := range memories {
		fmt.Fprintf(w, "%d. %s %s\n", i+1, engram.MemoryLabel(m), m.Content)
	}
}

// visibleMemoryKey returns the key a person uses with the CLI. Agent-layer
// storage prefixes are an implementation detail, so render them using the same
// @agent notation as MemoryLabel.
func visibleMemoryKey(m engram.Memory) string {
	if agent, base, ok := engram.ParseAgentLayerKey(m.Key); ok {
		return fmt.Sprintf("%s @%s", base, agent)
	}
	return m.Key
}

func memoryKey(m engram.Memory) string {
	if _, base, ok := engram.ParseAgentLayerKey(m.Key); ok {
		return base
	}
	return m.Key
}

func printMemorySummaries(w io.Writer, memories []engram.Memory, global bool) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ADDRESS\tTLDR")
	for _, m := range memories {
		// Tldrs are intended to be one line, but imported or legacy data may not
		// honor that contract. Keep the index scannable regardless.
		summary := strings.Join(strings.Fields(m.InjectSummary()), " ")
		fmt.Fprintf(tw, "%s\t%s\n", engram.MemoryAddressFor(global, m), summary)
	}
	_ = tw.Flush()
}

func printMemoryKeys(w io.Writer, memories []engram.Memory) {
	for _, m := range memories {
		fmt.Fprintln(w, memoryKey(m))
	}
}

var memTldr string

// effectiveWriteTldr decides the tldr a `write` should persist. When --tldr was
// given (flagChanged), its value wins, including "" to clear deliberately. When
// it was not given, the existing memory's tldr is preserved so a content-only
// rewrite never silently wipes a curated summary; a brand-new memory (existing
// nil) starts empty.
func effectiveWriteTldr(existing *engram.Memory, flagChanged bool, flagVal string) string {
	if flagChanged {
		return strings.TrimSpace(flagVal)
	}
	if existing != nil {
		return existing.Tldr
	}
	return ""
}

// validateMemWriteContent rejects the human-readable output of `mem read` when
// it is fed back to `mem write`. The read form includes a display label before
// the body; accepting it would persist that label as content. More importantly,
// a read performed after a failed write returns the old body, so a retry built
// from command substitution can silently discard the intended edit.
func validateMemWriteContent(existing *engram.Memory, content string) error {
	if existing == nil {
		return nil
	}
	if strings.HasPrefix(content, engram.MemoryLabel(*existing)+"\n") {
		return fmt.Errorf(
			"content begins with formatted `engram mem read` output; refusing to overwrite %s: "+
				"use `engram mem tldr` for a summary-only change, or retry `engram mem write` "+
				"with the intended body",
			engram.MemoryLabel(*existing),
		)
	}
	return nil
}

// memWriteIsTldrOnly identifies a write that re-supplies the unchanged body only
// to alter its tldr. Treating that as the surgical metadata operation makes the
// result explicit and avoids touching the memory's recency or logging a body
// update that did not happen.
func memWriteIsTldrOnly(existing *engram.Memory, content string, tldrFlagChanged bool) bool {
	return existing != nil && tldrFlagChanged && content == existing.Content
}

var memWriteCmd = &cobra.Command{
	Use:   "write <key-or-address> <content>",
	Short: "Write (upsert) a memory entry",
	Long: `Write (upsert) a memory entry, replacing its content body.

The output of 'engram mem read' is human-readable display text, not a round-trip
format; do not substitute it back into this command. To change only an existing
memory's summary, use 'engram mem tldr <key> <summary>', which leaves its content
untouched.`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
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

		content := strings.Join(args[1:], " ")
		tier := target.Tier
		key, err := target.storedKey()
		if err != nil {
			return err
		}
		existing, err := engram.ReadMemory(ctx, h.DB, tier, key)
		if err != nil {
			return fmt.Errorf("read existing memory before write: %w", err)
		}
		if err := validateMemWriteContent(existing, content); err != nil {
			return err
		}
		if memWriteIsTldrOnly(existing, content, cmd.Flag("tldr").Changed) {
			tldr := effectiveWriteTldr(existing, true, memTldr)
			ok, err := engram.SetMemoryTldr(ctx, h.DB, tier, key, tldr,
				engram.WithCurationSource(engram.SourceInteractive),
				engram.WithCurationScope(scopeName(target.Global)))
			if err != nil {
				return fmt.Errorf("memory was not changed: %w", err)
			}
			if !ok {
				return fmt.Errorf("memory was not changed: %s no longer exists", engram.MemoryLabel(*existing))
			}
			if tldr == "" {
				fmt.Printf("content unchanged; cleared tldr: %s\n", engram.MemoryLabel(*existing))
			} else {
				fmt.Printf("content unchanged; set tldr: %s\n", engram.MemoryLabel(*existing))
			}
			return nil
		}
		m := engram.Memory{
			TS:      time.Now().UnixMilli(),
			Tier:    tier,
			Key:     key,
			Content: content,
			Tldr:    effectiveWriteTldr(existing, cmd.Flag("tldr").Changed, memTldr),
		}
		if existing != nil {
			m.Trigger = existing.Trigger
		}
		if err := engram.WriteMemory(ctx, h.DB, m,
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(target.Global))); err != nil {
			return fmt.Errorf("memory was not changed: %w", err)
		}
		syncStandingIfTouched(ctx, h, target.Global, tier)
		if target.Agent != "" {
			fmt.Printf("stored in global %s %s layer: %s\n", target.Agent, tier, target.Key)
			return nil
		}
		fmt.Printf("stored in %s %s memory: %s\n", scopeName(target.Global), tier, target.Key)
		return nil
	},
}

var memReadCmd = &cobra.Command{
	Use:   "read <key-or-address>",
	Short: "Read a memory entry. Omit --tier to search all tiers.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, err := resolveMemoryTarget(cmd, args[0], "tier")
		if err != nil {
			return err
		}
		ctx := context.Background()
		h, err := target.openDBReadOnly(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		if target.Addressed {
			m, err := target.exactMemory(ctx, h)
			if err != nil {
				return err
			}
			if m == nil {
				return fmt.Errorf("not found: %s", args[0])
			}
			fmt.Printf("%s\n%s\n", engram.MemoryLabel(*m), m.Content)
			return nil
		}

		if !target.TierExplicit || target.Agent != "" || target.Global {
			tiers, err := target.viewTiers(cmd)
			if err != nil {
				return err
			}
			matches, err := engram.ListMemoriesForView(ctx, h.DB, tiers, target.Agent, target.Key)
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", target.Key)
			}
			for _, m := range matches {
				fmt.Printf("%s\n%s\n\n", engram.MemoryLabel(m), m.Content)
			}
			return nil
		}

		key, err := target.storedKey()
		if err != nil {
			return err
		}
		m, err := engram.ReadMemory(ctx, h.DB, target.Tier, key)
		if err != nil {
			return err
		}
		if m == nil {
			return fmt.Errorf("not found: %s/%s", target.Tier, target.Key)
		}
		fmt.Printf("%s\n%s\n", engram.MemoryLabel(*m), m.Content)
		return nil
	},
}

var (
	memListJSON        bool
	memListMissingTldr bool
	memListKeys        bool
	memListFull        bool
)

var memListCmd = &cobra.Command{
	Use:   "list [key-or-address]",
	Short: "List memory addresses and summaries. Omit --tier to list all active tiers.",
	Long: `List memories as a compact index of copyable address and tldr.

Omit --tier to list all active tiers. Cold memory is excluded from that default;
use --tier cold to list the archive. Use --keys for keys alone, --full to include
complete memory bodies, or --json for structured output.

Addresses include scope and tier, and can be passed directly to read, edit,
write, delete, tldr, list, and move:

  engram:long/deployment            current project's long/deployment
  engram:/preference/editor         global preference/editor
  engram:/preference/@codex/editor  global codex preference layer`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		selectedFormats := 0
		for _, selected := range []bool{memListJSON, memListKeys, memListFull} {
			if selected {
				selectedFormats++
			}
		}
		if selectedFormats > 1 {
			return fmt.Errorf("--json, --keys, and --full are mutually exclusive")
		}

		var raw string
		if len(args) > 0 {
			raw = args[0]
		}
		target, err := resolveMemoryTarget(cmd, raw, "tier")
		if err != nil {
			return err
		}

		ctx := context.Background()
		h, err := target.openDBReadOnly(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		tiers, err := target.viewTiers(cmd)
		if err != nil {
			return err
		}
		var memories []engram.Memory
		if target.Addressed {
			m, err := target.exactMemory(ctx, h)
			if err != nil {
				return err
			}
			if m != nil {
				memories = append(memories, *m)
			}
		} else {
			memories, err = engram.ListMemoriesForView(ctx, h.DB, tiers, target.Agent, target.Key)
			if err != nil {
				return err
			}
		}

		if memListMissingTldr {
			var missing []engram.Memory
			for _, m := range memories {
				// Invariants inject in full and never use a tldr, so they are
				// never "missing" one -- exclude them from the coverage view.
				if m.Tier != engram.TierInvariant && m.Tldr == "" {
					missing = append(missing, m)
				}
			}
			memories = missing
		}

		if memListJSON {
			out, err := json.Marshal(memories)
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		}

		if len(memories) == 0 {
			if memListMissingTldr {
				fmt.Println("all listed memories have tldrs")
			} else {
				fmt.Println("no memories")
			}
			return nil
		}
		switch {
		case memListKeys:
			printMemoryKeys(cmd.OutOrStdout(), memories)
		case memListFull:
			printMemories(cmd.OutOrStdout(), memories)
		default:
			printMemorySummaries(cmd.OutOrStdout(), memories, target.Global)
		}
		return nil
	},
}

var memDeleteCmd = &cobra.Command{
	Use:   "delete <key-or-address>",
	Short: "Delete a memory entry. Omit --tier to delete if unambiguous.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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

		tier := target.Tier
		key := target.Key
		if !target.TierExplicit {
			tiers, err := target.viewTiers(cmd)
			if err != nil {
				return err
			}
			matches, err := engram.ListMemoriesForView(ctx, h.DB, tiers, target.Agent, target.Key)
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", target.Key)
			}
			if len(matches) > 1 {
				fmt.Printf("ambiguous: %q found in multiple tiers, specify --tier:\n", target.Key)
				for _, m := range matches {
					fmt.Printf("  %s\n", engram.MemoryLabel(m))
				}
				return fmt.Errorf("ambiguous key")
			}
			tier = matches[0].Tier
			key = matches[0].Key
		} else {
			key, err = target.storedKey()
			if err != nil {
				return err
			}
		}

		if err := engram.DeleteMemory(ctx, h.DB, tier, key,
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(target.Global))); err != nil {
			return err
		}
		syncStandingIfTouched(ctx, h, target.Global, tier)
		return nil
	},
}

var memTldrCmd = &cobra.Command{
	Use:   "tldr <key-or-address> [summary]",
	Short: "Show or set the one-line inject summary of an existing memory",
	Long: `With a summary, set or replace a memory's tldr -- the one-line summary inject
surfaces in place of its full content -- without rewriting the content. This is
the safe way to curate what a memory shows at session start; plain 'write' would
require re-supplying the entire content body.

With no summary, print the memory's current tldr (or, when none is set, the
first-line fallback inject would use), so you can review before overwriting.

Pass an empty summary ("") to clear the tldr and fall back to the first line of
content. Omit --tier to resolve the key automatically when it is unambiguous.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, err := resolveMemoryTarget(cmd, args[0], "tier")
		if err != nil {
			return err
		}
		ctx := context.Background()
		var h *engram.DBHandle
		if len(args) == 1 {
			h, err = target.openDBReadOnly(ctx)
		} else {
			h, err = target.openDB(ctx)
		}
		if err != nil {
			return err
		}
		defer h.DB.Close()

		tier := target.Tier
		key := target.Key
		if !target.TierExplicit {
			tiers, err := target.viewTiers(cmd)
			if err != nil {
				return err
			}
			matches, err := engram.ListMemoriesForView(ctx, h.DB, tiers, target.Agent, target.Key)
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", target.Key)
			}
			if len(matches) > 1 {
				fmt.Printf("ambiguous: %q found in multiple tiers, specify --tier:\n", target.Key)
				for _, m := range matches {
					fmt.Printf("  %s\n", engram.MemoryLabel(m))
				}
				return fmt.Errorf("ambiguous key")
			}
			tier = matches[0].Tier
			key = matches[0].Key
		} else {
			key, err = target.storedKey()
			if err != nil {
				return err
			}
		}

		// Getter: no summary given -> print the current tldr, or the first-line
		// fallback inject would show when none is set.
		if len(args) == 1 {
			m, err := engram.ReadMemory(ctx, h.DB, tier, key)
			if err != nil {
				return err
			}
			if m == nil {
				return fmt.Errorf("not found: %s/%s", tier, target.Key)
			}
			if m.Tldr == "" {
				fmt.Printf("%s/%s: (no tldr set; inject falls back to first line)\n  %s\n", tier, target.Key, m.InjectSummary())
			} else {
				fmt.Printf("%s/%s: %s\n", tier, target.Key, m.Tldr)
			}
			return nil
		}

		// Setter: summary given.
		tldr := strings.TrimSpace(strings.Join(args[1:], " "))
		ok, err := engram.SetMemoryTldr(ctx, h.DB, tier, key, tldr,
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(target.Global)))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("not found: %s/%s", tier, target.Key)
		}
		if tldr == "" {
			fmt.Printf("cleared tldr: %s/%s\n", tier, target.Key)
		} else {
			fmt.Printf("set tldr: %s/%s\n", tier, target.Key)
		}
		return nil
	},
}

var (
	moveFrom string
	moveTo   string
	moveToDB string
)

var memMoveCmd = &cobra.Command{
	Use:   "move <key-or-address>",
	Short: "Move a memory to a different tier, or to the other database",
	Long: `Move a memory to a different tier, and optionally to the other database.

The source tier is inferred automatically unless --from is specified.
Use --to to specify the destination tier (required).

By default the move stays within the same database (global with -g/--agent,
otherwise the project DB). Pass --to-db to relocate across databases:

  --to-db global    move into ~/.engram        (e.g. promote a project rule)
  --to-db project   move into ./.engram        (e.g. scope a global rule to
                                                 this repo only)

An agent-layer key is de-scoped to its base key when it lands in a project,
which has no layers.

Tiers: invariant, preference, long, short, cold`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, err := resolveMemoryTarget(cmd, args[0], "from")
		if err != nil {
			return err
		}
		ctx := context.Background()
		src, err := target.openDB(ctx)
		if err != nil {
			return err
		}
		defer src.DB.Close()

		if moveTo == "" {
			return fmt.Errorf("--to is required")
		}
		toTier, err := engram.ParseTier(moveTo)
		if err != nil {
			return fmt.Errorf("--to: %w", err)
		}
		if target.Agent != "" && !engram.IsStandingTier(toTier) {
			return fmt.Errorf("--agent only applies to global invariant/preference memory; --to must be invariant or preference")
		}

		// Resolve the source tier and its stored key against the source DB.
		from := target.Tier
		key := target.Key
		if !target.TierExplicit {
			tiers, err := target.viewTiers(cmd)
			if err != nil {
				return err
			}
			matches, err := engram.ListMemoriesForView(ctx, src.DB, tiers, target.Agent, target.Key)
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return fmt.Errorf("not found: %s", target.Key)
			}
			if len(matches) > 1 {
				fmt.Printf("ambiguous: %q found in multiple tiers, specify --from:\n", target.Key)
				for _, m := range matches {
					fmt.Printf("  %s\n", engram.MemoryLabel(m))
				}
				return fmt.Errorf("ambiguous key")
			}
			from = matches[0].Tier
			key = matches[0].Key
		} else {
			// --from deliberately remains a raw tier string so a user can recover
			// rows written by older versions under a noncanonical tier. Every
			// destination is parsed above and therefore canonical.
			key, err = target.storedKey()
			if err != nil {
				return err
			}
		}

		// Decide the destination database. Default: stay put.
		srcGlobal := target.Global
		destGlobal := srcGlobal
		if cmd.Flag("to-db").Changed {
			switch moveToDB {
			case "global":
				destGlobal = true
			case "project":
				destGlobal = false
			default:
				return fmt.Errorf(`--to-db must be "global" or "project"`)
			}
		}

		// Same-database move: the common case, unchanged behavior.
		if destGlobal == srcGlobal {
			if err := engram.MoveMemory(ctx, src.DB, key, from, toTier,
				engram.WithCurationSource(engram.SourceInteractive),
				engram.WithCurationScope(scopeName(srcGlobal))); err != nil {
				return err
			}
			syncStandingIfTouched(ctx, src, target.Global, from, toTier)
			fmt.Printf("moved %q from %s to %s\n", target.Key, from, toTier)
			return nil
		}

		// Cross-database move.
		dst, err := openScopeDB(ctx, destGlobal)
		if err != nil {
			return err
		}
		defer dst.DB.Close()

		// A project has no agent layers, so de-scope a layer key on the way in.
		dstKey := key
		if !destGlobal {
			if _, base, ok := engram.ParseAgentLayerKey(key); ok {
				dstKey = base
			}
		}
		if err := engram.MoveMemoryAcrossDB(ctx, src.DB, dst.DB, key, dstKey, from, toTier,
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(srcGlobal)),
			engram.WithCurationToScope(scopeName(destGlobal))); err != nil {
			return err
		}
		// Re-render the global standing files if a standing tier crossed the global
		// channel in either direction (a preference left global, or arrived there).
		if engram.IsStandingTier(from) || engram.IsStandingTier(toTier) {
			globalDB := src.DB
			if destGlobal {
				globalDB = dst.DB
			}
			if _, err := engram.SyncStandingMemory(ctx, globalDB); err != nil {
				fmt.Fprintf(os.Stderr, "engram: sync standing memory: %v\n", err)
			}
		}
		fmt.Printf("moved %q from %s %s to %s %s\n", target.Key, scopeName(srcGlobal), from, scopeName(destGlobal), toTier)
		return nil
	},
}

var memPopCmd = &cobra.Command{
	Use:   "pop",
	Short: "Read and remove the most recent short-term memory",
	RunE: func(cmd *cobra.Command, args []string) error {
		if memAgent != "" {
			return fmt.Errorf("--agent only applies to global invariant/preference memory")
		}
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		m, err := engram.PopMemory(ctx, h.DB, engram.Tier(memTier))
		if err != nil {
			return err
		}
		if m == nil {
			fmt.Printf("no %s memories\n", memTier)
			return nil
		}
		fmt.Printf("[%s] %s\n%s\n", m.Tier, m.Key, m.Content)
		return nil
	},
}

func init() {
	memWriteCmd.Flags().StringVar(&memTldr, "tldr", "", fmt.Sprintf("one-line summary shown at inject time (max %d chars; falls back to the first line of content)", engram.MaxTldrLen))
	memListCmd.Flags().BoolVar(&memListJSON, "json", false, "output as JSON array")
	memListCmd.Flags().BoolVar(&memListMissingTldr, "missing-tldr", false, "list only memories with no tldr (excludes invariants)")
	memListCmd.Flags().BoolVar(&memListKeys, "keys", false, "output visible keys only, one per line")
	memListCmd.Flags().BoolVar(&memListFull, "full", false, "include complete memory bodies")
	memMoveCmd.Flags().StringVar(&moveFrom, "from", "", "source tier (inferred if omitted; noncanonical values accepted for legacy recovery)")
	memMoveCmd.Flags().StringVar(&moveTo, "to", "", "destination tier (required)")
	memMoveCmd.Flags().StringVar(&moveToDB, "to-db", "", `destination database: "global" or "project" (default: same as source)`)

	memCmd.AddCommand(memWriteCmd, memReadCmd, memListCmd, memDeleteCmd, memMoveCmd, memPopCmd, memTldrCmd)
}
