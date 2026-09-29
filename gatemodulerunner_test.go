package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

// runModuleRunner runs the real gate from workDir, which holds the fixture
// modules. workDir is the process CWD, so the gate's relative `cd "$m"` reaches
// the fixtures while its repo root still resolves from the script's own
// location.
func runModuleRunner(t *testing.T, workDir string, env ...string) moduleRunnerResult {
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
	cmd := exec.Command("bash", script, "-short", "-count=1")
	cmd.Dir = workDir
	cmd.Env = runnerEnv(t, env...)
	out, err := cmd.CombinedOutput()
	result := moduleRunnerResult{output: string(out)}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %s: %v\n%s", moduleRunnerScript, err, out)
		}
		result.exitCode = exitErr.ExitCode()
	}
	return result
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
