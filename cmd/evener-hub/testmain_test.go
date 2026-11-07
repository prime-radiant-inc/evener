package hub

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtestenv"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/internal/devtool/shardrun"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/rendezvous"
)

// testEnv is the throwaway environment TestMain builds and removes. Anything a
// test needs for the whole run rather than for one case — the live-stack
// binaries, for one — belongs under testEnv.Root, so the Discard below is the
// only cleanup path that has to exist.
var testEnv *hubtestenv.Env

const detachHelperRunDirEnv = "EVENER_HUB_DETACH_HELPER_RUN_DIR"
const managerCleanupServeHelperEnv = "EVENER_MANAGER_CLEANUP_SERVE_HELPER"
const managerCleanupServeReadyEnv = "EVENER_MANAGER_CLEANUP_SERVE_READY"

// liveStackWarmEnv marks a process whose live-stack binaries are already built:
// either its own warm ran or a shard runner handed it the directory. Every
// descendant that inherits the environment (however a test launches it — the
// shell, a daemon, a fixture) can then skip the build. It is set only when
// binaries exist, never merely because this process's selection needs none: a
// child re-executed with a different -test.run has to judge its own selection.
//
// A name list of helper variables was the obvious first cut and it failed
// twice: TestHostDeployFilesystemSafety re-invokes this binary 16 times, and
// TestBackgroundJobsBrowser launches its paging producer through the real
// shell from a session workspace, which inherits no test variable at all. A
// child that ran the warm build from there resolved the repo root to nothing
// and died before producing a byte, so the browser journey waited out its
// tripwire.
const (
	liveStackWarmEnv = shardrun.PrebuiltBinWarmEnv
	// liveStackBinEnv names the directory a shard runner built once; see
	// shardrun.PrebuiltBinDirEnv.
	liveStackBinEnv = shardrun.PrebuiltBinDirEnv
)
const retainHostCleanupRootMarker = ".evener-host-cleanup-unproven"

func retainHostCleanupRoot(t *testing.T) {
	t.Helper()
	root := os.Getenv(sandboxtest.RootVar)
	if root == "" {
		t.Errorf("cannot retain unsafe host cleanup state: %s is unset", sandboxtest.RootVar)
		return
	}
	if err := os.WriteFile(filepath.Join(root, retainHostCleanupRootMarker), []byte("hub exit unproved\n"), 0o600); err != nil {
		t.Errorf("marking HostTemp root %s for retention after unproved host cleanup: %v", root, err)
	}
}

func hostCleanupRootMustBeRetained(root string) bool {
	_, err := os.Stat(filepath.Join(root, retainHostCleanupRootMarker))
	return err == nil
}

// outsideCheckoutHelperEnv marks the subprocess TestTestMainWarmsNothingOutside-…
// launches. That case proves TestMain does not need a checkout to start a
// helper: it is what the browser guard's paging producer looked like when the
// warm build killed it.
const outsideCheckoutHelperEnv = "EVENER_HUB_OUTSIDE_CHECKOUT_HELPER"

// TestTestMainSurvivesOutsideTheCheckout runs this binary from a temp directory
// with the warm marker unset, as a helper launched from a session workspace is.
// TestMain must not fail there: a build it cannot locate is not a helper's
// problem, and failing killed a producer that only needed to write bytes.
func TestTestMainSurvivesOutsideTheCheckout(t *testing.T) {
	if os.Getenv(outsideCheckoutHelperEnv) == "1" {
		// Reached only if TestMain let this process boot. Reaching here is the
		// assertion.
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestTestMainSurvivesOutsideTheCheckout$")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		outsideCheckoutHelperEnv+"=1",
		// Unset the marker the parent set, so this really exercises the
		// no-warm-build path.
		liveStackWarmEnv+"=",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper outside the checkout failed to run: %v\n%s", err, out)
	}
}

// reexecSelf runs this test binary with run as its -test.run selection and
// extraEnv appended to the inherited environment. The executable, count flag,
// and environment assembly live here so a fourth self re-exec cannot forget
// one of them.
func reexecSelf(t *testing.T, run string, extraEnv ...string) ([]byte, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run="+run, "-test.count=1")
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd.CombinedOutput()
}

// TestTestMainSkipsTheWarmWithoutALiveStackSelection proves the -run
// classification run from TestMain: this binary is re-executed on a unit-only
// selection with no `go` on PATH. A warm attempt would fail to find the
// toolchain, so the child completing green means TestMain skipped the build.
func TestTestMainSkipsTheWarmWithoutALiveStackSelection(t *testing.T) {
	// The empty PATH is the tripwire: a warm attempt cannot find `go` and
	// fails. The selected test needs no toolchain, so it passes exactly when
	// TestMain skipped the build.
	out, err := reexecSelf(t, "^TestMainAppliesTheShardRunFile$",
		liveStackWarmEnv+"=", liveStackBinEnv+"=", "PATH="+t.TempDir())
	if err != nil {
		t.Fatalf("a unit-only -run selection warmed the live-stack binaries: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "warm live-stack binaries") {
		t.Fatalf("a unit-only -run selection reported a warm build:\n%s", out)
	}
}

// TestTestMainWarmsWhenALiveStackTestIsSelected is the other half: a selection
// that contains a live-stack test must reach the warm, which fails here because
// the re-executed process has no `go` on PATH.
func TestTestMainWarmsWhenALiveStackTestIsSelected(t *testing.T) {
	out, err := reexecSelf(t, "^TestModelListExplicitRefreshRecoversProvider$",
		liveStackWarmEnv+"=", liveStackBinEnv+"=", "PATH="+t.TempDir())
	if err == nil {
		t.Fatalf("a live-stack -run selection skipped the warm:\n%s", out)
	}
	if !strings.Contains(string(out), "warm live-stack binaries") {
		t.Fatalf("a live-stack selection did not reach the warm:\n%s", out)
	}
}

// probeLiveStackDirEnv marks the re-executed child of
// TestTestMainUsesTheExportedLiveStackDirectory. Like the shardrun probe it is
// deliberately not an EVENER_* name: TestMain's redirect clears product
// variables and a child that lost the mark would not assert anything.
const probeLiveStackDirEnv = "HUB_PROBE_LIVE_STACK_DIR"

// TestLiveStackBinariesUseTheExportedDirectory is the re-executed half: it
// proves liveStackBinariesBuild uses the directory a parent exported instead of
// compiling its own copy.
func TestLiveStackBinariesUseTheExportedDirectory(t *testing.T) {
	if os.Getenv(probeLiveStackDirEnv) != "1" {
		t.Skip("re-executed helper for TestTestMainUsesTheExportedLiveStackDirectory")
	}
	dir := liveStackBinaries(t, repoRootForTestMain())
	if want := os.Getenv(liveStackBinEnv); dir != want {
		t.Fatalf("liveStackBinaries = %q, want the exported directory %q", dir, want)
	}
	out, err := exec.Command(filepath.Join(dir, "evener"), "probe").CombinedOutput()
	if err != nil {
		t.Fatalf("running the exported binary: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "exported-live-stack" {
		t.Fatalf("exported binary printed %q", got)
	}
}

func TestTestMainUsesTheExportedLiveStackDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the exported-binary probe is a POSIX shell script")
	}
	dir := t.TempDir()
	probe := "#!/bin/sh\necho exported-live-stack\n"
	if err := os.WriteFile(filepath.Join(dir, "evener"), []byte(probe), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := reexecSelf(t, "^TestLiveStackBinariesUseTheExportedDirectory$",
		liveStackWarmEnv+"=1",
		liveStackBinEnv+"="+dir,
		probeLiveStackDirEnv+"=1",
	)
	if err != nil {
		t.Fatalf("the exported live-stack directory did not reach the test: %v\n%s", err, out)
	}
}

func TestMain(m *testing.M) {
	if os.Getenv(managerCleanupServeHelperEnv) == "1" {
		runManagerCleanupServeHelper()
		os.Exit(0)
	}
	// The remote startup probe must observe its supplied environment before
	// the suite redirects HOME and XDG roots.
	if os.Getenv(hostDeployEnvironmentChildEnv) == "1" {
		if err := runHostDeployEnvironmentProbe(); err != nil {
			fmt.Fprintf(os.Stderr, "remote startup environment: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv(detachHelperRunDirEnv) != "" {
		runDetachFakeDaemon()
		os.Exit(0)
	}
	// When evener dev hub-shards launches this binary as a shard, its
	// -test.run regex arrives through a file (see shardrun). flag.Parse must
	// run first so the command line's (absent) -test.run cannot clobber it.
	flag.Parse()
	if err := shardrun.ConfigureRunFile(); err != nil {
		fmt.Fprintf(os.Stderr, "evener-hub TestMain: %v\n", err)
		os.Exit(2)
	}
	// Collects the session scratch and temp containers that sessions under test
	// retain at close; testEnv's root is created inside it.
	hostTemp, err := sandboxtest.RedirectHostTemp("evener-hub-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "evener-hub TestMain: %v\n", err)
		os.Exit(2)
	}
	// A slow navigation build must not write a stats line to stderr from a test
	// that did not ask for it; tests asserting the line set WebConfig.Logf.
	navigationStatsLogfFor = func(cfg hubcore.WebConfig) func(string, ...any) {
		return cfg.Logf
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
		_ = hostTemp.Discard()
		os.Exit(1)
	}
	// Build the live-stack binaries before any test runs. liveStackBinaries
	// guards the build with a sync.Once, so without this the first test that
	// starts a stack pays for it: 1.77s of one test's 2.50s here, and on a
	// contended CI runner that build is what pushed
	// TestE2E_RetireOverlappedByAnotherOwnershipAction over its strict
	// per-test ceiling. That test builds a stack per subtest, so the cost
	// landed entirely on its first one.
	//
	// The warm is skipped when the selection has no live-stack test to use it:
	// a shard running only unit tests would otherwise compile a binary it
	// never starts. evener dev hub-shards builds the binaries once for the
	// whole run and passes liveStackBinEnv down, so its shards bind that
	// directory here instead.
	//
	// The parent warms once and marks the environment so descendants skip it.
	// The marker is set only when this process actually has binaries to hand
	// down: a descendant re-executed with a different -test.run must judge its
	// own selection, not inherit "nothing to build" from a parent that selected
	// a unit test.
	//
	// The build also refuses where no repo root resolves, which is what a
	// helper running from a session workspace sees.
	repoRoot := repoRootForTestMain()
	var warmErr error
	if dir := os.Getenv(liveStackBinEnv); dir != "" {
		// The shard runner built these once. Bind the directory now: tests
		// clear EVENER_* variables while building their own isolated
		// environments, and the once-guarded state must already hold the
		// shared path when they ask for it later.
		liveStackBinariesBuild(repoRoot)
		warmErr = liveStackBuild.err
		if warmErr == nil {
			_ = os.Setenv(liveStackWarmEnv, "1")
		}
	} else if os.Getenv(liveStackWarmEnv) == "" && liveStackTestSelected(effectiveRunPattern()) {
		warmErr = warmLiveStackBinaries(repoRoot)
		if warmErr == nil && repoRoot != "" {
			_ = os.Setenv(liveStackWarmEnv, "1")
		}
	}
	if warmErr != nil {
		fmt.Fprintf(os.Stderr, "evener-hub TestMain: warm live-stack binaries: %v\n", warmErr)
		testEnv.Discard()
		_ = hostTemp.Discard()
		os.Exit(1)
	}

	code := m.Run()
	root := os.Getenv(sandboxtest.RootVar)
	if hostCleanupRootMustBeRetained(root) {
		fmt.Fprintf(os.Stderr, "evener-hub TestMain: retaining HostTemp root after unproved SSH hub cleanup: %s\n", root)
	} else {
		if _, err := os.Stat(filepath.Join(root, retainHostCleanupRootMarker)); !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "evener-hub TestMain: checking SSH cleanup retention marker: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
		testEnv.Discard()
		if err := hostTemp.Discard(); err != nil {
			fmt.Fprintf(os.Stderr, "evener-hub TestMain: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

// effectiveRunPattern is the -test.run regex m.Run will apply, after
// shardrun.ConfigureRunFile has replaced a shard's command line with its
// partition. Empty selects every test.
func effectiveRunPattern() string {
	if f := flag.Lookup("test.run"); f != nil {
		return f.Value.String()
	}
	return ""
}

// liveStackTestSelected reports whether run selects a test that can start a
// live stack. TestMain warms the binaries only when one can be used: a shard
// whose -run selection holds only unit tests must not pay for a build it never
// touches.
//
// The classifier reads the package's test sources rather than a hand-kept name
// list. A new live-stack test is then warm-covered the day it is written, and a
// missed test costs nothing worse than the lazy build every live-stack test
// paid before the warm existed. A scan that cannot run is treated as "may need
// them": the warm is the safe answer when the selection cannot be classified.
func liveStackTestSelected(run string) bool {
	names, ok := liveStackTestNames()
	if !ok {
		return true
	}
	if len(names) == 0 {
		return false
	}
	if run == "" {
		return true
	}
	// testing matches each '/'-separated element of -run against one level of
	// test name, so only the first element can select a top-level test.
	top := strings.SplitN(run, "/", 2)[0]
	pattern, err := regexp.Compile(top)
	if err != nil {
		return true
	}
	for name := range names {
		if pattern.MatchString(name) {
			return true
		}
	}
	return false
}

// liveStackEntryPoints are the test helpers that reach a live stack. A test is
// live-stack when its body reaches one of these, directly or through helpers
// defined in the same package.
var liveStackEntryPoints = map[string]bool{
	"evenerArtifact":                    true,
	"liveStackBinaries":                 true,
	"startE2EHubStack":                  true,
	"startHubStack":                     true,
	"startHubStackOnProvider":           true,
	"startHubStackOnProviderWithEvener": true,
	"startHubStackWithOptions":          true,
}

// liveStackTestNames returns the top-level tests whose call graph reaches a
// live-stack entry point. ok is false when the package's sources could not be
// read or parsed. The scan runs once: TestMain asks before m.Run and the tests
// that pin the classification ask again for each case.
var liveStackTestNames = sync.OnceValues(scanLiveStackTestNames)

func scanLiveStackTestNames() (names map[string]bool, ok bool) {
	root := repoRootForTestMain()
	if root == "" {
		return nil, false
	}
	files, err := filepath.Glob(filepath.Join(root, "cmd", "evener-hub", "*_test.go"))
	if err != nil {
		return nil, false
	}
	fset := token.NewFileSet()
	refs := make(map[string]map[string]bool)
	tests := make(map[string]bool)
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, false
		}
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			// Methods share the bare-name key with functions: a selector call
			// (f.start()) records "start", and merging overloads only widens
			// the classification.
			set := refs[fn.Name.Name]
			if set == nil {
				set = make(map[string]bool)
				refs[fn.Name.Name] = set
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if ident, isIdent := n.(*ast.Ident); isIdent {
					set[ident.Name] = true
				}
				return true
			})
			if fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				tests[fn.Name.Name] = true
			}
		}
	}
	names = make(map[string]bool)
	for name := range tests {
		if reachesLiveStack(name, refs, make(map[string]bool)) {
			names[name] = true
		}
	}
	return names, true
}

// reachesLiveStack walks name's references to a live-stack entry point. seen
// keeps a self-referential helper from looping.
func reachesLiveStack(name string, refs map[string]map[string]bool, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	for ref := range refs[name] {
		if liveStackEntryPoints[ref] {
			return true
		}
		if _, defined := refs[ref]; defined && reachesLiveStack(ref, refs, seen) {
			return true
		}
	}
	return false
}

// TestLiveStackTestSelection pins the classifier TestMain uses to decide
// whether a -run selection can need the live-stack binaries.
func TestLiveStackTestSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  string
		want bool
	}{
		{"everything", "", true},
		{"a live-stack test", "^TestE2E_TurnControlReachesTheSession$", true},
		{"a live-stack test selected below the top level", "^TestE2E_TurnControlReachesTheSession$/a case", true},
		{"a test that reuses the shared binary", "^TestModelListExplicitRefreshRecoversProvider$", true},
		{"a unit test", "^TestGoSubprocessesCacheOutsideTheTestRoot$", false},
		{"the shard wiring test", "^TestMainAppliesTheShardRunFile$", false},
		{"nothing", "^TestNoSuchTest$", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveStackTestSelected(tc.run); got != tc.want {
				t.Fatalf("liveStackTestSelected(%q) = %v, want %v", tc.run, got, tc.want)
			}
		})
	}
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
		{Name: "userdirs.DefaultStateRoot", Path: defaultRoot(userdirs.DefaultStateRoot())},
		{Name: "plugins.DefaultRoot", Path: plugins.DefaultRoot()},
		{Name: "defaultMCPConfigPath", Path: defaultMCPConfigPath()},
		{Name: "DefaultHubStateRoot", Path: defaultRoot(DefaultHubStateRoot())},
		{Name: "DefaultConfigPath", Path: DefaultConfigPath()},
		{Name: "DefaultStateGlob", Path: defaultRoot(DefaultStateGlob())},
		{Name: "DefaultPastIndexDBPath", Path: defaultRoot(DefaultPastIndexDBPath())},
		{Name: "rendezvous.DefaultDir", Path: defaultRoot(rendezvous.DefaultDir())},
	}...)
}

// defaultRoot unwraps a default-root resolution. The throwaway test environment
// always carries a HOME, so a failure here is a broken test environment.
func defaultRoot(path string, err error) string {
	if err != nil {
		panic(err)
	}
	return path
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

func writeRendezvous(t testing.TB, dir string, e rendezvous.Entry) {
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
		if sandboxtest.Redirected(v) {
			continue // the test rig's own value, which replaced the seeded one
		}
		if value, ok := os.LookupEnv(v.Name); ok {
			t.Errorf("%s=%q survived TestMain; the hub, its daemons and `evener launch-check` all inherit it", v.Name, value)
		}
	}
}

// TestTestMainLeavesTheHostTempRedirectForChildren guards the order TestMain
// runs in: RedirectHostTemp exports EVENER_HOST_TEMP_BASES before
// hubtestenv.Redirect clears the product variables, and that clear must keep
// the value. Cleared, every evener and evener serve the non-short suite starts
// would sweep the developer's /tmp and /var/tmp at startup and reclaim other
// sessions' abandoned scratch.
func TestTestMainLeavesTheHostTempRedirectForChildren(t *testing.T) {
	if !sandboxtest.Redirected(envvars.EVENERHostTempBases) {
		t.Fatalf("%s = %q after TestMain, want the host temp redirect's own base", envvars.EVENERHostTempBases.Name, envvars.EVENERHostTempBases.Getenv())
	}
}

// chdirTemp is testing.T.Chdir minus the os.Open(".") the standard
// implementation performs first: the test log records it as a relative "open
// .", cmd/go resolves that against this package's directory and hashes the
// whole directory into the test cache key, so churn there -- frontend/dist is
// rebuilt unconditionally and is go:embed-pinned -- re-ran every test in this
// package. Changing directory with a saved path keeps the same isolation
// without that enumeration. It lives in this file so the next test that wants
// a different working directory finds it rather than reaching for t.Chdir.
func chdirTemp(t *testing.T, dir string) {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Set PWD before changing directory: t.Setenv carries T.Chdir's
	// parallel-test guard (checkParallel), and it has to fire while the process
	// is still where it started, or a parallel test could resolve relative
	// paths against the changed directory in the window before the panic.
	t.Setenv("PWD", abs)
	// Registered before the chdir so a failed one cannot leave the process in
	// the new directory.
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore working directory to %s: %v", old, err)
		}
	})
	if err := os.Chdir(abs); err != nil {
		t.Fatal(err)
	}
}

// TestMainAppliesTheShardRunFile pins the TestMain wiring evener dev
// hub-shards depends on to hand each shard its tests.
func TestMainAppliesTheShardRunFile(t *testing.T) {
	shardrun.RequireTestMainAppliesRunFile(t)
}
