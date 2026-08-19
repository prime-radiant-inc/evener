package main

// Tests for the `coverage-floor` subcommand of evener-dev, the Go port of
// scripts/coverage/test-coverage-floor.sh (and its companions coverage-union.sh
// and coverage-gaps.sh). RED phase: the production coverageFloor function and
// coverageFloorConfig type these tests drive do not exist yet, so this file
// does not compile until the green phase adds them.
//
// The shell script's contract, ported faithfully:
//
//   - For each module in --modules, run `go test -cover -coverprofile` (here
//     the injectable runGoTest) to produce a coverage profile, then count
//     covered/total statements via internal/devtool/covstmt.StmtCounts.
//   - Compute pct = 100 * covered / total, formatted to one decimal place.
//   - Look up the module's floor in the --floors file ("<module> <pct>" per
//     line; lines starting with '#' are comments; blank lines are skipped).
//   - Print a table to stdout: module, covered, total, cov%, floor, status.
//   - --check: exit non-zero (1) when a measured module's coverage is below
//     its floor minus the tolerance (default 0.5pp). Also fail when a floor-file
//     module was not measured (its floor is unenforced).
//   - --bless: raise floors to the measured percentage (upward only: the new
//     floor is max(old, measured)), preserving comment lines. A floor-file
//     module that was not measured keeps its existing floor. Exit 0.
//   - Default (no --check, no --bless): measure and print, exit 0. Warn to
//     stderr about floor-file modules that were not measured.
//
// runGoTest is injected so tests drive the parse/compare/bless path without
// spawning `go test`. A test's stub writes a pre-built coverage profile to
// the given profilePath; covstmt.StmtCounts then parses it for real, so the
// integration with the real counting package is exercised end-to-end.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/devtool/gatesurface"
)

// writeCovProfile writes a Go coverage profile at path with the given covered
// and total statement counts. The profile has one covered block of `covered`
// statements and one uncovered block of `total - covered` statements, so
// covstmt.StmtCounts reports exactly (covered, total). When covered is 0 only
// the uncovered block is emitted; when total == covered only the covered
// block is emitted.
func writeCovProfile(path string, covered, total int) error {
	uncovered := total - covered
	var b strings.Builder
	b.WriteString("mode: set\n")
	if covered > 0 {
		fmt.Fprintf(&b, "pkg/file.go:10.1,20.2 %d 1\n", covered)
	}
	if uncovered > 0 {
		fmt.Fprintf(&b, "pkg/file.go:30.1,40.2 %d 0\n", uncovered)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// fakeGoTest returns a runGoTest stub that writes a coverage profile for each
// module in profiles (map of module → {covered, total}). A module not in the
// map yields an error, modelling a `go test` failure. The stub creates the
// profile directory so it works regardless of how the implementation manages
// scratch space.
func fakeGoTest(profiles map[string][2]int) func(module, profilePath, logPath string) error {
	return func(module, profilePath, logPath string) error {
		ct, ok := profiles[module]
		if !ok {
			return fmt.Errorf("fake go test: no profile configured for module %q", module)
		}
		if dir := filepath.Dir(profilePath); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("fake go test: creating profile dir: %w", err)
			}
		}
		return writeCovProfile(profilePath, ct[0], ct[1])
	}
}

// writeFloorFile writes a floor file at path with the given raw content.
func writeFloorFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing floor file: %v", err)
	}
}

// readFloorFile reads a floor file at path, failing the test on error.
func readFloorFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading floor file: %v", err)
	}
	return string(b)
}

// floorPath returns a floor file path inside a fresh temp dir.
func floorPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "floors.txt")
}

// runCovFloor calls coverageFloor with the given config fields and returns
// the exit code, stdout, and stderr. It sets up stdout/stderr buffers and a
// default getenv (os.Getenv) so the function can read TMPDIR for its scratch
// directory; each test calls t.Setenv("TMPDIR", t.TempDir()) for hermeticity.
func runCovFloor(t *testing.T, args []string, runGoTest func(module, profilePath, logPath string) error) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	cfg := coverageFloorConfig{
		args:      args,
		getenv:    os.Getenv,
		stdout:    &stdout,
		stderr:    &stderr,
		runGoTest: runGoTest,
	}
	code := coverageFloor(cfg)
	return code, stdout.String(), stderr.String()
}

// ---------------------------------------------------------------------------

// TestCoverageFloorMeasureOnly: a fake profile with 80% coverage and no floor
// file. The command prints the measurement to stdout and exits 0. No floor
// file means no floor column value (shown as "—" or similar) and no regression
// check.
func TestCoverageFloorMeasureOnly(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t) // file deliberately NOT created

	code, stdout, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors},
		fakeGoTest(map[string][2]int{"agent": {80, 100}}),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"agent", "80", "100", "80.0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, stdout)
		}
	}
	if strings.Contains(stderr, "REGRESSION") {
		t.Errorf("stderr should not report a regression in measure-only mode; got:\n%s", stderr)
	}
}

// TestCoverageFloorCheckPasses: coverage 85%, floor 80%, --check. The measured
// percentage is at or above the floor (within tolerance), so the command exits
// 0 and reports a passing status.
func TestCoverageFloorCheckPasses(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\n")

	code, stdout, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors, "--check"},
		fakeGoTest(map[string][2]int{"agent": {85, 100}}),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"agent", "85", "100", "85.0", "80.0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, stdout)
		}
	}
	if strings.Contains(stderr, "REGRESSION") {
		t.Errorf("stderr should not report a regression when coverage meets the floor; got:\n%s", stderr)
	}
}

// TestCoverageFloorCheckFails: coverage 75%, floor 80%, --check. The measured
// percentage is below the floor minus tolerance (75 < 80 - 0.5 = 79.5), so the
// command exits 1 and reports the regression to stderr, naming the module and
// both the measured and floor percentages.
func TestCoverageFloorCheckFails(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\n")

	code, stdout, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors, "--check"},
		fakeGoTest(map[string][2]int{"agent": {75, 100}}),
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (coverage below floor); stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "REGRESSION") {
		t.Errorf("stderr should report a REGRESSION; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "agent") {
		t.Errorf("stderr should name the failing module 'agent'; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "75.0") {
		t.Errorf("stderr should report the measured percentage 75.0; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "80.0") {
		t.Errorf("stderr should report the floor percentage 80.0; got:\n%s", stderr)
	}
	// The table on stdout should also reflect the regression status.
	if !strings.Contains(stdout, "agent") {
		t.Errorf("stdout should contain the module row; got:\n%s", stdout)
	}
}

// TestCoverageFloorBless: coverage 85%, floor 80%, --bless. The floor file is
// raised to the measured percentage (85.0) and the command exits 0. The old
// floor value is replaced, not kept alongside the new one.
func TestCoverageFloorBless(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\n")

	code, _, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors, "--bless"},
		fakeGoTest(map[string][2]int{"agent": {85, 100}}),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	updated := readFloorFile(t, floors)
	if !strings.Contains(updated, "agent 85.0") {
		t.Errorf("floor file should contain 'agent 85.0' after bless; got:\n%s", updated)
	}
	if strings.Contains(updated, "agent 80.0") {
		t.Errorf("floor file should not retain the old floor 'agent 80.0'; got:\n%s", updated)
	}
}

// TestCoverageFloorBlessPreservesComments: the floor file has comment lines
// (starting with '#'). --bless carries them through unchanged while raising
// the measured module's floor.
func TestCoverageFloorBlessPreservesComments(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	original := "# Full-suite coverage floors.\n" +
		"# Managed by evener-dev coverage-floor --bless.\n" +
		"# A downward reset is a hand edit.\n" +
		"agent 80.0\n"
	writeFloorFile(t, floors, original)

	code, _, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors, "--bless"},
		fakeGoTest(map[string][2]int{"agent": {85, 100}}),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	updated := readFloorFile(t, floors)
	for _, comment := range []string{
		"# Full-suite coverage floors.",
		"# Managed by evener-dev coverage-floor --bless.",
		"# A downward reset is a hand edit.",
	} {
		if !strings.Contains(updated, comment) {
			t.Errorf("bless should preserve comment %q; got:\n%s", comment, updated)
		}
	}
	if !strings.Contains(updated, "agent 85.0") {
		t.Errorf("floor file should contain raised floor 'agent 85.0'; got:\n%s", updated)
	}
	if strings.Contains(updated, "agent 80.0") {
		t.Errorf("floor file should not retain old floor 'agent 80.0'; got:\n%s", updated)
	}
}

// TestCoverageFloorMultipleModules: two modules with different coverage and
// floors. Both are reported in the stdout table. agent (85% vs floor 80%)
// passes; llm (70% vs floor 75%) regresses, so --check exits 1 and stderr names
// llm as the failing module.
func TestCoverageFloorMultipleModules(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\nllm 75.0\n")

	code, stdout, stderr := runCovFloor(t,
		[]string{"--modules", "agent llm", "--floors", floors, "--check"},
		fakeGoTest(map[string][2]int{
			"agent": {85, 100},
			"llm":   {70, 100},
		}),
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (llm regresses); stderr:\n%s", code, stderr)
	}
	// Both modules should appear in the table.
	for _, mod := range []string{"agent", "llm"} {
		if !strings.Contains(stdout, mod) {
			t.Errorf("stdout should contain module %q; got:\n%s", mod, stdout)
		}
	}
	// agent's numbers.
	for _, want := range []string{"85", "85.0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout should contain agent's covered/pct %q; got:\n%s", want, stdout)
		}
	}
	// llm's numbers.
	for _, want := range []string{"70", "70.0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout should contain llm's covered/pct %q; got:\n%s", want, stdout)
		}
	}
	// stderr should name llm as the regressing module.
	if !strings.Contains(stderr, "llm") {
		t.Errorf("stderr should name the regressing module 'llm'; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "REGRESSION") {
		t.Errorf("stderr should report a REGRESSION for llm; got:\n%s", stderr)
	}
}

// TestCoverageFloorMissingModule: the floor file lists a module ("llm") that
// was not measured (only "agent" is in --modules). Under --check the unenforced
// floor fails the gate; under measure-only a warning goes to stderr but the
// command exits 0.
func TestCoverageFloorMissingModule(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\nllm 90.0\n")
	profiles := fakeGoTest(map[string][2]int{"agent": {85, 100}})

	t.Run("check", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		code, _, stderr := runCovFloor(t,
			[]string{"--modules", "agent", "--floors", floors, "--check"},
			profiles,
		)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (llm floor unenforced); stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "llm") {
			t.Errorf("stderr should name the unmeasured floored module 'llm'; got:\n%s", stderr)
		}
	})

	t.Run("measure-only", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		code, _, stderr := runCovFloor(t,
			[]string{"--modules", "agent", "--floors", floors},
			profiles,
		)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (measure-only warns); stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "llm") {
			t.Errorf("stderr should warn about unmeasured floored module 'llm'; got:\n%s", stderr)
		}
	})
}

// TestCoverageFloorUnmeasuredFloored: a floored module that WAS measured but
// got 0% coverage. Under --check this fails loudly: 0.0% is far below the
// 80.0% floor, so the command exits 1 and stderr reports the regression with
// both numbers.
func TestCoverageFloorUnmeasuredFloored(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\n")

	code, stdout, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors, "--check"},
		fakeGoTest(map[string][2]int{"agent": {0, 100}}),
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (0%% below floor); stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "REGRESSION") {
		t.Errorf("stderr should report a REGRESSION; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "agent") {
		t.Errorf("stderr should name the failing module 'agent'; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "0.0") {
		t.Errorf("stderr should report the measured percentage 0.0; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "80.0") {
		t.Errorf("stderr should report the floor percentage 80.0; got:\n%s", stderr)
	}
	// The table should still print the measurement.
	if !strings.Contains(stdout, "agent") {
		t.Errorf("stdout should contain the module row; got:\n%s", stdout)
	}
}

// TestCoverageFloorParseFloorFile: the floor file parser skips comment lines
// (starting with '#') and blank lines, and parses the percentage from each
// data line. Two modules with floors defined among comments and blanks both
// pass --check when their coverage exceeds their floors.
func TestCoverageFloorParseFloorFile(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors,
		"# Full-suite coverage floors.\n"+
			"# Managed by evener-dev coverage-floor --bless.\n"+
			"\n"+
			"agent 80.0\n"+
			"\n"+
			"# another comment\n"+
			"llm 90.0\n")

	code, stdout, stderr := runCovFloor(t,
		[]string{"--modules", "agent llm", "--floors", floors, "--check"},
		fakeGoTest(map[string][2]int{
			"agent": {85, 100}, // 85.0% >= 80.0 floor
			"llm":   {95, 100}, // 95.0% >= 90.0 floor
		}),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (both modules above floors); stderr:\n%s", code, stderr)
	}
	// Both modules and their floors should appear in the table.
	for _, want := range []string{"agent", "85.0", "80.0", "llm", "95.0", "90.0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, stdout)
		}
	}
	if strings.Contains(stderr, "REGRESSION") {
		t.Errorf("stderr should not report a regression; got:\n%s", stderr)
	}
}

// ---------------------------------------------------------------------------
// The rest of the shell script's contract, ported from
// scripts/coverage/test-coverage-floor-selftest.sh. These are the behaviours
// the ratchet actually defends: measuring the same surface the gate proves,
// refusing to quietly stop enforcing a floor, and keeping the evidence when a
// measurement fails.

// recordingGoTest returns a runGoTest stub that appends each module it is asked
// to measure to *seen, then delegates to the given behaviour.
func recordingGoTest(seen *[]string, inner func(module, profilePath, logPath string) error) func(string, string, string) error {
	return func(module, profilePath, logPath string) error {
		*seen = append(*seen, module)
		return inner(module, profilePath, logPath)
	}
}

// TestCoverageFloorDefaultModules: with no --modules, the command measures the
// same seven modules scripts/coverage/test-coverage-floor.sh defaulted to. A
// shorter default silently stops measuring whatever it dropped.
func TestCoverageFloorDefaultModules(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "")

	var seen []string
	code, _, stderr := runCovFloor(t,
		[]string{"--floors", floors},
		recordingGoTest(&seen, fakeGoTest(map[string][2]int{
			".": {80, 100}, "agent": {88, 100}, "llm": {90, 100}, "auth": {95, 100},
			"envvars": {100, 100}, "invariant": {0, 0}, "identifier": {95, 100},
		})),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	want := []string{".", "agent", "llm", "auth", "envvars", "invariant", "identifier"}
	if strings.Join(seen, " ") != strings.Join(want, " ") {
		t.Errorf("default modules = %q, want %q", seen, want)
	}
}

// TestCoverageFloorDefaultFloorsFile: with no --floors, the command reads
// scripts/coverage/testcov-global-floors.txt relative to the working directory,
// which is where the Makefile invokes it from. A wrong default reads no floors
// at all and every check passes vacuously.
func TestCoverageFloorDefaultFloorsFile(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "scripts", "coverage"), 0o755); err != nil {
		t.Fatalf("creating floors dir: %v", err)
	}
	writeFloorFile(t, filepath.Join(repo, "scripts", "coverage", "testcov-global-floors.txt"), "agent 90.0\n")
	t.Chdir(repo)

	code, _, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--check"},
		fakeGoTest(map[string][2]int{"agent": {85, 100}}),
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (85.0 is below the default file's 90.0 floor); stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "REGRESSION") {
		t.Errorf("stderr should report the regression read from the default floors file; got:\n%s", stderr)
	}
}

// TestCoverageFloorTolerance: --tolerance sets the wobble band. The same
// measurement is a regression at the default 0.5pp and clean at 3pp, so the
// flag is proven to reach the comparison rather than being parsed and dropped.
func TestCoverageFloorTolerance(t *testing.T) {
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\n")
	profiles := fakeGoTest(map[string][2]int{"agent": {78, 100}})

	t.Run("default tolerance fails", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		code, _, stderr := runCovFloor(t,
			[]string{"--modules", "agent", "--floors", floors, "--check"}, profiles)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (78.0 < 80.0 - 0.5); stderr:\n%s", code, stderr)
		}
	})

	t.Run("widened tolerance passes", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		code, _, stderr := runCovFloor(t,
			[]string{"--modules", "agent", "--floors", floors, "--check", "--tolerance", "3"}, profiles)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (78.0 >= 80.0 - 3); stderr:\n%s", code, stderr)
		}
		if strings.Contains(stderr, "REGRESSION") {
			t.Errorf("stderr should not report a regression inside the widened band; got:\n%s", stderr)
		}
	})
}

// TestCoverageFloorNoModule: a --modules entry with no go.mod is reported, not
// silently skipped. With a floor it is an unenforced ratchet and --check fails;
// without one it stays an advisory skip.
func TestCoverageFloorNoModule(t *testing.T) {
	noModule := func(module, profilePath, logPath string) error { return errCovFloorNoModule }

	t.Run("floored", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		floors := floorPath(t)
		writeFloorFile(t, floors, "nosuch 50.0\n")
		code, stdout, stderr := runCovFloor(t,
			[]string{"--modules", "nosuch", "--floors", floors, "--check"}, noModule)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (a floored module that cannot be measured); stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stdout, "no module") {
			t.Errorf("stdout should report the missing module; got:\n%s", stdout)
		}
		if !strings.Contains(stderr, "UNMEASURED") || !strings.Contains(stderr, "nosuch") {
			t.Errorf("stderr should name the unmeasurable floored module; got:\n%s", stderr)
		}
	})

	t.Run("unfloored", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		floors := floorPath(t)
		writeFloorFile(t, floors, "")
		code, stdout, stderr := runCovFloor(t,
			[]string{"--modules", "nosuch", "--floors", floors, "--check"}, noModule)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (nobody floored it); stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stdout, "no module") {
			t.Errorf("stdout should still report the skip; got:\n%s", stdout)
		}
	})
}

// TestCoverageFloorNoStatements: a profile that counts no statements is the
// shape every module degrades to when the counting itself breaks. It is not a
// 0.0% measurement - blessing it would write a bogus floor - so it is reported
// as unmeasured and fails --check for a floored module.
func TestCoverageFloorNoStatements(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\n")

	code, stdout, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors, "--check"},
		fakeGoTest(map[string][2]int{"agent": {0, 0}}),
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (a floored module whose profile counted nothing); stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "no statements") {
		t.Errorf("stdout should report the empty measurement; got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "UNMEASURED") || !strings.Contains(stderr, "agent") {
		t.Errorf("stderr should name the floored module with an empty measurement; got:\n%s", stderr)
	}
}

// TestCoverageFloorFailedMeasurementKeepsLog: a module whose `go test` fails is
// a failure of the run, and the only record of why is the output that run
// produced. The log survives the command and its path is printed.
func TestCoverageFloorFailedMeasurementKeepsLog(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\n")

	var logPaths []string
	code, stdout, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors},
		func(module, profilePath, logPath string) error {
			logPaths = append(logPaths, logPath)
			if err := os.WriteFile(logPath, []byte("boom: undefined: Thing\n"), 0o644); err != nil {
				return err
			}
			return fmt.Errorf("exit status 2")
		},
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (the measurement failed); stderr:\n%s", code, stderr)
	}
	if len(logPaths) != 1 {
		t.Fatalf("runGoTest should be given exactly one log path; got %v", logPaths)
	}
	if !strings.Contains(stdout+stderr, logPaths[0]) {
		t.Errorf("the retained log path %q should be printed; stdout:\n%s\nstderr:\n%s", logPaths[0], stdout, stderr)
	}
	body, err := os.ReadFile(logPaths[0])
	if err != nil {
		t.Fatalf("the failing module's log should survive the run: %v", err)
	}
	if !strings.Contains(string(body), "boom") {
		t.Errorf("the retained log should hold the failure output; got:\n%s", body)
	}
}

// TestCoverageFloorCleanRunLeavesNoScratch: a run with nothing to explain takes
// its scratch directory with it. The dev-tooling wave fails a suite that leaks.
func TestCoverageFloorCleanRunLeavesNoScratch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	floors := floorPath(t)
	writeFloorFile(t, floors, "agent 80.0\n")

	code, _, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors, "--check"},
		fakeGoTest(map[string][2]int{"agent": {85, 100}}),
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("reading TMPDIR: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a clean run should leave no scratch behind; TMPDIR holds %v", entries)
	}
}

// TestCoverageFloorBlessAppendsMeasuredModule: a measured module the floor file
// has never heard of gets a floor. Without this, blessing a newly measured
// module wrote nothing and the module stayed unratcheted forever.
func TestCoverageFloorBlessAppendsMeasuredModule(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, "# header note\nagent 80.0\n")

	code, _, stderr := runCovFloor(t,
		[]string{"--modules", "agent llm", "--floors", floors, "--bless"},
		fakeGoTest(map[string][2]int{"agent": {85, 100}, "llm": {70, 100}}),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	updated := readFloorFile(t, floors)
	for _, want := range []string{"# header note", "agent 85.0", "llm 70.0"} {
		if !strings.Contains(updated, want) {
			t.Errorf("blessed floor file should contain %q; got:\n%s", want, updated)
		}
	}
}

// TestCoverageFloorBlessPartialKeepsOtherFloors: improving one module at a time
// is how coverage work happens, so a partial bless must not drop the floors it
// did not measure.
func TestCoverageFloorBlessPartialKeepsOtherFloors(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	floors := floorPath(t)
	writeFloorFile(t, floors, ". 10.0\nagent 10.0\nllm 91.4\nauth 95.7\n")

	code, _, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--floors", floors, "--bless"},
		fakeGoTest(map[string][2]int{"agent": {75, 100}}),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
	}
	updated := readFloorFile(t, floors)
	for _, want := range []string{"agent 75.0", "llm 91.4", "auth 95.7", ". 10.0"} {
		if !strings.Contains(updated, want) {
			t.Errorf("a partial bless should keep %q; got:\n%s", want, updated)
		}
	}
}

// TestCoverageFloorBlessDoesNotHideAFailedRun: --bless that measured nothing,
// or that lost a module to a failed measurement, exits non-zero. A bless is the
// one mode with no ratchet of its own, so a silent exit 0 turns a broken run
// into "floors updated" and the next --check compares against nothing.
func TestCoverageFloorBlessDoesNotHideAFailedRun(t *testing.T) {
	t.Run("measurement failed", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		floors := floorPath(t)
		writeFloorFile(t, floors, "agent 80.0\nllm 70.0\n")
		code, _, stderr := runCovFloor(t,
			[]string{"--modules", "agent llm", "--floors", floors, "--bless"},
			fakeGoTest(map[string][2]int{"agent": {85, 100}}), // llm errors
		)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (llm's measurement failed); stderr:\n%s", code, stderr)
		}
		updated := readFloorFile(t, floors)
		if !strings.Contains(updated, "agent 85.0") {
			t.Errorf("what was measured should still be blessed upward; got:\n%s", updated)
		}
		if !strings.Contains(updated, "llm 70.0") {
			t.Errorf("the unmeasured module must keep its floor; got:\n%s", updated)
		}
	})

	t.Run("nothing measured", func(t *testing.T) {
		t.Setenv("TMPDIR", t.TempDir())
		floors := floorPath(t)
		writeFloorFile(t, floors, "agent 80.0\n")
		code, _, stderr := runCovFloor(t,
			[]string{"--modules", "agent", "--floors", floors, "--bless"},
			func(module, profilePath, logPath string) error { return errCovFloorNoModule },
		)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (nothing was measured); stderr:\n%s", code, stderr)
		}
		if !strings.Contains(readFloorFile(t, floors), "agent 80.0") {
			t.Errorf("a bless that measured nothing must not rewrite the floors")
		}
	})
}

// TestCoverageFloorUnknownFlag: a typo in a flag is refused with the shell's
// exit 2, rather than being ignored into a run that measures the defaults.
func TestCoverageFloorUnknownFlag(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	code, _, stderr := runCovFloor(t,
		[]string{"--modules", "agent", "--chekc"},
		fakeGoTest(map[string][2]int{"agent": {85, 100}}),
	)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (unknown flag); stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "--chekc") {
		t.Errorf("stderr should name the unknown flag; got:\n%s", stderr)
	}
}

// TestCovFloorGoTestArgs: the measured surface must stay the gate's surface. A
// ratchet that drifts from what `ROOT_FULL=1 make test` proves cannot be
// defended when its number moves.
func TestCovFloorGoTestArgs(t *testing.T) {
	pkgs := []string{"example.com/m/alpha", "example.com/m/beta"}

	root := strings.Join(covFloorGoTestArgs(".", "/tmp/root.cov", pkgs), " ")
	agent := strings.Join(covFloorGoTestArgs("agent", "/tmp/agent.cov", pkgs), " ")

	for name, args := range map[string]string{"root": root, "agent": agent} {
		if !strings.Contains(args, "-run "+gatesurface.TestRun) {
			t.Errorf("%s: the coverage run must use the gate's Test/Example filter; got: %s", name, args)
		}
		if !strings.Contains(args, "-skip "+gatesurface.FuzzTestSkip) {
			t.Errorf("%s: the coverage run must skip the fuzz-owned names; got: %s", name, args)
		}
		if !strings.Contains(args, "-count=1") {
			t.Errorf("%s: -count=1 is required or a cached profile reports stale numbers; got: %s", name, args)
		}
		// -coverpkg takes FILESYSTEM patterns, and under go.work `./...` also
		// matches every nested module, which made the root row a whole-repo number.
		if !strings.Contains(args, "-coverpkg=example.com/m/alpha,example.com/m/beta") {
			t.Errorf("%s: -coverpkg must name the module's own packages; got: %s", name, args)
		}
		if strings.Contains(args, "-coverpkg=./...") {
			t.Errorf("%s: -coverpkg must never be the tree-wide pattern; got: %s", name, args)
		}
		if !strings.Contains(args, "-coverprofile=") {
			t.Errorf("%s: the run must write a coverage profile; got: %s", name, args)
		}
	}
	// -short everywhere except the root module, mirroring run-module-tests.sh's
	// module_test_flags under ROOT_FULL=1.
	if strings.Contains(root, "-short") {
		t.Errorf("the root module must NOT be measured in -short mode (ROOT_FULL semantics); got: %s", root)
	}
	if !strings.Contains(agent, "-short") {
		t.Errorf("non-root modules must be measured in -short mode; got: %s", agent)
	}
}

// writeThrowawayModule creates a real, dependency-free Go module under dir with
// one covered and one uncovered statement, i.e. exactly 50.0% coverage.
func writeThrowawayModule(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating throwaway module: %v", err)
	}
	files := map[string]string{
		"go.mod": "module example.com/throwaway\n\ngo 1.25\n",
		"lib.go": "package throwaway\n\n" +
			"func Covered() int { return 1 }\n\n" +
			"func Uncovered() int { return 2 }\n",
		"lib_test.go": "package throwaway\n\nimport \"testing\"\n\n" +
			"func TestCovered(t *testing.T) {\n\tif Covered() != 1 {\n\t\tt.Fatal(\"Covered() is wrong\")\n\t}\n}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}

// TestCoverageFloorEndToEnd drives the real measurement path - the actual `go
// test -coverpkg -coverprofile` runGoTest, not a stub - against a real
// throwaway module whose coverage is known to be 50.0%. Every stubbed test
// above proves the arithmetic; only this one proves the command can measure
// anything at all.
func TestCoverageFloorEndToEnd(t *testing.T) {
	work := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	// The throwaway module lives outside the repo's go.work workspace; the
	// child toolchain must resolve its own go.mod instead.
	t.Setenv("GOWORK", "off")
	writeThrowawayModule(t, filepath.Join(work, "mod"))
	floors := filepath.Join(work, "floors.txt")
	t.Chdir(work)

	runReal := func(t *testing.T, extra ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr strings.Builder
		code := coverageFloor(coverageFloorConfig{
			args:      append([]string{"--modules", "mod", "--floors", floors}, extra...),
			getenv:    os.Getenv,
			stdout:    &stdout,
			stderr:    &stderr,
			runGoTest: realCovFloorGoTest,
		})
		return code, stdout.String(), stderr.String()
	}

	t.Run("measures the real module", func(t *testing.T) {
		writeFloorFile(t, floors, "mod 40.0\n")
		code, stdout, stderr := runReal(t, "--check")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (50.0%% clears the 40.0 floor); stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "50.0") {
			t.Errorf("stdout should report the module's real 50.0%% coverage; got:\n%s", stdout)
		}
	})

	// The mutation: a floor above the measured percentage must fail. Without
	// it the case above would pass against an implementation that measures
	// nothing and compares nothing.
	t.Run("a floor above the measurement fails", func(t *testing.T) {
		writeFloorFile(t, floors, "mod 90.0\n")
		code, _, stderr := runReal(t, "--check")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (50.0%% is below the 90.0 floor); stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "REGRESSION") {
			t.Errorf("stderr should report the regression; got:\n%s", stderr)
		}
	})

	t.Run("bless raises the floor to the real measurement", func(t *testing.T) {
		writeFloorFile(t, floors, "mod 10.0\n")
		code, _, stderr := runReal(t, "--bless")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr)
		}
		if updated := readFloorFile(t, floors); !strings.Contains(updated, "mod 50.0") {
			t.Errorf("bless should raise the floor to the measured 50.0; got:\n%s", updated)
		}
	})
}

// TestRealCovFloorGoTestMissingModule: the real measurement path reports a
// directory with no go.mod as unmeasurable rather than as a failed run.
func TestRealCovFloorGoTestMissingModule(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	err := realCovFloorGoTest("nosuch", filepath.Join(work, "p.cov"), filepath.Join(work, "p.log"))
	if !errors.Is(err, errCovFloorNoModule) {
		t.Fatalf("realCovFloorGoTest on a module-less path = %v, want errCovFloorNoModule", err)
	}
}
