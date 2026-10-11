package sandbox

import (
	"path/filepath"
	"slices"
	"testing"
)

// The probe lists the plugin store's installed plugin files and the user skills
// directory under Evener's config root, wherever XDG_CONFIG_HOME puts it.
func TestProbeFindsEvenerContentRoots(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		root string
	}{
		{"default", nil, "/Users/tester/.config/evener"},
		{"XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "/xdg/evener"},
		// Evener resolves a relative XDG_CONFIG_HOME against its working
		// directory, and the probe records that absolute root.
		{"relative XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": "rel"}, mustAbs(filepath.Join("rel", "evener"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts := probeHost(stubProbeSystem{env: tc.env})
			want := []string{tc.root + "/plugins/cache", tc.root + "/plugins/bundled", tc.root + "/skills"}
			if !slices.Equal(facts.EvenerContentRoots, want) {
				t.Errorf("probe = %v, want %v", facts.EvenerContentRoots, want)
			}
		})
	}
}

// A session run with EVENER_NO_USER_SKILLS=1 loads no user skills, so the probe
// leaves that directory out; installed plugins stay, since their hooks run.
func TestProbeLeavesUserSkillsOutWhenTheyAreNotLoaded(t *testing.T) {
	t.Parallel()
	facts := probeHost(stubProbeSystem{env: map[string]string{"EVENER_NO_USER_SKILLS": "1"}})
	if slices.Contains(facts.EvenerContentRoots, "/Users/tester/.config/evener/skills") {
		t.Errorf("user skills must be left out when EVENER_NO_USER_SKILLS=1: %v", facts.EvenerContentRoots)
	}
	if !slices.Contains(facts.EvenerContentRoots, "/Users/tester/.config/evener/plugins/cache") {
		t.Errorf("installed plugins must stay: %v", facts.EvenerContentRoots)
	}
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		panic(err)
	}
	return abs
}
