#!/usr/bin/env bash
# merge-enqueue.sh: add a PR to its base branch's GitHub merge queue, and
# optionally wait until it merges or the queue drops it.
#
# Use it to merge a PR into main once every check is green and RoboRev's app
# has approved (AGENTS.md, "Merge through the merge queue"). The queue runs the
# required checks on top of main and the PRs ahead of it and merges when they
# pass there. `gh pr merge --squash` falls back to auto-merge, which this repo
# disallows, so this calls the enqueuePullRequest mutation directly. Running it
# again on a queued PR does not enqueue it twice.
#
#   scripts/ops/merge-enqueue.sh --pr N [--wait SECONDS] [--jump] [--repo OWNER/NAME]
#
# --wait polls every 30s until the PR merges, leaves the queue, or SECONDS
# pass. --jump puts the PR at the front of the queue as it is enqueued (admins
# only; for a fix the queue itself is waiting on); it refuses an already
# queued PR. --repo defaults to prime-radiant-inc/evener.
#
# Output (last line is the outcome):
#   MERGEQ queued position=P            (no --wait)
#   MERGEQ merged sha=SHA               (exit 0)
#   MERGEQ dropped: <reason>            (exit 1: left the queue unmerged; the
#                                         reason is the queue's own removal
#                                         reason, so a green drop is distinct)
#   MERGEQ timeout position=P state=S   (exit 1: still queued after --wait)
# Exit 2: usage error or the enqueue call failed (message says why).
set -euo pipefail

usage() { sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

repo="prime-radiant-inc/evener" pr="" wait=0 jump=false
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo="$2"; shift 2 ;;
    --pr) pr="$2"; shift 2 ;;
    --wait) wait="$2"; shift 2 ;;
    --jump) jump=true; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done
[ -n "$repo" ] && [ -n "$pr" ] || usage
case "$wait" in ''|*[!0-9]*) echo "--wait takes whole seconds, got: $wait" >&2; usage ;; esac
wait=$((10#$wait))  # decimal: bash arithmetic reads a leading 0 as octal

# status prints "ID STATE MERGE_SHA POSITION QUEUE_STATE BASE" from one query,
# so the PR's state, its queue entry and its base branch are read at the same
# instant ("-" for absent values). It exits nonzero when the query fails, and
# so does the script. BASE names the merge-group run's queue branch:
# gh-readonly-queue/<BASE>/pr-<N>-<base tip>.
status() {
  local out
  if ! out=$(gh api graphql -f query='query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(number:$p){id state baseRefName mergeCommit{oid} mergeQueueEntry{position state}}}}' \
    -f o="${repo%%/*}" -f n="${repo##*/}" -F p="$pr" \
    --jq '.data.repository.pullRequest | "\(.id) \(.state) \(.mergeCommit.oid // "-") \(.mergeQueueEntry.position // "-") \(.mergeQueueEntry.state // "-") \(.baseRefName // "-")"' 2>&1); then
    echo "MERGEQ lookup failed: $out" >&2
    exit 2
  fi
  echo "$out"
}

# refresh sets id, st, sha, pos, qstate and base from status, stopping the
# script if the lookup failed (a failure inside "$(...)" alone would not).
refresh() {
  local s
  s=$(status) || exit 2
  read -r id st sha pos qstate base <<<"$s"
}

# merged_sha prints the merge commit and succeeds when the PR's own state says
# MERGED. The queue snapshot can lag a merge by an interval or more, so a
# timeout verdict read from it alone reports a PR that already merged; the PR's
# own state is what decides whether the wait is over. A failed lookup reports
# not-merged, and the queue read that follows surfaces a real failure.
merged_sha() {
  local out
  out=$(gh pr view "$pr" --repo "$repo" --json state,mergeCommit \
    --jq 'select(.state == "MERGED") | .mergeCommit.oid // "-"' 2>/dev/null) || return 1
  [ -n "$out" ] || return 1
  printf '%s\n' "$out"
}

# exit_if_merged ends the run with the merged outcome when the PR has merged.
exit_if_merged() {
  local merged
  if merged=$(merged_sha); then echo "MERGEQ merged sha=$merged"; exit 0; fi
}

# queue_drop_reason prints "<createdAt-epoch> <reason>" for the latest removal
# event, or an empty line when the timeline has none yet, and fails when the
# lookup itself fails so that is not read as "no reason". The queue reports why
# it removed the entry, which distinguishes a failed check from a timeout even
# though merge-group checks never report on the member PR.
queue_drop_reason() {
  gh api graphql -f query='query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(number:$p){timelineItems(last:1,itemTypes:[REMOVED_FROM_MERGE_QUEUE_EVENT]){nodes{... on RemovedFromMergeQueueEvent{createdAt reason}}}}}}' \
    -f o="${repo%%/*}" -f n="${repo##*/}" -F p="$pr" \
    --jq '.data.repository.pullRequest.timelineItems.nodes[0] | if . == null then "" else "\(.createdAt | fromdateiso8601) \(.reason // "")" end'
}

# A merge-group run's headBranch is gh-readonly-queue/<base>/pr-<N>-<base tip>:
# GitHub puts the base tip the queue tested on top of, not the PR head, so the
# exact branch cannot be built before the run exists and the suffix is matched
# on its prefix. The prefix is passed through the environment rather than
# interpolated into the jq program, whose literals a branch name can break.
#
# merge_group_ref names how to reach this PR's merge-group run: its run id and
# URL when the most recent match resolves, otherwise the queue branch prefix so
# the operator can list it. It never fails the script: a run lookup that errors
# or finds nothing falls back to the prefix, and a base the status read did not
# supply falls back to the generic run.
merge_group_ref() {
  local prefix out
  if [ -z "${base:-}" ] || [ "$base" = "-" ] || [ "$base" = "null" ]; then
    printf 'the merge-group run for this PR'
    return
  fi
  prefix="gh-readonly-queue/${base}/pr-${pr}-"
  out=$(QUEUE_PREFIX="$prefix" gh run list --repo "$repo" --event merge_group --limit 100 \
    --json databaseId,url,conclusion,headBranch \
    --jq '[.[] | select(.headBranch | startswith(env.QUEUE_PREFIX))][0] | if . == null then "" else "merge-group run \(.databaseId) (\(.conclusion // "unknown")): \(.url)" end' 2>/dev/null) || out=""
  if [ -n "$out" ]; then printf '%s' "$out"; else printf 'the merge-group run for queue branch prefix %s' "$prefix"; fi
}

# drop_outcome prints the one outcome line for a queue drop, keyed on the
# queue's own removal reason so a green entry dropped by a batch rebuild is not
# read as a failed PR. A removal event older than the drop it is meant to
# explain is a stale prior event, not this drop's reason. Every branch names
# this PR's merge-group run, so the lane can go straight to it instead of
# hunting through the Actions API.
drop_outcome() {
  local event created reason run
  run=$(merge_group_ref)
  if ! event=$(queue_drop_reason 2>/dev/null); then
    printf 'MERGEQ dropped: PR left the queue without merging (state=%s); could not read the queue removal reason; see %s before re-enqueueing\n' "$st" "$run"
    return
  fi
  created=${event%% *}
  reason=${event#* }
  case "$reason" in
  failed_checks)
    reason="a required check failed in the merge group; see $run and re-enqueue when the failed lanes are unrelated to your change or look transient"
    ;;
  checks_timed_out)
    reason="the queue timed out waiting for checks; confirm $run completed green, then re-enqueue the same head"
    ;;
  "")
    reason="the queue removal reason was unavailable; see $run before re-enqueueing"
    ;;
  *)
    reason="queue removal reason: $reason — see $run before re-enqueueing"
    ;;
  esac
  if [ -n "$event" ] && [ "$created" -lt $(( $(date +%s) - 600 )) ]; then
    reason="the latest queue removal event predates this drop; see $run before re-enqueueing"
  fi
  printf 'MERGEQ dropped: PR left the queue without merging (state=%s); %s\n' "$st" "$reason"
}

refresh
if [ "$st" = "MERGED" ]; then echo "MERGEQ merged sha=$sha"; exit 0; fi
exit_if_merged
if [ "$pos" = "-" ]; then
  if ! out=$(gh api graphql -f query='mutation($id:ID!,$jump:Boolean!){enqueuePullRequest(input:{pullRequestId:$id,jump:$jump}){mergeQueueEntry{position}}}' \
    -f id="$id" -F jump="$jump" 2>&1); then
    echo "MERGEQ enqueue failed: $out" >&2; exit 2
  fi
  refresh
  if [ "$st" = "MERGED" ]; then echo "MERGEQ merged sha=$sha"; exit 0; fi
  if [ "$pos" = "-" ]; then echo "MERGEQ enqueue failed: no queue entry after enqueueing (state=$st)" >&2; exit 2; fi
elif [ "$jump" = true ]; then
  # jump rides on the enqueue call, which a queued PR does not get.
  echo "MERGEQ jump refused: PR is already queued at position $pos; remove it from the queue first" >&2
  exit 2
fi
echo "MERGEQ queued position=$pos"
[ "$wait" -gt 0 ] || exit 0

deadline=$(( $(date +%s) + wait ))
while :; do
  left=$(( deadline - $(date +%s) ))
  [ "$left" -gt 0 ] || break
  sleep $(( left < 30 ? left : 30 ))
  exit_if_merged
  refresh
  if [ "$st" = "MERGED" ]; then echo "MERGEQ merged sha=$sha"; exit 0; fi
  if [ "$pos" = "-" ]; then
    drop_outcome
    exit 1
  fi
done
# The loop's last read lands on the deadline, and the queue can finish in the
# moment after it. A timeout verdict printed from that read reports a merge that
# already happened, so observe once more before calling it.
exit_if_merged
refresh
if [ "$st" = "MERGED" ]; then echo "MERGEQ merged sha=$sha"; exit 0; fi
if [ "$pos" = "-" ]; then
  drop_outcome
  exit 1
fi
echo "MERGEQ timeout position=$pos state=$qstate"
exit 1
