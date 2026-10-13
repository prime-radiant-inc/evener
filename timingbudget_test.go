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

// runBless runs the real runner's bless path over budgetJSON with extra
// flags and returns the budget it wrote. The caller names a module list that
// matches nothing, so no producer runs: the bless only rewrites and preserves
// entries, which is exactly the marker decision under test, with no test suite
// to wait on and nothing faked.
func runBless(t *testing.T, budgetJSON string, args ...string) string {
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
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bless failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(budget)
	if err != nil {
		t.Fatalf("read blessed budget: %v", err)
	}
	return string(got)
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
// (an empty list, false, 0, "") that a plain `or {}` would quietly accept.
func TestTimingBudgetFailsWhenTheClassMapIsNotAnObject(t *testing.T) {
	for _, value := range []string{`["nope"]`, `[]`, `false`, `0`, `""`, `"a string"`} {
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
