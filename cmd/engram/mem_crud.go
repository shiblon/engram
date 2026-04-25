package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

var memWriteCmd = &cobra.Command{
	Use:   "write <key> <content>",
	Short: "Write (upsert) a memory entry",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		content := strings.Join(args[1:], " ")
		m := engram.Memory{
			TS:      time.Now().UnixMilli(),
			Tier:    engram.Tier(memTier),
			Key:     args[0],
			Content: content,
		}
		if err := engram.WriteMemory(ctx, h.DB, m); err != nil {
			return err
		}
		scope := "project"
		if memGlobal {
			scope = "global"
		}
		fmt.Printf("stored in %s %s memory: %s\n", scope, memTier, args[0])
		return nil
	},
}

var memReadCmd = &cobra.Command{
	Use:   "read <key>",
	Short: "Read a memory entry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		m, err := engram.ReadMemory(ctx, h.DB, engram.Tier(memTier), args[0])
		if err != nil {
			return err
		}
		if m == nil {
			fmt.Printf("no %s memory found with key %q\n", memTier, args[0])
			return nil
		}
		fmt.Printf("[%s] %s\n%s\n", m.Tier, m.Key, m.Content)
		return nil
	},
}

var memListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all memories in a tier",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		memories, err := engram.ListMemories(ctx, h.DB, engram.Tier(memTier))
		if err != nil {
			return err
		}
		if len(memories) == 0 {
			fmt.Printf("no %s memories\n", memTier)
			return nil
		}
		for i, m := range memories {
			fmt.Printf("%d. [%s] %s\n", i+1, m.Key, m.Content)
		}
		return nil
	},
}

var memDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Delete a memory entry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		return engram.DeleteMemory(ctx, h.DB, engram.Tier(memTier), args[0])
	},
}

var (
	promoteFrom string
	promoteTo   string
)

var memPromoteCmd = &cobra.Command{
	Use:   "promote <key>",
	Short: "Move a memory from one tier to another",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		h, err := openMemDB(ctx)
		if err != nil {
			return err
		}
		defer h.DB.Close()

		if err := engram.PromoteMemory(ctx, h.DB, args[0],
			engram.Tier(promoteFrom), engram.Tier(promoteTo)); err != nil {
			return err
		}
		fmt.Printf("promoted %q from %s to %s\n", args[0], promoteFrom, promoteTo)
		return nil
	},
}

var memPopCmd = &cobra.Command{
	Use:   "pop",
	Short: "Read and remove the most recent short-term memory",
	RunE: func(cmd *cobra.Command, args []string) error {
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
	memPromoteCmd.Flags().StringVar(&promoteFrom, "from", string(engram.TierShort), "source tier")
	memPromoteCmd.Flags().StringVar(&promoteTo, "to", string(engram.TierLong), "destination tier")

	memCmd.AddCommand(memWriteCmd, memReadCmd, memListCmd, memDeleteCmd, memPromoteCmd, memPopCmd)
}
