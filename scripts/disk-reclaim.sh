#!/usr/bin/env bash
# disk-reclaim.sh — report, and optionally reclaim, the disk this repo's own
# tooling consumes. Run it BEFORE a parallel-agent fleet run, and again when
# anything reports "no space left on device".
#
# Why this exists (kata 98x9): the Data volume has filled to 100% twice during
# fleet runs, at which point nothing can run at all — every tool call needs to
# write an output file. Worse, it does not announce itself. It surfaces as
# `link: mapping output file failed`, as t.TempDir() failures, as jobstore open
# errors — four test failures were once root-caused to transient exhaustion
# after being investigated as flakes. Recovery has needed a human each time.
#
# The three consumers, re-measured 2026-07-25 (the fleet had grown since the
# first pass the same day - these numbers move; re-run the report, don't trust
# either set):
#   go build cache      10-13G (largest by far, fully regenerable; a single
#                        `go test -c` of the biggest package alone grew it by
#                        ~1G from a warm cache - that is the real burn rate,
#                        not a one-time cost)
#   git worktrees       3.6G   (~80-90MB per fresh checkout, up to ~260MB with
#                        build artifacts; 22 registered)
#   frontend node_modules     one real install, symlinked from the rest
# Free space on the Data volume at measurement time: ~12-17G of 228G (92-94%
# used) - i.e. the whole floor most of this script cares about is smaller than
# ONE fresh package's build-cache growth, multiplied by however many agents in
# the fleet touch different code at once.
#
# A fourth consumer this script does NOT see or touch: ~40 checkouts under
# .claude/worktrees (~1.6G) whose `.git` file points at this repo's OLD path
# from before a directory rename, so `git worktree list` here has no record of
# them at all - `git worktree remove`/`prune` can't reach what it doesn't know
# about. Real, but small next to the build cache, and removing them needs a
# human to confirm they're truly abandoned rather than someone's stashed work
# on an old checkout of this same repo. See the filed follow-up kata.
#
# Usage:
#   scripts/disk-reclaim.sh                 # report only, changes nothing
#   scripts/disk-reclaim.sh --cache         # also empty the Go build cache
#   scripts/disk-reclaim.sh --worktrees     # also remove MERGED worktrees
#   scripts/disk-reclaim.sh --all           # both
#   scripts/disk-reclaim.sh --into <ref>    # "merged" means merged into <ref>
#                                           # (default: the current HEAD)
#   scripts/disk-reclaim.sh --check         # exit 1 with a specific message if
#                                           # free space is below the floor;
#                                           # silent, exit 0, otherwise. Fast:
#                                           # a bare df, nothing else. This is
#                                           # what scripts/run-module-tests.sh
#                                           # calls before every test run (kata
#                                           # 98x9): a silent-exhaustion failure
#                                           # is a mystery ("no space left on
#                                           # device" 40s into a build) unless
#                                           # something already on the path
#                                           # everyone runs catches it first.
#                                           # Floor defaults to 5 (GiB); override
#                                           # with SERF_DISK_MIN_FREE_GB.
#
# Safety: an unmerged worktree is NEVER removed, and neither is the one you are
# standing in. ~/.serf and ~/.local/state/serf are never touched. Emptying the
# build cache costs a cold rebuild (~2-3 min) and nothing else.
set -uo pipefail

RECLAIM_CACHE=0
RECLAIM_WORKTREES=0
CHECK_ONLY=0
INTO=""

while [ $# -gt 0 ]; do
	case "$1" in
	--cache) RECLAIM_CACHE=1 ;;
	--worktrees) RECLAIM_WORKTREES=1 ;;
	--check) CHECK_ONLY=1 ;;
	--all)
		RECLAIM_CACHE=1
		RECLAIM_WORKTREES=1
		;;
	--into)
		shift
		INTO="${1:-}"
		[ -n "$INTO" ] || {
			echo "disk-reclaim: --into needs a ref" >&2
			exit 2
		}
		;;
	-h | --help)
		sed -n '2,59p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "disk-reclaim: unknown argument: $1 (try --help)" >&2
		exit 2
		;;
	esac
	shift
done

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
	echo "disk-reclaim: not inside a git repository" >&2
	exit 1
}
cd "$repo_root" || exit 1

if [ "$CHECK_ONLY" = 1 ]; then
	# Deliberately does none of the reporting below: this runs on every single
	# test invocation across the fleet (wired into run-module-tests.sh), so it
	# has to be a single fast syscall, not a du of a 10G+ build cache or a
	# merge-base walk over twenty-odd worktrees.
	min_gb=${SERF_DISK_MIN_FREE_GB:-5}
	avail_kb=$(df -k "$repo_root" | awk 'NR==2 {print $4}')
	min_kb=$((min_gb * 1024 * 1024))
	if [ "${avail_kb:-0}" -lt "$min_kb" ]; then
		avail_gb=$((avail_kb / 1024 / 1024))
		cat >&2 <<-MSG
			disk-reclaim: only ${avail_gb}G free on $repo_root (floor is ${min_gb}G) — kata 98x9.
			Left alone this shows up as an unrelated-looking failure instead of this message:
			"link: mapping output file failed: no space left on device", a t.TempDir() setup
			failure, or a jobstore open error. Free some space, then re-run:
			  scripts/disk-reclaim.sh --cache       # empty the Go build cache (fully regenerable, ~2-3min cold rebuild)
			  scripts/disk-reclaim.sh --worktrees   # remove worktrees already merged into HEAD
			  scripts/disk-reclaim.sh --all         # both
			Override the floor for this run only: SERF_DISK_MIN_FREE_GB=<N> (default 5).
		MSG
		exit 1
	fi
	exit 0
fi

# The worktrees live beside the MAIN checkout, not beside whichever worktree you
# happen to be standing in - and --show-toplevel gives the latter. The common
# git dir is the main checkout's .git, so its parent is the main checkout.
main_root=$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")
worktrees_dir="$main_root/.claude/worktrees"

# --into defaults to HEAD: the usual question is "which worktrees have already
# landed in what I am standing on".
[ -n "$INTO" ] || INTO=$(git rev-parse HEAD)
git rev-parse --verify --quiet "$INTO" >/dev/null || {
	echo "disk-reclaim: no such ref: $INTO" >&2
	exit 1
}

into_tip=$(git rev-parse "$INTO^{commit}")

free_report() { df -h "$repo_root" | awk 'NR==2 {print $4 " free of " $2 " (" $5 " used)"}'; }

echo "disk-reclaim: $(free_report)"

gocache=$(go env GOCACHE 2>/dev/null)
if [ -n "$gocache" ] && [ -d "$gocache" ]; then
	echo "  go build cache   $(du -sh "$gocache" 2>/dev/null | cut -f1)	$gocache"
fi

# Classify every registered worktree as merged-into-$INTO or not. Only the
# merged ones are ever removal candidates.
#
# Parsed from `git worktree list --porcelain`, NOT the human-readable
# `git worktree list`: the human format appends a bare "locked" or "prunable"
# word after the `[branch]` bracket for a locked or administratively-stale
# worktree, and simple bracket-stripping folded that word into the branch name
# itself (e.g. "k6-worktreefollow] locked"). That name never resolves as a
# ref, so every locked/prunable worktree silently fell into the "unresolvable"
# bucket - never a data-loss risk (unresolvable is kept, same as unmerged),
# but it meant --worktrees could never even consider them. The porcelain
# format gives the branch on its own `branch refs/heads/<name>` line, with
# `locked`/`prunable` as separate lines entirely.
merged=()
unmerged=()
here=$(git rev-parse --show-toplevel)
classify_worktree() {
	local path="$1" branch="$2" tip
	[ -n "$path" ] || return
	[ "$path" = "$here" ] && return
	[ -n "$branch" ] || return # detached HEAD: nothing to classify against
	# A branch with no commits of its own is UNSTARTED, not merged - and
	# --is-ancestor cannot tell the difference, because a branch that has not
	# diverged yet is trivially an ancestor of what it was cut from. This
	# script deleted six worktrees ninety seconds after they were created for
	# six running agents, and the dirty-check did not save them: a fresh
	# checkout nobody has written to yet is perfectly clean.
	#
	# So "removable" needs both halves: the branch has commits of its own, AND
	# those commits have landed. Same tip as the base means the first half
	# fails, and the worktree is left alone.
	# An unresolvable branch is classified unmerged: this script's failure mode
	# must always be "kept something it could have removed", never the reverse.
	tip=$(git rev-parse --verify --quiet "$branch^{commit}" 2>/dev/null) || tip=""
	if [ -z "$tip" ]; then
		unmerged+=("$path	$branch (unresolvable)")
	elif [ "$tip" = "$into_tip" ]; then
		unmerged+=("$path	$branch (unstarted)")
	elif git merge-base --is-ancestor "$branch" "$INTO" 2>/dev/null; then
		merged+=("$path	$branch")
	else
		unmerged+=("$path	$branch")
	fi
}
wt_path="" wt_branch=""
while IFS= read -r line; do
	case "$line" in
	"worktree "*)
		wt_path=${line#worktree }
		wt_branch=""
		;;
	"branch "*)
		wt_branch=${line#branch }
		wt_branch=${wt_branch#refs/heads/}
		;;
	"detached") wt_branch="" ;;
	"")
		classify_worktree "$wt_path" "$wt_branch"
		wt_path=""
		wt_branch=""
		;;
	esac
done < <(git worktree list --porcelain
	echo)

wt_size=$(du -sh "$worktrees_dir" 2>/dev/null | cut -f1)
echo "  worktrees        ${wt_size:-0}	${#merged[@]} merged into ${INTO:0:12}, ${#unmerged[@]} unmerged"

# A real node_modules directory inside a worktree is a symptom worth naming: the
# fleet shares ONE install by symlink, and `npm ci` deletes its target before
# reinstalling, which has emptied the shared copy for every other worktree at
# once (four times). web-preflight refuses that now; this reports the residue.
real_nm=0
while IFS= read -r nm; do
	[ -L "$nm" ] || real_nm=$((real_nm + 1))
done < <(find "$worktrees_dir" -maxdepth 4 -name node_modules -not -path '*/node_modules/*' 2>/dev/null)
[ "$real_nm" -gt 1 ] && echo "  node_modules     $real_nm REAL installs under .claude/worktrees (expected 1; the rest should be symlinks)"

if [ "$RECLAIM_CACHE" = 0 ] && [ "$RECLAIM_WORKTREES" = 0 ]; then
	echo
	echo "Report only. Re-run with --cache, --worktrees, or --all to reclaim."
	[ "${#unmerged[@]}" -gt 0 ] && echo "Unmerged worktrees are never removed; ${#unmerged[@]} would be kept."
	exit 0
fi

if [ "$RECLAIM_CACHE" = 1 ]; then
	echo
	echo "Emptying the Go build cache (costs one cold rebuild)..."
	go clean -cache || echo "disk-reclaim: go clean -cache failed" >&2
fi

if [ "$RECLAIM_WORKTREES" = 1 ]; then
	echo
	if [ "${#merged[@]}" -eq 0 ]; then
		echo "No merged worktrees to remove."
	else
		echo "Removing ${#merged[@]} worktree(s) merged into ${INTO:0:12}:"
		kept_dirty=0
		for entry in "${merged[@]}"; do
			path=${entry%%	*}
			branch=${entry##*	}
			# Drop the shared-install symlink first: `git worktree remove` will
			# not delete a symlink's target, but leaving it makes the removal
			# noisier than it needs to be.
			nm="$path/cmd/serf-hub/frontend/node_modules"
			[ -L "$nm" ] && rm -f "$nm"
			# NO --force, deliberately. git refuses a worktree with
			# uncommitted changes, and that refusal is the only thing standing
			# between this script and another agent's live work: "merged" only
			# says the BRANCH landed, not that nobody is standing in the
			# checkout right now. A skip here is the safe outcome.
			if git worktree remove "$path" >/dev/null 2>&1; then
				echo "  removed  $branch"
			else
				echo "  kept     $branch	(dirty or in use)"
				kept_dirty=$((kept_dirty + 1))
			fi
		done
	fi
	[ "${#unmerged[@]}" -gt 0 ] && echo "Kept ${#unmerged[@]} unmerged worktree(s)."
	[ "${kept_dirty:-0}" -gt 0 ] && echo "Kept ${kept_dirty} merged worktree(s) that were dirty or in use."
fi

echo
echo "disk-reclaim: $(free_report)"
