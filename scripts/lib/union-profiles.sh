#!/usr/bin/env bash
# union-profiles.sh — append Go coverage profiles into one file for union
# counting, with each profile terminated by a newline.
#
# Sourced, never executed. Kept out of e2e-cover.sh so the append logic — in
# particular its failure path — is unit-testable without the heavy build/run
# harness that script needs.

# union_profiles OUT IN... — append each existing IN to OUT, each followed by a
# newline separator.
#
# The separator matters: a profile whose final block line lacks a trailing
# newline would otherwise fuse with the next profile's "mode:" header, and the
# parser would skip both lines, silently dropping that block.
#
# Every step's status is checked individually. A brace group
# `{ cat ...; printf ...; }` reports only printf's status and would mask a cat
# failure, appending a partial profile that counts as if it were whole. Returns
# non-zero on the first read/write failure so the caller can abort.
union_profiles() {
	local out="$1"; shift
	local prof
	for prof in "$@"; do
		[ -f "$prof" ] || continue
		if ! cat "$prof" >>"$out"; then
			echo "union-profiles: cannot append $prof to $out" >&2
			return 1
		fi
		if ! printf '\n' >>"$out"; then
			echo "union-profiles: cannot append the separator to $out" >&2
			return 1
		fi
	done
	return 0
}
