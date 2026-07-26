#!/usr/bin/env bash
# report-orphaned-worktrees-selftest.sh — offline, deterministic test of
# scripts/report-orphaned-worktrees.sh's orphan detection (kata smw0), against
# a throwaway repo with one real worktree and one synthetic orphaned checkout.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/report-orphaned-worktrees.sh"
checks=0 fails=0
ok() { checks=$((checks + 1)); printf '  ok: %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL: %s\n' "$1"; }

work="$(mktemp -d -t report-orphaned-worktrees-selftest.XXXXXX)"
# Resolve symlinks now (macOS's /var/folders is a symlink to /private/var/folders):
# the script under test reports fully-resolved paths via git rev-parse, so an
# unresolved $work would make string-equality path comparisons below spuriously
# fail without there being any real bug.
work="$(cd "$work" && pwd -P)"
trap 'rm -rf "$work"' EXIT

repo="$work/repo"
mkdir -p "$repo/.claude/worktrees"
(
	cd "$repo" &&
		git init -q &&
		git config user.email t@t &&
		git config user.name t &&
		git symbolic-ref HEAD refs/heads/main &&
		echo one >file &&
		git add -A &&
		git commit -qm init
) || {
	echo "FAIL: could not set up throwaway repo" >&2
	exit 1
}

# A REAL worktree, registered under this repo's own .git: must NOT be
# reported as orphaned.
(cd "$repo" && git worktree add -q -b healthy ".claude/worktrees/healthy" main)

# A SYNTHETIC orphaned checkout: a directory with a .git FILE (not a real
# worktree, never registered) pointing at a path that does not exist —
# exactly the shape kata smw0 found, without needing a real pre-rename repo.
orphan_dir="$repo/.claude/worktrees/orphan-sim"
mkdir -p "$orphan_dir"
echo "gitdir: /nonexistent/old/repo/path/.git/worktrees/orphan-sim" >"$orphan_dir/.git"
echo payload >"$orphan_dir/some-file"

out="$(cd "$repo" && bash "$script")"

if echo "$out" | grep -q "orphan-sim"; then
	ok "synthetic orphaned checkout is reported"
else
	bad "synthetic orphaned checkout was NOT reported: $out"
fi
if echo "$out" | grep -q "healthy"; then
	bad "a REAL, currently-registered worktree was reported as orphaned"
else
	ok "a real, currently-registered worktree is not reported"
fi
if echo "$out" | grep -q "git-resolvable: no"; then
	ok "orphaned checkout is flagged git-unresolvable"
else
	bad "orphaned checkout was not flagged git-unresolvable: $out"
fi
if echo "$out" | grep -qi "Nothing here was deleted"; then
	ok "report states nothing was deleted"
else
	bad "report did not state that nothing was deleted"
fi
if [ -d "$orphan_dir" ]; then
	ok "orphaned directory still exists after running the report (no deletion happened)"
else
	bad "orphaned directory was REMOVED by a report-only run"
fi

paths_out="$(cd "$repo" && bash "$script" --paths-only)"
if [ "$paths_out" = "$orphan_dir" ]; then
	ok "--paths-only prints exactly the one orphaned path"
else
	bad "--paths-only output unexpected: $paths_out"
fi

(cd "$repo" && git worktree remove -f ".claude/worktrees/healthy" 2>/dev/null)

echo
echo "$checks checks, $fails failed"
[ "$fails" -eq 0 ]
