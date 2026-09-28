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
