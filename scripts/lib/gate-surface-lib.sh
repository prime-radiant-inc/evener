#!/usr/bin/env bash
# gate-surface-lib.sh — the single definition of WHICH tests the deterministic
# non-fuzz gate runs, shared by the gate itself (run-module-tests.sh) and by the
# ratchets that must measure the same surface (evener dev coverage-floor,
# test-timing-budget.sh).
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
GATE_FUZZ_TEST_SKIP='(SeqFuzz|SchemaFuzz|Structured.*Reach|LifecycleAdapter|ToolArgsAdapter|SeqAdapter|TurnPagingEquivalenceSanity|WireTypeRegistryCoverage|LineWindowExtractorsSanity|WriteListRoundTrip|LaunchConfigThreeStateRoundTrip|DifferentialSanity|StreamVsNonStreamSanity|FuzzBuildEnforces)'

# The gate runs ordinary Test/Example functions only. Without this filter
# `go test` also executes every native Fuzz target's committed seed corpus,
# which is make fuzz's job, not the default gate's.
GATE_TEST_RUN='^(Test|Example)'

