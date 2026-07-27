package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var templateGlobal bool

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage directive templates with named blanks",
	Long: `Manage fixed directive templates with named blanks like {artifact} or
{principle}, the "madlibs" substrate a later layer will learn to select and fill.

A template's text carries named blanks; the blanks draw candidate words from the
per-slot vocabulary managed by 'engram vocab'. Render fills one binding; enumerate
expands the cross-product of the template's slots against the current vocabulary.

Substitution is literal -- no morphology. Without -g templates live in the project
database, with -g in the global (~/.engram) one.

  engram template add greet "Write the {artifact} with {principle}."
  engram vocab add artifact memo
  engram vocab add principle clarity
  engram template render greet --set artifact=memo --set principle=clarity
  engram template enumerate greet`,
}

var templateAddCmd = &cobra.Command{
	Use:   "add <key> <text>",
	Short: "Add or overwrite a template (upsert); prints detected slots",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openScopeDB(ctx, templateGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		key := args[0]
		text := strings.Join(args[1:], " ")
		if err := engram.UpsertTemplate(ctx, h.DB, engram.Template{Key: key, Text: text, Tldr: templateTldr},
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(templateGlobal))); err != nil {
			return err
		}
		slots := engram.DetectSlots(text)
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "stored in %s templates: %s\n", scopeName(templateGlobal), key)
		if len(slots) == 0 {
			fmt.Fprintln(out, "detected slots: (none)")
		} else {
			fmt.Fprintf(out, "detected slots: %s\n", strings.Join(slots, ", "))
		}
		return nil
	},
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List templates",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := context.Background()
		h, err := openScopeDB(ctx, templateGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		templates, err := engram.ListTemplates(ctx, h.DB)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(templates) == 0 {
			fmt.Fprintln(out, "no templates")
			return nil
		}
		for _, t := range templates {
			slots := engram.DetectSlots(t.Text)
			fmt.Fprintf(out, "%s\t[%s]\t%s\n", t.Key, strings.Join(slots, ", "), t.Text)
		}
		return nil
	},
}

var templateShowCmd = &cobra.Command{
	Use:   "show <key>",
	Short: "Show a template's text, tldr, and detected slots",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openScopeDB(ctx, templateGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		t, err := engram.GetTemplate(ctx, h.DB, args[0])
		if err != nil {
			return err
		}
		if t == nil {
			return fmt.Errorf("template not found: %s", args[0])
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "key:   %s\n", t.Key)
		if t.Tldr != "" {
			fmt.Fprintf(out, "tldr:  %s\n", t.Tldr)
		}
		fmt.Fprintf(out, "slots: %s\n", strings.Join(engram.DetectSlots(t.Text), ", "))
		fmt.Fprintf(out, "text:  %s\n", t.Text)
		return nil
	},
}

var templateDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Delete a template",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openScopeDB(ctx, templateGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		if err := engram.DeleteTemplate(ctx, h.DB, args[0],
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(templateGlobal))); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "deleted %s template: %s\n", scopeName(templateGlobal), args[0])
		return nil
	},
}

var templateSet []string

var templateRenderCmd = &cobra.Command{
	Use:   "render <key> --set slot=word [--set ...]",
	Short: "Render a template with one binding per slot",
	Long: `Render a template, substituting each {slot} with the word bound to it via
--set slot=word. A slot appearing more than once is filled at every occurrence.
Extra bindings with no matching slot are ignored; if any slot has no binding the
command fails and names the unbound slots.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openScopeDB(ctx, templateGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		t, err := engram.GetTemplate(ctx, h.DB, args[0])
		if err != nil {
			return err
		}
		if t == nil {
			return fmt.Errorf("template not found: %s", args[0])
		}
		bindings, err := parseBindings(templateSet)
		if err != nil {
			return err
		}
		rendered, err := engram.RenderTemplate(t.Text, bindings)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), rendered)
		return nil
	},
}

// parseBindings turns repeated --set slot=word arguments into a binding map,
// erroring on a malformed argument that has no '=' or an empty slot name.
func parseBindings(sets []string) (map[string]string, error) {
	bindings := make(map[string]string, len(sets))
	for _, s := range sets {
		slot, word, ok := strings.Cut(s, "=")
		if !ok || slot == "" {
			return nil, fmt.Errorf("malformed --set %q: want slot=word", s)
		}
		bindings[slot] = word
	}
	return bindings, nil
}

var (
	enumerateLimit  int
	enumerateSample int
)

var templateEnumerateCmd = &cobra.Command{
	Use:   "enumerate <key>",
	Short: "Expand a template's slots against the current vocabulary",
	Long: `Expand a template's cross-product against the current vocabulary: one rendered
line per combination of slot bindings. Every slot must have vocabulary or the
command fails and names the empty slots.

--limit N caps the output to the first N cells in deterministic order.
--sample N instead draws a uniform random sample of N distinct cells, for a
stochastic generativity estimate; if N is at least the full product, the whole
product is shown. Whenever fewer cells are shown than the product holds, the
total size and the number omitted are reported -- output is never truncated
silently.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openScopeDB(ctx, templateGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		result, err := engram.Enumerate(ctx, h.DB, args[0], engram.EnumerateOptions{
			Limit:  enumerateLimit,
			Sample: enumerateSample,
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		for _, line := range result.Rendered {
			fmt.Fprintln(out, line)
		}
		if result.Omitted > 0 {
			mode := "shown"
			if result.Sampled {
				mode = "sampled"
			}
			fmt.Fprintf(out, "(%d of %d cells %s, %d omitted)\n",
				len(result.Rendered), result.Total, mode, result.Omitted)
		} else {
			fmt.Fprintf(out, "(%d cells, full product)\n", result.Total)
		}
		return nil
	},
}

var templateTldr string

func init() {
	templateCmd.PersistentFlags().BoolVarP(&templateGlobal, "global", "g", false, "use the global (~/.engram) database")
	templateAddCmd.Flags().StringVar(&templateTldr, "tldr", "", "optional one-line summary of the template")
	templateRenderCmd.Flags().StringArrayVar(&templateSet, "set", nil, "bind a slot: --set slot=word (repeatable)")
	templateEnumerateCmd.Flags().IntVar(&enumerateLimit, "limit", 0, "cap output to the first N cells (0 for all)")
	templateEnumerateCmd.Flags().IntVar(&enumerateSample, "sample", 0, "draw a uniform random sample of N distinct cells")

	templateCmd.AddCommand(templateAddCmd, templateListCmd, templateShowCmd, templateDeleteCmd, templateRenderCmd, templateEnumerateCmd)
	markExperimental(templateCmd, "template-vocab")
	rootCmd.AddCommand(templateCmd)
}
