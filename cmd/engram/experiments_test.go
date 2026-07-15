package main

import (
	"strings"
	"testing"

	"github.com/shiblon/engram/pkg/engram"
	"github.com/spf13/cobra"
)

func TestExperimentalCommandsMatchRegistry(t *testing.T) {
	if err := engram.ValidateExperimentRegistry(); err != nil {
		t.Fatal(err)
	}

	annotated := make(map[string]string)
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if key := cmd.Annotations[experimentAnnotation]; key != "" {
			path := cmd.CommandPath()
			annotated[path] = key
			if _, ok := engram.ExperimentByKey(key); !ok {
				t.Errorf("command %q names unregistered experiment %q", path, key)
			}
			if marker := "[experimental: " + key + "]"; !strings.Contains(cmd.Short, marker) {
				t.Errorf("command %q missing visible marker %q", path, marker)
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)

	for _, experiment := range engram.Experiments() {
		for _, command := range experiment.Commands {
			if got := annotated[command]; got != experiment.Key {
				t.Errorf("registry command %q annotated as %q, want %q", command, got, experiment.Key)
			}
		}
	}
}
