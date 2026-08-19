//go:build linux || darwin

// Package main is the home of evener-dev's subcommands. This file implements
// `capability-preflight`, formerly scripts/gate/gate-capability-preflight.sh.
//
// The preflight classifies the sandbox-sensitive host capabilities the gate's
// live/e2e test components depend on (loopback binds, Chrome/CDP, process
// inspection, git cache) via internal/devtool/capabilityprobe, then builds a
// combined skip regex from internal/devtool/gatesurface's skip patterns for
// each BLOCKED capability. The skip regex goes to stdout (one line, empty
// when nothing is blocked) for the Makefile to capture; the human-readable
// summary goes to stderr, one line per capability in fixed order.
//
// FAKE_GATE_PROBE_BLOCKED (when SET, even to the empty string) short-circuits
// the real probe: every id named in the value (space-separated) is BLOCKED
// with a fixed reason, every id not named is AVAILABLE. This is the test
// path; the real path calls capabilityprobe.Classify.
//
// Exit 0 on a successful classification, 1 on an internal failure.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"primeradiant.com/evener/internal/devtool/capabilityprobe"
	"primeradiant.com/evener/internal/devtool/gatesurface"
)

// gateCapabilityIDs is the fixed capability order the preflight reports.
// Output (summary and skip-regex accumulation) follows this order so two
// runs classify identically. Matches capabilityprobe.AllCapabilityIDs.
var gateCapabilityIDs = capabilityprobe.AllCapabilityIDs

// classifyFn is the shape of the real probe call: it returns one Capability
// per gateCapabilityIDs entry, in that fixed order. Injected so tests can
// drive the parse path without running real probes.
type classifyFn func() []capabilityprobe.Capability

// capabilityPreflight is the port of scripts/gate/gate-capability-preflight.sh.
//
// getenv is LookupEnv-style (value, set) rather than Getenv-style because the
// shell distinguishes "FAKE_GATE_PROBE_BLOCKED set to empty" (fake mode,
// nothing blocked) from "FAKE_GATE_PROBE_BLOCKED unset" (run the real probe)
// via `${VAR+set}`; os.Getenv collapses both to "", which would make the
// all-available selftest indistinguishable from the real path.
func capabilityPreflight(getenv func(string) (string, bool), stdout io.Writer, stderr io.Writer, classify classifyFn) int {
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
					reason:  "forced blocked via FAKE_GATE_PROBE_BLOCKED (test)",
					rerun:   "go run ./cmd/evener-dev capability-preflight -only=" + id,
				}
			} else {
				classified[id] = entry{blocked: false}
			}
		}
	} else {
		// Real probe path: call the injected classify function (in production,
		// capabilityprobe.Classify).
		caps := classify()
		for _, c := range caps {
			classified[c.ID] = entry{
				blocked: !c.Available,
				reason:  c.Reason,
				rerun:   c.Rerun,
			}
		}
		// The probe must classify every known capability. A missing one is an
		// internal failure.
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
// to the real environment and capabilityprobe.Classify.
func runCapabilityPreflight(args []string) int {
	classify := func() []capabilityprobe.Capability {
		return capabilityprobe.Classify(context.Background())
	}
	return capabilityPreflight(os.LookupEnv, os.Stdout, os.Stderr, classify)
}
