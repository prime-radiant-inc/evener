#!/usr/bin/env bash
# disk-reclaim-selftest.sh — offline, deterministic test of scripts/disk-reclaim.sh's
# worktree classification and --check disk-floor logic, against throwaway repos with
# known branch topologies.
#
# This is exactly where the script's one confirmed bug lived (kata 98x9): a branch
# with no commits of its own is trivially an ancestor of what it was cut from, so
# --is-ancestor alone called it "merged" and the dirty-check backstop did not save
# it — a fresh checkout nobody has written to yet is perfectly clean. That fix is
# already committed; scenario 1 below is its regression test. Scenario 5 covers a
# second, independently-found parsing bug in the same neighborhood: `git worktree
# list`'s human format appends a bare "locked" or "prunable" word after the branch
# name, which the old bracket-stripping parse folded into the branch name itself,
# sending every locked/prunable worktree down the "unresolvable branch" path.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/disk-reclaim.sh"
checks=0 fails=0
ok() { checks=$((checks + 1)); printf '  ok: %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL: %s\n' "$1"; }

work="$(mktemp -d -t disk-reclaim-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

repo="$work/repo"
wt_dir="$repo/wt"
mkdir -p "$wt_dir"
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

run_disk_reclaim() (
	cd "$repo" && bash "$script" "$@"
)

# --- scenario 1: an unstarted branch (no commits of its own) is not "merged" ---
(cd "$repo" && git worktree add -q -b unstarted "$wt_dir/unstarted" main)
out="$(run_disk_reclaim --into main)"
if echo "$out" | grep -qE '0 merged into [^,]+, 1 unmerged'; then
	ok "unstarted branch counted as the sole unmerged entry (not merged)"
else
	bad "unexpected merged/unmerged counts for an unstarted branch: $out"
fi
run_disk_reclaim --worktrees --into main >/dev/null
if [ -d "$wt_dir/unstarted" ]; then
	ok "unstarted worktree survives --worktrees"
else
	bad "unstarted worktree was REMOVED by --worktrees (the six-worktree-deletion bug)"
fi
(cd "$repo" && git worktree remove "$wt_dir/unstarted" 2>/dev/null; git branch -D unstarted >/dev/null 2>&1)

# --- scenario 2: a branch merged into the target is removed ---
(cd "$repo" && git worktree add -q -b merged-branch "$wt_dir/merged" main)
(cd "$wt_dir/merged" && echo two >file2 && git add -A && git commit -qm "merged-branch change")
# --no-ff: a plain fast-forward would leave main's tip identical to the
# branch's tip, which the "unstarted" check (same tip as base) cannot tell
# apart from a branch that never advanced. Real merges land as a distinct
# commit; simulate that here.
(cd "$repo" && git merge --no-ff -q -m "merge merged-branch" merged-branch)
out="$(run_disk_reclaim --worktrees --into main)"
if [ -d "$wt_dir/merged" ]; then
	bad "merged worktree was NOT removed by --worktrees"
else
	ok "merged worktree is removed by --worktrees"
fi
if echo "$out" | grep -q "removed  merged-branch"; then
	ok "removal of merged-branch is reported"
else
	bad "removal of merged-branch not reported: $out"
fi

# --- scenario 3: a branch with real, unmerged commits is kept ---
(cd "$repo" && git worktree add -q -b unmerged-branch "$wt_dir/unmerged" main)
(cd "$wt_dir/unmerged" && echo three >file3 && git add -A && git commit -qm "unmerged-branch change")
run_disk_reclaim --worktrees --into main >/dev/null
if [ -d "$wt_dir/unmerged" ]; then
	ok "unmerged (diverged) worktree survives --worktrees"
else
	bad "unmerged worktree was REMOVED by --worktrees"
fi
(cd "$repo" && git worktree remove "$wt_dir/unmerged" 2>/dev/null; git branch -D unmerged-branch >/dev/null 2>&1)

# --- scenario 4: a MERGED branch with an uncommitted change is kept (dirty backstop) ---
(cd "$repo" && git worktree add -q -b dirty-branch "$wt_dir/dirty" main)
(cd "$wt_dir/dirty" && echo four >file4 && git add -A && git commit -qm "dirty-branch change")
(cd "$repo" && git merge --no-ff -q -m "merge dirty-branch" dirty-branch)
echo uncommitted >>"$wt_dir/dirty/file4"
out="$(run_disk_reclaim --worktrees --into main)"
if [ -d "$wt_dir/dirty" ]; then
	ok "merged-but-dirty worktree survives --worktrees"
else
	bad "merged-but-dirty worktree was REMOVED despite uncommitted changes"
fi
if echo "$out" | grep -q "dirty or in use"; then
	ok "dirty worktree's kept removal attempt is reported"
else
	bad "dirty worktree removal attempt not reported: $out"
fi

# --- scenario 5: a LOCKED worktree's branch name still parses correctly ---
(cd "$repo" && git worktree add -q -b locked-branch "$wt_dir/locked" main)
(cd "$wt_dir/locked" && echo five >file5 && git add -A && git commit -qm "locked-branch change")
(cd "$repo" && git merge --no-ff -q -m "merge locked-branch" locked-branch)
(cd "$repo" && git worktree lock "$wt_dir/locked")
out="$(run_disk_reclaim --worktrees --into main)"
if echo "$out" | grep -q "locked-branch"; then
	ok "locked worktree's branch name parsed correctly (not swallowed by the 'locked' annotation)"
else
	bad "locked worktree's branch was not classified at all (parsing regression): $out"
fi
if [ -d "$wt_dir/locked" ]; then
	ok "locked worktree survives (git itself refuses to remove a locked worktree)"
else
	bad "locked worktree was removed"
fi

# --- scenario 6: report-only mode (no flags) changes nothing ---
(cd "$repo" && git worktree add -q -b report-only-branch "$wt_dir/reportonly" main)
(cd "$wt_dir/reportonly" && echo six >file6 && git add -A && git commit -qm change)
(cd "$repo" && git merge --no-ff -q -m "merge report-only-branch" report-only-branch)
run_disk_reclaim --into main >/dev/null
if [ -d "$wt_dir/reportonly" ]; then
	ok "report-only mode does not remove a merged worktree"
else
	bad "report-only mode removed a worktree"
fi

# --- scenario 7: --check fails loud below the floor, is silent above it ---
# SERF_SKIP_GOCACHE_CHECK=1: this scenario is about the disk-floor logic only;
# dedicated scenarios 8/9 below cover the GOCACHE block with GOCACHE pinned.
if SERF_SKIP_GOCACHE_CHECK=1 SERF_DISK_MIN_FREE_GB=999999999 run_disk_reclaim --check >"$work/check-fail.out" 2>&1; then
	bad "--check with an impossible floor (999999999G) exited 0"
else
	if grep -qi "free" "$work/check-fail.out"; then
		ok "--check below the floor exits non-zero with a specific message"
	else
		bad "--check below the floor printed no specific message"
	fi
fi
if SERF_SKIP_GOCACHE_CHECK=1 SERF_DISK_MIN_FREE_GB=0 run_disk_reclaim --check >"$work/check-pass.out" 2>&1; then
	if [ -s "$work/check-pass.out" ]; then
		bad "--check above the floor printed output (expected silent success)"
	else
		ok "--check above the floor (0G) exits 0 silently"
	fi
else
	bad "--check with a trivial floor (0G) still failed"
fi

# --- scenario 8: --check fails loud when GOCACHE points at an unreachable
# path (kata 98x9's new failure mode: the volume it lives on is unmounted).
# Simulated portably (no real removable volume needed): a chmod-000 ancestor
# directory makes `mkdir -p` fail exactly like it would against a vanished
# mountpoint, for any user, on any OS.
locked_vol="$work/locked-vol"
mkdir -p "$locked_vol"
chmod 000 "$locked_vol"
unreachable_gocache="$locked_vol/would-be-mounted/serf-build-cache"
if GOCACHE="$unreachable_gocache" run_disk_reclaim --check >"$work/gocache-unreachable.out" 2>&1; then
	bad "--check with an unreachable GOCACHE exited 0"
else
	if grep -q "unmounted" "$work/gocache-unreachable.out" && grep -qF "$unreachable_gocache" "$work/gocache-unreachable.out"; then
		ok "--check with an unreachable GOCACHE fails loud and names the path"
	else
		bad "--check with an unreachable GOCACHE gave an unspecific message: $(cat "$work/gocache-unreachable.out")"
	fi
fi
chmod 755 "$locked_vol"

# --- scenario 9: --check warns (but does not fail) when GOCACHE is reachable
# but back on the same volume as the checkout ---
samevol_gocache="$work/samevol-gocache"
if GOCACHE="$samevol_gocache" run_disk_reclaim --check >"$work/gocache-samevol.out" 2>&1; then
	if grep -q "setup-gocache.sh" "$work/gocache-samevol.out"; then
		ok "--check warns (exit 0) when GOCACHE shares the checkout's volume"
	else
		bad "--check with same-volume GOCACHE exited 0 but did not warn: $(cat "$work/gocache-samevol.out")"
	fi
else
	bad "--check with a reachable (same-volume) GOCACHE exited non-zero"
fi

echo
echo "$checks checks, $fails failed"
[ "$fails" -eq 0 ]
