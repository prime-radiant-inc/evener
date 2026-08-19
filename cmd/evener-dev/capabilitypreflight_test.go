package main

// Tests for the `capability-preflight` subcommand of evener-dev.
//
// The contract, ported from scripts/gate/gate-capability-preflight.sh:
//
//   - FAKE_GATE_PROBE_BLOCKED, when SET (even to the empty string), short-
//     circuits the real probe: every capability id named in it (space-
//     separated) is reported BLOCKED with a fixed reason/rerun, and every
//     capability id NOT named is reported AVAILABLE. The real probe is never
//     invoked in that mode. When UNSET, the real (injected) classify function
//     is run and its []Capability results are consumed directly.
//
//   - For each BLOCKED capability, the skip pattern is looked up via
//     internal/devtool/gatesurface.CapabilitySkipPattern. Non-empty patterns
//     are unioned into one pipe-separated skip regex, in capability order
//     (loopback-bind, chrome-cdp, process-inspect, git-cache), WITHOUT
//     deduplication.
//
//   - The skip regex is written to stdout as a single line for the Makefile to
//     capture. The human-readable summary is written to stderr, one line per
//     capability, in capability order.
//
//   - Exit 0 on a successful classification (even when capabilities are
//     blocked); exit 1 on an internal failure (the probe never classified a
//     known capability).

import (
	"strings"
	"testing"

	"primeradiant.com/evener/internal/devtool/capabilityprobe"
)

// capabilityID is the skip pattern the gate associates with loopback-bind and
// process-inspect (internal/devtool/gatesurface.CapabilitySkipPattern). Inlined
// here so the test is the spec, not a re-import of the production lookup.
const wantLoopbackSkipPattern = `^(TestE2E_|TestTUITmuxE2E_)`

// fixed capability order the probe reports, matching
// capabilityprobe.AllCapabilityIDs.
var preflightCapabilityIDs = capabilityprobe.AllCapabilityIDs

// fakeEnv returns a getenv for which only FAKE_GATE_PROBE_BLOCKED is defined.
func fakeEnv(val string, set bool) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if name == "FAKE_GATE_PROBE_BLOCKED" {
			return val, set
		}
		return "", false
	}
}

// mustNotCallClassify is a classifyFn stub that fails the test if the fake path
// ever delegates to the real probe.
func mustNotCallClassify(t *testing.T) classifyFn {
	t.Helper()
	return func() []capabilityprobe.Capability {
		t.Fatalf("classify must not be called when FAKE_GATE_PROBE_BLOCKED is set")
		return nil
	}
}

// allAvailableClassify returns a classifyFn that reports every capability as
// available.
func allAvailableClassify() classifyFn {
	return func() []capabilityprobe.Capability {
		caps := make([]capabilityprobe.Capability, len(preflightCapabilityIDs))
		for i, id := range preflightCapabilityIDs {
			caps[i] = capabilityprobe.Capability{ID: id, Available: true}
		}
		return caps
	}
}

// fixtureClassify returns a classifyFn that yields fixed Capability results.
func fixtureClassify(caps []capabilityprobe.Capability) classifyFn {
	return func() []capabilityprobe.Capability { return caps }
}

// TestCapabilityPreflightAllAvailable: FAKE_GATE_PROBE_BLOCKED set to the empty
// string means fake mode is active and nothing is blocked.
func TestCapabilityPreflightAllAvailable(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("", true), &stdout, &stderr, mustNotCallClassify(t))

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
}

// TestCapabilityPreflightLoopbackBlocked: a single blocked capability with a
// non-empty skip pattern.
func TestCapabilityPreflightLoopbackBlocked(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("loopback-bind", true), &stdout, &stderr, mustNotCallClassify(t))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if got := strings.TrimRight(stdout.String(), "\n"); got != wantLoopbackSkipPattern {
		t.Errorf("skip regex = %q, want %q", got, wantLoopbackSkipPattern)
	}
	if !strings.Contains(stderr.String(), "BLOCKED loopback-bind:") {
		t.Errorf("stderr missing BLOCKED loopback-bind summary; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "forced blocked via FAKE_GATE_PROBE_BLOCKED (test)") {
		t.Errorf("stderr missing fake-mode reason; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "skips tests matching '"+wantLoopbackSkipPattern+"'") {
		t.Errorf("stderr missing skip-pattern mention; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "reprobe: go run ./cmd/evener-dev capability-preflight -only=loopback-bind") {
		t.Errorf("stderr missing reprobe command; got:\n%s", stderr.String())
	}
	for _, id := range []string{"chrome-cdp", "process-inspect", "git-cache"} {
		if !strings.Contains(stderr.String(), "AVAILABLE "+id) {
			t.Errorf("stderr missing AVAILABLE %s; got:\n%s", id, stderr.String())
		}
	}
}

// TestCapabilityPreflightMultipleBlockedDuplicatesPattern: loopback-bind and
// process-inspect share the same skip pattern. The regex is pattern|pattern
// (no deduplication, matching the shell's accumulation).
func TestCapabilityPreflightMultipleBlockedDuplicatesPattern(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(
		fakeEnv("loopback-bind process-inspect", true),
		&stdout, &stderr, mustNotCallClassify(t),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	wantRegex := wantLoopbackSkipPattern + "|" + wantLoopbackSkipPattern
	if got := strings.TrimRight(stdout.String(), "\n"); got != wantRegex {
		t.Errorf("skip regex = %q, want %q (no deduplication)", got, wantRegex)
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
// has no gate consumer, so CapabilitySkipPattern returns "".
func TestCapabilityPreflightChromeCDPBlockedNoPattern(t *testing.T) {
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("chrome-cdp", true), &stdout, &stderr, mustNotCallClassify(t))

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
	for _, id := range []string{"loopback-bind", "process-inspect", "git-cache"} {
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
	code := capabilityPreflight(fakeEnv("", false), &stdout, &stderr, allAvailableClassify())

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

// TestCapabilityPreflightRealProbeLoopbackBlocked: the real-probe path with
// one blocked capability produces the correct skip regex and summary.
func TestCapabilityPreflightRealProbeLoopbackBlocked(t *testing.T) {
	caps := []capabilityprobe.Capability{
		{ID: "loopback-bind", Available: false, Reason: "cannot bind 127.0.0.1:0: permission denied", Rerun: "go run ./cmd/evener-dev capability-preflight -only=loopback-bind"},
		{ID: "chrome-cdp", Available: true},
		{ID: "process-inspect", Available: true},
		{ID: "git-cache", Available: true},
	}
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("", false), &stdout, &stderr, fixtureClassify(caps))

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
	for _, id := range []string{"chrome-cdp", "process-inspect", "git-cache"} {
		if !strings.Contains(stderr.String(), "AVAILABLE "+id) {
			t.Errorf("stderr missing AVAILABLE %s; got:\n%s", id, stderr.String())
		}
	}
}

// TestCapabilityPreflightMissingCapabilityExitsOne: the probe must classify
// every known capability. If one is missing from probe output, the preflight
// treats it as an internal failure and exits 1.
func TestCapabilityPreflightMissingCapabilityExitsOne(t *testing.T) {
	// git-cache is absent from the probe output.
	caps := []capabilityprobe.Capability{
		{ID: "loopback-bind", Available: true},
		{ID: "chrome-cdp", Available: true},
		{ID: "process-inspect", Available: true},
	}
	var stdout, stderr strings.Builder
	code := capabilityPreflight(fakeEnv("", false), &stdout, &stderr, fixtureClassify(caps))

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (missing capability); stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "git-cache") {
		t.Errorf("stderr should name the unclassified capability git-cache; got:\n%s", stderr.String())
	}
}
