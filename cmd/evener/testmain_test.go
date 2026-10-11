package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/devtool/shardrun"
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

// TestMain isolates the entire cmd/evener test package from the developer's real
// environment. EVENER_STATE_DIR is pointed at a throwaway directory so anything
// resolving a project/session state root without an explicit --state-dir
// (evener run/serve's EVENER_STATE_DIR precedence, cmd/evener/run.go) sees a
// predictable, empty fixture instead of a per-project XDG path. Provider
// config (providers.toml/credentials.toml) is governed separately by
// XDG_CONFIG_HOME/cmdutil.DefaultConfigRoot, also redirected below into
// envRoot, so it never touches the user's real ~/.config/evener either.
// EVENER_PROVIDERS_CONFIG is cleared so a stray value in the dev shell cannot
// leak in; tests that need specific provider config set it (and
// OPENAI_BASE_URL / provider key envs) explicitly.
//
// Both throwaway roots live inside a private host temp that also collects the
// session scratch and temp containers the sessions under test retain at close.
// The self-exec helper children inherit it (sandboxtest.RootVar), so the roots
// their own TestMain creates land inside it too: several are killed by a signal
// or leave through os.Exit on purpose and never reach their own cleanup.
func TestMain(m *testing.M) {
	// When evener dev cli-shards launches this binary as a shard, its
	// -test.run regex arrives through a file (see shardrun). Apply it first:
	// flag.Parse must run before it, and the EVENER_* sweep below would
	// otherwise be free to clear the variable naming the file.
	flag.Parse()
	if err := shardrun.ConfigureRunFile(); err != nil {
		fmt.Fprintf(os.Stderr, "evener TestMain: %v\n", err)
		os.Exit(2)
	}
	hostTemp, err := sandboxtest.RedirectHostTemp("evener-cli-test-")
	if err != nil {
		panic(err)
	}
	stateDir, err := os.MkdirTemp("", "evener-cli-test-state-*")
	if err != nil {
		panic(err)
	}
	envRoot, err := os.MkdirTemp("", "evener-cli-test-env-*")
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
	// Clear every EVENER_* product variable (including EVENER_STATE_DIR) BEFORE
	// pinning EVENER_STATE_DIR to the throwaway below. Deriving from
	// envvars.All() keeps this list current with the product. Mirrors the hub.
	// The host temp bases RedirectHostTemp exported above stay: they keep the
	// evener binaries these tests start from sweeping the developer's /tmp.
	for _, v := range productEvenerEnvVars() {
		if sandboxtest.Redirected(v) {
			continue
		}
		_ = os.Unsetenv(v.Name)
	}
	os.Setenv("EVENER_STATE_DIR", stateDir)
	os.Setenv("HOME", envRoot)
	os.Setenv("XDG_CONFIG_HOME", envRoot+"/config")
	os.Setenv("XDG_STATE_HOME", envRoot+"/state")
	os.Setenv("XDG_CACHE_HOME", envRoot+"/cache")
	code := m.Run()
	os.RemoveAll(stateDir)
	os.RemoveAll(envRoot)
	if err := hostTemp.Discard(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// TestMainAppliesTheShardRunFile pins the TestMain wiring evener dev
// cli-shards depends on to hand each shard its tests.
func TestMainAppliesTheShardRunFile(t *testing.T) {
	shardrun.RequireTestMainAppliesRunFile(t)
}

// TestTestMainLeavesTheHostTempRedirectForChildren guards the order TestMain
// runs in: RedirectHostTemp exports EVENER_HOST_TEMP_BASES before the scrub of
// product variables, and the scrub must keep that value. Cleared, the evener
// binaries these tests start would sweep the developer's /tmp and /var/tmp at
// startup and reclaim other sessions' abandoned scratch.
func TestTestMainLeavesTheHostTempRedirectForChildren(t *testing.T) {
	if !sandboxtest.Redirected(envvars.EVENERHostTempBases) {
		t.Fatalf("%s = %q after TestMain, want the host temp redirect's own base", envvars.EVENERHostTempBases.Name, envvars.EVENERHostTempBases.Getenv())
	}
}
