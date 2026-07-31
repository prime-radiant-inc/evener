#!/usr/bin/env bash
# report-orphaned-worktrees.sh — find directories under .claude/worktrees whose
# .git file points at a path that no longer exists (kata smw0).
#
# Why this exists: this repo was moved from /Users/jesse/prime-radiant/serf to
# /Users/jesse/prime-radiant/toil-suite/serf. Every worktree checkout made
# BEFORE that move has a `.git` file (worktrees use a file, not a directory)
# containing `gitdir: <old-repo-path>/.git/worktrees/<name>`. That path is
# gone, so:
#   - `git -C <dir> status` (or any git command run FROM inside <dir>) fails
#     with "fatal: not a git repository", because git resolves the worktree's
#     gitdir through that dead pointer before it can do anything else —
#     including tell you whether the checkout is dirty or holds commits
#     nobody pushed.
#   - `git worktree list` run from THIS repo has no record of them at all —
#     they were never registered under the current .git, only under the old
#     one — so `git worktree prune`/`remove` cannot reach them, and neither
#     can scripts/disk-reclaim.sh, which walks `git worktree list`.
#
# That combination is the finding this script exists to report: the usual
# "is it safe to delete" check (ask git) is UNAVAILABLE for these specific
# directories. This script only reports; it never deletes anything. See
# --help for the human-run command to review and remove them by hand.
#
# Detection is a `find` + `grep` over 42 small files, not a `du` of the whole
# tree — the latter is what stalled a previous run (minutes, against a <1s
# scan). Per-directory `du -sh` here is fine; it's one small directory at a
# time, not the whole worktrees tree at once.
#
# Usage:
#   scripts/report-orphaned-worktrees.sh              # report, human-readable
#   scripts/report-orphaned-worktrees.sh --paths-only  # one path per line, for scripting
#   scripts/report-orphaned-worktrees.sh --help
#
# Removal is deliberately NOT built into this script or wired into
# disk-reclaim.sh --worktrees: that script's whole safety model assumes git's
# own merge/dirty checks are available, and for these directories git has no
# idea they exist. The "kept if dirty" backstop that already had one
# near-miss on that script cannot fire here. Review by hand, per directory,
# with git's own tooling repaired just enough to ask the real question:
#
#   name=exp-a   # one candidate from the report above
#   dir=".claude/worktrees/$name"
#   # Point git at the OLD repo's worktree metadata for THIS worktree only
#   # (nothing above it is touched) so ordinary git commands work again:
#   old_gitdir=$(grep '^gitdir:' "$dir/.git" | sed 's/^gitdir: //')
#   git --git-dir="$old_gitdir" --work-tree="$dir" status
#   git --git-dir="$old_gitdir" --work-tree="$dir" log --oneline -5
#   git --git-dir="$old_gitdir" --work-tree="$dir" branch -v
#   # If that confirms nothing uncommitted or unpushed is worth keeping:
#   rm -rf "$dir"
#
# ($old_gitdir itself, under the pre-rename repo's .git/worktrees/, does not
# exist either in most cases — the whole old checkout was moved, not just
# this repo's working copy — so expect that repair step to also fail for
# most of these; when it does, there is no git-mediated way to inspect the
# directory's history at all, and the decision comes down to the mtime and
# name shown in the report, same as the kata's own finding.)
set -uo pipefail

PATHS_ONLY=0
for arg in "$@"; do
	case "$arg" in
	--paths-only) PATHS_ONLY=1 ;;
	-h | --help)
		# Everything from the shebang to the first non-comment line. The
		# hardcoded range this replaces stopped at line 29 of a header that had
		# grown to 58, so --help cut off before the by-hand review recipe —
		# which is the one thing this script's own output sends readers here
		# for.
		awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"
		exit 0
		;;
	*)
		echo "report-orphaned-worktrees: unknown argument: $arg (try --help)" >&2
		exit 2
		;;
	esac
done

main_root=$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)") || {
	echo "report-orphaned-worktrees: not inside a git repository" >&2
	exit 1
}
worktrees_dir="$main_root/.claude/worktrees"
[ -d "$worktrees_dir" ] || {
	echo "report-orphaned-worktrees: no $worktrees_dir" >&2
	exit 1
}

# This repo's OWN current path — a directory is orphaned if its .git file
# names a path OTHER than this one. Deliberately not hardcoded to the specific
# pre-rename path found in kata smw0's investigation, so this keeps working
# after the next rename instead of needing a new kata to notice the old
# constant stopped matching anything.
here_repo=$(git rev-parse --path-format=absolute --git-common-dir)
here_repo=${here_repo%/.git}

candidates=()
while IFS= read -r gitfile; do
	dir=$(dirname "$gitfile")
	gitdir_line=$(grep '^gitdir:' "$gitfile" 2>/dev/null | head -1)
	pointed=${gitdir_line#gitdir: }
	case "$pointed" in
	"$here_repo"/*) ;; # points at THIS repo: not orphaned, skip
	*) candidates+=("$dir	$pointed") ;;
	esac
done < <(find "$worktrees_dir" -maxdepth 2 -name .git -type f)

if [ "${#candidates[@]}" -eq 0 ]; then
	[ "$PATHS_ONLY" = 1 ] || echo "report-orphaned-worktrees: none found under $worktrees_dir"
	exit 0
fi

if [ "$PATHS_ONLY" = 1 ]; then
	for entry in "${candidates[@]}"; do
		echo "${entry%%	*}"
	done
	exit 0
fi

total_kb=0
echo "report-orphaned-worktrees: ${#candidates[@]} director$([ "${#candidates[@]}" = 1 ] && echo y || echo ies) under $worktrees_dir point at a gone repo path:"
echo
for entry in "${candidates[@]}"; do
	dir=${entry%%	*}
	pointed=${entry##*	}
	name=$(basename "$dir")
	mtime=$(stat -f '%Sm' -t '%Y-%m-%d' "$dir" 2>/dev/null || date -r "$(stat -c '%Y' "$dir" 2>/dev/null || echo 0)" '+%Y-%m-%d' 2>/dev/null || echo "unknown")
	size_kb=$(du -sk "$dir" 2>/dev/null | cut -f1)
	size_kb=${size_kb:-0}
	total_kb=$((total_kb + size_kb))
	size_h=$(du -sh "$dir" 2>/dev/null | cut -f1)
	git_resolvable="no"
	if git -C "$dir" rev-parse --git-dir >/dev/null 2>&1; then
		git_resolvable="yes"
	fi
	printf '  %-40s %8s  mtime %s  git-resolvable: %s\n' "$name" "${size_h:-?}" "$mtime" "$git_resolvable"
	printf '      points at: %s\n' "$pointed"
done

total_h=$(awk -v kb="$total_kb" 'BEGIN { printf (kb>=1048576) ? "%.1fG" : (kb>=1024) ? "%.0fM" : "%dK", (kb>=1048576) ? kb/1048576 : (kb>=1024) ? kb/1024 : kb }')
echo
echo "Total: ${total_h} across ${#candidates[@]} orphaned director$([ "${#candidates[@]}" = 1 ] && echo y || echo ies)."
echo
echo "git cannot answer whether any of these are dirty or hold unmerged commits"
echo "(that's what 'git-resolvable: no' above means) — the usual safety check is"
echo "unavailable. Nothing here was deleted. To review and remove by hand, see:"
echo "  scripts/report-orphaned-worktrees.sh --help"
