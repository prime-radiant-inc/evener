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
# Safety: only 16-hex-named directories directly under this repository's
# bucket are candidates; everything else in the cache root is left alone. If
# `git worktree list` fails, or the repository is bare (the live set is
# degenerate), the live set is unknown or unusable and the script fails loudly
# without deleting anything — an unknown set must never be treated as empty.
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
if ! worktree_lines="$(git worktree list --porcelain 2>/dev/null)"; then
	echo "golangci-cache-sweep: not inside a workable git repository; the live worktree set is unknown, refusing to sweep" >&2
	exit 1
fi
# A bare repository succeeds with a degenerate set — only the bare root
# itself — which would make every other worktree's cache look dead, so a
# `bare` attribute line refuses the run for the same reason as a git failure.
# The match is anchored to the full porcelain attribute line ("bare" on its
# own line), not the substring, so a worktree path or branch name containing
# "bare" cannot refuse a legitimate run.
case "$worktree_lines" in
	*'
bare'*)
		echo "golangci-cache-sweep: this is a bare repository; its worktree list cannot name this machine's lanes, refusing to sweep" >&2
		exit 1
		;;
esac
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
# One shasum per root is inherent (content-hash tools hash files, not argv
# strings); the cut runs once over the whole set instead of once per root.
# The hash loop runs as the LAST pipeline element into a command
# substitution whose status the if tests directly — a cut downstream of it
# would mask a shasum failure the way POSIX pipelines masked git's in the
# worktree-list guard above. Empty input (no worktree lines) would also
# produce empty output, so the loop only succeeds when it hashed at least
# one root.
if ! shasum_lines="$(printf '%s\n' "$worktree_lines" | sed -n 's/^worktree //p' \
	| while IFS= read -r root; do printf '%s' "$root" | shasum -a 256; done)"; then
	echo "golangci-cache-sweep: hashing the live worktree roots failed; the live set is incomplete, refusing to sweep" >&2
	exit 1
fi
live_keys="$(printf '%s\n' "$shasum_lines" | cut -c1-16)"

[ -d "$sweep_root" ] || { echo "$(date -Iseconds) golangci-cache-sweep: no cache bucket for this repository at $sweep_root; nothing to sweep"; exit 0; }

# Candidates: 16-hex-named dirs under this repository's bucket, old enough
# to sweep. Both predicates stay in find — -name's bracket glob is portable
# where GNU find's -regextype is not, and -newermt runs on the BSD find macOS
# ships — and keeping the name filter out of the shell also keeps it
# executable under macOS's /bin/sh (bash 3.2): a `case` inside a command
# substitution ends the substitution at the first case pattern's `)`, a break
# `sh -n` cannot see because it only fires when the substitution runs. A
# find that errors fails the run now — the substitution preserves its
# status under set -e — instead of yielding a fail-open empty candidate set.
candidates="$(find "$sweep_root" -mindepth 1 -maxdepth 1 -type d \
	-name '[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]' \
	'!' -newermt "-${min_age_hours} hours")"

# The delete loop reads the candidate list through a pipe, so the loop body
# runs in a subshell — but a failing rm still fails the run: the subshell
# inherits set -e, exits before "swept" prints, and its status is the
# pipeline's, which set -e propagates.
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
		# This delete is the behaviour the script exists for, so it takes the
		# auditable variable-fed form and sits on the count-pinned list of
		# the recursive-delete audit (recursiveDeleteAllowedLines in
		# internal/devtool/repoaudit) with this reason: $dir comes only from
		# the candidates find above — a 16-hex dir inside this repository's
		# own bucket, past the age guard and absent from the live set — so
		# the delete cannot be handed a path from outside the candidate set.
		rm -rf "$dir"
		echo "swept $key (${size_kb}K, no worktree has this cache's key and the cache has carried an mtime older than ${min_age_hours}h)"
	done
fi

if [ "$dry_run" -eq 1 ]; then
	echo "$(date -Iseconds) dry run complete; nothing deleted under $sweep_root (min age ${min_age_hours}h)"
else
	echo "$(date -Iseconds) swept $sweep_root (this repository's dead caches older than ${min_age_hours}h are gone; younger dead caches kept; pre-bucketing entries directly under $cache_root have unknown owners and are never swept)"
fi
