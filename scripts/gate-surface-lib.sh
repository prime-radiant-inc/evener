#!/usr/bin/env bash
# gate-surface-lib.sh — the single definition of WHICH tests the deterministic
# non-fuzz gate runs, shared by the gate itself (run-module-tests.sh) and by the
# coverage ratchet that must measure the same surface (test-coverage-floor.sh).
#
# Sourced, never executed. Deliberately pure declaration: it sets variables
# and defines pure lookup functions and does nothing else (no file writes, no
# process launches), so it is safe for the dev-tooling wave's leak check.
#
# This lives in one file because the alternative is two copies that drift. A
# ratchet measuring a different surface than the gate proves is worse than no
# ratchet: the number moves for reasons no one can attribute, and the floor
# ends up blessed against a surface no gate reproduces.

# Fuzz-designated Test* functions are not part of the regular gate. Native Fuzz*
# targets are already excluded by -run; these names cover rapid/sequence fuzz
# tests and structured-generator reachability proofs that remain under make fuzz.
GATE_FUZZ_TEST_SKIP='(SeqFuzz|SchemaFuzz|Structured.*Reach|LifecycleAdapter|ToolArgsAdapter|SeqAdapter|TurnPagingEquivalenceSanity|WireTypeRegistryCoverage|LineWindowExtractorsSanity|TranscriptReadersAgreeSanity|WriteListRoundTrip|LaunchConfigThreeStateRoundTrip|DifferentialSanity|StreamVsNonStreamSanity|FuzzBuildEnforces)'

# The gate runs ordinary Test/Example functions only. Without this filter
# `go test` also executes every native Fuzz target's committed seed corpus,
# which is make fuzz's job, not the default gate's.
GATE_TEST_RUN='^(Test|Example)'

# --- sandbox capability skip registry (kata 5gvk) ---------------------------
#
# scripts/gate-capability-preflight.sh classifies four sandbox-sensitive host
# capabilities ONCE, up front: loopback binds, a Chrome/Chromium binary,
# process inspection via `ps`, and a writable external git cache directory.
# Any it finds blocked feeds this registry to decide which KNOWN test-name
# pattern to skip instead of letting those tests fail into a denied
# bind/exec/write. gate_capability_skip_pattern CAPABILITY_ID prints the
# pattern for one id, or nothing when that capability has no known consumer.
#
# Evidenced mapping, root module ONLY (kata 5gvk's premise check):
#   - loopback-bind, process-inspect: cmd/serf-hub's TestE2E_* family and
#     cmd/serf-tui's TestTUITmuxE2E_* family are the root module's ONLY test
#     files that both (a) run only under ROOT_FULL=1 (gated by
#     testing.Short(); merge-approval-gate is the only caller that sets it)
#     and (b) spawn a real hub/daemon or tmux pane bound to a real loopback
#     port. Ordinary `make test` never reaches them.
#   - chrome-cdp, git-cache: no test file anywhere in this tree consumes
#     either today. test-web-browser needs Chrome but is a separate,
#     non-gate target; the only thing in the tree that names the fixed
#     /tmp/git-cache path is cmd/serf-gate-probe's own default, which the
#     probe creates in order to prove it is writable. Both are still probed
#     and reported for completeness and honesty; the pattern is empty because
#     nothing is skipped yet.
#
# Applied to the ROOT module ONLY, deliberately: `agent/session_escalation_e2e_test.go`
# (Linux-only, unrelated sandbox-escalation coverage) also has two
# `TestE2E_*`-named tests that share nothing with the cmd/serf-hub family
# except the prefix. Unioning this pattern into every module's `-skip` would
# silently and wrongly skip those two whenever loopback-bind or
# process-inspect is blocked. run-module-tests.sh applies the pattern to "."
# only for exactly this reason - see its root_skip.
gate_capability_skip_pattern() {
	case "$1" in
		loopback-bind | process-inspect) printf '%s' '^(TestE2E_|TestTUITmuxE2E_)' ;;
		chrome-cdp | git-cache) printf '' ;;
		*) printf '' ;;
	esac
}
