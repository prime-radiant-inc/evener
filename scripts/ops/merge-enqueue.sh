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
#   MERGEQ dropped: <reason>            (exit 1: left the queue unmerged; look
#                                         at the PR's checks, fix, enqueue again)
#   MERGEQ timeout position=P state=S   (exit 1: still queued after --wait)
# Exit 2: usage error or the enqueue call failed (message says why).
set -euo pipefail

usage() { sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

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
while [ "$(date +%s)" -lt "$deadline" ]; do
  sleep 30
  refresh
  if [ "$st" = "MERGED" ]; then echo "MERGEQ merged sha=$sha"; exit 0; fi
  if [ "$pos" = "-" ]; then
    echo "MERGEQ dropped: PR left the queue without merging (state=$st); see gh pr checks $pr --repo $repo"
    exit 1
  fi
done
echo "MERGEQ timeout position=$pos state=$qstate"
exit 1
