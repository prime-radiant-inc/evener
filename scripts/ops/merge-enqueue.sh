#!/usr/bin/env bash
# merge-enqueue.sh: add a PR to its base branch's GitHub merge queue, and
# optionally wait until it merges or the queue drops it.
#
# Use it to merge a PR into main once every check is green and RoboRev's app
# has approved (AGENTS.md, "Merge through the merge queue"). `gh pr merge
# --squash` falls back to auto-merge, which this repo disallows, so this calls
# the enqueuePullRequest mutation directly. Running it again on a queued PR
# does not enqueue it twice.
#
#   scripts/ops/merge-enqueue.sh --pr N [--wait SECONDS] [--jump] [--repo OWNER/NAME]
#
# --wait polls every 30s until the PR merges, leaves the queue, or SECONDS
# pass. --jump puts the PR at the front of the queue as it is enqueued (admins
# only; for a fix the queue itself is waiting on) and refuses an already queued
# PR. --repo defaults to prime-radiant-inc/evener.
#
# The decisions are internal/devtool/mergeq's, reached through `evener-dev dev
# merge-enqueue`. Every argument is forwarded to it unchanged, and so are its
# output and its exit status. The helper is $EVENER_DEV_BIN when it is set, else
# an `evener-dev` on PATH that resolves inside this checkout, else one built into
# a temp directory this run owns and removes.
#
# Output (last line is the outcome):
#   MERGEQ queued position=P            (no --wait)
#   MERGEQ merged sha=SHA               (exit 0)
#   MERGEQ dropped: <reason>            (exit 1: left the queue unmerged)
#   MERGEQ timeout position=P state=S   (exit 1: still queued after --wait)
# Exit 2: usage error, a failed enqueue call, or a helper that could not be
#         built or started.
# Exit 130: the wait was interrupted (SIGINT, SIGTERM or SIGHUP). The helper
#         leads its own process group; one signal asks it to cancel and stop the
#         `gh` call behind it, a second stops the whole group.
#
# Cancelling a background run: send SIGTERM to this script's PID; bash cannot
# install an INT trap in a job started with `&` from a non-interactive shell.
set -euo pipefail

# CDPATH is cleared for these two lookups alone: a caller's CDPATH makes `cd`
# print the directory it chose, and that line would land in the captured path.
script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
root=$(CDPATH='' cd -- "$script_dir/../.." && pwd -P)
caller_pwd=$PWD

# --help is the contract block above: every comment line from line 2 down to
# `set -euo pipefail`, derived rather than numbered so a paragraph can be added
# without the help text going stale or stopping short of the exit codes.
usage() { sed -n '2,/^set -euo pipefail$/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'; }

for arg in "$@"; do
  case "$arg" in
    -h | --help) usage; exit 0 ;;
  esac
done

# The helper is resolved against the caller's directory and runs from the
# checkout root, so the build below and the helper both see ./cmd/evener-dev/bin.
cd -- "$root"

# A failure in this script, before the helper decided anything, is exit 2: 1
# would tell the caller the queue dropped the PR and invite a retry.
fail() {
  echo "merge-enqueue: $1" >&2
  exit 2
}

# installed_helper prints an `evener-dev` from PATH only when it resolves inside
# this checkout; nothing proves a foreign one carries these queue decisions.
installed_helper() {
  local found dir
  found=$(command -v evener-dev 2>/dev/null) || return 1
  case "$found" in /*) ;; *) return 1 ;; esac
  dir=$(cd -- "$(dirname -- "$found")" 2>/dev/null && pwd -P) || return 1
  case "$dir/" in
    "$root"/*) printf '%s\n' "$dir/$(basename -- "$found")" ;;
    *) return 1 ;;
  esac
}

child=""
stops=0
term_sent=0

# stop_child starts the helper's stop, or ends it. The first signal asks the
# helper to cancel: it stops the `gh` call it waits on and reports 130, the
# status passed through. A second signal does not wait: the helper leads its own
# process group, so the group is signalled and `gh` goes with it. A signal before
# the helper starts is remembered, and the run stops before launching it.
stop_child() {
  stops=$((stops + 1))
  if [ -z "$child" ] || [ "$child" -le 1 ] || ! kill -0 "$child" 2>/dev/null; then
    return 0
  fi
  if [ "$term_sent" -eq 0 ]; then
    term_sent=1
    kill -TERM "$child" 2>/dev/null || true
    return 0
  fi
  kill -KILL -- "-$child" 2>/dev/null || true
}

. "$root/scripts/lib/scratch-lib.sh"

# The cleanup trap is armed before anything is minted, so a signal in between
# still removes the directory; scratch_rm takes no argument and removes only what
# scratch_dir registered for this run.
trap 'scratch_rm' EXIT
trap 'stop_child' INT
trap 'stop_child' TERM
trap 'stop_child' HUP

if [ -n "${EVENER_DEV_BIN:-}" ]; then
  # A relative override belongs to the caller's directory, not the checkout.
  case "$EVENER_DEV_BIN" in
    /*) helper_bin=$EVENER_DEV_BIN ;;
    *) helper_bin=$caller_pwd/$EVENER_DEV_BIN ;;
  esac
  [ -x "$helper_bin" ] || fail "EVENER_DEV_BIN is not an executable file: $EVENER_DEV_BIN"
elif helper_bin=$(installed_helper); then
  :
else
  # Built, not `go run`: `go run` reports every child status as 1, which would
  # erase both the drop/timeout 1 and the refusal 2. GOOS/GOARCH ask for a helper
  # this host can run, and GOFLAGS= keeps an inherited GOFLAGS out of the build
  # (a value saved with `go env -w` still applies). An interruption during the
  # build reports 130 rather than a build error.
  scratch_dir work merge-enqueue
  helper_bin="$work/evener-dev"
  build_status=0
  GOOS= GOARCH= GOFLAGS= go build -o "$helper_bin" ./cmd/evener-dev/bin || build_status=$?
  [ "$stops" -eq 0 ] || exit 130
  [ "$build_status" -eq 0 ] || fail "building the evener-dev helper failed"
  [ -x "$helper_bin" ] || fail "the built helper is not executable: $helper_bin"
fi

# Monitor mode puts the helper in its own process group, so a terminal Ctrl-C
# reaches this script and not the helper's `gh`: the helper's own cancellation
# stops the call, where a `gh` killed mid-submit would read as a GitHub failure
# and print a drop verdict. It also makes `-$child` the group to stop.
set -m
"$helper_bin" dev merge-enqueue "$@" &
set +m
child=$!

# A signal that arrived between the fork and the PID record above was
# remembered; the helper is addressable now, so deliver the stop.
[ "$stops" -eq 0 ] || stop_child

# wait returns 128+signal when a trapped signal interrupts it, and stop_child has
# already started stopping the helper, so the loop re-waits until the helper is
# gone. A helper that reports its own outcome -- 0, 1, 2 or 130 -- keeps it.
status=0
while :; do
  wait "$child" || status=$?
  kill -0 "$child" 2>/dev/null || break
done
# A helper the shell could not execute reports 126 or 127, which the contract
# does not define and which would read as "command not executable" rather than
# the launcher failure it is.
if [ "$status" -eq 126 ] || [ "$status" -eq 127 ]; then
  fail "the helper could not be started: $helper_bin"
fi
exit "$status"
