package hub

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtestenv"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/rendezvous"
)

// testEnv is the throwaway environment TestMain builds and removes. Anything a
// test needs for the whole run rather than for one case — the live-stack
// binaries, for one — belongs under testEnv.Root, so the Discard below is the
// only cleanup path that has to exist.
var testEnv *hubtestenv.Env

const detachHelperRunDirEnv = "EVENER_HUB_DETACH_HELPER_RUN_DIR"

func TestMain(m *testing.M) {
	if os.Getenv(detachHelperRunDirEnv) != "" {
		runDetachFakeDaemon()
		os.Exit(0)
	}
	testEnv = hubtestenv.Redirect("evener-hub-test-env-")
	// Refuse to start when a default root still resolves outside the throwaway
	// env: every test from here on would otherwise read and write the
	// developer's own ~/.config/evener and ~/.local/state/evener, and a failing
	// guard test cannot stop the tests that run beside it.
	// TestHubDefaultRootsStayInsideTheTestEnvironment re-checks this mid-run.
	if escaped := defaultRootsOutsideTestEnv(); len(escaped) > 0 {
		fmt.Fprintf(os.Stderr, "evener-hub test env: default roots resolve outside %s:\n  %s\n", testEnv.Root, strings.Join(escaped, "\n  "))
		testEnv.Discard()
		os.Exit(1)
	}

	code := m.Run()
	testEnv.Discard()
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
		if strings.HasPrefix(got, testEnv.Root) {
			t.Fatalf("go env %s = %q, inside the throwaway test root %q; the cache it writes there outlives the run", key, got, testEnv.Root)
		}
	}
}

// hubDefaultRoots lists every filesystem root the hub falls back to when a
// WebConfig field is left unset (the launch config root, the hub state root,
// the plugin store root, the MCP config path), every path runMain opens when a
// flag is unset (the config file, the state glob, the past-index DB, the
// rendezvous run dir), plus the HOME and XDG bases they derive from.
func hubDefaultRoots() []hubtestenv.NamedPath {
	return append(hubtestenv.BaseRoots(), []hubtestenv.NamedPath{
		{Name: "hubLaunchConfigRoot with LaunchConfigRoot unset", Path: hubLaunchConfigRoot(hubcore.WebConfig{})},
		{Name: "cmdutil.DefaultStateRoot", Path: cmdutil.DefaultStateRoot()},
		{Name: "plugins.DefaultRoot", Path: plugins.DefaultRoot()},
		{Name: "defaultMCPConfigPath", Path: defaultMCPConfigPath()},
		{Name: "DefaultHubStateRoot", Path: DefaultHubStateRoot()},
		{Name: "DefaultConfigPath", Path: DefaultConfigPath()},
		{Name: "DefaultStateGlob", Path: DefaultStateGlob()},
		{Name: "DefaultPastIndexDBPath", Path: DefaultPastIndexDBPath()},
		{Name: "rendezvous.DefaultDir", Path: rendezvous.DefaultDir()},
	}...)
}

// defaultRootsOutsideTestEnv is testEnv.PathsOutside over the hub's own default
// roots. Empty means the throwaway env contains them all.
func defaultRootsOutsideTestEnv() []string {
	return testEnv.PathsOutside(hubDefaultRoots())
}

// TestHubDefaultRootsStayInsideTheTestEnvironment pins the other half of
// TestMain's isolation: the hub's default roots resolve inside the throwaway
// root, never in the developer's own home, for the whole run and not only at
// startup. TestMain refuses to start when defaultRootsOutsideTestEnv reports
// an escape; this catches one that opens mid-run, such as a test that swaps
// configUserHomeDir or the XDG env without restoring it.
//
// Any hub test that dispatches a handler is why this has to be pinned. A
// handler called with empty params reads and writes against whichever root
// the fixture left unset — TestHubRPCRegistersExpectedHandlerSet's single
// model/list round-trip is one such dispatch — and for a handler where an
// empty request is a valid write (evener/launch/setLayer today; any "set
// this file's content" handler tomorrow) the write happens for real. The
// HOME/XDG redirect in TestMain is what keeps that out of ~/.config/evener.
func TestHubDefaultRootsStayInsideTheTestEnvironment(t *testing.T) {
	if escaped := defaultRootsOutsideTestEnv(); len(escaped) > 0 {
		t.Fatalf("default roots resolve outside the throwaway test root %q; a handler dispatched with empty params would read or write there for real:\n  %s", testEnv.Root, strings.Join(escaped, "\n  "))
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
// environment. Like hubtestenv.RootVar it names the harness rather than the
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
	for _, v := range hubtestenv.ProductEvenerEnvVars() {
		env = append(env, v.Assignment("host-value-that-must-not-survive"))
		seeded++
	}
	if seeded == 0 {
		t.Fatal("envvars declares no EVENER_* variables, so this test asserts nothing")
	}
	env = append(env, hubtestenv.RootVar+"="+testEnv.Root, evenerEnvScrubHelperVar+"=1")

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
	for _, v := range hubtestenv.ProductEvenerEnvVars() {
		if value, ok := os.LookupEnv(v.Name); ok {
			t.Errorf("%s=%q survived TestMain; the hub, its daemons and `evener launch-check` all inherit it", v.Name, value)
		}
	}
}
