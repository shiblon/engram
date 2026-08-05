package engram

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed dispatch_seeds/*.json
var dispatchSeeds embed.FS

// SeedProviderSpecs returns the specs shipped with engram, keyed by provider, so
// a fresh install can dispatch without a learning round trip. They are seeds, not
// truth: a learned spec overrides one, and the probe catches a stale one. The gain
// over compiled-in adapters is not that defaults disappear, it is that a stale
// default becomes recoverable without a release.
func SeedProviderSpecs() (map[string]*ProviderSpec, error) {
	entries, err := dispatchSeeds.ReadDir("dispatch_seeds")
	if err != nil {
		return nil, err
	}
	out := make(map[string]*ProviderSpec, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := dispatchSeeds.ReadFile(path.Join("dispatch_seeds", entry.Name()))
		if err != nil {
			return nil, err
		}
		spec, err := ParseProviderSpec(data)
		if err != nil {
			return nil, fmt.Errorf("seed spec %s: %w", entry.Name(), err)
		}
		out[spec.Provider] = spec
	}
	return out, nil
}

// SeedProviderSpec returns one shipped seed spec.
func SeedProviderSpec(provider string) (*ProviderSpec, bool) {
	seeds, err := SeedProviderSpecs()
	if err != nil {
		return nil, false
	}
	spec, ok := seeds[provider]
	return spec, ok
}

// SpecOrigin says where a resolved spec came from, so a caller can tell a probed
// local truth from a shipped guess.
type SpecOrigin string

const (
	SpecOriginMemory SpecOrigin = "memory"
	SpecOriginSeed   SpecOrigin = "seed"
)

// ResolvedSpec pairs a spec with its origin.
type ResolvedSpec struct {
	Spec   *ProviderSpec
	Origin SpecOrigin
}

// ReadProviderSpec loads and validates one provider's spec memory.
func ReadProviderSpec(ctx context.Context, db *sql.DB, provider string) (*ProviderSpec, error) {
	memory, err := ReadMemory(ctx, db, TierLong, DispatchSpecKey(provider))
	if err != nil {
		return nil, err
	}
	if memory == nil {
		return nil, nil
	}
	body, err := ExtractSpecJSON(memory.Content)
	if err != nil {
		return nil, fmt.Errorf("provider spec %s (memory %s): %w", provider, memory.Key, err)
	}
	spec, err := ParseProviderSpec(body)
	if err != nil {
		return nil, fmt.Errorf("provider spec %s (memory %s): %w; repair the JSON block with `engram mem edit %s`",
			provider, memory.Key, err, memory.Key)
	}
	if spec.Provider != provider {
		return nil, fmt.Errorf("provider spec in memory %s names provider %q", memory.Key, spec.Provider)
	}
	return spec, nil
}

// ResolveProviderSpec prefers a learned spec memory and falls back to the shipped
// seed, reporting which it used.
func ResolveProviderSpec(ctx context.Context, db *sql.DB, provider string) (ResolvedSpec, error) {
	if db != nil {
		spec, err := ReadProviderSpec(ctx, db, provider)
		if err != nil {
			return ResolvedSpec{}, err
		}
		if spec != nil {
			return ResolvedSpec{Spec: spec, Origin: SpecOriginMemory}, nil
		}
	}
	if spec, ok := SeedProviderSpec(provider); ok {
		return ResolvedSpec{Spec: spec, Origin: SpecOriginSeed}, nil
	}
	return ResolvedSpec{}, fmt.Errorf("no invocation spec for provider %q: none learned and no seed ships with engram; "+
		"survey the CLI with `engram dispatch survey %s`, then write one with `engram dispatch spec put %s`",
		provider, provider, provider)
}

// WriteProviderSpec stores a validated spec as a long-term memory.
func WriteProviderSpec(ctx context.Context, db *sql.DB, spec *ProviderSpec, opts ...CurationOption) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	memory, err := FormatSpecMemory(spec)
	if err != nil {
		return err
	}
	return WriteMemory(ctx, db, memory, opts...)
}

// ListProviderSpecs returns every learned spec, in provider order. A memory whose
// JSON no longer parses is reported as an error alongside the specs that do, so one
// broken block does not hide the rest.
func ListProviderSpecs(ctx context.Context, db *sql.DB) ([]*ProviderSpec, []error, error) {
	memories, err := ListMemories(ctx, db, TierLong)
	if err != nil {
		return nil, nil, err
	}
	var specs []*ProviderSpec
	var problems []error
	for _, memory := range memories {
		provider, ok := ProviderFromSpecKey(memory.Key)
		if !ok {
			continue
		}
		body, err := ExtractSpecJSON(memory.Content)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", memory.Key, err))
			continue
		}
		spec, err := ParseProviderSpec(body)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", memory.Key, err))
			continue
		}
		if spec.Provider != provider {
			problems = append(problems, fmt.Errorf("%s: spec names provider %q", memory.Key, spec.Provider))
			continue
		}
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Provider < specs[j].Provider })
	return specs, problems, nil
}
