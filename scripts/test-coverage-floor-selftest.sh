#!/usr/bin/env bash
# test-coverage-floor-selftest.sh — exercises scripts/test-coverage-floor.sh against
# a throwaway repo and a fake `go`, so the rollup and ratchet arithmetic is checked
# without compiling or running a real suite. Run via `make test-coverage-floor-selftest`
# or the dev-tooling wave.
#
# The regression that motivated this suite: the script accumulated measurements in a
# `declare -A` associative array, which bash 3.2 — the `env bash` every macOS host
# resolves — does not have. Worse, module "." made the fallback subscript an
# ARITHMETIC expression, so the run died mid-loop with "operand expected" after
# paying for a full coverage build. The script had no selftest, so it read as
# working for as long as nobody ran it on a Mac. The "." row assertions below are
# that tripwire: they fail loudly on any bash-4-only construct reintroduced here.
set -uo pipefail

real_script="$(cd "$(dirname "$0")" && pwd)/test-coverage-floor.sh"
. "$(dirname "$0")/selftest-lib.sh"

scratch_dir work evener-testcov-selftest
trap 'scratch_rm' EXIT

# The script derives repo_root and the floors path from its OWN location, so the
# copy lives in the throwaway repo's scripts/ and both land inside it naturally —
# no production seam needed, and the real file's content is what runs.
repo="$work/repo"
mkdir -p "$repo/scripts" "$repo/agent"
cp "$real_script" "$repo/scripts/test-coverage-floor.sh"
# The script sources its gate-surface definition from its own directory, so the
# throwaway repo needs the real one beside it.
cp "$(dirname "$0")/gate-surface-lib.sh" "$repo/scripts/gate-surface-lib.sh"
cp "$(dirname "$0")/covstmt-lib.sh" "$repo/scripts/covstmt-lib.sh"
cp "$(dirname "$0")/covscratch-lib.sh" "$repo/scripts/covscratch-lib.sh"
script="$repo/scripts/test-coverage-floor.sh"
floors="$repo/scripts/testcov-global-floors.txt"
printf 'module fake\n\ngo 1.25\n' >"$repo/go.mod"
printf 'module fake/agent\n\ngo 1.25\n' >"$repo/agent/go.mod"

# A fake `go` that writes a synthetic coverprofile instead of compiling. The
# per-module split keys off the directory it was invoked in, which is how the
# suite tells the "." row apart from the "agent" row.
fake_bin="$work/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/go" <<'FAKEGO'
#!/bin/sh
[ -n "${FAKE_GO_LOG:-}" ] && echo "$(basename "$PWD") $*" >>"$FAKE_GO_LOG"
# `go list ./...` is how the script scopes -coverpkg to the module under test.
if [ "$1" = list ]; then
	echo "fake/$(basename "$PWD")/alpha"
	echo "fake/$(basename "$PWD")/beta"
	exit 0
fi
prof=""
for a in "$@"; do
	case "$a" in -coverprofile=*) prof="${a#-coverprofile=}" ;; esac
done
# FAKE_GO_SLEEP holds a measurement open so the run can be observed and
# signalled while its scratch directory exists.
[ -n "${FAKE_GO_SLEEP:-}" ] && sleep "$FAKE_GO_SLEEP"
[ -n "$prof" ] || { echo "fake go: no -coverprofile in: $*" >&2; exit 2; }
# FAKE_GO_EMPTY_PROF simulates a run whose profile counts nothing — the shape
# every module takes when stmt_counts itself breaks (e.g. no python3).
if [ -n "${FAKE_GO_EMPTY_PROF:-}" ]; then
	echo "mode: set" >"$prof"
	exit 0
fi
case "$(basename "$PWD")" in
	repo)  cov=1 ;;   # the "." module: 1 of 4 statements -> 25.0%
	agent) cov=3 ;;   # 3 of 4 statements -> 75.0%
	*)     cov=2 ;;
esac
{
	echo "mode: set"
	echo "fake/a.go:1.1,2.1 $cov 1"
	echo "fake/b.go:3.1,4.1 $((4 - cov)) 0"
} >"$prof"
exit 0
FAKEGO
chmod +x "$fake_bin/go"

go_log="$work/go-invocations.txt"
# A private TMPDIR makes the script's own scratch observable: the wave runner
# fails a suite that leaves anything behind, and the profiles directory used to
# leak once per invocation.
tmphome="$work/tmp"
mkdir -p "$tmphome"
run() { PATH="$fake_bin:$PATH" FAKE_GO_LOG="$go_log" TMPDIR="$tmphome" bash "$script" --modules ". agent" "$@"; }

out="$work/out.txt"
: >"$floors"
run >"$out" 2>&1
status=$?
assert_eq "$status" "0" "measure-only run exits zero"

# The bash-3.2 tripwire: under the old declare -A the run emitted these and then
# died before printing a single module row.
assert_not_has "$out" "declare: -A" "no bash-4-only declare -A"
assert_not_has "$out" "operand expected" "module \".\" is not evaluated as arithmetic"

if grep -qE '^\. +1 +4 +25\.0%' "$out"; then
	ok "module \".\" is measured and printed (25.0%)"
else
	bad "the \".\" row is missing or wrong"; sed 's/^/    | /' "$out"
fi
if grep -qE '^agent +3 +4 +75\.0%' "$out"; then
	ok "module \"agent\" is measured and printed (75.0%)"
else
	bad "the agent row is missing or wrong"; sed 's/^/    | /' "$out"
fi

# The measured surface must stay the gate's surface. A ratchet that drifts from
# what `ROOT_FULL=1 make test` proves cannot be defended when its number moves,
# so the gate's own -run/-skip selection is asserted here rather than assumed.
. "$(dirname "$0")/gate-surface-lib.sh"
assert_has "$go_log" "-run ^(Test|Example)" "the coverage run uses the gate's Test/Example filter"
assert_has "$go_log" "-skip $GATE_FUZZ_TEST_SKIP" "the coverage run skips the fuzz-owned names"
# -coverpkg must name the module's OWN packages. `./...` is a filesystem pattern
# that under go.work also matches every nested module, which turned the root row
# into a whole-repo number diluted by code its tests never run.
assert_has "$go_log" "-coverpkg=fake/repo/alpha,fake/repo/beta" "-coverpkg is scoped to the module's own packages"
assert_not_has "$go_log" "-coverpkg=./..." "-coverpkg is never the tree-wide pattern"
if grep -q '^agent .*-short' "$go_log"; then
	ok "non-root modules are measured in -short mode"
else
	bad "agent was measured without -short"; sed 's/^/    | /' "$go_log"
fi
if grep -q '^repo .*-short' "$go_log"; then
	bad "the root module must NOT be measured in -short mode (ROOT_FULL semantics)"
	sed 's/^/    | /' "$go_log"
else
	ok "the root module is measured without -short"
fi

assert_eq "$(ls -A "$tmphome")" "" "a clean run leaves no scratch directory behind"

# ...which only means something if the scratch would have landed in $tmphome at
# all. See scratch-selftest-lib.sh: it did not, and the line above passed for as
# long as the bug existed.
. "$(dirname "$0")/scratch-selftest-lib.sh"
start_scratch_run() {
	set -m   # give the run its own process group, so a signal reaches the group
	PATH="$fake_bin:$PATH" FAKE_GO_SLEEP=30 TMPDIR="$tmphome" \
		bash "$script" --modules ". agent" >"$out" 2>&1 &
	run_pid=$!
	set +m
}
assert_scratch_inside_tmpdir
assert_killed_run_cleans_up TERM
assert_killed_run_cleans_up INT
assert_killed_run_cleans_up HUP

# The exits a trap cannot see, and the failed run that keeps its scratch on
# purpose, are this script's own to reclaim: no janitor sweeps them.
scratch_prefix=evener-testcov
assert_reclaims_abandoned_scratch
assert_keeps_concurrent_scratch
printf '. 90.0\nagent 75.0\n' >"$floors"
assert_failed_run_keeps_scratch_until_next_run run --check
: >"$floors"

# --bless must carry the MEASURED percentages through; the associative-array bug
# silently wrote back stale floors here even when the rows above looked right.

run --bless >"$out" 2>&1
assert_has "$floors" ". 25.0" "bless records the measured \".\" floor"
assert_has "$floors" "agent 75.0" "bless records the measured agent floor"

run --check >"$out" 2>&1
assert_eq "$?" "0" "check passes at the blessed floors"

# A hand-written basis note explains why a floor was reset downward. Blessing
# raises other floors later, and rewriting the header would delete that reason.
printf '# why this basis changed: measured surface moved\n. 25.0\nagent 75.0\n' >"$floors"
run --bless >"$out" 2>&1
assert_has "$floors" "why this basis changed" "bless preserves a hand-written header note"
assert_has "$floors" "agent 75.0" "bless still records floors alongside the preserved note"

# Improving one module at a time is the normal way coverage work happens, so a
# partial bless must not delete the floors it did not measure.
printf '. 10.0\nagent 10.0\nllm 91.4\nauth 95.7\n' >"$floors"
PATH="$fake_bin:$PATH" FAKE_GO_LOG="$go_log" TMPDIR="$tmphome" bash "$script" --modules "agent" --bless >"$out" 2>&1
assert_has "$floors" "agent 75.0" "a partial bless raises the module it measured"
assert_has "$floors" "llm 91.4" "a partial bless keeps an unmeasured module's floor"
assert_has "$floors" "auth 95.7" "a partial bless keeps every other unmeasured floor"
assert_has "$floors" ". 10.0" "a partial bless keeps the root floor it did not measure"

printf '. 90.0\nagent 75.0\n' >"$floors"
run --check >"$out" 2>&1
assert_eq "$?" "1" "check fails when a module drops below its floor"
assert_has "$out" "REGRESSION: ." "the failing module is named"

printf '. 25.3\nagent 75.0\n' >"$floors"
run --check >"$out" 2>&1
assert_eq "$?" "0" "a drop within tolerance is not a regression"

printf '. 99.0\nagent 75.0\n' >"$floors"
run --bless >"$out" 2>&1
assert_has "$floors" ". 99.0" "bless keeps a higher existing floor"

# A module directory with no go.mod is reported, not silently skipped.
run --modules "nosuch" >"$out" 2>&1
assert_has "$out" "(no module)" "a missing module is reported"

# A FLOORED module that cannot be measured is an unenforced ratchet, not a
# skippable row: under --check it must fail loudly. Without this, renaming or
# deleting a floored module quietly disabled its floor while check stayed green.
printf 'nosuch 50.0\n' >"$floors"
run --modules "nosuch" --check >"$out" 2>&1
assert_eq "$?" "1" "check fails when a floored module cannot be measured"
assert_has "$out" "UNMEASURED: nosuch" "the unmeasurable floored module is named"

# ...while a module nobody floored keeps its advisory skip.
: >"$floors"
run --modules "nosuch" --check >"$out" 2>&1
assert_eq "$?" "0" "an unmeasurable module without a floor stays a reported skip"

# The same enforcement covers the profile-counts-nothing shape, which is what
# EVERY floored module degrades to when stmt_counts itself breaks.
printf '. 25.0\nagent 75.0\n' >"$floors"
FAKE_GO_EMPTY_PROF=1 run --check >"$out" 2>&1
assert_eq "$?" "1" "check fails when a floored module's profile counts no statements"
assert_has "$out" "no statements" "the empty measurement is reported"
assert_has "$out" "UNMEASURED: agent" "the floored module with an empty measurement is named"

help_out="$(bash "$script" --help 2>&1)"
if echo "$help_out" | grep -q "^Usage:" && ! echo "$help_out" | grep -q "^set -uo pipefail"; then
	ok "--help prints the whole header and stops at the script body"
else
	bad "--help is truncated or overran the header: $help_out"
fi

selftest_summary
