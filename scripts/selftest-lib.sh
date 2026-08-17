#!/usr/bin/env bash
# selftest-lib.sh — shared assertion/counter/summary helpers for scripts/*-selftest.sh.
#
# Sourced, never executed: `. "$(dirname "$0")/selftest-lib.sh"`. Sourcing has
# no side effects: this file defines functions and initializes counters and does
# nothing else — it creates no files, installs no traps, and touches nothing
# under TMPDIR until a suite calls something. That is what keeps it safe for the
# wave runner's leak check, which fails a suite for anything it leaves behind.
#
# selftest_scratch does create a directory, but only when a suite calls it, and
# only inside the suite's own TMPDIR — which is exactly where the wave runner's
# leak check looks. A suite that creates scratch and removes it in its EXIT trap
# leaves nothing behind, so the invariant above still holds.
#
# Suites that need a helper no one else does (e.g. merge-approval-gate's
# assert_before) keep it locally rather than growing this file for one caller.
checks=0
fails=0

# selftest_scratch_dirs are the directories selftest_scratch has created, and
# the only ones selftest_rm_scratch will delete. Being under TMPDIR is not
# enough to be deletable: a suite's own working directory can be under TMPDIR
# too, and that is precisely the path this guard exists to protect.
selftest_scratch_dirs=()

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

# selftest_scratch VARNAME PREFIX — create a scratch directory under TMPDIR and
# assign its canonical path to the caller's VARNAME. Any failure is fatal: the
# suite exits non-zero with a diagnostic naming itself and the failure, having
# created nothing.
#
# Call it as a statement, never inside a command substitution:
#
#	selftest_scratch work my-selftest        # right
#	work="$(selftest_scratch work my-...)"   # WRONG: swallows the exit
#
# The wrong spelling runs the guard in a subshell, where `exit 1` ends only that
# subshell and hands the caller an empty path. TestNoSelftestResolvesScratchToCWD
# fails on it so the mistake cannot reach a run.
#
# The guard exists because the idiom it replaces aimed a recursive delete at the
# repo (kata 5hs2). `mktemp -d` unchecked, then `work="$(cd "$work" && pwd -P)"`,
# resolves $work to the CALLER'S WORKING DIRECTORY when mktemp fails, because
# `cd ""` succeeds and leaves $PWD alone. The suite then wrote its fixtures into
# the checkout and its EXIT trap deleted it. So every step below is checked, and
# the canonical path is required to be inside TMPDIR before the caller sees it.
#
# The path is assigned only after it is known good: if a caller ever does spell
# the call wrong, its variable is left empty rather than pointed somewhere real.
#
# Locals carry an _st_ prefix so they cannot shadow the VARNAME a caller asks to
# have filled in.
selftest_scratch() {
	local _st_var="${1:?selftest_scratch: VARNAME required}"
	local _st_prefix="${2:?selftest_scratch: PREFIX required}"
	local _st_me _st_root _st_dir
	_st_me="$(basename "$0" .sh)"

	# A TMPDIR that does not exist is one of the real failure modes here: an
	# inherited setting pointing at a reaped sandbox. Reject it before asking
	# mktemp for anything, so the diagnostic names the actual problem.
	_st_root="${TMPDIR:-/tmp}"
	if ! _st_root="$(cd "$_st_root" 2>/dev/null && pwd -P)"; then
		printf '%s: TMPDIR "%s" is not a usable directory; refusing to continue\n' \
			"$_st_me" "${TMPDIR:-/tmp}" >&2
		exit 1
	fi

	# An explicit template, never `-t`: macOS mktemp -t ignores TMPDIR and
	# creates in the Darwin per-user temp directory, outside both the wave's
	# per-suite isolation and its leak check (docs/testing.md, kata cqne).
	if ! _st_dir="$(mktemp -d "$_st_root/$_st_prefix.XXXXXX")"; then
		printf '%s: mktemp -d under "%s" failed; refusing to continue\n' \
			"$_st_me" "$_st_root" >&2
		exit 1
	fi
	if [ -z "$_st_dir" ] || [ ! -d "$_st_dir" ]; then
		printf '%s: mktemp -d under "%s" returned no usable directory ("%s"); refusing to continue\n' \
			"$_st_me" "$_st_root" "$_st_dir" >&2
		exit 1
	fi
	# Canonicalize: on macOS $TMPDIR lives behind the /var -> /private/var
	# symlink, and suites compare paths against what a script under test
	# resolved with `pwd -P` or `git rev-parse`. Safe to spell this way here
	# because the directory above is known to exist.
	if ! _st_dir="$(cd "$_st_dir" 2>/dev/null && pwd -P)"; then
		printf '%s: scratch directory under "%s" vanished before it could be resolved; refusing to continue\n' \
			"$_st_me" "$_st_root" >&2
		exit 1
	fi
	case "$_st_dir" in
	"$_st_root"/*) ;;
	*)
		printf '%s: scratch "%s" resolved outside TMPDIR "%s"; refusing to continue\n' \
			"$_st_me" "$_st_dir" "$_st_root" >&2
		exit 1
		;;
	esac
	# The template above fixes what a working mktemp can return, so anything
	# else is a directory this call did not create — an existing one the caller
	# may well be standing in. Being somewhere under TMPDIR is not enough to be
	# safe to fill with fixtures and delete afterwards.
	case "${_st_dir##*/}" in
	"$_st_prefix".??????) ;;
	*)
		printf '%s: mktemp returned "%s", which is not the directory it was asked to create under "%s"; refusing to continue\n' \
			"$_st_me" "$_st_dir" "$_st_root" >&2
		exit 1
		;;
	esac

	selftest_scratch_dirs=(${selftest_scratch_dirs[@]+"${selftest_scratch_dirs[@]}"} "$_st_dir")
	printf -v "$_st_var" '%s' "$_st_dir"
}

# selftest_rm_scratch — remove every directory selftest_scratch created for this
# suite.
#
# It takes no argument, and that is the whole point. A delete that accepts a
# path can be handed $PWD, $HOME, or / by a variable that was emptied or
# clobbered, and no amount of guarding inside the function makes the caller's
# variable trustworthy. With no argument there is nothing to get wrong: the only
# paths it can reach are ones selftest_scratch minted and validated.
#
# Safe to call with no scratch yet (a suite that died early still runs its
# trap), and safe to call twice. Never exits: it is called from EXIT traps where
# the suite's own status is already decided.
selftest_rm_scratch() {
	local _st_dir _st_root _st_me
	if [ "$#" -ne 0 ]; then
		_st_me="$(basename "$0" .sh)"
		printf '%s: selftest_rm_scratch takes no arguments; it removes what selftest_scratch created\n' \
			"$_st_me" >&2
		return 2
	fi
	_st_root="${TMPDIR:-/tmp}"
	_st_root="$(cd "$_st_root" 2>/dev/null && pwd -P)" || return 1
	for _st_dir in ${selftest_scratch_dirs[@]+"${selftest_scratch_dirs[@]}"}; do
		# selftest_scratch already proved each of these is a directory it
		# minted under TMPDIR. Re-checking costs nothing and means even a
		# corrupted array cannot widen the delete.
		[ -n "$_st_dir" ] || continue
		case "$_st_dir" in
		"$_st_root"/*) ;;
		*) continue ;;
		esac
		case "$_st_dir" in
		*/../* | */..) continue ;;
		esac
		rm -rf "$_st_dir"
	done
	selftest_scratch_dirs=()
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
