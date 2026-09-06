package hub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/rendezvous"
)

// testEnvRoot is the throwaway root TestMain builds and removes. Anything a
// test needs for the whole run rather than for one case — the live-stack
// binaries, for one — belongs under it, so the removal below is the only
// cleanup path that has to exist.
var testEnvRoot string

// testEnvRootVar hands an inherited throwaway root down to re-executed test
// helpers. A child that inherits one uses it and leaves it for the parent to
// remove.
const testEnvRootVar = "EVENER_HUB_TEST_ENV_ROOT"

const detachHelperRunDirEnv = "EVENER_HUB_DETACH_HELPER_RUN_DIR"

// retiredEvenerEnvVars are names the product no longer declares but that a
// developer machine may still export from when it did. envvars cannot list them
// (nothing reads them any more), so they are carried here.
var retiredEvenerEnvVars = []string{"EVENER_API_TOKEN"}

// productEvenerEnvVars is every EVENER_* variable Evener itself reads. TestMain
// clears the lot; TestHostEvenerEnvNeverReachesTheTestEnvironment asserts it did.
// Deriving the set from envvars rather than writing it out is the point: a
// variable added to the product is isolated from these tests the day it exists,
// which a hand-kept list does not manage (EVENER_PROVIDERS_CONFIG was missing from
// one for as long as it took a developer to export it).
//
// The harness's own EVENER_-prefixed variables — testEnvRootVar,
// evenerEnvScrubHelperVar, EVENER_LIVE_TESTS, EVENER_TEST_PROVIDER, and
// EVENER_TEST_MODEL — name the test rig, not the product, so they are absent
// from envvars and survive.
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

func TestMain(m *testing.M) {
	if os.Getenv(detachHelperRunDirEnv) != "" {
		runDetachFakeDaemon()
		os.Exit(0)
	}
	root, inherited := os.LookupEnv(testEnvRootVar)
	if inherited {
		if _, err := os.Stat(root); err != nil {
			inherited = false
		}
	}
	if !inherited {
		created, err := os.MkdirTemp("", "evener-hub-test-env-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "evener-hub test env: %v\n", err)
			os.Exit(1)
		}
		root = created
	}
	testEnvRoot = root
	for _, dir := range []string{"home", "config", "state", "cache", "codex"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "evener-hub test env: %v\n", err)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}

	// Pin the Go build/module caches to their real locations before redirecting
	// HOME below, exactly as cmd/evener's TestMain does. All three default to
	// paths under $HOME, and the live-stack `go build` inherits this env, so
	// without the pin every run compiles from a cold cache into the throwaway
	// root — and leaves it there, because the module cache is written read-only
	// and the RemoveAll below discards its error.
	// TestGoSubprocessesCacheOutsideTheTestRoot is the guard.
	for _, key := range []string{"GOCACHE", "GOPATH", "GOMODCACHE"} {
		out, err := exec.Command("go", "env", key).Output()
		if err != nil {
			continue
		}
		if value := strings.TrimSpace(string(out)); value != "" {
			_ = os.Setenv(key, value)
		}
	}

	_ = os.Setenv("HOME", filepath.Join(root, "home"))
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	_ = os.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	_ = os.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	// Clear Evener's own configuration environment. HOME and the XDG roots above
	// redirect where these tests look; this decides what configures them, and
	// the two have to agree or the fixtures written into the throwaway root are
	// not what the code under test reads. EVENER_PROVIDERS_CONFIG is the sharp
	// edge — it names the providers.toml every evener process loads, so a value in
	// the developer's shell reached the live-stack hub and its `evener
	// launch-check` and enumerated that developer's real providers instead of
	// the scripted "fake" instance the harness had just written.
	// TestHostEvenerEnvNeverReachesTheTestEnvironment is the guard.
	for _, v := range productEvenerEnvVars() {
		_ = os.Unsetenv(v.Name)
	}

	code := m.Run()
	// Say so when the root cannot be removed. Discarding this error is how a
	// per-run module-cache leak grew to 15GB across hundreds of runs without
	// anything reporting it: the cache is written read-only, RemoveAll failed on
	// every run, and nobody heard.
	if !inherited {
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintf(os.Stderr, "evener-hub test env: leaked %s: %v\n", root, err)
		}
	}
	os.Exit(code)
}

// TestGoSubprocessesCacheOutsideTheTestRoot guards the live-stack builds
// against writing the Go module and build caches into TestMain's throwaway
// root. GOCACHE, GOPATH and GOMODCACHE all default to locations under $HOME,
// which TestMain redirects, and every `go build` these tests shell out to
// inherits that environment. Unpinned, each `go test ./cmd/evener-hub/` re-downloads
// the whole module graph (~118MB) plus a cold build cache into a directory
// TestMain's os.RemoveAll then cannot delete, because the module cache is
// written read-only and the removal error is discarded. Runs accumulate until
// the disk fills.
//
// This asserts against the real toolchain rather than the pinning loop's own
// bookkeeping, so it stays true whatever the mechanism: what matters is where a
// `go` subprocess spawned from this package actually resolves its caches.
func TestGoSubprocessesCacheOutsideTheTestRoot(t *testing.T) {
	for _, key := range []string{"GOCACHE", "GOPATH", "GOMODCACHE"} {
		out, err := exec.Command("go", "env", key).Output()
		if err != nil {
			t.Fatalf("go env %s: %v", key, err)
		}
		got := strings.TrimSpace(string(out))
		if got == "" {
			t.Fatalf("go env %s resolved empty", key)
		}
		if strings.HasPrefix(got, testEnvRoot) {
			t.Fatalf("go env %s = %q, inside the throwaway test root %q; the cache it writes there outlives the run", key, got, testEnvRoot)
		}
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := fspaths.CanonicalizeDir(dir)
	if err != nil {
		t.Fatalf("canonicalize temp dir %s: %v", dir, err)
	}
	return resolved
}

func writeRendezvous(t *testing.T, dir string, e rendezvous.Entry) {
	t.Helper()
	if _, err := rendezvous.Write(dir, e); err != nil {
		t.Fatalf("write rendezvous: %v", err)
	}
}

// fakeProber is a hubcore.Prober that reports a fixed session_id/status (or
// fails), letting tests stand up a roster with a deterministic probe result.
type fakeProber struct {
	sessionID  string
	status     string
	pendingAsk bool
	shouldFail bool
}

func (p fakeProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	if p.shouldFail {
		return hubcore.ProbeResult{}
	}
	return hubcore.ProbeResult{SessionID: p.sessionID, Status: p.status, PendingAsk: p.pendingAsk, OK: true}
}

// evenerEnvScrubHelperVar gates the helper below, which is only meaningful in a
// re-executed copy of this binary whose parent seeded a developer-shaped
// environment. Like testEnvRootVar it names the harness rather than the
// product, so it is absent from envvars.All() and TestMain's scrub leaves it
// alone -- the same property that keeps EVENER_LIVE_TESTS working.
const evenerEnvScrubHelperVar = "EVENER_HUB_TEST_ENV_SCRUB_HELPER"

// TestHostEvenerEnvNeverReachesTheTestEnvironment pins the isolation rule that
// makes this package's results a property of its fixtures rather than of the
// machine it runs on.
//
// Every EVENER_* variable in envvars is production configuration, and the hub,
// the daemons it spawns and the `evener launch-check` it shells out to all read
// the test process's environment. One of them, EVENER_PROVIDERS_CONFIG, names the
// providers.toml every evener process loads: exported in a developer's shell it
// overrode the fake instance the live-stack harness writes into its throwaway
// HOME, so the launch harness enumerated that developer's real providers and
// rejected every spawn with "model provider is not reported by the Evener launch
// harness: fake". Fifteen e2e cases failed on one machine and passed on
// another with the same commit.
//
// The assertion is on the whole EVENER_* set, not on the one variable that bit
// us, because the failure mode is a scrub list that falls behind the product.
func TestHostEvenerEnvNeverReachesTheTestEnvironment(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	env := append([]string{}, os.Environ()...)
	seeded := 0
	for _, v := range productEvenerEnvVars() {
		env = append(env, v.Assignment("host-value-that-must-not-survive"))
		seeded++
	}
	if seeded == 0 {
		t.Fatal("envvars declares no EVENER_* variables, so this test asserts nothing")
	}
	env = append(env, testEnvRootVar+"="+testEnvRoot, evenerEnvScrubHelperVar+"=1")

	cmd := exec.Command(exe, "-test.run=^TestEvenerEnvScrubHelper$")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("a host EVENER_* environment survived into the test process (%d variables seeded): %v\n%s", seeded, err, out)
	}
}

// TestEvenerEnvScrubHelper is the re-executed half of
// TestHostEvenerEnvNeverReachesTheTestEnvironment. It runs after this package's
// TestMain, so what it sees is what every subprocess the tests start would see.
func TestEvenerEnvScrubHelper(t *testing.T) {
	if os.Getenv(evenerEnvScrubHelperVar) == "" {
		t.Skip("re-executed helper for TestHostEvenerEnvNeverReachesTheTestEnvironment")
	}
	for _, v := range productEvenerEnvVars() {
		if value, ok := os.LookupEnv(v.Name); ok {
			t.Errorf("%s=%q survived TestMain; the hub, its daemons and `evener launch-check` all inherit it", v.Name, value)
		}
	}
}
