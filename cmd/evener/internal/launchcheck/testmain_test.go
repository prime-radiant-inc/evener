package launchcheck

import (
	"os"
	"testing"
)

// TestMain isolates the launchcheck test package from the developer's real
// environment. XDG_CONFIG_HOME is pointed at a throwaway directory so anything
// resolving the evener config root (cmdutil.DefaultConfigRoot — the
// providers.toml + credentials.toml location) sees an empty fixture rather than
// the user's real ~/.config/evener. EVENER_PROVIDERS_CONFIG is cleared so a
// stray value in the dev shell cannot leak in; tests that need specific
// provider config set it (and OPENAI_BASE_URL / provider key envs) explicitly.
func TestMain(m *testing.M) {
	envRoot, err := os.MkdirTemp("", "evener-launchcheck-test-env-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", envRoot)
	os.Setenv("XDG_CONFIG_HOME", envRoot+"/config")
	os.Setenv("XDG_STATE_HOME", envRoot+"/state")
	os.Setenv("XDG_CACHE_HOME", envRoot+"/cache")
	os.Unsetenv("EVENER_PROVIDERS_CONFIG")
	os.Unsetenv("EVENER_STATE_DIR") // project/session state override; unrelated to config-root resolution but cleared defensively
	code := m.Run()
	os.RemoveAll(envRoot)
	os.Exit(code)
}
