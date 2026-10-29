package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// timingBudgetScript is the runner under test.
const timingBudgetScript = "scripts/gate/test-timing-budget.sh"

// hugeCeilingSeconds is a JSON integer too large to convert to a float — the
// shape issue #4629 crashed on — shared by the tests that drive it.
var hugeCeilingSeconds = strings.Repeat("9", 400)

// runTimingBudgetCompare drives the runner's comparison half under the CI-strict
// policy through the --measured fixture seam: the fixture IS the measurement the
// comparator reads, so no go test or vitest run is involved and no toolchain is
// faked (docs/developing-evener/testing.md's ban on PATH stubs).
func runTimingBudgetCompare(t *testing.T, measured, budget string) (string, error) {
	t.Helper()
	return runTimingBudgetCompareMode(t, measured, budget, true)
}

// runTimingBudgetScript drives the comparison over --measured fixtures with the
// given extra flags and environment. The fixture IS the measurement the
// comparator reads, so no go test or vitest run is involved and no toolchain is
// faked (docs/developing-evener/testing.md's ban on PATH stubs).
func runTimingBudgetScript(t *testing.T, measured, budget string, flags, extraEnv []string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	measuredPath := filepath.Join(dir, "measured.tsv")
	writeAuditScriptFixture(t, measuredPath, measured)
	budgetPath := filepath.Join(dir, "budget.json")
	writeAuditScriptFixture(t, budgetPath, budget)
	return runTimingBudgetFixture(t, measuredPath, budgetPath, dir, flags, extraEnv)
}

// runTimingBudgetFixture runs the runner against explicit on-disk fixture paths
// with the given extra flags and environment. The caller writes the fixtures, so
// budgetPath may name a file that does not exist — that is how the missing-file
// warn state is exercised — and measuredPath and budgetPath may be asserted in
// the output. dir receives the runner's scratch (dir/tmp).
func runTimingBudgetFixture(t *testing.T, measuredPath, budgetPath, dir string, flags, extraEnv []string) (string, error) {
	t.Helper()
	tmpDir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", tmpDir, err)
	}
	args := append([]string{timingBudgetScript,
		"--measured", measuredPath, "--budget", budgetPath, "--no-web"}, flags...)
	cmd := exec.Command("bash", args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "TMPDIR=" + tmpDir}, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runTimingBudgetCompareMode drives the comparison with an explicit posture, so a
// test can pin both CI's enforcement (--strict) and the local warn-only run
// (--local): the two differ only in the exit status, never in the diagnostics.
func runTimingBudgetCompareMode(t *testing.T, measured, budget string, strict bool) (string, error) {
	t.Helper()
	mode := "--local"
	if strict {
		mode = "--strict"
	}
	return runTimingBudgetScript(t, measured, budget, []string{"--check", mode}, nil)
}

// TestTimingBudgetWarnsOnBudgetsInThePreWallTimeUnits pins issue #172 review
// finding 1. The checked-in testing-budget.json numbers were blessed under the
// old sum-of-test-Elapsed metric, so comparing them to the package wall time
// measured now is not meaningful. Until #141 regenerates the baseline, a budget
// in the old units must be warned about, never enforced: the ratio is reported
// as a WARN naming #141, and --check --strict still exits zero.
func TestTimingBudgetWarnsOnBudgetsInThePreWallTimeUnits(t *testing.T) {
	measured := "SUM\texample.com/mod\t10.00\n"
	budget := `{"perTestCeilingSeconds":3,"packages":{"example.com/mod":1}}`

	out, err := runTimingBudgetCompare(t, measured, budget)
	if err != nil {
		t.Fatalf("a budget blessed under the old metric must not fail the run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "STALE UNITS") || !strings.Contains(out, "#141") {
		t.Fatalf("no explicit stale-units warning naming #141:\n%s", out)
	}
	if !strings.Contains(out, "WARN  example.com/mod") {
		t.Fatalf("a stale number over 1.5x must be reported as a warning:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("a stale number was enforced as a failure:\n%s", out)
	}
}

// TestTimingBudgetFailsOnWallTimeBudgets is the other half: once a rebaseline
// records the wall-time metric marker, the same excess is enforced again.
func TestTimingBudgetFailsOnWallTimeBudgets(t *testing.T) {
	measured := "SUM\texample.com/mod\t10.00\n"
	budget := `{"metric":"package-wall-seconds","perTestCeilingSeconds":3,"packages":{"example.com/mod":1}}`

	out, err := runTimingBudgetCompare(t, measured, budget)
	if err == nil {
		t.Fatalf("a wall-time budget over 1.5x must fail under --strict:\n%s", out)
	}
	if !strings.Contains(out, "FAIL  example.com/mod") {
		t.Fatalf("no FAIL for a wall-time budget over 1.5x:\n%s", out)
	}
}

// TestTimingParseHelperEmitsTheRatchetsRows covers the glue the other tests do
// not: test-timing-budget.sh reaches the parser through `go run
// ./cmd/evener-dev/bin dev timing-parse`. A renamed subcommand, a changed flag,
// or a wrong binary path would otherwise only surface when the gate ran, so
// this drives the same command over a fixture stream — a data seam, not a faked
// toolchain — and asserts the rows the ratchet reads.
func TestTimingParseHelperEmitsTheRatchetsRows(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "stream.jsonl")
	writeAuditScriptFixture(t, stream, `{"Action":"pass","Package":"example.com/mod","Test":"TestA","Elapsed":0.3}`+"\n"+
		`{"Action":"pass","Package":"example.com/mod","Elapsed":0.45}`+"\n")
	pkgs := filepath.Join(dir, "packages")
	writeAuditScriptFixture(t, pkgs, "example.com/mod\n")
	cmd := exec.Command("go", "run", "./cmd/evener-dev/bin", "dev", "timing-parse",
		"--json", stream, "--packages", pkgs)
	// Isolate the toolchain, as the other Go-invoking tests do and as the
	// script itself does (`GOFLAGS= go run`): a developer's persisted
	// `go env -w GOFLAGS=...` would otherwise fail this nested `go run`.
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOENV=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("timing-parse helper failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"TEST\texample.com/mod\tTestA\t0.3",
		"SUM\texample.com/mod\t0.45",
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("helper output missing %q:\n%s", want, out)
		}
	}
}

// runBlessCommand writes budgetJSON to a fresh budget file and runs the real
// runner's bless path over it with extra flags, returning the combined output,
// the file's content after the run, and the run's error. The caller names a
// module list that matches nothing, so no producer runs: the bless only rewrites
// and preserves entries, which is exactly the decision under test, with no test
// suite to wait on and nothing faked. Returning the error (rather than failing)
// lets a caller assert a refused bless, which is why runBless wraps it.
func runBlessCommand(t *testing.T, budgetJSON string, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	budget := filepath.Join(dir, "budget.json")
	writeAuditScriptFixture(t, budget, budgetJSON)
	tmpDir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", tmpDir, err)
	}
	cmd := exec.Command("bash", append([]string{
		timingBudgetScript, "--budget", budget, "--no-web", "--bless",
	}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "TMPDIR=" + tmpDir}
	out, runErr := cmd.CombinedOutput()
	got, readErr := os.ReadFile(budget)
	if readErr != nil {
		t.Fatalf("read blessed budget: %v", readErr)
	}
	return string(out), string(got), runErr
}

// runBless runs the real runner's bless path and returns the budget it wrote,
// failing the test if the bless did not succeed.
func runBless(t *testing.T, budgetJSON string, args ...string) string {
	t.Helper()
	out, got, err := runBlessCommand(t, budgetJSON, args...)
	if err != nil {
		t.Fatalf("bless failed: %v\n%s", err, out)
	}
	return got
}

// TestTimingBudgetBlessKeepsAWallTimeMarker pins the marker rule for a narrowed
// bless. A file already blessed under the wall-time metric keeps its marker when
// a run refreshes only part of it: the preserved entries are still wall time, so
// enforcement stays safe, and dropping the marker would silently disable the
// ratchet (issue #172 review).
func TestTimingBudgetBlessKeepsAWallTimeMarker(t *testing.T) {
	got := runBless(t,
		`{"metric":"package-wall-seconds","perTestCeilingSeconds":3,"packages":{"example.com/other":1}}`,
		"--modules", "no-such-module")
	if !strings.Contains(got, `"metric": "package-wall-seconds"`) {
		t.Fatalf("a narrowed bless of an already wall-time file dropped the marker:\n%s", got)
	}
	if !strings.Contains(got, `"example.com/other": 1`) {
		t.Fatalf("the preserved, unmeasured entry was not kept:\n%s", got)
	}
}

// TestTimingBudgetBlessDoesNotStampAStaleFile is the other half: a narrowed
// bless of a file still in the old sum units must NOT add the wall-time marker,
// or it would enforce ratios against the old-unit entries it preserved.
func TestTimingBudgetBlessDoesNotStampAStaleFile(t *testing.T) {
	got := runBless(t,
		`{"perTestCeilingSeconds":3,"packages":{"example.com/other":1}}`,
		"--modules", "no-such-module")
	if strings.Contains(got, `"metric"`) {
		t.Fatalf("a narrowed bless of a stale file stamped the wall-time marker:\n%s", got)
	}
}

// TestTimingBudgetWarnsOnTheWebRowWithoutTheMarker pins the 2026-10-13 ruling
// on PR #4600: the checked-in `"web"` baseline was blessed on a quiet
// workstation, and a four-core CI runner's vitest parallelism measures the same
// suite 2.6x to 3.9x higher (356.45s over a 90.77s budget on the run that
// produced this test), so the row is not comparable to this run any more than
// the Go packages' pre-#172 numbers are. Both therefore warn, with the numbers
// printed, until #141 re-blesses the baseline for the host class CI uses. The
// exit status is the assertion.
func TestTimingBudgetWarnsOnTheWebRowWithoutTheMarker(t *testing.T) {
	measured := "SUM\tweb\t10.00\n"
	budget := `{"perTestCeilingSeconds":3,"packages":{"web":1}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err != nil {
		t.Fatalf("an incomparable-baseline ratio must warn, not fail:\n%s", out)
	}
	for _, want := range []string{"WARN  web", "#141"} {
		if !strings.Contains(out, want) {
			t.Fatalf("web-row warning missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("the stale web ratio was enforced as a failure:\n%s", out)
	}
}

// TestTimingBudgetWarnsOnThePreExistingCeilingBreach pins the posture for a
// test in NO class (ruling, 2026-10-13, #4601). The flat per-test ceiling has
// never run in CI and the unclassified backlog was always over it, so even under
// the CI-strict policy a breach is reported and named but does not fail the
// gate; only a test named in a class is enforced (see
// TestTimingBudgetFailsABlessedClassBreach). The package-ratio check and the
// missing-measurement refusal keep their strictness (see
// TestTimingBudgetFailsOnWallTimeBudgets and
// TestTimingBudgetFailsWhenTheMeasuredFileIsMissing). The exit status is the
// assertion.
func TestTimingBudgetWarnsOnThePreExistingCeilingBreach(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestSlow\t9.00\n"
	budget := `{"perTestCeilingSeconds":3,"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err != nil {
		t.Fatalf("a per-test ceiling breach must warn, not fail, until #4601 clears:\n%s", out)
	}
	if !strings.Contains(out, "per-test ceiling") {
		t.Fatalf("the ceiling breach was not reported at all:\n%s", out)
	}
	if !strings.Contains(out, "#4601") {
		t.Fatalf("the ceiling warning does not name the #4601 backlog that holds it:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("a warn-only ceiling breach was enforced as a failure:\n%s", out)
	}
}

// TestTimingBudgetFailsWhenTheMeasuredFileIsMissing pins the gate's fail-loud
// contract for issue #144. Once the ratchet reads durations from the gate's own
// run, a run that produced no duration file must stop the gate, not compare an
// empty measurement and pass: a missing file silently treated as "nothing over
// budget" would turn the whole ratchet into a no-op the moment it is wired in.
// The exit status is the assertion — what fails here is that the check stops.
func TestTimingBudgetFailsWhenTheMeasuredFileIsMissing(t *testing.T) {
	dir := t.TempDir()
	budget := filepath.Join(dir, "budget.json")
	writeAuditScriptFixture(t, budget,
		`{"metric":"package-wall-seconds","perTestCeilingSeconds":3,"packages":{"example.com/mod":1}}`)
	tmpDir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", tmpDir, err)
	}
	missing := filepath.Join(dir, "no-such-measured.tsv")
	cmd := exec.Command("bash", timingBudgetScript,
		"--measured", missing, "--budget", budget, "--no-web", "--check", "--strict")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "TMPDIR=" + tmpDir}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a missing measured file must fail --check, not pass silently:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("exit = %v, want exit status 1:\n%s", err, out)
	}
}

// TestTimingBudgetRecordsAPackageWithNoDuration pins that a package whose
// terminal event carries no Elapsed (a PKG row with no SUM — a package with no
// test files) is still recorded, at 0. The full-rebaseline drop keys on that
// recorded set, so without this a full rebaseline would delete a still-present
// package's budget entry even though it only claims to drop packages go list no
// longer reports.
func TestTimingBudgetRecordsAPackageWithNoDuration(t *testing.T) {
	measured := "PKG\texample.com/empty\nSUM\texample.com/other\t0.10\n"
	budget := `{"perTestCeilingSeconds":3,"packages":{"example.com/empty":0,"example.com/other":1}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err != nil {
		t.Fatalf("a complete fixture must not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "example.com/empty: 0.00s") {
		t.Fatalf("a no-duration package was not recorded, so a full rebaseline would drop it:\n%s", out)
	}
}

// TestTimingBudgetFailsABlessedClassBreach pins the #4601 enforcement ruling:
// once a test is named in a class, a breach of that class's ceiling is a FAIL —
// nonzero under the strict (CI) policy — unlike an unclassified test, which
// only warns. The exit status and the named class are the assertions.
func TestTimingBudgetFailsABlessedClassBreach(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t31.00\n"
	budget := `{"perTestCeilingSeconds":3,` +
		`"perTestCeilingClasses":{"deploy-host integration":{"ceilingSeconds":30,"tests":["TestClassed"]}},` +
		`"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err == nil {
		t.Fatalf("a blessed-class ceiling breach must fail under --strict:\n%s", out)
	}
	for _, want := range []string{"FAIL", "TestClassed", "deploy-host integration"} {
		if !strings.Contains(out, want) {
			t.Fatalf("class-breach output missing %q:\n%s", want, out)
		}
	}
}

// TestTimingBudgetPassesAClassifiedTestUnderItsClassCeiling is the other half of
// the class map: the same 9s test the flat 3s ceiling would flag is OK — no
// FAIL — once it is named in a class whose ceiling is above it. Without this, a
// map that merely silenced tests would look the same as one that re-ceilings
// them.
func TestTimingBudgetPassesAClassifiedTestUnderItsClassCeiling(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t9.00\n"
	budget := `{"perTestCeilingSeconds":3,` +
		`"perTestCeilingClasses":{"retirement":{"ceilingSeconds":20,"tests":["TestClassed"]}},` +
		`"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err != nil {
		t.Fatalf("a classified test under its class ceiling must pass:\n%s", out)
	}
	// 9.00s is over the flat 3s ceiling, so an UNCLASSIFIED test of this name
	// would warn; no warning or failure line proves the class ceiling was
	// applied and cleared it. Asserting the wording of the class line instead
	// would pin copy (docs/developing-evener/testing.md's "Copy Is Not a Test
	// Oracle").
	if strings.Contains(out, "WARN") || strings.Contains(out, "FAIL") {
		t.Fatalf("a classified test under its class ceiling was not cleared:\n%s", out)
	}
}

// TestTimingBudgetKeepsUnclassifiedBreachesWarnOnly pins the other posture: a
// test in no class keeps the flat ceiling and warns — never fails, even under
// --strict — so the unclassified backlog cannot red the gate (#4601).
func TestTimingBudgetKeepsUnclassifiedBreachesWarnOnly(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestUnclassed\t9.00\n"
	budget := `{"perTestCeilingSeconds":3,` +
		`"perTestCeilingClasses":{"retirement":{"ceilingSeconds":20,"tests":["TestOther"]}},` +
		`"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err != nil {
		t.Fatalf("an unclassified breach must warn, not fail, under --strict:\n%s", out)
	}
	for _, want := range []string{"WARN", "TestUnclassed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("unclassified-breach output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("an unclassified breach was enforced as a failure:\n%s", out)
	}
}

// TestTimingBudgetComparesASubtestUnderItsParentsClass pins membership for a
// subtest. The mechanism checks every subtest (a slow subtest a parent-only view
// would hide), so a subtest of a classified parent must be compared strictly
// against the parent's class ceiling, not left on the flat warn-only default.
func TestTimingBudgetComparesASubtestUnderItsParentsClass(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed/shadow\t31.00\n"
	budget := `{"perTestCeilingSeconds":3,` +
		`"perTestCeilingClasses":{"deploy-host integration":{"ceilingSeconds":30,"tests":["TestClassed"]}},` +
		`"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err == nil || !strings.Contains(out, "FAIL") {
		t.Fatalf("a subtest over its parent's class ceiling must fail:\n%s", out)
	}
}

// TestTimingBudgetFailsWhenATestIsListedInTwoClasses pins the exactly-one-class
// rule. The map is hand-edited, and silently picking the first class would
// enforce the wrong number, so a name matched by two classes is a config error
// the check FAILs on. The fixture is otherwise valid — the package is well
// under its budget and 1.00s is under both classes' ceilings — so under the
// strict policy the exit status is the assertion: only the duplicate can make
// it nonzero.
func TestTimingBudgetFailsWhenATestIsListedInTwoClasses(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t1.00\n"
	budget := `{"perTestCeilingSeconds":3,` +
		`"perTestCeilingClasses":{"a":{"ceilingSeconds":10,"tests":["TestClassed"]},` +
		`"b":{"ceilingSeconds":20,"tests":["TestClassed"]}},` +
		`"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err == nil {
		t.Fatalf("a test listed in two classes must make an otherwise-valid fixture fail under --strict:\n%s", out)
	}
}

// TestTimingBudgetFailsAMalformedClassSpec pins the map's own shape. Each case
// is a hand-edit mistake that would otherwise crash the comparison or silently
// drop the class's enforcement — a string `tests` iterates as characters and
// matches nothing, so its members would quietly fall back to the warn-only flat
// ceiling. Every one must FAIL naming the class.
func TestTimingBudgetFailsAMalformedClassSpec(t *testing.T) {
	cases := map[string]string{
		"no ceiling":        `{"tests":["TestClassed"]}`,
		"bool ceiling":      `{"ceilingSeconds":true,"tests":["TestClassed"]}`,
		"no tests":          `{"ceilingSeconds":10}`,
		"empty tests":       `{"ceilingSeconds":10,"tests":[]}`,
		"string tests":      `{"ceilingSeconds":10,"tests":"TestClassed"}`,
		"nonfinite ceiling": `{"ceilingSeconds":1e309,"tests":["TestClassed"]}`,
		"non-string test":   `{"ceilingSeconds":10,"tests":[7]}`,
		"not an object":     `"deploy-host integration"`,
		// Issue #4629: an integer too large to convert to a float (see
		// hugeCeilingSeconds) — it used to crash before any class check ran.
		"huge integer ceiling": `{"ceilingSeconds":` + hugeCeilingSeconds + `,"tests":["TestClassed"]}`,
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t1.00\n"
			budget := `{"perTestCeilingSeconds":3,"perTestCeilingClasses":{"broken":` + spec + `},"packages":{"pkg":100}}`
			out, err := runTimingBudgetCompare(t, measured, budget)
			if err == nil || !strings.Contains(out, "broken") {
				t.Fatalf("a malformed class spec (%s) must FAIL naming the class:\n%s", name, out)
			}
		})
	}
}

// TestTimingBudgetFailsOnAnUnusableClassCeilingInEveryMode pins issue #4629's
// every-mode contract for a class ceilingSeconds that cannot serve as seconds:
// the huge integer that used to traceback, and the other unusable values
// (Infinity, NaN, a bool, a string, null) that used to exit 0 outside
// --strict/CI. Each must fail like the fields above (#4609/#4614/#4618/#4622),
// exit 1 in bare, warn-only --check --local, a CI-shaped --check, and --bless,
// with no traceback and the file untouched on --bless. The class name is a data
// value, not copy (docs/developing-evener/testing.md's "Copy Is Not a Test
// Oracle").
func TestTimingBudgetFailsOnAnUnusableClassCeilingInEveryMode(t *testing.T) {
	const measured = "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t1.00\n"
	for name, value := range map[string]string{
		"huge-int": hugeCeilingSeconds,
		"infinity": "1e309",
		"nan":      "NaN",
		"bool":     "true",
		"string":   `"x"`,
		"null":     "null",
	} {
		budget := `{"perTestCeilingSeconds":3,"perTestCeilingClasses":{"retirement":{"ceilingSeconds":` +
			value + `,"tests":["TestClassed"]}},"packages":{"pkg":100}}`
		assertBudgetFailsInEveryMode(t, name, measured, budget)
	}
}

// TestTimingBudgetKeepsAHugeFiniteClassCeiling pins the decision issue #4629
// leaves open: a very large but FINITE ceilingSeconds (1e300) is a valid ceiling,
// not a malformed one. A ceiling is an upper bound, so a huge finite value simply
// disables that class's enforcement — legitimate, and it must not be rejected.
// The measured test is 1000s, far over the flat 3s ceiling, so only applying the
// class's huge finite ceiling can clear it. The exit status and the absence of a
// FAIL/WARN line are the assertions, never the diagnostic wording.
func TestTimingBudgetKeepsAHugeFiniteClassCeiling(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t1000.00\n"
	budget := `{"metric":"package-wall-seconds","perTestCeilingSeconds":3,` +
		`"perTestCeilingClasses":{"retirement":{"ceilingSeconds":1e300,"tests":["TestClassed"]}},` +
		`"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err != nil {
		t.Fatalf("a huge but finite class ceiling is a valid class-disabling ceiling, not a broken config:\n%s", out)
	}
	if strings.Contains(out, "FAIL") || strings.Contains(out, "WARN") {
		t.Fatalf("a classified test under its huge finite class ceiling was not cleared:\n%s", out)
	}
}

// TestTimingBudgetNamesTheClassAndFieldForAnUnusableClassCeiling pins that the
// failure names the offending class and the field, not merely that it exits 1
// (#4629): a hand-editor reads the diagnostic to find the bad entry, and both the
// field name and the class key are data values, not copy
// (docs/developing-evener/testing.md's "Copy Is Not a Test Oracle").
func TestTimingBudgetNamesTheClassAndFieldForAnUnusableClassCeiling(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t1.00\n"
	budget := `{"perTestCeilingSeconds":3,"perTestCeilingClasses":{"retirement":{"ceilingSeconds":` +
		hugeCeilingSeconds + `,"tests":["TestClassed"]}},"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	assertBrokenConfigFails(t, out, err)
	for _, want := range []string{"retirement", "ceilingSeconds"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the failure does not name %q:\n%s", want, out)
		}
	}
}

// TestTimingBudgetFailsABlessedClassBreachUnderTheCIEnvOnly pins the CI-shaped
// enforcement path the other tests do not: strictness comes from $CI being set,
// with no --strict/--local flag at all (the way the gate's own lanes call it).
func TestTimingBudgetFailsABlessedClassBreachUnderTheCIEnvOnly(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t31.00\n"
	budget := `{"perTestCeilingSeconds":3,` +
		`"perTestCeilingClasses":{"deploy-host integration":{"ceilingSeconds":30,"tests":["TestClassed"]}},` +
		`"packages":{"pkg":100}}`
	out, err := runTimingBudgetScript(t, measured, budget, []string{"--check"}, []string{"CI=1"})
	if err == nil {
		t.Fatalf("$CI set with no --strict must enforce a blessed-class breach:\n%s", out)
	}
}

// TestTimingBudgetBlessPreservesTheClassMap pins that a rebaseline does not drop
// the class map: --bless rewrites package numbers, not the segmentation, and a
// class map silently lost on --bless would turn every classified test back into
// a warn-only one.
func TestTimingBudgetBlessPreservesTheClassMap(t *testing.T) {
	got := runBless(t,
		`{"perTestCeilingSeconds":3,`+
			`"perTestCeilingClasses":{"retirement":{"ceilingSeconds":20,"tests":["TestClassed"]}},`+
			`"packages":{"example.com/other":1}}`,
		"--modules", "no-such-module")
	if !strings.Contains(got, `"perTestCeilingClasses"`) || !strings.Contains(got, "retirement") {
		t.Fatalf("a bless dropped the class map:\n%s", got)
	}
}

// TestTimingBudgetWarnsOnAClassBreachOnALocalRun pins the non-CI posture: with
// no $CI and no --strict (--local), a blessed-class breach is a FAIL severity
// but the warn-only run still exits 0, exactly like the package ratios; the
// class and its number are named so the local report says what to fix.
func TestTimingBudgetWarnsOnAClassBreachOnALocalRun(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t31.00\n"
	budget := `{"perTestCeilingSeconds":3,` +
		`"perTestCeilingClasses":{"deploy-host integration":{"ceilingSeconds":30,"tests":["TestClassed"]}},` +
		`"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompareMode(t, measured, budget, false)
	if err != nil {
		t.Fatalf("a local warn-only run must not fail on a class breach:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "deploy-host integration") {
		t.Fatalf("the local report should name the class breach:\n%s", out)
	}
}

// TestTimingBudgetFailsWhenTheClassMapIsNotAnObject pins the outer shape of the
// map: a perTestCeilingClasses that is present but not an object is a config
// error, not a crash and not a silent empty map — including the falsy values
// (an empty list, false, 0, "") that a plain `or {}` would quietly accept, and
// `null`, which `budget.get` cannot tell from an absent key and which would
// otherwise silently disarm every class ceiling (issue #4618).
func TestTimingBudgetFailsWhenTheClassMapIsNotAnObject(t *testing.T) {
	for _, value := range []string{`["nope"]`, `[]`, `false`, `0`, `""`, `"a string"`, `null`} {
		t.Run(value, func(t *testing.T) {
			measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t1.00\n"
			budget := `{"perTestCeilingSeconds":3,"perTestCeilingClasses":` + value + `,"packages":{"pkg":100}}`
			out, err := runTimingBudgetCompare(t, measured, budget)
			if err == nil {
				t.Fatalf("a present non-object class map (%s) must fail under --strict, not read as no classes:\n%s", value, out)
			}
		})
	}
}

// TestTimingBudgetFailsWhenThePackagesValueIsNotAnObject pins issue #4614. The
// budget's packages value feeds every ratio and, through it, the class
// ceilings. A value that is present but not an object is a hand-edit mistake
// (`[]` instead of `{}`, a stray `null`) that a plain `packages or {}` swallows
// into the NO BASELINE warn state — --check exits 0 with the whole ratchet
// silently disarmed — while the truthy values crash with a traceback. It must
// fail loudly in every mode instead. The fixture measurement is valid, so only
// the packages value can make the run nonzero: the exit status and the absence
// of a traceback are the assertions, never the diagnostic wording
// (docs/developing-evener/testing.md's "Copy Is Not a Test Oracle").
func TestTimingBudgetFailsWhenThePackagesValueIsNotAnObject(t *testing.T) {
	for _, value := range []string{`[]`, `0`, `false`, `""`, `null`, `"x"`, `5`, `true`} {
		t.Run(value, func(t *testing.T) {
			out, err := runTimingBudgetCompare(t, "SUM\tpkg\t100.00\n", `{"packages":`+value+`}`)
			assertBrokenConfigFails(t, out, err)
		})
	}
}

// TestTimingBudgetFailsOnANonObjectPackagesValueInEveryMode pins that a
// non-object packages value fails in every posture, not only strict: a broken
// config is not a ratio warning, so bare (no --check), warn-only --check
// --local, a CI-shaped --check ($CI set, no --strict), and --bless must each
// exit 1. It runs several values rather than `[]` alone: a guard that fired for
// `[]` only, or only under --strict, would pass a single-value test while the
// silent disarm (`false`, `null`) or traceback (`5`) reappeared in the other
// postures. `[]` is the issue's example; `false`/`null` disarm silently (exit 0
// before the fix), and `5` exits 1 there only via a traceback, so the no
// traceback assertion is what makes the truthy case non-vacuous. On --bless the
// file is left byte-for-byte untouched, because failing before the bless write
// is the observable behavior that matters.
func TestTimingBudgetFailsOnANonObjectPackagesValueInEveryMode(t *testing.T) {
	const measured = "SUM\tpkg\t100.00\n"
	for _, value := range []string{`[]`, `false`, `null`, `5`} {
		assertBudgetFailsInEveryMode(t, value, measured, `{"packages":`+value+`}`)
	}
}

// assertBrokenConfigFails asserts the #4609/#4614/#4618 contract structurally:
// a budget value that is present but broken exits 1 and never crashes with a
// traceback. The diagnostic wording is never asserted
// (docs/developing-evener/testing.md's "Copy Is Not a Test Oracle").
func assertBrokenConfigFails(t *testing.T, out string, err error) {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("a broken budget value must exit 1, not pass or crash: exit = %v\n%s", err, out)
	}
	if strings.Contains(out, "Traceback") {
		t.Fatalf("a broken budget value crashed with a traceback instead of failing structurally:\n%s", out)
	}
}

// assertBudgetFailsInEveryMode drives the runner over one budget fixture in all
// four postures a check may run under — bare, warn-only --check --local, a
// CI-shaped --check ($CI set, no --strict), and --bless — and asserts each exits
// 1 with no traceback. On --bless it also asserts the budget file is left
// byte-for-byte untouched: failing before the bless write is the observable
// behavior that matters. Subtests are named name/<posture>.
func assertBudgetFailsInEveryMode(t *testing.T, name, measured, budget string) {
	t.Helper()
	t.Run(name+"/bare", func(t *testing.T) {
		out, err := runTimingBudgetScript(t, measured, budget, nil, nil)
		assertBrokenConfigFails(t, out, err)
	})
	t.Run(name+"/warn-only", func(t *testing.T) {
		out, err := runTimingBudgetCompareMode(t, measured, budget, false)
		assertBrokenConfigFails(t, out, err)
	})
	t.Run(name+"/ci-env", func(t *testing.T) {
		out, err := runTimingBudgetScript(t, measured, budget, []string{"--check"}, []string{"CI=1"})
		assertBrokenConfigFails(t, out, err)
	})
	t.Run(name+"/bless", func(t *testing.T) {
		out, got, err := runBlessCommand(t, budget, "--modules", "no-such-module")
		assertBrokenConfigFails(t, out, err)
		if got != budget {
			t.Fatalf("--bless overwrote a broken budget instead of refusing it:\n%s", got)
		}
	})
}

// TestTimingBudgetFailsOnANonNumericPackageValue pins issue #4618. A package
// budget that is present but not a number is a broken config, not a budget: a
// string crashes the ratio with a TypeError traceback, a bool IS an int in
// Python (True/False) so the ratio is computed against 1/0, and a null reads as
// "no budget entry" — a WARN that leaves the package silently unenforced. A
// list or object crashes like the string. Each must exit 1 with no traceback,
// naming the offending entry. The fixture uses a 1.00s measurement against a
// 1.00s-equivalent budget, so a bool would read OK and a null would merely warn
// — only a real rejection makes the run nonzero. The entry name is a data value
// identifying which entry failed, not copy
// (docs/developing-evener/testing.md's "Copy Is Not a Test Oracle").
func TestTimingBudgetFailsOnANonNumericPackageValue(t *testing.T) {
	measured := "SUM\tslowpkg\t1.00\n"
	for _, value := range []string{`"x"`, `true`, `null`, `[]`, `{}`} {
		t.Run(value, func(t *testing.T) {
			budget := `{"metric":"package-wall-seconds","packages":{"slowpkg":` + value + `}}`
			out, err := runTimingBudgetCompare(t, measured, budget)
			assertBrokenConfigFails(t, out, err)
			if !strings.Contains(out, "slowpkg") {
				t.Fatalf("the failure does not name the offending package entry slowpkg:\n%s", out)
			}
		})
	}
}

// TestTimingBudgetFailsOnANonNumericPackageValueInEveryMode pins that a
// non-numeric package budget fails in every posture, not only strict: the
// silent WARN (`null`) or the Python bool (`true`) must not survive a bare run,
// a warn-only --check --local, a CI-shaped --check, or --bless. It runs two
// values rather than one so a guard that only caught a string crash, or only
// fired under --strict, cannot pass. On --bless the file is left byte-for-byte
// untouched, because failing before the bless write is the observable behavior
// that matters.
func TestTimingBudgetFailsOnANonNumericPackageValueInEveryMode(t *testing.T) {
	// `true` is the sharp case: against a 1.00s budget it makes a 1.00s
	// measurement read OK, so only rejecting the bool can make the run nonzero.
	const measured = "SUM\tpkg\t1.00\n"
	for _, value := range []string{`true`, `null`} {
		assertBudgetFailsInEveryMode(t, value, measured, `{"packages":{"pkg":`+value+`}}`)
	}
}

// TestTimingBudgetFailsOnANonObjectClassMapInEveryMode pins the every-mode
// contract for the class map: a perTestCeilingClasses that is present but not an
// object — `null` in particular, which `budget.get` cannot tell from an absent
// key and which silently disarms every class ceiling — is the same broken
// config as a non-object packages value, so it fails in every posture, --bless
// included, not only under --strict.
func TestTimingBudgetFailsOnANonObjectClassMapInEveryMode(t *testing.T) {
	const measured = "SUM\tpkg\t0.10\nTEST\tpkg\tTestClassed\t1.00\n"
	for _, value := range []string{`null`, `[]`} {
		assertBudgetFailsInEveryMode(t, value, measured,
			`{"perTestCeilingClasses":`+value+`,"packages":{"pkg":100}}`)
	}
}

// TestTimingBudgetKeepsThePackagesFallbacks pins the values the #4614 guard
// must NOT reject: an absent packages key and a present empty map keep the
// deliberate NO BASELINE warn state (exit 0), and a real map is compared as
// before. A present empty map `{"packages":{}}` is distinct from an absent key
// — a regression that rejected the empty map while accepting an absent one
// would otherwise pass — and these are the "present but not an object" rule's
// boundary: the fix closes the malformed values without disarming a budget
// that legitimately has no packages yet.
func TestTimingBudgetKeepsThePackagesFallbacks(t *testing.T) {
	cases := map[string]string{
		"absent key":        `{"perTestCeilingSeconds":3}`,
		"present empty map": `{"packages":{}}`,
		"real map":          `{"packages":{"pkg":100}}`,
	}
	for name, budget := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := runTimingBudgetCompare(t, "SUM\tpkg\t100.00\n", budget)
			if err != nil {
				t.Fatalf("a %s budget must keep exiting 0, not fail the run: %v\n%s", name, err, out)
			}
		})
	}
}

// TestTimingBudgetFailsOnAMalformedBudget pins issue #4609.
// json.JSONDecodeError is a ValueError, and the comparer's
// `except (FileNotFoundError, ValueError)` used to treat a syntax error exactly
// like a missing file: an empty budget and --check exiting 0, with every package
// ratio and every perTestCeilingClasses ceiling silently unenforced. A malformed
// file is a broken config and must fail loudly in every mode. The fixture
// measurement is valid, so only the malformed budget can make the run nonzero:
// the exit status is the assertion, never the diagnostic wording
// (docs/developing-evener/testing.md's "Copy Is Not a Test Oracle"). Red before
// the fix: exit 0.
func TestTimingBudgetFailsOnAMalformedBudget(t *testing.T) {
	dir := t.TempDir()
	malformed := `{ "packages": { "pkg": 1 }` // missing both closing braces
	measuredPath := filepath.Join(dir, "measured.tsv")
	writeAuditScriptFixture(t, measuredPath, "SUM\tpkg\t100.00\n")
	budgetPath := filepath.Join(dir, "budget.json")
	writeAuditScriptFixture(t, budgetPath, malformed)
	out, err := runTimingBudgetFixture(t, measuredPath, budgetPath, dir, []string{"--check", "--strict"}, nil)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("a malformed budget must fail --check --strict with exit 1: exit = %v\n%s", err, out)
	}
}

// TestTimingBudgetFailsOnAMalformedBudgetWhenWarnOnly pins the "non-zero under
// --check" half of #4609. --check with neither $CI nor --strict is the
// warn-only posture for ratios — a real ratio excess only warns and exits 0 —
// but a broken config is not a ratio warning: a malformed budget must fail
// regardless of the strictness posture, so the exit status is the assertion.
func TestTimingBudgetFailsOnAMalformedBudgetWhenWarnOnly(t *testing.T) {
	out, err := runTimingBudgetScript(t, "SUM\tpkg\t100.00\n", `{"packages":`, []string{"--check"}, nil)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("a malformed budget must fail --check even on a warn-only run: exit = %v\n%s", err, out)
	}
}

// TestTimingBudgetBlessRefusesAMalformedBudget pins the same fail-loud rule on
// the bless path. A narrowed --bless reads the existing file to preserve the
// entries it did not measure; reading a malformed file as an empty budget would
// make the bless rewrite it from that empty fallback and drop every preserved
// entry. The refusal is asserted by the exit status and by the file being left
// byte-for-byte untouched — the observable behavior, not the wording.
func TestTimingBudgetBlessRefusesAMalformedBudget(t *testing.T) {
	const malformed = `{ "packages": `
	out, got, err := runBlessCommand(t, malformed, "--modules", "no-such-module")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("--bless must refuse a malformed budget: exit = %v\n%s", err, out)
	}
	if got != malformed {
		t.Fatalf("--bless overwrote a malformed budget instead of refusing it:\n%s", got)
	}
}

// TestTimingBudgetKeepsTheMissingFileWarnState pins the deliberate warn state
// #4609 separates from a syntax error: a budget file that does not exist is not
// a broken config — the tree stays green until a measured baseline is blessed —
// so --check --strict must still exit 0, unlike a malformed file. The exit
// status is the assertion.
func TestTimingBudgetKeepsTheMissingFileWarnState(t *testing.T) {
	dir := t.TempDir()
	measuredPath := filepath.Join(dir, "measured.tsv")
	writeAuditScriptFixture(t, measuredPath, "SUM\tpkg\t100.00\n")
	budgetPath := filepath.Join(dir, "absent-budget.json") // never written
	out, err := runTimingBudgetFixture(t, measuredPath, budgetPath, dir, []string{"--check", "--strict"}, nil)
	if err != nil {
		t.Fatalf("a missing budget file must keep exiting 0 (the warn state), not fail the run: %v\n%s", err, out)
	}
}

// TestTimingBudgetKeepsTheEmptyObjectWarnState pins a valid-but-partial case:
// an empty JSON object is valid, has no packages, and must keep exiting 0 like a
// missing file — it is not a malformed-budget failure. The exit status is the
// assertion.
func TestTimingBudgetKeepsTheEmptyObjectWarnState(t *testing.T) {
	out, err := runTimingBudgetCompare(t, "SUM\tpkg\t100.00\n", "{}")
	if err != nil {
		t.Fatalf("an empty budget object must keep exiting 0, not fail the run: %v\n%s", err, out)
	}
}

// TestTimingBudgetKeepsTheClassMapOnlyWarnState pins the other valid-but-partial
// case: a file with a perTestCeilingClasses map but no packages is still no
// baseline — the map is validated, but with no packages the run keeps exiting 0,
// exactly as before #4609. The exit status is the assertion.
func TestTimingBudgetKeepsTheClassMapOnlyWarnState(t *testing.T) {
	budget := `{"perTestCeilingSeconds":3,` +
		`"perTestCeilingClasses":{"retirement":{"ceilingSeconds":20,"tests":["TestClassed"]}}}`
	out, err := runTimingBudgetCompare(t, "SUM\tpkg\t100.00\n", budget)
	if err != nil {
		t.Fatalf("a class-map-only budget must keep exiting 0, not fail the run: %v\n%s", err, out)
	}
}

// TestTimingBudgetRunnerBoundMemberWarnsWhileKeptClassMemberFails pins the
// rulings that moved tests onto the flat warn-only ceiling, against the
// checked-in testing-budget.json, not a synthetic map: a member of a removed
// runner-bound class (2026-10-14, issue #4615) and a member moved off a kept
// class (2026-10-16, issue #4699) both warn instead of failing, while a member
// of a kept class still fails over its class ceiling. It reads the real budget
// file (the evener_test package runs with the repo root as its working
// directory), so each half also reds if a class membership creeps back in.
func TestTimingBudgetRunnerBoundMemberWarnsWhileKeptClassMemberFails(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name     string
		pkg      string
		test     string
		seconds  string
		wantFail bool
	}{
		{
			name:    "removed runner-bound class member warns",
			pkg:     "primeradiant.com/evener/cmd/evener-dev",
			test:    "TestCrossCompileWindows",
			seconds: "52.26",
		},
		{
			name:     "kept class member still fails",
			pkg:      "primeradiant.com/evener/internal/plugins",
			test:     "TestGitCloneSurvivesAHookThatOrphansAPipeHolder",
			seconds:  "20.00",
			wantFail: true,
		},
		{
			// Moved off the session-activity class on 2026-10-16 (#4699): it runs
			// t.Parallel() in the agent package, so the package's parallel load sets
			// its wall time (7.6-9.6s measured alone, 30.09-32.28s in CI).
			name:    "member moved off a kept class warns",
			pkg:     "primeradiant.com/evener/agent",
			test:    "TestLoadSessionJobActivityTree_TuesdayFullyFoldsAndFullyPaginates",
			seconds: "32.28",
		},
		{
			// A parent that builds one live stack per subtest: its wall time is the
			// sum of three runner-bound stack starts (6.8s typical, 15.3-16.9s on a
			// loaded runner), so a class ceiling measures the runner.
			name:    "multi-stack E2E parent warns",
			pkg:     "primeradiant.com/evener/cmd/evener-hub",
			test:    "TestE2E_RetireOverlappedByAnotherOwnershipAction",
			seconds: "16.86",
		},
		{
			// The same class's remaining members stay strict: moving one member
			// off must not quiet the class.
			name:     "kept session-activity member still fails",
			pkg:      "primeradiant.com/evener/agent",
			test:     "TestSessionActivityReportCaptureBudgetPreservesPageContinuation",
			seconds:  "32.28",
			wantFail: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			measured := filepath.Join(dir, tc.test+".tsv")
			writeAuditScriptFixture(t, measured, "TEST\t"+tc.pkg+"\t"+tc.test+"\t"+tc.seconds+"\n")
			out, err := runTimingBudgetFixture(t, measured, "testing-budget.json", dir, []string{"--check", "--strict"}, nil)
			if !tc.wantFail {
				if err != nil {
					t.Fatalf("a member on the flat warn-only ceiling must warn, not fail (#4615, #4699):\n%s", out)
				}
				if !strings.Contains(out, "WARN") || !strings.Contains(out, tc.test) {
					t.Fatalf("the warn-only member was not reported on the flat ceiling:\n%s", out)
				}
				if strings.Contains(out, "FAIL") {
					t.Fatalf("the warn-only member was still enforced as a failure:\n%s", out)
				}
				return
			}
			if err == nil {
				t.Fatalf("a kept class member over its ceiling must still fail:\n%s", out)
			}
			if !strings.Contains(out, "FAIL") {
				t.Fatalf("the kept class member was not enforced over its class ceiling:\n%s", out)
			}
		})
	}
}

// TestTimingBudgetFailsOnANonNumericPerTestCeiling pins issue #4622's
// perTestCeilingSeconds rows. The flat ceiling is read with budget.get, so a
// present null is indistinguishable from an absent key; a string, null, list or
// object crashes the `secs > ceiling` comparison with a TypeError traceback, and
// a bool IS an int in Python so `true` silently becomes a 1-second ceiling
// instead of a config error. Each must exit 1 with no traceback, naming the
// field. The measured fixture carries a TEST row so the pre-fix string/null
// crash path is actually reached — with a SUM row alone the ceiling is never
// compared and the run exits 0. A JSON integer too large to convert to a float
// is the same unusable value: it reads as an enormous ceiling and silently
// disarms the check, so it is rejected too. The field name is a data value, not
// copy (docs/developing-evener/testing.md's "Copy Is Not a Test Oracle").
func TestTimingBudgetFailsOnANonNumericPerTestCeiling(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestX\t9.00\n"
	cases := map[string]string{
		"string":   `"x"`,
		"null":     `null`,
		"bool":     `true`,
		"list":     `[]`,
		"object":   `{}`,
		"infinity": `1e309`,
		"huge int": strings.Repeat("9", 400),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			budget := `{"perTestCeilingSeconds":` + value + `,"packages":{"pkg":100}}`
			out, err := runTimingBudgetCompare(t, measured, budget)
			assertBrokenConfigFails(t, out, err)
			if !strings.Contains(out, "perTestCeilingSeconds") {
				t.Fatalf("the failure does not name the perTestCeilingSeconds field:\n%s", out)
			}
		})
	}
}

// TestTimingBudgetFailsOnANonNumericPerTestCeilingInEveryMode pins the every-mode
// contract for perTestCeilingSeconds: a present non-number fails in every
// posture, not only strict. A bool (`true`, which would silently read as a
// 1-second ceiling) or a null (which budget.get cannot tell from an absent key)
// must not survive a bare run, a warn-only --check --local, a CI-shaped --check,
// or --bless. On --bless the file is left byte-for-byte untouched, because
// failing before the bless write is the observable behavior that matters.
func TestTimingBudgetFailsOnANonNumericPerTestCeilingInEveryMode(t *testing.T) {
	const measured = "SUM\tpkg\t0.10\nTEST\tpkg\tTestX\t9.00\n"
	for _, value := range []string{`true`, `null`} {
		assertBudgetFailsInEveryMode(t, value, measured,
			`{"perTestCeilingSeconds":`+value+`,"packages":{"pkg":100}}`)
	}
}

// TestTimingBudgetFailsOnANonFinitePackageValue pins issue #4622's Infinity row
// (and the huge-integer shape of the same value). A package budget of Infinity —
// JSON's `1e309`, `Infinity`, or `NaN` — is not a usable number of seconds: it
// makes the ratio m/inf read 0, so the package prints OK and the run exits 0, a
// silent disarm rather than a comparison, while a JSON integer too large to
// convert to a float tracebacks the ratio arithmetic. Each must exit 1 with no
// traceback, naming the entry. The entry name is a data value, not copy
// (docs/developing-evener/testing.md's "Copy Is Not a Test Oracle").
func TestTimingBudgetFailsOnANonFinitePackageValue(t *testing.T) {
	const measured = "SUM\tslowpkg\t1.00\n"
	cases := map[string]string{
		"infinity": "1e309",
		"huge int": strings.Repeat("9", 400),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			budget := `{"metric":"package-wall-seconds","packages":{"slowpkg":` + value + `}}`
			out, err := runTimingBudgetCompare(t, measured, budget)
			assertBrokenConfigFails(t, out, err)
			if !strings.Contains(out, "slowpkg") {
				t.Fatalf("the failure does not name the offending package entry slowpkg:\n%s", out)
			}
		})
	}
}

// TestTimingBudgetFailsOnANonFinitePackageValueInEveryMode pins the every-mode
// contract for a non-finite package budget. `1e309` (Infinity) reads as OK and
// exits 0 even under --strict; `NaN` reads as a false FAIL, nonzero under
// --strict but zero on a bare or warn-only run, and a --bless would overwrite
// the file with a rounded number. Each posture must exit 1 with the file
// untouched on --bless.
func TestTimingBudgetFailsOnANonFinitePackageValueInEveryMode(t *testing.T) {
	const measured = "SUM\tpkg\t1.00\n"
	for _, value := range []string{`1e309`, `NaN`} {
		assertBudgetFailsInEveryMode(t, value, measured, `{"packages":{"pkg":`+value+`}}`)
	}
}

// TestTimingBudgetFailsOnANonObjectTopLevelBudget pins issue #4622's top-level
// row: a budget file whose top level is a list, number, string, null or bool is
// a broken config reached before any field can be read — budget.get crashes with
// an AttributeError traceback. #4609 deferred this because the base already
// exited nonzero (via that traceback), but the issue's contract is a failure
// that NAMES the file, not an unnamed crash, so it routes through the same
// structured path as the rest (#4609/#4614/#4618). Each must exit 1 with no
// traceback. The exit status and the absence of a traceback are the assertions,
// never the diagnostic wording (docs/developing-evener/testing.md's "Copy Is Not
// a Test Oracle").
func TestTimingBudgetFailsOnANonObjectTopLevelBudget(t *testing.T) {
	const measured = "SUM\tpkg\t100.00\n"
	for _, value := range []string{`[]`, `5`, `"x"`, `null`, `true`} {
		t.Run(value, func(t *testing.T) {
			out, err := runTimingBudgetCompare(t, measured, value)
			assertBrokenConfigFails(t, out, err)
		})
	}
}

// TestTimingBudgetFailsOnANonObjectTopLevelBudgetInEveryMode pins that a
// non-object top level fails in every posture, --bless included, not only under
// --strict: a bare run and a --bless must refuse it (and leave the file
// untouched) rather than crash.
func TestTimingBudgetFailsOnANonObjectTopLevelBudgetInEveryMode(t *testing.T) {
	const measured = "SUM\tpkg\t100.00\n"
	assertBudgetFailsInEveryMode(t, "list", measured, `[]`)
	assertBudgetFailsInEveryMode(t, "number", measured, `5`)
}

// TestCheckedInTimingBudgetEnforcesPackageRatios guards the ratchet against
// silently disarming: the checked-in budget must carry the wall-time metric
// marker, so a package far over its recorded budget fails under --strict
// instead of downgrading to a stale-units warning.
func TestCheckedInTimingBudgetEnforcesPackageRatios(t *testing.T) {
	dir := t.TempDir()
	measured := filepath.Join(dir, "measured.tsv")
	const pkg = "primeradiant.com/evener/agent"
	writeAuditScriptFixture(t, measured, "SUM\t"+pkg+"\t100000.00\n")
	out, err := runTimingBudgetFixture(t, measured, "testing-budget.json", dir, []string{"--check", "--strict"}, nil)
	if err == nil {
		t.Fatalf("a package far over its checked-in budget must fail the strict check:\n%s", out)
	}
	if !strings.Contains(out, "FAIL  "+pkg) {
		t.Fatalf("the over-budget package was not reported as a FAIL:\n%s", out)
	}
}

// TestCheckedInTimingBudgetAcceptsHealthyWebMeasurement pairs with the test
// above: with the ratios armed, the "web" row must be a CI-class number, so a
// healthy web-unit measurement (the slowest seen was 547.26s) passes.
func TestCheckedInTimingBudgetAcceptsHealthyWebMeasurement(t *testing.T) {
	dir := t.TempDir()
	measured := filepath.Join(dir, "measured.tsv")
	writeAuditScriptFixture(t, measured, "SUM\tweb\t547.26\n")
	out, err := runTimingBudgetFixture(t, measured, "testing-budget.json", dir, []string{"--check", "--strict"}, nil)
	if err != nil {
		t.Fatalf("a healthy CI web measurement must pass the strict check:\n%s", out)
	}
}

// TestTimingTSVLibParsesFromAnyDirectoryWithoutRepoRoot pins the way CI's
// web-unit lane uses the lib: sourced in a bare step, from the checkout root,
// with no repo_root set and nothing but the report path. The lib must find its
// own checkout rather than cd into an empty variable.
func TestTimingTSVLibParsesFromAnyDirectoryWithoutRepoRoot(t *testing.T) {
	lib, err := filepath.Abs("scripts/lib/timing-tsv-lib.sh")
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "vitest.json")
	writeAuditScriptFixture(t, report, `{"testResults":[{"assertionResults":[{"fullName":"a b","duration":1500}]}]}`)
	cmd := exec.Command("bash", "-c", `. "$LIB" && cd "$WORK" && vitest_json_to_tsv "$REPORT"`)
	cmd.Env = append(os.Environ(), "LIB="+lib, "WORK="+t.TempDir(), "REPORT="+report, "repo_root=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vitest_json_to_tsv without repo_root: %v\n%s", err, out)
	}
	if want := "SUM\tweb\t1.5\n"; !strings.Contains(string(out), want) {
		t.Fatalf("rows = %q, want the web SUM row %q", out, want)
	}
}
