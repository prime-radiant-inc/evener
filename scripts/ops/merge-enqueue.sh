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
#
# The decisions are internal/devtool/mergeq's, tested under `go test`; this
# script parses its arguments and keeps the contract above. It was an
# orchestration script whose only test seam was a fake `gh` on PATH, which
# docs/developing-evener/testing.md bans: a fake on PATH tests the fake, and
# the argv a script hands it is an implementation restated. Porting the
# decisions is that rule's remedy (the port-on-touch trigger), not a fixture
# harness around the fake.
set -euo pipefail

# The contract block above is lines 2-26; the rationale after it is for a
# reader of this file, not for --help.
usage() { sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

repo="prime-radiant-inc/evener" pr="" wait=0 jump=()
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo="$2"; shift 2 ;;
    --pr) pr="$2"; shift 2 ;;
    --wait) wait="$2"; shift 2 ;;
    --jump) jump=(--jump); shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done
[ -n "$repo" ] && [ -n "$pr" ] || usage
case "$wait" in ''|*[!0-9]*) echo "--wait takes whole seconds, got: $wait" >&2; usage ;; esac
wait=$((10#$wait))  # decimal: bash arithmetic reads a leading 0 as octal
case "$repo" in
  */*) ;;
  *) echo "--repo takes OWNER/NAME, got: $repo" >&2; usage ;;
esac

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# The Go command's exit codes are the contract's (0 queued/merged, 1 dropped or
# timed out, 2 refused), so they must survive the launcher. That rules out
# `go run`, which reports every child status as 1: the helper is built and run
# instead. GOFLAGS= keeps an inherited GOFLAGS from changing how it builds, the
# same reason timing-tsv-lib.sh pins it.
if [ -n "${EVENER_DEV_BIN:-}" ]; then
  cd "$root"
  exec env GOFLAGS= "$EVENER_DEV_BIN" dev merge-enqueue \
    --repo "$repo" --pr "$pr" --wait "$wait" ${jump[@]+"${jump[@]}"}
fi

# Scratch is minted by scratch_dir and reclaimed by the no-argument scratch_rm
# (scripts/lib/scratch-lib.sh): a recursive delete must never take an argument
# a caller could clobber. The trap is armed before the mint, so a failure in
# between still reclaims the directory, and the helper runs as a child rather
# than through exec so the trap still fires.
. "$root/scripts/lib/scratch-lib.sh"
trap 'scratch_rm' EXIT
scratch_dir work merge-enqueue
(
  cd "$root" || exit 1
  GOFLAGS= go build -o "$work/evener-dev" ./cmd/evener-dev/bin
) || { echo "merge-enqueue: building the evener-dev helper failed" >&2; exit 2; }
status=0
"$work/evener-dev" dev merge-enqueue \
  --repo "$repo" --pr "$pr" --wait "$wait" ${jump[@]+"${jump[@]}"} || status=$?
exit "$status"
