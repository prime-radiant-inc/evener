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

# status prints "STATE MERGE_SHA POSITION QUEUE_STATE" from one query, so the
# PR's state and its queue entry are read at the same instant ("-" for absent
# values). It exits nonzero when the query fails, and so does the script.
status() {
  local out
  if ! out=$(gh api graphql -f query='query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(number:$p){id state mergeCommit{oid} mergeQueueEntry{position state}}}}' \
    -f o="${repo%%/*}" -f n="${repo##*/}" -F p="$pr" \
    --jq '.data.repository.pullRequest | "\(.id) \(.state) \(.mergeCommit.oid // "-") \(.mergeQueueEntry.position // "-") \(.mergeQueueEntry.state // "-")"' 2>&1); then
    echo "MERGEQ lookup failed: $out" >&2
    exit 2
  fi
  echo "$out"
}

# refresh sets id, st, sha, pos and qstate from status, stopping the script if
# the lookup failed (a failure inside "$(...)" alone would not).
refresh() {
  local s
  s=$(status) || exit 2
  read -r id st sha pos qstate <<<"$s"
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

# drop_outcome prints the one outcome line for a queue drop, keyed on the
# queue's own removal reason so a green entry dropped by a batch rebuild is not
# read as a failed PR. A removal event older than the drop it is meant to
# explain is a stale prior event, not this drop's reason.
drop_outcome() {
  local event created reason
  if ! event=$(queue_drop_reason 2>/dev/null); then
    printf 'MERGEQ dropped: PR left the queue without merging (state=%s); could not read the queue removal reason; inspect the merge-group run before re-enqueueing\n' "$st"
    return
  fi
  created=${event%% *}
  reason=${event#* }
  case "$reason" in
  failed_checks)
    reason="a required check failed in the merge group; read the merge-group failure comment on the PR and re-enqueue when the failed lanes are unrelated to your change or look transient"
    ;;
  checks_timed_out)
    reason="the queue timed out waiting for checks; confirm this entry's merge-group run completed green, then re-enqueue the same head"
    ;;
  "")
    reason="the queue removal reason was unavailable; inspect the merge-group run before re-enqueueing"
    ;;
  *)
    reason="queue removal reason: $reason — inspect the merge-group run before re-enqueueing"
    ;;
  esac
  if [ -n "$event" ] && [ "$created" -lt $(( $(date +%s) - 600 )) ]; then
    reason="the latest queue removal event predates this drop; inspect the merge-group run before re-enqueueing"
  fi
  printf 'MERGEQ dropped: PR left the queue without merging (state=%s); %s\n' "$st" "$reason"
}

refresh
if [ "$st" = "MERGED" ]; then echo "MERGEQ merged sha=$sha"; exit 0; fi
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
  refresh
  if [ "$st" = "MERGED" ]; then echo "MERGEQ merged sha=$sha"; exit 0; fi
  if [ "$pos" = "-" ]; then
    drop_outcome
    exit 1
  fi
done
echo "MERGEQ timeout position=$pos state=$qstate"
exit 1
