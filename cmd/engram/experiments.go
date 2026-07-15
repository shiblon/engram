package main

import (
	"fmt"
	"strings"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

const experimentAnnotation = "engram.experimental"

// markExperimental gives a command a visible maturity label and a structured
// registry link. Tests walk the Cobra tree and reject missing or stale links.
func markExperimental(cmd *cobra.Command, key string) {
	if _, ok := engram.ExperimentByKey(key); !ok {
		panic(fmt.Sprintf("command %q names unregistered experiment %q", cmd.Use, key))
	}
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[experimentAnnotation] = key
	cmd.Short += " [experimental: " + key + "]"
}

var experimentsCmd = &cobra.Command{
	Use:   "experiments",
	Short: "List experimental features and their exit conditions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		for i, experiment := range engram.Experiments() {
			if i > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s [%s]\n", experiment.Key, experiment.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "  Hypothesis: %s\n", experiment.Hypothesis)
			fmt.Fprintf(cmd.OutOrStdout(), "  Unstable: %s\n", experiment.UnstableSurfaces)
			fmt.Fprintf(cmd.OutOrStdout(), "  Promote when: %s\n", experiment.PromoteWhen)
			fmt.Fprintf(cmd.OutOrStdout(), "  Remove when: %s\n", experiment.RemoveWhen)
			fmt.Fprintf(cmd.OutOrStdout(), "  Commands: %s\n", strings.Join(experiment.Commands, ", "))
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(experimentsCmd)
}
