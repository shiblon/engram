package main

import (
	"strings"
	"testing"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

func addressTestCommand(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Bool("global", false, "")
	cmd.Flags().String("agent", "", "")
	cmd.Flags().String("tier", "", "")
	return cmd
}

func TestResolveMemoryTargetAddressOverridesOmittedFlags(t *testing.T) {
	oldGlobal, oldTier, oldAgent := memGlobal, memTier, memAgent
	t.Cleanup(func() { memGlobal, memTier, memAgent = oldGlobal, oldTier, oldAgent })
	memGlobal, memTier, memAgent = false, string(engram.TierShort), ""

	target, err := resolveMemoryTarget(addressTestCommand(t), "engram:/preference/@codex/style", "tier")
	if err != nil {
		t.Fatal(err)
	}
	want := memoryTarget{
		Global: true, Tier: engram.TierPreference, TierExplicit: true,
		Agent: "codex", Key: "style", Addressed: true,
	}
	if target != want {
		t.Errorf("resolved target = %+v, want %+v", target, want)
	}
	if got, err := target.storedKey(); err != nil || got != "agent/codex/style" {
		t.Errorf("stored key = %q, %v", got, err)
	}
}

func TestResolveMemoryTargetRejectsConflictingFlags(t *testing.T) {
	oldGlobal, oldTier, oldAgent := memGlobal, memTier, memAgent
	t.Cleanup(func() { memGlobal, memTier, memAgent = oldGlobal, oldTier, oldAgent })

	tests := []struct {
		name  string
		setup func(*cobra.Command)
		raw   string
		want  string
	}{
		{
			name: "scope", raw: "engram:long/key", want: "--global",
			setup: func(cmd *cobra.Command) { memGlobal = true; _ = cmd.Flags().Set("global", "true") },
		},
		{
			name: "tier", raw: "engram:long/key", want: "--tier",
			setup: func(cmd *cobra.Command) { memTier = "short"; _ = cmd.Flags().Set("tier", "short") },
		},
		{
			name: "agent", raw: "engram:/preference/@codex/key", want: "--agent",
			setup: func(cmd *cobra.Command) { memAgent = "claude"; _ = cmd.Flags().Set("agent", "claude") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			memGlobal, memTier, memAgent = false, "short", ""
			cmd := addressTestCommand(t)
			tt.setup(cmd)
			_, err := resolveMemoryTarget(cmd, tt.raw, "tier")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("resolveMemoryTarget error = %v, want %q", err, tt.want)
			}
		})
	}
}
