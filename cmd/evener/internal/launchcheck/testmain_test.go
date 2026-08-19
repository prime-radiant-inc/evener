package launchcheck

import (
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

// retiredEvenerEnvVars are names the product no longer declares but that a
// developer machine may still export from when it did. envvars cannot list them,
// so they are carried here. Mirrors cmd/evener-hub/testmain_test.go.
var retiredEvenerEnvVars = []string{"EVENER_API_TOKEN"}

// productEvenerEnvVars is every EVENER_* variable Evener itself reads, plus the
// retired names above. TestMain clears the lot. Deriving the set from envvars
// rather than writing it out is the point: a variable added to the product is
// isolated from these tests the day it exists, which a hand-kept list does not
// manage (EVENER_PROVIDERS_CONFIG was missing from one). Mirrors the hub.
func productEvenerEnvVars() []envvars.Var {
	out := []envvars.Var{}
	for _, v := range envvars.All() {
		if strings.HasPrefix(v.Name, "EVENER_") {
			out = append(out, v)
		}
	}
	for _, name := range retiredEvenerEnvVars {
		out = append(out, envvars.Var{Name: name})
	}
	return out
}

// TestMain isolates the launchcheck test package from the developer's real
// environment. EVENER_STATE_DIR is pointed at a throwaway directory so anything
// resolving the evener state root (cmdutil.DefaultStateRoot — the
// providers.toml + credentials.toml location) sees an empty fixture rather than
// the user's real ~/.evener. Tests that need specific provider config set it
// (and OPENAI_BASE_URL / provider key envs) explicitly.
func TestMain(m *testing.M) {
	stateDir, err := os.MkdirTemp("", "evener-launchcheck-test-state-*")
	if err != nil {
		panic(err)
	}
	// Clear every EVENER_* product variable (including EVENER_STATE_DIR) BEFORE
	// pinning EVENER_STATE_DIR to the throwaway below. Deriving from
	// envvars.All() keeps this list current with the product. Mirrors the hub.
	for _, v := range productEvenerEnvVars() {
		_ = os.Unsetenv(v.Name)
	}
	os.Setenv("EVENER_STATE_DIR", stateDir)
	code := m.Run()
	os.RemoveAll(stateDir)
	os.Exit(code)
}
