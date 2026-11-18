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
# Exit 2: usage error, the enqueue call failed, or this launcher could not
#         build or start the helper (message says why).
# Exit 130: the wait was interrupted (SIGINT, SIGTERM or SIGHUP); the launcher
#           forwards the signal to the helper, which stops polling at once.
#
# The exit codes are the contract. 0 means merged or queued, 1 means the queue
# ended the attempt (dropped or timed out), 2 means nothing was decided: bad
# arguments, a failed GitHub call, or a launcher that could not run the helper.
# 130 means an operator stopped it. A launcher failure is never reported as 1
# (which a caller retries) or 126/127 (which mean "command not executable"),
# and a helper that did start always keeps its own status.
#
# The decisions are internal/devtool/mergeq's, tested under `go test`; this
# script parses its arguments and keeps the contract above. It was an
# orchestration script whose only test seam was a fake `gh` on PATH, which
# docs/developing-evener/testing.md bans: a fake on PATH tests the fake, and
# the argv a script hands it is an implementation restated. Porting the
# decisions is that rule's remedy (the port-on-touch trigger), not a fixture
# harness around the fake.
set -euo pipefail

# The contract block above is lines 2-36; the rationale after it is for a
# reader of this file, not for --help.
usage() { sed -n '2,36p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

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
# The caller's directory, resolved before anything changes it: an
# EVENER_DEV_BIN relative to the caller must mean the file the caller named,
# not something under the checkout root.
caller_pwd="$PWD"

# Forward a signal to the helper and reap it before this script exits, so a
# SIGTERM at the script's PID stops the poll instead of leaving it running
# until the wait expires. Ctrl-C already reaches the helper through the
# terminal's process group; this is what covers a signal sent to the PID.
#
# The handler ends the script even when no helper has started yet (a signal
# during the build would otherwise be swallowed), and it always reports 130:
# the helper's own interrupt exit. Reaping here rather than leaving it to the
# EXIT trap is what guarantees the helper is gone before scratch is reclaimed.
helper=""
helper_started=false
interrupted() {
  local sig="$1" code=130
  if [ -n "$helper" ] && kill -0 "$helper" 2>/dev/null; then
    kill -s "$sig" "$helper" 2>/dev/null || true
    # Bounded: a helper that ignores the signal must not hang the launcher.
    local waited=0
    while kill -0 "$helper" 2>/dev/null && [ "$waited" -lt 50 ]; do
      sleep 0.1
      waited=$((waited + 1))
    done
    kill -s KILL "$helper" 2>/dev/null || true
    wait "$helper" 2>/dev/null || true
  fi
  exit "$code"
}
trap 'interrupted TERM' TERM
trap 'interrupted INT' INT
trap 'interrupted HUP' HUP

# The exit-code table's backstop. scratch_dir and scratch_rm are shared
# libraries that exit 1 on their own guard failures, and a shared library must
# not be edited for one caller's contract. Without this, a stale TMPDIR would
# report exit 1 -- "the queue dropped your PR, retry" -- for a launcher that
# never asked GitHub anything, and a noexec scratch would surface the shell's
# own 126. So: once the helper has started, its status is the contract and
# passes through untouched. Before that, only 0, 2 and an interrupt are
# legitimate; anything else is this script failing to get as far as a decision,
# which is exit 2.
finish() {
  local status=$?
  # Under set -e a failing scratch_rm would abort this handler before the
  # remap below, leaving the library's own status as the script's. Its failure
  # is not the caller's problem: scratch that cannot be resolved is already
  # gone or unreachable, and the exit code is the contract.
  scratch_rm || true
  if [ "$helper_started" != true ] && [ "$status" -ne 0 ] && [ "$status" -ne 2 ] && [ "$status" -ne 130 ]; then
    status=2
  fi
  exit "$status"
}

# start_helper BIN — run the helper as a child, record its PID for the signal
# handlers, and wait for it. Returns the helper's own status, which is the
# contract's whenever the helper started.
start_helper() {
  cd "$root" || return 2
  env GOFLAGS= "$1" dev merge-enqueue \
    --repo "$repo" --pr "$pr" --wait "$wait" ${jump[@]+"${jump[@]}"} &
  helper=$!
  helper_started=true
  local status=0
  wait "$helper" || status=$?
  helper=""
  # 126 and 127 are the shell's "found but could not execute" and "not found".
  # A noexec mount produces the first after a successful build, and neither is
  # a decision the helper made: the helper's own codes are 0, 1, 2 and 130.
  case "$status" in
    126|127)
      helper_started=false
      echo "merge-enqueue: the helper could not be executed: $1" >&2
      return 2
      ;;
  esac
  return "$status"
}

# launcher_failed MESSAGE — a failure in this script, before or instead of the
# helper deciding anything. That is exit 2 by the contract: 1 would tell a
# caller the queue dropped the PR and invite a retry, and the shell's own
# 126/127 would name an executable problem the operator cannot act on.
launcher_failed() {
  echo "merge-enqueue: $1" >&2
  exit 2
}

if [ -n "${EVENER_DEV_BIN:-}" ]; then
  # A relative override belongs to the caller's directory. Absolute paths are
  # used as given, and the helper still runs from the checkout root so the
  # argv it is handed does not depend on where the operator stood.
  case "$EVENER_DEV_BIN" in
    /*) helper_bin="$EVENER_DEV_BIN" ;;
    *) helper_bin="$caller_pwd/$EVENER_DEV_BIN" ;;
  esac
  [ -x "$helper_bin" ] || launcher_failed "EVENER_DEV_BIN is not an executable file: $EVENER_DEV_BIN"
  start_helper "$helper_bin" || exit $?
  exit 0
fi

# Scratch is minted by scratch_dir and reclaimed by the no-argument scratch_rm
# (scripts/lib/scratch-lib.sh): a recursive delete must never take an argument
# a caller could clobber. The trap is armed before the mint, so a failure in
# between still reclaims the directory, and the helper runs as a child rather
# than through exec so the trap still fires.
#
# The scratch must also be able to run what is written to it: a noexec TMPDIR
# is writable, so the build succeeds and the helper then fails to start with
# the shell's own 126. gate_scratch_root_executes is the repository's probe for
# exactly that, and its refusal is a launcher failure (exit 2).
#
# The helper is built and run rather than launched with `go run`: `go run`
# reports every child status as 1, which would erase both the drop/timeout 1
# and the refusal 2. GOFLAGS= keeps an inherited GOFLAGS from changing how it
# builds, the same reason timing-tsv-lib.sh pins it.
. "$root/scripts/lib/scratch-lib.sh"
. "$root/scripts/lib/gate-scratch-root.sh"
trap 'finish' EXIT
scratch_dir work merge-enqueue
gate_scratch_root_executes "$work" ||
  launcher_failed "scratch $work cannot execute a program (a noexec mount?); point TMPDIR at an exec-capable directory"
(
  cd "$root" || exit 1
  GOFLAGS= go build -o "$work/evener-dev" ./cmd/evener-dev/bin
) || launcher_failed "building the evener-dev helper failed"
[ -x "$work/evener-dev" ] || launcher_failed "the built helper is not executable: $work/evener-dev"
start_helper "$work/evener-dev" || exit $?
exit 0
