#!/usr/bin/env bash
# report-tmp-debris.sh — size the per-session scratch this repo's tooling leaves
# in /tmp (kata gmpr, kata td3g). Reports only; never deletes anything.
#
# Why this exists: when the repo volume hits its disk floor, the biggest single
# pocket of reclaimable space has not been anything git can see. Measured
# 2026-07-30, with 3G free against a 5G floor: 8.4G across 120 `/tmp/serf*`
# entries — per-session scratch CHECKOUTS (~270M each), stray per-session Go
# build caches, chrome profiles, screenshots, DOM dumps, logs — against ~264M
# of removable worktrees, which was the only lever the floor message named.
# /tmp is on the same volume as the checkout on this machine, so that 8.4G
# counts directly against the floor, and nothing measured it.
#
# Why it does not delete: a scratch checkout can hold a never-pushed
# experiment, and a live session may still be writing to one. Deletion here
# needs a human, and the bulk sweep needs authorization (kata gmpr). Keeping
# that decision out of a script whose siblings DO delete is deliberate — see
# scripts/report-orphaned-worktrees.sh, which exists for the same reason.
#
# Usage:
#   scripts/report-tmp-debris.sh              # report, human-readable
#   scripts/report-tmp-debris.sh --paths-only # one path per line, biggest first
#   scripts/report-tmp-debris.sh --help
#
# SERF_TMP_DEBRIS_ROOT overrides the directory scanned (default /tmp). It is
# how report-tmp-debris-selftest.sh points this at a throwaway tree instead of
# the real /tmp. /tmp is scanned rather than $TMPDIR because that is where the
# debris is: on macOS $TMPDIR is a per-user /var/folders path, and the entries
# this reports were created against an explicit /tmp path.
set -uo pipefail

# How many entries the human report lists. The rest are summarised in one line.
# The full list is --paths-only's job; this cap exists because the report is
# read at the disk floor, often by an agent with a context budget, and 120
# lines of it would crowd out the thing it is trying to say.
MAX_LISTED=20

PATHS_ONLY=0
for arg in "$@"; do
	case "$arg" in
	--paths-only) PATHS_ONLY=1 ;;
	-h | --help)
		sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "report-tmp-debris: unknown argument: $arg (try --help)" >&2
		exit 2
		;;
	esac
done

root=${SERF_TMP_DEBRIS_ROOT:-/tmp}
[ -d "$root" ] || {
	echo "report-tmp-debris: no such directory: $root" >&2
	exit 1
}

# Human-readable from KB, matching scripts/report-orphaned-worktrees.sh so the
# two reports read as one set of numbers.
human_kb() {
	awk -v kb="$1" 'BEGIN { printf (kb>=1048576) ? "%.1fG" : (kb>=1024) ? "%.0fM" : "%dK", (kb>=1048576) ? kb/1048576 : (kb>=1024) ? kb/1024 : kb }'
}

# One `du` invocation over every match, not one per entry: with ~120 entries
# that is the difference between one walk and 120 process spawns, and blocks
# shared between entries are then counted once, the way the volume counts them.
entries=()
for entry in "$root"/serf*; do
	[ -e "$entry" ] || continue # unmatched glob stays literal
	entries+=("$entry")
done

if [ "${#entries[@]}" -eq 0 ]; then
	[ "$PATHS_ONLY" = 1 ] || echo "report-tmp-debris: none found under $root"
	exit 0
fi

sized=()
while IFS=$'\t' read -r kb path; do
	[ -n "$path" ] || continue
	sized+=("$kb	$path")
done < <(du -sk "${entries[@]}" 2>/dev/null | sort -rn)

if [ "$PATHS_ONLY" = 1 ]; then
	for row in "${sized[@]}"; do
		echo "${row#*	}"
	done
	exit 0
fi

total_kb=0
for row in "${sized[@]}"; do
	total_kb=$((total_kb + ${row%%	*}))
done
total_h=$(human_kb "$total_kb")

plural() { [ "$1" = 1 ] && echo "y" || echo "ies"; }
echo "report-tmp-debris: ${#sized[@]} entr$(plural "${#sized[@]}") under $root match serf* (${total_h}):"
echo

listed=0
for row in "${sized[@]}"; do
	[ "$listed" -lt "$MAX_LISTED" ] || break
	listed=$((listed + 1))
	kb=${row%%	*}
	path=${row#*	}
	mtime=$(stat -f '%Sm' -t '%Y-%m-%d' "$path" 2>/dev/null || date -r "$(stat -c '%Y' "$path" 2>/dev/null || echo 0)" '+%Y-%m-%d' 2>/dev/null || echo "unknown")
	# RECENT is a ONE-WAY signal: touched in the last day, so a session may
	# still be using it. Its absence proves nothing — a directory's mtime does
	# not move when something writes deeper inside it.
	recent=""
	[ -n "$(find "$path" -maxdepth 0 -mtime -1 2>/dev/null)" ] && recent="  RECENT"
	printf '  %-44s %8s  mtime %s%s\n' "$(basename "$path")" "$(human_kb "$kb")" "$mtime" "$recent"
done
if [ "${#sized[@]}" -gt "$MAX_LISTED" ]; then
	rest_kb=0
	i=0
	for row in "${sized[@]}"; do
		i=$((i + 1))
		[ "$i" -gt "$MAX_LISTED" ] || continue
		rest_kb=$((rest_kb + ${row%%	*}))
	done
	echo "  ... and $((${#sized[@]} - MAX_LISTED)) smaller entr$(plural $((${#sized[@]} - MAX_LISTED))) totalling $(human_kb "$rest_kb") (--paths-only lists them all)"
fi

echo
echo "Total: ${total_h} across ${#sized[@]} entr$(plural "${#sized[@]}")."
echo

# Whether this space can move the repo volume's disk floor is a fact about
# devices, not about size. Saying it helps when it cannot is the dishonesty
# kata td3g exists to remove, so say which it is or say nothing.
repo_root=$(git rev-parse --show-toplevel 2>/dev/null)
if [ -n "$repo_root" ]; then
	root_dev=$(df -P "$root" 2>/dev/null | awk 'NR==2 {print $1}')
	repo_dev=$(df -P "$repo_root" 2>/dev/null | awk 'NR==2 {print $1}')
	if [ -n "$root_dev" ] && [ "$root_dev" = "$repo_dev" ]; then
		echo "$root is on the same volume as $repo_root, so all of that counts"
		echo "against the disk floor (scripts/disk-reclaim.sh --check)."
	elif [ -n "$root_dev" ] && [ -n "$repo_dev" ]; then
		echo "$root is on a DIFFERENT volume from $repo_root, so clearing"
		echo "it cannot move that checkout's disk floor."
	fi
	echo
fi

cat <<MSG
Nothing here was deleted, and nothing in this repo deletes it: a scratch
checkout can hold a never-pushed experiment, and RECENT means a session may
still be writing there. Review each entry, then remove it by hand — the bulk
sweep needs authorization (kata gmpr):

  dir=$root/<one entry from above>
  ls -la "\$dir"
  git -C "\$dir" status && git -C "\$dir" log --oneline -5   # if it is a checkout
  rm -rf "\$dir"
MSG
