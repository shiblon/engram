package main

import (
	"context"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var memCmd = &cobra.Command{
	Use:   "mem",
	Short: "Manage agent memory (invariants, preferences, long-term, short-term, cold)",
	Long: `Manage engram memories across five tiers:

  invariant   Identity: codename, personality. Global; rarely changes.
  preference  Behavioral rules and standing defaults. Global (every project) or
              project-scoped.
  long        Settled decisions, facts, durable backlog.
  short       In-flight working state, the live stack.
  cold        Low-priority archive. Injected as an index only.

Tier and database are orthogonal. The -g/--global flag selects the DATABASE:
with -g a memory lives in ~/.engram, without it in the project's .engram. Any of
the five tiers can live in either; move one between them with
'move --to-db global|project'.

Long vs short is one test, not a forecast: can you name the event that makes the
memory obsolete? If yes it is short (record it: "Retire when: ..."); if no it is
long. Any concrete todo list is short.

At session start inject merges both databases (global first): invariant from global
only; preference, long, and short from both; cold from both as an index. A global
memory loads in every project, a project memory only in its own.

Every entry carries a one-line tldr (set with --tldr on write; falls back to the
first line of content). Inject surfaces the tldr for every tier except invariants,
which show in full; read an entry's full text with 'engram mem read <key>'.

Agent layers are global and selected with --agent <name>; they apply on top of the
primary invariant/preference tiers only when inject runs with the same agent.

Common operations:
  engram mem -g -t invariant list          list all global invariants
  engram mem -g list personality           list primary + agent personality layers
  engram mem -g -t preference write <key> <content> --tldr "<summary>"
  engram mem -t long write <key> <content> write to project long-term memory
  engram mem move <key> --to long --to-db global   relocate across tier and database
  engram mem search <query>                full-text search across all tiers
  engram inject                            print session-start context as JSON

Run 'engram mem <subcommand> --help' for details on each operation.`,
	PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
		tier, err := engram.ParseTier(memTier)
		if err != nil {
			return err
		}
		memTier = string(tier)
		return nil
	},
}

// shared flags
var memGlobal bool
var memTier string
var memAgent string

func memUsesGlobal() bool {
	return memGlobal || memAgent != ""
}

func openMemDB(ctx context.Context) (*engram.DBHandle, error) {
	return openScopeDB(ctx, memUsesGlobal())
}

// openScopeDB opens either the global (~/.engram) or the project (.engram)
// database explicitly, independent of the command's flags. The move command uses
// it to open a second, cross-scope handle when relocating a memory between the two
// databases.
func openScopeDB(ctx context.Context, global bool) (*engram.DBHandle, error) {
	if global {
		db, err := engram.OpenGlobalDB(ctx)
		if err != nil {
			return nil, err
		}
		path, _ := engram.GlobalDBPath()
		return &engram.DBHandle{DB: db, Path: path}, nil
	}
	root, err := engram.FindProjectRoot(effectiveCWD())
	if err != nil {
		return nil, err
	}
	db, err := engram.OpenProjectDB(ctx, root)
	if err != nil {
		return nil, err
	}
	return &engram.DBHandle{DB: db, Path: engram.DBPath(root)}, nil
}

// scopeName is the user-facing word for a database scope.
func scopeName(global bool) string {
	if global {
		return "global"
	}
	return "project"
}

func init() {
	memCmd.PersistentFlags().BoolVarP(&memGlobal, "global", "g", false, "use global (~/.engram) database")
	memCmd.PersistentFlags().StringVarP(&memTier, "tier", "t", string(engram.TierShort), "memory tier (invariant, preference, long, short, cold)")
	memCmd.PersistentFlags().StringVar(&memAgent, "agent", "", "agent layer for global invariant/preference memory (implies --global)")
}
