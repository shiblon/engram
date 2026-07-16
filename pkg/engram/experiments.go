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

var experimentRegistry []Experiment

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
