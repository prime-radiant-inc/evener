#!/usr/bin/env bash
# process-group-lib.selfcheck.sh — prove scripts/lib/process-group-lib.sh against
# real processes.
#
# One case per row of the record state table. Each spawns a real `sleep` through
# the library's own wrapper and record, and asserts three things: the status
# stop_recorded_job returns, what became of the record, and whether the group is
# still alive by the library's own probe. No stand-in toolchain — the only shim
# is a `ps` that fails, for the one case about a listing that will not run, since
# the kernel cannot be asked to break `ps`.
#
# Every job is spawned in a process group of its own and stopped by that group id
# alone: never by name, never by a number read out of a global listing. The
# scratch directory comes from scratch-lib, so nothing here deletes a path it did
# not mint.
#
# Run it alone:  scripts/lib/process-group-lib.selfcheck.sh
# Run it in CI:  make lint-process-group
set -uo pipefail

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
. "$script_dir/scratch-lib.sh"
. "$script_dir/process-group-lib.sh"

grace=1
failures=0
work=""
trap scratch_rm EXIT
scratch_dir work process-group-selfcheck
own_pgid="$(pid_pgroup $$)"
marker_prefix=evener-selfcheck
marker="$(pgroup_marker "$marker_prefix")"

check() {
	if [ "$2" = "$3" ]; then
		printf 'ok   %s\n' "$1"
		return 0
	fi
	printf 'FAIL %s: expected [%s], got [%s]\n' "$1" "$2" "$3" >&2
	failures=$((failures + 1))
}

# spawn RECORD MARKER COMMAND... — a real job in a group of its own, recorded the
# way the gate and the installer record theirs. Prints the pid.
spawn() {
	local record="$1" spawn_marker="$2" pid
	shift 2
	: >"$record"
	pgroup_write_wrapper "$record" || return 1
	perl "$record.wrapper.pl" "$record" "$spawn_marker" "$@" >/dev/null 2>&1 &
	pid=$!
	pgroup_record_spawned "$record" "$pid" "$spawn_marker" || return 1
	pgroup_record_value "$record" "$grace" >/dev/null || return 1
	printf '%s' "$pid"
}

record_state() { [ -e "$1" ] && printf 'kept' || printf 'removed'; }
group_state() {
	local members
	members="$(pgroup_survivors "$1")" || { printf 'unknown'; return; }
	[ -n "$members" ] && printf 'alive' || printf 'empty'
}
run_case() {
	status=0
	stop_recorded_job "$1" "$grace" "$marker_prefix" || status=$?
}

# 1. a live group of the job's own: stopped, record removed, group empty.
rec="$work/1.pgid"
pid="$(spawn "$rec" "$marker" sleep 30)"
if [ "$(pid_pgroup "$pid")" = "$own_pgid" ]; then
	printf 'process-group-lib.selfcheck.sh: refusing to run: the job landed in this shell own group\n' >&2
	exit 1
fi
run_case "$rec"
check "live group stopped" "0 removed empty" "$status $(record_state "$rec") $(group_state "$pid")"

# 2. the same record after the group has gone on its own.
rec="$work/2.pgid"
pid="$(spawn "$rec" "$marker" sleep 0)"
sleep 1
run_case "$rec"
check "group already gone" "0 removed empty" "$status $(record_state "$rec") $(group_state "$pid")"

# 3. a pid: record for a job that has not split: it is in this shell's own group,
# which nothing may signal as a group, so only the pid stop can reach it.
rec="$work/3.pgid"
perl -e 'sleep 30' "$marker" &
presplit=$!
sleep 1
printf 'pid:%s:%s' "$presplit" "$marker" >"$rec"
run_case "$rec"
pid_is_running "$presplit"
running=$?
check "pre-split pid stopped" "0 removed 1" "$status $(record_state "$rec") $running"

# 4. a record written by somebody else's script: not this caller's job at all.
rec="$work/4.pgid"
pid="$(spawn "$rec" "$marker" sleep 30)"
printf 'pid:%s:some-other-script-77' "$pid" >"$rec"
run_case "$rec"
check "record of another script" "1 removed alive" "$status $(record_state "$rec") $(group_state "$pid")"
stop_pgroup "$pid" "$grace" >/dev/null 2>&1 || :

# 4b. this caller's prefix, but a live process that does not carry the token:
# unknown, not "somebody else's" — a live process that cannot be identified is
# exactly what must not be dropped.
rec="$work/4b.pgid"
pid="$(spawn "$rec" "$marker" sleep 30)"
printf 'pid:%s:%s-stranger' "$pid" "$marker_prefix" >"$rec"
run_case "$rec"
check "pid record, unidentifiable" "2 kept alive" "$status $(record_state "$rec") $(group_state "$pid")"
stop_pgroup "$pid" "$grace" >/dev/null 2>&1 || :

# 5. a survivor: record: kept, and nothing signalled.
rec="$work/5.pgid"
pid="$(spawn "$rec" "$marker" sleep 30)"
pgroup_record_survivor "$rec" "$pid"
run_case "$rec"
check "survivor record kept" "3 kept alive" "$status $(record_state "$rec") $(group_state "$pid")"
stop_pgroup "$pid" "$grace" >/dev/null 2>&1 || :

# 6. an empty record for a job that never names itself: the grace, then unknown.
rec="$work/6.pgid"
: >"$rec"
started="$(date +%s)"
run_case "$rec"
waited=$(( $(date +%s) - started ))
check "empty record waits then refuses" "2 kept yes" "$status $(record_state "$rec") $([ "$waited" -ge "$grace" ] && echo yes || echo no)"

# 7. a number handed on: a leader that started after the record was written.
rec="$work/7.pgid"
perl -e 'setpgrp(0, 0); exec @ARGV or die "exec: $!\n"' -- sleep 30 &
stranger=$!
sleep 1
printf 'pgid:%s:%s' "$stranger" "$marker" >"$rec"
python3 -c 'import os,sys,time; t=time.time()-60; os.utime(sys.argv[1], (t, t))' "$rec"
run_case "$rec"
check "number handed on" "1 removed alive" "$status $(record_state "$rec") $(group_state "$stranger")"
stop_pgroup "$stranger" "$grace" >/dev/null 2>&1 || :

# 8. the structural refusals.
refusals=""
for number in 0 1 not-a-number "$own_pgid"; do
	if pgroup_signalable "$number" 2>/dev/null; then
		refusals="$refusals allowed"
	else
		refusals="$refusals refused"
	fi
done
check "refusals" " refused refused refused refused" "$refusals"

# 9. a listing that will not run: unknown, record kept.
rec="$work/9.pgid"
pid="$(spawn "$rec" "$marker" sleep 30)"
mkdir -p "$work/psbin"
printf '#!/usr/bin/env bash\nexit 1\n' >"$work/psbin/ps"
chmod +x "$work/psbin/ps"
PATH="$work/psbin:$PATH" run_case "$rec"
check "unreadable listing kept" "2 kept" "$status $(record_state "$rec")"
stop_pgroup "$pid" "$grace" >/dev/null 2>&1 || :

# 10. a child that ignores SIGTERM: the escalation reaches it inside the grace.
rec="$work/10.pgid"
pid="$(spawn "$rec" "$marker" bash -c 'trap "" TERM; sleep 30')"
run_case "$rec"
check "TERM-ignoring child killed" "0 removed empty" "$status $(record_state "$rec") $(group_state "$pid")"

if [ "$failures" -ne 0 ]; then
	printf 'process-group-lib.selfcheck.sh: %s case(s) failed\n' "$failures" >&2
	exit 1
fi
printf 'process-group-lib.selfcheck.sh: all 11 cases passed\n'
