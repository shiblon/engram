package main

import (
	"context"
	"fmt"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var memSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Full-text search across memories",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
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
		if len(results) == 0 {
			fmt.Println("no results")
			return nil
		}
		for i, m := range results {
			fmt.Printf("%d. %s %s\n", i+1, engram.MemoryLabel(m), m.Content)
		}
		return nil
	},
}

func init() {
	memCmd.AddCommand(memSearchCmd)
}
