package evener_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A lane's Go cache save must not be gated behind the timing budget check.
// The budget fails on wall time while the build cache the run just warmed is
// good, and go-cache-save only acts on main pushes, so a main run that fails
// the budget would otherwise never refresh the lane's entry: the next run
// restores nothing, builds cold, goes over budget again, and skips the save
// again.
func TestGoCacheSavePrecedesTheTimingBudgetGate(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"web-unit", "tests-parts"} {
		saveIndex, budgetIndex := -1, -1
		for i, step := range workflow.Jobs[job].Steps {
			switch {
			case step.Uses == "./.github/actions/go-cache-save":
				saveIndex = i
			case strings.HasPrefix(step.Name, "Timing budget"):
				budgetIndex = i
			}
		}
		if budgetIndex == -1 {
			t.Fatalf("%s: no timing budget step", job)
		}
		if saveIndex == -1 {
			t.Fatalf("%s: no go-cache-save step", job)
		}
		if saveIndex > budgetIndex {
			t.Errorf("%s: go-cache-save (step %d) runs after the timing budget (step %d); a failed budget check would skip the save", job, saveIndex, budgetIndex)
		}
	}
}
