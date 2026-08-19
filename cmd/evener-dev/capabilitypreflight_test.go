//go:build linux || darwin

package main

// Tests for the `capability-preflight` subcommand of evener-dev, the Go port of
// scripts/gate/gate-capability-preflight.sh. RED phase: the production
// capabilityPreflight function these tests drive does not exist yet, so this
// file does not compile until the green phase adds it.
//
// The shell script's contract, ported faithfully:
//
//   - FAKE_GATE_PROBE_BLOCKED, when SET (even to the empty string), short-
//     circuits the real probe: every capability id named in it (space-
//     separated) is reported BLOCKED with a fixed reason/rerun, and every
//     capability id NOT named is reported AVAILABLE. The real probe is never
//     invoked in that mode. When UNSET, the real probe (here the injectable
//     probeOutput) is run and its tab-delimited lines are parsed.
//
//   - For each BLOCKED capability, the skip pattern is looked up via
//     internal/devtool/gatesurface.CapabilitySkipPattern. Non-empty patterns
//     are unioned into one pipe-separated skip regex, in capability order
//     (loopback-bind, chrome-cdp, process-inspect, git-cache), WITHOUT
//     deduplication — exactly what the shell's `${skip_regex}|${pattern}`
//     accumulation produces, so two blocked capabilities sharing one pattern
//     yield `pattern|pattern`.
//
//   - The skip regex is written to stdout as a single line for the Makefile to
//     capture via `$(shell ...)`. The human-readable summary is written to
//     stderr, one line per capability, in capability order.
//
//   - Exit 0 on a successful classification (even when capabilities are
//     blocked); exit 1 on an internal failure (the probe could not run, it
//     emitted an unrecognized status, or it never classified a known
//     capability).
//
// getenv is `func(string) (string, bool)` (LookupEnv-style) rather than
// `func(string) string` because the shell distinguishes "FAKE_GATE_PROBE_BLOCKED
// is set to empty" (fake mode, all available) from "FAKE_GATE_PROBE_BLOCKED is
// unset" (run the real probe) via `${VAR+set}`. os.Getenv collapses both to "",
// which would make the all-available selftest indistinguishable from the real
// path and break port fidelity. The bool is the set-ness.

import (
	"strings"
	"testing"
)

// capabilityID is the skip pattern the gate associates with loopback-bind and
// process-inspect (internal/devtool/gatesurface.CapabilitySkipPattern). Inlined
// here so the test is the spec, not a re-import of the production lookup.
const wantLoopbackSkipPattern = `^(TestE2E_|TestTUITmuxE2E_)`

// fixed capability order the probe reports, matching
// cmd/evener-gate-probe.AllCapabilityIDs and the shell's capability_ids.
var preflightCapabilityIDs = []string{
	"loopback-bind",
	"chrome-cdp",
	"process-inspect",
	"git-cache",
}

// fakeEnv returns a getenv for which only FAKE_GATE_PROBE_BLOCKED is defined.
// set=false models the variable being unset (the real-probe path); set=true
// with val="" models the variable set to the empty string (fake mode, nothing
// blocked).
func fakeEnv(val string, set bool) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if name == "FAKE_GATE_PROBE_BLOCKED" {
			return val, set
		}
		return "", false
	}
}

// mustNotCallProbe is a probeOutput stub that fails the test if the fake path
// ever delegates to the real probe. The whole point of FAKE_GATE_PROBE_BLOCKED
// is that the real probe is never run.
func mustNotCallProbe(t *testing.T) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		t.Fatalf("probeOutput must not be called when FAKE_GATE_PROBE_BLOCKED is set")
		return "", nil
	}
}

// fixtureProbe returns a probeOutput stub that yields fixed tab-delimited lines
// (the shape `go run ./cmd/evener-gate-probe` prints: id\tstatus\treason\trerun).
func fixtureProbe(lines string) func() (string, error) {
	return func() (string, error) { return lines, nil }
}

// errorProbe returns a probeOutput stub that fails, modelling the real
// `go run ./cmd/evener-gate-probe` exiting nonzero.
func errorProbe(errMsg string) func() (string, error) {
	return func() (string, error) {
		return errMsg, errProbeFailure
	}
}

// errProbeFailure is a sentinel so tests can assert the diagnostic carries the
// probe's own error text.
type probeErrSentinel struct{}

var errProbeFailure = probeErrSentinel{}

func (probeErrSentinel) Error() string { return "probe failed: simulated nonzero exit" }

// allAvailableFakeLines is the tab-delimited block the fake path must produce
// when nothing is blocked: every capability AVAILABLE, empty reason/rerun.
func allAvailableFakeLines() string {
	var b strings.Builder
	for _, id := range preflightCapabilityIDs {
		b.WriteString(id)
		b.WriteString("\tAVAILABLE\t\t\n")
	}
	return b.String()
}

// TestCapabilityPreflightAllAvailable: FAKE_GATE_PROBE_BLOCKED set to the empty
// string means fake mode is active and nothing is blocked. The skip regex is
// empty, every capability is reported AVAILABLE, the real probe is never run,
// and the command exits 0.
func TestCapabilityPreflightAllAvailable(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("", true), &stdout, &stderr, mustNotCallProbe(t))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if got := strings.TrimRight(stdout.String(), "\n"); got != "" {
		t.Errorf("skip regex = %q, want empty (nothing blocked)", got)
	}
	for _, id := range preflightCapabilityIDs {
		want := "AVAILABLE " + id
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr missing %q; got:\n%s", want, stderr.String())
		}
	}
	for _, id := range preflightCapabilityIDs {
		if strings.Contains(stderr.String(), "BLOCKED "+id) {
			t.Errorf("stderr should not report %s as BLOCKED; got:\n%s", id, stderr.String())
		}
	}
}

// TestCapabilityPreflightLoopbackBlocked: a single blocked capability with a
// non-empty skip pattern. The skip regex is exactly that pattern, the summary
// names the capability BLOCKED with the pattern and the rerun command, the
// other capabilities remain AVAILABLE, and exit is 0.
func TestCapabilityPreflightLoopbackBlocked(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("loopback-bind", true), &stdout, &stderr, mustNotCallProbe(t))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if got := strings.TrimRight(stdout.String(), "\n"); got != wantLoopbackSkipPattern {
		t.Errorf("skip regex = %q, want %q", got, wantLoopbackSkipPattern)
	}
	if !strings.Contains(stderr.String(), "BLOCKED loopback-bind:") {
		t.Errorf("stderr missing BLOCKED loopback-bind summary; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "forced blocked via FAKE_GATE_PROBE_BLOCKED (selftest)") {
		t.Errorf("stderr missing fake-mode reason; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "skips tests matching '"+wantLoopbackSkipPattern+"'") {
		t.Errorf("stderr missing skip-pattern mention; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "reprobe: go run ./cmd/evener-gate-probe -only=loopback-bind") {
		t.Errorf("stderr missing reprobe command; got:\n%s", stderr.String())
	}
	for _, id := range []string{"chrome-cdp", "process-inspect", "git-cache"} {
		if !strings.Contains(stderr.String(), "AVAILABLE "+id) {
			t.Errorf("stderr missing AVAILABLE %s; got:\n%s", id, stderr.String())
		}
	}
}

// TestCapabilityPreflightMultipleBlockedDuplicatesPattern: loopback-bind and
// process-inspect share the same skip pattern. The shell accumulates without
// deduplication, so the combined regex is `pattern|pattern` — ugly but exactly
// what the shell emits. The Go port matches the shell for port fidelity.
func TestCapabilityPreflightMultipleBlockedDuplicatesPattern(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(
		fakeEnv("loopback-bind process-inspect", true),
		&stdout, &stderr, mustNotCallProbe(t),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	wantRegex := wantLoopbackSkipPattern + "|" + wantLoopbackSkipPattern
	if got := strings.TrimRight(stdout.String(), "\n"); got != wantRegex {
		t.Errorf("skip regex = %q, want %q (shell does not deduplicate)", got, wantRegex)
	}
	if !strings.Contains(stderr.String(), "BLOCKED loopback-bind:") {
		t.Errorf("stderr missing BLOCKED loopback-bind; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "BLOCKED process-inspect:") {
		t.Errorf("stderr missing BLOCKED process-inspect; got:\n%s", stderr.String())
	}
	for _, id := range []string{"chrome-cdp", "git-cache"} {
		if !strings.Contains(stderr.String(), "AVAILABLE "+id) {
			t.Errorf("stderr missing AVAILABLE %s; got:\n%s", id, stderr.String())
		}
	}
}

// TestCapabilityPreflightChromeCDPBlockedNoPattern: chrome-cdp is BLOCKED but
// has no gate consumer, so CapabilitySkipPattern returns "". Nothing is added
// to the skip regex (it stays empty), and the summary says so explicitly
// rather than naming a skip pattern.
func TestCapabilityPreflightChromeCDPBlockedNoPattern(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("chrome-cdp", true), &stdout, &stderr, mustNotCallProbe(t))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if got := strings.TrimRight(stdout.String(), "\n"); got != "" {
		t.Errorf("skip regex = %q, want empty (chrome-cdp has no skip pattern)", got)
	}
	if !strings.Contains(stderr.String(), "BLOCKED chrome-cdp:") {
		t.Errorf("stderr missing BLOCKED chrome-cdp; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "no gate component currently depends on this") {
		t.Errorf("stderr missing no-consumer note; got:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "skips tests matching") {
		t.Errorf("stderr should not mention a skip pattern for chrome-cdp; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "reprobe: go run ./cmd/evener-gate-probe -only=chrome-cdp") {
		t.Errorf("stderr missing reprobe command; got:\n%s", stderr.String())
	}
	for _, id := range []string{"loopback-bind", "process-inspect", "git-cache"} {
		if !strings.Contains(stderr.String(), "AVAILABLE "+id) {
			t.Errorf("stderr missing AVAILABLE %s; got:\n%s", id, stderr.String())
		}
	}
}

// TestCapabilityPreflightParsesRealProbeOutput: with FAKE_GATE_PROBE_BLOCKED
// unset, the real (injected) probe is run and its tab-delimited output is
// parsed. This exercises the non-fake path: one capability BLOCKED with a
// pattern, the rest AVAILABLE, and the skip regex drawn from probe output
// rather than from the fake generator.
func TestCapabilityPreflightParsesRealProbeOutput(t *testing.T) {
	// Real probe shape: id\tstatus\treason\trerun, one per line, in capability
	// order. Here loopback-bind is blocked by a real bind failure; the others
	// are available with empty reason/rerun.
	probeLines := strings.Join([]string{
		"loopback-bind\tBLOCKED\tcannot bind 127.0.0.1:0: permission denied\tgo run ./cmd/evener-gate-probe -only=loopback-bind",
		"chrome-cdp\tAVAILABLE\t\t",
		"process-inspect\tAVAILABLE\t\t",
		"git-cache\tAVAILABLE\t\t",
	}, "\n") + "\n"

	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("", false), &stdout, &stderr, fixtureProbe(probeLines))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if got := strings.TrimRight(stdout.String(), "\n"); got != wantLoopbackSkipPattern {
		t.Errorf("skip regex = %q, want %q", got, wantLoopbackSkipPattern)
	}
	if !strings.Contains(stderr.String(), "BLOCKED loopback-bind:") {
		t.Errorf("stderr missing BLOCKED loopback-bind; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot bind 127.0.0.1:0: permission denied") {
		t.Errorf("stderr missing the real probe reason; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "skips tests matching '"+wantLoopbackSkipPattern+"'") {
		t.Errorf("stderr missing skip-pattern mention; got:\n%s", stderr.String())
	}
	for _, id := range []string{"chrome-cdp", "process-inspect", "git-cache"} {
		if !strings.Contains(stderr.String(), "AVAILABLE "+id) {
			t.Errorf("stderr missing AVAILABLE %s; got:\n%s", id, stderr.String())
		}
	}
}

// TestCapabilityPreflightRealProbeAllAvailable: the real-probe path with
// everything available produces an empty skip regex and an all-AVAILABLE
// summary, exit 0.
func TestCapabilityPreflightRealProbeAllAvailable(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("", false), &stdout, &stderr, fixtureProbe(allAvailableFakeLines()))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if got := strings.TrimRight(stdout.String(), "\n"); got != "" {
		t.Errorf("skip regex = %q, want empty", got)
	}
	for _, id := range preflightCapabilityIDs {
		if !strings.Contains(stderr.String(), "AVAILABLE "+id) {
			t.Errorf("stderr missing AVAILABLE %s; got:\n%s", id, stderr.String())
		}
	}
}

// TestCapabilityPreflightProbeErrorExitsOne: when the real probe itself fails
// (nonzero exit, modelled by probeOutput returning an error), the preflight
// reports an internal failure to stderr and exits 1 — it does not guess.
func TestCapabilityPreflightProbeErrorExitsOne(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("", false), &stdout, &stderr, errorProbe("go run: exit status 2"))

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (probe failure is an internal failure); stderr:\n%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "AVAILABLE") || strings.Contains(stderr.String(), "BLOCKED loopback-bind") {
		t.Errorf("stderr should not present a classification on probe failure; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "go run: exit status 2") {
		t.Errorf("stderr should include the probe's own error text; got:\n%s", stderr.String())
	}
	if got := strings.TrimRight(stdout.String(), "\n"); got != "" {
		t.Errorf("skip regex = %q, want empty on internal failure", got)
	}
}

// TestCapabilityPreflightUnrecognizedStatusExitsOne: probe output with a status
// that is neither AVAILABLE nor BLOCKED is unparseable. The shell emits_fatal;
// the Go port exits 1 with a diagnostic naming the offending line.
func TestCapabilityPreflightUnrecognizedStatusExitsOne(t *testing.T) {
	badLines := strings.Join([]string{
		"loopback-bind\tAVAILABLE\t\t",
		"chrome-cdp\tWAT\treason\trerun",
		"process-inspect\tAVAILABLE\t\t",
		"git-cache\tAVAILABLE\t\t",
	}, "\n") + "\n"

	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("", false), &stdout, &stderr, fixtureProbe(badLines))

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (unrecognized status); stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "chrome-cdp") {
		t.Errorf("stderr should name the offending capability; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "WAT") {
		t.Errorf("stderr should name the unrecognized status; got:\n%s", stderr.String())
	}
}

// TestCapabilityPreflightMissingCapabilityExitsOne: the probe must classify
// every known capability. If one is missing from probe output, the preflight
// treats it as an internal failure and exits 1, mirroring the shell's
// "the probe never classified $id" fatal.
func TestCapabilityPreflightMissingCapabilityExitsOne(t *testing.T) {
	// git-cache is absent from the probe output.
	dropped := strings.Join([]string{
		"loopback-bind\tAVAILABLE\t\t",
		"chrome-cdp\tAVAILABLE\t\t",
		"process-inspect\tAVAILABLE\t\t",
	}, "\n") + "\n"

	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("", false), &stdout, &stderr, fixtureProbe(dropped))

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (missing capability); stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "git-cache") {
		t.Errorf("stderr should name the unclassified capability git-cache; got:\n%s", stderr.String())
	}
}
