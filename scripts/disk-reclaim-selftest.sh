#!/usr/bin/env bash
# disk-reclaim-selftest.sh — offline, deterministic test of scripts/disk-reclaim.sh's
# worktree classification and --check disk-floor logic, against throwaway repos with
# known branch topologies.
#
# This is exactly where the script's confirmed bugs have lived. A branch with no
# commits of its own is trivially an ancestor of what it was cut from, so
# --is-ancestor alone called it "merged" and the dirty-check backstop did not save
# it — a fresh checkout nobody has written to yet is perfectly clean (kata 98x9;
# scenario 1). Scenario 5 covers a second, independently-found parsing bug in the
# same neighborhood: `git worktree list`'s human format appends a bare "locked" or
# "prunable" word after the branch name, which the old bracket-stripping parse
# folded into the branch name itself, sending every locked/prunable worktree down
# the "unresolvable branch" path.
#
# Scenarios 7-9 are kata datr, the SECOND time this script deleted live agent
# worktrees. 98x9's fix tested "unstarted" as `tip == into_tip`, which is only
# true at cut time (7); the dirty-check does not see git-IGNORED content, so a
# checkout holding nothing but an agent's reports is "clean" (8); and the MAIN
# checkout reached the candidate list at all (9).
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/disk-reclaim.sh"
checks=0 fails=0
ok() { checks=$((checks + 1)); printf '  ok: %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL: %s\n' "$1"; }

work="$(mktemp -d -t disk-reclaim-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

# Point every run at a throwaway build cache that does not exist. The report
# path `du -sh`s GOCACHE whenever it is a real directory, and on a machine
# whose cache holds a couple of million inodes that is ~32s PER RUN — the
# suite's whole runtime, spent measuring ambient machine state no scenario
# asserts on. Scenarios that are about the GOCACHE block set GOCACHE
# explicitly on the invocation, which overrides this.
export GOCACHE="$work/absent-gocache"

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
		printf '.superpowers/\nnode_modules/\n' >.gitignore &&
		git add file .gitignore &&
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
# Same claim the single-line report made before the classes were split out:
# zero merged, and the unstarted branch is the one entry, on the kept side.
if echo "$out" | grep -qE '0 of 0 merged into ' && echo "$out" | grep -qE '^ +kept +[0-9.]+[KMGTP] +1 unmerged$'; then
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

# --- scenario 7: a branch with no work of its OWN survives, in every shape (kata datr) ---
# The second five-worktree deletion. `tip == into_tip` is a valid proxy for
# "unstarted" only at cut time: let merges land on the base between `git
# worktree add` and this run, and every fresh branch has a tip that is no
# longer the base's while still being an ancestor of it. All three shapes below
# hold zero work of their own and all reach `--is-ancestor` as true, so all
# three are removed by an ancestry-only classifier:
#
#   stale-base  never touched at all
#   ff-catchup  fast-forwarded to the base to catch up, committed nothing — a
#               cut-point diff on its own counts those base commits as "its own"
#   reset-back  committed, then moved back to the commit it was cut from — a
#               "did anyone ever commit here" reflog scan on its own still
#               says yes
(cd "$repo" && git worktree add -q -b stale-base "$wt_dir/stalebase" main)
(cd "$repo" && git worktree add -q -b ff-catchup "$wt_dir/ffcatchup" main)
(cd "$repo" && git worktree add -q -b reset-back "$wt_dir/resetback" main)
# update-ref rather than `reset --hard`: it moves the branch back to its cut
# point and leaves the checkout CLEAN, so the dirty backstop cannot be the
# thing that saves it.
(cd "$wt_dir/resetback" && git commit -q --allow-empty -m "work later thrown away" && git update-ref refs/heads/reset-back main)
# Advance main the way a fleet's base advances: another branch lands on it.
(cd "$repo" && git worktree add -q -b base-advance "$wt_dir/baseadvance" main)
(cd "$wt_dir/baseadvance" && echo seven >file7 && git add file7 && git commit -qm "base-advance change")
(cd "$repo" && git merge --no-ff -q -m "merge base-advance" base-advance)
(cd "$wt_dir/ffcatchup" && git merge --ff-only -q main)
# ...and once more, so ff-catchup's tip is no longer the base's tip either.
(cd "$repo" && git commit -q --allow-empty -m "base advances again")
out="$(run_disk_reclaim --worktrees --into main)"
for shape in stalebase ffcatchup resetback; do
	if [ -d "$wt_dir/$shape" ]; then
		ok "worktree with no work of its own survives --worktrees ($shape)"
	else
		bad "worktree with no work of its own was REMOVED ($shape) — the five-worktree-deletion bug"
	fi
done
# Without this half the scenario would also pass against a script that had
# simply stopped removing anything.
if echo "$out" | grep -q "removed  base-advance"; then
	ok "a branch that really did land is still removed in the same run"
else
	bad "base-advance was not removed, so the assertions above prove nothing: $out"
fi
for shape in stalebase:stale-base ffcatchup:ff-catchup resetback:reset-back; do
	(cd "$repo" && git worktree remove "$wt_dir/${shape%%:*}" 2>/dev/null; git branch -D "${shape##*:}" >/dev/null 2>&1)
done

# --- scenario 8: a merged worktree holding only IGNORED content is kept (kata datr) ---
# `git worktree remove` without --force refuses modified tracked files and
# untracked files, but git considers a checkout whose only content is ignored
# perfectly clean — so it deleted a worktree holding an agent's `.superpowers/`
# SDD archive. Ignored is not the same as disposable.
(cd "$repo" && git worktree add -q -b ignored-work "$wt_dir/ignoredwork" main)
(cd "$wt_dir/ignoredwork" && echo eight >file8 && git add file8 && git commit -qm "ignored-work change")
(cd "$repo" && git merge --no-ff -q -m "merge ignored-work" ignored-work)
mkdir -p "$wt_dir/ignoredwork/.superpowers/sdd"
echo "the ledger nobody can regenerate" >"$wt_dir/ignoredwork/.superpowers/sdd/ledger.md"
# The same shape, but the ignored content is a rebuildable node_modules. This
# one must still be removed, or "keep every worktree with any ignored file at
# all" would satisfy the assertion above while making --worktrees a no-op.
(cd "$repo" && git worktree add -q -b regenerable-only "$wt_dir/regenonly" main)
(cd "$wt_dir/regenonly" && echo nine >file9 && git add file9 && git commit -qm "regenerable-only change")
(cd "$repo" && git merge --no-ff -q -m "merge regenerable-only" regenerable-only)
mkdir -p "$wt_dir/regenonly/node_modules/pkg"
echo "rebuildable" >"$wt_dir/regenonly/node_modules/pkg/index.js"
out="$(run_disk_reclaim --worktrees --into main)"
if [ -d "$wt_dir/ignoredwork" ]; then
	ok "merged worktree holding git-ignored work survives --worktrees"
else
	bad "merged worktree holding only git-ignored work was REMOVED (the .superpowers/sdd deletion)"
fi
if echo "$out" | grep -q "holds ignored work"; then
	ok "the kept worktree's ignored work is named in the report"
else
	bad "ignored-work keep was not reported: $out"
fi
if [ -d "$wt_dir/regenonly" ]; then
	bad "merged worktree holding only REGENERABLE ignored content was not removed"
else
	ok "regenerable ignored content (node_modules) does not block removal"
fi

# --- scenario 9: the MAIN checkout is never a removal candidate (kata datr) ---
# Run from a LINKED worktree against a base that main is an ancestor of — the
# fleet's own situation. main has commits of its own and they have landed, so
# the classifier reaches "merged" for it; the run that deleted five worktrees
# duly reported "kept main (dirty or in use)", saved only by git's refusal to
# remove a main working tree. run_disk_reclaim is not used here on purpose: it
# cds to the main checkout, which is exactly what must not be the caller.
(cd "$repo" && git worktree add -q -b caller "$wt_dir/caller" main)
(cd "$wt_dir/caller" && echo ten >file10 && git add file10 && git commit -qm "caller change")
out="$(cd "$wt_dir/caller" && bash "$script" --worktrees --into caller)"
if echo "$out" | grep -qE "^ +(removed|kept) +main(	|$)"; then
	bad "the MAIN checkout was classified a removal candidate: $out"
else
	ok "the MAIN checkout is never a removal candidate"
fi
if [ -d "$repo/.git" ]; then
	ok "the main checkout is still there"
else
	bad "the main checkout was removed"
fi

# --- scenario 10: --check fails loud below the floor, is silent above it ---
# SERF_SKIP_GOCACHE_CHECK=1: this scenario is about the disk-floor logic only;
# dedicated scenarios 11/12 below cover the GOCACHE block with GOCACHE pinned.
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

# --- scenario 11: --check fails loud when GOCACHE points at an unreachable
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

# --- scenario 12: --check warns (but does not fail) when GOCACHE is reachable
# but back on the same volume as the checkout ---
# SERF_DISK_MIN_FREE_GB=0 pins the floor: this scenario asserts exit 0, and
# without the pin it asserts the machine happens to have 5G free as well. It
# does not always — this failed for real on a night the repo volume was down to
# 2G, which is exactly when the rest of this script matters most.
samevol_gocache="$work/samevol-gocache"
if GOCACHE="$samevol_gocache" SERF_DISK_MIN_FREE_GB=0 run_disk_reclaim --check >"$work/gocache-samevol.out" 2>&1; then
	if grep -q "setup-gocache.sh" "$work/gocache-samevol.out"; then
		ok "--check warns (exit 0) when GOCACHE shares the checkout's volume"
	else
		bad "--check with same-volume GOCACHE exited 0 but did not warn: $(cat "$work/gocache-samevol.out")"
	fi
else
	bad "--check with a reachable (same-volume) GOCACHE exited non-zero"
fi

# --- scenarios 13-15 use their own repo, laid out the way the real one is ---
# The fixture above puts its worktrees in "$repo/wt", but the script sizes
# "$main_root/.claude/worktrees" — so nothing above this line has ever
# exercised the sizing code at all. These scenarios need a known population in
# the real location, and a population that does not drift as scenarios are
# added above.
repo2="$work/repo2"
wt2="$repo2/.claude/worktrees"
mkdir -p "$wt2"
(
	cd "$repo2" &&
		git init -q &&
		git config user.email t@t &&
		git config user.name t &&
		git symbolic-ref HEAD refs/heads/main &&
		echo one >file &&
		printf '.superpowers/\nnode_modules/\n' >.gitignore &&
		git add file .gitignore &&
		git commit -qm init
) || {
	echo "FAIL: could not set up the second throwaway repo" >&2
	exit 1
}
run_in_repo2() (
	cd "$repo2" && bash "$script" "$@"
)
add_merged_wt2() { # name -> a worktree whose branch has landed in main
	(cd "$repo2" && git worktree add -q -b "$1" "$wt2/$1" main)
	(cd "$wt2/$1" && echo payload >"f-$1" && git add "f-$1" && git commit -qm "$1 change")
	(cd "$repo2" && git merge --no-ff -q -m "merge $1" "$1")
}
add_merged_wt2 merged-free
add_merged_wt2 merged-held
# merged-held is merged AND holds an agent's report, so --worktrees keeps it.
# The 8M payload is what makes the size assertions below sharp: counted in the
# wrong bucket it shows up as megabytes, and no rounding can hide that.
mkdir -p "$wt2/merged-held/.superpowers"
dd if=/dev/zero of="$wt2/merged-held/.superpowers/report.md" bs=1024 count=8192 2>/dev/null
(cd "$repo2" && git worktree add -q -b live-branch "$wt2/live-branch" main)
(cd "$wt2/live-branch" && echo live >f-live && git add f-live && git commit -qm "live change")
# A plain directory git has no record of: kata smw0's shape, and 1.6G of the
# real machine's worktree directory.
mkdir -p "$wt2/unregistered-dir"
echo payload >"$wt2/unregistered-dir/some-file"

out="$(run_in_repo2 --into main)"

# --- scenario 13: the report sizes each worktree class separately ---
# The single `du -sh` this replaced read as "what --worktrees could free". On
# the real machine that number was 8.3G and the true answer was zero: every
# merged worktree held an agent's .superpowers report. A number that overstates
# the yield is the failure this kata exists to remove, so "removable" must
# exclude what the script already knows it will keep.
if echo "$out" | grep -qE '^ +removable +[0-9.]+K.*1 of 2 merged'; then
	ok "removable is sized on its own, in K: the 8M ignored-work worktree is excluded"
else
	bad "removable line missing, miscounted, or sized in M (held worktree counted): $out"
fi
if echo "$out" | grep -qE '^ +kept +[0-9.]+M.*1 unmerged, 1 merged but holding ignored work'; then
	ok "kept carries the held megabytes and names why each worktree is kept"
else
	bad "kept line missing or did not account for the held worktree: $out"
fi
if echo "$out" | grep -qE '^ +unregistered +[0-9.]+K.*1 dir'; then
	ok "directories git has no record of are counted and sized as their own class"
else
	bad "unregistered class missing from the report: $out"
fi
if echo "$out" | grep -q "report-orphaned-worktrees.sh"; then
	ok "the unregistered class names the tool that can inspect it"
else
	bad "unregistered class named no tool: $out"
fi
if echo "$out" | grep -q "report-tmp-debris.sh"; then
	ok "the report names the /tmp scratch class it does not measure"
else
	bad "the report did not name the /tmp scratch class: $out"
fi
for name in merged-free merged-held live-branch unregistered-dir; do
	if [ -d "$wt2/$name" ]; then
		ok "report-only run leaves $name alone"
	else
		bad "report-only run REMOVED $name"
	fi
done
# The footer had the floor message's bug too: "Re-run with --cache,
# --worktrees, or --all" named a cache the same run had just called unreachable
# and a sweep it had just measured. GOCACHE is the absent path exported at the
# top of this file, so neither cache lever can do anything here.
if echo "$out" | grep -qE 'would free at most [0-9.]+[KMGTP]'; then
	ok "the footer states the measured upper bound on what --worktrees frees"
else
	bad "the footer did not state what --worktrees would free: $out"
fi
if echo "$out" | grep -qE '\-\-(cache|all)\b'; then
	bad "the footer offers --cache or --all with no reachable cache to empty: $out"
else
	ok "the footer offers no cache lever when there is no cache it can help with"
fi

# --- scenario 14: the report says whether emptying the cache can move THIS
# floor, and only pays for a du when it can ---
# `du` of the real machine's 111G build cache is 32.6s per run, on a volume
# where emptying it provably cannot move this checkout's floor. The off-volume
# branch cannot be built portably (it needs a second device), so it is verified
# against the real repo by timing; the branch below is the one a fixture can
# reach, and it is the one that must still pay for the number.
samevol_cache="$work/samevol-report-cache"
mkdir -p "$samevol_cache"
dd if=/dev/zero of="$samevol_cache/blob" bs=1024 count=256 2>/dev/null
cache_out="$(GOCACHE="$samevol_cache" run_in_repo2 --into main)"
if echo "$cache_out" | grep -qF "$samevol_cache"; then
	ok "the report names the build cache path"
else
	bad "the report did not name the build cache path: $cache_out"
fi
# Anchored on the label, not just "a size somewhere on the line": mktemp's
# random suffix can itself contain a digit followed by K/M/G, and matching that
# would be a green with no behaviour behind it.
if echo "$cache_out" | grep -qE 'go build cache +[0-9.]+[KMGTP]'; then
	ok "a cache on this volume is sized: that size is what --cache would free here"
else
	bad "a cache on this checkout's own volume was not sized: $cache_out"
fi
if echo "$cache_out" | grep -qi "this volume"; then
	ok "the report states that emptying the cache can move this floor"
else
	bad "the report did not say whether --cache can move this floor: $cache_out"
fi
if echo "$cache_out" | grep -qE '\-\-cache would free'; then
	ok "the footer offers --cache when the cache IS on this volume"
else
	bad "the cache lever was suppressed in the footer even though it can help: $cache_out"
fi

# --- scenario 15: the floor message offers only levers it can substantiate ---
# The message fires on every test run below the floor and is the whole remedy
# the next person gets. Measured on the real machine at 3G free: it named
# --worktrees, worth exactly 0 bytes, while 10.0G of /tmp scratch and 1.6G of
# unregistered checkouts went unnamed. --check is a bare df by design and
# cannot measure, so it must point at what can.
floor_out=$(SERF_SKIP_GOCACHE_CHECK=1 SERF_DISK_MIN_FREE_GB=999999999 run_disk_reclaim --check 2>&1)
for tool in "scripts/disk-reclaim.sh" "scripts/report-orphaned-worktrees.sh" "scripts/report-tmp-debris.sh"; do
	if echo "$floor_out" | grep -qF "$tool"; then
		ok "the floor message points at $tool"
	else
		bad "the floor message never mentions $tool: $floor_out"
	fi
done
if echo "$floor_out" | grep -qiE '(routinely|often|usually) (frees )?(0|nothing|zero)'; then
	ok "--worktrees is offered with its real mid-fleet yield, not as a sure remedy"
else
	bad "--worktrees is still offered as though it will free something: $floor_out"
fi
# Guard: none of the above should be satisfiable by a message that stopped
# saying how bad the situation is.
if echo "$floor_out" | grep -qE 'free.*floor is'; then
	ok "the floor message still states the free space and the floor"
else
	bad "the floor message lost its numbers: $floor_out"
fi
# `--all` means "cache and worktrees". When the cache lever is suppressed —
# because emptying it cannot move this floor — an unconditional "--all # both"
# points at a lever the same message deliberately declined to name.
if echo "$floor_out" | grep -qE 'disk-reclaim\.sh --(cache|all)\b'; then
	bad "--cache or --all offered when the cache cannot move this floor: $floor_out"
else
	ok "neither --cache nor --all is offered when the cache cannot move this floor"
fi
cache_floor_out=$(GOCACHE="$samevol_cache" SERF_DISK_MIN_FREE_GB=999999999 run_disk_reclaim --check 2>&1)
if echo "$cache_floor_out" | grep -qE 'disk-reclaim\.sh --cache\b' && echo "$cache_floor_out" | grep -qE 'disk-reclaim\.sh --all\b'; then
	ok "both --cache and --all are offered when the cache IS on this volume"
else
	bad "the cache lever was suppressed even though it can move this floor: $cache_floor_out"
fi

# --- scenario 16: --help prints the whole header, not a stale line range ---
# It printed lines 2-64 of a header that had grown to 77, so --help stopped
# mid-sentence and dropped the Safety paragraph entirely. Nothing failed; the
# documentation just quietly went missing.
if help_out=$(run_disk_reclaim --help 2>&1) && echo "$help_out" | grep -q "^Usage:" && echo "$help_out" | grep -q "^Safety:"; then
	ok "--help prints the header through to its last paragraph"
else
	bad "--help is truncated or failed: $help_out"
fi
if echo "$help_out" | grep -q "^set -uo pipefail"; then
	bad "--help ran past the header into the script body: $help_out"
else
	ok "--help stops at the end of the header"
fi

echo
echo "$checks checks, $fails failed"
[ "$fails" -eq 0 ]
