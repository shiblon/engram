package engram

import (
	"fmt"
	"net/url"
	"strings"
)

// MemoryAddress is a copyable reference to one memory entry. A rootless path
// addresses the current project; a leading slash addresses global memory.
// Agent layers are explicit and exist only in global standing memory.
type MemoryAddress struct {
	Global bool
	Tier   Tier
	Agent  string
	Key    string
}

// ParseMemoryAddress parses an engram: URI. The bool reports whether raw used
// the engram scheme, allowing callers to preserve ordinary bare-key behavior.
// net/url owns URI syntax and unescaping; this function supplies only Engram's
// deliberately narrow scheme grammar.
func ParseMemoryAddress(raw string) (MemoryAddress, bool, error) {
	scheme, rest, ok := strings.Cut(raw, ":")
	if !ok || !strings.EqualFold(scheme, "engram") {
		return MemoryAddress{}, false, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: %w", raw, err)
	}
	if strings.HasPrefix(rest, "//") || u.Host != "" || u.User != nil {
		return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: authorities (//) are not supported", raw)
	}
	// Check the literal delimiters as well as the parsed fields so empty ? and #
	// suffixes are rejected rather than silently accepted.
	if strings.Contains(rest, "?") || u.ForceQuery || u.RawQuery != "" {
		return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: query parameters are not supported", raw)
	}
	if strings.Contains(rest, "#") || u.Fragment != "" {
		return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: fragments are not supported", raw)
	}

	global := strings.HasPrefix(rest, "/")
	var escapedPath string
	if global {
		escapedPath = strings.TrimPrefix(u.EscapedPath(), "/")
	} else {
		// net/url represents scheme:path-without-a-leading-slash as opaque.
		escapedPath = u.Opaque
	}
	parts := strings.Split(escapedPath, "/")
	if len(parts) != 2 && len(parts) != 3 {
		return MemoryAddress{}, true, fmt.Errorf(
			"invalid Engram address %q: use engram:tier/key or engram:/tier/key", raw,
		)
	}
	for i := range parts {
		parts[i], err = url.PathUnescape(parts[i])
		if err != nil {
			return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: %w", raw, err)
		}
		if parts[i] == "" {
			return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: path segments cannot be empty", raw)
		}
	}

	tier, err := ParseTier(parts[0])
	if err != nil {
		return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: %w", raw, err)
	}
	address := MemoryAddress{Global: global, Tier: tier}
	if len(parts) == 2 {
		address.Key = parts[1]
		return address, true, nil
	}
	if !strings.HasPrefix(parts[1], "@") || len(parts[1]) == 1 {
		return MemoryAddress{}, true, fmt.Errorf(
			"invalid Engram address %q: a three-segment address must use tier/@agent/key", raw,
		)
	}
	if !global {
		return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: agent layers are global; add / after engram:", raw)
	}
	if !IsStandingTier(tier) {
		return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: agent layers only support invariant or preference memory", raw)
	}
	address.Agent, err = NormalizeAgent(strings.TrimPrefix(parts[1], "@"))
	if err != nil {
		return MemoryAddress{}, true, fmt.Errorf("invalid Engram address %q: %w", raw, err)
	}
	address.Key = parts[2]
	return address, true, nil
}

// String returns the canonical, shell-safe form of a memory address.
func (a MemoryAddress) String() string {
	segments := []string{url.PathEscape(string(a.Tier))}
	if a.Agent != "" {
		segments = append(segments, "@"+url.PathEscape(a.Agent))
	}
	segments = append(segments, escapeMemoryAddressKey(a.Key))
	prefix := "engram:"
	if a.Global {
		prefix += "/"
	}
	return prefix + strings.Join(segments, "/")
}

// MemoryAddressFor converts a stored memory into its user-facing address. The
// agent/NAME storage prefix is decoded only for global memory, where layers
// exist; in a project it remains an ordinary (escaped) key.
func MemoryAddressFor(global bool, m Memory) MemoryAddress {
	address := MemoryAddress{Global: global, Tier: m.Tier, Key: m.Key}
	if global && IsStandingTier(m.Tier) {
		if agent, base, ok := ParseAgentLayerKey(m.Key); ok {
			address.Agent = agent
			address.Key = base
		}
	}
	return address
}

func escapeMemoryAddressKey(key string) string {
	// URI resolution treats literal dot segments specially. Preserve these two
	// valid keys by emitting escaped dots in the canonical address.
	switch key {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	default:
		return url.PathEscape(key)
	}
}
