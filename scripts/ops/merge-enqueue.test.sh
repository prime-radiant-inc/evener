#!/usr/bin/env bash
# merge-enqueue.test.sh: simulate merge-queue drops for merge-enqueue.sh with
# a stubbed `gh`, no network and no real repository state.
#
#   bash scripts/ops/merge-enqueue.test.sh
#
# Each drop scenario runs the real script (`--pr 6499 --wait 1`) against a stub
# that answers the GraphQL status / removal-event calls and `gh run list`
# from canned data, then checks the run identity the drop outcome names:
# the run id and URL when a run resolves for the queue branch, the queue
# branch name itself when none does. Two further scenarios pin the
# non-drop outcomes (enqueue with `--wait 0`, timeout with `--wait 1`).
# Assertions are on those data tokens,
# the `MERGEQ dropped:` outcome marker, the exit code, and the `gh run list`
# call itself — never on the surrounding prose.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/merge-enqueue.sh"
HEAD_SHA="abcdef0123456789abcdef0123456789abcdef01"
QUEUE_BRANCH="gh-readonly-queue/main/pr-6499-$HEAD_SHA"
RUN_ID="123456789"
RUN_URL="https://github.com/prime-radiant-inc/evener/actions/runs/$RUN_ID"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT
STUBDIR="$TMPDIR/bin"
mkdir -p "$STUBDIR"
GH_LOG="$TMPDIR/gh-calls.log"
GH_STATE="$TMPDIR/gh-state"
LAST_OUT="$TMPDIR/last-out.txt"
LAST_CODE="$TMPDIR/last-code.txt"

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
    # 7 fields, as status() prints: id state sha pos qstate head base.
    # The dropped read may report a different head (a push between the drop
    # and its detection); STUB_HEAD_SHA2 controls it, defaulting to the
    # queued head. STUB_BASE / STUB_BASE2 do the same for the base.
    n=0; [ -f "$GH_STATE" ] && n=$(cat "$GH_STATE")
    n=$(( n + 1 )); printf '%s' "$n" > "$GH_STATE"
    if [ "$n" -eq 1 ]; then
      echo "PR_6499 OPEN - ${STUB_FIRST_POS:-1} QUEUED $STUB_HEAD_SHA ${STUB_BASE:-main}"
    elif [ "${STUB_NEVER_DROP:-0}" = "1" ]; then
      echo "PR_6499 OPEN - 1 QUEUED ${STUB_HEAD_SHA2:-$STUB_HEAD_SHA} ${STUB_BASE2:-main}"
    else
      echo "PR_6499 OPEN - - - ${STUB_HEAD_SHA2:-$STUB_HEAD_SHA} ${STUB_BASE2:-main}"
    fi
    ;;
  *pr\ view*)
    # Not merged: merged_sha prints nothing and fails.
    exit 1
    ;;
  *run\ list*)
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

# run_scenario NAME REASON EVENT_AGE RUN_JSON [RUN_FAIL] [FAIL_REASON] [HEAD_OVERRIDE] [EXPECT_RUN_LIST] [HEAD2_OVERRIDE] [BASE_OVERRIDE] [BASE2_OVERRIDE]
run_scenario() {
  current_name="$1"
  local out code
  scenario_failures=0
  : > "$GH_LOG"; : > "$GH_STATE"
  if out=$(PATH="$STUBDIR:$PATH" GH_LOG="$GH_LOG" GH_STATE="$GH_STATE" \
      STUB_HEAD_SHA="${7:-$HEAD_SHA}" STUB_HEAD_SHA2="${9:-$HEAD_SHA}" STUB_REASON="$2" STUB_EVENT_AGE="$3" \
      STUB_RUN_JSON="$4" STUB_RUN_FAIL="${5:-0}" STUB_FAIL_REASON="${6:-0}" \
      STUB_BASE="${10:-main}" STUB_BASE2="${11:-main}" \
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
  # The script looked the merge-group run up for this head's queue branch,
  # unless the status read supplied no head to look it up by.
  if [ "${8:-1}" = "1" ]; then
    grep -Fq "run list --repo prime-radiant-inc/evener --branch $QUEUE_BRANCH" "$GH_LOG" \
      || fail "no gh run list call for $QUEUE_BRANCH"
  fi
}

expect_token() { grep -Fq "$1" "$LAST_OUT" || fail "output lacks $2"; }

RUN_JSON="[{\"databaseId\":$RUN_ID,\"url\":\"$RUN_URL\",\"conclusion\":\"failure\"}]"

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

run_scenario "no run resolves: names the queue branch" failed_checks 60 "[]"
expect_token "$QUEUE_BRANCH" "queue branch $QUEUE_BRANCH"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "run lookup fails: names the queue branch" failed_checks 60 "$RUN_JSON" 1
expect_token "$QUEUE_BRANCH" "queue branch $QUEUE_BRANCH"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "run without conclusion still names id and URL" failed_checks 60 "[{\"databaseId\":$RUN_ID,\"url\":\"$RUN_URL\"}]"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "no head from status: falls back without crashing" failed_checks 60 "$RUN_JSON" 0 0 "-" 0 "-"
expect_token "the merge-group run for this PR" "generic run reference"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

run_scenario "null head from status: falls back without crashing" failed_checks 60 "$RUN_JSON" 0 0 "null" 0 "null"
expect_token "the merge-group run for this PR" "generic run reference"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# The queued read supplied no head but the detecting read does: the snapshot
# must not prefer the placeholder over the real head.
run_scenario "placeholder queued head, real detecting head: uses the real head" failed_checks 60 "$RUN_JSON" 0 0 "-" 1 "$HEAD_SHA"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# The head changed between the drop and its detection (a push in the poll
# window): the run lookup must use the queued head, not the new one.
NEW_HEAD="1111111111111111111111111111111111111111"
run_scenario "head changed after drop: names the queued head's run" failed_checks 60 "$RUN_JSON" 0 0 "$HEAD_SHA" 1 "$NEW_HEAD"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
grep -Fq "pr-6499-$NEW_HEAD" "$GH_LOG" && fail "run list used the post-drop head $NEW_HEAD"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# The queued read supplied a placeholder base but a real head, and the head
# changed before detection: the snapshot must keep the real queued head even
# though the queued base was not snapshotted, and use the real detecting base.
run_scenario "placeholder queued base, real detecting base: keeps the queued head" failed_checks 60 "$RUN_JSON" 0 0 "$HEAD_SHA" 1 "$NEW_HEAD" "-" "main"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
grep -Fq "pr-6499-$NEW_HEAD" "$GH_LOG" && fail "run list used the post-drop head $NEW_HEAD"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# The queued base was real and the detecting read reports a placeholder: the
# snapshotted base survives, so the lookup still names the queued head's run.
run_scenario "real queued base survives a placeholder detecting base" failed_checks 60 "$RUN_JSON" 0 0 "$HEAD_SHA" 1 "$NEW_HEAD" "main" "-"
expect_token "$RUN_ID" "run id $RUN_ID"
expect_token "$RUN_URL" "run URL $RUN_URL"
grep -Fq "pr-6499-$NEW_HEAD" "$GH_LOG" && fail "run list used the post-drop head $NEW_HEAD"
[ "$scenario_failures" -eq 0 ] && printf 'PASS: %s\n' "$current_name" || failures=$(( failures + 1 ))

# The PR is not queued yet: the script enqueues it and reports the position.
# No drop happens, so no run is looked up.
current_name="enqueue path reports the queued position"
scenario_failures=0
: > "$GH_LOG"; : > "$GH_STATE"
if out=$(PATH="$STUBDIR:$PATH" GH_LOG="$GH_LOG" GH_STATE="$GH_STATE" \
    STUB_HEAD_SHA="$HEAD_SHA" STUB_HEAD_SHA2="$HEAD_SHA" STUB_FIRST_POS="-" STUB_NEVER_DROP=1 \
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
    STUB_HEAD_SHA="$HEAD_SHA" STUB_HEAD_SHA2="$HEAD_SHA" STUB_NEVER_DROP=1 \
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
