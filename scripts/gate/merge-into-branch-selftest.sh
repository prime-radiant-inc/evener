#!/usr/bin/env bash
# merge-into-branch-selftest.sh — deterministic integration test for
# scripts/merge-into-branch.sh against real throwaway git repos: no faked git,
# because the whole point of the tool is a real `git merge` plus a real
# `git update-ref` compare-and-swap.
#
# Case 7 (race lost) is the kata's actual requirement: it reproduces the
# incident by using EVENER_MERGE_INTO_BRANCH_PRECAS_HOOK to move
# refs/heads/target, deterministically, in the window the tool itself leaves
# open between reading the branch's tip and writing it back — no sleep, no
# poll, same discipline as the rest of this repo's selftests.
#
# Run: scripts/merge-into-branch-selftest.sh   (also: make merge-into-branch-selftest)
set -uo pipefail

tool="$(cd "$(dirname "$0")" && pwd)/merge-into-branch.sh"
. "$(dirname "$0")/../lib/selftest-lib.sh"

trap 'scratch_rm' EXIT
scratch_dir work merge-into-branch-selftest

# new_repo DIR BRANCH — an empty repo with a private identity, so nothing here
# depends on the machine's ambient git config (this tool's whole point).
new_repo() {
	mkdir -p "$1"
	git -C "$1" init -q -b "$2"
	git -C "$1" config user.email selftest@example.com
	git -C "$1" config user.name selftest
}

# commit REPO FILE CONTENT MSG
commit() {
	printf '%s\n' "$3" >"$1/$2"
	git -C "$1" add "$2"
	git -C "$1" commit -q -m "$4"
}

# ---------------------------------------------------------------------------
# Case 1: fast-forward happy path, and proof the source checkout is untouched.
# ---------------------------------------------------------------------------
r="$work/ff"
new_repo "$r" target
commit "$r" f.txt base base
old_sha="$(git -C "$r" rev-parse target)"
git -C "$r" checkout -q -b feature
commit "$r" f.txt "base
more" add-more
feature_sha="$(git -C "$r" rev-parse feature)"
git -C "$r" checkout -q target

out="$work/ff.out"
"$tool" --repo "$r" target feature >"$out" 2>&1
assert_eq "$?" "0" "fast-forward: exits zero"
assert_eq "$(git -C "$r" rev-parse refs/heads/target)" "$feature_sha" "fast-forward: target ref advances to feature's tip"
assert_has "$out" "updated refs/heads/target: $old_sha -> $feature_sha" "fast-forward: reports the exact old and new SHAs"
# target already equals feature's own SHA (asserted above), so there is no
# separate merge commit to check for.
#
# The checkout that has target checked out must never be touched: still on
# target, and its WORKING FILE content is exactly what it was before the ref
# moved (a ref update alone never rewrites a checkout's files — only a
# checkout/reset run *in that checkout* would). `git status --porcelain`
# legitimately starts reporting f.txt as staged-modified here: its on-disk
# index still matches the OLD tree, and HEAD (via the moved branch) now
# names the NEW one — that divergence is inherent to moving a ref out from
# under a checkout and is not something this tool can or should paper over
# without running commands inside that checkout. `git diff` (worktree vs
# index only, blind to which commit HEAD names) is the precise "did anything
# on disk change" check.
assert_eq "$(git -C "$r" symbolic-ref -q HEAD)" "refs/heads/target" "fast-forward: source checkout is still on target"
assert_eq "$(git -C "$r" diff)" "" "fast-forward: source checkout's worktree has no unstaged diff from its own index"
assert_eq "$(cat "$r/f.txt")" "base" "fast-forward: source checkout's working file is untouched by the ref move"
assert_eq "$(git -C "$r" worktree list --porcelain | grep -c '^worktree ')" "1" "fast-forward: no worktree left behind"

# ---------------------------------------------------------------------------
# Case 2: real merge on diverged branches, both parents correct, and the
# incident scenario itself: the checkout has target checked out AND dirty
# (uncommitted edit plus a deleted tracked file), same shape as the kata body.
# ---------------------------------------------------------------------------
r="$work/diverge"
new_repo "$r" target
commit "$r" base.txt base base
commit "$r" plan.txt keep-me plan
git -C "$r" checkout -q -b feature
commit "$r" feature.txt feat feat-commit
feature_sha="$(git -C "$r" rev-parse feature)"
git -C "$r" checkout -q target
commit "$r" target.txt tgt target-commit
old_sha="$(git -C "$r" rev-parse target)"
# Dirty state a concurrent session left behind, mirroring the kata: an edited
# tracked file plus a deleted one.
printf 'uncommitted edit\n' >"$r/base.txt"
rm -f "$r/plan.txt"
# git diff (worktree vs index) is exactly the edit + delete above, and is
# blind to which commit HEAD names — see the fast-forward case for why that
# is the right invariant to compare before/after instead of `git status`.
dirty_before="$(git -C "$r" diff)"

out="$work/diverge.out"
"$tool" --repo "$r" target feature >"$out" 2>&1
assert_eq "$?" "0" "real merge: exits zero"
new_sha="$(git -C "$r" rev-parse refs/heads/target)"
[ "$new_sha" != "$old_sha" ] && [ "$new_sha" != "$feature_sha" ]
assert_eq "$?" "0" "real merge: target ref points at a NEW commit (neither old target nor feature)"
assert_eq "$(git -C "$r" log -1 --format=%P "$new_sha")" "$old_sha $feature_sha" "real merge: parents are old-target-tip then feature-tip, in that order"
assert_has "$out" "updated refs/heads/target: $old_sha -> $new_sha" "real merge: reports the exact old and new SHAs"
# The dirty, checked-out working tree must be BYTE IDENTICAL to before the
# tool ran: this is the incident's actual failure mode (a live checkout's
# uncommitted work got disturbed), and it is what a private temp worktree
# instead of `git -C "$r" merge` buys.
assert_eq "$(git -C "$r" diff)" "$dirty_before" "real merge: pre-existing dirty state (edit + delete) is untouched"
assert_eq "$(cat "$r/base.txt")" "uncommitted edit" "real merge: uncommitted edit survives verbatim"
[ ! -e "$r/plan.txt" ]
assert_eq "$?" "0" "real merge: the deleted file stays deleted, not resurrected"
assert_eq "$(git -C "$r" symbolic-ref -q HEAD)" "refs/heads/target" "real merge: source checkout is still on target"

# ---------------------------------------------------------------------------
# Case 3: --ff-only refuses a real divergence; ref is untouched.
# ---------------------------------------------------------------------------
r="$work/ffonly-refuse"
new_repo "$r" target
commit "$r" f.txt base base
git -C "$r" checkout -q -b feature
commit "$r" feature.txt feat feat-commit
feature_sha="$(git -C "$r" rev-parse feature)"
git -C "$r" checkout -q target
commit "$r" target.txt tgt target-commit
old_sha="$(git -C "$r" rev-parse target)"

out="$work/ffonly-refuse.out"
"$tool" --repo "$r" --ff-only target feature >"$out" 2>&1
rc=$?
assert_eq "$rc" "1" "ff-only: a real divergence exits nonzero (merge-failure code)"
assert_eq "$(git -C "$r" rev-parse refs/heads/target)" "$old_sha" "ff-only: target ref is untouched"
assert_has "$out" "Not possible to fast-forward" "ff-only: names the reason it refused"
assert_eq "$(git -C "$r" worktree list --porcelain | grep -c '^worktree ')" "1" "ff-only: no worktree left behind"

# ---------------------------------------------------------------------------
# Case 4: --no-ff forces a merge commit even though a fast-forward exists.
# ---------------------------------------------------------------------------
r="$work/noff-force"
new_repo "$r" target
commit "$r" f.txt base base
old_sha="$(git -C "$r" rev-parse target)"
git -C "$r" checkout -q -b feature
commit "$r" f.txt "base
more" add-more
feature_sha="$(git -C "$r" rev-parse feature)"
git -C "$r" checkout -q target

out="$work/noff-force.out"
"$tool" --repo "$r" --no-ff target feature >"$out" 2>&1
assert_eq "$?" "0" "no-ff: exits zero"
new_sha="$(git -C "$r" rev-parse refs/heads/target)"
[ "$new_sha" != "$feature_sha" ]
assert_eq "$?" "0" "no-ff: created a real merge commit instead of fast-forwarding"
assert_eq "$(git -C "$r" log -1 --format=%P "$new_sha")" "$old_sha $feature_sha" "no-ff: parents are old-target-tip then feature-tip"

# ---------------------------------------------------------------------------
# Case 5: already merged is a no-op, not a spurious "updated" report.
# ---------------------------------------------------------------------------
r="$work/noop"
new_repo "$r" target
commit "$r" f.txt base base
old_sha="$(git -C "$r" rev-parse target)"
git -C "$r" branch already-in "$old_sha"
commit "$r" f.txt "base
more" add-more
new_target_sha="$(git -C "$r" rev-parse target)"

out="$work/noop.out"
"$tool" --repo "$r" target already-in >"$out" 2>&1
assert_eq "$?" "0" "no-op: exits zero when source is already merged"
assert_eq "$(git -C "$r" rev-parse refs/heads/target)" "$new_target_sha" "no-op: target ref is unchanged"
assert_has "$out" "already merged into refs/heads/target" "no-op: says so"
assert_not_has "$out" "updated refs/heads/target" "no-op: does not claim to have updated the ref"

# ---------------------------------------------------------------------------
# Case 6: a genuine conflict fails loudly and changes nothing.
# ---------------------------------------------------------------------------
r="$work/conflict"
new_repo "$r" target
commit "$r" f.txt line1 base
git -C "$r" checkout -q -b feature
commit "$r" f.txt line1-feature feature-edit
feature_sha="$(git -C "$r" rev-parse feature)"
git -C "$r" checkout -q target
commit "$r" f.txt line1-target target-edit
old_sha="$(git -C "$r" rev-parse target)"

out="$work/conflict.out"
"$tool" --repo "$r" target feature >"$out" 2>&1
assert_eq "$?" "1" "conflict: exits nonzero (merge-failure code)"
assert_eq "$(git -C "$r" rev-parse refs/heads/target)" "$old_sha" "conflict: target ref is untouched"
assert_has "$out" "CONFLICT" "conflict: names the conflict"
assert_eq "$(git -C "$r" worktree list --porcelain | grep -c '^worktree ')" "1" "conflict: the conflicted temp worktree was discarded, not left behind"
assert_eq "$(cat "$r/f.txt")" "line1-target" "conflict: source checkout's file is untouched"

# ---------------------------------------------------------------------------
# Case 7: THE KATA. refs/heads/target moves after preflight, before the CAS
# write. The tool must refuse, and the sandbox must be untouched by the tool
# itself — only the simulated concurrent session's move is visible afterward.
# ---------------------------------------------------------------------------
r="$work/race"
new_repo "$r" target
commit "$r" f.txt base base
old_sha="$(git -C "$r" rev-parse target)"
git -C "$r" checkout -q -b feature
commit "$r" feature.txt feat feat-commit
feature_sha="$(git -C "$r" rev-parse feature)"
git -C "$r" checkout -q target

# The "other session": a real commit built on target's tip, held on a side
# branch so it exists in the object store WITHOUT moving refs/heads/target
# yet. The hook below is the only thing that ever applies it — reproducing
# "another session switched/advanced the branch between preflight and merge".
git -C "$r" branch concurrent "$old_sha"
git -C "$r" checkout -q concurrent
commit "$r" concurrent.txt from-another-session concurrent-commit
concurrent_sha="$(git -C "$r" rev-parse concurrent)"
git -C "$r" checkout -q target

hook="$work/race-hook.sh"
cat >"$hook" <<HOOK
#!/usr/bin/env bash
git -C "$r" update-ref refs/heads/target "$concurrent_sha"
HOOK
chmod +x "$hook"

out="$work/race.out"
EVENER_MERGE_INTO_BRANCH_PRECAS_HOOK="$hook" "$tool" --repo "$r" target feature >"$out" 2>&1
rc=$?
assert_eq "$rc" "3" "race: exits with the dedicated CAS-refused code"
assert_eq "$(git -C "$r" rev-parse refs/heads/target)" "$concurrent_sha" "race: target ref is exactly the concurrent session's commit, nothing more"
assert_has "$out" "refused" "race: says it refused"
assert_has "$out" "expected $old_sha" "race: names the old value it preflighted"
assert_has "$out" "now $concurrent_sha" "race: names what the ref actually holds now"
assert_not_has "$out" "updated refs/heads/target" "race: never claims to have updated the ref"
assert_eq "$(git -C "$r" symbolic-ref -q HEAD)" "refs/heads/target" "race: source checkout is still on target"
assert_eq "$(git -C "$r" diff)" "" "race: source checkout's worktree has no unstaged diff from its own index"
assert_eq "$(git -C "$r" worktree list --porcelain | grep -c '^worktree ')" "1" "race: the losing temp worktree was discarded, not left behind"
assert_eq "$(git -C "$r" branch --list feature | tr -d ' *')" "feature" "race: feature branch itself untouched"

# ---------------------------------------------------------------------------
# Case 8/9: usage errors on a nonexistent target/source touch nothing.
# ---------------------------------------------------------------------------
r="$work/usage"
new_repo "$r" target
commit "$r" f.txt base base
old_sha="$(git -C "$r" rev-parse target)"

out="$work/usage-target.out"
"$tool" --repo "$r" no-such-branch target >"$out" 2>&1
assert_eq "$?" "2" "usage: nonexistent target branch is a usage error"
assert_has "$out" "target branch does not exist" "usage: names the missing target"
assert_eq "$(git -C "$r" rev-parse refs/heads/target)" "$old_sha" "usage: target ref untouched by a bad-target invocation"

out="$work/usage-source.out"
"$tool" --repo "$r" target no-such-ref >"$out" 2>&1
assert_eq "$?" "2" "usage: nonexistent source is a usage error"
assert_has "$out" "source does not resolve to a commit" "usage: names the unresolvable source"
assert_eq "$(git -C "$r" rev-parse refs/heads/target)" "$old_sha" "usage: target ref untouched by a bad-source invocation"

selftest_summary
