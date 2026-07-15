package engram

import "testing"

func TestExperimentRegistryIsValid(t *testing.T) {
	if err := ValidateExperimentRegistry(); err != nil {
		t.Fatal(err)
	}
}

func TestExperimentsReturnsDeepCopy(t *testing.T) {
	got := Experiments()
	if len(got) == 0 {
		t.Fatal("experiment registry is empty")
	}
	got[0].Key = "changed"
	got[0].Commands[0] = "changed"

	again := Experiments()
	if again[0].Key == "changed" || again[0].Commands[0] == "changed" {
		t.Fatal("Experiments exposed mutable registry storage")
	}
}
