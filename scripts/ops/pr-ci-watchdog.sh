#!/usr/bin/env bash
# pr-ci-watchdog.sh: after a push to a PR branch, verify the expected
# pull_request-triggered CI run actually appeared on the pushed head, and
# dispatch the workflow when GitHub silently dropped the event.
#
# Use it right after pushing to a PR whose CI you are about to wait on.
# GitHub has dropped pull_request events outright for some PRs (every
# synchronize event for the native journey PR, #5615, has gone missing
# since 2026-11-06, while other PRs kept theirs), so a push can end with no
# CI run at all and nothing on the PR page says one was ever expected. The
# watchdog resolves the PR's head, polls for a run of the expected workflow
# on that head, and dispatches the workflow itself when nothing appeared.
# Any run already on the head, pull_request (GitHub delivered) or
# workflow_dispatch (someone covered it manually), counts as covered, so
# the watchdog never double-dispatches. A cancelled run does not count, so a
# cancelled dispatch is re-dispatched rather than trusted. Run one
# invocation per push: two watchdogs racing each other (or a manual dispatch)
# land in the workflow's own dispatch concurrency group, and the newer run
# cancels the in-flight older one; the watchdog then reports whatever run
# actually covers the head.
#
# The default workflow, ios-qualification.yml, is the native lane's macOS
# simulator qualification: its workflow_dispatch trigger takes no inputs, it
# builds and qualifies without publishing anything, and a dispatch run
# shares no concurrency group with a pull_request run, so a watchdog
# dispatch cannot cancel one. It fires on pull_request only when the push
# touches its trigger paths (mobile-native/, appwire-client/typescript/,
# scripts/native/ and friends): run the watchdog after a push you expect to
# trigger it, since on other pushes no run is expected and the watchdog
# would dispatch one needlessly. Pass --workflow (repeatable) to check
# different workflows instead of the default; only workflows with a
# workflow_dispatch trigger can be re-dispatched (ci.yml has none: a missing
# ci.yml run is reported with reason=dispatch-refused, not replaced, though
# every run found on the head is listed, so it stays visible).
#
#   scripts/ops/pr-ci-watchdog.sh --pr N [--workflow FILE]... [--wait SECONDS]
#                                 [--sha SHA] [--dry-run] [--repo OWNER/NAME]
#
# --pr is the pull request just pushed. --workflow repeats; each is a file
# under .github/workflows/, and together they replace the default list
# (ios-qualification.yml). --wait is how long to poll for the run before
# concluding the event was dropped (default 300 seconds, checked every 20;
# --wait 0 checks once). --sha checks a specific head commit instead of the
# PR's current head, once, without polling, and never dispatches: a
# dispatch builds the branch head, so it would not cover the checked
# commit (with --dry-run it previews the dispatch a live check would do).
# --dry-run does everything except dispatch: it prints the gh workflow run
# command it would run and exits with the missing verdict, so the
# dropped-event path can be demonstrated without a real push. --repo
# defaults to prime-radiant-inc/evener.
#
# Output: earlier lines name the PR, the head being checked, every run
# found on it, and what was done; then one verdict line per expected
# workflow, which for a single-workflow invocation is the last line:
#   CIWATCH covered workflow=F by=pull_request run=ID url=U
#                                (exit 0: GitHub delivered the event)
#   CIWATCH covered workflow=F by=workflow_dispatch run=ID url=U
#                                (exit 0: a dispatch run covers the head)
#   CIWATCH dispatched workflow=F run=ID url=U
#                                (exit 0: the event was dropped; we
#                                dispatched and confirmed the run exists)
#   CIWATCH missing workflow=F head=SHA reason=R
#                                (exit 1: no run covers the head; R is one
#                                of --sha-check-only, dry-run, head-moved,
#                                pr-closed, dispatch-refused, or
#                                dispatch-unconfirmed, where a dispatch was
#                                accepted but no run ever registered, so
#                                look before dispatching anything yourself)
# Exit 2: usage error, the PR could not be resolved, or a run lookup
# failed (the message says which).
set -euo pipefail

usage() { sed -n '2,70p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

repo="prime-radiant-inc/evener" pr="" wait=300 sha="" dry_run=false
workflows=()
while [ $# -gt 0 ]; do
  case "$1" in
    --repo|--pr|--workflow|--wait|--sha)
      [ $# -ge 2 ] || { echo "missing value for $1" >&2; usage; } ;;
  esac
  case "$1" in
    --repo) repo="$2"; shift 2 ;;
    --pr) pr="$2"; shift 2 ;;
    --workflow) workflows+=("$2"); shift 2 ;;
    --wait) wait="$2"; shift 2 ;;
    --sha) sha="$2"; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done
[ -n "$pr" ] || usage
case "$pr" in ''|*[!0-9]*) echo "--pr takes a pull request number, got: $pr" >&2; usage ;; esac
pr=$((10#$pr))       # decimal: bash arithmetic reads a leading 0 as octal
case "$wait" in ''|*[!0-9]*) echo "--wait takes whole seconds, got: $wait" >&2; usage ;; esac
wait=$((10#$wait))   # decimal, same reason
if [ -n "$sha" ] && ! [[ "$sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "--sha takes one full lowercase 40-character commit SHA, got: $sha" >&2
  usage
fi
if [ -n "$sha" ]; then
  wait=0  # a supplied head is history to GitHub; waiting cannot add a run
fi
if [ "${#workflows[@]}" -eq 0 ]; then workflows=("ios-qualification.yml"); fi
for w in "${workflows[@]}"; do
  case "$w" in '') echo "--workflow takes a workflow file name" >&2; usage ;; esac
done
workflows=("${workflows[@]##*/}")
cov_event=() cov_status=() cov_id=() cov_url=()  # per-workflow covering run, set by classify_runs

# resolve_pr prints the PR's state, head branch and head SHA in one query,
# so every decision is about the head GitHub has right now. It stops the
# script when the lookup fails (a failure inside "$(...)" alone would not).
resolve_pr() {
  local out
  if ! out=$(gh pr view "$pr" --repo "$repo" --json state,headRefName,headRefOid \
    --jq '[.state, .headRefName, .headRefOid] | @tsv' 2>&1); then
    echo "CIWATCH resolve failed: gh pr view $pr --repo $repo said: $out" >&2
    exit 2
  fi
  printf '%s\n' "$out"
}

pr_line="$(resolve_pr)" || exit 2
IFS=$'\t' read -r state head_branch head_oid <<<"$pr_line"
if [ "$state" != "OPEN" ]; then
  echo "CIWATCH refused: PR $pr is $state, not OPEN; a $state PR gets no pull_request events and dispatching would build a stale head" >&2
  exit 2
fi
check_oid="${sha:-$head_oid}"

# fetch_runs prints one tab-separated line per run on the head, oldest
# first: path, event, status, id, url. It stops the script on a failed
# lookup (a failure inside "$(...)" alone would not).
fetch_runs() {
  local out
  if ! out=$(gh api "repos/$repo/actions/runs?head_sha=$check_oid&per_page=100" \
    --jq '.workflow_runs | sort_by(.created_at) | .[] | [.path, .event, (.conclusion // .status), (.id|tostring), .html_url] | @tsv' 2>&1); then
    echo "CIWATCH lookup failed: gh api actions/runs?head_sha=$check_oid said: $out" >&2
    exit 2
  fi
  [ -n "$out" ] || return 0
  printf '%s\n' "$out"
}

# classify_runs reads runs_raw once and records the run covering each
# expected workflow in cov_event/cov_status/cov_id/cov_url, keyed like
# workflows: a pull_request run beats a dispatch, the newest within each
# kind wins, and a cancelled run covers nothing. merge-enqueue's refresh()
# sets its state the same way, so a verdict never mixes two observations.
classify_runs() {
  local i path event status id url base
  for i in "${!workflows[@]}"; do
    cov_event[i]="" cov_status[i]="" cov_id[i]="" cov_url[i]=""
  done
  while IFS=$'\t' read -r path event status id url; do
    [ -n "${path:-}" ] || continue
    [ "$status" != "cancelled" ] || continue  # a cancelled run covers nothing
    base="${path##*/}"
    for i in "${!workflows[@]}"; do
      [ "$base" = "${workflows[i]}" ] || continue
      if [ "$event" = "pull_request" ] || [ "${cov_event[i]:-}" != "pull_request" ]; then
        cov_event[i]="$event" cov_status[i]="$status" cov_id[i]="$id" cov_url[i]="$url"
      fi
    done
  done <<<"$runs_raw"
}

# missing_workflows prints the expected workflows with no run on the head,
# one per line; nothing at all when every expected workflow is covered.
missing_workflows() {
  local i out=""
  for i in "${!workflows[@]}"; do
    [ -n "${cov_event[i]:-}" ] || out="$out${workflows[i]}"$'\n'
  done
  printf '%s' "$out"
}

echo "PR #$pr on $repo: branch $head_branch, head $head_oid"
if [ "$check_oid" != "$head_oid" ]; then
  echo "checking --sha $check_oid; a --sha check never dispatches"
fi
echo "expecting a run of: ${workflows[*]}"

runs_raw="$(fetch_runs)" || exit 2
classify_runs
missing="$(missing_workflows)"
if [ -n "$missing" ] && [ "$wait" -gt 0 ]; then
  echo "missing after first check: $(printf '%s' "$missing" | tr '\n' ' ')polling for up to ${wait}s"
fi
deadline=$(( $(date +%s) + wait ))
while [ -n "$missing" ]; do
  now=$(date +%s)
  if [ "$now" -ge "$deadline" ]; then break; fi
  left=$(( deadline - now ))
  sleep $(( left < 20 ? left : 20 ))
  runs_raw="$(fetch_runs)" || exit 2
  classify_runs
  missing="$(missing_workflows)"
done

if [ -n "$runs_raw" ]; then
  echo "runs on head $check_oid:"
  while IFS=$'\t' read -r path event status id url; do
    [ -n "${path:-}" ] || continue
    echo "  $event  ${path##*/}  $status  $url"
  done <<<"$runs_raw"
else
  echo "no runs at all on head $check_oid"
fi

rc=0
rechecked=false
for i in "${!workflows[@]}"; do
  w="${workflows[i]}"
  if [ -n "${cov_event[i]:-}" ]; then
    event="${cov_event[i]}" status="${cov_status[i]}" id="${cov_id[i]}" url="${cov_url[i]}"
    if [ "$event" = "pull_request" ]; then
      note="GitHub delivered the event"
    else
      note="the pull_request event is missing, but this run covers the head"
    fi
    echo "$w: covered by a $event run (status $status); $note"
    echo "CIWATCH covered workflow=$w by=$event run=$id url=$url"
  elif [ "$dry_run" = true ]; then
    echo "$w: no run on $check_oid; GitHub dropped the event; dry run, would dispatch on $head_branch:"
    echo "  gh workflow run $w --repo $repo --ref $head_branch"
    echo "CIWATCH missing workflow=$w head=$check_oid reason=dry-run"
    rc=1
  elif [ -n "$sha" ]; then
    echo "$w: no run on $check_oid; --sha is check-only, nothing was dispatched"
    echo "CIWATCH missing workflow=$w head=$check_oid reason=--sha-check-only"
    rc=1
  else
    if [ "$rechecked" != true ]; then
      pr_line="$(resolve_pr)" || exit 2
      IFS=$'\t' read -r cur_state _ cur_oid <<<"$pr_line"
      rechecked=true
    fi
    if [ "$cur_state" != "OPEN" ]; then
      echo "$w: no run on $check_oid, and the PR is now $cur_state; not dispatching for a closed PR"
      echo "CIWATCH missing workflow=$w head=$check_oid reason=pr-closed"
      rc=1
      continue
    fi
    if [ "$cur_oid" != "$head_oid" ]; then
      echo "$w: no run on $check_oid, and the PR head moved to $cur_oid during the wait; not dispatching for a stale head; re-run for the new push"
      echo "CIWATCH missing workflow=$w head=$check_oid reason=head-moved"
      rc=1
      continue
    fi
    echo "$w: no run on $check_oid after ${wait}s; GitHub dropped the event; dispatching $w on $head_branch"
    if ! gh workflow run "$w" --repo "$repo" --ref "$head_branch" 2>&1; then
      echo "$w: dispatch refused, nothing was created; a workflow without a workflow_dispatch trigger, or a ref missing the workflow file, reports this" >&2
      echo "CIWATCH missing workflow=$w head=$check_oid reason=dispatch-refused"
      rc=1
      continue
    fi
    # gh workflow run returns before GitHub registers the run; confirm the
    # run exists so the verdict names one the operator can watch.
    deadline2=$(( $(date +%s) + 90 ))
    while :; do
      runs_raw="$(fetch_runs)" || exit 2
      classify_runs
      if [ -n "${cov_event[i]:-}" ]; then
        event="${cov_event[i]}" status="${cov_status[i]}" id="${cov_id[i]}" url="${cov_url[i]}"
        if [ "$event" = "pull_request" ]; then
          echo "$w: GitHub delivered the event while the dispatch was in flight; run $id covers the head, and the extra dispatch run cannot cancel it"
          echo "CIWATCH covered workflow=$w by=$event run=$id url=$url"
        else
          echo "$w: dispatched; run $id (status $status) covers the head now"
          echo "CIWATCH dispatched workflow=$w run=$id url=$url"
        fi
        break
      fi
      now=$(date +%s)
      if [ "$now" -ge "$deadline2" ]; then
        echo "$w: dispatch accepted but no run covers $head_oid within 90s (a cancelled run does not count); look for it at https://github.com/$repo/actions/workflows/$w"
        echo "CIWATCH missing workflow=$w head=$check_oid reason=dispatch-unconfirmed"
        rc=1
        break
      fi
      left=$(( deadline2 - now ))
      sleep $(( left < 10 ? left : 10 ))
    done
  fi
done
exit "$rc"
