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
# a temp directory this run owns and removes. That directory also holds the gh
# process group the helper publishes while a call is in flight.
#
# Output (last line is the outcome):
#   MERGEQ queued position=P            (no --wait)
#   MERGEQ merged sha=SHA               (exit 0)
#   MERGEQ dropped: <reason>            (exit 1: left the queue unmerged)
#   MERGEQ timeout position=P state=S   (exit 1: still queued after --wait)
# Exit 2: usage error, a failed enqueue call, a scratch directory that could not
#         be set up, or a helper that could not be built or started.
# Exit 130: the wait was interrupted (SIGINT, SIGTERM or SIGHUP). The helper
#         leads its own process group; one signal asks it to cancel and stop the
#         `gh` call behind it, a second kills that group and the `gh` group too.
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
# this checkout; nothing proves a foreign one carries these queue decisions. The
# resolved file is what is judged, so a symlink out of the checkout is refused.
installed_helper() {
  local found dir
  found=$(command -v evener-dev 2>/dev/null) || return 1
  case "$found" in /*) ;; *) return 1 ;; esac
  # realpath takes no `--` on BSD userlands; $found is absolute, so no argv
  # element here can read as an option.
  found=$(realpath "$found" 2>/dev/null) || return 1
  dir=$(cd -- "$(dirname -- "$found")" 2>/dev/null && pwd -P) || return 1
  case "$dir/" in
    "$root"/*) printf '%s\n' "$dir/$(basename -- "$found")" ;;
    *) return 1 ;;
  esac
}

child=""
stops=0
term_sent=0
group_stop=""
gh_group_file=""

# kill_groups PGID — stop a tracked group, and the gh group the helper
# published. Used by the second signal and after any interrupted wait: a helper
# that exits on TERM without forwarding it to its own child leaves that child and
# an in-flight gh call behind, and they would outlive the scratch this run owns.
kill_groups() {
  [ -z "$1" ] || [ "$1" -le 1 ] || kill -KILL -- "-$1" 2>/dev/null || true
  # internal/devtool/mergeq publishes the live gh group here while a call is in
  # flight (EVENER_MERGEQ_GH_GROUP_FILE). A value that is not a whole number
  # above 1 is not one, and is left alone.
  if [ -n "$gh_group_file" ] && [ -r "$gh_group_file" ]; then
    gh_pgid=$(cat -- "$gh_group_file" 2>/dev/null) || gh_pgid=""
    case "$gh_pgid" in
      '' | *[!0-9]*) ;;
      *) [ "$gh_pgid" -gt 1 ] && kill -KILL -- "-$gh_pgid" 2>/dev/null || true ;;
    esac
  fi
}

# stop_child starts the tracked child's stop, or ends it. The first signal asks
# it to cancel: for the helper that means stopping the `gh` call it waits on and
# reporting 130, the status passed through. A second signal does not wait: the
# child leads its own process group, so the group is signalled, and so is the gh
# group the helper published, because a `gh` call runs in a group of its own and
# would otherwise outlive the helper. A signal before the child starts is
# remembered, and the run stops before launching it.
stop_child() {
  stops=$((stops + 1))
  if [ -z "$child" ] || [ "$child" -le 1 ] || ! kill -0 "$child" 2>/dev/null; then
    return 0
  fi
  if [ "$term_sent" -eq 0 ]; then
    term_sent=1
    # A build has no cancellation of its own and its compilers are children in
    # its group, so the whole group is signalled. The helper takes the signal
    # itself, so it can stop the gh call it waits on and report 130.
    if [ -n "$group_stop" ]; then
      kill -TERM -- "-$child" 2>/dev/null || true
    else
      kill -TERM "$child" 2>/dev/null || true
    fi
    return 0
  fi
  kill_groups "$child"
}

# start_tracked CMD... — run CMD as the tracked child, in a process group of its
# own so the stop above reaches it and what it starts. The group is fixed at the
# fork, so monitor mode ends at the next line.
start_tracked() {
  set -m
  "$@" &
  set +m
  child=$!
  # A signal that arrived between the fork and the record above was
  # remembered; the child is addressable now, so deliver the stop.
  [ "$stops" -eq 0 ] || stop_child
}

# wait_tracked — wait for the tracked child, leaving its status in status. A
# trapped signal interrupts wait with 128+signal while the child is only being
# stopped, so the loop re-waits until the child is gone. `child` is cleared so a
# stop after this cannot signal a pid this run has already reaped.
wait_tracked() {
  status=0
  while :; do
    wait "$child" || status=$?
    kill -0 "$child" 2>/dev/null || break
  done
  child=""
}

. "$root/scripts/lib/scratch-lib.sh"
. "$root/scripts/lib/gate-scratch-root.sh"

# The cleanup trap is armed before anything is minted, so a signal in between
# still removes the directory; scratch_rm takes no argument and removes only what
# scratch_dir registered for this run. scratch_dir exits 1 when it cannot mint,
# which the contract reads as a queue drop, so a setup failure reports 2.
in_scratch_setup=""
cleanup() {
  scratch_rm
  [ -z "$in_scratch_setup" ] || exit 2
}
trap 'cleanup' EXIT
trap 'stop_child' INT
trap 'stop_child' TERM
trap 'stop_child' HUP

# The helper is resolved first: only a build needs a root that can execute.
need_build=""
if [ -n "${EVENER_DEV_BIN:-}" ]; then
  # A relative override belongs to the caller's directory, not the checkout.
  case "$EVENER_DEV_BIN" in
    /*) helper_bin=$EVENER_DEV_BIN ;;
    *) helper_bin=$caller_pwd/$EVENER_DEV_BIN ;;
  esac
  [ -x "$helper_bin" ] || fail "EVENER_DEV_BIN is not an executable file: $EVENER_DEV_BIN"
else
  helper_bin=$(installed_helper) || need_build=1
fi

# Choose a usable root first: a writable directory is enough for a prebuilt
# helper, and a build also needs one that executes what is written there, or a
# noexec mount fails `go build` at the last moment instead.
for scratch_root in "${TMPDIR:-}" "${XDG_RUNTIME_DIR:-}" "${XDG_CACHE_HOME:-${HOME:+$HOME/.cache}}" /tmp; do
  [ -n "$scratch_root" ] && [ -d "$scratch_root" ] && [ -w "$scratch_root" ] || continue
  [ -z "$need_build" ] || gate_scratch_root_executes "$scratch_root" || continue
  TMPDIR=$scratch_root
  export TMPDIR
  break
done
[ -d "${TMPDIR:-}" ] && [ -w "${TMPDIR:-}" ] ||
  fail "no writable directory for scratch (TMPDIR, XDG_RUNTIME_DIR, XDG_CACHE_HOME and /tmp are all unusable)"
[ -z "$need_build" ] || gate_scratch_root_executes "${TMPDIR:-}" ||
  fail "no scratch directory can execute the helper built into it (a noexec mount?)"

in_scratch_setup=1
scratch_dir work merge-enqueue
in_scratch_setup=""
gh_group_file="$work/gh-group"

if [ -n "$need_build" ]; then
  # Built, not `go run`, which reports every child status as 1 and would erase
  # both the drop/timeout 1 and the refusal 2. The build is tracked like the
  # helper, and the host pair is read inside that group too: on a cold machine
  # `go env` can download a toolchain, and a foreground call would outlive the
  # signal stopping the build. GOOS/GOARCH name this host over an inherited or
  # saved pair, GOFLAGS carries a harmless flag so a saved `go env -w
  # GOFLAGS=...` cannot apply, GOWORK pins this checkout's workspace, and
  # GOTMPDIR keeps the build's work directory in this scratch.
  helper_bin="$work/evener-dev"
  group_stop=1
  start_tracked bash -c '
export GOWORK="$1/go.work" GOFLAGS=-buildvcs=false GOTMPDIR="$2"
GOOS=$(go env GOHOSTOS) GOARCH=$(go env GOHOSTARCH) exec go build -o "$3" ./cmd/evener-dev/bin' _ "$root" "$work" "$helper_bin"
  build_pgid=$child
  wait_tracked
  group_stop=""
  if [ "$stops" -ne 0 ]; then
    # The build's children run in its group; stopping it again leaves nothing
    # writing into a scratch directory that is about to be removed.
    kill_groups "$build_pgid"
    exit 130
  fi
  [ "$status" -eq 0 ] || fail "building the evener-dev helper failed"
  [ -x "$helper_bin" ] || fail "the built helper is not executable: $helper_bin"
fi
[ "$stops" -eq 0 ] || exit 130

start_tracked env EVENER_MERGEQ_GH_GROUP_FILE="$gh_group_file" "$helper_bin" dev merge-enqueue "$@"
helper_pgid=$child
wait_tracked

# An interrupted run reports the interruption, not whatever the killed group
# died with: a wait broken by a trapped signal answers 128+signal even when the
# handler then killed the child, so the collected status depends on timing. What
# the helper left behind is stopped here either way, including a helper that
# exited on TERM without forwarding it to its own child.
if [ "$stops" -ne 0 ]; then
  kill_groups "$helper_pgid"
  exit 130
fi

# A helper the shell could not execute reports 126 or 127, which the contract
# does not define; that is the launcher failure, not "command not executable".
if [ "$status" -eq 126 ] || [ "$status" -eq 127 ]; then
  fail "the helper could not be started: $helper_bin"
fi
exit "$status"
