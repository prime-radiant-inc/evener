#!/usr/bin/env bash
# setup-gocache-selftest.sh — offline, deterministic test of scripts/setup-gocache.sh.
#
# The script writes GOCACHE with `go env -w`, which lands in the file named by
# GOENV. Every scenario here pins GOENV to a throwaway file, so nothing touches
# the real per-user go env, and assertions read that file directly.
#
# Scenario 1 pins the contract that created this suite: the script used to
# default to a hardcoded external volume ("/Volumes/Local Archives"), and when
# that volume ceased to exist, every no-argument run chased a dead path. There
# is no safe machine-independent default; a target must be given explicitly.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/setup-gocache.sh"
. "$(dirname "$0")/selftest-lib.sh"

work="$(mktemp -d -t setup-gocache-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT
export GOENV="$work/goenv"

# --- scenario 1: no argument is an error that asks for a path, not a write ---
if bash "$script" >"$work/noarg.out" 2>&1; then
	bad "a no-argument run exited 0; there is no safe default target"
else
	if grep -q "no target path given" "$work/noarg.out" && ! grep -q "/Volumes" "$work/noarg.out"; then
		ok "a no-argument run fails and asks for an explicit path"
	else
		bad "a no-argument run failed for the wrong reason (a baked-in default?): $(cat "$work/noarg.out")"
	fi
fi
if [ -e "$GOENV" ] && grep -q GOCACHE "$GOENV" 2>/dev/null; then
	bad "a no-argument run wrote GOCACHE anyway"
else
	ok "a no-argument run writes nothing"
fi

# --- scenario 2: an explicit target is created and written to the go env ---
target="$work/big-volume/serf-build-cache"
mkdir -p "$work/big-volume"
if bash "$script" "$target" >"$work/explicit.out" 2>&1; then
	if [ -d "$target" ] && grep -qF "GOCACHE=$target" "$GOENV"; then
		ok "an explicit target is created and set as GOCACHE"
	else
		bad "the run exited 0 but the target or go env write is missing: $(cat "$work/explicit.out")"
	fi
else
	bad "an explicit, reachable target failed: $(cat "$work/explicit.out")"
fi

# --- scenario 3: a target under a missing parent is refused, not created ---
# A missing parent is what an unmounted volume looks like; mkdir -p would
# happily bury the mistake under a path nothing will ever mount over.
ghost="$work/not-mounted/serf-build-cache"
if bash "$script" "$ghost" >"$work/ghost.out" 2>&1; then
	bad "a target under a missing parent exited 0"
else
	if grep -q "does not exist" "$work/ghost.out" && [ ! -d "$ghost" ]; then
		ok "a target under a missing parent is refused and not created"
	else
		bad "the refusal was unspecific or the path was created anyway: $(cat "$work/ghost.out")"
	fi
fi

selftest_summary
