package engram

import "testing"

func TestExperimentRegistryIsValid(t *testing.T) {
	if err := ValidateExperimentRegistry(); err != nil {
		t.Fatal(err)
	}
}
