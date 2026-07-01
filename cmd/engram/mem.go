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

  invariant  (-g, --global)  Identity, codename, personality. Rarely changed.
                             Applies to all projects.
  preference (-g, --global)  Code and behavior rules. Add and remove over time.
                             Applies to all projects.
  long                       Settled project decisions and facts.
  short                      In-flight context, conversation stack, backlog.
  cold                       Low-priority archive. Injected as index only.

The -g/--global flag selects which database a memory lives in, independent of its
tier: with -g, memories live in ~/.engram/mem.db; without it, in .engram/mem.db at
the current project root. Tier and database are orthogonal -- any tier can be
stored in either.

At session start inject surfaces invariant and preference from the global database,
long and short from both the global and project databases (merged, global first),
and cold from both as an index only (keys and one-line summaries; fetch full
contents on demand). So a global long-term memory loads in every project, while a
project long-term memory loads only in that project.

Agent-specific layers are also global and are selected with --agent <name>; they
apply on top of the primary invariant/preference tiers only when inject is called
with the same agent.

Common operations:
  engram mem -g -t invariant list          list all global invariants
  engram mem -g list personality           list primary + agent personality layers
  engram mem -g -t invariant read <key>    read a specific invariant
  engram mem -g -t preference write <key> <content>
  engram mem -g --agent codex -t preference write <key> <content>
  engram mem -t long write <key> <content> write to project long-term memory
  engram mem search <query>                full-text search across all tiers
  engram inject                            print session-start context as JSON

Run 'engram mem <subcommand> --help' for details on each operation.`,
}

// shared flags
var memGlobal bool
var memTier string
var memAgent string

func memUsesGlobal() bool {
	return memGlobal || memAgent != ""
}

func openMemDB(ctx context.Context) (*engram.DBHandle, error) {
	if memUsesGlobal() {
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

func init() {
	memCmd.PersistentFlags().BoolVarP(&memGlobal, "global", "g", false, "use global (~/.engram) database")
	memCmd.PersistentFlags().StringVarP(&memTier, "tier", "t", string(engram.TierShort), "memory tier (invariant, preference, long, short, cold)")
	memCmd.PersistentFlags().StringVar(&memAgent, "agent", "", "agent layer for global invariant/preference memory (implies --global)")
}
