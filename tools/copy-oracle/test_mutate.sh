#!/usr/bin/env bash
# Regression tests for mutate.sh's signal cleanup.
#
# Each case runs mutate.sh in a scratch git repo, sends it TERM while a test
# run is in flight, and checks that it exits 143 and leaves no test process and
# no throwaway worktree behind:
#
#   - before setpgrp: a copy of mutate.sh whose child blocks on a FIFO nobody
#     writes, just before setpgrp, so the signal provably lands while the run's
#     process group does not exist yet;
#   - TERM-ignoring run: a test that ignores TERM and leaves a worker that
#     ignores it too, both blocked on that FIFO, so only KILL stops them;
#   - post-wait drain: once a run is reaped, mutate.sh signals only its
#     process group, never the bare pid, which may have been reused.
#
# The test run reports readiness through a FIFO, so TERM is sent only once the
# run is in the state under test. The exit ceiling is a tripwire for a hang.
#
# Usage: tools/copy-oracle/test_mutate.sh   (takes about 6 seconds; exit 0 = pass)
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

# term_mid_run <name> <mutate.sh> <test command...> runs mutate.sh with the test
# command, waits for the run to write a line to the FIFO $READY, sends TERM,
# and checks the outcome. Test processes carry $tmp/<name> in their arguments
# so leftovers can be found.
term_mid_run() {
  local name=$1 script=$2 ready="$tmp/$1.ready" started status=0
  shift 2
  mkdir "$tmp/$name"
  mkfifo "$ready"
  # Opened read-write so the run's open for writing never waits on us.
  exec 3<>"$ready"
  (cd "$repo" && TMPDIR="$tmp/$name" READY="$ready" GATE="$tmp/gate" exec "$script" file.txt 's/x/y/' -- "$@") \
    >"$tmp/$name.out" 2>&1 &
  pid=$!
  read -r -t 30 _ <&3 || fail "$name: the test run never reported ready"
  exec 3<&-
  kill -TERM "$pid"
  started=$SECONDS
  while kill -0 "$pid" 2>/dev/null; do
    ((SECONDS - started <= 30)) || fail "$name: mutate.sh was still running 30s after TERM"
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

# Nothing ever writes to the gate, so opening it blocks a process until a
# signal stops it.
mkfifo "$tmp/gate"
perl -pe 's/setpgrp\(0, 0\)/open(my \$r, ">", \$ENV{READY}); print \$r "ready\\n"; close \$r; open(my \$g, "<", \$ENV{GATE}); setpgrp(0, 0)/' \
  "$here/mutate.sh" >"$tmp/mutate-gated-setpgrp.sh"
grep -q 'ENV{GATE}' "$tmp/mutate-gated-setpgrp.sh" || fail "could not instrument mutate.sh's setpgrp call"
chmod +x "$tmp/mutate-gated-setpgrp.sh"

term_mid_run before-setpgrp "$tmp/mutate-gated-setpgrp.sh" \
  perl -e 'sleep 20' "$tmp/before-setpgrp"
# shellcheck disable=SC2016 # the $ expressions are perl's
term_mid_run term-ignoring "$here/mutate.sh" \
  perl -e '$SIG{TERM} = "IGNORE"; fork or do { open(my $g, "<", $ENV{GATE}); exit }; open(my $r, ">", $ENV{READY}); print $r "ready\n"; close $r; open(my $g, "<", $ENV{GATE})' \
  "$tmp/term-ignoring"

# After `wait` reaps a test run, its pid is free for reuse, so the drain that
# follows must signal only the process group, never the bare pid. A copy of
# mutate.sh records every target its stop helper is given.
perl -0pe 's/^stop\(\) \{\n/stop() {\n  echo "\$1" >>"\$STOP_LOG"\n/m' \
  "$here/mutate.sh" >"$tmp/mutate-recording-stop.sh"
grep -q STOP_LOG "$tmp/mutate-recording-stop.sh" || fail "could not instrument mutate.sh's stop helper"
chmod +x "$tmp/mutate-recording-stop.sh"
status=0
(cd "$repo" && TMPDIR="$tmp" STOP_LOG="$tmp/stops" "$tmp/mutate-recording-stop.sh" file.txt 's/x/y/' -- true) \
  >"$tmp/post-wait.out" 2>&1 || status=$?
((status == 1)) || fail "post-wait drain: mutate.sh exited $status, want 1 (SURVIVED); output: $(cat "$tmp/post-wait.out")"
if [[ -e "$tmp/stops" ]] && grep -qv '^-' "$tmp/stops"; then
  fail "post-wait drain: stop was given a bare pid after the run was reaped: $(tr '\n' ' ' <"$tmp/stops")"
fi
echo "PASS: post-wait drain signals only the process group"
