package main

import (
	"context"
	"fmt"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

// memoryTarget is the fully resolved scope, layer, tier, and key for one CLI
// argument. Address-derived values live here rather than in Cobra's package
// globals, so parsing one address cannot leak state into another command or test.
type memoryTarget struct {
	Global       bool
	Tier         engram.Tier
	TierExplicit bool
	Agent        string
	Key          string
	Addressed    bool
}

func resolveMemoryTarget(cmd *cobra.Command, raw, tierFlag string) (memoryTarget, error) {
	address, addressed, err := engram.ParseMemoryAddress(raw)
	if err != nil {
		return memoryTarget{}, err
	}

	agent, err := memAgentName()
	if err != nil {
		return memoryTarget{}, err
	}
	target := memoryTarget{
		Global:    memGlobal || agent != "",
		Tier:      engram.Tier(memTier),
		Agent:     agent,
		Key:       raw,
		Addressed: addressed,
	}
	if tierFlag == "from" {
		target.Tier = engram.Tier(moveFrom)
	}
	if flag := cmd.Flag(tierFlag); flag != nil {
		target.TierExplicit = flag.Changed
	}
	if !addressed {
		return target, nil
	}

	if cmd.Flag("global").Changed && memGlobal != address.Global {
		return memoryTarget{}, fmt.Errorf(
			"address %s selects %s memory but --global selects %s memory",
			raw, scopeName(address.Global), scopeName(memGlobal),
		)
	}
	if cmd.Flag("agent").Changed && agent != address.Agent {
		addressLayer := "primary"
		if address.Agent != "" {
			addressLayer = "@" + address.Agent
		}
		flagLayer := "primary"
		if agent != "" {
			flagLayer = "@" + agent
		}
		return memoryTarget{}, fmt.Errorf(
			"address %s selects the %s layer but --agent selects the %s layer",
			raw, addressLayer, flagLayer,
		)
	}
	if target.TierExplicit {
		flagTier, parseErr := engram.ParseTier(string(target.Tier))
		if parseErr != nil {
			return memoryTarget{}, fmt.Errorf("--%s: %w", tierFlag, parseErr)
		}
		if flagTier != address.Tier {
			return memoryTarget{}, fmt.Errorf(
				"address %s selects tier %s but --%s selects tier %s",
				raw, address.Tier, tierFlag, target.Tier,
			)
		}
	}
	// move historically also inherits --tier, even though --from is its clearer
	// source-tier flag. Do not let either spelling silently disagree with an
	// address.
	if tierFlag == "from" && cmd.Flag("tier").Changed {
		flagTier, parseErr := engram.ParseTier(memTier)
		if parseErr != nil {
			return memoryTarget{}, parseErr
		}
		if flagTier != address.Tier {
			return memoryTarget{}, fmt.Errorf(
				"address %s selects tier %s but --tier selects tier %s",
				raw, address.Tier, flagTier,
			)
		}
	}

	return memoryTarget{
		Global:       address.Global,
		Tier:         address.Tier,
		TierExplicit: true,
		Agent:        address.Agent,
		Key:          address.Key,
		Addressed:    true,
	}, nil
}

func (t memoryTarget) openDB(ctx context.Context) (*engram.DBHandle, error) {
	return openScopeDB(ctx, t.Global)
}

func (t memoryTarget) openDBReadOnly(ctx context.Context) (*engram.DBHandle, error) {
	return openScopeDBReadOnly(ctx, t.Global)
}

func (t memoryTarget) storedKey() (string, error) {
	if t.Agent == "" {
		return t.Key, nil
	}
	if !engram.IsStandingTier(t.Tier) {
		return "", fmt.Errorf("agent layers only apply to global invariant/preference memory")
	}
	return engram.AgentLayerKey(t.Agent, t.Key)
}

func (t memoryTarget) viewTiers(cmd *cobra.Command) ([]engram.Tier, error) {
	if t.TierExplicit {
		if t.Agent != "" && !engram.IsStandingTier(t.Tier) {
			return nil, fmt.Errorf("agent layers only apply to global invariant/preference memory")
		}
		return []engram.Tier{t.Tier}, nil
	}
	return memViewTiers(cmd)
}

func (t memoryTarget) exactMemory(ctx context.Context, h *engram.DBHandle) (*engram.Memory, error) {
	key, err := t.storedKey()
	if err != nil {
		return nil, err
	}
	return engram.ReadMemory(ctx, h.DB, t.Tier, key)
}
