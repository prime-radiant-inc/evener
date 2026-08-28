#!/usr/bin/env bash
# selftest-lib.sh — shared assertion/counter/summary helpers for scripts/*-selftest.sh.
#
# Sourced, never executed: `. "$(dirname "$0")/selftest-lib.sh"`. Sourcing has
# no side effects: this file defines functions and initializes counters and does
# nothing else — it creates no files, installs no traps, and touches nothing
# under TMPDIR until a suite calls something. That is what keeps it safe for the
# wave runner's leak check, which fails a suite for anything it leaves behind.
#
# Scratch handling lives in scratch-lib.sh, sourced below: suites mint scratch
# with `scratch_dir <var> <prefix>` and reclaim it with the no-argument
# `scratch_rm`, the same way every other script here does.
#
. "$(dirname "${BASH_SOURCE[0]}")/scratch-lib.sh"

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
