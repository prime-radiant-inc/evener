//go:build linux || darwin

// Package main is the home of evener-dev's subcommands. This file implements
// `capability-preflight`, the Go port of scripts/gate/gate-capability-preflight.sh.
//
// The port keeps the shell's contract verbatim: FAKE_GATE_PROBE_BLOCKED (when
// SET, even to the empty string) short-circuits the real probe; otherwise the
// real probe (here the injectable probeOutput) is run and its tab-delimited
// lines are parsed. Each BLOCKED capability's skip pattern is looked up via
// internal/devtool/gatesurface.CapabilitySkipPattern and unioned into one
// pipe-separated skip regex WITHOUT deduplication, exactly as the shell's
// `${skip_regex}|${pattern}` accumulation produces. The skip regex goes to
// stdout (one line, empty when nothing is blocked) for the Makefile to
// capture; the human-readable summary goes to stderr, one line per
// capability in fixed order. Exit 0 on a successful classification, 1 on an
// internal failure (probe could not run, unrecognized status, or a known
// capability the probe never classified).
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"primeradiant.com/evener/internal/devtool/gatesurface"
)

// gateCapabilityIDs is the fixed capability order the probe reports,
// matching cmd/evener-gate-probe.AllCapabilityIDs and the shell's
// capability_ids. Output (summary and skip-regex accumulation) follows this
// order so two runs classify identically.
var gateCapabilityIDs = []string{
	"loopback-bind",
	"chrome-cdp",
	"process-inspect",
	"git-cache",
}

// probeOutput is the shape of the real probe call: it returns the probe's
// tab-delimited stdout (id\tstatus\treason\trerun, one per line) or an error
// when the probe itself could not run. Injected so tests can drive the
// parse path without spawning `go run`.
type probeOutput func() (string, error)

// capabilityPreflight is the port of scripts/gate/gate-capability-preflight.sh.
//
// getenv is LookupEnv-style (value, set) rather than Getenv-style because the
// shell distinguishes "FAKE_GATE_PROBE_BLOCKED set to empty" (fake mode,
// nothing blocked) from "FAKE_GATE_PROBE_BLOCKED unset" (run the real probe)
// via `${VAR+set}`; os.Getenv collapses both to "", which would make the
// all-available selftest indistinguishable from the real path.
func capabilityPreflight(getenv func(string) (string, bool), stdout io.Writer, stderr io.Writer, probe probeOutput) int {
	type entry struct {
		blocked bool
		reason  string // empty when available
		rerun   string // exact command to re-probe just this capability
	}
	classified := make(map[string]entry, len(gateCapabilityIDs))

	if val, set := getenv("FAKE_GATE_PROBE_BLOCKED"); set {
		// Fake mode: the real probe is never invoked. Every id named in val
		// (space-separated) is BLOCKED with a fixed reason/rerun; every id not
		// named is AVAILABLE.
		blocked := make(map[string]bool)
		for _, id := range strings.Fields(val) {
			blocked[id] = true
		}
		for _, id := range gateCapabilityIDs {
			if blocked[id] {
				classified[id] = entry{
					blocked: true,
					reason:  "forced blocked via FAKE_GATE_PROBE_BLOCKED (selftest)",
					rerun:   "go run ./cmd/evener-gate-probe -only=" + id,
				}
			} else {
				classified[id] = entry{blocked: false}
			}
		}
	} else {
		// Real probe path.
		out, err := probe()
		if err != nil {
			// Mirror the shell's emit_fatal, which includes the probe's own
			// captured output ($lines) so the diagnostic carries the real
			// tool's error text rather than just a generic wrapper.
			fmt.Fprintf(stderr, "capability-preflight: probe failed: %v: %s\n", err, strings.TrimRight(out, "\n"))
			return 1
		}
		sc := bufio.NewScanner(strings.NewReader(out))
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				continue
			}
			fields := strings.Split(line, "\t")
			id := fields[0]
			status := ""
			if len(fields) > 1 {
				status = fields[1]
			}
			reason := ""
			if len(fields) > 2 {
				reason = fields[2]
			}
			// `read -r id status reason rerun` puts the remainder of the line
			// (everything after the third tab) into the last variable, so a
			// rerun containing a tab would be preserved; join reproduces that.
			rerun := ""
			if len(fields) > 3 {
				rerun = strings.Join(fields[3:], "\t")
			}
			switch status {
			case "AVAILABLE":
				classified[id] = entry{blocked: false}
			case "BLOCKED":
				classified[id] = entry{blocked: true, reason: reason, rerun: rerun}
			default:
				fmt.Fprintf(stderr, "capability-preflight: unrecognized status %q for capability %q in probe output: %q\n", status, id, line)
				return 1
			}
		}
		// The probe must classify every known capability. A missing one is an
		// internal failure, mirroring the shell's "the probe never classified
		// $id" fatal.
		for _, id := range gateCapabilityIDs {
			if _, ok := classified[id]; !ok {
				fmt.Fprintf(stderr, "capability-preflight: probe never classified capability %q\n", id)
				return 1
			}
		}
	}

	// Build the combined skip regex in capability order, without
	// deduplication: the shell's `${skip_regex}|${pattern}` accumulation
	// emits `pattern|pattern` when two blocked capabilities share a pattern,
	// and the port matches that for fidelity.
	skipRegex := ""
	for _, id := range gateCapabilityIDs {
		e := classified[id]
		if !e.blocked {
			continue
		}
		pattern := gatesurface.CapabilitySkipPattern(id)
		if pattern == "" {
			continue
		}
		if skipRegex == "" {
			skipRegex = pattern
		} else {
			skipRegex = skipRegex + "|" + pattern
		}
	}

	// The skip regex goes to stdout as a single line for the Makefile to
	// capture via `$(shell ...)`. Empty when nothing is blocked.
	fmt.Fprintln(stdout, skipRegex)

	// The human-readable summary goes to stderr, one line per capability in
	// fixed order.
	for _, id := range gateCapabilityIDs {
		e := classified[id]
		if !e.blocked {
			fmt.Fprintf(stderr, "AVAILABLE %s\n", id)
			continue
		}
		pattern := gatesurface.CapabilitySkipPattern(id)
		if pattern != "" {
			fmt.Fprintf(stderr,
				"BLOCKED %s: %s -- skips tests matching '%s'; rerun once fixed: ROOT_FULL=1 go test ./... -run '%s' -v; reprobe: %s\n",
				id, e.reason, pattern, pattern, e.rerun)
		} else {
			fmt.Fprintf(stderr,
				"BLOCKED %s: %s -- no gate component currently depends on this; reprobe: %s\n",
				id, e.reason, e.rerun)
		}
	}

	return 0
}

// runCapabilityPreflight is the subcommand entry: it wires capabilityPreflight
// to the real environment and a probeOutput that runs `go run
// ./cmd/evener-gate-probe` from the repo root, exactly as the shell did.
func runCapabilityPreflight(args []string) int {
	probe := func() (string, error) {
		cmd := exec.Command("go", "run", "./cmd/evener-gate-probe")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		return out.String(), err
	}
	return capabilityPreflight(os.LookupEnv, os.Stdout, os.Stderr, probe)
}
