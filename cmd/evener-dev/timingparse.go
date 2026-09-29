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
		var b strings.Builder
		for i, pkg := range missing {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "package %s listed by go list produced no terminal event in the go test -json stream", pkg)
		}
		return nil, errors.New(b.String())
	}
	return rows, nil
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
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "usage: evener-dev timing-parse --json FILE --packages FILE\n\n"+
			"Print the test-timing-budget ratchet's rows for one module's go test -json\n"+
			"stream: PKG/TEST rows as results arrive, SUM rows from each package's own\n"+
			"wall time. Fails when a package in the inventory never ends.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *jsonPath == "" || *pkgPath == "" {
		fs.Usage()
		return 2
	}
	f, err := os.Open(*jsonPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev timing-parse: %v\n", err)
		return 1
	}
	defer func() { _ = f.Close() }()
	expected, err := readTimingPackages(*pkgPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev timing-parse: %v\n", err)
		return 1
	}
	rows, err := parseGoTestJSON(f, expected)
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
