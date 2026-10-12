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
if ! git ls-files --error-unmatch -- "$file" >/dev/null 2>&1; then
  echo "refusing: $file is not tracked by git, so it cannot be restored" >&2
  exit 2
fi
if ! git diff --quiet -- "$file"; then
  echo "refusing: $file has uncommitted edits; mutate.sh restores it with git restore" >&2
  exit 2
fi
# One mutation at a time per worktree: a second run would mutate files under
# the first run's tests, or restore them mid-run.
lock="$(git rev-parse --absolute-git-dir)/copy-oracle-mutate.lock"
if ! mkdir "$lock" 2>/dev/null; then
  echo "refusing: another mutate.sh is running in this worktree ($lock)" >&2
  exit 2
fi
trap 'rmdir "$lock"' EXIT
# One log per run: parallel checks in other worktrees must not mix output.
log=$(mktemp "${TMPDIR:-/tmp}/copy-oracle-mutate.XXXXXX")
# The tests must pass unmutated, or a failure afterwards proves nothing: the
# runner may be missing, or the test may already be broken.
if ! "$@" >"$log" 2>&1; then
  echo "BASELINE-FAIL the test command fails without the mutation (log: $log)" >&2
  exit 2
fi
trap 'git restore -- "$file"; rmdir "$lock"' EXIT
perl -0pi -e "$subst" "$file"
if git diff --quiet -- "$file"; then
  echo "NO-OP   $file: $subst"
  exit 1
fi
if "$@" >"$log" 2>&1; then
  echo "SURVIVED $file: $subst (log: $log)"
  exit 1
fi
echo "KILLED  $file: $subst"
# Show which tests caught it, so a kill for the wrong reason (a crash, a
# syntax error) is visible.
grep -E '^\s*(×|FAIL|--- FAIL)' "$log" | sed 's/^/    /' | head -5 || true
