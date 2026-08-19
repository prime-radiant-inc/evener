#!/usr/bin/env bash
# gate-surface-lib-selftest.sh — pins WHICH tests the gate's fuzz skip list
# excludes, so a stale entry can never again silently ungates a test that has
# already been fixed red (issue #170).
#
# The bug: scripts/gate-surface-lib.sh's GATE_FUZZ_TEST_SKIP regex still named
# TranscriptReadersAgreeSanity and Structured.*Reach after PR #122 fixed those
# tests, so the merge-approval gate (and the coverage ratchet that must measure
# the same surface) passed -skip with that regex and never RAN them. They sat
# red for a month with the gate green. This suite fails the moment the skip
# list names any test the gate is supposed to run.
#
# Pure string logic: it sources gate-surface-lib.sh (the single definition of
# the skip list) and asserts each test name is NOT matched by the regex, plus
# one known-fuzz-designated name IS still matched so the check is precise and
# not vacuously true. No go test, no fixtures, no scratch.
set -uo pipefail

. "$(dirname "$0")/selftest-lib.sh"
. "$(dirname "$0")/gate-surface-lib.sh"

# Sanity: the variable was actually sourced. A selftest that passes by
# accident because the variable is empty is worse than no selftest.
if [ -n "$GATE_FUZZ_TEST_SKIP" ]; then
	ok "GATE_FUZZ_TEST_SKIP is non-empty (the skip list was sourced)"
else
	bad "GATE_FUZZ_TEST_SKIP is empty (gate-surface-lib.sh did not define it)"
fi

# assert_not_skipped NAME DESC — fails if the gate's skip regex would match
# NAME, i.e. if the gate would skip a test it is supposed to run.
assert_not_skipped() {
	local name="$1" desc="$2"
	if [[ "$name" =~ $GATE_FUZZ_TEST_SKIP ]]; then
		bad "$desc (matched by GATE_FUZZ_TEST_SKIP, so the gate skips it)"
	else
		ok "$desc"
	fi
}

# assert_still_skipped NAME DESC — the control: a real fuzz-designated name
# that must remain in the skip list, so removing the two stale patterns did
# not nuke the whole list and turn this suite vacuously green.
assert_still_skipped() {
	local name="$1" desc="$2"
	if [[ "$name" =~ $GATE_FUZZ_TEST_SKIP ]]; then
		ok "$desc"
	else
		bad "$desc (no longer matched by GATE_FUZZ_TEST_SKIP; the skip list lost a real fuzz entry)"
	fi
}

# These are the tests the skip list wrongly excluded. Each must NOT be matched
# by the regex, so the gate (and the coverage ratchet) run it. All exist in the
# tree; all were verified to pass on main once ungated. The two evenerfuzz-tagged
# ones (TranscriptReadersAgreeSanity, StructuredTranscriptReachesDeeper) only
# run under make fuzz, but the skip-list entry is still redundant and the issue
# says remove it, so this pins that they are not in the gate's skip list.
assert_not_skipped TestTranscriptReadersAgreeSanity \
	"TestTranscriptReadersAgreeSanity is not skipped by the gate"
assert_not_skipped TestStructuredFrameReachesDecoder \
	"TestStructuredFrameReachesDecoder is not skipped by the gate"
assert_not_skipped TestStructuredResponsesReachesDeeper \
	"TestStructuredResponsesReachesDeeper is not skipped by the gate"
assert_not_skipped TestStructuredAnthropicReachesDeeper \
	"TestStructuredAnthropicReachesDeeper is not skipped by the gate"
assert_not_skipped TestStructuredOpenAICompatReachesDeeper \
	"TestStructuredOpenAICompatReachesDeeper is not skipped by the gate"
assert_not_skipped TestStructuredGeminiReachesDeeper \
	"TestStructuredGeminiReachesDeeper is not skipped by the gate"
assert_not_skipped TestStructuredTranscriptReachesDeeper \
	"TestStructuredTranscriptReachesDeeper is not skipped by the gate"

# A real fuzz-designated name that should stay in the skip list: the gate
# excludes rapid/sequence fuzz tests (make fuzz's job). TestRouterSeqFuzz exists
# in internal/appserver/router_seqfuzz_test.go and is skipped by default via
# t.Skip, so the skip-list entry for SeqFuzz must keep matching it.
assert_still_skipped TestRouterSeqFuzz \
	"TestRouterSeqFuzz (a real fuzz-designated SeqFuzz name) is still skipped by the gate"

selftest_summary
