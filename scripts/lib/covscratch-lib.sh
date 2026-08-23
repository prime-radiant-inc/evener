#!/usr/bin/env bash
# covscratch-lib.sh — the coverage runners' shared reclaim of their OWN
# abandoned scratch, sourced by coverage-floor.sh.
#
# Sourced, never executed. Sourcing has no side effects: it defines one function
# and touches nothing until a runner calls it.
#
# Why it exists: this project cleans up at the source, and nothing sweeps TMPDIR
# on these runners' behalf — the last janitor retired when its one remaining
# debris class moved to a self-reclaiming owner (evener dev agent-shards).
# Two exit paths still leave a directory behind. A run killed by
# SIGKILL (or an OOM kill, or a power cut) never reaches its trap. And
# coverage-floor.sh KEEPS its scratch on a failed run on purpose: the failure
# line printed its path, and the profiles and per-module logs are the only
# record of why. So each runner reclaims those
# leftovers itself, at the start of its next run — which is exactly when a
# failed run's diagnostics stop being the current answer.
#
# One copy, not four: the pid rules below are subtle enough that a second
# spelling would be a second thing to get wrong (see covstmt-lib.sh).

# reclaim_own_scratch TMPBASE PREFIX — remove every "TMPBASE/PREFIX.<pid>"
# directory whose pid is no longer running. Call it BEFORE creating this run's
# own scratch. Never fatal: a leftover that cannot be removed is reported and
# the run continues.
reclaim_own_scratch() {
	local tmpbase="${1%/}" prefix="$2" leftover pid
	# A loop over the glob with an existence guard, never a bare expansion
	# handed to rm: an unmatched glob passes through literally, and a TMPDIR
	# holding a space would split into two arguments — both of which are
	# arbitrary paths for a recursive delete.
	for leftover in "$tmpbase/$prefix".*; do
		[ -d "$leftover" ] || continue
		# A reclaimer never follows a link out of TMPDIR, and nothing these
		# runners create is one.
		[ -L "$leftover" ] && continue
		pid="${leftover##*.}"
		case "$pid" in
		'' | *[!0-9]*) continue ;;
		esac
		# The one thing that must never be touched is a live run's scratch. The
		# dev-tooling wave gives each suite its own TMPDIR, but a developer can
		# still start the same runner twice against one TMPDIR, and $$ is unique
		# among live processes — so a concurrent run is always named after a pid
		# that answers.
		#
		# That includes OUR pid: `kill -0 $$` always succeeds, so a stale
		# same-pid leftover is kept and the caller's mkdir still fails loudly on
		# it, rather than this deleting a directory it cannot tell apart from a
		# live sibling.
		#
		# Pid reuse only ever keeps a leftover one round longer — the next run,
		# under a different pid, reclaims it. For a recursive delete that is the
		# right direction to fail.
		kill -0 "$pid" 2>/dev/null && continue
		rm -rf -- "$leftover" ||
			printf '%s: could not reclaim abandoned scratch %s\n' "${0##*/}" "$leftover" >&2
	done
}
