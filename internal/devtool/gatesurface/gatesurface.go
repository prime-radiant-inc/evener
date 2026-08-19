// Package gatesurface is the single definition of which tests the
// deterministic non-fuzz gate runs, shared by the gate itself and by the
// coverage ratchet that must measure the same surface. It is a Go port of
// scripts/lib/gate-surface-lib.sh.
//
// This lives in one file because the alternative is two copies that drift. A
// ratchet measuring a different surface than the gate proves is worse than no
// ratchet: the number moves for reasons no one can attribute, and the floor
// ends up blessed against a surface no gate reproduces.
package gatesurface

// FuzzTestSkip is the regex matching fuzz-designated Test* functions excluded
// from the regular deterministic gate. Native Fuzz* targets are already
// excluded by -run; these names cover rapid/sequence fuzz tests and
// structured-generator reachability proofs that remain under make fuzz.
//
// Ported verbatim from scripts/lib/gate-surface-lib.sh's GATE_FUZZ_TEST_SKIP.
var FuzzTestSkip = `(SeqFuzz|SchemaFuzz|Structured.*Reach|LifecycleAdapter|ToolArgsAdapter|SeqAdapter|TurnPagingEquivalenceSanity|WireTypeRegistryCoverage|LineWindowExtractorsSanity|WriteListRoundTrip|LaunchConfigThreeStateRoundTrip|DifferentialSanity|StreamVsNonStreamSanity|FuzzBuildEnforces)`

// TestRun is the regex for which tests the gate runs. The gate runs ordinary
// Test/Example functions only; without this filter `go test` also executes
// every native Fuzz target's committed seed corpus, which is make fuzz's job,
// not the default gate's.
//
// Ported verbatim from scripts/lib/gate-surface-lib.sh's GATE_TEST_RUN.
var TestRun = `^(Test|Example)`

// CapabilitySkipPattern returns the known test-name pattern to skip for the
// given sandbox capability id, or "" when that capability has no known
// consumer (nothing is skipped).
//
// evener-dev capability-preflight classifies four sandbox-sensitive host
// capabilities ONCE, up front: loopback binds, a Chrome/Chromium binary,
// process inspection via `ps`, and a writable external git cache directory. Any
// it finds blocked feeds this registry to decide which KNOWN test-name pattern
// to skip instead of letting those tests fail into a denied bind/exec/write.
//
// Evidenced mapping, root module ONLY:
//   - loopback-bind, process-inspect: cmd/evener-hub's TestE2E_* family and
//     cmd/evener-tui's TestTUITmuxE2E_* family are the root module's ONLY test
//     files that both (a) run only under ROOT_FULL=1 (gated by testing.Short();
//     merge-approval-gate is the only caller that sets it) and (b) spawn a real
//     hub/daemon or tmux pane bound to a real loopback port. Ordinary `make
//     test` never reaches them.
//   - chrome-cdp, git-cache: no test file anywhere in this tree consumes
//     either today. test-web-browser needs Chrome but is a separate, non-gate
//     target; the only thing in the tree that names the fixed /tmp/git-cache
//     path is internal/devtool/capabilityprobe's own default, which the probe
//     creates in order to prove it is writable. Both are still probed and reported for
//     completeness and honesty; the pattern is empty because nothing is
//     skipped yet.
//
// Applied to the ROOT module ONLY, deliberately: agent/session_escalation_e2e_test.go
// (Linux-only, unrelated sandbox-escalation coverage) also has two
// TestE2E_*-named tests that share nothing with the cmd/evener-hub family
// except the prefix. Unioning this pattern into every module's -skip would
// silently and wrongly skip those two whenever loopback-bind or
// process-inspect is blocked.
func CapabilitySkipPattern(capabilityID string) string {
	switch capabilityID {
	case "loopback-bind", "process-inspect":
		return `^(TestE2E_|TestTUITmuxE2E_)`
	default:
		// chrome-cdp, git-cache, and any unrecognized id all yield "" (nothing
		// is skipped).
		return ""
	}
}
