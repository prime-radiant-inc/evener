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
# Output: KILLED (tests failed, good; the failing tests are listed under it),
# SURVIVED (tests passed: the conversion lost coverage), or NO-OP (the
# substitution matched nothing). The full test log path is printed on SURVIVED
# and kept for every run under $TMPDIR/copy-oracle-mutate.*.log.
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
child=""

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
  rmdir "$scratch" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

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

# Run the tests from the throwaway root in their own process group (job
# control; the leader's pid is the group id), and drain the whole group
# afterwards so no worker from the baseline runs into the mutation.
run_tests() {
  local status=0
  set -m
  (cd "$work" && exec "$@") >"$log" 2>&1 &
  child=$!
  set +m
  wait "$child" || status=$?
  stop_group "$child"
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
echo "KILLED  $file: $subst"
# Show which tests caught it, so a kill for the wrong reason (a crash, a
# syntax error) is visible.
grep -E '^\s*(×|FAIL|--- FAIL)' "$log" | sed 's/^/    /' | head -5 || true
