//go:build linux || darwin

// Package main is the home of evener-dev's subcommands. This file implements
// `coverage-floor`, the Go port of scripts/coverage/test-coverage-floor.sh (and
// its companions coverage-union.sh and coverage-gaps.sh).
//
// The port keeps the shell's contract:
//
//   - For each module in --modules, produce a coverage profile via the
//     injectable runGoTest (the real subcommand shells out to `go test -cover`),
//     then count covered/total statements via internal/devtool/covstmt.StmtCounts.
//   - Compute pct = 100 * covered / total, formatted to one decimal place.
//   - Look up the module's floor in the --floors file ("<module> <pct>" per
//     line; lines starting with '#' are comments; blank lines are skipped).
//   - Print a table to stdout: module, covered, total, cov%, floor, status.
//   - --check: exit non-zero (1) when a measured module's coverage is below its
//     floor minus the tolerance (default 0.5pp). Also fail when a floor-file
//     module was not measured (its floor is unenforced).
//   - --bless: raise floors to the measured percentage (upward only: the new
//     floor is max(old, measured)), preserving comment lines. A floor-file
//     module that was not measured keeps its existing floor. Exit 0.
//   - Default (no --check, no --bless): measure and print, exit 0. Warn to
//     stderr about floor-file modules that were not measured.
//
// runGoTest is injected so tests drive the parse/compare/bless path without
// spawning `go test`.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/devtool/covstmt"
	"primeradiant.com/evener/internal/devtool/gatesurface"
)

// coverageFloorConfig wires the coverage-floor command to its environment.
// runGoTest must write a Go coverage profile to profilePath for the given
// module; covstmt.StmtCounts parses it for real, so the integration with the
// real counting package is exercised end-to-end.
type coverageFloorConfig struct {
	args      []string
	getenv    func(string) string
	stdout    io.Writer
	stderr    io.Writer
	runGoTest func(module, profilePath string) error
}

// covFloorMeasurement is the counted coverage for one measured module.
type covFloorMeasurement struct {
	module  string
	covered int
	total   int
	pct     float64
}

// coverageFloorTolerance is the ratchet tolerance: a measured percentage within
// this many percentage points below the floor still passes, matching the shell
// script's default.
const coverageFloorTolerance = 0.5

// coverageFloor runs the coverage-floor ratchet against the configured modules
// and floor file, returning the process exit code.
func coverageFloor(cfg coverageFloorConfig) int {
	// Parse args.
	var modules []string
	var floorsPath string
	check, bless := false, false
	for i := 0; i < len(cfg.args); i++ {
		switch cfg.args[i] {
		case "--modules":
			i++
			if i < len(cfg.args) {
				modules = strings.Fields(cfg.args[i])
			}
		case "--floors":
			i++
			if i < len(cfg.args) {
				floorsPath = cfg.args[i]
			}
		case "--check":
			check = true
		case "--bless":
			bless = true
		}
	}

	// Read the floor file. A missing floor file is not an error: it means no
	// floors are enforced (measure-only with no baseline).
	floors, floorOrder, err := readCovFloors(floorsPath)
	if err != nil {
		fmt.Fprintf(cfg.stderr, "coverage-floor: reading floors %q: %v\n", floorsPath, err)
		return 1
	}

	// Scratch directory for per-module coverage profiles. The test stub
	// creates the profile directory itself, but the real runGoTest relies on
	// this existing; TMPDIR is read via getenv for hermeticity.
	tmpdir := cfg.getenv(envvars.TmpDir.Name)
	if tmpdir == "" {
		tmpdir = os.TempDir()
	}
	scratch, err := os.MkdirTemp(tmpdir, "covfloor-*")
	if err != nil {
		fmt.Fprintf(cfg.stderr, "coverage-floor: creating scratch dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(scratch)

	// Measure each module.
	measured := make(map[string]covFloorMeasurement)
	measureFailed := false
	for _, m := range modules {
		profilePath := filepath.Join(scratch, m+".out")
		if err := cfg.runGoTest(m, profilePath); err != nil {
			fmt.Fprintf(cfg.stderr, "coverage-floor: go test for module %q failed: %v\n", m, err)
			measureFailed = true
			continue
		}
		covered, total, err := covstmt.StmtCounts(profilePath)
		if err != nil {
			fmt.Fprintf(cfg.stderr, "coverage-floor: counting statements for module %q: %v\n", m, err)
			measureFailed = true
			continue
		}
		var pct float64
		if total > 0 {
			pct = 100.0 * float64(covered) / float64(total)
		}
		measured[m] = covFloorMeasurement{module: m, covered: covered, total: total, pct: pct}
	}

	// Identify floor-file modules that were not measured.
	var unmeasured []string
	for _, m := range floorOrder {
		if _, ok := measured[m]; !ok {
			unmeasured = append(unmeasured, m)
		}
	}

	// Build and print the table.
	var rows [][]string
	for _, m := range modules {
		meas, ok := measured[m]
		if !ok {
			continue
		}
		floor, hasFloor := floors[m]
		floorStr := "-"
		status := "PASS"
		if hasFloor {
			floorStr = fmt.Sprintf("%.1f", floor)
			if meas.pct < floor-coverageFloorTolerance {
				status = "DROP"
			}
		}
		rows = append(rows, []string{
			m,
			strconv.Itoa(meas.covered),
			strconv.Itoa(meas.total),
			fmt.Sprintf("%.1f", meas.pct),
			floorStr,
			status,
		})
	}
	for _, m := range unmeasured {
		rows = append(rows, []string{
			m, "-", "-", "-",
			fmt.Sprintf("%.1f", floors[m]),
			"UNMEASURED",
		})
	}
	tw := tabwriter.NewWriter(cfg.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODULE\tCOVERED\tTOTAL\tPCT\tFLOOR\tSTATUS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r[0], r[1], r[2], r[3], r[4], r[5])
	}
	tw.Flush()

	// Report regressions and unmeasured floors to stderr.
	if check {
		for _, m := range modules {
			meas, ok := measured[m]
			if !ok {
				continue
			}
			floor, hasFloor := floors[m]
			if hasFloor && meas.pct < floor-coverageFloorTolerance {
				fmt.Fprintf(cfg.stderr,
					"REGRESSION module=%s measured=%.1f floor=%.1f (below floor by %.1fpp)\n",
					m, meas.pct, floor, floor-meas.pct)
			}
		}
		for _, m := range unmeasured {
			fmt.Fprintf(cfg.stderr,
				"REGRESSION module=%s status=UNMEASURED floor=%.1f (floor not enforced: module not measured)\n",
				m, floors[m])
		}
	} else if !bless {
		for _, m := range unmeasured {
			fmt.Fprintf(cfg.stderr,
				"WARNING: floor-file module %q was not measured (floor %.1f unenforced)\n",
				m, floors[m])
		}
	}

	// --bless rewrites the floor file upward and exits 0.
	if bless {
		if err := blessCovFloors(floorsPath, modules, measured); err != nil {
			fmt.Fprintf(cfg.stderr, "coverage-floor: writing floors: %v\n", err)
			return 1
		}
		return 0
	}

	// Exit code: measurement failure is always fatal; --check fails on any
	// regression or unenforced floor.
	exitCode := 0
	if measureFailed {
		exitCode = 1
	}
	if check {
		for _, m := range modules {
			meas, ok := measured[m]
			if !ok {
				continue
			}
			floor, hasFloor := floors[m]
			if hasFloor && meas.pct < floor-coverageFloorTolerance {
				exitCode = 1
				break
			}
		}
		if len(unmeasured) > 0 {
			exitCode = 1
		}
	}
	return exitCode
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
// to max(old, measured) while preserving comment and blank lines verbatim. A
// floor-file module that was not measured keeps its existing floor. A missing
// floor file is created from the measured modules.
func blessCovFloors(path string, modules []string, measured map[string]covFloorMeasurement) error {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Missing or empty floor file: seed it from the measured modules.
	if os.IsNotExist(err) || len(content) == 0 {
		var b strings.Builder
		for _, m := range modules {
			meas, ok := measured[m]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "%s %.1f\n", m, meas.pct)
		}
		return os.WriteFile(path, []byte(b.String()), 0o644)
	}

	// Existing file: rewrite line by line, preserving comments and blanks and
	// the file's original trailing-newline shape.
	lines := strings.Split(string(content), "\n")
	var out strings.Builder
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		switch {
		case trim == "", strings.HasPrefix(trim, "#"):
			out.WriteString(line)
		default:
			fields := strings.Fields(trim)
			if len(fields) >= 2 {
				if meas, ok := measured[fields[0]]; ok {
					old, _ := strconv.ParseFloat(fields[1], 64)
					newPct := old
					if meas.pct > newPct {
						newPct = meas.pct
					}
					fmt.Fprintf(&out, "%s %.1f", fields[0], newPct)
				} else {
					out.WriteString(line)
				}
			} else {
				out.WriteString(line)
			}
		}
		if i < len(lines)-1 {
			out.WriteString("\n")
		}
	}
	return os.WriteFile(path, []byte(out.String()), 0o644)
}

// runCoverageFloor is the subcommand entry: it wires coverageFloor to the real
// environment and a runGoTest that runs `go test -cover -coverprofile` for the
// module. Workspace modules (agent, auth, etc.) are run from inside their
// directory, since under go.work a bare module name is not a valid package
// path from the root. The root module (".") uses the gate's -run/-skip
// filters from internal/devtool/gatesurface and -short, matching `make test`,
// so fuzz-family tests are excluded from the coverage measurement.
func runCoverageFloor(args []string) int {
	cfg := coverageFloorConfig{
		args:   args,
		getenv: os.Getenv,
		stdout: os.Stdout,
		stderr: os.Stderr,
		runGoTest: func(module, profilePath string) error {
			dir := "."
			pkg := "./..."
			if module != "." {
				if info, err := os.Stat(module); err == nil && info.IsDir() {
					dir = module
				}
			}
			// Under go.work, -coverpkg=./... from the root module expands to
			// every workspace package including nested modules (agent, auth,
			// llm, etc.), inflating the denominator with code the root module's
			// own tests never run. Compute the coverpkg list from `go list ./...`
			// which, under go.work, lists only the current module's packages.
			coverpkg := "./..."
			if module == "." {
				listCmd := exec.Command("go", "list", "./...")
				listCmd.Dir = dir
				out, err := listCmd.Output()
				if err == nil {
					pkgs := strings.TrimSpace(string(out))
					if pkgs != "" {
						coverpkg = strings.ReplaceAll(pkgs, "\n", ",")
					}
				}
			}
			args := []string{"test",
				"-coverpkg=" + coverpkg,
				"-coverprofile=" + profilePath,
				"-run", gatesurface.TestRun,
				"-skip", gatesurface.FuzzTestSkip,
				"-short"}
			args = append(args, pkg)
			cmd := exec.Command("go", args...)
			cmd.Dir = dir
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
	}
	return coverageFloor(cfg)
}
