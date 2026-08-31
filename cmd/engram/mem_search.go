package main

import (
	"context"
	"fmt"
	"io"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

const defaultSearchLimit = 0

var (
	memSearchLimit int
	memSearchFull  bool
)

// limitSearchResults applies the CLI's shared search bound after every scope-
// specific filter has run. A zero limit deliberately means all results, matching
// the curation command's established --limit convention.
func limitSearchResults(results []engram.Memory, limit int) ([]engram.Memory, int, error) {
	if err := validateSearchLimit(limit); err != nil {
		return nil, 0, err
	}
	if limit == 0 || len(results) <= limit {
		return results, 0, nil
	}
	return results[:limit], len(results) - limit, nil
}

func validateSearchLimit(limit int) error {
	if limit < 0 {
		return fmt.Errorf("--limit must be non-negative")
	}
	return nil
}

func reportOmittedSearchResults(w io.Writer, shown, omitted int) {
	if omitted == 0 {
		return
	}
	fmt.Fprintf(w, "engram: showing %d results; %d omitted (use --limit 0 for all)\n", shown, omitted)
}

func printFullMemorySearchResults(w io.Writer, results []engram.Memory, global bool) {
	for i, m := range results {
		fmt.Fprintf(w, "%d. %s %s\n", i+1, engram.MemoryAddressFor(global, m), m.Content)
	}
}

var memSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search memories and show compact ranked results",
	Long: `Search memory keys, summaries, and bodies using ranked full-text search.

By default every ranked match is shown as a copyable address and summary. Use
--limit to bound the result set, or --full to include complete bodies.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateSearchLimit(memSearchLimit); err != nil {
			return err
		}
		ctx := context.Background()
		h, err := openMemDBReadOnly(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		// Only filter by tier if explicitly set; default searches all tiers.
		var tier engram.Tier
		if cmd.Flag("tier").Changed {
			tier = engram.Tier(memTier)
		}
		if memAgent != "" {
			if _, err := memViewTiers(cmd); err != nil {
				return err
			}
		}

		results, err := engram.SearchMemories(ctx, h.DB, args[0], tier)
		if err != nil {
			return err
		}
		if memAgent != "" {
			agent, err := memAgentName()
			if err != nil {
				return err
			}
			filtered := results[:0]
			for _, m := range results {
				if !engram.IsStandingTier(m.Tier) {
					continue
				}
				if a, _, ok := engram.ParseAgentLayerKey(m.Key); ok && a != agent {
					continue
				}
				filtered = append(filtered, m)
			}
			results = filtered
		}
		results, omitted, err := limitSearchResults(results, memSearchLimit)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no results")
			return nil
		}
		if memSearchFull {
			printFullMemorySearchResults(cmd.OutOrStdout(), results, memUsesGlobal())
		} else {
			printMemorySummaries(cmd.OutOrStdout(), results, memUsesGlobal())
		}
		reportOmittedSearchResults(cmd.ErrOrStderr(), len(results), omitted)
		return nil
	},
}

func init() {
	memSearchCmd.Flags().IntVar(&memSearchLimit, "limit", defaultSearchLimit, "maximum number of matches to show (0 for all)")
	memSearchCmd.Flags().BoolVar(&memSearchFull, "full", false, "include complete memory bodies")
	memCmd.AddCommand(memSearchCmd)
}
