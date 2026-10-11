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
# Output: KILLED (tests failed, good; the failing tests are listed under it),
# SURVIVED (tests passed: the conversion lost coverage), or NO-OP (the
# substitution matched nothing). The full test log is kept at $TMPDIR/copy-oracle-mutate.log.
set -euo pipefail

if [[ $# -lt 4 || "$3" != "--" ]]; then
  sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
fi
file=$1 subst=$2
shift 3
root=$(git rev-parse --show-toplevel)
cd "$root"
if ! git diff --quiet -- "$file"; then
  echo "refusing: $file has uncommitted edits; mutate.sh restores it with git restore" >&2
  exit 2
fi
log="${TMPDIR:-/tmp}/copy-oracle-mutate.log"
trap 'git restore -- "$file"' EXIT
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
