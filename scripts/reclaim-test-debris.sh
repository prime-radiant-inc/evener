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
#   - The gocache path is refused outright if `go env GOCACHE` points at it:
#     that would mean it stopped being debris and became the machine's live
#     build cache.
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

# Enumerate old-enough shard dirs. -mindepth/-maxdepth pin the search to
# direct children; -mmin +N keeps anything fresher than the age floor.
shard_dirs=()
while IFS= read -r -d '' d; do
	shard_dirs+=("$d")
done < <(find "$tmpbase" -mindepth 1 -maxdepth 1 -type d -name "${shard_prefix}*" -mmin "+${age_minutes}" -print0 2>/dev/null)

kept=$(find "$tmpbase" -mindepth 1 -maxdepth 1 -type d -name "${shard_prefix}*" -mmin "-${age_minutes}" 2>/dev/null | wc -l | tr -d ' ')

total_kb=0
if [ "${#shard_dirs[@]}" -gt 0 ]; then
	while IFS= read -r kb; do
		total_kb=$((total_kb + kb))
	done < <(du -sk "${shard_dirs[@]}" 2>/dev/null | awk '{print $1}')
fi

echo "shard dirs under $tmpbase older than ${age_minutes}m: ${#shard_dirs[@]} ($((total_kb / 1024))MB); kept as possibly-live: $kept"

gocache_target=""
if [ -d "$gocache_debris" ]; then
	# Refuse unless we can POSITIVELY verify the live GOCACHE is elsewhere:
	# an empty answer (go missing/broken) is a failed verification, not a
	# green light.
	live_gocache=$(go env GOCACHE 2>/dev/null || true)
	if [ -z "$live_gocache" ]; then
		echo "REFUSING $gocache_debris: could not verify the live GOCACHE (go env failed)" >&2
	elif [ "$live_gocache" = "$gocache_debris" ]; then
		echo "REFUSING $gocache_debris: it is the machine's live GOCACHE (go env GOCACHE)" >&2
	else
		gocache_target="$gocache_debris"
		echo "gocache debris: $gocache_debris ($(du -sk "$gocache_debris" 2>/dev/null | awk '{printf "%dMB", $1/1024}'))"
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
	exit 0
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
	if rm -rf -- "$gocache_target"; then
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
