#!/bin/sh
# golangci-cache-sweep — reclaim per-worktree golangci-lint analysis caches
# whose owning worktree is no longer registered.
#
# Why: scripts/lib/golangci-lint-cache.sh keys each checkout's golangci-lint
# cache by a hash of its worktree root (the #290 cross-checkout poisoning
# guard), so nothing else cleans up after a worktree is removed — the cache
# dir outlives its tree, and on a machine with a fast-churning worktree fleet
# the dead dirs hold hundreds of megabytes each (measured 2026-11-13: 855M
# dead against 978M live).
#
# Scope: the sweeper only ever judges inside THIS repository's cache bucket —
# <cache root>/<sha256-16 of this repository's Git common directory> — which
# is exactly where scripts/lib/golangci-lint-cache.sh writes this clone's
# caches. Independent clones share one cache root (the same XDG_CACHE_HOME)
# but never a common directory, and `git worktree list` only ever names this
# checkout's own trees, so without the bucket a sweep from one clone would
# judge another clone's live caches dead once their mtimes passed the age
# guard. Cache dirs written by the pre-bucketing layout sit directly under
# the cache root with no owning-clone marker: their ownership is unknown, so
# they are never candidates — do not delete what you cannot attribute.
#
# How it decides "dead": the cache key is the first 16 hex chars of sha256 over
# the worktree root path, so the live set is `git worktree list --porcelain -z`
# from this checkout, hashed the same way. A cache dir whose key is not in the
# live set belonged to a worktree that is gone.
#
# The age guard: a worktree can be disposed while a lint run is still writing
# to its cache, and a freshly-dead key may also be a lane that will be
# re-created at the same path (agent lanes re-use paths). Only keys whose
# cache dir has carried an mtime older than GOLANGCI_SWEEP_MIN_AGE_HOURS
# (default 48) are swept. The dir's own mtime is a lagging proxy for last
# use — writes into existing shard dirs do not refresh it — so the guard
# buys hysteresis, not a guarantee that nothing live was written within the
# window. A lane re-created at the same path re-enters the live set by its
# key, so re-created lanes are protected by the key check, not the clock.
#
# Safety: only 16-hex-named directories directly under this repository's
# bucket are candidates; everything else in the cache root is left alone. If
# `git worktree list` fails or names no registered worktrees, the live set is
# unknown or unusable and the script fails without deleting anything.
#
# Usage: golangci-cache-sweep.sh [--dry-run]
#   GOLANGCI_SWEEP_MIN_AGE_HOURS=168 golangci-cache-sweep.sh  # keep a week
# A stale registration (a prunable worktree) keeps its cache alive until the
# registration is pruned; this script never mutates repository state to
# "fix" that.
set -eu

usage() { awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"; }

dry_run=0
for arg in "$@"; do
	case "$arg" in
	--dry-run) dry_run=1 ;;
	-h|--help) usage; exit 0 ;;
	*) echo "golangci-cache-sweep: unknown argument: $arg" >&2; usage >&2; exit 2 ;;
	esac
done

min_age_hours=${GOLANGCI_SWEEP_MIN_AGE_HOURS:-48}
case "$min_age_hours" in
	''|*[!0-9]*|0) echo "golangci-cache-sweep: GOLANGCI_SWEEP_MIN_AGE_HOURS must be a positive integer, got '$min_age_hours'" >&2; exit 2 ;;
esac

cache_root="${XDG_CACHE_HOME:-${HOME:?HOME must be set}/.cache}/evener/golangci-lint"

# The live key set: every registered worktree root (the main checkout
# included), hashed exactly as scripts/lib/golangci-lint-cache.sh hashes.
# Git 2.36 introduced -z for worktree list; earlier versions cannot safely
# represent a root containing a newline, so they cannot supply a live set.
git_version=$(git --version 2>/dev/null) || git_version=
git_version=${git_version#git version }
git_version=${git_version%% *}
git_major=${git_version%%.*}
git_minor=${git_version#*.}
git_minor=${git_minor%%.*}
case "$git_major:$git_minor" in
	:*|*:|*[!0-9:]*)
		echo "golangci-cache-sweep: git >= 2.36 is required for lossless worktree listing; refusing to sweep" >&2
		exit 1
		;;
esac
if [ "$git_major" -lt 2 ] || { [ "$git_major" -eq 2 ] && [ "$git_minor" -lt 36 ]; }; then
	echo "golangci-cache-sweep: git >= 2.36 is required for lossless worktree listing; refusing to sweep" >&2
	exit 1
fi
# A file keeps NUL bytes intact and lets the guarded git command retain its
# status; a pipeline ending in xargs would otherwise mask a git failure.
worktree_stream=$(mktemp "${TMPDIR:-/tmp}/golangci-cache-sweep.XXXXXXXX") || exit 1
trap 'rm -f "$worktree_stream"' 0
if ! git worktree list --porcelain -z > "$worktree_stream" 2>/dev/null; then
	echo "golangci-cache-sweep: not inside a workable git repository; the live worktree set is unknown, refusing to sweep" >&2
	exit 1
fi
# A bare common repository contributes a metadata entry but no worktree root;
# its linked worktrees remain usable. An empty registered set cannot safely
# classify any cache as dead.
if [ ! -s "$worktree_stream" ]; then
	echo "golangci-cache-sweep: no registered worktree roots; the live set is unusable, refusing to sweep" >&2
	exit 1
fi
# The repository bucket, derived exactly as scripts/lib/golangci-lint-cache.sh
# derives it: sha256-16 over the absolute Git common directory
# (--path-format=absolute needs git >= 2.31). Every worktree of this clone
# resolves to the same common directory and no other clone does, so the
# bucket is this repository's alone. A resolution failure refuses the run:
# without the bucket the sweeper cannot know which caches are this clone's
# to judge.
if ! common_dir="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || [ -z "$common_dir" ]; then
	echo "golangci-cache-sweep: this repository's common directory could not be resolved; which caches are this clone's is unknown, refusing to sweep" >&2
	exit 1
fi
# The hash is the LAST pipeline element into the guarded substitution; the
# longer comment at the live-set hashing below explains why the cut stays
# downstream of the guard.
if ! repo_shasum="$(printf '%s' "$common_dir" | shasum -a 256)"; then
	echo "golangci-cache-sweep: hashing the repository's common directory failed; refusing to sweep" >&2
	exit 1
fi
repo_key="$(printf '%s' "$repo_shasum" | cut -c1-16)"
sweep_root="$cache_root/$repo_key"
# xargs -0 preserves every field, including a newline in a root. Its shell
# emits only fixed-format hash and bare markers, so awk never sees a raw path.
# shasum is the last element of each hash pipeline. A failed hash exits its
# xargs child explicitly; the guarded substitution tests xargs' status, so no
# later hash masks the failure.
# The quoted body is expanded by the xargs child, not by this parent shell.
# shellcheck disable=SC2016
if ! field_keys="$(xargs -0 sh -c '
	for field do
		case "$field" in
		"worktree "*)
			root=${field#worktree }
			# The helper reads this path through command substitution, which
			# strips trailing newlines while preserving embedded newlines.
			root=$(printf "%s" "$root")
			if [ -z "$root" ]; then
				echo "golangci-cache-sweep: empty worktree root in porcelain; refusing to sweep" >&2
				exit 1
			fi
			hash=$(printf "%s" "$root" | shasum -a 256) || exit 1
			printf "hash %.16s\n" "$hash"
			;;
		bare) printf "bare\n" ;;
		"") printf "\n" ;;
		esac
	done
' sh < "$worktree_stream")"; then
	echo "golangci-cache-sweep: hashing the live worktree roots failed; the live set is incomplete, refusing to sweep" >&2
	exit 1
fi
live_keys="$(printf '%s\n' "$field_keys" | awk '
	/^hash / { root = substr($0, 6); next }
	/^bare$/ { root = ""; next }
	/^$/ { if (root != "") print root; root = ""; next }
	END { if (root != "") print root }
')"
if [ -z "$live_keys" ]; then
	echo "golangci-cache-sweep: no registered worktree roots; the live set is unusable, refusing to sweep" >&2
	exit 1
fi

[ -d "$sweep_root" ] || { echo "$(date -Iseconds) golangci-cache-sweep: no cache bucket for this repository at $sweep_root; nothing to sweep"; exit 0; }

# Candidates remain individual argv paths from find's bucket, name, and age
# filters through deletion. Neither newlines nor whitespace in a cache root
# may turn a candidate into a different deletion target.
if ! find "$sweep_root" -mindepth 1 -maxdepth 1 -type d \
	-name '[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]' \
	'!' -newermt "-${min_age_hours} hours" \
	-exec sh -c '
		set -e
		live_keys=$1 dry_run=$2 min_age_hours=$3
		shift 3
		for dir do
			key=$(basename "$dir")
			if printf "%s\n" "$live_keys" | grep -qxF "$key"; then
				continue
			fi
			size_kb=$(du -sk "$dir" | cut -f1)
			if [ "$dry_run" -eq 1 ]; then
				echo "would sweep $key (${size_kb}K, no worktree has this cache key and the cache has carried an mtime older than ${min_age_hours}h)"
				continue
			fi
			# Every dir arrives as one find-filtered argv element, not a split list.
			rm -rf "$dir"
			echo "swept $key (${size_kb}K, no worktree has this cache key and the cache has carried an mtime older than ${min_age_hours}h)"
		done
	' sh "$live_keys" "$dry_run" "$min_age_hours" {} +; then
	echo "golangci-cache-sweep: finding or deleting cache candidates failed" >&2
	exit 1
fi

if [ "$dry_run" -eq 1 ]; then
	echo "$(date -Iseconds) dry run complete; nothing deleted under $sweep_root (min age ${min_age_hours}h)"
else
	echo "$(date -Iseconds) swept $sweep_root (this repository's dead caches older than ${min_age_hours}h are gone; younger dead caches kept; pre-bucketing entries directly under $cache_root have unknown owners and are never swept)"
fi
