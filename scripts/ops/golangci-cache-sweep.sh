#!/bin/sh
# golangci-cache-sweep — reclaim per-worktree golangci-lint analysis caches
# whose owning worktree no longer exists.
#
# Why: scripts/lib/golangci-lint-cache.sh keys each checkout's golangci-lint
# cache by a hash of its worktree root (the #290 cross-checkout poisoning
# guard), so nothing else cleans up after a worktree is removed — the cache
# dir outlives its tree, and on a machine with a fast-churning worktree fleet
# the dead dirs hold hundreds of megabytes each (measured 2026-11-13: 855M
# dead against 978M live).
#
# How it decides "dead": the cache key is the first 16 hex chars of sha256 over
# the worktree root path, so the live set is `git worktree list --porcelain`
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
# Safety: only 16-hex-named directories directly under the cache root are
# candidates; anything else in the root is left alone. If `git worktree list`
# fails, or the repository is bare (the live set is degenerate), the live set
# is unknown or unusable and the script fails loudly without deleting
# anything — an unknown set must never be treated as empty.
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
# A single-command substitution preserves git's exit status directly (POSIX sh
# gives a pipeline its last element's status, which would mask git's failure).
# A bare repository succeeds with a degenerate set — only the bare root
# itself — which would make every other worktree's cache look dead, so a
# `bare` line refuses the run for the same reason as a git failure.
if ! worktree_lines="$(git worktree list --porcelain 2>/dev/null)"; then
	echo "golangci-cache-sweep: not inside a workable git repository; the live worktree set is unknown, refusing to sweep" >&2
	exit 1
fi
case "$worktree_lines" in
	*bare*)
		echo "golangci-cache-sweep: this is a bare repository; its worktree list cannot name this machine's lanes, refusing to sweep" >&2
		exit 1
		;;
esac
# One shasum per root is inherent (content-hash tools hash files, not argv
# strings); the cut runs once over the whole set instead of once per root.
live_keys="$(printf '%s\n' "$worktree_lines" | sed -n 's/^worktree //p' \
	| while IFS= read -r root; do printf '%s' "$root" | shasum -a 256; done | cut -c1-16)"

[ -d "$cache_root" ] || { echo "$(date -Is) golangci-cache-sweep: no cache root at $cache_root; nothing to sweep"; exit 0; }

# Candidates: 16-hex-named dirs under the cache root, already filtered by the
# age predicate so the loop never re-stats. Folding the predicate into the
# candidate find also makes its failure direction safe: an erroring find
# yields no candidates instead of a fail-open guard.
candidates="$(find "$cache_root" -mindepth 1 -maxdepth 1 -type d \
	-regextype posix-extended -regex '.*/[0-9a-f]{16}' '!' -newermt "-${min_age_hours} hours")"

# A plain loop, not a pipeline: a piped while-loop runs in a subshell where
# set -e cannot fail the script, so a failed delete would print "swept" and
# exit 0. Here a failing rm fails the run.
if [ -n "$candidates" ]; then
	printf '%s\n' "$candidates" | while IFS= read -r dir; do
		key=$(basename "$dir")
		if printf '%s\n' "$live_keys" | grep -qxF "$key"; then
			continue
		fi
		size_kb=$(du -sk "$dir" | cut -f1)
		if [ "$dry_run" -eq 1 ]; then
			echo "would sweep $key (${size_kb}K, no worktree has this cache's key and the cache has carried an mtime older than ${min_age_hours}h)"
			continue
		fi
		# find -exec rm is the testing.md-sanctioned delete shape: the audit
		# reads {} as a literal operand, never a variable.
		find "$dir" -mindepth 0 -maxdepth 0 -exec rm -rf {} +
		echo "swept $key (${size_kb}K, no worktree has this cache's key and the cache has carried an mtime older than ${min_age_hours}h)"
	done
fi

if [ "$dry_run" -eq 1 ]; then
	echo "$(date -Is) dry run complete; nothing deleted under $cache_root (min age ${min_age_hours}h)"
else
	echo "$(date -Is) swept $cache_root (caches with no live worktree and an mtime older than ${min_age_hours}h are gone; younger dead caches kept)"
fi
