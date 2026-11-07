package evener_test

import (
	"errors"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/internal/devtool/gatesurface"
	"primeradiant.com/evener/internal/devtool/procgroup"
)

// moduleRunnerScript is the gate that owns module selection, private per-module
// HOME/TMPDIR, wave scheduling, the concurrent frontend stream, and the
// zero-test refusal. Until this file, nothing drove that orchestration end to
// end: only the libraries under it had direct tests (gatebounded_test.go,
// gatebudgets_test.go), and make/testing.mk recorded the gap in its own comment.
// The cases here run the real script against tiny local Go modules built by the
// installed toolchain -- a real `go test` with a real exit status, never a faked
// `go` on PATH, which testing.md bans outright. They pin module selection, the
// explicit WAVE1/WAVE2 overrides, the private per-stream environment, caller
// flags reaching `go test`, failure propagation, cleanup, and the zero-test
// refusal. The root-module short-mode rewrite and the frontend (WEB) stream are
// left to the root module's own suites: exercising them would re-enter the real
// test tree or npm, not a bounded fixture.
const moduleRunnerScript = "scripts/gate/run-module-tests.sh"

// controlledScratchEnv makes the runner's scratch deterministic and test-owned.
// An impossibly large minimum forces gate_scratch_root's fallback to TMPDIR, so
// the gate's scratch (and any failed-run logs it keeps) lands under ctrl, a
// t.TempDir the test reclaims, rather than in a shared /dev/shm. WEB=0 keeps the
// frontend stream out of these Go-only cases.
func controlledScratchEnv(ctrl string) []string {
	return []string{"TMPDIR=" + ctrl, "GATE_SCRATCH_MIN_KB=1099511627776", "WEB=0"}
}

// runnerEnv builds the environment for one runner invocation. Every variable
// the runner or the nested `go` would otherwise read from an enclosing run is
// dropped: the runner's own GATE_* knobs (a case re-adds only the ones it
// wants), the module/wave/stream selection, and the Go toolchain controls. The
// nested `go` still needs the test's own build and module caches, so those are
// resolved explicitly from `go env` rather than inherited, making the
// dependency visible instead of ambient. GOFLAGS is forced empty so a caller's
// flags cannot make the runner refuse before its work.
func runnerEnv(t *testing.T, overrides ...string) []string {
	t.Helper()
	controlled := map[string]bool{
		"MODULES": true, "WAVE1": true, "WAVE2": true, "WEB": true,
		"TMPDIR": true, "EVENER_RUNNER_TEST_OBSERVE": true,
		// The race lane exports this to keep the hub-integration set in its
		// shards. A case that inherits it would silently run the default-gate
		// fixture with the skip already lifted.
		"EVENER_GATE_KEEP_HUB_INTEGRATION": true,
		// The timing capture's controls. An enclosing measured run exports
		// EVENER_GATE_TIMING_TSV, and a fixture runner that inherited it would
		// write fixture-package rows over the real --measured destination — and
		// leave them there even if the enclosing run failed. The others steer
		// the same capture, so they are stripped for the same reason.
		"EVENER_GATE_TIMING_TSV": true, "EVENER_SHARD_JSON_DIR": true,
		"EVENER_SHARD_JSON_PACKAGE": true, "EVENER_WEB_VITEST_JSON": true,
		"GOFLAGS": true, "GOCACHE": true, "GOMODCACHE": true, "GOPATH": true,
		"GOENV": true, "GOTOOLCHAIN": true, "GOPROXY": true, "GOSUMDB": true,
		"GOPRIVATE": true, "GONOPROXY": true, "GONOSUMDB": true, "GOTMPDIR": true,
		"GOWORK": true,
	}
	base := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if controlled[name] || strings.HasPrefix(name, "GATE_") {
			continue
		}
		base = append(base, entry)
	}
	// GOWORK is forced empty (auto-discovery) rather than inherited: an ambient
	// workspace path would make the fixture modules, which live outside it, fail
	// to resolve while the runner's own repo-root build still needs to find the
	// repository's go.work.
	base = append(base, "GOFLAGS=", "GOWORK=")
	for _, name := range []string{"GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOCACHE", "GOMODCACHE", "GOPATH"} {
		base = append(base, name+"="+goEnvValue(t, name))
	}
	return envOverride(base, overrides...)
}

// goEnvValue is the test process's own effective value for one `go env`
// variable, used to seed the nested runner's environment explicitly.
func goEnvValue(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("go", "env", name).Output()
	if err != nil {
		t.Fatalf("go env %s: %v", name, err)
	}
	return strings.TrimSpace(string(out))
}

// writeRunnerFixtureModule writes a tiny local module under root and returns it.
// The module carries no dependencies, so the installed toolchain compiles and
// runs it without the network.
func writeRunnerFixtureModule(t *testing.T, root, moduleName, testSrc string) {
	t.Helper()
	dir := filepath.Join(root, moduleName)
	writeTestFile(t, filepath.Join(dir, "go.mod"),
		[]byte("module example.com/"+moduleName+"\n\ngo 1.21\n"), 0o644)
	writeTestFile(t, filepath.Join(dir, moduleName+"_test.go"), []byte(testSrc), 0o644)
}

// newRunnerFixture lays out one case's throwaway tree: root holds everything,
// modules is the runner's workdir, and ctrl is the TMPDIR its scratch lands
// under so t.TempDir reclaims any logs a failing run keeps.
func newRunnerFixture(t *testing.T) (root, modules, ctrl string) {
	t.Helper()
	root = t.TempDir()
	modules = filepath.Join(root, "modules")
	ctrl = filepath.Join(root, "tmp")
	for _, dir := range []string{modules, ctrl} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, modules, ctrl
}

// observingModuleSrc is a fixture test that records the environment the gate
// handed it into $EVENER_RUNNER_TEST_OBSERVE/<module>.env. The gate runs it with
// the real toolchain, so the record is the runner's actual per-module
// environment, not a simulation of it.
func observingModuleSrc(moduleName string) string {
	return `package fixture

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestObserve(t *testing.T) {
	dir := os.Getenv("EVENER_RUNNER_TEST_OBSERVE")
	if dir == "" {
		t.Fatal("EVENER_RUNNER_TEST_OBSERVE is unset")
	}
	record := "HOME=" + os.Getenv("HOME") + "\n" +
		"TMPDIR=" + os.Getenv("TMPDIR") + "\n" +
		"XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME") + "\n" +
		"XDG_CACHE_HOME=" + os.Getenv("XDG_CACHE_HOME") + "\n" +
		"XDG_STATE_HOME=" + os.Getenv("XDG_STATE_HOME") + "\n" +
		"GOENV=" + os.Getenv("GOENV") + "\n" +
		"SHORT=" + strconv.FormatBool(testing.Short()) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "` + moduleName + `.env"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
}
`
}

// readRunnerObservation parses one module's recorded environment.
func readRunnerObservation(t *testing.T, observeDir, moduleName string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(observeDir, moduleName+".env"))
	if err != nil {
		t.Fatalf("read %s observation (did the module run?): %v", moduleName, err)
	}
	record := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		record[key] = value
	}
	return record
}

// moduleRunnerResult is one run's combined output and exit status.
type moduleRunnerResult struct {
	output   string
	exitCode int
}

// runGateCommand runs cmd and decodes its exit status into a
// moduleRunnerResult. label names the command in the failure message for an
// error that is not an exit status, such as a missing binary.
func runGateCommand(t *testing.T, cmd *exec.Cmd, label string) moduleRunnerResult {
	t.Helper()
	out, err := cmd.CombinedOutput()
	result := moduleRunnerResult{output: string(out)}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %s: %v\n%s", label, err, out)
		}
		result.exitCode = exitErr.ExitCode()
	}
	return result
}

// runModuleRunner runs the real gate from workDir, which holds the fixture
// modules. workDir is the process CWD, so the gate's relative `cd "$m"` reaches
// the fixtures while its repo root still resolves from the script's own
// location.
func runModuleRunner(t *testing.T, workDir string, env ...string) moduleRunnerResult {
	t.Helper()
	return runModuleRunnerWithFlags(t, workDir, []string{"-short", "-count=1"}, env...)
}

// runModuleRunnerWithFlags is runModuleRunner with the caller's own gate flags
// spelled out, so a case can drive the runner with a build flag (-tags) whose
// effect on the package inventory is exactly what it is pinning.
func runModuleRunnerWithFlags(t *testing.T, workDir string, flags []string, env ...string) moduleRunnerResult {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the module runner is a bash script")
	}
	script, err := filepath.Abs(moduleRunnerScript)
	if err != nil {
		t.Fatalf("abs %s: %v", moduleRunnerScript, err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("stat %s: %v", moduleRunnerScript, err)
	}
	cmd := exec.Command("bash", append([]string{script}, flags...)...)
	cmd.Dir = workDir
	cmd.Env = runnerEnv(t, env...)
	return runGateCommand(t, cmd, moduleRunnerScript)
}

// runnerRetainedLogDir returns the "... full logs: <dir>" path a failing run
// prints, so a case can assert what was kept.
func runnerRetainedLogDir(t *testing.T, output string) string {
	t.Helper()
	for line := range strings.SplitSeq(output, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "full logs: "); ok {
			return rest
		}
	}
	t.Fatalf("runner output names no retained log directory:\n%s", output)
	return ""
}

// runnerScratchDirs lists the gate scratch directories under ctrl.
func runnerScratchDirs(t *testing.T, ctrl string) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(ctrl, "evener-module-tests.*"))
	if err != nil {
		t.Fatalf("glob scratch under %s: %v", ctrl, err)
	}
	return dirs
}

// assertPrivateModuleEnv checks the hermetic environment the gate promises each
// stream: a per-module TMPDIR whose last element is the module name, with HOME
// and every XDG directory beneath it, and never the ambient TMPDIR.
func assertPrivateModuleEnv(t *testing.T, record map[string]string, module, ambientTMPDIR string) {
	t.Helper()
	tmp := record["TMPDIR"]
	if tmp == "" {
		t.Fatalf("%s recorded no TMPDIR", module)
	}
	if base := filepath.Base(tmp); base != module {
		t.Errorf("%s TMPDIR = %q, want its last element to be the module name %q", module, tmp, module)
	}
	if tmp == ambientTMPDIR {
		t.Errorf("%s TMPDIR = %q is the ambient TMPDIR; each stream must get a private one", module, tmp)
	}
	for key, sub := range map[string]string{
		"HOME":            "home",
		"XDG_CONFIG_HOME": "xdg-config",
		"XDG_CACHE_HOME":  "xdg-cache",
		"XDG_STATE_HOME":  "xdg-state",
	} {
		if want := filepath.Join(tmp, sub); record[key] != want {
			t.Errorf("%s %s = %q, want %q", module, key, record[key], want)
		}
	}
	if env := record["GOENV"]; env != "" && env != "off" && !strings.HasPrefix(env, tmp+string(filepath.Separator)) {
		t.Errorf("%s GOENV = %q, want it unset, off, or private under %q", module, env, tmp)
	}
}

// TestModuleRunnerRunsSelectedModulesWithPrivateHomes pins the selection and the
// isolation in one real run: only the MODULES-selected modules execute (an
// unscheduled module on disk stays untouched), each reports PASS, and each
// stream gets its own private HOME/TMPDIR/XDG. A regression that ran an
// unselected module, shared one environment between streams, or left the
// ambient HOME in place would flip an assertion even with every helper test
// green.
func TestModuleRunnerRunsSelectedModulesWithPrivateHomes(t *testing.T) {
	t.Parallel()
	root, modules, ctrl := newRunnerFixture(t)
	observeDir := filepath.Join(root, "observe")
	if err := os.MkdirAll(observeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, module := range []string{"alpha", "beta", "gamma"} {
		writeRunnerFixtureModule(t, modules, module, observingModuleSrc(module))
	}

	env := append(controlledScratchEnv(ctrl),
		"EVENER_RUNNER_TEST_OBSERVE="+observeDir,
		// gamma sits on disk but is not selected: it must not run.
		"MODULES=alpha beta",
	)
	result := runModuleRunner(t, modules, env...)
	if result.exitCode != 0 {
		t.Fatalf("runner exit = %d, want 0 for passing selected modules:\n%s", result.exitCode, result.output)
	}
	for _, module := range []string{"alpha", "beta"} {
		if !strings.Contains(result.output, "PASS  "+module) {
			t.Errorf("runner did not report PASS for selected module %s:\n%s", module, result.output)
		}
	}
	for _, verdict := range []string{"PASS  gamma", "FAIL  gamma"} {
		if strings.Contains(result.output, verdict) {
			t.Errorf("runner reported an unselected module:\n%s", result.output)
		}
	}
	if _, err := os.Stat(filepath.Join(observeDir, "gamma.env")); !os.IsNotExist(err) {
		t.Errorf("unselected module gamma ran (observe file err = %v)", err)
	}

	alpha := readRunnerObservation(t, observeDir, "alpha")
	beta := readRunnerObservation(t, observeDir, "beta")
	assertPrivateModuleEnv(t, alpha, "alpha", ctrl)
	assertPrivateModuleEnv(t, beta, "beta", ctrl)
	if alpha["TMPDIR"] == beta["TMPDIR"] {
		t.Errorf("alpha and beta share TMPDIR %q; each stream must get a private one", alpha["TMPDIR"])
	}
	// The caller's -short must reach the fixture's own go test.
	if alpha["SHORT"] != "true" {
		t.Errorf("fixture ran with testing.Short()=%q; the caller's -short must reach go test", alpha["SHORT"])
	}

	// A successful run reclaims its scratch.
	if leftovers := runnerScratchDirs(t, ctrl); len(leftovers) != 0 {
		t.Errorf("successful run left scratch behind: %v", leftovers)
	}
}

// TestModuleRunnerHonorsWaveOverrides pins that an explicit WAVE1/WAVE2 split
// selects the modules, not MODULES: a caller redistributes modules across waves
// without changing the coverage boundary, so a regression that ignored the
// overrides and fell back to the MODULES split would run the wrong set while the
// default-split case stayed green. The waved modules are deliberately absent
// from MODULES, and a module present only in MODULES must stay unrun.
func TestModuleRunnerHonorsWaveOverrides(t *testing.T) {
	t.Parallel()
	root, modules, ctrl := newRunnerFixture(t)
	observeDir := filepath.Join(root, "observe")
	if err := os.MkdirAll(observeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, module := range []string{"wave-one", "wave-two", "gamma"} {
		writeRunnerFixtureModule(t, modules, module, observingModuleSrc(module))
	}
	env := append(controlledScratchEnv(ctrl),
		"EVENER_RUNNER_TEST_OBSERVE="+observeDir,
		// MODULES names only gamma; the waves name the two modules that must run.
		"MODULES=gamma",
		"WAVE1=wave-one",
		"WAVE2=wave-two",
	)
	result := runModuleRunner(t, modules, env...)
	if result.exitCode != 0 {
		t.Fatalf("runner exit = %d, want 0:\n%s", result.exitCode, result.output)
	}
	for _, module := range []string{"wave-one", "wave-two"} {
		if !strings.Contains(result.output, "PASS  "+module) {
			t.Errorf("runner did not run WAVE-assigned module %s:\n%s", module, result.output)
		}
		if _, err := os.Stat(filepath.Join(observeDir, module+".env")); err != nil {
			t.Errorf("WAVE-assigned module %s did not run: %v", module, err)
		}
	}
	if _, err := os.Stat(filepath.Join(observeDir, "gamma.env")); !os.IsNotExist(err) {
		t.Errorf("module gamma ran from MODULES despite being in no wave (err = %v)", err)
	}
}

// TestModuleRunnerPropagatesAModuleFailure pins that a failing module's exit
// status reaches the runner's exit code and its output is surfaced, and that a
// failed run keeps its logs for the reader.
func TestModuleRunnerPropagatesAModuleFailure(t *testing.T) {
	t.Parallel()
	_, modules, ctrl := newRunnerFixture(t)
	writeRunnerFixtureModule(t, modules, "bad", `package fixture

import "testing"

func TestBad(t *testing.T) { t.Fatal("fixture failure") }
`)

	env := append(controlledScratchEnv(ctrl), "MODULES=bad")
	result := runModuleRunner(t, modules, env...)
	if result.exitCode == 0 {
		t.Fatalf("runner exit = 0 for a failing module:\n%s", result.output)
	}
	if !strings.Contains(result.output, "FAIL  bad") {
		t.Errorf("runner did not report the module failure:\n%s", result.output)
	}
	if !strings.Contains(result.output, "fixture failure") {
		t.Errorf("runner did not surface the failing test's output:\n%s", result.output)
	}
	logdir := runnerRetainedLogDir(t, result.output)
	if _, err := os.Stat(logdir); err != nil {
		t.Errorf("failed run did not retain its logs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logdir, "bad.log")); err != nil {
		t.Errorf("retained logs have no per-module log: %v", err)
	}
	if leftovers := runnerScratchDirs(t, ctrl); len(leftovers) == 0 {
		t.Errorf("failed run removed its scratch; logs a reader needs are gone")
	}
}

// TestModuleRunnerRefusesAZeroTestRun pins the silent-no-op guard: a module
// whose only tests are outside the gate's Test/Example surface reports PASS yet
// executes nothing, so the runner must fail the whole run rather than prove
// nothing.
func TestModuleRunnerRefusesAZeroTestRun(t *testing.T) {
	t.Parallel()
	_, modules, ctrl := newRunnerFixture(t)
	// A test file with no Test, Example, or Fuzz entrypoint: the gate's
	// Test/Example surface executes nothing, so the guard is unambiguous.
	writeRunnerFixtureModule(t, modules, "zero", `package fixture
`)

	env := append(controlledScratchEnv(ctrl), "MODULES=zero")
	result := runModuleRunner(t, modules, env...)
	if result.exitCode == 0 {
		t.Fatalf("runner exit = 0 when no scheduled module executed a test:\n%s", result.output)
	}
	if !strings.Contains(result.output, "ran zero tests") {
		t.Errorf("runner did not explain the silent no-op:\n%s", result.output)
	}
}

// hubIntegrationMembers returns the test names GATE_HUB_INTEGRATION_RUN
// selects, read from the Go home of the shared surface (byte-pinned to the
// shell lib by the gatesurface drift tests). Fixtures name their tests after
// real members, so a rename of the surface moves the fixture with it instead of
// failing the skip assertion as though the gate were broken. A member that is
// not a Go identifier means the surface's wrapper changed shape, which must not
// surface as a broken fixture.
func hubIntegrationMembers(t *testing.T) []string {
	t.Helper()
	body := strings.TrimSuffix(strings.TrimPrefix(gatesurface.HubIntegrationRun, "^("), ")$")
	names := strings.Split(body, "|")
	for _, name := range names {
		if !token.IsIdentifier(name) {
			t.Fatalf("HubIntegrationRun member %q is not a Go identifier; the fixture cannot name a test after it", name)
		}
	}
	return names
}

// markerTestSrc is a fixture file whose tests are named after members and
// record each execution in $EVENER_RUNNER_TEST_MARKER, so a case can tell which
// of them the gate ran. extra is appended verbatim: an always-run test keeps a
// run off the gate's zero-test refusal.
func markerTestSrc(pkg string, members []string, extra ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\n", pkg)
	for _, member := range members {
		fmt.Fprintf(&b, `func %s(t *testing.T) {
	path := os.Getenv("EVENER_RUNNER_TEST_MARKER")
	if path == "" {
		t.Fatal("EVENER_RUNNER_TEST_MARKER is unset")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("%s\n"); err != nil {
		t.Fatal(err)
	}
}

`, member, member)
	}
	b.WriteString(strings.Join(extra, "\n"))
	b.WriteString("\n")
	return b.String()
}

// TestModuleRunnerKeepsHubIntegrationOnlyWhenTold pins the
// EVENER_GATE_KEEP_HUB_INTEGRATION contract the race lane depends on: the
// default gate skips the tests GATE_HUB_INTEGRATION_RUN names, and setting the
// toggle to 1 runs them. Until this case, only the drift tests compared the
// regex text, so a misspelled toggle in the script would have left the race
// lane quietly skipping the deploy-host, retirement, and live-stack cases while
// every check stayed green. The fixture's test is a real member of that list and
// records its execution, so a skip that stopped reaching go test fails the
// default case and a toggle that stopped lifting it fails the kept case.
func TestModuleRunnerKeepsHubIntegrationOnlyWhenTold(t *testing.T) {
	t.Parallel()
	root, modules, ctrl := newRunnerFixture(t)
	member := hubIntegrationMembers(t)[0]
	writeTestFile(t, filepath.Join(modules, "go.mod"),
		[]byte("module example.com/fixtureroot\n\ngo 1.21\n"), 0o644)
	writeTestFile(t, filepath.Join(modules, "fixture_test.go"),
		[]byte(markerTestSrc("fixture", []string{member}, "func TestOrdinary(t *testing.T) {}")), 0o644)
	marker := filepath.Join(root, "hub-integration-ran")

	// The skip reaches the root module's own go test, so the fixture is that
	// module. Its shard runners are the next case's business: both run inline
	// here, leaving the root go test to carry the skip under test.
	base := append(controlledScratchEnv(ctrl),
		"MODULES=.",
		"HUB_SHARDS=0", "CLI_SHARDS=0", "ROOT_REST=1",
		"EVENER_RUNNER_TEST_MARKER="+marker,
	)

	skipped := runModuleRunner(t, modules, base...)
	if skipped.exitCode != 0 {
		t.Fatalf("default runner exit = %d, want 0:\n%s", skipped.exitCode, skipped.output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the default gate ran a GATE_HUB_INTEGRATION_RUN member (marker stat err = %v):\n%s", err, skipped.output)
	}

	kept := runModuleRunner(t, modules, envOverride(base, "EVENER_GATE_KEEP_HUB_INTEGRATION=1")...)
	if kept.exitCode != 0 {
		t.Fatalf("EVENER_GATE_KEEP_HUB_INTEGRATION=1 runner exit = %d, want 0:\n%s", kept.exitCode, kept.output)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("EVENER_GATE_KEEP_HUB_INTEGRATION=1 did not run the GATE_HUB_INTEGRATION_RUN member: %v\n%s", err, kept.output)
	}
	if !strings.Contains(string(got), member+"\n") {
		t.Fatalf("the hub-integration fixture recorded %q, want the member %q", got, member)
	}
}

// shardSkipObservingMainSrc is a fixture stand-in for `evener dev hub-shards`
// that records the HUB_SHARD_SKIP the gate hands the shard runner. The gate
// composes that skip in the shard environment, not in the root go test, and the
// race lane's hub runner is where the hub-integration set actually runs.
const shardSkipObservingMainSrc = `package main

import "os"

func main() {
	if err := os.WriteFile(os.Getenv("EVENER_RUNNER_TEST_SHARD_SKIP_FILE"), []byte(os.Getenv("HUB_SHARD_SKIP")), 0o644); err != nil {
		panic(err)
	}
}
`

// TestModuleRunnerShardsKeepHubIntegrationOnlyWhenTold pins the toggle on the
// path the race lane actually uses. With HUB_SHARDS=1 the root go test leaves
// cmd/evener-hub to the shard runner, and run-module-tests.sh composes that
// runner's HUB_SHARD_SKIP from root_skip plus the caller's own skip. A
// regression that passed only the fuzz skip there would leave the deploy-host,
// retirement, and live-stack cases unrun while the inline root-path case above
// and every other check stayed green. The fixture's evener-dev stand-in records
// the skip it received; the ordinary root test keeps the run off the gate's
// zero-test refusal honestly.
func TestModuleRunnerShardsKeepHubIntegrationOnlyWhenTold(t *testing.T) {
	t.Parallel()
	root, modules, ctrl := newRunnerFixture(t)
	member := hubIntegrationMembers(t)[0]
	writeTestFile(t, filepath.Join(modules, "go.mod"),
		[]byte("module example.com/fixtureroot\n\ngo 1.21\n"), 0o644)
	writeTestFile(t, filepath.Join(modules, "fixture_test.go"),
		[]byte("package fixture\n\nimport \"testing\"\n\nfunc TestOrdinary(t *testing.T) {}\n"), 0o644)
	writeTestFile(t, filepath.Join(modules, "cmd", "evener-dev", "bin", "main.go"),
		[]byte(shardSkipObservingMainSrc), 0o644)
	skipFile := filepath.Join(root, "hub-shard-skip")

	base := append(controlledScratchEnv(ctrl),
		"MODULES=.",
		"HUB_SHARDS=1", "CLI_SHARDS=elsewhere", "ROOT_REST=1",
		"EVENER_RUNNER_TEST_SHARD_SKIP_FILE="+skipFile,
	)
	for _, tc := range []struct {
		name       string
		env        []string
		wantMember bool
	}{
		{"default", base, true},
		{"kept", envOverride(base, "EVENER_GATE_KEEP_HUB_INTEGRATION=1"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runModuleRunner(t, modules, tc.env...)
			if result.exitCode != 0 {
				t.Fatalf("%s runner exit = %d, want 0:\n%s", tc.name, result.exitCode, result.output)
			}
			skip, err := os.ReadFile(skipFile)
			if err != nil {
				t.Fatalf("the shard runner received no HUB_SHARD_SKIP: %v\n%s", err, result.output)
			}
			if got := strings.Contains(string(skip), member); got != tc.wantMember {
				t.Fatalf("%s HUB_SHARD_SKIP contains the hub-integration member = %t, want %t:\n%s", tc.name, got, tc.wantMember, skip)
			}
			if !strings.Contains(string(skip), "SeqFuzz") {
				t.Fatalf("%s HUB_SHARD_SKIP is not the gate's composed skip: %q", tc.name, skip)
			}
		})
	}
}

// TestRaceRecipeKeepsHubIntegration pins the race lane's side of the toggle:
// `make test-race` exports EVENER_GATE_KEEP_HUB_INTEGRATION=1 to the module
// runner. The script-side case above proves the runner honours that name; this
// one proves the recipe still spells it, so a typo in testing.mk fails here
// instead of leaving the race lane silently skipping the hub-integration set.
// make -n prints the recipe without running it, so the check stays hermetic.
func TestRaceRecipeKeepsHubIntegration(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the recipe is a make target backed by a shell")
	}
	makefile, err := filepath.Abs(filepath.Join("make", "testing.mk"))
	if err != nil {
		t.Fatalf("abs make/testing.mk: %v", err)
	}
	cmd := exec.Command("make", "-f", makefile, "-n", "test-race")
	cmd.Env = runnerEnv(t, "MAKEFLAGS=", "MFLAGS=")
	result := runGateCommand(t, cmd, "make -n test-race")
	if result.exitCode != 0 {
		t.Fatalf("make -n test-race exit = %d:\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "EVENER_GATE_KEEP_HUB_INTEGRATION=1") {
		t.Fatalf("make -n test-race does not export EVENER_GATE_KEEP_HUB_INTEGRATION=1; the race lane would skip the hub-integration set:\n%s", result.output)
	}
}

// newHubIntegrationTargetFixture lays out a tiny stand-in hub package at the
// path `make test-hub-integration` tests, plus the real shared gate-surface lib
// the recipe sources, so the target runs its own recipe against the fixture
// instead of the repository's cmd/evener-hub.
func newHubIntegrationTargetFixture(t *testing.T, testSrc string) (root, marker string) {
	t.Helper()
	root = t.TempDir()
	copyRepositoryFile(t, ".", root, "scripts/lib/gate-surface-lib.sh", 0o644)
	writeTestFile(t, filepath.Join(root, "go.mod"),
		[]byte("module example.com/hubfixture\n\ngo 1.21\n"), 0o644)
	writeTestFile(t, filepath.Join(root, "cmd", "evener-hub", "hub_test.go"),
		[]byte(testSrc), 0o644)
	return root, filepath.Join(root, "hub-integration-ran")
}

// runHubIntegrationTarget runs the real make/testing.mk test-hub-integration
// recipe with root as the working directory. MAKEFLAGS is cleared: this test
// itself runs under make, and a jobserver the child cannot reach only adds
// noise to the run being judged. The make-level environment is pinned too:
// MAKEFILES could make the child read extra files, and the nested go test needs
// a writable TMPDIR/HOME rather than whatever the ambient environment has.
// testing.mk sets no SHELL/.SHELLFLAGS and includes nothing, so running it
// standalone is the shipped recipe; a Makefile-level shell change would have to
// reach this helper too.
func runHubIntegrationTarget(t *testing.T, root string, extraEnv ...string) moduleRunnerResult {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the target is a make recipe backed by a shell")
	}
	makefile, err := filepath.Abs(filepath.Join("make", "testing.mk"))
	if err != nil {
		t.Fatalf("abs make/testing.mk: %v", err)
	}
	if _, err := os.Stat(makefile); err != nil {
		t.Fatalf("stat %s: %v", makefile, err)
	}
	cmd := exec.Command("make", "-f", makefile, "test-hub-integration")
	cmd.Dir = root
	cmd.Env = runnerEnv(t, append([]string{
		"GOWORK=off",
		"MAKEFLAGS=", "MFLAGS=", "MAKEFILES=", "GNUMAKEFLAGS=",
		"TMPDIR=" + t.TempDir(), "HOME=" + t.TempDir(),
	}, extraEnv...)...)
	return runGateCommand(t, cmd, "make test-hub-integration")
}

// TestHubIntegrationTargetRefusesAZeroMatch pins the refusal in
// `make test-hub-integration`: `go test -run` exits 0 when its pattern matches
// no test, so a GATE_HUB_INTEGRATION_RUN that stopped matching would let the
// target pass while running nothing — the exact silent no-op PR #222 shipped in
// a Makefile copy. The positive control first proves the target can pass and
// execute a real list member on this fixture; the same fixture with a
// non-member test name must then fail with the refusal message instead of
// passing.
func TestHubIntegrationTargetRefusesAZeroMatch(t *testing.T) {
	t.Parallel()
	members := hubIntegrationMembers(t)
	probed := []string{members[0], members[len(members)-1]}
	if probed[0] == probed[1] {
		probed = probed[:1]
	}
	root, marker := newHubIntegrationTargetFixture(t, markerTestSrc("hub", probed))
	matched := runHubIntegrationTarget(t, root, "EVENER_RUNNER_TEST_MARKER="+marker)
	if matched.exitCode != 0 {
		t.Fatalf("test-hub-integration exit = %d for a fixture whose test matches the shipped regex:\n%s", matched.exitCode, matched.output)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the target did not execute the matching fixture tests: %v\n%s", err, matched.output)
	}
	for _, member := range probed {
		if !strings.Contains(string(got), member+"\n") {
			t.Fatalf("the target did not execute the matching fixture test %s; a narrower -run than the list guard would pass this control:\n%s", member, got)
		}
	}

	root, _ = newHubIntegrationTargetFixture(t, `package hub

import "testing"

func TestUnrelated(t *testing.T) {}
`)
	zero := runHubIntegrationTarget(t, root)
	if zero.exitCode == 0 {
		t.Fatalf("test-hub-integration exit = 0 when GATE_HUB_INTEGRATION_RUN matched no test; the refusal did not fire:\n%s", zero.output)
	}
	if !strings.Contains(zero.output, "matched no tests") {
		t.Fatalf("test-hub-integration failed without the zero-match refusal message:\n%s", zero.output)
	}
}

// countingModuleSrc is a fixture test that appends one line per execution to
// $EVENER_RUNNER_TEST_COUNT, so a case can count how many times the gate ran
// it: the measured form of issue #144's "no test executes twice".
func countingModuleSrc() string {
	return `package fixture

import (
	"os"
	"testing"
)

func TestCounted(t *testing.T) {
	f, err := os.OpenFile(os.Getenv("EVENER_RUNNER_TEST_COUNT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("run\n"); err != nil {
		t.Fatal(err)
	}
}
`
}

// TestModuleRunnerCapturesTheTimingRowsItAlreadyRan pins issue #144's whole
// point: with EVENER_GATE_TIMING_TSV set, one gate run both proves the tests and
// writes the ratchet's rows for them, and the fixture's test executes exactly
// once -- the capture adds no second run. The count file is the measurement; the
// rows are asserted structurally.
func TestModuleRunnerCapturesTheTimingRowsItAlreadyRan(t *testing.T) {
	t.Parallel()
	root, modules, ctrl := newRunnerFixture(t)
	writeRunnerFixtureModule(t, modules, "timingmod", countingModuleSrc())
	counts := filepath.Join(root, "counts")
	tsv := filepath.Join(root, "measured.tsv")
	res := runModuleRunner(t, modules, append(controlledScratchEnv(ctrl),
		"MODULES=timingmod",
		"EVENER_GATE_TIMING_TSV="+tsv,
		"EVENER_RUNNER_TEST_COUNT="+counts)...)
	if res.exitCode != 0 {
		t.Fatalf("runner exit = %d, want 0\n%s", res.exitCode, res.output)
	}
	ran, err := os.ReadFile(counts)
	if err != nil {
		t.Fatalf("the fixture test did not run: %v\n%s", err, res.output)
	}
	if got := strings.Count(string(ran), "run\n"); got != 1 {
		t.Fatalf("the fixture test executed %d times, want exactly 1 (issue #144: no test may run twice):\n%s", got, res.output)
	}
	rows, err := os.ReadFile(tsv)
	if err != nil {
		t.Fatalf("no timing rows written: %v\n%s", err, res.output)
	}
	for _, want := range []string{
		"PKG\texample.com/timingmod\n",
		"SUM\texample.com/timingmod\t",
		"TEST\texample.com/timingmod\tTestCounted\t",
	} {
		if !strings.Contains(string(rows), want) {
			t.Fatalf("timing rows missing %q:\n%s", want, rows)
		}
	}
}

// writeRunnerFixtureFile writes one file into a fixture's module tree, creating
// the directories it needs.
func writeRunnerFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, []byte(content), 0o644)
}

// TestModuleRunnerEnumerationHonorsTheCallersBuildFlags pins issue #144 review
// finding 2: the plain-module package inventory must be enumerated with the
// caller's package-selection flags, exactly as the root and sharded-agent paths
// already do. `-tags` can drop a whole package from `./...`; a flagless
// `go list ./...` still reports it, and the completeness oracle then rejects an
// otherwise green run as missing that package's terminal event. The fixture's
// `legacy` package exists only without the `special` tag.
func TestModuleRunnerEnumerationHonorsTheCallersBuildFlags(t *testing.T) {
	t.Parallel()
	root, modules, ctrl := newRunnerFixture(t)
	mod := filepath.Join(modules, "tagmod")
	writeRunnerFixtureFile(t, filepath.Join(mod, "go.mod"), "module example.com/tagmod\n\ngo 1.21\n")
	writeRunnerFixtureFile(t, filepath.Join(mod, "plain", "plain_test.go"), `package plain

import "testing"

func TestPlain(t *testing.T) {}
`)
	writeRunnerFixtureFile(t, filepath.Join(mod, "legacy", "legacy.go"), "//go:build !special\n\npackage legacy\n")
	writeRunnerFixtureFile(t, filepath.Join(mod, "legacy", "legacy_test.go"), `//go:build !special

package legacy

import "testing"

func TestLegacy(t *testing.T) {}
`)

	tsv := filepath.Join(root, "measured.tsv")
	res := runModuleRunnerWithFlags(t, modules, []string{"-short", "-count=1", "-tags", "special"},
		append(controlledScratchEnv(ctrl),
			"MODULES=tagmod",
			"EVENER_GATE_TIMING_TSV="+tsv)...)
	if res.exitCode != 0 {
		t.Fatalf("runner exit = %d for a green run whose -tags excludes a package; the inventory ignored the caller's flags:\n%s", res.exitCode, res.output)
	}
	rows, err := os.ReadFile(tsv)
	if err != nil {
		t.Fatalf("no timing rows written: %v\n%s", err, res.output)
	}
	if !strings.Contains(string(rows), "PKG\texample.com/tagmod/plain\n") {
		t.Fatalf("timing rows do not record the package the run measured:\n%s", rows)
	}
	if strings.Contains(string(rows), "legacy") {
		t.Fatalf("the inventory still names a package -tags excluded from the run:\n%s", rows)
	}
}

// TestModuleRunnerLeavesAnEnclosingTimingDestinationUntouched pins the review
// finding on runnerEnv: it must strip the timing-control variables an enclosing
// measured run exports, or a fixture runner the root suite starts inherits
// EVENER_GATE_TIMING_TSV and writes its fixture-package rows over the real
// `--measured` destination. A later replay would then read the fixture rows as
// the enclosing run's measurement, and read them even when that run failed —
// the opposite of the green-run-only contract the capture rests on.
func TestModuleRunnerLeavesAnEnclosingTimingDestinationUntouched(t *testing.T) {
	// Not parallel: t.Setenv simulates an enclosing measured run's ambient destination.
	_, modules, ctrl := newRunnerFixture(t)
	writeRunnerFixtureModule(t, modules, "plainmod", `package fixture

import "testing"

func TestPlain(t *testing.T) {}
`)

	// The destination an enclosing measured run would have exported.
	dest := filepath.Join(t.TempDir(), "enclosing-timing.tsv")
	const sentinel = "ENCLOSING MEASUREMENT\n"
	writeTestFile(t, dest, []byte(sentinel), 0o644)
	t.Setenv("EVENER_GATE_TIMING_TSV", dest)

	res := runModuleRunner(t, modules, append(controlledScratchEnv(ctrl), "MODULES=plainmod")...)
	if res.exitCode != 0 {
		t.Fatalf("runner exit = %d, want 0:\n%s", res.exitCode, res.output)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("the enclosing timing destination is gone: %v", err)
	}
	if string(got) != sentinel {
		t.Fatalf("a fixture run wrote over the enclosing timing destination:\n%s", got)
	}
}

// TestModuleRunnerKeepsItsRowsWhenTheTimingExportFails pins the review finding
// on the timing export: it runs after the failure-log retention block, so a
// write failure used to let cleanup delete the scratch directory holding the
// successful measurements and force the whole lane to run again. The run must
// fail, leave nothing at the destination, and keep the captured rows.
func TestModuleRunnerKeepsItsRowsWhenTheTimingExportFails(t *testing.T) {
	t.Parallel()
	root, modules, ctrl := newRunnerFixture(t)
	writeRunnerFixtureModule(t, modules, "timingmod", `package fixture

import "testing"

func TestTimed(t *testing.T) {}
`)

	// A destination whose parent directory does not exist: the export cannot land.
	dest := filepath.Join(root, "no-such-dir", "measured.tsv")
	res := runModuleRunner(t, modules, append(controlledScratchEnv(ctrl),
		"MODULES=timingmod",
		"EVENER_GATE_TIMING_TSV="+dest)...)
	if res.exitCode == 0 {
		t.Fatalf("a timing export to an unwritable destination must fail the run:\n%s", res.output)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a timing file was left at %s (stat err = %v); the destination must stay untouched", dest, err)
	}
	leftovers := runnerScratchDirs(t, ctrl)
	if len(leftovers) == 0 {
		t.Fatalf("a failed timing export deleted the scratch holding the captured rows")
	}
	rows, err := filepath.Glob(filepath.Join(leftovers[0], "json", "*.rows"))
	if err != nil || len(rows) == 0 {
		t.Fatalf("the retained scratch holds no timing rows (glob err = %v)", err)
	}
}

// TestModuleRunnerRefusesADirectoryTimingDestination pins the review finding on
// the atomic publish: renaming the temp file onto a destination that is a
// directory (or a symlink to one) moves it inside that directory and succeeds,
// so the run would exit green while publishing nothing at the requested path.
// The destination must be refused through the export-failure path, with the
// captured rows retained.
func TestModuleRunnerRefusesADirectoryTimingDestination(t *testing.T) {
	t.Parallel()
	root, modules, ctrl := newRunnerFixture(t)
	writeRunnerFixtureModule(t, modules, "timingmod", `package fixture

import "testing"

func TestTimed(t *testing.T) {}
`)

	dest := filepath.Join(root, "timing-destination")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	res := runModuleRunner(t, modules, append(controlledScratchEnv(ctrl),
		"MODULES=timingmod",
		"EVENER_GATE_TIMING_TSV="+dest)...)
	if res.exitCode == 0 {
		t.Fatalf("a directory timing destination must fail the run:\n%s", res.output)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("reading the destination directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the timing capture was moved into the destination directory: %v", entries)
	}
	if leftovers := runnerScratchDirs(t, ctrl); len(leftovers) == 0 {
		t.Fatalf("a refused timing destination deleted the scratch holding the captured rows")
	}
}

// TestModuleRunnerDoesNotWriteThroughASiblingSymlink pins the review finding on
// the temp file's name. The capture used to write `<dest>.tmp-<script pid>` with
// plain truncating redirection, so a symlink planted at that predictable path
// was followed and its target overwritten before publication. The temp file is
// created exclusively instead, so a planted symlink is never written through.
//
// The fixture holds its own test open until the caller has planted the symlink,
// so the plant is provably before the export rather than racing it: a test that
// only hoped to win the race could pass against the vulnerable implementation.
func TestModuleRunnerDoesNotWriteThroughASiblingSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the module runner is a bash script")
	}
	root, modules, ctrl := newRunnerFixture(t)
	writeRunnerFixtureModule(t, modules, "timingmod", `package fixture

import (
	"os"
	"testing"
	"time"
)

// TestTimed announces itself, then blocks until the caller releases it, so the
// caller can plant its symlink before this run reaches its timing export.
func TestTimed(t *testing.T) {
	started := os.Getenv("FIXTURE_STARTED")
	release := os.Getenv("FIXTURE_RELEASE")
	if started == "" || release == "" {
		return
	}
	if err := os.WriteFile(started, []byte("started\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(release); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the handshake release file never appeared")
}
`)

	dest := filepath.Join(root, "measured.tsv")
	victim := filepath.Join(root, "victim.txt")
	const precious = "PRECIOUS\n"
	writeTestFile(t, victim, []byte(precious), 0o644)
	started := filepath.Join(root, "fixture-started")
	release := filepath.Join(root, "fixture-release")

	script, err := filepath.Abs(moduleRunnerScript)
	if err != nil {
		t.Fatalf("abs %s: %v", moduleRunnerScript, err)
	}
	cmd := exec.Command("bash", script, "-short", "-count=1")
	cmd.Dir = modules
	cmd.Env = runnerEnv(t, append(controlledScratchEnv(ctrl),
		"MODULES=timingmod", "EVENER_GATE_TIMING_TSV="+dest,
		"FIXTURE_STARTED="+started, "FIXTURE_RELEASE="+release)...)
	// The output goes to a file, not a pipe: the runner's own `run_bounded` puts
	// its monitored commands in their own process groups, so a stop that reaches
	// only bash's group can leave a descendant holding a pipe open and block
	// Wait. With a file there is nothing for a survivor to hold.
	outPath := filepath.Join(root, "runner-output.log")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("creating the output file: %v", err)
	}
	cmd.Stdout, cmd.Stderr = outFile, outFile
	runnerOutput := func() string {
		data, readErr := os.ReadFile(outPath)
		if readErr != nil {
			return ""
		}
		return string(data)
	}
	// The runner is a bash script that forks the module's `go test`; starting it
	// through procgroup puts the whole tree in one process group so a failed
	// handshake is reaped as a unit rather than leaving bash's group alive.
	if err := procgroup.Start(cmd); err != nil {
		_ = outFile.Close()
		t.Fatalf("starting the runner: %v", err)
	}
	abort := func() {
		// bash's group is not the whole tree: run_bounded enables job control, so
		// its monitored commands and their builds sit in process groups of their
		// own. Walk the tree and KILL each descendant, deepest first, before
		// reaping bash — a signal aimed only at bash's group would orphan them.
		_ = exec.Command("bash", "-c",
			`walk() { for c in $(pgrep -P "$1" 2>/dev/null); do walk "$c"; done; kill -9 "$1" 2>/dev/null; }; walk "$1"`,
			"bash", strconv.Itoa(cmd.Process.Pid)).Run()
		procgroup.Kill(cmd.Process.Pid)
		_ = cmd.Wait()
		_ = outFile.Close()
	}
	// The fixture's own test is inside its run by now, which is before the
	// export by construction.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			abort()
			t.Fatalf("the fixture never announced itself:\n%s", runnerOutput())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// $$ in the script is this bash process's pid.
	planted := fmt.Sprintf("%s.tmp-%d", dest, cmd.Process.Pid)
	if err := os.Symlink(victim, planted); err != nil {
		abort()
		t.Fatalf("planting the symlink at %s: %v", planted, err)
	}
	writeTestFile(t, release, []byte("go\n"), 0o644)
	waitErr := cmd.Wait()
	_ = outFile.Close()

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("reading the symlink's target: %v", err)
	}
	if string(got) != precious {
		t.Fatalf("the capture wrote through a planted sibling symlink:\n%s", got)
	}
	if waitErr != nil {
		t.Fatalf("the run failed with the symlink planted: %v\n%s", waitErr, runnerOutput())
	}
	if rows, err := os.ReadFile(dest); err != nil || !strings.Contains(string(rows), "TEST\t") {
		t.Fatalf("the run published no timing rows at %s (err = %v):\n%s", dest, err, runnerOutput())
	}
}
