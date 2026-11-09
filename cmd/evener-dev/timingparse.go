package dev

// timing-parse turns one module's `go test -json` stream into the timing rows
// the test-timing-budget ratchet reads. It is the parser half of
// scripts/gate/test-timing-budget.sh, ported to Go so it can be tested directly
// against JSON stream strings instead of through a faked toolchain
// (docs/developing-evener/testing.md's port-on-touch rule).
//
// The rows are tab-separated, one per line, and mirror the contract the shell
// ratchet has always read:
//
//	PKG  <package>                     one per package terminal event
//	TEST <package> <name> <seconds>    one per test AND subtest result
//	SUM  <package> <seconds>           the package's own wall time
//
// The SUM is the package-level terminal event's Elapsed field — the package's
// actual wall time — not the sum of its tests' Elapsed values. Under t.Parallel
// those tests overlap, so summing them counts the same wall-clock second more
// than once and tracks contention rather than work (issue #172). The per-test
// and per-subtest TEST rows still feed the ratchet's per-test ceiling.
//
// A package `go list` reported that never produces a terminal event is the
// silent-drop shape issue #172 filed: a build-failed package contributes no
// rows, so a later --bless would write a budget without it. The parser refuses
// that stream rather than parse around it.

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// timingEvent is the subset of `go test -json`'s event shape this reads. A
// missing Elapsed is indistinguishable from 0.0 for a test, but for a package
// terminal event it is the difference between "measured 0s" and "no duration
// reported", so it is a pointer.
type timingEvent struct {
	Action  string
	Package string
	Test    string
	Elapsed *float64
}

// timingTerminalActions are the actions that end a test's or package's run.
// Everything else — run, start, pause, cont, output — is progress, not a
// result, and must not be read as a terminal event: a package seen only on
// `start` has reported no duration and no outcome (issue #172).
var timingTerminalActions = map[string]bool{"pass": true, "fail": true, "skip": true}

// missingTerminalEventError names the expected packages a `go test -json`
// stream never ended, one line per package.
type missingTerminalEventError struct {
	packages []string
}

func (e *missingTerminalEventError) Error() string {
	var b strings.Builder
	for i, pkg := range e.packages {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "package %s listed by go list produced no terminal event in the go test -json stream", pkg)
	}
	return b.String()
}

// parseGoTestJSON reads a `go test -json` stream and returns the ratchet's rows,
// or an error naming every package in expected that the stream never ended.
func parseGoTestJSON(r io.Reader, expected []string) ([]string, error) {
	var rows []string
	seen := map[string]bool{}
	pkgSeconds := map[string]float64{}

	// bufio.Reader, not Scanner: a Scanner caps a line at its buffer and would
	// fail the whole measurement on one oversized `Output` event (go test -json
	// embeds each output line in one), when an unreadable line is no worse than
	// a malformed one. ReadString has no such cap; stream completeness is proven
	// by the package inventory, not by every line being small.
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		line, readErr := reader.ReadString('\n')
		if line = strings.TrimSpace(line); line != "" {
			var ev timingEvent
			// A truncated or malformed line is not a measurement.
			if err := json.Unmarshal([]byte(line), &ev); err == nil &&
				timingTerminalActions[ev.Action] && ev.Package != "" {
				if ev.Test == "" {
					// Package-level terminal event: its Elapsed is the package's
					// own wall time, the metric the budget compares.
					seen[ev.Package] = true
					rows = append(rows, "PKG\t"+ev.Package)
					if ev.Elapsed != nil {
						pkgSeconds[ev.Package] = *ev.Elapsed
					}
				} else {
					elapsed := 0.0
					if ev.Elapsed != nil {
						elapsed = *ev.Elapsed
					}
					rows = append(rows, "TEST\t"+ev.Package+"\t"+ev.Test+"\t"+formatSeconds(elapsed))
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("reading go test -json stream: %w", readErr)
		}
	}

	pkgs := make([]string, 0, len(pkgSeconds))
	for pkg := range pkgSeconds {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		rows = append(rows, "SUM\t"+pkg+"\t"+formatSeconds(pkgSeconds[pkg]))
	}

	var missing []string
	for _, pkg := range expected {
		if pkg != "" && !seen[pkg] {
			missing = append(missing, pkg)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &missingTerminalEventError{packages: missing}
	}
	return rows, nil
}

// vitestReport is the subset of vitest's Jest-compatible JSON reporter output
// the ratchet reads. Durations are milliseconds. The lists stay raw so a
// literal null (a malformed report) can be told from an absent key.
type vitestReport struct {
	TestResults json.RawMessage `json:"testResults"` //nolint:tagliatelle // vitest's reporter spells the key in camelCase
}

type vitestSuite struct {
	AssertionResults json.RawMessage `json:"assertionResults"` //nolint:tagliatelle // vitest's reporter spells the key in camelCase
}

type vitestAssertion struct {
	FullName string   `json:"fullName"` //nolint:tagliatelle // vitest's reporter spells the key in camelCase
	Title    string   `json:"title"`
	Duration *float64 `json:"duration"`
}

// decodeVitestList decodes an optional JSON array of objects. An absent key is
// an empty list; a null list or a null entry is a malformed report, which must
// not measure as a clean, smaller frontend.
func decodeVitestList[T any](raw json.RawMessage, what string) ([]*T, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var items []*T
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, fmt.Errorf("vitest report: %s is not a list of objects", what)
	}
	for _, item := range items {
		if item == nil {
			return nil, fmt.Errorf("vitest report: %s has a null entry", what)
		}
	}
	return items, nil
}

// parseVitestJSON rolls the whole frontend suite up under one package key,
// "web": vitest has no per-package shape the way a Go module does. Each
// assertion is a TEST row and the SUM is their total, in seconds. Whitespace in
// a name is flattened so a tab or newline cannot split a row and make the
// comparator read a truncated line.
func parseVitestJSON(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading vitest report: %w", err)
	}
	// Unmarshal, not a stream decode: content after the first document is a
	// malformed report, not a smaller one.
	var report *vitestReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("reading vitest report: %w", err)
	}
	if report == nil {
		return nil, errors.New("vitest report: the document is null")
	}
	suites, err := decodeVitestList[vitestSuite](report.TestResults, "testResults")
	if err != nil {
		return nil, err
	}
	var rows []string
	total := 0.0
	for _, suite := range suites {
		assertions, err := decodeVitestList[vitestAssertion](suite.AssertionResults, "assertionResults")
		if err != nil {
			return nil, err
		}
		for _, a := range assertions {
			seconds := 0.0
			if a.Duration != nil {
				seconds = *a.Duration / 1000
			}
			name := a.FullName
			if name == "" {
				name = a.Title
			}
			if name = strings.Join(strings.Fields(name), " "); name == "" {
				name = "(unnamed)"
			}
			rows = append(rows, "TEST\tweb\t"+name+"\t"+formatSeconds(seconds))
			total += seconds
		}
	}
	return append(rows, "SUM\tweb\t"+formatSeconds(total)), nil
}

// formatSeconds writes a duration the way the ratchet's Python comparator has
// always read it: the shortest decimal that round-trips.
func formatSeconds(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// readTimingPackages reads the `go list ./...` inventory: one import path per
// line, blank lines skipped.
func readTimingPackages(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var pkgs []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			pkgs = append(pkgs, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return pkgs, nil
}

func timingParseMain(args []string) int {
	return runTimingParse(args, os.Stdout, os.Stderr)
}

func runTimingParse(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("timing-parse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonPath := fs.String("json", "", "path to the go test -json stream")
	pkgPath := fs.String("packages", "", "path to the go list package inventory, one per line")
	vitestPath := fs.String("vitest", "", "path to the vitest JSON report; prints the frontend's rows instead")
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "usage: evener-dev timing-parse --json FILE --packages FILE\n"+
			"       evener-dev timing-parse --vitest FILE\n\n"+
			"Print the test-timing-budget ratchet's rows for one module's go test -json\n"+
			"stream: PKG/TEST rows as results arrive, SUM rows from each package's own\n"+
			"wall time. Fails when a package in the inventory never ends. With --vitest,\n"+
			"print the frontend's TEST rows and one SUM row under the key web.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var rows []string
	var err error
	switch {
	case *vitestPath != "" && *jsonPath == "" && *pkgPath == "":
		rows, err = readRows(*vitestPath, func(f *os.File) ([]string, error) { return parseVitestJSON(f) })
	case *vitestPath == "" && *jsonPath != "" && *pkgPath != "":
		rows, err = readRows(*jsonPath, func(f *os.File) ([]string, error) {
			expected, err := readTimingPackages(*pkgPath)
			if err != nil {
				return nil, err
			}
			return parseGoTestJSON(f, expected)
		})
	default:
		fs.Usage()
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev timing-parse: %v\n", err)
		return 1
	}
	for _, row := range rows {
		if _, err := io.WriteString(stdout, row+"\n"); err != nil {
			_, _ = fmt.Fprintf(stderr, "evener-dev timing-parse: writing rows: %v\n", err)
			return 1
		}
	}
	return 0
}

// readRows opens path and hands it to parse.
func readRows(path string, parse func(*os.File) ([]string, error)) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parse(f)
}
