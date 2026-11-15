package repoaudit

import (
	"path/filepath"
	"testing"
)

// TestScenarioExemptionMatchesEitherSeparator pins scenarioExemption's
// normalization: a slash path, a backslash path, and the current host's
// spelling must all resolve the same exemption.
func TestScenarioExemptionMatchesEitherSeparator(t *testing.T) {
	t.Parallel()
	const slash = "test/scenarios/compact-note-survives-resume.md"
	const backslash = `test\scenarios\compact-note-survives-resume.md`
	for _, path := range []string{slash, backslash, filepath.FromSlash(slash)} {
		if _, ok := scenarioExemption(path); !ok {
			t.Errorf("scenarioExemption(%q) did not resolve; a Windows glob's backslash "+
				"path must match the slash-keyed exemption map", path)
		}
	}
}
