#!/usr/bin/env bash
# Regression tests for mutate.sh's signal cleanup.
#
# Each case runs mutate.sh in a scratch git repo, sends it TERM while a test
# run is in flight, and checks that it exits 143 within a bounded time and
# leaves no test process and no throwaway worktree behind:
#
#   - before setpgrp: a copy of mutate.sh whose child pauses before setpgrp,
#     so the signal lands when the run's process group does not exist yet;
#   - TERM-ignoring run: a test that ignores TERM and leaves a worker that
#     ignores it too, so only the KILL escalation stops them.
#
# Usage: tools/copy-oracle/test_mutate.sh   (takes about 10 seconds; exit 0 = pass)
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd -P)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/test-mutate.XXXXXX")
pid=""
finish() {
  if [[ -n "$pid" ]]; then
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  pkill -KILL -f "$tmp" 2>/dev/null || true
  rm -rf "$tmp"
}
trap finish EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

repo="$tmp/repo"
git init -q "$repo"
echo x >"$repo/file.txt"
git -C "$repo" add file.txt
git -C "$repo" -c user.name=t -c user.email=t@t commit -qm init

# term_mid_run <name> <max-seconds> <mutate.sh> <test command...> runs mutate.sh
# with the test command, waits for the run to create $tmp/<name>.ready, sends
# TERM, and checks the outcome. Test processes carry $tmp/<name> in their
# arguments so leftovers can be found.
term_mid_run() {
  local name=$1 max=$2 script=$3 started status=0 deadline
  shift 3
  (cd "$repo" && TMPDIR="$tmp/$name" READY="$tmp/$name.ready" exec "$script" file.txt 's/x/y/' -- "$@") \
    >"$tmp/$name.out" 2>&1 &
  pid=$!
  deadline=$((SECONDS + 10))
  until [[ -e "$tmp/$name.ready" ]]; do
    ((SECONDS < deadline)) || fail "$name: the test run never started"
    sleep 0.05
  done
  kill -TERM "$pid"
  started=$SECONDS
  while kill -0 "$pid" 2>/dev/null; do
    ((SECONDS - started <= max)) || fail "$name: mutate.sh was still running ${max}s after TERM"
    sleep 0.1
  done
  wait "$pid" || status=$?
  pid=""
  ((status == 143)) || fail "$name: mutate.sh exited $status after TERM, want 143"
  if pgrep -f "$tmp/$name" >/dev/null; then
    fail "$name: a test process outlived mutate.sh"
  fi
  if compgen -G "$tmp/$name/copy-oracle-mutate.*/worktree" >/dev/null; then
    fail "$name: mutate.sh left its throwaway worktree behind"
  fi
  echo "PASS: $name (exit $status after $((SECONDS - started))s)"
}

# The instrumented child announces itself, then sleeps 2 seconds before setpgrp.
perl -pe 's/setpgrp\(0, 0\)/open(my \$m, ">", \$ENV{READY}); close \$m; select(undef, undef, undef, 2); setpgrp(0, 0)/' \
  "$here/mutate.sh" >"$tmp/mutate-slow-setpgrp.sh"
grep -q 'ENV{READY}' "$tmp/mutate-slow-setpgrp.sh" || fail "could not instrument mutate.sh's setpgrp call"
chmod +x "$tmp/mutate-slow-setpgrp.sh"

mkdir "$tmp/before-setpgrp" "$tmp/term-ignoring"
term_mid_run before-setpgrp 6 "$tmp/mutate-slow-setpgrp.sh" \
  perl -e 'sleep 20' "$tmp/before-setpgrp"
# shellcheck disable=SC2016 # the $ expressions are perl's
term_mid_run term-ignoring 12 "$here/mutate.sh" \
  perl -e '$SIG{TERM} = "IGNORE"; fork or do { sleep 20; exit }; open(my $m, ">", $ENV{READY}); close $m; sleep 20' \
  "$tmp/term-ignoring"
