//go:build linux || darwin

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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
func fakeGoTest(profiles map[string][2]int) func(module, profilePath string) error {
	return func(module, profilePath string) error {
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
func runCovFloor(t *testing.T, args []string, runGoTest func(string, string) error) (int, string, string) {
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
