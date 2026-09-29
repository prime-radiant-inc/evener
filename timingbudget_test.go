package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// timingBudgetScript is the runner under test.
const timingBudgetScript = "scripts/gate/test-timing-budget.sh"

// runTimingBudgetCompare drives the runner's comparison half through the
// --measured fixture seam: the fixture IS the measurement the comparator reads,
// so no go test or vitest run is involved and no toolchain is faked
// (docs/developing-evener/testing.md's ban on PATH stubs).
func runTimingBudgetCompare(t *testing.T, measured, budget string) (string, error) {
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
	cmd := exec.Command("bash", timingBudgetScript,
		"--measured", measuredPath, "--budget", budgetPath, "--no-web", "--check", "--strict")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "TMPDIR=" + tmpDir}
	out, err := cmd.CombinedOutput()
	return string(out), err
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

// TestTimingBudgetStillEnforcesTheWebRowWithoutTheMarker pins the scope of the
// stale-units suspension: the "web" entry is the vitest reporter's assertion
// durations, the metric issue #172 did not change, so it stays comparable to the
// checked-in budget and over 1.5x must still fail even while the Go packages'
// units are stale.
func TestTimingBudgetStillEnforcesTheWebRowWithoutTheMarker(t *testing.T) {
	measured := "SUM\tweb\t10.00\n"
	budget := `{"perTestCeilingSeconds":3,"packages":{"web":1}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err == nil {
		t.Fatalf("the web row's units did not change, so over 1.5x must still fail:\n%s", out)
	}
	if !strings.Contains(out, "FAIL  web") {
		t.Fatalf("no FAIL for the web row over 1.5x:\n%s", out)
	}
}

// TestTimingBudgetStillEnforcesTheCeilingWithoutTheMarker pins that the per-test
// ceiling stays enforced while the Go-package ratios are suspended: its seconds
// come from the same field on both sides of issue #172, so a breach is a defect
// regardless of the budget's metric.
func TestTimingBudgetStillEnforcesTheCeilingWithoutTheMarker(t *testing.T) {
	measured := "SUM\tpkg\t0.10\nTEST\tpkg\tTestSlow\t9.00\n"
	budget := `{"perTestCeilingSeconds":3,"packages":{"pkg":100}}`
	out, err := runTimingBudgetCompare(t, measured, budget)
	if err == nil {
		t.Fatalf("the per-test ceiling is metric-independent; a breach must still fail:\n%s", out)
	}
	if !strings.Contains(out, "per-test ceiling") {
		t.Fatalf("no ceiling FAIL while the metric marker is absent:\n%s", out)
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
