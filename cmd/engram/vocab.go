package main

import (
	"context"
	"fmt"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var vocabGlobal bool

var vocabCmd = &cobra.Command{
	Use:   "vocab",
	Short: "Manage the per-slot vocabulary templates draw from",
	Long: `Manage the flat per-slot vocabulary the named blanks in 'engram template' draw
from. Each slot (e.g. artifact, principle) holds a set of candidate words; a word
cannot appear twice in one slot.

Without -g the vocabulary lives in the project database, with -g in the global
(~/.engram) one.

  engram vocab add artifact memo
  engram vocab add artifact report
  engram vocab list artifact
  engram vocab delete artifact report`,
}

var vocabAddCmd = &cobra.Command{
	Use:   "add <slot> <word>",
	Short: "Add a word to a slot's vocabulary",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openScopeDB(ctx, vocabGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		slot, word := args[0], args[1]
		added, err := engram.AddVocab(ctx, h.DB, slot, word,
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(vocabGlobal)))
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if !added {
			fmt.Fprintf(out, "already present in %s vocab: %s/%s\n", scopeName(vocabGlobal), slot, word)
			return nil
		}
		fmt.Fprintf(out, "added to %s vocab: %s/%s\n", scopeName(vocabGlobal), slot, word)
		return nil
	},
}

var vocabListCmd = &cobra.Command{
	Use:   "list [slot]",
	Short: "List vocabulary, optionally for one slot",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openScopeDB(ctx, vocabGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		var slot string
		if len(args) > 0 {
			slot = args[0]
		}
		entries, err := engram.ListVocab(ctx, h.DB, slot)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(entries) == 0 {
			fmt.Fprintln(out, "no vocabulary")
			return nil
		}
		for _, e := range entries {
			fmt.Fprintf(out, "%s\t%s\n", e.Slot, e.Word)
		}
		return nil
	},
}

var vocabDeleteCmd = &cobra.Command{
	Use:   "delete <slot> <word>",
	Short: "Delete a word from a slot's vocabulary",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openScopeDB(ctx, vocabGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		slot, word := args[0], args[1]
		if err := engram.DeleteVocab(ctx, h.DB, slot, word,
			engram.WithCurationSource(engram.SourceInteractive),
			engram.WithCurationScope(scopeName(vocabGlobal))); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "deleted from %s vocab: %s/%s\n", scopeName(vocabGlobal), slot, word)
		return nil
	},
}

func init() {
	vocabCmd.PersistentFlags().BoolVarP(&vocabGlobal, "global", "g", false, "use the global (~/.engram) database")
	vocabCmd.AddCommand(vocabAddCmd, vocabListCmd, vocabDeleteCmd)
	markExperimental(vocabCmd, "template-vocab")
	rootCmd.AddCommand(vocabCmd)
}
