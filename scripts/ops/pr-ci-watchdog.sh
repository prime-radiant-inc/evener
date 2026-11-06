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
# the watchdog never double-dispatches.
#
# The default workflow, ios-qualification.yml, is the native lane's macOS
# simulator qualification: its workflow_dispatch trigger takes no inputs, it
# builds and qualifies without publishing anything, and a dispatch run
# shares no concurrency group with a pull_request run, so a watchdog
# dispatch cannot cancel one. Pass --workflow to also verify other
# workflows; only workflows with a workflow_dispatch trigger can be
# re-dispatched (ci.yml has none, so a dropped CI run can be reported, not
# replaced; every run found on the head is listed, so a missing CI run
# stays visible either way). A workflow with paths filters only fires when
# the push touches them: run the watchdog after a push you expect to
# trigger it.
#
#   scripts/ops/pr-ci-watchdog.sh --pr N [--workflow FILE]... [--wait SECONDS]
#                                 [--sha SHA] [--dry-run] [--repo OWNER/NAME]
#
# --pr is the pull request just pushed. --workflow repeats; each is a file
# under .github/workflows/ that should have a run for the head (default
# ios-qualification.yml). --wait is how long to poll for the run before
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
#                                (exit 1: no run and no dispatch happened;
#                                R is one of --sha-check-only, dry-run,
#                                head-moved, dispatch-unconfirmed)
# Exit 2: usage error, the PR could not be resolved, or a gh call failed
# (the message says which).
set -euo pipefail

usage() { sed -n '2,60p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

repo="prime-radiant-inc/evener" pr="" wait=300 sha="" dry_run=false
workflows=()
while [ $# -gt 0 ]; do
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

# resolve prints the PR's state, head branch and head SHA in one query, so
# the verdict is about the head GitHub has right now. It stops the script
# when the lookup fails (a failure inside "$(...)" alone would not).
if ! out=$(gh pr view "$pr" --repo "$repo" --json state,headRefName,headRefOid \
  --jq '[.state, .headRefName, .headRefOid] | @tsv' 2>&1); then
  echo "CIWATCH resolve failed: gh pr view $pr --repo $repo said: $out" >&2
  exit 2
fi
IFS=$'\t' read -r state head_branch head_oid <<<"$out"
if [ "$state" != "OPEN" ]; then
  echo "CIWATCH refused: PR $pr is $state, not OPEN; a $state PR gets no pull_request events and dispatching would build a stale head" >&2
  exit 2
fi
check_oid="$head_oid"
if [ -n "$sha" ]; then
  check_oid="$sha"
fi

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

# run_of_workflow prints the run covering the given workflow file (event,
# status, id, url), preferring a pull_request run over a dispatch and the
# newest within each kind; nothing when the workflow has no run on the head.
run_of_workflow() {
  local want="$1" last="" prrun="" path event status id url
  while IFS=$'\t' read -r path event status id url; do
    [ -n "${path:-}" ] || continue
    [ "${path##*/}" = "$want" ] || continue
    last=$(printf '%s\t%s\t%s\t%s' "$event" "$status" "$id" "$url")
    if [ "$event" = "pull_request" ]; then prrun="$last"; fi
  done <<<"$runs_raw"
  if [ -n "$prrun" ]; then printf '%s' "$prrun"; else printf '%s' "$last"; fi
}

# missing_workflows prints the expected workflows with no run on the head,
# one per line; nothing at all when every expected workflow is covered.
missing_workflows() {
  local w out=""
  for w in "${workflows[@]}"; do
    if [ -z "$(run_of_workflow "$w")" ]; then
      out="$out$w"$'\n'
    fi
  done
  printf '%s' "$out"
}

echo "PR #$pr on $repo: branch $head_branch, head $head_oid"
if [ "$check_oid" != "$head_oid" ]; then
  echo "checking --sha $check_oid; a --sha check never dispatches"
fi
echo "expecting a run of: ${workflows[*]}"

runs_raw="$(fetch_runs)" || exit 2
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
for w in "${workflows[@]}"; do
  line="$(run_of_workflow "$w")"
  if [ -n "$line" ]; then
    IFS=$'\t' read -r event status id url <<<"$line"
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
    if ! cur=$(gh pr view "$pr" --repo "$repo" --json headRefOid --jq '.headRefOid' 2>&1); then
      echo "CIWATCH resolve failed while re-checking the head: gh pr view $pr said: $cur" >&2
      exit 2
    fi
    if [ "$cur" != "$head_oid" ]; then
      echo "$w: no run on $check_oid, and the PR head moved to $cur during the wait; not dispatching for a stale head; re-run for the new push"
      echo "CIWATCH missing workflow=$w head=$check_oid reason=head-moved"
      rc=1
      continue
    fi
    echo "$w: no run on $check_oid after ${wait}s; GitHub dropped the event; dispatching $w on $head_branch"
    if ! gh workflow run "$w" --repo "$repo" --ref "$head_branch" 2>&1; then
      echo "CIWATCH dispatch failed: gh workflow run $w --ref $head_branch did not accept the dispatch; a workflow without a workflow_dispatch trigger reports this" >&2
      exit 2
    fi
    # gh workflow run returns before GitHub registers the run; confirm the
    # run exists so the verdict names one the operator can watch.
    deadline2=$(( $(date +%s) + 90 ))
    while :; do
      runs_raw="$(fetch_runs)" || exit 2
      line="$(run_of_workflow "$w")"
      if [ -n "$line" ]; then
        IFS=$'\t' read -r event status id url <<<"$line"
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
        echo "$w: dispatch accepted but no run registered on $head_oid within 90s; look for it at https://github.com/$repo/actions/workflows/$w"
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
