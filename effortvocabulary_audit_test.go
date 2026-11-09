package evener_test

import (
	"os"
	"strings"
	"testing"
)

// TestEffortVocabularyHasOneSource keeps the reasoning-effort lattice from
// being restated per language (#5553). Go's one source is llm/effort; the
// TypeScript table is generated from it by internal/effortts. A local rank
// table, a local fallback ladder, or a registry-side copy is the drift this
// check exists to catch.
func TestEffortVocabularyHasOneSource(t *testing.T) {
	t.Parallel()
	checks := []struct {
		path   string
		must   string
		forbid string
		why    string
	}{
		{
			path:   "llm/registry/schema.go",
			must:   `"primeradiant.com/evener/llm/effort"`,
			forbid: "effortRank = map[string]int{",
			why:    "the registry validates the ladder the session defines; import llm/effort instead of restating its rank",
		},
		{
			path:   "appwire-client/typescript/reasoningEffort.ts",
			must:   `from "./reasoningEffort.gen"`,
			forbid: "const EFFORT_RANK: Record<string, number> = {",
			why:    "the client ranks the daemon's ladder; derive it from the generated table instead of restating the tiers",
		},
		{
			path:   "cmd/evener-hub/frontend/src/panes/spawn/spawnLaunch.ts",
			must:   "FALLBACK_EFFORT_LEVELS",
			forbid: "export const FALLBACK_EFFORT_LEVELS = [",
			why:    "the picker fallback is one shared-client export; importing it keeps the spawn and session pickers from drifting",
		},
	}
	for _, tc := range checks {
		body, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		source := string(body)
		if !strings.Contains(source, tc.must) {
			t.Errorf("%s does not contain %q; %s", tc.path, tc.must, tc.why)
		}
		if strings.Contains(source, tc.forbid) {
			t.Errorf("%s still carries %q; %s", tc.path, tc.forbid, tc.why)
		}
	}
}
