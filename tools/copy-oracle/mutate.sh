#!/usr/bin/env bash
# Mutation check for a converted test: break the behavior, confirm the tests fail.
#
# When converting a copy-oracle test (see inventory.py), the new assertion must
# still catch a real regression. This applies one perl substitution to a
# production file, runs the given test command, and passes only if the tests
# FAIL. The production file is always restored with `git restore`, so the file
# must have no uncommitted edits of its own.
#
# Usage:
#   tools/copy-oracle/mutate.sh <file> <perl-subst> -- <test command...>
#
# Example:
#   tools/copy-oracle/mutate.sh cmd/evener-hub/frontend/src/panes/welcome/Welcome.tsx \
#     's/hint=\{params.note\}//' -- \
#     bash -c 'cd cmd/evener-hub/frontend && npx vitest run src/panes/welcome'
#
# The test command runs twice: first unmutated, where it must pass
# (BASELINE-FAIL otherwise), then mutated. Only one mutate.sh may run per
# worktree at a time.
#
# Output: KILLED (tests failed, good; the failing tests are listed under it),
# SURVIVED (tests passed: the conversion lost coverage), or NO-OP (the
# substitution matched nothing). The full test log path is printed on SURVIVED
# and kept for every run under $TMPDIR/copy-oracle-mutate.*.
set -euo pipefail

if [[ $# -lt 4 || "$3" != "--" ]]; then
  awk 'NR > 1 && !/^#/ { exit } NR > 1 { sub(/^# ?/, ""); print }' "$0"
  exit 2
fi
file=$1 subst=$2
shift 3
root=$(git rev-parse --show-toplevel)
cd "$root"
# One mutation at a time per worktree: a second run would mutate files under
# the first run's tests, or restore them mid-run. The lock records its owner's
# pid, so a lock left by a run that was killed outright (SIGKILL) is recovered
# instead of blocking every later run.
lock="$(git rev-parse --absolute-git-dir)/copy-oracle-mutate.lock"
take_lock() { mkdir "$lock" 2>/dev/null && echo $$ >"$lock/pid"; }
if ! take_lock; then
  owner=$(cat "$lock/pid" 2>/dev/null || true)
  if [[ -n "$owner" ]] && kill -0 "$owner" 2>/dev/null; then
    echo "refusing: another mutate.sh (pid $owner) is running in this worktree" >&2
    exit 2
  fi
  echo "recovering stale lock from pid ${owner:-unknown}" >&2
  # mutate.sh never starts on a dirty file, so any edit to the dead run's
  # target is its unrestored mutation.
  stale_target=$(cat "$lock/target" 2>/dev/null || true)
  if [[ -n "$stale_target" ]]; then
    git restore -- "$stale_target"
    echo "restored $stale_target, left mutated by that run" >&2
  fi
  rm -f "$lock/pid" "$lock/target"
  rmdir "$lock" 2>/dev/null || true
  if ! take_lock; then
    echo "refusing: could not take $lock" >&2
    exit 2
  fi
fi

# Every exit path, including HUP/INT/TERM, stops the test process tree before
# restoring the file, so a stale run never reads the restored source.
mutated=0 child=""
kill_tree() {
  local kid
  for kid in $(pgrep -P "$1" 2>/dev/null); do kill_tree "$kid"; done
  kill -TERM "$1" 2>/dev/null || true
}
cleanup() {
  if [[ -n "$child" ]]; then
    kill_tree "$child"
    wait "$child" 2>/dev/null || true
  fi
  if ((mutated)); then git restore -- "$file"; fi
  rm -f "$lock/pid" "$lock/target"
  rmdir "$lock" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
# Checked after taking the lock, so a dead run's leftover mutation has
# already been restored.
if ! git ls-files --error-unmatch -- "$file" >/dev/null 2>&1; then
  echo "refusing: $file is not tracked by git, so it cannot be restored" >&2
  exit 2
fi
if ! git diff --quiet -- "$file"; then
  echo "refusing: $file has uncommitted edits; mutate.sh restores it with git restore" >&2
  exit 2
fi

# Run the tests as a tracked child so a signal can stop them.
run_tests() {
  local status=0
  "$@" >"$log" 2>&1 &
  child=$!
  wait "$child" || status=$?
  child=""
  return "$status"
}

# One log per run: parallel checks in other worktrees must not mix output.
log=$(mktemp "${TMPDIR:-/tmp}/copy-oracle-mutate.XXXXXX")
# The tests must pass unmutated, or a failure afterwards proves nothing: the
# runner may be missing, or the test may already be broken.
if ! run_tests "$@"; then
  echo "BASELINE-FAIL the test command fails without the mutation (log: $log)" >&2
  exit 2
fi
mutated=1
echo "$file" >"$lock/target"
perl -0pi -e "$subst" "$file"
if git diff --quiet -- "$file"; then
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
