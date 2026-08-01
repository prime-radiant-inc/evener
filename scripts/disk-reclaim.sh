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
# These numbers move a lot. Re-run the report; do not trust any set of them,
# including this one (2026-07-30, at 3G free of 228G):
#   go build cache      111G, on an EXTERNAL volume by design
#                        (scripts/setup-gocache.sh). Fully regenerable, still
#                        the fastest-growing thing here - one `go test -c` of
#                        the biggest package adds ~1G from warm - but while it
#                        lives off this volume, emptying it cannot move this
#                        volume's floor at all, and the report says so instead
#                        of spending 32.6s measuring it.
#   /tmp scratch        10.0G across 120 `serf*` entries, SAME volume as the
#                        checkout: per-session scratch checkouts (~270M each),
#                        stray per-session build caches, chrome profiles, DOM
#                        dumps, logs. The biggest reclaimable pocket there has
#                        been, and nothing here deletes it.
#                        See scripts/report-tmp-debris.sh.
#   git worktrees       8.3G across 77 checkouts - of which 6.7G is live agent
#                        work this script keeps, 1.6G is unregistered (below),
#                        and the removable share was ZERO.
#   frontend node_modules     one real install, symlinked from the rest
#
# That zero is the shape to remember, not the number: every merged worktree
# held its agent's `.superpowers/<kata>-report.md`, so every one was correctly
# classified as merged and correctly kept as holding ignored work. A fleet that
# writes reports into its worktrees will keep producing that result, so the
# report gives the removable share its own measured line rather than letting a
# whole-directory total imply a yield (kata td3g).
#
# Two consumers this script reports but never removes, because for both of them
# git's own "is it safe" check is unavailable:
#   - ~42 checkouts under .claude/worktrees (~1.6G) whose `.git` file points at
#     this repo's OLD path from before a directory rename, so `git worktree
#     list` has no record of them and prune/remove cannot reach them (kata
#     smw0). scripts/report-orphaned-worktrees.sh inspects them.
#   - the /tmp scratch above (kata gmpr). scripts/report-tmp-debris.sh.
# Both need a human: a scratch checkout can hold a never-pushed experiment.
#
# Usage:
#   scripts/disk-reclaim.sh                 # report only, changes nothing. Its
#                                           # touches of the GOCACHE volume are
#                                           # bounded the same way --check's
#                                           # are, so a stalled volume is named
#                                           # in the report instead of
#                                           # swallowing it (kata 6jxs).
#   scripts/disk-reclaim.sh --cache         # also empty the Go build cache
#   scripts/disk-reclaim.sh --worktrees     # also remove MERGED worktrees
#   scripts/disk-reclaim.sh --all           # both
#   scripts/disk-reclaim.sh --into <ref>    # "merged" means merged into <ref>
#                                           # (default: the current HEAD)
#   scripts/disk-reclaim.sh --check         # exit 1 with a specific message if
#                                           # free space is below the floor, or
#                                           # if the GOCACHE volume is gone or
#                                           # stalled; silent, exit 0, otherwise.
#                                           # Fast: a bare df and a bounded probe
#                                           # of GOCACHE. This is
#                                           # what scripts/run-module-tests.sh
#                                           # calls before every test run (kata
#                                           # 98x9): a silent-exhaustion failure
#                                           # is a mystery ("no space left on
#                                           # device" 40s into a build) unless
#                                           # something already on the path
#                                           # everyone runs catches it first.
#                                           # Floor defaults to 5 (GiB); override
#                                           # with SERF_DISK_MIN_FREE_GB. The
#                                           # probe gives up after 10s; override
#                                           # with SERF_GOCACHE_PROBE_TIMEOUT.
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
		# Everything from the shebang to the first non-comment line. A
		# hardcoded range silently truncates --help mid-sentence the first
		# time the header grows, which is exactly what it had done here.
		awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"
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

# Long enough for a sleeping external disk to spin up and answer, and three
# orders of magnitude below what a real stall costs.
gocache_probe_timeout=${SERF_GOCACHE_PROBE_TIMEOUT:-10}

# Runs one command that touches the GOCACHE volume, bounded. Prints whatever
# the command wrote (stdout and stderr together) and returns its exit status —
# or prints nothing and returns 124, the code timeout(1) uses, if it never
# answered. timeout(1) itself is not on a stock macOS.
#
# Every touch of that path goes through here except the report's one `du`,
# which says at its call site why it cannot be bounded. The build-cache
# volume's worst failure is not being gone, it is STALLING while still
# mounted: nothing fails, everything blocks. `mkdir`, `[ -d ]`, `df` and `du`
# all block against it, and so does every go command that touches the cache —
# four go processes were found sleeping 12-23 HOURS on one such stall, on a
# volume that answered `ls` instantly by the time anyone looked (kata r07s).
# Unbounded, --check handed that hang to every test run on the machine (kata
# r07s) and the report printed its first line and then nothing at all (6jxs).
gocache_probe() { # seconds command...
	local limit="$1" out pid polls status
	shift
	out=$(mktemp "${TMPDIR:-/tmp}/disk-reclaim-gocache-probe.XXXXXX") || return 125
	# Backgrounded as a plain command so $! is the probe process itself and not
	# a wrapper shell: the kill below has to reach whatever is actually blocked
	# on the volume. Its output goes to a FILE and never to this function's own
	# stdout, so a probe that outlives the kill cannot hold a caller's command
	# substitution open — which would be the very hang this bound exists to end.
	#
	# SERF_GOCACHE_PROBE_CMD replaces the probe. No filesystem can be made to
	# stall on demand, so this is the seam disk-reclaim-selftest.sh uses to pin
	# the stall outcome; nothing else sets it.
	if [ -n "${SERF_GOCACHE_PROBE_CMD:-}" ]; then
		# shellcheck disable=SC2086 # the seam is a command line, not one word
		$SERF_GOCACHE_PROBE_CMD >"$out" 2>&1 &
	else
		"$@" >"$out" 2>&1 &
	fi
	pid=$!
	# Wait for the probe's own answer, 20 polls a second: an awake volume
	# answers inside the first one, and that is what this costs on every test
	# invocation across the fleet.
	polls=$((limit * 20))
	while [ "$polls" -gt 0 ] && kill -0 "$pid" 2>/dev/null; do
		sleep 0.05
		polls=$((polls - 1))
	done
	if kill -0 "$pid" 2>/dev/null; then
		# Kill it rather than wait on it. A process blocked on a stalled volume
		# can outlive the signal, and waiting here for it to die would be that
		# same hang again; leaving it alive would keep the blocked I/O open with
		# nobody watching.
		kill -KILL "$pid" 2>/dev/null
		# bash announces a signal-killed background job on stderr the next time
		# it forks, which would staple a "Killed: 9" line onto the caller's
		# diagnosis. Dropping the job from bash's own table leaves the kill
		# exactly as it is and the diagnosis alone.
		disown "$pid" 2>/dev/null
		rm -f "$out"
		return 124
	fi
	wait "$pid"
	status=$?
	cat "$out"
	rm -f "$out"
	return "$status"
}

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
	# That probe goes through gocache_probe, which BOUNDS it, because the
	# volume's other failure mode is worse than being gone: when it STALLS,
	# mkdir does not fail, it blocks — and so does every go command that
	# touches the cache, invisibly, for as long as the stall lasts (kata r07s).
	# An unbounded probe would inherit that hang and hand it to every test run
	# on the machine, which is the opposite of the job.
	#
	# SERF_SKIP_GOCACHE_CHECK=1 skips this whole block. Used only by
	# disk-reclaim-selftest.sh's floor-only scenario, to keep that scenario's
	# "silent above the floor" assertion independent of whatever GOCACHE
	# happens to be set to on the machine running the test; dedicated
	# scenarios cover this block itself with GOCACHE pinned explicitly.
	if [ "${SERF_SKIP_GOCACHE_CHECK:-0}" != 1 ]; then
		# Safe on a stalled volume: go resolves GOCACHE from the environment
		# and the go env file (cache.DefaultDir) and does not touch the
		# directory until something needs to read or write the cache.
		gocache=$(go env GOCACHE 2>/dev/null)
		if [ -n "$gocache" ]; then
			probe_err=$(gocache_probe "$gocache_probe_timeout" mkdir -p "$gocache")
			probe_status=$?
			if [ "$probe_status" -eq 124 ]; then
				cat >&2 <<-MSG
					disk-reclaim: GOCACHE at "$gocache" did not answer in ${gocache_probe_timeout}s — the volume it lives on is STALLED (kata r07s).
					A stalled build-cache volume fails nothing; it blocks. Every go command that
					touches the cache waits on it without saying so — measured at 12 to 23 hours,
					looking like anything except its cause.
					Wake or remount the volume and re-run: this probe touched it, so a volume that
					was only asleep may answer next time. To point GOCACHE at another path, run:
					scripts/setup-gocache.sh <path>
				MSG
				exit 1
			fi
			if [ "$probe_status" -ne 0 ]; then
				cat >&2 <<-MSG
					disk-reclaim: GOCACHE is set to "$gocache" but it could not be created — kata 98x9.
					  $probe_err
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
			#
			# Bounded like the probe above: this is a SECOND touch of the same
			# path, and a volume that answered the probe can stall before it.
			# An unanswered df leaves gocache_dev empty, which reads as "not the
			# same volume" — the quiet direction, and the right one for a drift
			# warning when the probe has already passed.
			gocache_df=$(gocache_probe "$gocache_probe_timeout" df -P "$gocache")
			gocache_dev=$(awk 'NR==2 {print $1}' <<<"$gocache_df")
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
		# --cache only helps THIS volume's floor if GOCACHE is actually on it;
		# once moved off (the default now), emptying it does nothing here. The
		# same is true of --all, which is only "--cache and --worktrees" — an
		# unconditional "--all # both" pointed at a lever the line above it had
		# just deliberately declined to name (kata td3g).
		cache_lines=""
		if [ -n "${gocache_dev:-}" ] && [ "$gocache_dev" = "${repo_dev:-}" ]; then
			cache_lines="  scripts/disk-reclaim.sh --cache       # empty the Go build cache (regenerable, ~2-3min cold rebuild)
  scripts/disk-reclaim.sh --all         # both of the above
"
		fi
		# Everything this message says about yield has to be something a bare
		# df can substantiate, which is nothing: --check runs on every test
		# invocation across the fleet and cannot afford to measure (kata 98x9).
		# So it names what CAN measure rather than promising an outcome. It used
		# to send the reader straight to --worktrees, and on the night this was
		# written that was worth exactly 0 bytes — every merged worktree held an
		# agent's report — while 10.0G of /tmp scratch and 1.6G of unregistered
		# checkouts sat unnamed on the same volume (kata td3g).
		cat >&2 <<-MSG
			disk-reclaim: only ${avail_gb}G free on $repo_root (floor is ${min_gb}G) — kata 98x9.
			Left alone this shows up as an unrelated-looking failure instead of this message:
			"link: mapping output file failed: no space left on device", a t.TempDir() setup
			failure, or a jobstore open error.

			--check is a bare df, so it does not know what is reclaimable. Measure first;
			none of these delete anything:
			  scripts/disk-reclaim.sh               # each worktree class, sized, and how
			                                        # much of it is removable right now
			  scripts/report-orphaned-worktrees.sh  # checkouts git has no record of
			  scripts/report-tmp-debris.sh          # per-session scratch under /tmp
			Then reclaim what the report actually found:
			${cache_lines}  scripts/disk-reclaim.sh --worktrees   # remove MERGED worktrees. Mid-fleet this
			                                        # routinely frees 0: live worktrees are kept,
			                                        # and a merged one holding an agent's
			                                        # .superpowers report is kept too.
			The two report-only classes above need a human; nothing here deletes them for you.
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

# Whether the build cache is worth measuring is the same question as whether
# --cache is worth offering: only a cache on THIS volume can move THIS floor.
# Sizing one that cannot is 32.6s per report on the real machine (111G, ~2M
# inodes, on an external volume by design — scripts/setup-gocache.sh), spent to
# print a number that answers no question the report is asking. `df` of its
# volume is the fact that does matter about an off-volume cache, and is free.
#
# Every `df` here goes through gocache_probe, and so does the `[ -d ]` they
# replace. This is the mode a human runs when something is ALREADY wrong, so a
# stalled build-cache volume has to be named here — unbounded, those three
# touches swallowed the whole rest of the report instead: "disk-reclaim: NNG
# free of ..." and then nothing at all, for as long as the stall lasted, with
# the volume that caused it unnamed (kata 6jxs).
gocache=$(go env GOCACHE 2>/dev/null)
if [ -n "$gocache" ]; then
	# This df stands in for the `[ -d "$gocache" ]` that used to guard the
	# block as well: df fails on a path that is not there, which is what an
	# unmounted volume looks like, and the report stays silent about the cache
	# in that case exactly as it did before.
	gocache_df=$(gocache_probe "$gocache_probe_timeout" df -P "$gocache")
	gocache_probe_status=$?
	if [ "$gocache_probe_status" -eq 124 ]; then
		echo "  go build cache   $gocache"
		echo "                   VOLUME STALLED: no answer in ${gocache_probe_timeout}s, so it is not sized here — kata 6jxs."
		echo "                   Every go command touching this cache is blocked on it too; wake or remount the volume."
	elif [ "$gocache_probe_status" -eq 0 ]; then
		gocache_dev=$(awk 'NR==2 {print $1}' <<<"$gocache_df")
		repo_dev=$(df -P "$repo_root" 2>/dev/null | awk 'NR==2 {print $1}')
		if [ -n "$gocache_dev" ] && [ "$gocache_dev" = "$repo_dev" ]; then
			# The one touch deliberately left unbounded. `du` of a real cache
			# is 32.6s of honest work on the machine that was measured on, so
			# any bound tight enough to name a stall would call a healthy walk
			# of a 2M-inode tree stalled instead. It runs only for a cache on
			# THIS volume, which the df above and this report's own opening
			# free-space line have both just read successfully.
			echo "  go build cache   $(du -sh "$gocache" 2>/dev/null | cut -f1)	$gocache"
			echo "                   on this volume, so --cache frees exactly that here"
		else
			# Third touch of the same path, bounded for the same reason as the
			# first two: the volume can stall between them. No answer costs the
			# free-space figure and nothing else.
			gocache_free_df=$(gocache_probe "$gocache_probe_timeout" df -h "$gocache")
			gocache_free=$(awk 'NR==2 {print $4}' <<<"$gocache_free_df")
			[ -n "$gocache_free" ] || gocache_free="unknown"
			echo "  go build cache   $gocache"
			echo "                   on another volume ($gocache_free free there), so --cache cannot move this floor"
		fi
	fi
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
# Every path git knows about, newline-delimited (bash 3.2 on macOS has no
# associative arrays). Anything under the worktrees directory that is NOT in
# here is a checkout git has no record of - kata smw0's ~1.6G of pre-rename
# orphans - and no amount of `git worktree prune` will reach it.
registered_paths=""
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
		[ -z "$wt_path" ] || registered_paths="$registered_paths$wt_path
"
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

# One number for the whole worktrees directory is what this used to print, and
# it read as the amount --worktrees could free. On the real machine that number
# was 8.3G and the true answer was ZERO: every merged worktree held an agent's
# `.superpowers/<kata>-report.md`, which this script keeps by design (kata
# datr), so every one of them was reported as merged and kept as held. Sending
# someone at the disk floor to run a lever worth nothing is how the classifier
# gets loosened and the deletion incident happens a third time (kata td3g).
#
# So size the classes the reader actually has to choose between, and let
# "removable" mean removable: merged AND not already known to be kept. It is
# still an upper bound - the dirty/in-use refusal cannot be predicted without
# attempting the removal - hence "at most". Same `du` walk as the single number
# it replaces, split three ways.
sum_kb() { # `du -sk` over the given paths, summed; 0 for none
	[ "$#" -gt 0 ] || {
		echo 0
		return
	}
	du -sk "$@" 2>/dev/null | awk '{s+=$1} END {print s+0}'
}
human_kb() {
	awk -v kb="$1" 'BEGIN { printf (kb>=1048576) ? "%.1fG" : (kb>=1024) ? "%.0fM" : "%dK", (kb>=1048576) ? kb/1048576 : (kb>=1024) ? kb/1024 : kb }'
}

removable_paths=()
kept_paths=()
held_ignored=0
for entry in "${merged[@]:-}"; do
	[ -n "$entry" ] || continue
	wt=${entry%%	*}
	# Read-only, and the same probe the removal path re-runs before it deletes
	# anything. Reporting a worktree as removable when this already says it is
	# not is the overstatement this whole block exists to stop.
	if [ -n "$(worktree_ignored_work "$wt")" ]; then
		kept_paths+=("$wt")
		held_ignored=$((held_ignored + 1))
	else
		removable_paths+=("$wt")
	fi
done
for entry in "${unmerged[@]:-}"; do
	[ -n "$entry" ] || continue
	kept_paths+=("${entry%%	*}")
done

unregistered_paths=()
if [ -d "$worktrees_dir" ]; then
	while IFS= read -r child; do
		[ -n "$child" ] || continue
		grep -Fxq "$child" <<<"$registered_paths" && continue
		unregistered_paths+=("$child")
	done < <(find "$worktrees_dir" -mindepth 1 -maxdepth 1 -type d 2>/dev/null)
fi

removable_h=$(human_kb "$(sum_kb "${removable_paths[@]:-}")")
echo "  worktrees        registered checkouts, by what you can do about them:"
printf '    %-14s %-8s %s\n' "removable" "$removable_h" \
	"${#removable_paths[@]} of ${#merged[@]} merged into ${INTO:0:12}; at most what --worktrees frees"
kept_why="${#unmerged[@]} unmerged"
[ "$held_ignored" -gt 0 ] && kept_why="$kept_why, $held_ignored merged but holding ignored work"
printf '    %-14s %-8s %s\n' "kept" \
	"$(human_kb "$(sum_kb "${kept_paths[@]:-}")")" \
	"$kept_why"
if [ "${#unregistered_paths[@]}" -gt 0 ]; then
	printf '    %-14s %-8s %s\n' "unregistered" \
		"$(human_kb "$(sum_kb "${unregistered_paths[@]:-}")")" \
		"${#unregistered_paths[@]} dir$([ "${#unregistered_paths[@]}" = 1 ] || echo s) git has no record of; scripts/report-orphaned-worktrees.sh"
fi
echo "  not sized here   per-session scratch under /tmp; scripts/report-tmp-debris.sh"

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
	# Same rule as the floor message: name a lever only where it can do
	# something. "Re-run with --cache, --worktrees, or --all" advertised, in
	# one breath, a cache on a volume this run had just said it cannot help and
	# a worktree sweep this run had just measured at zero.
	echo "Report only; nothing above was changed."
	echo "  --worktrees would free at most $removable_h of that, and keeps the rest."
	if [ -n "${gocache_dev:-}" ] && [ "$gocache_dev" = "${repo_dev:-}" ]; then
		echo "  --cache would free the build cache sized above; --all does both."
	fi
	echo "The unregistered and /tmp classes are removed by nobody but you."
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
