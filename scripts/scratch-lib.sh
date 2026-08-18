#!/usr/bin/env bash
# scratch-lib.sh — the one way scripts in this repository mint and reclaim
# temporary directories.
#
# Sourced, never executed, and bash-only (arrays, local, printf -v): a script
# that sources this needs a bash shebang. Sourcing has no side effects — it
# defines two functions and one array and touches nothing under TMPDIR until a
# caller asks.
#
# The design rule, standing since kata 5hs2's cleanup deleted a home
# directory: a recursive delete must never take an argument a caller could
# clobber. scratch_dir validates every step of creation and registers the
# result; scratch_rm takes no argument and removes only what scratch_dir
# registered. TestNoScriptFeedsVariableToRecursiveDelete keeps every other
# recursive delete in scripts/ on a count-pinned list.

# scratch_lib_dirs are the directories scratch_dir has created, and the only
# ones scratch_rm will delete. Being under TMPDIR is not enough to be
# deletable: a caller's own working directory can be under TMPDIR too, and
# that is precisely the path this guard exists to protect.
scratch_lib_dirs=()

# scratch_dir VARNAME PREFIX — create a scratch directory under TMPDIR and
# assign its canonical path to the caller's VARNAME. Any failure is fatal: the
# script exits non-zero with a diagnostic naming itself and the failure,
# having created nothing.
#
# Call it as a statement, never inside a command substitution:
#
#	scratch_dir work my-selftest        # right
#	work="$(scratch_dir work my-...)"   # WRONG: swallows the exit
#
# The wrong spelling runs the guard in a subshell, where `exit 1` ends only
# that subshell and hands the caller an empty path. TestScratchDirCannotResolveToCWD
# fails on it so the mistake cannot reach a run.
#
# The guard exists because the idiom it replaces aimed a recursive delete at
# the repo (kata 5hs2). `mktemp -d` unchecked, then `work="$(cd "$work" && pwd -P)"`,
# resolves $work to the CALLER'S WORKING DIRECTORY when mktemp fails, because
# `cd ""` succeeds and leaves $PWD alone. The suite then wrote its fixtures
# into the checkout and its EXIT trap deleted it. So every step below is
# checked, and the canonical path is required to be inside TMPDIR before the
# caller sees it.
#
# The path is assigned only after it is known good: if a caller ever does
# spell the call wrong, its variable is left empty rather than pointed
# somewhere real.
#
# Locals carry an _sc_ prefix so they cannot shadow the VARNAME a caller asks
# to have filled in.
scratch_dir() {
	local _sc_var="${1:?scratch_dir: VARNAME required}"
	local _sc_prefix="${2:?scratch_dir: PREFIX required}"
	local _sc_me _sc_root _sc_dir
	_sc_me="$(basename "$0" .sh)"

	# A TMPDIR that does not exist is one of the real failure modes here: an
	# inherited setting pointing at a reaped sandbox. Reject it before asking
	# mktemp for anything, so the diagnostic names the actual problem.
	_sc_root="${TMPDIR:-/tmp}"
	if ! _sc_root="$(cd "$_sc_root" 2>/dev/null && pwd -P)"; then
		printf '%s: TMPDIR "%s" is not a usable directory; refusing to continue\n' \
			"$_sc_me" "${TMPDIR:-/tmp}" >&2
		exit 1
	fi

	# An explicit template, never `-t`: macOS mktemp -t ignores TMPDIR and
	# creates in the Darwin per-user temp directory, outside both the wave's
	# per-suite isolation and its leak check (docs/testing.md, kata cqne).
	if ! _sc_dir="$(mktemp -d "$_sc_root/$_sc_prefix.XXXXXX")"; then
		printf '%s: mktemp -d under "%s" failed; refusing to continue\n' \
			"$_sc_me" "$_sc_root" >&2
		exit 1
	fi
	if [ -z "$_sc_dir" ] || [ ! -d "$_sc_dir" ]; then
		printf '%s: mktemp -d under "%s" returned no usable directory ("%s"); refusing to continue\n' \
			"$_sc_me" "$_sc_root" "$_sc_dir" >&2
		exit 1
	fi
	# Canonicalize: on macOS $TMPDIR lives behind the /var -> /private/var
	# symlink, and callers compare paths against what a script under test
	# resolved with `pwd -P` or `git rev-parse`. Safe to spell this way here
	# because the directory above is known to exist.
	if ! _sc_dir="$(cd "$_sc_dir" 2>/dev/null && pwd -P)"; then
		printf '%s: scratch directory under "%s" vanished before it could be resolved; refusing to continue\n' \
			"$_sc_me" "$_sc_root" >&2
		exit 1
	fi
	case "$_sc_dir" in
	"$_sc_root"/*) ;;
	*)
		printf '%s: scratch "%s" resolved outside TMPDIR "%s"; refusing to continue\n' \
			"$_sc_me" "$_sc_dir" "$_sc_root" >&2
		exit 1
		;;
	esac
	# The template above fixes what a working mktemp can return, so anything
	# else is a directory this call did not create — an existing one the
	# caller may well be standing in. Being somewhere under TMPDIR is not
	# enough to be safe to fill with fixtures and delete afterwards.
	case "${_sc_dir##*/}" in
	"$_sc_prefix".??????) ;;
	*)
		printf '%s: mktemp returned "%s", which is not the directory it was asked to create under "%s"; refusing to continue\n' \
			"$_sc_me" "$_sc_dir" "$_sc_root" >&2
		exit 1
		;;
	esac

	scratch_lib_dirs=(${scratch_lib_dirs[@]+"${scratch_lib_dirs[@]}"} "$_sc_dir")
	printf -v "$_sc_var" '%s' "$_sc_dir"
}

# scratch_rm — remove every directory scratch_dir created for this script.
#
# It takes no argument, and that is the whole point. A delete that accepts a
# path can be handed $PWD, $HOME, or / by a variable that was emptied or
# clobbered, and no amount of guarding inside the function makes the caller's
# variable trustworthy. With no argument there is nothing to get wrong: the
# only paths it can reach are ones scratch_dir minted and validated.
#
# Safe to call with no scratch yet (a script that died early still runs its
# trap), and safe to call twice. Never exits: it is called from EXIT traps
# where the script's own status is already decided.
scratch_rm() {
	local _sc_dir _sc_root _sc_me
	if [ "$#" -ne 0 ]; then
		_sc_me="$(basename "$0" .sh)"
		printf '%s: scratch_rm takes no arguments; it removes what scratch_dir created\n' \
			"$_sc_me" >&2
		return 2
	fi
	_sc_root="${TMPDIR:-/tmp}"
	_sc_root="$(cd "$_sc_root" 2>/dev/null && pwd -P)" || return 1
	for _sc_dir in ${scratch_lib_dirs[@]+"${scratch_lib_dirs[@]}"}; do
		# scratch_dir already proved each of these is a directory it minted
		# under TMPDIR. Re-checking costs nothing and means even a corrupted
		# array cannot widen the delete.
		[ -n "$_sc_dir" ] || continue
		case "$_sc_dir" in
		"$_sc_root"/*) ;;
		*) continue ;;
		esac
		case "$_sc_dir" in
		*/../* | */..) continue ;;
		esac
		rm -rf "$_sc_dir"
	done
	scratch_lib_dirs=()
}
