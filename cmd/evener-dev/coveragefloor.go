// Package main is the home of evener-dev's subcommands. This file implements
// `coverage-floor`, the Go port of scripts/coverage/test-coverage-floor.sh: a
// no-regression ratchet on whole-module FULL-SUITE (unit+integration) statement
// coverage. The companion fuzz ratchet (fuzz-coverage-global.sh) stays in
// shell; this one guards the coverage the whole `go test` suite drives, so a PR
// that deletes tests or adds untested code fails the gate instead of silently
// eroding the numbers the coverage campaign won.
//
// It measures the SAME surface `ROOT_FULL=1 make test` proves - the contract
// make merge-approval-gate runs - by reusing the gate's own test selection from
// internal/devtool/gatesurface: ordinary Test/Example functions, fuzz-owned
// names skipped, and -short on every module except the root. Matching the gate
// is the whole point: a floor blessed against a surface no gate reproduces
// cannot be defended when it moves.
//
// Each module is measured against ITS OWN packages (`go list ./...`), not
// against the `./...` filesystem pattern, which under go.work also matches
// every nested module beneath the root and made the root row a whole-repo
// number.
//
// Usage:
//
//	evener-dev coverage-floor                     # measure + print (all modules)
//	evener-dev coverage-floor --check             # ratchet: exit non-zero on a drop
//	evener-dev coverage-floor --bless             # raise floors to current %
//	evener-dev coverage-floor --modules "agent llm"
//	evener-dev coverage-floor --tolerance 0.5     # wobble band (default 0.5pp)
//
// Floors live in scripts/coverage/testcov-global-floors.txt ("<module> <pct>"
// per line). Raised upward only by --bless; a deliberate downward reset (a
// denominator change, not a coverage regression) is a hand edit with a comment,
// exactly like the fuzz floor file.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/devtool/covstmt"
	"primeradiant.com/evener/internal/devtool/gatesurface"
)

// coverageFloorConfig wires the coverage-floor command to its environment.
// runGoTest must write a Go coverage profile to profilePath for the given
// module and its own output to logPath; covstmt.StmtCounts parses the profile
// for real, so the integration with the real counting package is exercised
// end-to-end even under a stubbed runGoTest.
type coverageFloorConfig struct {
	args      []string
	getenv    func(string) string
	stdout    io.Writer
	stderr    io.Writer
	runGoTest func(module, profilePath, logPath string) error
}

// errCovFloorNoModule reports a --modules entry with no go.mod: there is
// nothing there to measure. It is deliberately not a failed measurement - the
// shell reported it as "(no module)" and kept going - but a FLOORED module that
// cannot be measured is an unenforced ratchet, which --check fails on.
var errCovFloorNoModule = errors.New("no go.mod in module directory")

// covFloorRow is one module's line in the report. A row that was not measured
// carries the reason in note instead of numbers.
type covFloorRow struct {
	module   string
	covered  int
	total    int
	pct      float64
	measured bool
	note     string
}

const (
	// covFloorDefaultModules is the module set the ratchet covers. It matches
	// run-module-tests.sh's MODULES: measuring fewer would silently stop
	// enforcing the floors of whatever was dropped.
	covFloorDefaultModules = ". agent llm auth envvars invariant identifier"
	// covFloorDefaultFloors is relative to the repo root, which is where the
	// Makefile invokes this command from.
	covFloorDefaultFloors = "scripts/coverage/testcov-global-floors.txt"
	// covFloorDefaultTolerance is the ratchet's wobble band: a measured
	// percentage within this many percentage points below the floor still
	// passes.
	covFloorDefaultTolerance = 0.5
)

const covFloorUsage = `usage: evener-dev coverage-floor [--check|--bless] [--modules "m1 m2"] [--floors PATH] [--tolerance PP]

Ratchets whole-module full-suite statement coverage against
scripts/coverage/testcov-global-floors.txt, measuring the same surface
ROOT_FULL=1 make test proves.

  --check          exit non-zero when a module drops below its floor
  --bless          raise floors to the measured percentages (upward only)
  --modules LIST   space-separated modules (default: ` + covFloorDefaultModules + `)
  --floors PATH    floor file (default: ` + covFloorDefaultFloors + `)
  --tolerance PP   wobble band in percentage points (default: 0.5)
`

// coverageFloor runs the coverage-floor ratchet against the configured modules
// and floor file, returning the process exit code.
func coverageFloor(cfg coverageFloorConfig) int {
	modules := strings.Fields(covFloorDefaultModules)
	floorsPath := covFloorDefaultFloors
	tolerance := covFloorDefaultTolerance
	check, bless := false, false

	// value reads the argument after flag i, reporting a flag given without one.
	value := func(i *int) (string, bool) {
		flag := cfg.args[*i]
		*i++
		if *i >= len(cfg.args) {
			fmt.Fprintf(cfg.stderr, "coverage-floor: %s needs a value\n", flag)
			return "", false
		}
		return cfg.args[*i], true
	}
	for i := 0; i < len(cfg.args); i++ {
		switch cfg.args[i] {
		case "--modules":
			v, ok := value(&i)
			if !ok {
				return 2
			}
			modules = strings.Fields(v)
		case "--floors":
			v, ok := value(&i)
			if !ok {
				return 2
			}
			floorsPath = v
		case "--tolerance":
			v, ok := value(&i)
			if !ok {
				return 2
			}
			parsed, err := strconv.ParseFloat(v, 64)
			if err != nil {
				fmt.Fprintf(cfg.stderr, "coverage-floor: --tolerance %q is not a number\n", v)
				return 2
			}
			tolerance = parsed
		case "--check":
			check = true
		case "--bless":
			bless = true
		case "-h", "--help":
			fmt.Fprint(cfg.stdout, covFloorUsage)
			return 0
		default:
			fmt.Fprintf(cfg.stderr, "coverage-floor: unknown flag: %s\n", cfg.args[i])
			fmt.Fprint(cfg.stderr, covFloorUsage)
			return 2
		}
	}

	// A missing floor file is not an error: it means no floors are enforced
	// (measure-only with no baseline).
	floors, floorOrder, err := readCovFloors(floorsPath)
	if err != nil {
		fmt.Fprintf(cfg.stderr, "coverage-floor: reading floors %q: %v\n", floorsPath, err)
		return 1
	}

	// Scratch for the per-module coverage profiles and `go test` logs. An
	// explicit path under TMPDIR, not the system temp dir, so the dev-tooling
	// wave's per-suite leftover check can see it. A failing run keeps the whole
	// directory, because those logs are the only record of why it failed.
	tmpdir := cfg.getenv(envvars.TmpDir.Name)
	if tmpdir == "" {
		tmpdir = os.TempDir()
	}
	scratch, err := os.MkdirTemp(tmpdir, "covfloor-*")
	if err != nil {
		fmt.Fprintf(cfg.stderr, "coverage-floor: creating scratch dir: %v\n", err)
		return 1
	}
	// The profile and log paths are handed to a `go test` that runs with its
	// working directory inside the module, so they have to be absolute.
	if abs, absErr := filepath.Abs(scratch); absErr == nil {
		scratch = abs
	}
	keepScratch := false
	defer func() {
		if !keepScratch {
			os.RemoveAll(scratch)
		}
	}()

	measured := make(map[string]covFloorRow)
	rows := make([]covFloorRow, 0, len(modules))
	measureFailed := false
	for _, m := range modules {
		row := covFloorRow{module: m}
		// Module "." would otherwise write its profile and log as dotfiles.
		name := m
		if name == "." {
			name = "root"
		}
		name = strings.ReplaceAll(name, "/", "_")
		profilePath := filepath.Join(scratch, name+".cov")
		logPath := filepath.Join(scratch, name+".log")

		switch err := cfg.runGoTest(m, profilePath, logPath); {
		case errors.Is(err, errCovFloorNoModule):
			row.note = "(no module)"
		case err != nil:
			row.note = fmt.Sprintf("TEST FAILED (log: %s)", logPath)
			fmt.Fprintf(cfg.stderr, "coverage-floor: %s: go test failed: %v (log: %s)\n", m, err, logPath)
			measureFailed = true
			keepScratch = true
		default:
			covered, total, countErr := covstmt.StmtCounts(profilePath)
			switch {
			case countErr != nil:
				row.note = "no profile"
				fmt.Fprintf(cfg.stderr, "coverage-floor: %s: counting statements: %v\n", m, countErr)
				measureFailed = true
				keepScratch = true
			case total <= 0:
				// The shape every module degrades to when the counting itself
				// breaks. It is not a 0.0% measurement: blessing it would write
				// a bogus floor, so it is reported as unmeasured instead.
				row.note = "no statements"
			default:
				row.covered, row.total = covered, total
				row.pct = 100.0 * float64(covered) / float64(total)
				row.measured = true
				measured[m] = row
			}
		}
		rows = append(rows, row)
	}

	// A floored module nobody measured is an unenforced ratchet, not a
	// skippable row: renaming or deleting one quietly disabled its floor while
	// --check stayed green.
	var unenforced []string
	for _, m := range floorOrder {
		if _, ok := measured[m]; !ok {
			unenforced = append(unenforced, m)
		}
	}
	reported := make(map[string]bool, len(rows))
	for _, r := range rows {
		reported[r.module] = true
	}

	tw := tabwriter.NewWriter(cfg.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODULE\tCOVERED\tTOTAL\tPCT\tFLOOR\tSTATUS")
	for _, r := range rows {
		floor, hasFloor := floors[r.module]
		floorStr := "-"
		if hasFloor {
			floorStr = fmt.Sprintf("%.1f", floor)
		}
		if !r.measured {
			fmt.Fprintf(tw, "%s\t-\t-\t-\t%s\t%s\n", r.module, floorStr, r.note)
			continue
		}
		status := "PASS"
		if hasFloor && r.pct < floor-tolerance {
			status = "DROP"
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%.1f\t%s\t%s\n", r.module, r.covered, r.total, r.pct, floorStr, status)
	}
	for _, m := range unenforced {
		if reported[m] {
			continue // already printed with its own reason
		}
		fmt.Fprintf(tw, "%s\t-\t-\t-\t%.1f\t%s\n", m, floors[m], "UNMEASURED")
	}
	tw.Flush()

	regressed := false
	for _, r := range rows {
		if !r.measured {
			continue
		}
		if floor, ok := floors[r.module]; ok && r.pct < floor-tolerance {
			regressed = true
			if check {
				fmt.Fprintf(cfg.stderr,
					"    REGRESSION: %s test coverage %.1f%% < floor %.1f%% (tolerance %gpp)\n",
					r.module, r.pct, floor, tolerance)
			}
		}
	}
	switch {
	case check:
		for _, m := range unenforced {
			fmt.Fprintf(cfg.stderr,
				"    UNMEASURED: %s has a floor (%.1f%%) but was not measured; the floor is not being enforced\n",
				m, floors[m])
		}
	case !bless:
		for _, m := range unenforced {
			fmt.Fprintf(cfg.stderr,
				"WARNING: floor-file module %q was not measured (floor %.1f%% unenforced)\n",
				m, floors[m])
		}
	}

	if bless {
		// A bless is the one mode with no ratchet of its own. Rewriting the
		// floors from a run that measured nothing turns a broken run into
		// "floors updated", and the next --check compares against nothing.
		if len(measured) == 0 {
			fmt.Fprintf(cfg.stderr, "coverage-floor: --bless measured nothing; leaving %s alone\n", floorsPath)
			return 1
		}
		if err := blessCovFloors(floorsPath, measured); err != nil {
			fmt.Fprintf(cfg.stderr, "coverage-floor: writing floors: %v\n", err)
			return 1
		}
		fmt.Fprintf(cfg.stdout, "blessed floors -> %s\n", floorsPath)
		// What was measured is blessed upward, but a run that lost a module to
		// a failed measurement still failed.
		if measureFailed {
			return 1
		}
		return 0
	}

	if measureFailed {
		fmt.Fprintf(cfg.stderr, "coverage-floor: retained logs: %s\n", scratch)
		return 1
	}
	if check && (regressed || len(unenforced) > 0) {
		return 1
	}
	return 0
}

// readCovFloors parses a floor file: "<module> <pct>" per line, '#' comments and
// blank lines skipped. order preserves the first-seen order of data modules. A
// missing file yields an empty map (no floors enforced), not an error.
func readCovFloors(path string) (map[string]float64, []string, error) {
	floors := make(map[string]float64)
	var order []string
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return floors, order, nil
		}
		return nil, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pct, perr := strconv.ParseFloat(fields[1], 64)
		if perr != nil {
			continue
		}
		if _, exists := floors[fields[0]]; !exists {
			order = append(order, fields[0])
		}
		floors[fields[0]] = pct
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	return floors, order, nil
}

// blessCovFloors rewrites the floor file, raising each measured module's floor
// to max(old, measured) and appending a floor for any measured module the file
// has never heard of. Comment and blank lines are preserved verbatim: a
// downward reset is a hand edit whose comment records WHY the basis changed,
// and rewriting the header would delete that reason the next time anyone raised
// a floor. A floor-file module that was not measured keeps its existing floor,
// so improving one module at a time - the normal way coverage work happens -
// does not drop the ratchet everywhere else.
func blessCovFloors(path string, measured map[string]covFloorRow) error {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	written := make(map[string]bool, len(measured))
	var out strings.Builder
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		fields := strings.Fields(trim)
		switch {
		case trim == "" || strings.HasPrefix(trim, "#") || len(fields) < 2:
			out.WriteString(line)
		default:
			if meas, ok := measured[fields[0]]; ok {
				old, _ := strconv.ParseFloat(fields[1], 64)
				if meas.pct > old {
					old = meas.pct
				}
				fmt.Fprintf(&out, "%s %.1f", fields[0], old)
				written[fields[0]] = true
			} else {
				out.WriteString(line)
			}
		}
		if i < len(lines)-1 {
			out.WriteString("\n")
		}
	}

	// A measured module with no line of its own gets one. Without this a newly
	// measured module was blessed into nothing and stayed unratcheted forever.
	var appended []string
	for m := range measured {
		if !written[m] {
			appended = append(appended, m)
		}
	}
	sort.Strings(appended)
	if len(appended) > 0 {
		body := out.String()
		if body != "" && !strings.HasSuffix(body, "\n") {
			out.WriteString("\n")
		}
		for _, m := range appended {
			fmt.Fprintf(&out, "%s %.1f\n", m, measured[m].pct)
		}
	}
	return os.WriteFile(path, []byte(out.String()), 0o644)
}

// covFloorGoTestArgs builds the `go test` argv for one module's measurement.
// The selection is the gate's own (gatesurface), with -short on every module
// except the root, mirroring run-module-tests.sh's module_test_flags under
// ROOT_FULL=1. -count=1 is REQUIRED: a cached coverage profile reports stale
// numbers.
func covFloorGoTestArgs(module, profilePath string, pkgs []string) []string {
	args := []string{"test", "-count=1"}
	if module != "." {
		args = append(args, "-short")
	}
	return append(args,
		"-coverpkg="+strings.Join(pkgs, ","),
		"-coverprofile="+profilePath,
		"-run", gatesurface.TestRun,
		"-skip", gatesurface.FuzzTestSkip,
		"./...")
}

// covFloorPackages lists the packages the module at dir owns. -coverpkg takes
// FILESYSTEM patterns, and under go.work `./...` matches every package in the
// tree below the module - which for the root module means agent/, llm/, auth/
// and every other nested module too, turning the root row into a whole-repo
// figure diluted by code the root module's own tests never run. `go list ./...`
// resolves within the module, so it names this module's packages and nothing
// else.
func covFloorPackages(dir string) ([]string, error) {
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list ./... in %s: %w", dir, err)
	}
	pkgs := strings.Fields(string(out))
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("go list ./... in %s named no packages", dir)
	}
	return pkgs, nil
}

// realCovFloorGoTest is the production measurement: it runs the module's suite
// under coverage from inside the module directory (under go.work a bare module
// name is not a valid package path from the root), keeping the full output in
// logPath so a failure has something behind it.
func realCovFloorGoTest(module, profilePath, logPath string) error {
	dir := module
	if dir == "" {
		dir = "."
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return errCovFloorNoModule
	}
	pkgs, err := covFloorPackages(dir)
	if err != nil {
		return err
	}
	log, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("creating log %s: %w", logPath, err)
	}
	defer log.Close()
	cmd := exec.Command("go", covFloorGoTestArgs(module, profilePath, pkgs)...)
	cmd.Dir = dir
	cmd.Stdout = log
	cmd.Stderr = log
	return cmd.Run()
}

// runCoverageFloor is the subcommand entry: it wires coverageFloor to the real
// environment and the real `go test` measurement.
func runCoverageFloor(args []string) int {
	return coverageFloor(coverageFloorConfig{
		args:      args,
		getenv:    os.Getenv,
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		runGoTest: realCovFloorGoTest,
	})
}
