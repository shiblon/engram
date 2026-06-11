package engram

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const agentLayerPrefix = "agent/"

var StandingTiers = []Tier{TierInvariant, TierPreference}

// NormalizeAgent canonicalizes the agent slug used for global standing-memory
// layers. Empty means "no agent layer".
func NormalizeAgent(agent string) (string, error) {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return "", nil
	}
	for i := 0; i < len(agent); i++ {
		c := agent[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '-' || c == '_') {
			continue
		}
		return "", fmt.Errorf("invalid agent %q: use lowercase letters, digits, hyphen, or underscore", agent)
	}
	return agent, nil
}

// AgentLayerKey returns the stored key for an agent-specific layer entry.
func AgentLayerKey(agent, key string) (string, error) {
	agent, err := NormalizeAgent(agent)
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if agent == "" {
		return key, nil
	}
	if key == "" {
		return "", fmt.Errorf("empty memory key")
	}
	if IsAgentLayerKey(key) {
		return "", fmt.Errorf("key %q is already an agent-layer key; pass the base key with --agent", key)
	}
	return agentLayerPrefix + agent + "/" + key, nil
}

// ParseAgentLayerKey reports whether key names an agent-specific layer entry.
func ParseAgentLayerKey(key string) (agent, base string, ok bool) {
	rest, ok := strings.CutPrefix(key, agentLayerPrefix)
	if !ok {
		return "", "", false
	}
	agent, base, ok = strings.Cut(rest, "/")
	if !ok || agent == "" || base == "" {
		return "", "", false
	}
	return agent, base, true
}

func IsAgentLayerKey(key string) bool {
	_, _, ok := ParseAgentLayerKey(key)
	return ok
}

func IsStandingTier(t Tier) bool {
	return t == TierInvariant || t == TierPreference
}

func PrimaryMemories(ms []Memory) []Memory {
	out := make([]Memory, 0, len(ms))
	for _, m := range ms {
		if !IsAgentLayerKey(m.Key) {
			out = append(out, m)
		}
	}
	return out
}

// AgentLayerMemories returns entries from the named layer with their keys
// de-scoped to the primary key. This is for rendering, not storage.
func AgentLayerMemories(ms []Memory, agent string) []Memory {
	agent, _ = NormalizeAgent(agent)
	if agent == "" {
		return nil
	}
	var out []Memory
	for _, m := range ms {
		a, base, ok := ParseAgentLayerKey(m.Key)
		if ok && a == agent {
			m.Key = base
			out = append(out, m)
		}
	}
	return out
}

// MemoryLabel formats a memory using the user-facing layer notation. The stored
// key remains unchanged; only display turns agent/codex/personality into
// invariant/personality @codex.
func MemoryLabel(m Memory) string {
	if agent, base, ok := ParseAgentLayerKey(m.Key); ok {
		return fmt.Sprintf("[%s/%s @%s]", m.Tier, base, agent)
	}
	return fmt.Sprintf("[%s/%s]", m.Tier, m.Key)
}

func memoryMatchesVisibleKey(m Memory, key string) bool {
	if key == "" || m.Key == key {
		return true
	}
	_, base, ok := ParseAgentLayerKey(m.Key)
	return ok && base == key
}

// ListMemoriesForView returns memories as a command-line user expects to see
// them. Without an agent it includes primary entries plus every stored layer,
// annotated by MemoryLabel. With an agent it includes primary entries plus only
// that agent's layer, representing the effective standing view for that agent.
func ListMemoriesForView(ctx context.Context, db *sql.DB, tiers []Tier, agent, key string) ([]Memory, error) {
	agent, err := NormalizeAgent(agent)
	if err != nil {
		return nil, err
	}
	var out []Memory
	for _, tier := range tiers {
		ms, err := ListMemories(ctx, db, tier)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			if agent != "" {
				if a, _, ok := ParseAgentLayerKey(m.Key); ok && a != agent {
					continue
				}
			}
			if memoryMatchesVisibleKey(m, key) {
				out = append(out, m)
			}
		}
	}
	return out, nil
}
