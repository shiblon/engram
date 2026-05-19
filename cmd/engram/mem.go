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

Global memories (invariant, preference) are stored in ~/.engram/mem.db and
injected at the start of every session across all projects.

Project memories (long, short, cold) are stored in .engram/mem.db at the project
root and injected only for that project.

Common operations:
  engram mem -g -t invariant list          list all global invariants
  engram mem -g -t invariant read <key>    read a specific invariant
  engram mem -g write <key> <content>      write to global short (default tier)
  engram mem -t long write <key> <content> write to project long-term memory
  engram mem search <query>                full-text search across all tiers
  engram inject                            print session-start context as JSON

Run 'engram mem <subcommand> --help' for details on each operation.`,
}

// shared flags
var memGlobal bool
var memTier string

func openMemDB(ctx context.Context) (*engram.DBHandle, error) {
	if memGlobal {
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
}
