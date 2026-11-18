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
# helper. SIGTERM always works.
#
# Ctrl-C in a terminal signals this script, and this script signals the helper:
# the helper runs in a process group of its own, so the tty's foreground group
# does not contain it and the terminal does not reach it directly. The launcher
# takes the interrupt and forwards cancellation as SIGTERM to the helper's PID,
# which lets the helper's own context stop its GitHub call and report the
# interrupt rather than a queue decision. A helper that does not act on SIGTERM
# within a couple of seconds has its tree stopped, the `gh` call it was waiting
# on included.
#
# The helper is built in a private scratch directory under TMPDIR or, when
# TMPDIR's entries can be replaced by another user and a safer root exists,
# under $XDG_RUNTIME_DIR or $HOME/.cache/evener-scratch. Where none of those is
# safe the run still proceeds under TMPDIR, and the scratch is verified unchanged
# just before the helper starts: a scratch that changed is exit 2. An ordinary
# host is unaffected -- 1777 /tmp and a per-user TMPDIR are used as before.
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

# The contract block above is lines 2-58; the rationale after it is for a
# reader of this file, not for --help. The range ends at the blank comment line
# after the exit-code paragraph, so --help cannot stop mid-sentence: it used to
# end at 50, one line before the sentence saying a started helper keeps its own
# status, which is the half of the contract a caller relies on when it sees 1.
# It moved to 58 when the scratch-isolation paragraph joined the contract, and
# TestMergeEnqueueLauncherHelpPrintsTheWholeContract pins the last line so the
# next addition cannot silently truncate it.
usage() { sed -n '2,58p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

# A value-taking option at the end of the line has no value: reading $2 there is
# an "unbound variable" abort under set -u, which exits 1 -- the code that means
# "the queue dropped your PR". Refuse it as usage instead.
need_value() {
  [ "$#" -ge 2 ] || { echo "$1 requires a value" >&2; usage; }
}

# A bad --wait is a usage error and must be one before anything expensive
# happens. It used to reach the helper, so a scratch mint and a full build stood
# between the operator and "that is not a number", and on a cold or noexec host
# a build failure reported itself instead of the typo. This test is not the
# parser -- cmd/evener-dev's owns that, including the overflow bound and reading
# leading zeros as decimal (#5304) -- it only refuses what is not a number at
# all, the way the pre-port launcher did.
#
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
case "$wait" in
  '' | *[!0-9]*) echo "--wait takes whole seconds, got: $wait" >&2; usage ;;
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

# scratch_root_is_replaceable DIR -- succeed when another local user could rename
# or replace DIR itself.
#
# A directory's mode protects what is inside it and says nothing about its entry
# in its parent: the parent's owner can rename or delete a directory whatever its
# mode is. That is the whole of the high finding on 7ef64c0d67. So the question
# is asked about DIR's own bits, which are the parent's rule applied to it:
#
#   - group- or other-write lets someone else create, rename and delete entries;
#   - the sticky bit (1777 /tmp) takes rename and delete away from everyone but
#     the entry's owner and the directory's owner.
#
# Replaceable, then, means group- or other-writable AND not sticky. 0755 is safe;
# 1777 is safe; macOS's per-user /var/folders/.../T and systemd's
# $XDG_RUNTIME_DIR are safe; a hand-made 0777 scratch root is not.
#
# `find -perm` applies the mode the kernel does and reads the same on GNU and BSD
# find, which is why it is used instead of stat: stat's format differs between
# them.
scratch_root_is_replaceable() {
  local dir="$1" writable
  [ -d "$dir" ] || return 1
  writable=$(find "$dir" -maxdepth 0 \( -perm -0020 -o -perm -0002 \) -print 2>/dev/null) || writable=""
  [ -n "$writable" ] || return 1
  [ -k "$dir" ] && return 1
  return 0
}

# The scratch root, chosen before anything is minted under it.
#
# The launcher builds the helper and then runs it from a scratch directory.
# `mktemp -d` gives that directory 0700, which protects its contents and says
# nothing about its entry: on a TMPDIR that is group- or other-writable and not
# sticky, an attacker replaces the whole directory after the build, and the `-x`
# test and the exec both resolve the attacker's file. The launcher then runs
# another user's binary with the operator's credentials and its `gh` token.
#
# Moving TMPDIR itself, here and once, is what fixes that for every path at the
# same time: the build scratch, the go build's own work directory and the
# group-file directory all descend from it. Guarding one of them would have left
# the others open.
#
# An ordinary host takes no branch: 1777 /tmp has the sticky bit and a per-user
# TMPDIR is not other-writable, so nothing moves and nothing changes. Only a
# genuinely shared, non-sticky TMPDIR moves, and only to a root that is not
# replaceable either -- $XDG_RUNTIME_DIR first, then a private cache directory.
#
# When no safe root exists the run continues under the original TMPDIR. A merge
# tool that refused to run on a host it could not prove isolated would be worse
# for every caller than the window it leaves, and this is the launcher the whole
# campaign merges through. The inode check before the exec is the second lock in
# that shape, and --help names what remains.
if scratch_root_is_replaceable "${TMPDIR:-/tmp}"; then
  for scratch_candidate in "${XDG_RUNTIME_DIR:-}" "${HOME:+$HOME/.cache/evener-scratch}"; do
    [ -n "$scratch_candidate" ] || continue
    case "$scratch_candidate" in
      /*) ;;
      *) continue ;;
    esac
    if [ ! -d "$scratch_candidate" ]; then
      # 0700, so only this user can enter it. An existing cache directory is used
      # as its owner left it.
      (umask 077 && mkdir -p "$scratch_candidate") 2>/dev/null || true
    fi
    [ -d "$scratch_candidate" ] && [ -w "$scratch_candidate" ] || continue
    scratch_root_is_replaceable "$scratch_candidate" && continue
    TMPDIR="$scratch_candidate"
    export TMPDIR
    break
  done
fi

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
# The forced stop names the helper's `gh` group as well as the helper's own:
# the client puts `gh` in a group of its own (so one signal reaches the CLI and
# anything a wrapper forked), which is exactly why a group signal at the helper
# does not reach it. Without that, a helper that never ran its handler would let
# the launcher report 130 while the enqueue call was still in flight.
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
# The file the helper publishes its live `gh` process group in. Empty until a
# helper is about to be started, and read only by the forced stop: it is the one
# handle that outlives a `gh` wrapper which starts the real CLI and exits.
gh_group_file=""
# The private directory the handle lives in on the prebuilt path, removed with the
# handle: it exists only for the duration of one launch.
gh_private_dir=""
# The scratch directory the helper was built in, or empty on the prebuilt path.
#
# Declared and assigned here, never read from the environment. scratch_dir writes
# this variable in this process, so no export is needed -- and an inherited one
# would be worse than useless: the prebuilt path would take a caller's directory
# for scratch, truncate any gh-group file in it at launch and remove it at exit.
# That is data loss in a directory this tool never created. The scratch the
# handle lives in is passed to the mint explicitly instead, so no code path has
# to trust a value it did not set.
work=""
# The inode of the scratch directory, read at mint time and checked again just
# before the exec. Only meaningful on a host with no root whose entries other
# users cannot replace; see the launch path.
work_inode=""
# The process group the launched helper leads, recorded while it is still the
# launcher's child. A wrapper that starts the Go helper and exits is the tracked
# child: waiting on that PID says nothing about the group, and by the time the
# wrapper is reaped the group has no parent left to walk from.
helper_group=""
# How long a TERMed helper gets to act on the signal before its tree is stopped
# by force. The override exists for the same reason gate-bounded.sh has one for
# its own grace (EVENER_STOP_TREE_GRACE_SECONDS): a test that has to let the
# grace expire can shrink it, instead of spending the whole ceiling on it.
helper_term_grace_seconds=${EVENER_HELPER_TERM_GRACE_SECONDS:-2}
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
# helper_call_groups <pid> <helper-group> — print each process group the
# helper's GitHub call may be in, once, other than the helper's own and this
# script's. helper-group may be empty when ps cannot name it any more; the
# recorded one is used then.
#
# Two sources, because neither covers the other. The helper publishes the group
# of the call it is waiting on (internal/devtool/mergeq's ghGroupFileEnv), which
# is the only handle that survives a `gh` wrapper starting the real CLI without
# `exec` and then exiting: that CLI is reparented out of the helper's tree, so a
# descendant walk cannot see it, and it is exactly the process still submitting.
# The walk adds whatever a helper too old to publish left behind, and any call
# forked since the last publish.
#
# The walk is every group a descendant is in, not just groups a descendant
# leads, for the same wrapper reason. The helper's own group and this script's
# are excluded: stop_command signals the first, and the second must never be
# signalled.
helper_call_groups() {
  local pid="$1" own_group call_group descendant group published
  own_group=$(ps -o pgid= -p "$$" 2>/dev/null | tr -d ' ')
  call_group="$2"
  {
    if [ -n "${gh_group_file:-}" ] && [ -r "$gh_group_file" ]; then
      published=$(tr -d ' \n' < "$gh_group_file" 2>/dev/null) || published=""
      case "$published" in
        '' | *[!0-9]*) ;;
        *)
          if [ "$published" -gt 1 ] && [ "$published" != "$call_group" ] && [ "$published" != "$own_group" ]; then
            printf '%s\n' "$published"
          fi
          ;;
      esac
    fi
    for descendant in $(process_descendants "$pid"); do
      group=$(ps -o pgid= -p "$descendant" 2>/dev/null | tr -d ' ')
      case "$group" in
        '' | *[!0-9]*) continue ;;
      esac
      [ "$group" -gt 1 ] || continue
      [ "$group" = "$call_group" ] && continue
      [ "$group" = "$own_group" ] && continue
      printf '%s\n' "$group"
    done
  } | sort -u
}
# stop_helper_tree <pid> — the forced stop for a helper that did not act on
# TERM. stop_command alone stops the helper's own group, and the helper's `gh`
# is not in it: a live enqueue call would outlive a launcher that already
# reported 130. So the `gh` groups are collected first, while the helper still
# holds them in its tree, TERMed with the helper, and KILLed after stop_command's
# grace whether or not the helper died. A group id survives its leader's death,
# so a call reparented mid-stop is still reachable.
#
# `pid` may already be gone. A wrapper started through EVENER_DEV_BIN that runs
# the Go helper and exits on TERM leaves the helper running in the group the
# launcher recorded at launch; the child's exit is not evidence about that group,
# so the sweep below runs on the recorded group whether or not the child was
# still there when this was called.
stop_helper_tree() {
  local pid="$1" call_group="$2" groups="" group deadline alive own_group
  own_group=$(ps -o pgid= -p "$$" 2>/dev/null | tr -d ' ')
  # Freeze before taking the census. A helper that did not act on TERM is still
  # free to fork, and free to start its GitHub call at any moment: an inventory
  # taken while it runs is a census of a moving population, so a call forked
  # after the read and before the kill appears in no list and keeps submitting
  # after the launcher has returned 130.
  #
  # SIGSTOP cannot be ignored, deferred or handled, so the tree below cannot
  # grow while it is read. Two targets, because the two shapes need different
  # ones:
  #
  #   - the recorded helper group, which still holds the helper when an
  #     EVENER_DEV_BIN wrapper started it and then exited on TERM. The tracked
  #     pid is gone in that shape, so a freeze by pid would freeze nothing.
  #   - the tracked pid itself, for a helper still running as a child whose group
  #     ps could not name at launch.
  #
  # This script's own group is never frozen: it holds the launcher, and SIGSTOP
  # there would hang the run it is trying to stop. The call groups are not frozen
  # either -- stopping `gh` first would leave the helper waiting on a client that
  # cannot finish, and the census is complete without it once the process that
  # forks is stopped.
  if [ -n "$call_group" ] && [ "$call_group" -gt 1 ] 2>/dev/null && [ "$call_group" != "$own_group" ]; then
    kill -STOP -"$call_group" 2>/dev/null || :
  fi
  if [ "$pid" -gt 1 ] 2>/dev/null && [ "$pid" != "$$" ] && child_running "$pid"; then
    kill -STOP "$pid" 2>/dev/null || :
  fi
  # The census, on a tree that cannot fork. helper_call_groups reads the published
  # handle as well as walking descendants, so a call started between the stop and
  # this read is still named.
  groups=$(helper_call_groups "$pid" "$call_group") || :
  if [ -n "$call_group" ] && [ "$call_group" -gt 1 ] 2>/dev/null && [ "$call_group" != "$own_group" ]; then
    groups=$(printf '%s\n%s\n' "$groups" "$call_group" | sed '/^$/d' | sort -u)
  fi
  # TERM first, so a `gh` that can finish its request does. Nothing new can start
  # while the helper is frozen, which is what makes this list final.
  for group in $groups; do
    kill -TERM -"$group" 2>/dev/null || :
  done
  deadline=$((SECONDS + helper_term_grace_seconds))
  while [ "$SECONDS" -lt "$deadline" ]; do
    alive=false
    for group in $groups; do
      kill -0 -"$group" 2>/dev/null && alive=true
    done
    [ "$alive" = false ] && break
    sleep 0.05
  done
  # Then KILL every group in the census, then the helper's own group. The helper
  # stays frozen throughout: it is killed, not resumed, because a resumed helper
  # is one that can fork again.
  for group in $groups; do
    kill -KILL -"$group" 2>/dev/null || :
  done
  if [ -n "$call_group" ] && [ "$call_group" -gt 1 ] 2>/dev/null && [ "$call_group" != "$own_group" ]; then
    kill -KILL -"$call_group" 2>/dev/null || :
  fi
  # The tracked pid last, through stop_command rather than `kill -KILL -pid`:
  # a negative signal is a group id, and a pid names its own group only when it
  # leads one. stop_command checks that before signalling a group and walks the
  # tree otherwise, which is the only safe form for a pid whose group this script
  # could not name at launch.
  if [ "$pid" -gt 1 ] 2>/dev/null && [ "$pid" != "$$" ] && child_running "$pid"; then
    stop_command "$pid"
  fi
  # Nothing may still be submitting when this returns 130. KILL cannot be
  # ignored, so this is bounded by how long the kernel takes to deliver it; a
  # wait rather than a bare assumption is what makes the claim in --help true.
  deadline=$((SECONDS + helper_term_grace_seconds + 1))
  while [ "$SECONDS" -lt "$deadline" ]; do
    alive=false
    for group in $groups; do
      kill -0 -"$group" 2>/dev/null && alive=true
    done
    if [ -n "$call_group" ] && [ "$call_group" -gt 1 ] 2>/dev/null && kill -0 -"$call_group" 2>/dev/null; then
      alive=true
    fi
    [ "$alive" = false ] && break
    sleep 0.05
  done
}
reap_child() {
  [ -n "$child" ] || return 0
  if [ "$child_is_helper" != true ]; then
    # The whole tree, not the PID: `go build` forks a compiler and a linker, and
    # signalling only the shell that started it leaves those children holding the
    # caller's output pipes after this script has exited. stop_command sends one
    # TERM then one KILL to the group when the child leads its own, and walks the
    # tree when it does not; it never waits past its grace.
    stop_command "$child"
    wait "$child" 2>/dev/null || true
    return 0
  fi
  # The polite stop first: TERM at the helper's PID alone, so its own handler
  # cancels the context it started `gh` under and reports the interrupt itself.
  if kill -TERM "$child" 2>/dev/null; then
    local deadline=$((SECONDS + helper_term_grace_seconds))
    while [ "$SECONDS" -lt "$deadline" ] && child_running "$child"; do
      sleep 0.1
    done
  fi
  # The forced stop runs whether or not the child is still there, and whether or
  # not the TERM could be delivered. A child that is gone is not evidence that
  # the helper is gone: a wrapper started through EVENER_DEV_BIN exits on TERM
  # without forwarding it, leaving the Go helper polling in the group recorded at
  # launch. Waiting on the wrapper's PID to decide that would report 130 over a
  # live enqueue. A child still there after the grace did not act on TERM either,
  # so its GitHub call will not be cancelled by the context.
  stop_helper_tree "$child" "$helper_group"
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
  # The handle names a process group, and a group id is reused: leaving one
  # behind for a later launcher to read would point at some unrelated job. It is
  # removed before the scratch, which is the directory it may live in.
  if [ -n "$gh_group_file" ]; then
    rm -f "$gh_group_file" 2>/dev/null || true
  fi
  # The prebuilt path's private directory holds only that file, so it goes with
  # it. The build path's handle lives in scratch, which scratch_rm reclaims.
  if [ -n "$gh_private_dir" ]; then
    rmdir "$gh_private_dir" 2>/dev/null || true
  fi
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

# mint_gh_group_file — create the file the helper publishes its live `gh` process
# group in, and name it in gh_group_file. Both launch paths need it: a prebuilt
# helper calls `gh` exactly as a built one does, and the forced stop is the same
# code.
#
# It is created inside a private directory, never at a name an attacker can
# predict. The first form opened "$TMPDIR/merge-enqueue-gh-group-$$" on the
# prebuilt path: on a shared TMPDIR that permits symlink traversal, another local
# user can pre-create that name as a link to a file the operator can write, and
# the launcher would truncate it. $$ is predictable, and the file is opened
# before anything can check what it is.
#
# The build path already has private scratch (scratch_dir's `mktemp -d`), so it
# uses that, passed in as this function's argument. The prebuilt path has none,
# and minting scratch there would mean the launcher owns a directory it must then
# reclaim for a run that may only start and wait -- so it gets a private
# directory of its own, created by `mktemp -d` under the scratch root chosen
# above, and named in gh_private_dir for the EXIT trap to remove.
#
# The scratch is an ARGUMENT, never read from the environment. An inherited
# `work` used to make the prebuilt path treat a caller's directory as scratch: a
# gh-group file already there was truncated at launch and removed at exit. That
# is data loss in a directory this tool never created, and the fix is that no
# code path trusts a variable it did not set.
#
# A private directory that cannot be created leaves the handle empty rather than
# failing the run. The prebuilt path is the one an operator uses to point at a
# helper they built themselves, and the handle only widens what a forced stop can
# reach: the descendant walk still covers a helper with a call in its tree. A
# hardening change must not become the reason that path cannot start.
mint_gh_group_file() {
  local scratch="${1:-}" dir
  if [ -n "$scratch" ]; then
    gh_group_file="$scratch/gh-group"
    : > "$gh_group_file" 2>/dev/null || gh_group_file=""
    return 0
  fi
  dir=$(mktemp -d "${TMPDIR:-/tmp}/merge-enqueue.XXXXXX" 2>/dev/null) || dir=""
  case "$dir" in
    /*) ;;
    *) dir="" ;;
  esac
  if [ -z "$dir" ]; then
    gh_group_file=""
    return 0
  fi
  gh_private_dir="$dir"
  gh_group_file="$dir/gh-group"
  : > "$gh_group_file" 2>/dev/null || gh_group_file=""
  return 0
}

# start_helper BIN — run the helper as a child, record its PID for the signal
# handlers, and wait for it. Returns the helper's own status, which is the
# contract's whenever the helper started.
start_helper() {
  local helper_scratch="${2:-}"
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
  #
  mint_gh_group_file "$helper_scratch"
  launching=true
  set -m
  env GOFLAGS= EVENER_MERGEQ_GH_GROUP_FILE="${gh_group_file:-}" "$1" dev merge-enqueue \
    --repo "$repo" --pr "$pr" --wait "$wait" ${jump[@]+"${jump[@]}"} &
  set +m
  child=$!
  child_is_helper=true
  helper_started=true
  # The group the launched child leads, read while it is certainly alive. With
  # set -m it is the child's own pid, and recording it here is what lets the
  # forced stop find the Go helper after a wrapper child has exited: `ps` on a
  # reaped pid names nothing, and the group outlives the process that led it.
  helper_group=$(ps -o pgid= -p "$child" 2>/dev/null | tr -d ' ') || helper_group=""
  case "$helper_group" in
    '' | *[!0-9]*) helper_group="" ;;
  esac
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
# A source that fails would otherwise end the script under `set -e` with the
# library's own status -- 1, "the queue dropped your PR" -- for a launcher that
# never asked GitHub anything. Each one is checked first and named, so an
# unreadable library is the launcher failure it is.
source_library() {
  [ -r "$1" ] || launcher_failed "cannot read the library $1"
  # shellcheck source=/dev/null
  . "$1" || launcher_failed "sourcing $1 failed"
}
source_library "$root/scripts/lib/scratch-lib.sh"
source_library "$root/scripts/lib/gate-scratch-root.sh"
source_library "$root/scripts/lib/gate-bounded.sh"
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
# The inode of the directory scratch_dir minted, read once here and compared
# again immediately before the exec. This is the second lock on the high finding
# and it is only load-bearing on a host with no root whose entries another user
# cannot replace: there the entry can be swapped between the build and the
# launch. A swap then costs exit 2 instead of the operator's credentials.
work_inode=$(ls -di "$work" 2>/dev/null | awk '{print $1}') || work_inode=""
gate_scratch_root_executes "$work" ||
  launcher_failed "scratch $work cannot execute a program (a noexec mount?); point TMPDIR at an exec-capable directory"
build_helper "$work/evener-dev" || launcher_failed "building the evener-dev helper failed"
# The entry is checked here, after the build and immediately before the exec,
# because the build is the window: it takes seconds, and an attacker watching
# for it only has to win once. bash cannot exec by file descriptor, so this is
# the closest the launcher can get to "the directory I checked is the directory
# I run from".
#
# What remains, honestly: a few shell builtins between this test and the exec in
# start_helper, and only on a host where no root protects its entries. On every
# ordinary host -- 1777 /tmp, a per-user TMPDIR, a private HOME -- the root
# chosen above already makes the swap impossible and this check cannot fail.
work_now=$(ls -di "$work" 2>/dev/null | awk '{print $1}') || work_now=""
if [ -n "$work_inode" ] && [ "$work_now" != "$work_inode" ]; then
  launcher_failed "scratch $work was replaced by another user after the build; refusing to run a helper from it"
fi
[ -x "$work/evener-dev" ] || launcher_failed "the built helper is not executable: $work/evener-dev"
start_helper "$work/evener-dev" "$work" || exit $?
exit 0
