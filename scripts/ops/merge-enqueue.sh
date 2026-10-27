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
#   scripts/ops/merge-enqueue.sh --pr N [--wait SECONDS] [--repo OWNER/NAME]
#
# --wait polls every 30s until the PR merges, leaves the queue, or SECONDS
# pass. --repo defaults to prime-radiant-inc/evener.
#
# Output (last line is the outcome):
#   MERGEQ queued position=P            (no --wait)
#   MERGEQ merged sha=SHA               (exit 0)
#   MERGEQ dropped: <reason>            (exit 1: left the queue unmerged; look
#                                         at the PR's checks, fix, enqueue again)
#   MERGEQ timeout position=P state=S   (exit 1: still queued after --wait)
# Exit 2: usage error or the enqueue call failed (message says why).
set -euo pipefail

usage() { sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

repo="prime-radiant-inc/evener" pr="" wait=0
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo="$2"; shift 2 ;;
    --pr) pr="$2"; shift 2 ;;
    --wait) wait="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done
[ -n "$repo" ] && [ -n "$pr" ] || usage

state_of() {
  gh pr view "$pr" --repo "$repo" --json state,mergeCommit --jq '.state+" "+(.mergeCommit.oid // "")'
}

entry_of() {
  gh api graphql -f query='query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(number:$p){mergeQueueEntry{position state}}}}' \
    -f o="${repo%%/*}" -f n="${repo##*/}" -F p="$pr" \
    --jq '.data.repository.pullRequest.mergeQueueEntry | if . == null then "none" else "\(.position) \(.state)" end'
}

read -r st sha < <(state_of)
if [ "$st" = "MERGED" ]; then echo "MERGEQ merged sha=$sha"; exit 0; fi

if [ "$(entry_of)" = "none" ]; then
  id=$(gh pr view "$pr" --repo "$repo" --json id --jq .id)
  if ! out=$(gh api graphql -f query='mutation($id:ID!){enqueuePullRequest(input:{pullRequestId:$id}){mergeQueueEntry{position}}}' -f id="$id" 2>&1); then
    echo "MERGEQ enqueue failed: $out" >&2; exit 2
  fi
fi
read -r pos qstate < <(entry_of) || true
echo "MERGEQ queued position=$pos"
[ "$wait" -gt 0 ] || exit 0

deadline=$(( $(date +%s) + wait ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  sleep 30
  read -r st sha < <(state_of)
  if [ "$st" = "MERGED" ]; then echo "MERGEQ merged sha=$sha"; exit 0; fi
  entry=$(entry_of)
  if [ "$entry" = "none" ]; then
    echo "MERGEQ dropped: PR left the queue without merging (state=$st); see gh pr checks $pr --repo $repo"
    exit 1
  fi
  read -r pos qstate <<<"$entry"
done
echo "MERGEQ timeout position=$pos state=$qstate"
exit 1
