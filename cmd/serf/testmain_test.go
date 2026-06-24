package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain isolates the entire cmd/serf test package from the developer's real
// environment. SERF_STATE_DIR is pointed at a throwaway directory so anything
// resolving the serf state root (cmdutil.DefaultStateRoot — the
// providers.toml + credentials.toml location) sees an empty fixture rather than
// the user's real ~/.serf. SERF_PROVIDERS_CONFIG is cleared so a stray value in
// the dev shell cannot leak in; tests that need specific provider config set it
// (and OPENAI_BASE_URL / provider key envs) explicitly.
func TestMain(m *testing.M) {
	stateDir, err := os.MkdirTemp("", "serf-cli-test-state-*")
	if err != nil {
		panic(err)
	}
	envRoot, err := os.MkdirTemp("", "serf-cli-test-env-*")
	if err != nil {
		panic(err)
	}
	// Pin the Go build/module caches to their real locations before redirecting
	// HOME/XDG below. Tests that shell out to `go run`/`go build` inherit this
	// env; redirecting HOME moves GOPATH/GOMODCACHE (and GOCACHE) to the throwaway
	// dirs, so without pinning them the subprocess recompiles the whole binary
	// from cold module + build caches on every run (~8s vs ~0.1s warm).
	for _, key := range []string{"GOCACHE", "GOPATH", "GOMODCACHE"} {
		if out, err := exec.Command("go", "env", key).Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				os.Setenv(key, v)
			}
		}
	}
	os.Setenv("SERF_STATE_DIR", stateDir)
	os.Setenv("HOME", envRoot)
	os.Setenv("XDG_CONFIG_HOME", envRoot+"/config")
	os.Setenv("XDG_STATE_HOME", envRoot+"/state")
	os.Setenv("XDG_CACHE_HOME", envRoot+"/cache")
	os.Unsetenv("SERF_PROVIDERS_CONFIG")
	code := m.Run()
	os.RemoveAll(stateDir)
	os.RemoveAll(envRoot)
	os.Exit(code)
}
