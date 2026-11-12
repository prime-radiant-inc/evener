package dev

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runTimingCompareFixture writes the measurement and budget to a fresh
// directory and runs the comparison in process, returning its exit code and
// stdout. The fixture files ARE the comparator's input, so no toolchain runs.
func runTimingCompareFixture(t *testing.T, measured, budget string, args ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	measuredPath := filepath.Join(dir, "measured.tsv")
	budgetPath := filepath.Join(dir, "budget.json")
	if err := os.WriteFile(measuredPath, []byte(measured), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(budgetPath, []byte(budget), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runTimingCompare(append([]string{
		"--measured", measuredPath, "--budget", budgetPath,
	}, args...), &stdout, &stderr)
	if stderr.Len() > 0 {
		t.Logf("stderr:\n%s", stderr.String())
	}
	return code, stdout.String()
}

// TestTimingCompareFailsCPUOverBudget pins the CPU ratio and both exit
// postures: --check --strict fails, while a warn-only --check prints the same
// FAIL verdict and exits 0.
func TestTimingCompareFailsCPUOverBudget(t *testing.T) {
	const measured = "SUM\texample.com/mod\t10.00\nCPU\texample.com/mod\t10.00\n"
	const budget = `{"metric":"package-cpu-seconds","perTestCeilingSeconds":3,"packages":{"example.com/mod":1}}`
	code, out := runTimingCompareFixture(t, measured, budget, "--check", "--strict")
	if code != 1 || !strings.Contains(out, "FAIL  example.com/mod") {
		t.Fatalf("strict check = exit %d:\n%s", code, out)
	}
	code, out = runTimingCompareFixture(t, measured, budget, "--check")
	if code != 0 {
		t.Fatalf("warn-only check = exit %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "FAIL  example.com/mod") || !strings.Contains(out, "warn-only run: would exit non-zero") {
		t.Fatalf("warn-only check did not report the same verdict:\n%s", out)
	}
}

// TestTimingCompareWarnsOnStaleUnitBudgets pins the metric marker: numbers in
// another unit warn, never fail, until a rebaseline rewrites them.
func TestTimingCompareWarnsOnStaleUnitBudgets(t *testing.T) {
	const measured = "SUM\ta\t10.00\nCPU\ta\t5.00\nTEST\ta\tTestX\t0.10\n"
	const budget = `{"metric":"package-wall-seconds","perTestCeilingSeconds":3,"packages":{"a":1}}`
	code, out := runTimingCompareFixture(t, measured, budget, "--check", "--strict")
	if code != 0 || strings.Contains(out, "FAIL") {
		t.Fatalf("a wall-unit budget was enforced against CPU time: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "WARN  a") || !strings.Contains(out, "stale budget") {
		t.Fatalf("the incomparable ratio went unreported:\n%s", out)
	}
}

// TestTimingCompareWallBackstop pins the gross-wait rule: with CPU unchanged,
// 1.8x wall passes as contention and 3x wall fails.
func TestTimingCompareWallBackstop(t *testing.T) {
	const budget = `{"metric":"package-cpu-seconds","perTestCeilingSeconds":3,` +
		`"packages":{"a":10,"b":10,"c":10,"d":10,"e":10},` +
		`"wallPackages":{"a":20,"b":20,"c":20,"d":20,"e":20}}`
	run := func(eWall string) string {
		var b strings.Builder
		for _, pkg := range []string{"a", "b", "c", "d"} {
			b.WriteString("SUM\t" + pkg + "\t20.00\nCPU\t" + pkg + "\t10.00\n")
		}
		b.WriteString("SUM\te\t" + eWall + "\nCPU\te\t10.00\n")
		return b.String()
	}
	if code, out := runTimingCompareFixture(t, run("36.00"), budget, "--check", "--strict"); code != 0 || strings.Contains(out, "FAIL") {
		t.Fatalf("a package at 1.8x wall with unchanged CPU failed: exit %d\n%s", code, out)
	}
	if code, out := runTimingCompareFixture(t, run("60.00"), budget, "--check", "--strict"); code != 1 || !strings.Contains(out, "FAIL  e") {
		t.Fatalf("a package at 3x wall with unchanged CPU passed: exit %d\n%s", code, out)
	}
}

// TestTimingCompareForgivesUniformSlowdown pins the run-level slowdown: a
// uniformly slow run passes, an outlier on it still fails, and the scaled class
// ceiling still binds beyond the slowdown.
func TestTimingCompareForgivesUniformSlowdown(t *testing.T) {
	const budget = `{"metric":"package-cpu-seconds","perTestCeilingSeconds":3,` +
		`"perTestCeiling":{"TestClassed":20},` +
		`"wallPackages":{"a":10,"b":10,"c":10,"d":10,"e":10}}`
	slow := "SUM\ta\t20.00\nSUM\tb\t20.00\nSUM\tc\t20.00\nSUM\td\t21.00\nSUM\te\t20.00\n"
	code, out := runTimingCompareFixture(t, slow+"CPU\ta\t1.00\nTEST\ta\tTestClassed\t39.00\n", budget, "--check", "--strict")
	if code != 0 || strings.Contains(out, "FAIL") {
		t.Fatalf("a uniformly slow run failed: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "NOTE") {
		t.Fatalf("the slow-runner judgement went unreported:\n%s", out)
	}
	code, out = runTimingCompareFixture(t,
		"SUM\ta\t20.00\nSUM\tb\t20.00\nSUM\tc\t20.00\nSUM\td\t20.00\nSUM\te\t60.00\n", budget, "--check", "--strict")
	if code != 1 || !strings.Contains(out, "FAIL  e") {
		t.Fatalf("an outlier passed on a slow runner: exit %d\n%s", code, out)
	}
	code, out = runTimingCompareFixture(t, slow+"CPU\ta\t1.00\nTEST\ta\tTestClassed\t41.00\n", budget, "--check", "--strict")
	if code != 1 || !strings.Contains(out, "TestClassed") {
		t.Fatalf("a class breach beyond the scaled ceiling passed: exit %d\n%s", code, out)
	}
}

// TestTimingCompareCeilings pins the per-test rules: a named test (or subtest of
// one) is enforced, while a test named nowhere warns in every posture.
func TestTimingCompareCeilings(t *testing.T) {
	measured := func(name, secs string) string {
		return "SUM\tpkg\t0.10\nCPU\tpkg\t0.10\nTEST\tpkg\t" + name + "\t" + secs + "\n"
	}
	const budget = `{"metric":"package-cpu-seconds","perTestCeilingSeconds":3,` +
		`"perTestCeiling":{"TestClassed":30},"packages":{"pkg":100}}`
	if code, out := runTimingCompareFixture(t, measured("TestClassed", "31.00"), budget, "--check", "--strict"); code != 1 || !strings.Contains(out, "FAIL") {
		t.Fatalf("a named-ceiling breach did not fail: exit %d\n%s", code, out)
	}
	if code, out := runTimingCompareFixture(t, measured("TestClassed/shadow", "31.00"), budget, "--check", "--strict"); code != 1 || !strings.Contains(out, "FAIL") {
		t.Fatalf("a subtest over its parent's ceiling did not fail: exit %d\n%s", code, out)
	}
	code, out := runTimingCompareFixture(t, measured("TestUnnamed", "9.00"), budget, "--check", "--strict")
	if code != 0 || strings.Contains(out, "FAIL") || !strings.Contains(out, "TestUnnamed") {
		t.Fatalf("an unnamed breach was enforced or went unreported: exit %d\n%s", code, out)
	}
	if code, out := runTimingCompareFixture(t, measured("TestClassed", "9.00"), budget, "--check", "--strict"); code != 0 || strings.Contains(out, "WARN") {
		t.Fatalf("a named test under its ceiling was not cleared: exit %d\n%s", code, out)
	}
}

// TestTimingCompareMalformedBudgets pins the every-mode broken-config contract:
// each shape exits 1 with a failure naming the file and the offending entry,
// and never reports a comparison verdict.
func TestTimingCompareMalformedBudgets(t *testing.T) {
	const measured = "SUM\tpkg\t0.10\nCPU\tpkg\t0.10\nTEST\tpkg\tTestX\t1.00\n"
	for name, tc := range map[string]struct {
		budget string
		names  []string
	}{
		"top level list":       {`[]`, []string{"must be a JSON object", "got list"}},
		"top level int":        {`5`, []string{"got int"}},
		"top level float":      {`1.5`, []string{"got float"}},
		"packages list":        {`{"packages":[]}`, []string{"packages"}},
		"package bool":         {`{"packages":{"slowpkg":true}}`, []string{"slowpkg"}},
		"package null":         {`{"packages":{"slowpkg":null}}`, []string{"slowpkg"}},
		"package infinity":     {`{"packages":{"slowpkg":1e309}}`, []string{"slowpkg", "Infinity"}},
		"package NaN":          {`{"packages":{"slowpkg":NaN}}`, []string{"slowpkg", "NaN"}},
		"package huge integer": {`{"packages":{"slowpkg":` + strings.Repeat("9", 400) + `}}`, []string{"slowpkg", "too large for a float"}},
		"duplicate test":       {`{"perTestCeiling":{"TestX":30,"TestX":20},"packages":{"pkg":100}}`, []string{"TestX", "more than once"}},
		"ceiling map null":     {`{"perTestCeiling":null,"packages":{"pkg":100}}`, []string{"perTestCeiling"}},
		"flat ceiling bool":    {`{"perTestCeilingSeconds":true,"packages":{"pkg":100}}`, []string{"perTestCeilingSeconds"}},
		"unparseable":          {`{"packages":`, []string{"could not be parsed"}},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runTimingCompareFixture(t, measured, tc.budget, "--check", "--strict")
			if code != 1 {
				t.Fatalf("exit = %d, want 1:\n%s", code, out)
			}
			if !strings.Contains(out, "MALFORMED BUDGET") {
				t.Fatalf("the failure did not identify a malformed budget:\n%s", out)
			}
			for _, want := range tc.names {
				if !strings.Contains(out, want) {
					t.Fatalf("the failure does not name %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "Traceback") {
				t.Fatalf("the failure crashed:\n%s", out)
			}
		})
	}
}

// TestTimingCompareBlessMatchesPythonFormat pins the whole blessed file: key
// order follows the checked-in file, new keys are sorted, Go budgets floor at
// 2.0, web keeps its own number, and the layout is json.dump(indent=1) — the
// format that keeps a rebaseline from reformatting the file.
func TestTimingCompareBlessMatchesPythonFormat(t *testing.T) {
	const budget = `{"metric":"package-cpu-seconds","perTestCeilingSeconds":3,` +
		`"perTestCeiling":{"TestClassed":20},` +
		`"packages":{"recorded":1},` +
		`"wallPackages":{"recorded":1}}`
	const measured = "SUM\trecorded\t2.50\nCPU\trecorded\t2.50\n" +
		"SUM\tnew\t0.50\nCPU\tnew\t0.50\n" +
		"SUM\tweb\t100.00\n"
	dir := t.TempDir()
	measuredPath := filepath.Join(dir, "measured.tsv")
	budgetPath := filepath.Join(dir, "budget.json")
	if err := os.WriteFile(measuredPath, []byte(measured), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(budgetPath, []byte(budget), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runTimingCompare([]string{"--measured", measuredPath, "--budget", budgetPath, "--bless"}, &stdout, &stderr); code != 0 {
		t.Fatalf("bless exit = %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	got, err := os.ReadFile(budgetPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
 "metric": "package-cpu-seconds",
 "perTestCeilingSeconds": 3,
 "perTestCeiling": {
  "TestClassed": 20
 },
 "packages": {
  "recorded": 2.5,
  "new": 2.0
 },
 "wallPackages": {
  "recorded": 2.5,
  "new": 2.0,
  "web": 100.0
 }
}
`
	if string(got) != want {
		t.Fatalf("blessed budget:\n%s\nwant:\n%s", got, want)
	}
}

// TestTimingCompareBlessFullDropsUnmeasured pins the full-surface rule: a full
// bless drops an entry go list no longer reports and stamps the metric marker.
func TestTimingCompareBlessFullDropsUnmeasured(t *testing.T) {
	const measured = "SUM\ta\t1.00\nCPU\ta\t1.00\n"
	dir := t.TempDir()
	measuredPath := filepath.Join(dir, "measured.tsv")
	budgetPath := filepath.Join(dir, "budget.json")
	if err := os.WriteFile(measuredPath, []byte(measured), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(budgetPath, []byte(`{"packages":{"gone":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runTimingCompare([]string{"--measured", measuredPath, "--budget", budgetPath, "--bless", "--bless-full"}, &stdout, &stderr); code != 0 {
		t.Fatalf("bless exit = %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	got, _ := os.ReadFile(budgetPath)
	if strings.Contains(string(got), "gone") {
		t.Fatalf("a full bless kept an unmeasured entry:\n%s", got)
	}
	if !strings.Contains(string(got), `"metric": "package-cpu-seconds"`) {
		t.Fatalf("a full bless did not stamp the metric marker:\n%s", got)
	}
}

// TestTimingCompareNoBaselineAlwaysExitsZero pins the deliberate green-tree
// warn state: with no packages recorded, even --check --strict exits 0.
func TestTimingCompareNoBaselineAlwaysExitsZero(t *testing.T) {
	code, out := runTimingCompareFixture(t, "SUM\ta\t100.00\nCPU\ta\t100.00\n", `{}`, "--check", "--strict")
	if code != 0 || !strings.Contains(out, "NO BASELINE") {
		t.Fatalf("no-baseline run = exit %d:\n%s", code, out)
	}
}

// TestTimingCompareBrokenMeasurement pins the completeness half: a Go package
// that ran tests without a CPU row fails in every mode before the budget is
// read, even when the budget itself would also be unusable.
func TestTimingCompareBrokenMeasurement(t *testing.T) {
	code, out := runTimingCompareFixture(t, "SUM\tpkg\t0.10\nTEST\tpkg\tTestX\t0.10\n", `{}`)
	if code != 1 || !strings.Contains(out, "BROKEN MEASUREMENT") {
		t.Fatalf("missing CPU row = exit %d:\n%s", code, out)
	}
}

// TestTimingCompareDuplicateKeysKeepTheLast pins json.load's duplicate-key rule
// by behaviour: a later package budget is the one enforced.
func TestTimingCompareDuplicateKeysKeepTheLast(t *testing.T) {
	const measured = "SUM\ta\t2.00\nCPU\ta\t2.00\n"
	const budget = `{"metric":"package-cpu-seconds","packages":{"a":10,"a":1}}`
	code, out := runTimingCompareFixture(t, measured, budget, "--check", "--strict")
	if code != 1 || !strings.Contains(out, "FAIL  a") {
		t.Fatalf("the last duplicate key was not enforced: exit %d\n%s", code, out)
	}
}

// TestPythonValues pins the formatting conversions the bless path depends on.
func TestPythonValues(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{2.0, "2.0"},
		{2.5, "2.5"},
		{math.Copysign(0, -1), "-0.0"},
		{0.0001, "0.0001"},
		{0.00001, "1e-05"},
		{1e300, "1e+300"},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
		{math.NaN(), "NaN"},
	} {
		if got := pythonFloat(tc.in); got != tc.want {
			t.Errorf("pythonFloat(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		raw, want string
	}{
		{"3", "3"},
		{"2.0", "2.0"},
		{"1e2", "100.0"},
		{"1e309", "Infinity"},
		{"-0", "0"},
	} {
		if got := pythonJSONNumber(tc.raw); got != tc.want {
			t.Errorf("pythonJSONNumber(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestParseJSONDocumentKeepsObjectOrderAndDuplicates pins the structural parse
// the duplicate-ceiling check relies on.
func TestParseJSONDocumentKeepsObjectOrderAndDuplicates(t *testing.T) {
	v, err := parseJSONDocument([]byte(`{"a":1,"b":2,"a":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.obj) != 3 || v.obj[0].key != "a" || v.obj[1].key != "b" || v.obj[2].key != "a" {
		t.Fatalf("members = %+v, want a, b, a in file order", v.obj)
	}
	if got, _ := v.member("a"); got == nil || got.num != "3" {
		t.Fatalf("member(a) = %+v, want the last value 3", got)
	}
}
