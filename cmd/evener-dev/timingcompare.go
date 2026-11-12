package dev

// timing-compare is the comparison half of scripts/gate/test-timing-budget.sh,
// ported from the inline Python comparator (issue #5528). It reads the measured
// TSV rows and the checked-in budget, applies the ratchet's ratio, slowdown and
// per-test-ceiling rules, and optionally rewrites the budget on --bless.
//
// The script keeps the measurement half (go test under test-cpu-exec, vitest)
// because that is where the toolchain lives; this subcommand is the whole
// verdict contract. timingbudget_test.go drives the script end to end, and
// timingcompare_test.go pins this port directly.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	timingDefaultCeiling      = 2.0
	timingFailRatio           = 1.5
	timingWarnRatio           = 1.1
	timingWallBackstopRatio   = 2.5
	timingGoBudgetFloor       = 2.0
	timingSlowdownMinBudget   = 5.0
	timingSlowdownMinPackages = 3
	timingCPUMetric           = "package-cpu-seconds"
	timingWebKey              = "web"
	timingWorstOK             = 0
	timingWorstWarn           = 1
	timingWorstFail           = 2
)

// timingTestResult is one TEST row: a top-level test or subtest.
type timingTestResult struct {
	pkg     string
	name    string
	seconds float64
}

// timingMeasurement is the TSV the producers write: per-package wall and CPU
// seconds plus the per-test rows the ceiling check reads.
type timingMeasurement struct {
	sums  map[string]float64
	cpus  map[string]float64
	tests []timingTestResult
}

type timingCompareOptions struct {
	measuredPath string
	budgetPath   string
	bless        bool
	check        bool
	strict       bool
	blessFull    bool
}

func timingCompareMain(args []string) int {
	return runTimingCompare(args, os.Stdout, os.Stderr)
}

func runTimingCompare(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("timing-compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o timingCompareOptions
	fs.StringVar(&o.measuredPath, "measured", "", "the SUM/CPU/TEST/PKG rows to compare")
	fs.StringVar(&o.budgetPath, "budget", "", "the testing-budget.json to compare against")
	fs.BoolVar(&o.bless, "bless", false, "rewrite the budget from this measurement")
	fs.BoolVar(&o.check, "check", false, "apply the check's exit policy")
	fs.BoolVar(&o.strict, "strict", false, "with --check, fail on a failure verdict")
	fs.BoolVar(&o.blessFull, "bless-full", false, "this run measured the whole budgeted surface, so a bless may drop unmeasured entries")
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "usage: evener-dev timing-compare --measured FILE --budget FILE [--check] [--strict] [--bless] [--bless-full]\n\n"+
			"Compare one measurement's TSV rows against testing-budget.json using the test-timing-budget ratchet's rules.\n"+
			"--bless rewrites the budget in the checked-in format; --bless-full also drops entries\n"+
			"the measurement does not contain. A broken measurement or malformed budget exits 1;\n"+
			"with --check, --strict makes a FAIL verdict nonzero. Without --check the verdicts print\n"+
			"but the exit is zero.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if o.measuredPath == "" || o.budgetPath == "" || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	return compareTimingBudget(o, stdout, stderr)
}

// readTimingMeasurement reads the ratchet's TSV rows. An unknown row kind is
// ignored, as the Python comparator ignored it; a row of a known kind with the
// wrong field count or an unparseable duration is a broken measurement.
func readTimingMeasurement(path string) (timingMeasurement, error) {
	m := timingMeasurement{sums: map[string]float64{}, cpus: map[string]float64{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	for lineNo, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(line, "\t")
		if parts[0] == "" {
			continue
		}
		switch parts[0] {
		case "SUM", "CPU":
			if len(parts) != 3 {
				return m, fmt.Errorf("%s:%d: %s row wants 3 fields, got %d", path, lineNo+1, parts[0], len(parts))
			}
			secs, err := strconv.ParseFloat(parts[2], 64)
			if err != nil {
				return m, fmt.Errorf("%s:%d: %s duration %q: %w", path, lineNo+1, parts[0], parts[2], err)
			}
			if parts[0] == "SUM" {
				m.sums[parts[1]] += secs
			} else {
				m.cpus[parts[1]] += secs
				if _, ok := m.sums[parts[1]]; !ok {
					m.sums[parts[1]] = 0
				}
			}
		case "TEST":
			if len(parts) != 4 {
				return m, fmt.Errorf("%s:%d: TEST row wants 4 fields, got %d", path, lineNo+1, len(parts))
			}
			secs, err := strconv.ParseFloat(parts[3], 64)
			if err != nil {
				return m, fmt.Errorf("%s:%d: TEST duration %q: %w", path, lineNo+1, parts[3], err)
			}
			m.tests = append(m.tests, timingTestResult{pkg: parts[1], name: parts[2], seconds: secs})
		case "PKG":
			if len(parts) < 2 {
				return m, fmt.Errorf("%s:%d: PKG row wants a package", path, lineNo+1)
			}
			if parts[1] != "" {
				if _, ok := m.sums[parts[1]]; !ok {
					m.sums[parts[1]] = 0
				}
			}
		}
	}
	return m, nil
}

// malformedBudgetError separates a broken config from an I/O failure: a
// malformed budget is a verdict naming the file, not a reason to stop before
// the comparison contract can speak.
type malformedBudgetError struct{ reason string }

func (e *malformedBudgetError) Error() string { return e.reason }

// readTimingBudgetFile parses the budget. A missing file is the deliberate
// no-baseline warn state and yields an empty object.
func readTimingBudgetFile(path string) (*jsonValue, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newJSONObject(), nil
	}
	if err != nil {
		return nil, err
	}
	budget, err := parseJSONDocument(data)
	if err != nil {
		return nil, &malformedBudgetError{reason: "could not be parsed: " + err.Error()}
	}
	return budget, nil
}

func compareTimingBudget(o timingCompareOptions, stdout, stderr io.Writer) int {
	measurement, err := readTimingMeasurement(o.measuredPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev timing-compare: %v\n", err)
		return 1
	}

	// A Go package whose tests ran without test-cpu-exec was measured without
	// the number its budget is in: that is a broken measurement in every mode,
	// and it is checked before the budget is read, as the Python comparator did.
	uncounted := map[string]bool{}
	for _, t := range measurement.tests {
		if t.pkg != timingWebKey {
			if _, ok := measurement.cpus[t.pkg]; !ok {
				uncounted[t.pkg] = true
			}
		}
	}
	if len(uncounted) > 0 {
		var lines []string
		for _, pkg := range sortedKeys(uncounted) {
			lines = append(lines, fmt.Sprintf("FAIL  BROKEN MEASUREMENT: %s ran tests but has no CPU row (its test binary did not run under test-cpu-exec)", pkg))
		}
		return writeTimingOutput(stdout, lines, 1)
	}

	failMalformed := func(what string) int {
		return writeTimingOutput(stdout, []string{
			"FAIL  MALFORMED BUDGET: testing-budget.json " + what + " (" + o.budgetPath + ")",
		}, 1)
	}
	budget, err := readTimingBudgetFile(o.budgetPath)
	if err != nil {
		if malformed, ok := errors.AsType[*malformedBudgetError](err); ok {
			return failMalformed(malformed.reason)
		}
		_, _ = fmt.Fprintf(stderr, "evener-dev timing-compare: %v\n", err)
		return 1
	}
	// A budget whose top level is not an object is a broken config, not an
	// empty one: every field read below would otherwise crash.
	if budget.kind != jsonObject {
		return failMalformed("must be a JSON object (got " + budget.typeName() + ")")
	}

	// The per-test ceiling map is hand-edited, so a test named twice must fail
	// naming the collision. The re-parse is structural: only the LAST
	// perTestCeiling object is the one json.load kept.
	if ceilingObj, ok := budget.member("perTestCeiling"); ok && ceilingObj.kind == jsonObject {
		seen := map[string]bool{}
		for _, m := range ceilingObj.obj {
			if seen[m.key] {
				return failMalformed("perTestCeiling[" + jsonQuote(m.key) + "] more than once")
			}
			seen[m.key] = true
		}
	}

	packagesObj, hasPackages := budget.member("packages")
	if hasPackages && packagesObj.kind != jsonObject {
		return failMalformed("packages must be an object mapping each package to its budget seconds (got " + packagesObj.typeName() + ")")
	}
	wallObj, hasWallPackages := budget.member("wallPackages")
	if hasWallPackages && wallObj.kind != jsonObject {
		return failMalformed("wallPackages must be an object mapping each package to its wall seconds (got " + wallObj.typeName() + ")")
	}
	if !hasPackages {
		packagesObj = newJSONObject()
	}
	if !hasWallPackages {
		wallObj = newJSONObject()
	}

	// Every package value must be a usable number, not only the map's type.
	packages := map[string]float64{}
	wallPackages := map[string]float64{}
	for _, group := range []struct {
		what string
		obj  *jsonValue
		into map[string]float64
	}{
		{"packages", packagesObj, packages},
		{"wallPackages", wallObj, wallPackages},
	} {
		for _, m := range group.obj.obj {
			if problem := secondsError(m.value); problem != "" {
				return failMalformed(group.what + "[" + jsonQuote(m.key) + "] must be a number of seconds (" + problem + ")")
			}
			f, _ := m.value.float()
			group.into[m.key] = f
		}
	}

	// The flat ceiling cannot tell a present null from an absent key, so test
	// the KEY: a bool would silently become one second and a non-finite value
	// would silently disarm the ceiling.
	ceiling := timingDefaultCeiling
	if flat, ok := budget.member("perTestCeilingSeconds"); ok {
		if problem := secondsError(flat); problem != "" {
			return failMalformed("perTestCeilingSeconds must be a number of seconds (" + problem + ")")
		}
		ceiling, _ = flat.float()
	}

	ceilingObj, hasCeilings := budget.member("perTestCeiling")
	if hasCeilings && ceilingObj.kind != jsonObject {
		return failMalformed("perTestCeiling must be an object mapping each test name to its ceiling seconds (got " + ceilingObj.typeName() + ")")
	}
	perTestCeilings := map[string]float64{}
	if hasCeilings {
		for _, m := range ceilingObj.obj {
			if problem := secondsError(m.value); problem != "" {
				return failMalformed("perTestCeiling[" + jsonQuote(m.key) + "] must be a number of seconds (" + problem + ")")
			}
			f, _ := m.value.float()
			perTestCeilings[m.key] = f
		}
	}

	noBaseline := len(packages) == 0 && len(wallPackages) == 0
	hadMetric := false
	if metric, ok := budget.member("metric"); ok && metric.kind == jsonString && metric.str == timingCPUMetric {
		hadMetric = true
	}
	staleUnits := !noBaseline && !hadMetric

	c := &timingComparator{
		lines:        nil,
		worst:        timingWorstOK,
		packages:     packages,
		wallPackages: wallPackages,
		ceilings:     perTestCeilings,
		staleUnits:   staleUnits,
		noBaseline:   noBaseline,
	}

	if noBaseline {
		c.lines = append(c.lines, "NO BASELINE: testing-budget.json has no packages recorded yet; every result below is informational only and --check always exits 0 until `make test-rebaseline` lands a measured baseline (kata b6rv).")
	} else if staleUnits {
		c.lines = append(c.lines, "STALE UNITS: testing-budget.json does not record \"metric\": \""+timingCPUMetric+"\", so its numbers are not in the units this run measured; every ratio failure below warns, with its numbers, until a rebaseline records them.")
	}

	cpuSlowdown := runnerSlowdown(measurement.cpus, packages, staleUnits)
	wallSlowdown := runnerSlowdown(measurement.sums, wallPackages, staleUnits)
	for _, item := range []struct {
		what   string
		factor float64
	}{{"CPU", cpuSlowdown}, {"wall", wallSlowdown}} {
		if item.factor > timingWarnRatio {
			c.lines = append(c.lines, fmt.Sprintf("NOTE  this run's packages sit at a median %.2fx of their %s budgets: the runner is slow, so %s failure limits below are scaled by %.2fx", item.factor, item.what, item.what, item.factor))
		}
	}

	for _, pkg := range sortedKeys(measurement.sums) {
		wall := measurement.sums[pkg]
		wallBudget, hasWall := wallPackages[pkg]
		if cpu, hasCPU := measurement.cpus[pkg]; hasCPU {
			cpuBudget, hasCPUBudget := packages[pkg]
			ok := false
			if !hasCPUBudget {
				c.lines = append(c.lines, fmt.Sprintf("WARN  %s: %.2fs cpu, %.2fs wall, no budget entry (add one to testing-budget.json)", pkg, cpu, wall))
				c.raise(timingWorstWarn)
			} else {
				ok = c.judge(pkg, cpu, cpuBudget, "cpu", timingFailRatio, cpuSlowdown, true)
			}
			if hasWall {
				ok = c.judge(pkg, wall, wallBudget, "wall", timingWallBackstopRatio, wallSlowdown, false) && ok
			}
			if ok {
				c.lines = append(c.lines, fmt.Sprintf("OK    %s: %.2fs cpu (budget %.2fs), %.2fs wall", pkg, cpu, cpuBudget, wall))
			}
			continue
		}
		if !hasWall {
			c.lines = append(c.lines, fmt.Sprintf("WARN  %s: %.2fs, no budget entry (add one to testing-budget.json)", pkg, wall))
			c.raise(timingWorstWarn)
			continue
		}
		if c.judge(pkg, wall, wallBudget, "wall", timingFailRatio, wallSlowdown, true) {
			c.lines = append(c.lines, fmt.Sprintf("OK    %s: %.2fs wall (budget %.2fs)", pkg, wall, wallBudget))
		}
	}

	for _, t := range measurement.tests {
		named, namedCeiling, ok := testCeiling(t.name, perTestCeilings)
		if ok {
			if t.seconds > namedCeiling*wallSlowdown {
				c.lines = append(c.lines, fmt.Sprintf("FAIL  %s: test %s took %.2fs, over its %s ceiling (named '%s' in testing-budget.json perTestCeiling)", t.pkg, t.name, t.seconds, pythonG(namedCeiling), named))
				c.raise(timingWorstFail)
			} else {
				c.lines = append(c.lines, fmt.Sprintf("OK    %s: test %s %.2fs (named ceiling %s)", t.pkg, t.name, t.seconds, pythonG(namedCeiling)))
			}
			continue
		}
		if t.seconds > ceiling {
			// A test named nowhere keeps the flat ceiling and stays warn-only in
			// EVERY mode, --strict included (#4601).
			c.lines = append(c.lines, fmt.Sprintf("WARN  %s: test %s took %.2fs, over the %.2fs per-test ceiling (unnamed; warn-only, #4601)", t.pkg, t.name, t.seconds, ceiling))
			c.raise(timingWorstWarn)
		}
	}

	if o.bless {
		budget.set("packages", blessedBudget(packagesObj, measurement.cpus, o.blessFull))
		budget.set("wallPackages", blessedBudget(wallObj, measurement.sums, o.blessFull))
		if o.blessFull || hadMetric {
			budget.set("metric", newJSONString(timingCPUMetric))
		}
		if !budget.has("perTestCeilingSeconds") {
			budget.set("perTestCeilingSeconds", newJSONNumber(timingDefaultCeiling))
		}
		if err := os.WriteFile(o.budgetPath, []byte(budget.jsonString()+"\n"), 0o644); err != nil {
			_, _ = fmt.Fprintf(stderr, "evener-dev timing-compare: writing %s: %v\n", o.budgetPath, err)
			return 1
		}
		c.lines = append(c.lines, "blessed budget -> "+o.budgetPath)
	}

	exitCode := 0
	if o.check {
		if noBaseline {
			exitCode = 0
		} else if o.strict {
			if c.worst == timingWorstFail {
				exitCode = 1
			}
		} else {
			exitCode = 0
			switch c.worst {
			case timingWorstFail:
				c.lines = append(c.lines, "warn-only run: would exit non-zero under --strict/CI (fail)")
			case timingWorstWarn:
				c.lines = append(c.lines, "warn-only run: the warnings above are reported but not enforced")
			}
		}
	}
	return writeTimingOutput(stdout, c.lines, exitCode)
}

type timingComparator struct {
	lines        []string
	worst        int
	packages     map[string]float64
	wallPackages map[string]float64
	ceilings     map[string]float64
	staleUnits   bool
	noBaseline   bool
}

func (c *timingComparator) raise(level int) {
	if level > c.worst {
		c.worst = level
	}
}

func (c *timingComparator) judge(pkg string, measured, budget float64, unit string, failRatio, slowdown float64, warn bool) bool {
	ratio := timingRatio(measured, budget)
	if ratio/slowdown > failRatio {
		if c.staleUnits {
			c.lines = append(c.lines, fmt.Sprintf("WARN  %s: %.2fs %s over stale budget %.2fs (%.2fx > %sx; not enforced until a rebaseline)", pkg, measured, unit, budget, ratio, pythonG(failRatio)))
			c.raise(timingWorstWarn)
		} else {
			c.lines = append(c.lines, fmt.Sprintf("FAIL  %s: %.2fs %s over budget %.2fs (%.2fx > %sx)", pkg, measured, unit, budget, ratio, pythonG(failRatio)))
			c.raise(timingWorstFail)
		}
		return false
	}
	if warn && ratio > timingWarnRatio {
		c.lines = append(c.lines, fmt.Sprintf("WARN  %s: %.2fs %s over budget %.2fs (%.2fx > %sx)", pkg, measured, unit, budget, ratio, pythonG(timingWarnRatio)))
		c.raise(timingWorstWarn)
		return false
	}
	return true
}

func timingRatio(measured, budget float64) float64 {
	if budget > 0 {
		return measured / budget
	}
	if measured > 0 {
		return math.Inf(1)
	}
	return 0
}

// runnerSlowdown is the run's own slowdown: the median measured/budget ratio
// over packages whose budget is big enough to be signal, and only with enough
// of them for a median to mean "the runner" and not "one package".
func runnerSlowdown(measured, budgets map[string]float64, staleUnits bool) float64 {
	if staleUnits {
		return 1
	}
	var ratios []float64
	for pkg, budget := range budgets {
		if m, ok := measured[pkg]; ok && budget >= timingSlowdownMinBudget {
			ratios = append(ratios, m/budget)
		}
	}
	if len(ratios) < timingSlowdownMinPackages {
		return 1
	}
	sort.Float64s(ratios)
	mid := len(ratios) / 2
	var median float64
	if len(ratios)%2 == 1 {
		median = ratios[mid]
	} else {
		median = (ratios[mid-1] + ratios[mid]) / 2
	}
	if median < 1 {
		return 1
	}
	return median
}

// testCeiling returns the ceiling for name or its nearest named parent. An
// exact name wins over a parent prefix.
func testCeiling(name string, ceilings map[string]float64) (string, float64, bool) {
	if value, ok := ceilings[name]; ok {
		return name, value, true
	}
	parent := name
	for strings.Contains(parent, "/") {
		parent = parent[:strings.LastIndex(parent, "/")]
		if value, ok := ceilings[parent]; ok {
			return parent, value, true
		}
	}
	return "", 0, false
}

// blessedBudget renders a budget map from one measurement. Recorded packages
// keep the file's order; only packages the file does not mention are ordered by
// name. A narrowed bless preserves an entry it did not measure; a full bless
// drops it, because go list enumerated every package and the entry is a package
// gone from the tree.
func blessedBudget(recorded *jsonValue, measured map[string]float64, full bool) *jsonValue {
	recordedValues := map[string]*jsonValue{}
	var order []string
	if recorded != nil && recorded.kind == jsonObject {
		for _, m := range recorded.obj {
			if _, ok := recordedValues[m.key]; !ok {
				order = append(order, m.key)
			}
			recordedValues[m.key] = m.value
		}
	}
	var added []string
	for pkg := range measured {
		if _, ok := recordedValues[pkg]; !ok {
			added = append(added, pkg)
		}
	}
	sort.Strings(added)
	order = append(order, added...)

	out := newJSONObject()
	for _, pkg := range order {
		if seconds, ok := measured[pkg]; ok {
			seconds = timingRound2(seconds)
			if pkg != timingWebKey && seconds < timingGoBudgetFloor {
				seconds = timingGoBudgetFloor
			}
			out.obj = append(out.obj, jsonMember{key: pkg, value: newJSONNumber(seconds)})
			continue
		}
		if !full {
			out.obj = append(out.obj, jsonMember{key: pkg, value: recordedValues[pkg]})
		}
	}
	return out
}

// timingRound2 rounds to two decimals the way Python's round(x, 2) does: the
// closest float to the correctly rounded two-decimal decimal.
func timingRound2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 2, 64), 64)
	if err != nil {
		return v
	}
	return rounded
}

// pythonG formats a number the way Python's "{:g}" does: six significant
// digits, trailing zeros stripped.
func pythonG(f float64) string {
	return strconv.FormatFloat(f, 'g', 6, 64)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func writeTimingOutput(w io.Writer, lines []string, exitCode int) int {
	if len(lines) > 0 {
		if _, err := io.WriteString(w, strings.Join(lines, "\n")+"\n"); err != nil {
			return 1
		}
	}
	return exitCode
}
