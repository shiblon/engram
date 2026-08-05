package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var (
	curationGlobal  bool
	curationLimit   int
	curationAction  string
	curationSession string
	curationKey     string
	curationJSON    bool
)

var curationCmd = &cobra.Command{
	Use:   "curation",
	Short: "List the append-only log of human curation actions on memory",
	Long: `Show the append-only curation log: one immutable row per mutating curation
action (create, update, delete, move, tldr-set, skill-adopt, skill-classify).

The memories table is last-write-wins, so an overwrite or delete erases all prior
state. This log preserves that history -- including the content and tldr snapshot
taken at event time -- as raw signal for a future learning layer. It is capture
only; nothing weighs or learns from it yet.

Without -g the project log is read; with -g the global log. Filter with --action,
--session, or --key, and cap rows with --limit.

The exact query behind this command:

  SELECT ts, session_id, action, tier, key, db_scope, source,
         content, tldr, trigger, from_tier, to_tier, from_db, to_db
  FROM curation_events
  [WHERE action = ? AND session_id = ? AND key = ?]
  ORDER BY ts DESC, id DESC
  [LIMIT ?];`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := context.Background()
		h, err := openScopeDBReadOnly(ctx, curationGlobal)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		events, err := engram.ListCurationEvents(ctx, h.DB, engram.CurationFilter{
			Action:    engram.CurationAction(curationAction),
			SessionID: curationSession,
			Key:       curationKey,
			Limit:     curationLimit,
		})
		if err != nil {
			return err
		}

		if curationJSON {
			out, err := json.Marshal(events)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		}

		if len(events) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no curation events")
			return nil
		}
		for _, ev := range events {
			ts := time.UnixMilli(ev.TS).UTC().Format(time.RFC3339)
			loc := string(ev.Tier)
			if ev.Action == engram.CurationMove {
				loc = fmt.Sprintf("%s->%s", ev.FromTier, ev.ToTier)
				if ev.FromDB != ev.ToDB {
					loc += fmt.Sprintf(" (%s->%s)", ev.FromDB, ev.ToDB)
				}
			}
			session := ev.SessionID
			if session == "" {
				session = "-"
			}
			source := string(ev.Source)
			if source == "" {
				source = "-"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\tsession=%s\tsource=%s\n",
				ts, ev.Action, ev.DBScope, loc, ev.Key, session, source)
		}
		return nil
	},
}

func init() {
	curationCmd.Flags().BoolVarP(&curationGlobal, "global", "g", false, "read the global (~/.engram) curation log")
	curationCmd.Flags().IntVar(&curationLimit, "limit", 50, "maximum number of events to show (0 for all)")
	curationCmd.Flags().StringVar(&curationAction, "action", "", "filter by action (create, update, delete, move, tldr-set, skill-adopt, skill-classify)")
	curationCmd.Flags().StringVar(&curationSession, "session", "", "filter by session id")
	curationCmd.Flags().StringVar(&curationKey, "key", "", "filter by memory key")
	curationCmd.Flags().BoolVar(&curationJSON, "json", false, "output as a JSON array")
	markExperimental(curationCmd, "curation-log")
	rootCmd.AddCommand(curationCmd)
}
