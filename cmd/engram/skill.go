package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var (
	skillGlobal            bool
	skillTrigger           string
	skillTldr              string
	skillDiscoverAll       bool
	skillDiscoverJSON      bool
	skillClassifyAs        string
	skillClassifyRationale string
	skillClassifySkillKey  string
	skillClassifyCommand   string
	skillClassifyStdin     bool
	skillSearchLimit       int
	skillSearchFull        bool
)

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Discover and manage task-triggered workflows",
	Long: `Skills are focused workflows retrieved when their trigger matches the task.

A skill is stored as long-term memory: trigger says when to retrieve it, tldr
says what outcome it provides, and content contains the full instructions.
Without --global skills belong to the current project; with --global they are
available in every project.`,
}

func openSkillDB(ctx context.Context) (*engram.DBHandle, error) {
	return openScopeDB(ctx, skillGlobal)
}

func openSkillDBReadOnly(ctx context.Context) (*engram.DBHandle, error) {
	return openScopeDBReadOnly(ctx, skillGlobal)
}

var skillWriteCmd = &cobra.Command{
	Use:   "write <key> <content>",
	Short: "Write or update a task-triggered skill",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openSkillDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		existing, err := engram.ReadMemory(ctx, h.DB, engram.TierLong, args[0])
		if err != nil {
			return err
		}
		trigger := strings.TrimSpace(skillTrigger)
		if !cmd.Flag("trigger").Changed && existing != nil {
			trigger = existing.Trigger
		}
		if trigger == "" {
			return fmt.Errorf("--trigger is required for a skill")
		}
		tldr := strings.TrimSpace(skillTldr)
		if !cmd.Flag("tldr").Changed && existing != nil {
			tldr = existing.Tldr
		}
		if err := engram.WriteMemory(ctx, h.DB, engram.Memory{
			TS:      time.Now().UnixMilli(),
			Tier:    engram.TierLong,
			Key:     args[0],
			Content: strings.Join(args[1:], " "),
			Tldr:    tldr,
			Trigger: trigger,
		}, engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(skillGlobal))); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "stored %s skill: %s\n", scopeName(skillGlobal), args[0])
		return nil
	},
}

// adoptedSkill returns an in-place metadata update for an existing long-term
// memory. Content, timestamp, and an omitted tldr are deliberately preserved:
// adoption classifies an existing procedure; it does not rewrite or reorder it.
func adoptedSkill(existing *engram.Memory, trigger string, tldrChanged bool, tldr string) (engram.Memory, error) {
	if existing == nil || existing.Tier != engram.TierLong {
		return engram.Memory{}, fmt.Errorf("long-term memory not found")
	}
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		return engram.Memory{}, fmt.Errorf("--trigger is required to adopt a skill")
	}
	m := *existing
	m.Trigger = trigger
	if tldrChanged {
		m.Tldr = strings.TrimSpace(tldr)
	}
	return m, nil
}

var skillAdoptCmd = &cobra.Command{
	Use:   "adopt <key>",
	Short: "Adopt an existing long-term memory as a skill without rewriting it",
	Long: `Add or replace the retrieval trigger on an existing long-term memory.

The memory's content and timestamp are preserved. Its tldr is also preserved
unless --tldr is explicitly supplied; pass --tldr "" to clear it. This command
is safe to rerun on an already adopted skill.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openSkillDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		existing, err := engram.ReadMemory(ctx, h.DB, engram.TierLong, args[0])
		if err != nil {
			return err
		}
		m, err := adoptedSkill(existing, skillTrigger, cmd.Flag("tldr").Changed, skillTldr)
		if err != nil {
			return fmt.Errorf("adopt skill %q: %w", args[0], err)
		}
		wasSkill := existing.Trigger != ""
		if err := engram.WriteMemory(ctx, h.DB, m,
			engram.WithCurationAction(engram.CurationSkillAdopt),
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(skillGlobal))); err != nil {
			return err
		}
		verb := "adopted"
		if wasSkill {
			verb = "updated"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s skill: %s\n", verb, scopeName(skillGlobal), args[0])
		return nil
	},
}

var skillReadCmd = &cobra.Command{
	Use:   "read <key>",
	Short: "Read a skill's trigger, outcome, and instructions",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openSkillDBReadOnly(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		m, err := engram.ReadMemory(ctx, h.DB, engram.TierLong, args[0])
		if err != nil {
			return err
		}
		if m == nil || m.Trigger == "" {
			return fmt.Errorf("skill not found: %s", args[0])
		}
		printSkill(cmd.OutOrStdout(), *m, skillGlobal)
		return nil
	},
}

func printSkill(w io.Writer, m engram.Memory, global bool) {
	fmt.Fprintf(w, "[%s skill/%s]\nTrigger: %s\nTLDR: %s\n\n%s\n",
		scopeName(global), m.Key, m.Trigger, m.Tldr, m.Content)
}

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List skills and their retrieval triggers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := context.Background()
		h, err := openSkillDBReadOnly(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		skills, err := engram.ListSkills(ctx, h.DB)
		if err != nil {
			return err
		}
		if len(skills) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no skills")
			return nil
		}
		for _, skill := range skills {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", skill.Key, skill.Trigger, skill.InjectSummary())
		}
		return nil
	},
}

var skillSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search skill names, triggers, outcomes, and instructions",
	Long: `Search skill names, triggers, outcomes, and instructions.

By default every ranked match is shown as a compact trigger-index row. Use
--limit to bound the result set, or --full to include complete instructions.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateSearchLimit(skillSearchLimit); err != nil {
			return err
		}
		ctx := context.Background()
		h, err := openSkillDBReadOnly(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		skills, err := engram.SearchSkills(ctx, h.DB, args[0])
		if err != nil {
			return err
		}
		skills, omitted, err := limitSearchResults(skills, skillSearchLimit)
		if err != nil {
			return err
		}
		if len(skills) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no results (search is a fallback; the injected Skills index already lists every trigger in context -- check there before concluding no skill exists)")
			return nil
		}
		for i, skill := range skills {
			if skillSearchFull {
				if i > 0 {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				printSkill(cmd.OutOrStdout(), skill, skillGlobal)
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", skill.Key, skill.Trigger, skill.InjectSummary())
		}
		reportOmittedSearchResults(cmd.ErrOrStderr(), len(skills), omitted)
		return nil
	},
}

var skillDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Delete a skill",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openSkillDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		m, err := engram.ReadMemory(ctx, h.DB, engram.TierLong, args[0])
		if err != nil {
			return err
		}
		if m == nil || m.Trigger == "" {
			return fmt.Errorf("skill not found: %s", args[0])
		}
		return engram.DeleteMemory(ctx, h.DB, engram.TierLong, args[0],
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(skillGlobal)))
	},
}

var skillDiscoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Find new, changed, or removed automation catalog entries",
	Long: `Find conventional automation entry points without executing them and compare
each one to its durable classification.

Review each candidate's documentation and call sites, then classify it as:

  direct tool    one callable operation with a stable invocation and result
  skill member   part of a workflow requiring instructions or judgment
  internal       implementation detail called by another entry point
  review         unclear, stale, or requiring user input
  ignore         generated, vendored, example, or deliberately uncataloged

By default only new, changed, and removed entries are shown. --all includes
unchanged classifications for catalog inspection. --json produces structured
review items suitable for preparing a batch 'skill classify --stdin' call.

Classification records the judgment and this candidate's content digest. A
later content change retains and displays the previous verdict rather than
invalidating unrelated entries.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := context.Background()
		root, err := engram.FindProjectRoot(effectiveCWD())
		if err != nil {
			return fmt.Errorf("skill discover: %w", err)
		}
		candidates, err := engram.DiscoverAutomation(root)
		if err != nil {
			return err
		}
		db, err := engram.OpenProjectDB(ctx, root)
		if err != nil {
			return err
		}
		defer db.Close()
		review, err := engram.ReconcileAutomationCatalog(ctx, db, candidates, skillDiscoverAll)
		if err != nil {
			return err
		}
		if skillDiscoverJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(review.Items)
		}
		if len(review.Items) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No automation entries require classification.")
			return nil
		}
		for _, item := range review.Items {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s", item.State, item.Candidate.Kind, item.Candidate.Path)
			if item.Previous != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "\tprevious=%s", item.Previous.Classification)
				if item.Previous.Rationale != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "\t%s", item.Previous.Rationale)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
		fmt.Fprintln(cmd.OutOrStdout(), "\nClassify current entries with `engram skill classify`; resolve removed entries with `--as removed`.")
		return nil
	},
}

type skillClassificationInput struct {
	Path           string `json:"path"`
	Classification string `json:"classification"`
	Rationale      string `json:"rationale,omitempty"`
	SkillKey       string `json:"skill_key,omitempty"`
	Command        string `json:"command,omitempty"`
}

type automationCatalogExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func classifyAutomationInput(ctx context.Context, db automationCatalogExecer, root string, candidates map[string]engram.AutomationCandidate, input skillClassificationInput) error {
	path := filepath.ToSlash(strings.TrimSpace(input.Path))
	if path == "" {
		return fmt.Errorf("classification path is required")
	}
	if input.Classification == "removed" {
		if _, exists := candidates[path]; exists {
			return fmt.Errorf("%s still exists; removed only resolves missing catalog entries", path)
		}
		removed, err := engram.RemoveAutomationClassification(ctx, db, path)
		if err != nil {
			return err
		}
		if !removed {
			return fmt.Errorf("removed automation entry not found: %s", path)
		}
		return nil
	}
	classification := engram.AutomationClassification(input.Classification)
	if !engram.ValidAutomationClassification(classification) {
		return fmt.Errorf("--as must be direct-tool, skill-member, internal, review, ignore, or removed")
	}
	candidate, exists := candidates[path]
	if !exists {
		return fmt.Errorf("current automation candidate not found: %s", path)
	}
	command := strings.TrimSpace(input.Command)
	if classification == engram.AutomationDirectTool && command == "" {
		var err error
		command, err = engram.InferAutomationInvocation(root, candidate)
		if err != nil {
			return fmt.Errorf("infer invocation for %s: %w", path, err)
		}
		if command == "" {
			return fmt.Errorf("direct tool %s needs --command; no script invocation can be inferred", path)
		}
	}
	return engram.ClassifyAutomation(ctx, db, candidate, classification,
		input.Rationale, input.SkillKey, command)
}

var skillClassifyCmd = &cobra.Command{
	Use:   "classify [path]",
	Short: "Persist the verdict for one or more automation candidates",
	Long: `Classify a current automation candidate at its current content digest.

For one entry, pass its path and --as. Direct scripts infer their runner command;
task manifests and other non-script tools require --command. Skill members may
use --skill to group several files into one proposed skill.

For a batch, pass --stdin and a JSON array of objects with path, classification,
and optional rationale, skill_key, and command fields. Use classification
"removed" to explicitly retire a catalog entry whose path no longer exists.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if skillClassifyStdin {
			if len(args) != 0 {
				return fmt.Errorf("path cannot be used with --stdin")
			}
			return nil
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		root, err := engram.FindProjectRoot(effectiveCWD())
		if err != nil {
			return fmt.Errorf("skill classify: %w", err)
		}
		candidates, err := engram.DiscoverAutomation(root)
		if err != nil {
			return err
		}
		byPath := make(map[string]engram.AutomationCandidate, len(candidates))
		for _, candidate := range candidates {
			byPath[candidate.Path] = candidate
		}
		h, err := openScopeDB(ctx, false)
		if err != nil {
			return err
		}
		defer h.DB.Close()
		tx, err := h.DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin classification batch: %w", err)
		}
		defer tx.Rollback()

		var inputs []skillClassificationInput
		if skillClassifyStdin {
			dec := json.NewDecoder(cmd.InOrStdin())
			dec.DisallowUnknownFields()
			if err := dec.Decode(&inputs); err != nil {
				return fmt.Errorf("decode classifications: %w", err)
			}
			if len(inputs) == 0 {
				return fmt.Errorf("classification batch is empty")
			}
		} else {
			inputs = []skillClassificationInput{{
				Path: args[0], Classification: skillClassifyAs,
				Rationale: skillClassifyRationale, SkillKey: skillClassifySkillKey,
				Command: skillClassifyCommand,
			}}
		}
		for _, input := range inputs {
			if err := classifyAutomationInput(ctx, tx, root, byPath, input); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit classifications: %w", err)
		}
		for _, input := range inputs {
			fmt.Fprintf(cmd.OutOrStdout(), "classified %s as %s\n", input.Path, input.Classification)
		}
		return nil
	},
}

func init() {
	for _, cmd := range []*cobra.Command{skillWriteCmd, skillAdoptCmd, skillReadCmd, skillListCmd, skillSearchCmd, skillDeleteCmd} {
		cmd.Flags().BoolVarP(&skillGlobal, "global", "g", false, "use global (~/.engram) skills")
	}
	skillWriteCmd.Flags().StringVar(&skillTrigger, "trigger", "", fmt.Sprintf("task condition that retrieves the skill (required; max %d chars)", engram.MaxTriggerLen))
	skillWriteCmd.Flags().StringVar(&skillTldr, "tldr", "", fmt.Sprintf("one-line description of the skill's outcome (max %d chars)", engram.MaxTldrLen))
	skillAdoptCmd.Flags().StringVar(&skillTrigger, "trigger", "", fmt.Sprintf("task condition that retrieves the skill (required; max %d chars)", engram.MaxTriggerLen))
	skillAdoptCmd.Flags().StringVar(&skillTldr, "tldr", "", fmt.Sprintf("replace the existing outcome summary (max %d chars; omitted preserves it)", engram.MaxTldrLen))
	skillDiscoverCmd.Flags().BoolVar(&skillDiscoverAll, "all", false, "include unchanged classified entries")
	skillDiscoverCmd.Flags().BoolVar(&skillDiscoverJSON, "json", false, "output structured review items as JSON")
	skillClassifyCmd.Flags().StringVar(&skillClassifyAs, "as", "", "classification: direct-tool, skill-member, internal, review, ignore, or removed")
	skillClassifyCmd.Flags().StringVar(&skillClassifyRationale, "rationale", "", fmt.Sprintf("optional one-line judgment (max %d chars)", engram.MaxAutomationRationaleLen))
	skillClassifyCmd.Flags().StringVar(&skillClassifySkillKey, "skill", "", "skill key grouping this skill member")
	skillClassifyCmd.Flags().StringVar(&skillClassifyCommand, "command", "", "exact invocation for a direct tool (inferred for scripts)")
	skillClassifyCmd.Flags().BoolVar(&skillClassifyStdin, "stdin", false, "read a JSON array of classifications from stdin")
	skillSearchCmd.Flags().IntVar(&skillSearchLimit, "limit", defaultSearchLimit, "maximum number of matches to show (0 for all)")
	skillSearchCmd.Flags().BoolVar(&skillSearchFull, "full", false, "include complete skill instructions")
	skillCmd.AddCommand(skillWriteCmd, skillAdoptCmd, skillReadCmd, skillListCmd, skillSearchCmd, skillDeleteCmd, skillDiscoverCmd, skillClassifyCmd)
	rootCmd.AddCommand(skillCmd)
}
