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
# Safety: four things must ALL hold before a worktree is removed. Its branch
# must have commits of its own (read from the branch's reflog - see the
# classifier below), those commits must have landed, the checkout must hold no
# git-ignored content this repo cannot regenerate, and `git worktree remove`
# must accept it without --force. The worktree you are standing in and the MAIN
# checkout are never candidates at all. ~/.serf and ~/.local/state/serf are
# never touched. Emptying the build cache costs a cold rebuild (~2-3 min) and
# nothing else.
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
		sed -n '2,64p' "$0" | sed 's/^# \{0,1\}//'
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

	# GOCACHE reachability (kata 98x9, reopened): the build cache now lives on
	# a second, external volume by design (scripts/setup-gocache.sh) precisely
	# BECAUSE it was the fastest-growing consumer on this one — one `go test
	# -c` of the biggest package alone grows it ~1G from warm. That moves the
	# risk, it does not remove it: an external volume can be unmounted, and a
	# GOCACHE pointing at a path that no longer exists must fail loudly, not
	# mysteriously. `go build` itself already does fail on this — confirmed by
	# actually unmounting-equivalent testing, not reasoned about — with
	# `mkdir /Volumes/X: permission denied`, but that message names neither
	# GOCACHE nor "unmounted", and would surface once per module mid-wave
	# rather than once, up front, with a diagnosis. Do the same probe go
	# itself does (mkdir -p; a no-op if the path already exists) so the
	# diagnosis lands here instead.
	#
	# SERF_SKIP_GOCACHE_CHECK=1 skips this whole block. Used only by
	# disk-reclaim-selftest.sh's floor-only scenario, to keep that scenario's
	# "silent above the floor" assertion independent of whatever GOCACHE
	# happens to be set to on the machine running the test; dedicated
	# scenarios cover this block itself with GOCACHE pinned explicitly.
	if [ "${SERF_SKIP_GOCACHE_CHECK:-0}" != 1 ]; then
		gocache=$(go env GOCACHE 2>/dev/null)
		if [ -n "$gocache" ]; then
			mkdir_err=$({ mkdir -p "$gocache"; } 2>&1)
			mkdir_status=$?
			if [ "$mkdir_status" -ne 0 ]; then
				cat >&2 <<-MSG
					disk-reclaim: GOCACHE is set to "$gocache" but it could not be created — kata 98x9.
					  $mkdir_err
					This is what an unmounted build-cache volume looks like. If it lives on an
					external volume, remount it and re-run. To point GOCACHE somewhere else,
					run: scripts/setup-gocache.sh <path>
				MSG
				exit 1
			fi
			# Drift warning, not a failure: GOCACHE reachable but back on the same
			# volume as this checkout defeats the point of moving it off in the
			# first place (kata 98x9) — warn so a machine that never ran
			# setup-gocache.sh, or had it reset by a Go toolchain reinstall, does
			# not silently regress to the original failure mode.
			gocache_dev=$(df -P "$gocache" 2>/dev/null | awk 'NR==2 {print $1}')
			repo_dev=$(df -P "$repo_root" 2>/dev/null | awk 'NR==2 {print $1}')
			if [ -n "$gocache_dev" ] && [ "$gocache_dev" = "$repo_dev" ]; then
				echo "disk-reclaim: warning: GOCACHE ($gocache) is on the same volume as this checkout — kata 98x9. Run scripts/setup-gocache.sh to move it to a bigger volume." >&2
			fi
		fi
	fi

	# The floor stays at 5 even now that GOCACHE has moved off this volume by
	# default (see scripts/setup-gocache.sh): the fastest-growing single
	# consumer left, but repo_root still sits at ~11G free of 228G (95% used)
	# from worktree checkouts, node_modules, and — per kata smw0 — ~1.6G of
	# orphaned pre-rename checkouts this script cannot even see. That is
	# slower growth than the old ~1G-per-build-cache-touch, not zero growth,
	# and 11G is only ~2x today's floor. Lowering it would shorten the
	# warning window for exactly the kind of slow creep that caused the
	# SECOND occurrence of this kata. Re-measure before changing it.
	min_gb=${SERF_DISK_MIN_FREE_GB:-5}
	avail_kb=$(df -k "$repo_root" | awk 'NR==2 {print $4}')
	min_kb=$((min_gb * 1024 * 1024))
	if [ "${avail_kb:-0}" -lt "$min_kb" ]; then
		avail_gb=$((avail_kb / 1024 / 1024))
		cache_line=""
		# --cache only helps THIS volume's floor if GOCACHE is actually on it;
		# once moved off (the default now), emptying it does nothing here.
		if [ -n "${gocache_dev:-}" ] && [ "$gocache_dev" = "${repo_dev:-}" ]; then
			cache_line="  scripts/disk-reclaim.sh --cache       # empty the Go build cache (fully regenerable, ~2-3min cold rebuild)
"
		fi
		cat >&2 <<-MSG
			disk-reclaim: only ${avail_gb}G free on $repo_root (floor is ${min_gb}G) — kata 98x9.
			Left alone this shows up as an unrelated-looking failure instead of this message:
			"link: mapping output file failed: no space left on device", a t.TempDir() setup
			failure, or a jobstore open error. Free some space, then re-run:
			${cache_line}  scripts/disk-reclaim.sh --worktrees   # remove worktrees already merged into HEAD
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

# The commit a branch was CREATED at, read from the oldest entry of the
# branch's own reflog. Empty when the branch has no reflog (deleted, expired,
# or core.logAllRefUpdates off) - `git reflog show` prints nothing and exits 0
# for a ref with no log, so an absent log reads as "cut point unknown", never
# as the branch's whole history.
branch_cut_point() {
	git reflog show --no-abbrev --format=%H "refs/heads/$1" 2>/dev/null | tail -1
}

# Did anyone ever COMMIT on this branch? Every reflog message git writes for a
# commit starts with "commit" ("commit:", "commit (initial):", "commit
# (amend):", "commit (merge):"); reflog messages are not translated, so this
# does not vary by locale. A branch that only ever fast-forwarded to catch up
# with its base has no such entry - its messages are "merge <ref>:
# Fast-forward" or "reset: moving to <ref>".
#
# Read into a variable rather than piping into grep: this script runs under
# `pipefail`, and grep -q closing the pipe early makes git exit on SIGPIPE,
# which would turn every match into a non-zero pipeline status.
branch_has_own_commit() {
	local msgs
	msgs=$(git reflog show --format=%gs "refs/heads/$1" 2>/dev/null)
	grep -q '^commit' <<<"$msgs"
}

classify_worktree() {
	local path="$1" branch="$2" tip cut own
	[ -n "$path" ] || return
	[ "$path" = "$here" ] && return
	# The MAIN checkout is never a removal candidate. It reached the candidate
	# list once (reported as "kept main (dirty or in use)") and was saved only
	# by git's own refusal to remove a main working tree - which is luck, not a
	# rule this script gets to rely on.
	[ "$path" = "$main_root" ] && return
	[ -n "$branch" ] || return # detached HEAD: nothing to classify against
	# "Removable" needs BOTH halves: the branch has commits of its own, AND
	# those commits have landed. Ancestry answers only the second half. It
	# cannot tell "contributed nothing because it merged" from "contributed
	# nothing because nobody has started" - a branch that has not diverged yet
	# is trivially an ancestor of what it was cut from - and the dirty-check
	# does not save an untouched checkout, because a fresh checkout nobody has
	# written to is perfectly clean. That deleted six live agent worktrees
	# ninety seconds after they were created.
	#
	# `tip == into_tip` was the first attempt at the missing half. It is only
	# true at cut time: let two merges land on the base between `worktree add`
	# and this run and every fresh branch has a tip that is no longer the
	# base's while still being its ancestor. That deleted five more, and is why
	# the first half is now read from the branch's own reflog instead: the
	# commit the branch was created at, and whether anyone has committed on it
	# since. Both are facts about THIS branch, so neither moves when the base
	# does.
	#
	# Every unknown resolves to unmerged - unresolvable branch, no reflog, no
	# commits of its own. This script's failure mode must always be "kept
	# something it could have removed", never the reverse.
	tip=$(git rev-parse --verify --quiet "$branch^{commit}" 2>/dev/null) || tip=""
	cut=$(branch_cut_point "$branch")
	# How far the branch has moved since it was created. An empty answer is
	# rev-list declining to walk it - a cut point pruned out of the object store,
	# say - and reads as zero, because an unknown has to be kept.
	own=$(git rev-list --count "$cut..$branch" 2>/dev/null)
	if [ -z "$tip" ]; then
		unmerged+=("$path	$branch (unresolvable)")
	elif [ "$tip" = "$into_tip" ]; then
		# The base points AT this branch's tip. Whether it got there by an
		# ff-merge of this branch or because the branch never moved, someone
		# may still be standing in the checkout; keep it either way.
		unmerged+=("$path	$branch (is the base tip)")
	elif [ -z "$cut" ]; then
		unmerged+=("$path	$branch (no reflog: cut point unknown)")
	elif ! branch_has_own_commit "$branch"; then
		unmerged+=("$path	$branch (unstarted)")
	elif [ "${own:-0}" = 0 ]; then
		# Committed on, then moved back to where it started: nothing of its own
		# survives, so it is unstarted again.
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

# Content this repo can rebuild from a fresh checkout, and nothing else. Every
# other git-ignored path in a worktree is treated as somebody's work (see
# worktree_ignored_work). Adding a row here is a deliberate act: get it wrong
# and the script deletes the thing you named. Forgetting to add a row only
# costs a reclaim opportunity, which is the failure direction this script is
# allowed to have.
is_regenerable_ignored_path() {
	case "${1%/}" in
	node_modules | node_modules/* | */node_modules | */node_modules/*) return 0 ;;
	.build | .build/*) return 0 ;;
	.DS_Store | */.DS_Store) return 0 ;;
	serf | serf-hub | serf-tui | serf-doctor | serf-namingcheck | serfeval | llmcall | serf-linux-amd64) return 0 ;;
	esac
	return 1
}

# Prints the first git-IGNORED path in a worktree that this repo cannot
# regenerate, or nothing if there is none.
#
# `git worktree remove` without --force refuses a worktree with modified
# tracked files or untracked files, and that refusal is this script's backstop
# against removing a checkout somebody is standing in. It does not see IGNORED
# content, by design - git considers such a checkout clean. So an agent whose
# only writes so far went into `.superpowers/` sits in a "clean" worktree, and
# a long-merged worktree holding a `.superpowers/sdd/` archive was silently
# deleted with it. Ignored is not the same as disposable.
#
# -unormal: `--porcelain` alone implies `-uall`, which expands an ignored
# directory into every file underneath it - tens of thousands of lines for a
# real node_modules. -unormal collapses each to a single entry.
# --no-optional-locks: this reads OTHER agents' live worktrees; do not let a
# status refresh write to an index somebody else is using.
worktree_ignored_work() {
	local path="$1" entry
	while IFS= read -r -d '' entry; do
		[ "${entry:0:2}" = "!!" ] || continue
		if ! is_regenerable_ignored_path "${entry:3}"; then
			printf '%s' "${entry:3}"
			return
		fi
	done < <(git --no-optional-locks -C "$path" status --porcelain -unormal --ignored -z 2>/dev/null)
}

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
		kept_ignored=0
		for entry in "${merged[@]}"; do
			path=${entry%%	*}
			branch=${entry##*	}
			# Before touching anything: does this checkout hold ignored content
			# nobody can regenerate? Ask BEFORE dropping the node_modules
			# symlink below, so a worktree that is about to be kept is left
			# exactly as it was found.
			held=$(worktree_ignored_work "$path")
			if [ -n "$held" ]; then
				echo "  kept     $branch	(holds ignored work: $held)"
				kept_ignored=$((kept_ignored + 1))
				continue
			fi
			# Drop the shared-install symlink first: `git worktree remove` will
			# not delete a symlink's target, but leaving it makes the removal
			# noisier than it needs to be.
			nm="$path/cmd/serf-hub/frontend/node_modules"
			[ -L "$nm" ] && rm -f "$nm"
			# NO --force, deliberately. git refuses a worktree with
			# uncommitted changes, and that refusal is the last thing standing
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
	[ "${kept_ignored:-0}" -gt 0 ] && echo "Kept ${kept_ignored} merged worktree(s) holding git-ignored work; look before removing by hand."
fi

echo
echo "disk-reclaim: $(free_report)"
