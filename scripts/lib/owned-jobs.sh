#!/usr/bin/env bash
# owned-jobs.sh — ask a gate shell's own job table whether a background job
# it started is still running. Sourced by the bash gate scripts that run
# checks side by side (scripts/web/test-web.sh, scripts/web/test-web-browser.sh).
#
# Bash 3.2 (macOS) has no `wait -n`, so completion is found by asking the job
# table, never by `kill -0`: once a job is reaped its pid can belong to an
# unrelated process.
#
# The listing goes through a file, not `$(jobs -pr)`. Bash 5.2 drops a trapped
# signal that lands while it is parsing a command substitution (the trap
# fails with "unexpected EOF while looking for matching `)'" and never runs),
# and these gates poll the job table continuously, so an interrupt could be
# silently ignored and the gate would run to completion.
#
# The caller sets owned_jobs_list to a file path it owns before the first call.

# owned_job_is_running PID — whether PID is one of this shell's running jobs.
owned_job_is_running() {
	local candidate
	jobs -pr >"$owned_jobs_list" || return 1
	while read -r candidate; do
		[ "$candidate" = "$1" ] && return 0
	done <"$owned_jobs_list"
	return 1
}

# wait_for_owned_job PID — wait until PID is no longer one of this shell's
# running jobs. One wait is not enough: a wait run from a trap handler can
# return early, with 128 plus the trap's signal, while the job is still
# running, so the job table has the last word.
wait_for_owned_job() {
	local status waiting=1
	while [ "$waiting" -eq 1 ]; do
		if wait "$1" 2>/dev/null; then status=0; else status=$?; fi
		# Negative and 127 statuses mean Bash no longer owns a child it can
		# reap.
		if [ "$status" -lt 0 ] || [ "$status" -eq 127 ] || ! owned_job_is_running "$1"; then
			waiting=0
		fi
	done
}
