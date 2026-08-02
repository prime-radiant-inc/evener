#!/bin/bash
# reclaim-test-debris.sh — delete accumulated per-run test scratch that no
# gate or selftest cleans up on its own:
#
#   1) $TMPDIR/agent-test-shards.*  — one directory per sharded `make test`
#      run (kept so a failing run's logs can be read; a green run's dir is
#      pure debris at ~41MB each — measured 103 dirs / ~4.2GB on 2026-07-31).
#   2) /tmp/serf-gocache-k3         — a one-off GOCACHE a session created to
#      route around the external-volume stall and flagged for cleanup
#      (recorded in kata r07s's filing).
#
# Usage:
#   scripts/reclaim-test-debris.sh --dry-run   # list what would go + totals
#   scripts/reclaim-test-debris.sh --yes       # actually delete
#
# Safety, in order of importance:
#   - Deletes only DIRECTORIES sitting directly under $TMPDIR whose names
#     start with the exact shard prefix. Symlinks never match (-type d under
#     find's default -P), and nothing is ever followed out of $TMPDIR.
#   - A directory modified in the last SERF_DEBRIS_MIN_AGE_MINUTES minutes
#     (default 30) is KEPT: an in-flight `make test` writes into its shard
#     dir continuously, so freshness is the "is anyone using this" signal.
#     Active shard runs also own a `.heartbeat` file and refresh it while they
#     run; that marker is authoritative because appending an existing nested
#     log does not update the shard directory itself.
#   - The gocache path is refused outright if `go env GOCACHE` points at it:
#     canonical filesystem identity, rather than spelling, decides whether it
#     stopped being debris and became the machine's live build cache.
#   - Every failed removal is reported and the script exits nonzero; partial
#     success is stated as partial.
set -euo pipefail

mode=""
case "${1:-}" in
--dry-run) mode=dry ;;
--yes) mode=go ;;
*)
	echo "usage: $0 --dry-run | --yes" >&2
	exit 2
	;;
esac

age_minutes=${SERF_DEBRIS_MIN_AGE_MINUTES:-30}
tmpbase=${TMPDIR:-/tmp}
tmpbase=${tmpbase%/}
shard_prefix='agent-test-shards.'
gocache_debris='/tmp/serf-gocache-k3'
# The selftest may replace only the parent root; the fixed basename keeps this
# cleanup target from becoming an arbitrary rm -rf input.
if [ -n "${SERF_GOCACHE_DEBRIS_ROOT:-}" ]; then
	gocache_debris="${SERF_GOCACHE_DEBRIS_ROOT%/}/serf-gocache-k3"
fi
heartbeat_age_minutes=10

# A fresh heartbeat protects a shard even when its directory mtime is old. The
# heartbeat window is independent of the debris age floor: a caller may set
# the latter to zero for a sweep without making a live marker stale.
heartbeat_status() {
	local marker="$1/.heartbeat" fresh
	if [ -L "$marker" ] || [ -e "$marker" ]; then
		[ -f "$marker" ] || return 2
		fresh=$(find "$marker" -type f -mmin "-${heartbeat_age_minutes}" -print 2>/dev/null) || return 2
		[ -n "$fresh" ] && return 0
		return 1
	fi
	return 3
}

directory_is_fresh() {
	find "$1" -type d -prune -mmin "-${age_minutes}" -print 2>/dev/null | grep -q .
}

# Enumerate old-enough shard dirs. The glob pins the search to direct children,
# and the symlink check preserves the rule that a reclaimer never follows a
# link out of TMPDIR. A fresh directory or heartbeat keeps the entry;
# everything else is old enough to reclaim, including legacy shard dirs with no
# marker.
shard_dirs=()
kept=0
for d in "$tmpbase"/"${shard_prefix}"*; do
	[ -d "$d" ] || continue
	[ -L "$d" ] && continue
	heartbeat_state=3
	if heartbeat_status "$d"; then
		heartbeat_state=0
	else
		heartbeat_state=$?
	fi
	if directory_is_fresh "$d" || [ "$heartbeat_state" -eq 0 ] || [ "$heartbeat_state" -eq 2 ]; then
		kept=$((kept + 1))
	else
		shard_dirs+=("$d")
	fi
done

total_kb=0
if [ "${#shard_dirs[@]}" -gt 0 ]; then
	while IFS= read -r kb; do
		total_kb=$((total_kb + kb))
	done < <(du -sk "${shard_dirs[@]}" 2>/dev/null | awk '{print $1}')
fi

echo "shard dirs under $tmpbase older than ${age_minutes}m: ${#shard_dirs[@]} ($((total_kb / 1024))MB); kept as possibly-live: $kept"

filesystem_identity() {
	local path="$1" identity
	[ -d "$path" ] || return 1
	identity=$(stat -L -c '%d:%i' "$path" 2>/dev/null) || identity=""
	if [ -n "$identity" ]; then
		printf '%s\n' "$identity"
		return 0
	fi
	identity=$(stat -L -f '%d:%i' "$path" 2>/dev/null) || return 1
	[ -n "$identity" ] || return 1
	printf '%s\n' "$identity"
}

gocache_target=""
gocache_verification_failed=0
if [ -d "$gocache_debris" ]; then
	# Refuse unless we can POSITIVELY verify the live GOCACHE is elsewhere:
	# an empty answer (go missing/broken) is a failed verification, not a
	# green light. Resolve both existing directories through the filesystem so
	# symlinks, macOS /private prefixes, and trailing slashes cannot bypass the
	# guard.
	live_gocache=$(go env GOCACHE 2>/dev/null || true)
	if [ -z "$live_gocache" ]; then
		echo "REFUSING $gocache_debris: could not verify the live GOCACHE (go env failed)" >&2
		gocache_verification_failed=1
	else
		debris_identity=$(filesystem_identity "$gocache_debris" 2>/dev/null || true)
		live_identity=$(filesystem_identity "$live_gocache" 2>/dev/null || true)
		if [ -z "$debris_identity" ] || [ -z "$live_identity" ]; then
			echo "REFUSING $gocache_debris: could not establish canonical filesystem identity for the live GOCACHE" >&2
			gocache_verification_failed=1
		elif [ "$live_identity" = "$debris_identity" ]; then
			echo "REFUSING $gocache_debris: it is the machine's live GOCACHE (go env GOCACHE)" >&2
		else
			gocache_target="$gocache_debris"
			echo "gocache debris: $gocache_debris ($(du -sk "$gocache_debris" 2>/dev/null | awk '{printf "%dMB", $1/1024}'))"
		fi
	fi
else
	echo "gocache debris: $gocache_debris not present"
fi

if [ "$mode" = dry ]; then
	# No pipe into head here: under pipefail, head closing the pipe early
	# would kill the whole script mid-report. A plain counter loop shows the
	# first eight and says how many more there are.
	shown=0
	for d in ${shard_dirs[@]+"${shard_dirs[@]}"}; do
		if [ "$shown" -ge 8 ]; then
			echo "  … and $((${#shard_dirs[@]} - 8)) more"
			break
		fi
		echo "  would remove: $d"
		shown=$((shown + 1))
	done
	echo "dry run: nothing removed"
	exit "$gocache_verification_failed"
fi

shard_failures=0
if [ "${#shard_dirs[@]}" -gt 0 ]; then
	for d in "${shard_dirs[@]}"; do
		rm -rf -- "$d" || { echo "FAILED to remove: $d" >&2; shard_failures=$((shard_failures + 1)); }
	done
fi
gocache_failed=0
gocache_note=""
if [ -n "$gocache_target" ]; then
	# Recheck immediately before removal: a live tool can repoint GOCACHE after
	# the initial report, and the guard must fail closed across that race too.
	live_gocache=$(go env GOCACHE 2>/dev/null || true)
	debris_identity=$(filesystem_identity "$gocache_target" 2>/dev/null || true)
	live_identity=$(filesystem_identity "$live_gocache" 2>/dev/null || true)
	if [ -z "$live_gocache" ] || [ -z "$debris_identity" ] || [ -z "$live_identity" ]; then
		echo "REFUSING $gocache_target: could not recheck canonical GOCACHE identity before removal" >&2
		gocache_verification_failed=1
	elif [ "$live_identity" = "$debris_identity" ]; then
		echo "REFUSING $gocache_target: it became the machine's live GOCACHE before removal" >&2
		gocache_verification_failed=1
	elif rm -rf -- "$gocache_target"; then
		gocache_note=" + the gocache debris"
	else
		echo "FAILED to remove: $gocache_target" >&2
		gocache_failed=1
	fi
fi

removed=$((${#shard_dirs[@]} - shard_failures))
echo "removed $removed shard dir(s)${gocache_note}; freed ~$((total_kb / 1024))MB"
if [ "$((shard_failures + gocache_failed))" -gt 0 ]; then
	echo "PARTIAL: $((shard_failures + gocache_failed)) removal(s) failed (listed above)" >&2
	exit 1
fi

if [ "$gocache_verification_failed" -ne 0 ]; then
	echo "PARTIAL: live GOCACHE identity could not be verified; no cache was removed" >&2
	exit 1
fi
