#!/usr/bin/env bash
# coverage-union-selftest.sh — exercises scripts/coverage-union.sh against a
# throwaway repo and a fake `go` that emits two DIFFERENT profiles depending on
# whether it was invoked with -tags evenerfuzz. No compilation, no real suite.
#
# The fixture is chosen so the union cannot be mistaken for either input: the
# test track covers only block A, the fuzz track covers only block B, so a
# correct union reports 100% where each track alone reports 40% and 60%.
set -uo pipefail

real_script="$(cd "$(dirname "$0")" && pwd)/coverage-union.sh"
. "$(dirname "$0")/../lib/selftest-lib.sh"

scratch_dir work evener-covunion-selftest
trap 'scratch_rm' EXIT

repo="$work/repo"
mkdir -p "$repo/scripts/coverage" "$repo/scripts/lib" "$repo/agent"
cp "$real_script" "$repo/scripts/coverage/coverage-union.sh"
cp "$(dirname "$0")/../lib/gate-surface-lib.sh" "$repo/scripts/lib/gate-surface-lib.sh"
cp "$(dirname "$0")/../lib/covstmt-lib.sh" "$repo/scripts/lib/covstmt-lib.sh"
cp "$(dirname "$0")/../lib/covscratch-lib.sh" "$repo/scripts/lib/covscratch-lib.sh"
script="$repo/scripts/coverage/coverage-union.sh"
floors="$repo/scripts/coverage/covunion-floors.txt"
printf 'module fake\n\ngo 1.25\n' >"$repo/go.mod"
printf 'module fake/agent\n\ngo 1.25\n' >"$repo/agent/go.mod"

fake_bin="$work/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/go" <<'FAKEGO'
#!/bin/sh
if [ "$1" = list ]; then
	echo "fake/$(basename "$PWD")/alpha"
	exit 0
fi
prof=""
tagged=0
for a in "$@"; do
	case "$a" in
		-coverprofile=*) prof="${a#-coverprofile=}" ;;
		evenerfuzz) tagged=1 ;;
	esac
done
[ -n "$prof" ] || { echo "fake go: no -coverprofile in: $*" >&2; exit 2; }
# FAKE_GO_SLEEP holds a measurement open so the run can be observed and
# signalled while its scratch directory exists.
[ -n "${FAKE_GO_SLEEP:-}" ] && sleep "$FAKE_GO_SLEEP"
{
	echo "mode: set"
	if [ "$tagged" = 1 ]; then
		# The fuzz track reaches blocks B and C only.
		echo "fake/a.go:1.1,2.1 200 0"
		if [ -n "${FAKE_GO_SHIFT_FUZZ_BLOCKS:-}" ]; then
			# A large block split differently by the two builds — a different
			# basis, whose union denominator would be meaningless.
			echo "fake/b.go:3.1,4.9 298 1"
		else
			echo "fake/b.go:3.1,4.1 298 1"
		fi
		if [ -n "${FAKE_GO_TINY_VARIANCE:-}" ]; then
			# ONE re-split block, the scale the evenerfuzz tag genuinely produces.
			echo "fake/c.go:9.1,9.9 2 1"
		else
			echo "fake/c.go:9.1,9.5 2 1"
		fi
	else
		# The test track reaches block A only.
		echo "fake/a.go:1.1,2.1 200 1"
		echo "fake/b.go:3.1,4.1 298 0"
		echo "fake/c.go:9.1,9.5 2 0"
	fi
} >"$prof"
exit 0
FAKEGO
chmod +x "$fake_bin/go"

tmphome="$work/tmp"
mkdir -p "$tmphome"
run() { PATH="$fake_bin:$PATH" TMPDIR="$tmphome" bash "$script" --modules "agent" "$@"; }

out="$work/out.txt"
: >"$floors"
run >"$out" 2>&1
assert_eq "$?" "0" "measure-only run exits zero"

# 500 of 500 statements, where neither track alone reaches more than 300.
if grep -qE '^agent +500 +500 +100\.0% +40\.0% +60\.0%' "$out"; then
	ok "union counts a block covered by EITHER track (100% from 40% and 60%)"
else
	bad "union row wrong"; sed 's/^/    | /' "$out"
fi
assert_eq "$(ls -A "$tmphome")" "" "a clean run leaves no scratch directory behind"

# ...which only means something if the scratch would have landed in $tmphome at
# all. See covscratch-selftest-lib.sh: it did not, and the line above passed for as
# long as the bug existed.
. "$(dirname "$0")/../lib/covscratch-selftest-lib.sh"
start_scratch_run() {
	set -m   # give the run its own process group, so a signal reaches the group
	PATH="$fake_bin:$PATH" FAKE_GO_SLEEP=30 TMPDIR="$tmphome" \
		bash "$script" --modules "agent" >"$out" 2>&1 &
	run_pid=$!
	set +m
}
assert_scratch_inside_tmpdir
assert_killed_run_cleans_up TERM
assert_killed_run_cleans_up INT
assert_killed_run_cleans_up HUP

# The exits a trap cannot see, and the failed run that keeps its scratch on
# purpose, are this script's own to reclaim: no janitor sweeps them.
scratch_prefix=evener-covunion
assert_reclaims_abandoned_scratch
assert_keeps_concurrent_scratch
printf 'nosuch 50.0\n' >"$floors"
assert_failed_run_keeps_scratch_until_next_run run --modules "nosuch" --check
: >"$floors"

run --bless >"$out" 2>&1
assert_has "$floors" "agent 100.0" "bless records the measured union floor"

run --check >"$out" 2>&1
assert_eq "$?" "0" "check passes at the blessed floor"

printf '# keep this note\nagent 100.0\nllm 77.0\n' >"$floors"
run --bless >"$out" 2>&1
assert_has "$floors" "keep this note" "bless preserves a hand-written header note"
assert_has "$floors" "llm 77.0" "a partial bless keeps an unmeasured module's floor"

printf 'agent 100.0\n' >"$floors"
PATH="$fake_bin:$PATH" TMPDIR="$tmphome" FAKE_GO_FAIL_ALL=1 bash "$script" --modules "nosuch" >"$out" 2>&1
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

# A union denominator larger than both tracks means the tagged and untagged
# builds disagree about block boundaries; the percentage would be nonsense.
PATH="$fake_bin:$PATH" TMPDIR="$tmphome" FAKE_GO_SHIFT_FUZZ_BLOCKS=1 bash "$script" --modules "agent" >"$out" 2>&1
assert_eq "$?" "1" "a boundary mismatch fails rather than reporting a nonsense percentage"
assert_has "$out" "boundary mismatch" "the boundary mismatch is named"

# A handful of re-split blocks is what the evenerfuzz tag genuinely produces
# (invariant.Hold becomes a real call), so it is reported, not fatal — failing
# there would discard a whole module's measurement over 0.02%.
PATH="$fake_bin:$PATH" TMPDIR="$tmphome" FAKE_GO_TINY_VARIANCE=1 bash "$script" --modules "agent" >"$out" 2>&1
assert_eq "$?" "0" "a negligible boundary variance still yields a measurement"
assert_has "$out" "boundary-variant" "the variance is reported rather than hidden"

help_out="$(bash "$script" --help 2>&1)"
if echo "$help_out" | grep -q "^Usage:" && ! echo "$help_out" | grep -q "^set -uo pipefail"; then
	ok "--help prints the whole header and stops at the script body"
else
	bad "--help is truncated or overran the header: $help_out"
fi

selftest_summary
