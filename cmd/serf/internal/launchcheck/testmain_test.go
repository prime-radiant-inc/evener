package launchcheck

import (
	"os"
	"testing"
)

// TestMain isolates the launchcheck test package from the developer's real
// environment. SERF_STATE_DIR is pointed at a throwaway directory so anything
// resolving the serf state root (providercfg.DefaultStateRoot — the
// providers.toml + credentials.toml location) sees an empty fixture rather than
// the user's real ~/.serf. SERF_PROVIDERS_CONFIG is cleared so a stray value in
// the dev shell cannot leak in; tests that need specific provider config set it
// (and OPENAI_BASE_URL / provider key envs) explicitly.
func TestMain(m *testing.M) {
	stateDir, err := os.MkdirTemp("", "serf-launchcheck-test-state-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("SERF_STATE_DIR", stateDir)
	os.Unsetenv("SERF_PROVIDERS_CONFIG")
	code := m.Run()
	os.RemoveAll(stateDir)
	os.Exit(code)
}
