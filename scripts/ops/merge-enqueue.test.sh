#!/usr/bin/env bash
# merge-enqueue.test.sh: simulate merge-queue drops for merge-enqueue.sh with
# a stubbed `gh`, no network and no real repository state.
#
#   bash scripts/ops/merge-enqueue.test.sh
#
# Each drop scenario runs the real script (`--pr 6499 --wait 1`) against a stub
# that answers the GraphQL status / removal-event calls and `gh run list`
# from canned data, then checks the run identity the drop outcome names:
# the run id and URL when a merge-group run resolves for this PR, the queue
# branch prefix when none does. Two further scenarios pin the non-drop
# outcomes (enqueue with `--wait 0`, timeout with `--wait 1`).
# Assertions are on those data tokens, the `MERGEQ dropped:` outcome marker, the
# exit code, and the `gh run list` call itself — never on the surrounding prose.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/merge-enqueue.sh"
HEAD_SHA="abcdef0123456789abcdef0123456789abcdef01"
# GitHub names a merge-queue branch with the base tip the queue tested the
# candidate on top of, not the PR's head:
#
#	gh-readonly-queue/<base>/pr-<N>-<base tip>
#
# This suffix is deliberately a different commit from HEAD_SHA. A test that put
# HEAD_SHA here would pass against a lookup that reconstructs the branch from
# the PR head, which is the shape production does not have.
BASE_TIP_SHA="114743f798cd16a42e0522a247157273e1203c28"
QUEUE_PREFIX="gh-readonly-queue/main/pr-6499-"
QUEUE_BRANCH="$QUEUE_PREFIX$BASE_TIP_SHA"
RUN_ID="123456789"
RUN_URL="https://github.com/prime-radiant-inc/evener/actions/runs/$RUN_ID"

# Scratch is minted by scratch_dir and reclaimed by the no-argument
# scratch_rm (scripts/lib/scratch-lib.sh): a recursive delete must never
# take an argument a caller could clobber. The trap is armed before the
# mint, so a failure in between still reclaims the directory.
. "$SCRIPT_DIR/../lib/scratch-lib.sh"
trap 'scratch_rm' EXIT
scratch_dir TEST_TMP merge-enqueue-test
STUBDIR="$TEST_TMP/bin"
mkdir -p "$STUBDIR"
GH_LOG="$TEST_TMP/gh-calls.log"
GH_STATE="$TEST_TMP/gh-state"
LAST_OUT="$TEST_TMP/last-out.txt"
LAST_CODE="$TEST_TMP/last-code.txt"

cat > "$STUBDIR/gh" <<'STUB'
#!/usr/bin/env bash
# Stub gh: answers from env, logs every call. The --jq expression the caller
# passes is evaluated with jq, exactly as real gh would evaluate it.
echo "$*" >> "$GH_LOG"
now=$(date +%s)
case "$*" in
  *RemovedFromMergeQueueEvent*)
    if [ "${STUB_FAIL_REASON:-0}" = "1" ]; then echo "HTTP 500" >&2; exit 1; fi
    printf '%s %s\n' "$(( now - ${STUB_EVENT_AGE:-60} ))" "${STUB_REASON-failed_checks}"
    ;;
  *enqueuePullRequest*)
    echo '{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"position":1}}}}'
    ;;
  *mergeQueueEntry*)
    # First status call: queued at position 1 unless STUB_FIRST_POS says
    # otherwise. Later calls: dropped (pos "-") unless STUB_NEVER_DROP=1.
    # 6 fields, as status() prints: id state sha pos qstate base.
    n=0; [ -f "$GH_STATE" ] && n=$(cat "$GH_STATE")
    n=$(( n + 1 )); printf '%s' "$n" > "$GH_STATE"
    if [ "$n" -eq 1 ]; then
      echo "PR_6499 OPEN - ${STUB_FIRST_POS:-1} QUEUED ${STUB_BASE:-main}"
    elif [ "${STUB_NEVER_DROP:-0}" = "1" ]; then
      echo "PR_6499 OPEN - 1 QUEUED ${STUB_BASE:-main}"
    else
      echo "PR_6499 OPEN - - - ${STUB_BASE:-main}"
    fi
    ;;
  *pr\ view*)
    # Not merged: merged_sha prints nothing and fails.
    exit 1
    ;;
  *run\ list*)
    # The script passes the queue branch prefix in the environment, because a
    # branch name can carry characters that would break a jq literal. Record it
    # beside the program so the test can assert what the filter matched on.
    printf 'QUEUE_PREFIX=%s\n' "${QUEUE_PREFIX:-}" >> "$GH_LOG"
    if [ "${STUB_RUN_FAIL:-0}" = "1" ]; then echo "HTTP 500" >&2; exit 1; fi
    jqexpr=""; prev=""
    for a in "$@"; do
      if [ "$prev" = "--jq" ]; then jqexpr="$a"; fi
      prev="$a"
    done
    printf '%s' "${STUB_RUN_JSON:-[]}" | jq -r "$jqexpr"
    ;;
  *)
    echo "stub gh: unexpected call: $*" >&2; exit 1
    ;;
esac
STUB
chmod +x "$STUBDIR/gh"

failures=0
scenario_failures=0
current_name=""

fail() {
  scenario_failures=$(( scenario_failures + 1 ))
  printf '  FAIL: %s\n' "$1"
}

# run_scenario NAME REASON EVENT_AGE RUN_JSON [RUN_FAIL] [FAIL_REASON] [EXPECT_RUN_LIST] [BASE]
#
# RUN_JSON is the stubbed `gh run list` output, newest first as gh returns it.
# EXPECT_RUN_LIST=0 says the status read supplied no base, so no lookup runs.
run_scenario() {
  current_name="$1"
  local out code
  scenario_failures=0
  : > "$GH_LOG"; : > "$GH_STATE"
  if out=$(PATH="$STUBDIR:$PATH" GH_LOG="$GH_LOG" GH_STATE="$GH_STATE" \
      STUB_REASON="$2" STUB_EVENT_AGE="$3" \
      STUB_RUN_JSON="$4" STUB_RUN_FAIL="${5:-0}" STUB_FAIL_REASON="${6:-0}" \
      STUB_BASE="${8:-main}" \
      bash "$SCRIPT" --pr 6499 --wait 1 --repo prime-radiant-inc/evener 2>&1); then
    code=0
  else
    code=$?
  fi
  printf '%s' "$out" > "$LAST_OUT"
  printf '%s' "$code" > "$LAST_CODE"
  # The drop outcome is exit 1 with the documented outcome marker.
  [ "$code" = "1" ] || fail "exit code is $code, want 1"
  grep -Fq "MERGEQ dropped:" "$LAST_OUT" || fail "no MERGEQ dropped: outcome line"
  # The script looked up this PR's merge-group runs by the queue branch
  # prefix, never by a branch reconstructed from the PR head.
  if [ "${7:-1}" = "1" ]; then
    grep -Fq "run list --repo prime-radiant-inc/evener --event merge_group" "$GH_LOG" \
      || fail "no gh run list --event merge_group call"
    grep -Fq "$QUEUE_PREFIX" "$GH_LOG" \
      || fail "run list did not filter on queue branch prefix $QUEUE_PREFIX"
    if grep -Fq "$HEAD_SHA" "$GH_LOG"; then
      fail "run list keyed on the PR head $HEAD_SHA, which the queue branch does not carry"
    fi
  fi
  return 0
}

expect_token() { grep -Fq "$1" "$LAST_OUT" || fail "output lacks $2"; }
expect_no_token() { if grep -Fq "$1" "$LAST_OUT"; then fail "output carries $2"; fi; }

RUN_JSON="[{\"databaseId\":$RUN_ID,\"url\":\"$RUN_URL\",\"conclusion\":\"failure\",\"headBranch\":\"$QUEUE_BRANCH\"}]"

run_scenario "failed_checks names the run" failed_checks 60 "$RUN_JSON"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "checks_timed_out names the run" checks_timed_out 60 "$RUN_JSON"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "empty reason names the run" "" 60 "$RUN_JSON"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "stale event still names the run" failed_checks 3600 "$RUN_JSON"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "unknown reason names the run" merge_conflict 60 "$RUN_JSON"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "reason lookup failure names the run" failed_checks 60 "$RUN_JSON" 0 1
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "no run resolves: names the queue branch prefix" failed_checks 60 "[]"
expect_token "$QUEUE_PREFIX" "queue branch prefix $QUEUE_PREFIX"
expect_no_token "$RUN_ID" "a run id no run supplied"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "run lookup fails: names the queue branch prefix" failed_checks 60 "$RUN_JSON" 1
expect_token "$QUEUE_PREFIX" "queue branch prefix $QUEUE_PREFIX"
expect_no_token "$RUN_ID" "a run id from a lookup that failed"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "run without conclusion still names id and URL" failed_checks 60 "[{\"databaseId\":$RUN_ID,\"url\":\"$RUN_URL\",\"headBranch\":\"$QUEUE_BRANCH\"}]"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "no base from status: falls back without crashing" failed_checks 60 "$RUN_JSON" 0 0 0 "-"
if grep -Fq "run list" "$GH_LOG"; then fail "gh run list called with no base to filter by"; fi
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "null base from status: falls back without crashing" failed_checks 60 "$RUN_JSON" 0 0 0 "null"
if grep -Fq "run list" "$GH_LOG"; then fail "gh run list called with a null base to filter by"; fi
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# The shape production has: the only run for this PR carries the base tip, and
# the base tip is not the PR head. Only a prefix match resolves it.
run_scenario "base-tip suffix, not the PR head: names that run" failed_checks 60 "$RUN_JSON"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
expect_no_token "pr-6499-$HEAD_SHA" "a queue branch built from the PR head"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# A push to the queued PR between the drop and its detection changes the PR
# head, and the entry that dropped ran on the old one. The run is found by PR
# number and base, so no head reaches the lookup and a push cannot redirect it
# to a run for a head that never got a merge-group run.
run_scenario "a post-drop push does not change the run named" failed_checks 60 "$RUN_JSON"
expect_token "$RUN_ID" "run id $RUN_ID"
if grep -Eq '[0-9a-f]{40}' "$GH_LOG"; then
  fail "run list was given a commit SHA: $(grep -Eo '[0-9a-f]{40}' "$GH_LOG" | head -1)"
fi
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# A newer run belongs to another PR: the prefix must select this PR's row, not
# simply the first row in the list.
OTHER_RUN_ID="987654321"
OTHER_RUN_URL="https://github.com/prime-radiant-inc/evener/actions/runs/$OTHER_RUN_ID"
RUN_JSON_MIXED="[{\"databaseId\":$OTHER_RUN_ID,\"url\":\"$OTHER_RUN_URL\",\"conclusion\":\"failure\",\"headBranch\":\"gh-readonly-queue/main/pr-6500-$BASE_TIP_SHA\"},{\"databaseId\":$RUN_ID,\"url\":\"$RUN_URL\",\"conclusion\":\"failure\",\"headBranch\":\"$QUEUE_BRANCH\"}]"
run_scenario "a newer run for another PR is skipped" failed_checks 60 "$RUN_JSON_MIXED"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
expect_no_token "$OTHER_RUN_ID" "another PR's run id $OTHER_RUN_ID"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# One PR can be queued more than once, on different base tips: the most recent
# run is the one for the entry that dropped.
OLD_RUN_ID="111222333"
OLD_RUN_URL="https://github.com/prime-radiant-inc/evener/actions/runs/$OLD_RUN_ID"
OLD_TIP_SHA="9999999999999999999999999999999999999999"
RUN_JSON_TWO="[{\"databaseId\":$RUN_ID,\"url\":\"$RUN_URL\",\"conclusion\":\"failure\",\"headBranch\":\"$QUEUE_BRANCH\"},{\"databaseId\":$OLD_RUN_ID,\"url\":\"$OLD_RUN_URL\",\"conclusion\":\"success\",\"headBranch\":\"$QUEUE_PREFIX$OLD_TIP_SHA\"}]"
run_scenario "two runs for this PR: names the most recent" failed_checks 60 "$RUN_JSON_TWO"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
expect_no_token "$OLD_RUN_ID" "older run id $OLD_RUN_ID"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# The PR is not queued yet: the script enqueues it and reports the position.
# No drop happens, so no run is looked up.
current_name="enqueue path reports the queued position"
scenario_failures=0
: > "$GH_LOG"; : > "$GH_STATE"
if out=$(PATH="$STUBDIR:$PATH" GH_LOG="$GH_LOG" GH_STATE="$GH_STATE" \
    STUB_FIRST_POS="-" STUB_NEVER_DROP=1 \
    bash "$SCRIPT" --pr 6499 --wait 0 --repo prime-radiant-inc/evener 2>&1); then
  code=0
else
  code=$?
fi
printf '%s' "$out" > "$LAST_OUT"
printf '%s' "$code" > "$LAST_CODE"
[ "$code" = "0" ] || fail "exit code is $code, want 0"
grep -Fq "MERGEQ queued position=1" "$LAST_OUT" || fail "no MERGEQ queued position=1 line"
grep -Fq "enqueuePullRequest" "$GH_LOG" || fail "no enqueue call"
grep -Fq "run list" "$GH_LOG" && fail "unexpected gh run list call"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# The PR stays queued until the wait runs out: a timeout, not a drop, so no
# run is looked up.
current_name="timeout path reports timeout without a run lookup"
scenario_failures=0
: > "$GH_LOG"; : > "$GH_STATE"
if out=$(PATH="$STUBDIR:$PATH" GH_LOG="$GH_LOG" GH_STATE="$GH_STATE" \
    STUB_NEVER_DROP=1 \
    bash "$SCRIPT" --pr 6499 --wait 1 --repo prime-radiant-inc/evener 2>&1); then
  code=0
else
  code=$?
fi
printf '%s' "$out" > "$LAST_OUT"
printf '%s' "$code" > "$LAST_CODE"
[ "$code" = "1" ] || fail "exit code is $code, want 1"
grep -Fq "MERGEQ timeout position=1 state=QUEUED" "$LAST_OUT" || fail "no MERGEQ timeout line"
grep -Fq "run list" "$GH_LOG" && fail "unexpected gh run list call"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

if [ "$failures" -eq 0 ]; then
  printf 'All merge-enqueue simulation scenarios passed.\n'
  exit 0
fi
printf '%s scenario(s) failed.\n' "$failures" >&2
exit 1
