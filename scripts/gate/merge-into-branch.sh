#!/usr/bin/env bash
# merge-into-branch.sh — merge SOURCE into a local branch by ref, without ever
# checking out or touching a live working tree, and without landing the result
# if the branch moved underneath the merge (kata h2tb).
#
# The incident this exists to prevent: a controller preflighted "the shared
# checkout is on main", another session switched that same checkout to a
# different branch before the merge command ran, and `git merge` landed the
# commit on the wrong branch. Recovery needed commit-tree/update-ref surgery.
# The bug was operating through MUTABLE checkout state — "whatever HEAD is
# right now" — between the preflight check and the write.
#
# This tool never reads or writes any existing checkout's HEAD or working
# files. It resolves the target branch's CURRENT tip once, builds the merge in
# a private, disposable worktree (so real `git merge` — conflict detection,
# merge strategies, fast-forward included — does the actual work), and then
# lands the result with a single atomic compare-and-swap:
#
#   git update-ref refs/heads/<target> <new> <old>
#
# update-ref takes a lock on the ref, re-reads its CURRENT value under that
# lock, and only writes if it still equals <old>. If some other session moved
# refs/heads/<target> after this script's preflight, the CAS fails atomically
# and NOTHING is written — that race window is exactly what the incident hit,
# and this is the enforcement point the kata asked for. No existing checkout
# is touched in either the success or the failure path.
#
# Usage:
#   scripts/gate/merge-into-branch.sh [--repo PATH] [--ff-only|--no-ff] [-m MESSAGE] TARGET SOURCE
#
#     TARGET    a local branch name; the ref merged into is refs/heads/TARGET.
#     SOURCE    any commit-ish to merge in (branch, tag, SHA, refs/remotes/...).
#     --repo    the repository to operate on (default: the current directory).
#               Any working directory inside the repo works, including a
#               linked worktree — refs/heads/* is shared across all of them.
#     --ff-only fail unless the merge is a fast-forward (like `git merge --ff-only`).
#     --no-ff   always create a merge commit, even when a fast-forward is possible.
#     -m MSG    merge commit message (default: "Merge SOURCE into TARGET").
#               Ignored for a fast-forward, same as plain `git merge`.
#
#   With neither --ff-only nor --no-ff, the merge fast-forwards when possible
#   and otherwise creates a real merge commit — deliberately NOT the operator's
#   ambient `merge.ff`/`pull.ff` git config, which would make this tool's
#   default behaviour depend on whose machine it runs on. It always passes an
#   explicit `--ff` to git for exactly that reason (verified against a machine
#   with `merge.ff = only` set globally, which otherwise silently turns the
#   default mode into --ff-only).
#
# Exit codes:
#   0   updated refs/heads/TARGET, or SOURCE was already merged (no-op)
#   1   the merge itself failed: conflicts, or --ff-only could not fast-forward
#   2   usage error: bad flags, or TARGET/SOURCE/--repo does not resolve
#   3   refused: refs/heads/TARGET moved between preflight and the CAS write.
#       The computed merge was NOT applied; refs/heads/TARGET is exactly what
#       the other session left it as.
#
# Env seams (default is unset in production; used by the self-test to prove
# the CAS deterministically, without sleeps or polling):
#   EVENER_MERGE_INTO_BRANCH_PRECAS_HOOK   if set, an executable run with no
#     arguments after the merge commit is built but before the CAS write —
#     the self-test's one chance to move refs/heads/TARGET and simulate the
#     incident's concurrent branch switch.
set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$script_dir/../lib/scratch-lib.sh"

repo="."
ff_mode="auto"
message=""
positional=()
while [ $# -gt 0 ]; do
	case "$1" in
	--repo)
		[ $# -ge 2 ] || { echo "merge-into-branch: --repo needs a value" >&2; exit 2; }
		repo="$2"; shift 2 ;;
	--ff-only)
		[ "$ff_mode" = "noff" ] && { echo "merge-into-branch: --ff-only conflicts with --no-ff" >&2; exit 2; }
		ff_mode="ffonly"; shift ;;
	--no-ff)
		[ "$ff_mode" = "ffonly" ] && { echo "merge-into-branch: --no-ff conflicts with --ff-only" >&2; exit 2; }
		ff_mode="noff"; shift ;;
	-m | --message)
		[ $# -ge 2 ] || { echo "merge-into-branch: $1 needs a value" >&2; exit 2; }
		message="$2"; shift 2 ;;
	-h | --help)
		awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "${BASH_SOURCE[0]}"
		exit 0 ;;
	-*)
		echo "merge-into-branch: unknown flag: $1 (try --help)" >&2
		exit 2 ;;
	*)
		positional+=("$1"); shift ;;
	esac
done

if [ "${#positional[@]}" -ne 2 ]; then
	echo "usage: merge-into-branch.sh [--repo PATH] [--ff-only|--no-ff] [-m MESSAGE] TARGET SOURCE" >&2
	exit 2
fi
target="${positional[0]}"
source_ref="${positional[1]}"

if ! repo="$(cd "$repo" 2>/dev/null && pwd -P)"; then
	echo "merge-into-branch: --repo path does not exist: $repo" >&2
	exit 2
fi
if ! git -C "$repo" rev-parse --git-dir >/dev/null 2>&1; then
	echo "merge-into-branch: not a git repository: $repo" >&2
	exit 2
fi

# Preflight: read the target branch's CURRENT tip exactly once. Everything
# from here on treats this as the ONLY value refs/heads/TARGET is allowed to
# have had when the CAS below runs — that is the whole enforcement.
if ! old_sha="$(git -C "$repo" rev-parse --verify --quiet "refs/heads/$target")" || [ -z "$old_sha" ]; then
	echo "merge-into-branch: target branch does not exist: refs/heads/$target" >&2
	exit 2
fi
if ! source_sha="$(git -C "$repo" rev-parse --verify --quiet "${source_ref}^{commit}")" || [ -z "$source_sha" ]; then
	echo "merge-into-branch: source does not resolve to a commit: $source_ref" >&2
	exit 2
fi

[ -n "$message" ] || message="Merge $source_ref into $target"

wt=""
cleanup() {
	# --force: the worktree may be mid-conflict (UU files, MERGE_HEAD) or,
	# on the happy path, hold a commit that never got attached to any
	# branch (the CAS lost the race) — either way it is entirely disposable
	# and none of it belongs to any checkout a caller owns.
	[ -n "$wt" ] && git -C "$repo" worktree remove --force "$wt" >/dev/null 2>&1
	scratch_rm
}
trap cleanup EXIT
scratch_dir wt merge-into-branch

# --detach: check out the raw commit, never the branch name. TARGET may
# already be checked out (with a caller's uncommitted work) in this repo or
# another linked worktree; --detach never contends for that branch's lock and
# never reads or writes those files.
if ! add_err="$(git -C "$repo" worktree add --quiet --detach "$wt" "$old_sha" 2>&1)"; then
	echo "merge-into-branch: could not create a private worktree at $old_sha:" >&2
	printf '%s\n' "$add_err" | sed 's/^/    /' >&2
	exit 1
fi

ff_flag="--ff"
case "$ff_mode" in
ffonly) ff_flag="--ff-only" ;;
noff) ff_flag="--no-ff" ;;
esac

if merge_output="$(git -C "$wt" merge "$ff_flag" -m "$message" "$source_sha" 2>&1)"; then
	merge_rc=0
else
	merge_rc=$?
fi

if [ "$merge_rc" -ne 0 ]; then
	printf 'merge-into-branch: merge of %s into %s failed:\n' "$source_ref" "$target" >&2
	printf '%s\n' "$merge_output" | sed 's/^/    /' >&2
	exit 1
fi

new_sha="$(git -C "$wt" rev-parse HEAD)"

if [ "$new_sha" = "$old_sha" ]; then
	printf 'merge-into-branch: %s is already merged into refs/heads/%s (%s); nothing to do\n' \
		"$source_ref" "$target" "$old_sha"
	exit 0
fi

# The self-test's one hook: move refs/heads/TARGET here, deterministically,
# to reproduce the incident's window between preflight and the write below.
if [ -n "${EVENER_MERGE_INTO_BRANCH_PRECAS_HOOK:-}" ]; then
	"$EVENER_MERGE_INTO_BRANCH_PRECAS_HOOK"
fi

if cas_err="$(git -C "$repo" update-ref "refs/heads/$target" "$new_sha" "$old_sha" 2>&1)"; then
	printf 'merge-into-branch: updated refs/heads/%s: %s -> %s\n' "$target" "$old_sha" "$new_sha"
	exit 0
fi

current_sha="$(git -C "$repo" rev-parse --verify --quiet "refs/heads/$target" 2>/dev/null)"
printf 'merge-into-branch: refused: refs/heads/%s moved since preflight (expected %s, now %s); computed merge %s was NOT applied\n' \
	"$target" "$old_sha" "${current_sha:-unresolvable}" "$new_sha" >&2
printf '%s\n' "$cas_err" | sed 's/^/    git: /' >&2
exit 3
