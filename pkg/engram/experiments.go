package engram

import (
	"fmt"
	"sort"
	"strings"
)

// ExperimentStatus is the maturity of a non-stable feature tracked in the
// experiment registry. Stable features leave the registry and lose their
// experimental command labels; deprecated experiments remain until their
// removal condition is satisfied.
type ExperimentStatus string

const (
	ExperimentExperimental ExperimentStatus = "experimental"
	ExperimentDeprecated   ExperimentStatus = "deprecated"
)

// Experiment records the evidence needed to decide whether a trial becomes a
// supported feature or is removed. Promotion and removal are event conditions,
// not dates: maturity should not depend on predicting the future.
type Experiment struct {
	Key              string
	Status           ExperimentStatus
	Hypothesis       string
	UnstableSurfaces string
	PromoteWhen      string
	RemoveWhen       string
	Commands         []string
}

var experimentRegistry = []Experiment{
	{
		Key:              "curation-log",
		Status:           ExperimentExperimental,
		Hypothesis:       "Losslessly capturing every human curation action (create, update, delete, move, tldr-set, skill-adopt, skill-classify) into an append-only log will provide the reward signal a future learning layer needs, which the last-write-wins memories table discards on every overwrite and delete.",
		UnstableSurfaces: "The curation_events schema, the captured action and source vocabulary, the `engram curation` read surface, and the log's rotation policy may change in patch releases. This is capture only; nothing consumes the log yet.",
		PromoteWhen:      "The log is confirmed lossless for overwrites and deletes across every mutation path, a consumer reads it, and its retention policy is settled against real volume.",
		RemoveWhen:       "Curation actions turn out not to be a useful learning signal, or a different capture mechanism replaces this table.",
		Commands:         []string{"engram curation"},
	},
}

// Experiments returns a copy of the registry in deterministic key order.
func Experiments() []Experiment {
	out := append([]Experiment(nil), experimentRegistry...)
	for i := range out {
		out[i].Commands = append([]string(nil), out[i].Commands...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ExperimentByKey returns the registered experiment with key.
func ExperimentByKey(key string) (Experiment, bool) {
	for _, experiment := range experimentRegistry {
		if experiment.Key == key {
			return experiment, true
		}
	}
	return Experiment{}, false
}

// ValidateExperimentRegistry checks the completeness and uniqueness properties
// release diligence relies on. CLI tests separately verify that Commands and
// Cobra annotations agree, keeping this package independent of Cobra.
func ValidateExperimentRegistry() error {
	seenKeys := make(map[string]bool, len(experimentRegistry))
	seenCommands := make(map[string]string)
	for i, experiment := range experimentRegistry {
		label := fmt.Sprintf("experiment %d", i)
		if experiment.Key != "" {
			label = fmt.Sprintf("experiment %q", experiment.Key)
		}
		if experiment.Key == "" || strings.ContainsAny(experiment.Key, " \t\n") {
			return fmt.Errorf("%s: key must be non-empty and contain no whitespace", label)
		}
		if seenKeys[experiment.Key] {
			return fmt.Errorf("%s: duplicate key", label)
		}
		seenKeys[experiment.Key] = true
		if experiment.Status != ExperimentExperimental && experiment.Status != ExperimentDeprecated {
			return fmt.Errorf("%s: unsupported status %q", label, experiment.Status)
		}
		for name, value := range map[string]string{
			"hypothesis":        experiment.Hypothesis,
			"unstable surfaces": experiment.UnstableSurfaces,
			"promote condition": experiment.PromoteWhen,
			"remove condition":  experiment.RemoveWhen,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s: missing %s", label, name)
			}
		}
		if len(experiment.Commands) == 0 {
			return fmt.Errorf("%s: no command surfaces", label)
		}
		for _, command := range experiment.Commands {
			if previous, exists := seenCommands[command]; exists {
				return fmt.Errorf("%s: command %q already belongs to experiment %q", label, command, previous)
			}
			seenCommands[command] = experiment.Key
		}
	}
	return nil
}
