#!/usr/bin/env bash
# Mutation check for a converted test: break the behavior, confirm the tests fail.
#
# When converting a copy-oracle test (see inventory.py), the new assertion must
# still catch a real regression. This applies one perl substitution to a
# production file, runs the given test command, and passes only if the tests
# FAIL.
#
# Usage:
#   tools/copy-oracle/mutate.sh <file> <perl-subst> -- <test command...>
#
# Example:
#   tools/copy-oracle/mutate.sh cmd/evener-hub/frontend/src/panes/welcome/Welcome.tsx \
#     's/hint=\{params.note\}//' -- \
#     bash -c 'cd cmd/evener-hub/frontend && npx vitest run src/panes/welcome'
#
# Everything runs in a throwaway git worktree under $TMPDIR, never in yours: it
# starts from HEAD plus your uncommitted tracked changes and untracked files,
# with every node_modules linked from your tree. Your files are never edited,
# so runs can overlap and a run that dies leaves nothing of yours changed. The
# test command runs from the throwaway root: first unmutated, where it must
# pass (BASELINE-FAIL otherwise), then mutated.
#
# Output: KILLED (a named test failed, its setup included; the failing tests
# are listed under it, exit 0), SURVIVED (tests passed: the conversion lost coverage,
# exit 1), NO-OP (the substitution matched nothing, exit 1), or BROKEN (the
# mutated code did not build or no test ran, so it proves nothing; exit 3).
# The full test log path is printed on SURVIVED and BROKEN, and kept for every
# run under $TMPDIR/copy-oracle-mutate.*.log.
#
# A run killed outright (SIGKILL) leaves its $TMPDIR/copy-oracle-mutate.*
# directory; delete it and run `git worktree prune`.
set -euo pipefail

if [[ $# -lt 4 || "$3" != "--" ]]; then
  awk 'NR > 1 && !/^#/ { exit } NR > 1 { sub(/^# ?/, ""); print }' "$0"
  exit 2
fi
file=$1 subst=$2
shift 3
root=$(git rev-parse --show-toplevel)
cd "$root"
if ! git ls-files --error-unmatch -- "$file" >/dev/null 2>&1; then
  echo "refusing: $file is not tracked by git" >&2
  exit 2
fi

scratch=$(mktemp -d "${TMPDIR:-/tmp}/copy-oracle-mutate.XXXXXX")
work="$scratch/worktree"
log="$scratch.log"
launch_pipe="$scratch/launch"
mkfifo "$launch_pipe"
# child is the pid, and so the process group id, of a launched test run.
# launching is set while a run is between fork and its setpgrp, when a signal
# has no group to stop yet; pending_signal holds such a signal's exit status.
child=""
launching=""
pending_signal=""

# stop_group <pgid> stops every process in a test run's process group: TERM,
# up to 5 seconds' grace, then KILL, then up to 5 more seconds for the kernel
# to reap them. It keys on the group, so workers that outlive their runner are
# stopped too, and it is bounded: a process that will not die is reported and
# left behind in the throwaway tree.
stop_group() {
  local pgid=$1 deadline
  kill -0 -"$pgid" 2>/dev/null || return 0
  kill -TERM -"$pgid" 2>/dev/null || true
  deadline=$((SECONDS + 5))
  while ((SECONDS < deadline)) && kill -0 -"$pgid" 2>/dev/null; do sleep 0.1; done
  kill -KILL -"$pgid" 2>/dev/null || true
  deadline=$((SECONDS + 5))
  while ((SECONDS < deadline)) && kill -0 -"$pgid" 2>/dev/null; do sleep 0.1; done
  if kill -0 -"$pgid" 2>/dev/null; then
    echo "warning: test process group $pgid did not exit after KILL" >&2
  fi
}

cleanup() {
  if [[ -n "$child" ]]; then
    stop_group "$child"
    wait "$child" 2>/dev/null || true
  fi
  git worktree remove --force "$work" 2>/dev/null || true
  rm -f "$launch_pipe"
  rmdir "$scratch" 2>/dev/null || true
}

# on_signal <status> exits with status, through cleanup. During a launch it
# only records the signal: run_tests honours it once the run's process group
# exists, so cleanup can stop the whole group.
on_signal() {
  if [[ -n "$launching" ]]; then
    pending_signal=$1
    return
  fi
  exit "$1"
}
trap cleanup EXIT
trap 'on_signal 129' HUP
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

# Build the throwaway tree: HEAD, then the caller's uncommitted tracked
# changes and untracked (not ignored) files, so a test edited but not yet
# committed is the one that runs.
git worktree add -q --detach "$work" HEAD
if ! git diff --quiet HEAD; then
  git diff --binary HEAD | git -C "$work" apply --binary --whitespace=nowarn
fi
# Dependency installs are linked below, never copied: an untracked
# node_modules symlink with a relative target would dangle here.
git ls-files -z --others --exclude-standard |
  perl -0ne 'print unless m{(?:^|/)node_modules(?:/|\0|\z)}' |
  tar -cf - --null -T - | tar -xf - -C "$work"
# Link each JavaScript package's installed dependencies at their canonical
# path. When the caller's node_modules is itself a link to a shared install,
# the preflights then compare the scratch lockfile with that install's own
# lockfile, not with the caller's identical copy.
while IFS= read -r manifest; do
  pkg=$(dirname "$manifest")
  if [[ -d "$pkg/node_modules" ]]; then
    ln -s "$(cd "$pkg/node_modules" && pwd -P)" "$work/$pkg/node_modules"
  fi
done < <(git ls-files -- '*package.json' 'package.json')

# Run the tests from the throwaway root in their own process group, and drain
# the whole group afterwards so no worker from the baseline runs into the
# mutation. perl makes the process its own group leader (setpgrp) and then
# execs the command in place, so the group id is the pid in $!; bash job
# control (set -m) races the subshell's exec and can leave it in ours.
#
# The launcher acknowledges on the launch pipe (fd 4 in the child) once setpgrp
# has run, and only then is the run registered in child. A signal before that
# is deferred until registration, so cleanup only ever signals a process group,
# never a bare pid that may already belong to another process. Holding fd 4
# open read-write until the launcher has it keeps opening the FIFO from
# blocking; closing it afterwards turns a launcher that dies early into EOF.
# The acknowledgement is one byte, so a read a signal interrupts has either
# consumed it or not: there is no partial acknowledgement to lose on retry.
run_tests() {
  local status=0 pid ack=""
  launching=1
  exec 4<>"$launch_pipe"
  exec 3<"$launch_pipe"
  (exec 3<&- && cd "$work" && exec perl -e 'setpgrp(0, 0) or die "setpgrp: $!\n"; open(my $launch, ">&=", 4) or die "launch pipe: $!\n"; print $launch "L"; close $launch; exec { $ARGV[0] } @ARGV or die "exec $ARGV[0]: $!\n"' "$@") >"$log" 2>&1 &
  pid=$!
  exec 4>&-
  # If a deferred signal interrupts read (status above 128), read again.
  until read -r -n 1 ack <&3; do (($? > 128)) || break; done
  exec 3<&-
  if [[ "$ack" == L ]]; then
    child=$pid
  else
    echo "launch failed: the test launcher exited before forming its process group (log: $log)" >&2
  fi
  launching=""
  if [[ -n "$pending_signal" ]]; then
    exit "$pending_signal"
  fi
  wait "$pid" || status=$?
  stop_group "$pid"
  child=""
  return "$status"
}

# The tests must pass unmutated, or a failure afterwards proves nothing: the
# runner may be missing, or the test may already be broken.
if ! run_tests "$@"; then
  echo "BASELINE-FAIL the test command fails without the mutation (log: $log)" >&2
  exit 2
fi
before=$(git hash-object -- "$work/$file")
perl -0pi -e "$subst" "$work/$file"
if [[ "$(git hash-object -- "$work/$file")" == "$before" ]]; then
  echo "NO-OP   $file: $subst"
  exit 1
fi
if run_tests "$@"; then
  echo "SURVIVED $file: $subst (log: $log)"
  exit 1
fi
# A failing run counts as KILLED only when a named test failed: a vitest
# failed-test line (`×`, or `FAIL file > test`) or Go's `--- FAIL:`. That
# includes a test whose own setup (beforeEach, a t.Cleanup) fails under the
# mutation: the test fails, which is the guarantee a converted test owes. The
# failing tests are listed so a reviewer can see which ones. A compile or
# transform error fails the run before any named test, so it proves nothing.
failed_tests=$(grep -E '^[[:space:]]*(×|--- FAIL:)|FAIL[[:space:]].* > ' "$log" || true)
if [[ -z "$failed_tests" ]]; then
  echo "BROKEN  $file: $subst (the mutated code did not build or no test ran; log: $log)"
  tail -n 15 "$log" | sed 's/^/    /'
  exit 3
fi
echo "KILLED  $file: $subst"
# Show which tests caught it.
printf '%s\n' "$failed_tests" | sed -n '1,5s/^/    /p'
