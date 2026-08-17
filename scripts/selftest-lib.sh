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

# selftest_rm_scratch PATH — remove a directory selftest_scratch created, or
# something inside one. Refuses everything else, so a caller whose variable was
# emptied or clobbered cannot turn its EXIT trap into a delete of $PWD, $HOME,
# or /. An empty path is nothing to do, not an error: a suite that died before
# it had scratch still runs its trap. Returns non-zero on refusal rather than
# exiting, because it is called from EXIT traps where the suite's own status is
# already decided.
selftest_rm_scratch() {
	local _st_path="${1:-}" _st_me _st_known
	[ -n "$_st_path" ] || return 0
	# A path that walks upward can prefix-match an allowlisted root and still
	# resolve outside it, so refuse it before the match rather than trusting
	# the string comparison below.
	case "$_st_path" in
	*/../* | */.. | ../* | ..)
		_st_me="$(basename "$0" .sh)"
		printf '%s: refusing to delete "%s": the path walks upward, so a prefix match would not prove where it lands\n' \
			"$_st_me" "$_st_path" >&2
		return 1
		;;
	esac
	for _st_known in ${selftest_scratch_dirs[@]+"${selftest_scratch_dirs[@]}"}; do
		if [ "$_st_path" = "$_st_known" ]; then
			rm -rf "$_st_path"
			return 0
		fi
		case "$_st_path" in
		"$_st_known"/*)
			rm -rf "$_st_path"
			return 0
			;;
		esac
	done
	_st_me="$(basename "$0" .sh)"
	printf '%s: refusing to delete "%s": selftest_scratch never created it\n' \
		"$_st_me" "$_st_path" >&2
	return 1
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
