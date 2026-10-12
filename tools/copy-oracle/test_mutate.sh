#!/usr/bin/env bash
# shellcheck disable=SC2016 # single-quoted $ expressions here are perl's
# Regression tests for mutate.sh's signal cleanup.
#
# Each case runs an instrumented copy of mutate.sh in a scratch git repo. The
# copy reports through hooks on a FIFO, so the harness acts only once the run
# is in the state under test, and it records every kill it issues:
#
#   - deferred launch: TERM lands while a test run is between fork and its
#     setpgrp. mutate.sh must defer it, register the run once its process
#     group exists, then exit 143 and stop the group;
#   - interrupted acknowledgement: the same, with TERM landing while the
#     parent is blocked reading the launcher's acknowledgement;
#   - launch failure: the launcher dies before setpgrp, and mutate.sh reports
#     a failed baseline with nothing left to stop;
#   - TERM-ignoring run: TERM lands after registration, on a test that ignores
#     TERM and leaves a worker that ignores it too, both blocked on a FIFO
#     nobody writes, so only KILL stops them. It runs twice, once with the
#     parent's registration held until the test is ready;
#   - completed runs: a run that ends on its own is drained without incident.
#
# Every case checks the exit status, that no test process and no throwaway
# worktree is left, and that mutate.sh never signalled a bare pid: only
# process groups, since a pid may have been reused by an unrelated process.
# There are no sleeps for synchronization; the 30s ceilings are tripwires
# for a hang.
#
# Usage: tools/copy-oracle/test_mutate.sh   (takes about 11 seconds; exit 0 = pass)
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

# Nothing ever writes to $tmp/never, so opening it blocks until a signal.
mkfifo "$tmp/never" "$tmp/gate" "$tmp/ackgate"

# Instrument a copy of mutate.sh. Each hook is idle unless its variable is set:
#   KILL_LOG   every kill's arguments are appended to it;
#   LAUNCH_DIE the launcher exits before setpgrp;
#   GATE       the launcher announces "forked" on $HOOKS, then blocks opening
#              this FIFO, before setpgrp;
#   ACK_GATE   the launcher announces "acking" on $HOOKS, then blocks opening
#              this FIFO, before it writes its acknowledgement;
#   HOOKS      the parent announces "deferred" when it defers a signal, and
#              "registered" once it has recorded the launched run;
#   REGATE     the parent blocks opening this FIFO before it announces
#              "registered", so a test can hold registration back.
instrumented="$tmp/mutate-instrumented.sh"
perl -0pe '
  s/^(set -euo pipefail\n)/$1kill() { [[ -z "\${KILL_LOG:-}" ]] || echo "\$*" >>"\$KILL_LOG"; builtin kill "\$@"; }\n/m or die "no set line\n";
  s/setpgrp\(0, 0\)/exit 3 if \$ENV{LAUNCH_DIE}; if (\$ENV{GATE}) { open(my \$h, ">", \$ENV{HOOKS}); print \$h "forked\\n"; close \$h; open(my \$g, "<", \$ENV{GATE}) } setpgrp(0, 0)/ or die "no setpgrp\n";
  s/print \$launch/if (\$ENV{ACK_GATE}) { open(my \$h, ">", \$ENV{HOOKS}); print \$h "acking\\n"; close \$h; open(my \$g, "<", \$ENV{ACK_GATE}) } print \$launch/ or die "no acknowledgement\n";
  s/^(\s*)(pending_signal=\$1\n)/$1$2$1\[[ -z "\${HOOKS:-}" ]] || echo deferred >"\$HOOKS"\n/m or die "no deferral\n";
  s/^(\s*)(child=\$pid\n)/$1$2$1\[[ -z "\${REGATE:-}" ]] || : <"\$REGATE"\n$1\[[ -z "\${HOOKS:-}" ]] || echo registered >"\$HOOKS"\n/m or die "no registration\n";
' "$here/mutate.sh" >"$instrumented" || fail "could not instrument mutate.sh"
chmod +x "$instrumented"

# start <name> <test command...> starts the instrumented mutate.sh in the
# background with $tmp/<name>/hooks open read-write on fd 3, so hooks never
# block. Test processes carry $tmp/<name> in their arguments so leftovers can
# be found.
start() {
  local name=$1
  shift
  mkdir "$tmp/$name"
  mkfifo "$tmp/$name/hooks"
  exec 3<>"$tmp/$name/hooks"
  (cd "$repo" && TMPDIR="$tmp/$name" HOOKS="$tmp/$name/hooks" KILL_LOG="$tmp/$name/kills" \
    exec "$instrumented" file.txt 's/x/y/' -- "$@") >"$tmp/$name/out" 2>&1 &
  pid=$!
}

# expect_hooks <name> <word...> waits until each word has been announced, in
# any order: announcements from separate processes are not ordered.
expect_hooks() {
  local name=$1 line="" left deadline=$((SECONDS + 30))
  shift
  left=" $* "
  while [[ "$left" != " " ]]; do
    read -r -t $((deadline > SECONDS ? deadline - SECONDS : 1)) line <&3 ||
      fail "$name: no hook announcement within 30s, still want${left% }"
    [[ "$left" == *" $line "* ]] || fail "$name: hook announced '$line', want one of${left% }"
    left=${left/ $line / }
  done
}

# finish_case <name> <status> waits for mutate.sh and checks what it left.
finish_case() {
  local name=$1 want=$2 status=0 started=$SECONDS
  while kill -0 "$pid" 2>/dev/null; do
    ((SECONDS - started <= 30)) || fail "$name: mutate.sh was still running after 30s"
    sleep 0.1
  done
  wait "$pid" || status=$?
  pid=""
  exec 3<&-
  ((status == want)) || fail "$name: mutate.sh exited $status, want $want: $(cat "$tmp/$name/out")"
  if pgrep -f "$tmp/$name" >/dev/null; then
    fail "$name: a test process outlived mutate.sh"
  fi
  if compgen -G "$tmp/$name/copy-oracle-mutate.*/worktree" >/dev/null; then
    fail "$name: mutate.sh left its throwaway worktree behind"
  fi
  if [[ -e "$tmp/$name/kills" ]] && awk '$NF !~ /^-/ { bad = 1 } END { exit !bad }' "$tmp/$name/kills"; then
    fail "$name: mutate.sh signalled a bare pid: $(tr '\n' ';' <"$tmp/$name/kills")"
  fi
  echo "PASS: $name (exit $status after $((SECONDS - started))s)"
}

# Deferred launch: TERM arrives while the launcher is held before setpgrp.
GATE="$tmp/gate" start deferred-launch perl -e 'open(my $g, "<", $ARGV[0])' "$tmp/never" "$tmp/deferred-launch"
expect_hooks deferred-launch forked
kill -TERM "$pid"
expect_hooks deferred-launch deferred
exec 5>"$tmp/gate" # releases the launcher into setpgrp and the test
exec 5>&-
expect_hooks deferred-launch registered
finish_case deferred-launch 143

# Interrupted acknowledgement: TERM arrives while the parent is blocked reading
# the acknowledgement of a launcher that has already formed its group.
ACK_GATE="$tmp/ackgate" start ack-interrupted perl -e 'open(my $g, "<", $ARGV[0])' "$tmp/never" "$tmp/ack-interrupted"
expect_hooks ack-interrupted acking
kill -TERM "$pid"
expect_hooks ack-interrupted deferred
exec 5>"$tmp/ackgate" # releases the launcher into its acknowledgement
exec 5>&-
expect_hooks ack-interrupted registered
finish_case ack-interrupted 143

# Launch failure: the launcher dies before setpgrp, so the baseline run fails
# (BASELINE-FAIL, exit 2) with no group to stop.
LAUNCH_DIE=1 start launch-failure true "$tmp/launch-failure"
finish_case launch-failure 2

# term_ignoring <name> runs a test whose processes ignore TERM, and sends TERM
# once the run is registered and the test is ready. When REGATE is set, the
# test releases the parent's registration only after announcing ready, which
# forces the reverse of the usual announcement order.
term_ignoring() {
  start "$1" perl -e '$SIG{TERM} = "IGNORE"; fork or do { open(my $g, "<", $ARGV[0]); exit }; open(my $h, ">", $ENV{HOOKS}); print $h "ready\n"; close $h; if ($ENV{REGATE}) { open(my $r, ">", $ENV{REGATE}); close $r } open(my $g, "<", $ARGV[0])' \
    "$tmp/never" "$tmp/$1"
  expect_hooks "$1" registered ready
  kill -TERM "$pid"
  finish_case "$1" 143
}
term_ignoring term-ignoring
mkfifo "$tmp/regate"
REGATE="$tmp/regate" term_ignoring term-ignoring-ready-first

# Completed runs: the baseline and mutated runs end on their own, leaving a
# worker each, and the tests pass both times (SURVIVED, exit 1).
start completed-runs bash -c 'perl -e "open(my \$g, \"<\", \$ARGV[0])" "$1" "$2" & exit 0' _ "$tmp/never" "$tmp/completed-runs"
finish_case completed-runs 1
