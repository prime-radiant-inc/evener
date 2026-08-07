#!/usr/bin/env bash
# selftest-lib.sh — shared assertion/counter/summary helpers for scripts/*-selftest.sh.
#
# Sourced, never executed: `. "$(dirname "$0")/selftest-lib.sh"`. Deliberately
# pure bookkeeping — it defines functions and the checks/fails counters and
# does nothing else: no files, no traps, no TMPDIR. That keeps it safe for the
# wave runner's leak check, which fails a suite for anything it leaves behind.
#
# Suites that need a helper no one else does (e.g. merge-approval-gate's
# assert_before) keep it locally rather than growing this file for one caller.
checks=0
fails=0

ok() {
	checks=$((checks + 1))
	printf '  ok: %s\n' "$1"
}

bad() {
	checks=$((checks + 1))
	fails=$((fails + 1))
	printf 'FAIL: %s\n' "$1"
}

# assert_eq ACTUAL EXPECTED DESC
assert_eq() {
	if [ "$1" = "$2" ]; then
		ok "$3"
	else
		bad "$3 (want '$2', got '$1')"
	fi
}

# assert_has FILE NEEDLE DESC — DESC passes if FILE contains NEEDLE.
assert_has() {
	if grep -qF -- "$2" "$1"; then
		ok "$3"
	else
		bad "$3 (missing '$2')"
		sed 's/^/    | /' "$1"
	fi
}

# assert_not_has FILE NEEDLE DESC — DESC passes if FILE does not contain NEEDLE.
assert_not_has() {
	if grep -qF -- "$2" "$1"; then
		bad "$3 (unexpected '$2')"
		sed 's/^/    | /' "$1"
	else
		ok "$3"
	fi
}

# selftest_summary — print "<name>: N checks, M failed" and return the
# suite's pass/fail status (0 iff nothing failed). Call it last, undecorated,
# so its return value becomes the suite's exit status. The name is
# $suite_name if the suite set one before sourcing this file, else $0 with
# its extension stripped (e.g. "setup-gocache-selftest").
selftest_summary() {
	name="${suite_name:-$(basename "$0" .sh)}"
	printf '\n%s: %d checks, %d failed\n' "$name" "$checks" "$fails"
	[ "$fails" -eq 0 ]
}
