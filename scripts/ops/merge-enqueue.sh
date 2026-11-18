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
# Cancelling a background run: send SIGTERM to this script's PID. Bash cannot
# install an INT trap, nor reset an inherited-ignored SIGINT, in a job started
# with `&` from a non-interactive shell -- the kernel already had it ignored, so
# a PID-directed SIGINT is ignored by the launcher and never reaches the
# helper. SIGTERM always works, and so does Ctrl-C in a terminal, which the
# tty's process group delivers to the helper directly.
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

# A value-taking option at the end of the line has no value: reading $2 there is
# an "unbound variable" abort under set -u, which exits 1 -- the code that means
# "the queue dropped your PR". Refuse it as usage instead.
need_value() {
  [ "$#" -ge 2 ] || { echo "$1 requires a value" >&2; usage; }
}

# wait stays a string all the way to the helper. Bash arithmetic would wrap a
# value like 18446744073709551616 to 0 and hand the helper a valid no-wait
# request for a number the operator never meant; the helper's own parser is the
# one that decides, and it reads leading zeros as decimal (#5304).
repo="prime-radiant-inc/evener" pr="" wait=0 jump=()
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) need_value "$@"; repo="$2"; shift 2 ;;
    --pr) need_value "$@"; pr="$2"; shift 2 ;;
    --wait) need_value "$@"; wait="$2"; shift 2 ;;
    --jump) jump=(--jump); shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done
[ -n "$repo" ] && [ -n "$pr" ] || usage
case "$repo" in
  */*) ;;
  *) echo "--repo takes OWNER/NAME, got: $repo" >&2; usage ;;
esac

# The checkout root. CDPATH is cleared for the same reason build-runtime-pair.sh
# clears it: a CDPATH entry that matches makes cd print the directory it moved
# to, and this substitution would capture that line as part of the root. A root
# with a newline in it is not a path, and the failure it used to cause -- the
# scratch library not loading before the exit trap exists -- reported exit 1,
# which means the queue ended the attempt. It is a launcher failure instead.
root="$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." 2>/dev/null && pwd)" || root=""
case "$root" in
  /*) ;;
  *) root="" ;;
esac
case "$root" in
  *$'\n'*) root="" ;;
esac
if [ -z "$root" ] || [ ! -d "$root/scripts/lib" ]; then
  echo "merge-enqueue: cannot resolve the checkout root from ${BASH_SOURCE[0]}" >&2
  exit 2
fi
# The caller's directory, resolved before anything changes it: an
# EVENER_DEV_BIN relative to the caller must mean the file the caller named,
# not something under the checkout root.
caller_pwd="$PWD"

# TMPDIR is resolved here for the same reason, and once: a relative TMPDIR such
# as `tmp` means the caller's `tmp`, but scratch_rm resolves TMPDIR again when
# it runs, after the script has moved to the checkout root. Left alone, the
# build would look for `tmp` under the root and cleanup would look for it
# somewhere else again, silently skipping the scratch it just created. An
# absolute TMPDIR is unchanged, so an unset or absolute one behaves as before.
if [ -n "${TMPDIR:-}" ]; then
  case "$TMPDIR" in
    /*) ;;
    *) TMPDIR="$caller_pwd/$TMPDIR" ;;
  esac
fi
export TMPDIR

# Forward a signal to the child and reap it before this script exits, so a
# SIGTERM at the script's PID stops the poll instead of leaving it running
# until the wait expires. Ctrl-C already reaches a terminal's children through
# its process group; this is what covers a signal sent to the PID.
#
# The child is whichever process is in flight: `go build` during a cold build,
# or the helper while it polls. Bash defers traps until a foreground command
# returns, so the build runs in the background and is waited for here; a
# foreground build would not be interrupted at all and the caller would keep
# its pipes open until it finished.
#
# The handler ends the script even when no child has started yet, and it always
# reports 130: the helper's own interrupt exit. Reaping here rather than leaving
# it to the EXIT trap is what guarantees the child is gone before scratch is
# reclaimed.
#
# The helper is signalled on its own PID first, and only its own: it handles
# TERM by cancelling the context it started `gh` under, so the cancelled context
# is what stops the GitHub call. `stop_command` on a helper that shares this
# script's process group walks the tree and signals the descendants first, which
# TERMs `gh` behind the helper's back: the helper then sees a failed removal-event
# read with a still-live context and reports a drop verdict and re-enqueue advice
# for a wait the operator cancelled. The build is the opposite case -- it is a
# process tree with no graceful stop of its own -- so it keeps `stop_command`.
#
# A helper that ignores TERM would otherwise hold the caller's pipes open, so it
# gets a short grace and then the tree stopper. Two seconds is longer than the
# helper needs to cancel a `gh` call and return 130 (measured under 0.7s) and
# short enough that an operator does not notice it.
#
# A signal that arrives during a launch is held, not acted on: `cmd &` and
# `child=$!` are two commands, and Bash runs a pending trap between them. The
# handler would then see an empty `child` and exit 130 with the just-started
# helper still polling -- or, worse, still enqueueing -- after the caller was
# told the attempt was interrupted. Both launch functions bracket their whole
# fork-record sequence in `launching=true`, and the handler sets
# cancel_requested and returns while that is set.
child=""
child_is_helper=false
helper_started=false
launching=false
cancel_requested=false
helper_term_grace_seconds=2
# child_running reports whether pid is still running. A child this script has
# not reaped yet is a zombie, and kill -0 still succeeds for one: counting that
# as running would spend the whole grace on every interrupt and then hand a
# corpse to the tree stopper. poll_gone in gatebounded_test.go uses the same
# zombie-aware form for the same reason.
child_running() {
  local state
  state=$(ps -o stat= -p "$1" 2>/dev/null) || return 1
  case "$state" in
    '' | Z*) return 1 ;;
  esac
  return 0
}
reap_child() {
  [ -n "$child" ] || return 0
  if [ "$child_is_helper" = true ] && kill -TERM "$child" 2>/dev/null; then
    local deadline=$((SECONDS + helper_term_grace_seconds))
    while [ "$SECONDS" -lt "$deadline" ] && child_running "$child"; do
      sleep 0.1
    done
    # Still there after the grace: it did not act on TERM, so stop its whole
    # tree and never wait past that stopper's own grace.
    if child_running "$child"; then
      stop_command "$child"
    fi
  else
    # The whole tree, not the PID: `go build` forks a compiler and a linker, and
    # signalling only the shell that started it leaves those children holding the
    # caller's output pipes after this script has exited. stop_command sends one
    # TERM then one KILL to the group when the child leads its own, and walks the
    # tree when it does not; it never waits past its grace.
    stop_command "$child"
  fi
  wait "$child" 2>/dev/null || true
}
interrupted() {
  if [ "$launching" = true ]; then
    cancel_requested=true
    return 0
  fi
  reap_child
  exit 130
}

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
  # Monitor mode gives the helper its own process group, following run_bounded
  # in scripts/lib/gate-bounded.sh. Two reasons, both #6730's review:
  #
  #   - A terminal Ctrl-C reaches the foreground process group, which used to
  #     include the helper's `gh`. `gh` would then die before the helper's own
  #     handler cancelled the context, and the helper would read the interrupted
  #     call as an ordinary GitHub failure and print a drop verdict with
  #     re-enqueue advice. In its own group the helper is signalled only by this
  #     script, so its cancelled context is what stops `gh`.
  #   - `kill -TERM` on the helper's PID reaches the helper and nothing else,
  #     which is what makes "let the helper decide a cancellation" enforceable.
  #
  # The launch is bracketed: `cmd &` and `child=$!` are two commands, and a
  # trap can run between them. A signal there is recorded instead of acted on,
  # because acting would exit 130 with the helper unsignalled.
  launching=true
  set -m
  env GOFLAGS= "$1" dev merge-enqueue \
    --repo "$repo" --pr "$pr" --wait "$wait" ${jump[@]+"${jump[@]}"} &
  set +m
  child=$!
  child_is_helper=true
  helper_started=true
  launching=false
  # A signal that arrived between the fork and the PID record was held by the
  # handler; the launch is now interruptible and complete, so act on it.
  if [ "$cancel_requested" = true ]; then
    interrupted
  fi
  local status=0
  wait "$child" || status=$?
  child=""
  child_is_helper=false
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

# build_helper DEST — build the helper into DEST, as a watched child so a
# signal interrupts the build instead of waiting for it. Returns the build's
# own status.
#
# Monitor mode is what makes the tracked PID the group leader, following
# run_bounded in scripts/lib/gate-bounded.sh rather than inventing a second
# pattern for the same problem. The previous form ran `setsid` *inside* a
# background subshell, so `$!` named the wrapper while the build led a group of
# its own: stop_command walked PIDs from the wrapper, and a compiler spawned
# after that walk's snapshot was reparented when `go build` exited, keeping the
# caller's output pipes open after the launcher returned 130.
#
# set -m is what puts the job in its own group; the subshell that leads it is
# the one that runs the build, so the group holds the build's whole tree and one
# TERM reaches all of it. `wait` then reaps the tracked child.
build_helper() {
  launching=true
  set -m
  (
    cd "$root" || exit 1
    env GOFLAGS= go build -o "$1" ./cmd/evener-dev/bin
  ) &
  set +m
  child=$!
  child_is_helper=false
  launching=false
  if [ "$cancel_requested" = true ]; then
    interrupted
  fi
  local status=0
  wait "$child" || status=$?
  child=""
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

# The libraries are loaded and the handlers installed before either launch
# path: the prebuilt path starts the helper too, and a handler installed after
# that start leaves a SIGTERM at this script's PID on its default action, which
# kills the launcher with 143 while the helper keeps polling and holding the
# caller's pipes. Installing a trap replaces the default action, so a handler
# that could not run would turn a TERM into a silent hang; every function it
# can call is therefore defined above this point, and the libraries are sourced
# before the traps so nothing in the handler is undefined. Sourcing has no side
# effects (scratch-lib.sh's own comment pins that), so the prebuilt path does no
# scratch setup and reclaims nothing.
. "$root/scripts/lib/scratch-lib.sh"
. "$root/scripts/lib/gate-scratch-root.sh"
. "$root/scripts/lib/gate-bounded.sh"
trap 'interrupted TERM' TERM
trap 'interrupted INT' INT
trap 'interrupted HUP' HUP
trap 'finish' EXIT

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
scratch_dir work merge-enqueue
gate_scratch_root_executes "$work" ||
  launcher_failed "scratch $work cannot execute a program (a noexec mount?); point TMPDIR at an exec-capable directory"
build_helper "$work/evener-dev" || launcher_failed "building the evener-dev helper failed"
[ -x "$work/evener-dev" ] || launcher_failed "the built helper is not executable: $work/evener-dev"
start_helper "$work/evener-dev" || exit $?
exit 0
