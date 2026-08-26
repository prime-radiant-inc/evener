#!/usr/bin/env bash
# test-web.sh — the frontend's single gate entry point: typecheck, unit
# tests, then lint (mirrors the Go test+lint split, but the frontend
# toolchain doesn't need separate targets per check).
#
# The three checks are independent readers of the same sources, so they run
# concurrently and wall time is the slowest one (vitest) instead of the sum.
# Each writes its own log; a failure replays exactly the failing check's
# output. Every check runs with its own HOME/TMPDIR/XDG roots under a
# scratch-lib minted directory; a failed or interrupted run keeps the log
# directory and prints its path.
set -u

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$script_dir/../lib/scratch-lib.sh"

cd "$script_dir/../../cmd/evener-hub/frontend" || exit 1

dir=""
active_pids=(); started=(); fail=0; complete=0; interrupt_status=0
defer_signals=0; stopping=0; finishing=0

forget_pid() {
	local pid="$1" candidate
	local -a remaining=()
	for candidate in "${active_pids[@]-}"; do
		[ -n "$candidate" ] || continue
		[ "$candidate" = "$pid" ] || remaining+=("$candidate")
	done
	active_pids=()
	for candidate in "${remaining[@]-}"; do
		[ -n "$candidate" ] && active_pids+=("$candidate")
	done
}

owned_job_is_running() {
	local pid="$1" candidate
	for candidate in $(jobs -pr); do
		[ "$candidate" = "$pid" ] && return 0
	done
	return 1
}

stop_checks() {
	local pid wait_status previous_stopping
	previous_stopping=$stopping
	stopping=1
	for pid in "${active_pids[@]-}"; do
		[ -n "$pid" ] || continue
		owned_job_is_running "$pid" || continue
		kill -TERM "$pid" 2>/dev/null || :
	done
	for pid in "${active_pids[@]-}"; do
		[ -n "$pid" ] || continue
		while :; do
			if wait "$pid" 2>/dev/null; then wait_status=0; else wait_status=$?; fi
			owned_job_is_running "$pid" || break
		done
	done
	stopping=$previous_stopping
	active_pids=()
}

finish() {
	finish_status=$?
	finishing=1
	[ "$complete" -eq 1 ] || stop_checks
	if [ "$interrupt_status" -ne 0 ]; then
		finish_status="$interrupt_status"
		interrupt_status=0
	fi
	if [ "$complete" -eq 1 ] && [ "$fail" -eq 0 ] && [ "$finish_status" -eq 0 ]; then
		scratch_rm || finish_status=1
	else
		[ -z "$dir" ] || printf 'full logs: %s\n' "$dir" >&2
	fi
	finishing=0
	consume_interrupt
	trap - 0; exit "$finish_status"
}

consume_interrupt() {
	local status="$interrupt_status"
	[ "$status" -eq 0 ] || interrupted "$status"
}

interrupted() {
	local status="$1"
	interrupt_status="$status"
	if [ "$defer_signals" -eq 1 ]; then
		return
	fi
	if [ "$stopping" -eq 1 ]; then
		return
	fi
	stopping=1
	stop_checks
	stopping=0
	status="$interrupt_status"
	interrupt_status=0
	if [ "$finishing" -eq 1 ]; then
		[ -z "$dir" ] || printf 'full logs: %s\n' "$dir" >&2
		trap - 0
		exit "$status"
	fi
	exit "$status"
}

# The trap is armed before any scratch exists: a crash between mint and arming
# would leak the directory (the trap-before-mkdir ordering the audit enforces).
trap finish EXIT
trap 'interrupted 129' 1; trap 'interrupted 130' 2; trap 'interrupted 143' 15

scratch_dir dir evener-test-web

for c in typecheck test lint; do
	check_dir="$dir/$c"
	if ! mkdir -p "$check_dir/home" "$check_dir/tmp" "$check_dir/xdg-config" "$check_dir/xdg-cache" "$check_dir/xdg-state"; then fail=1; break; fi
	defer_signals=1
	HOME="$check_dir/home" TMPDIR="$check_dir/tmp" XDG_CONFIG_HOME="$check_dir/xdg-config" XDG_CACHE_HOME="$check_dir/xdg-cache" XDG_STATE_HOME="$check_dir/xdg-state" NODE_DISABLE_COMPILE_CACHE=1 npm run "$c" >"$dir/$c.log" 2>&1 &
	active_pids+=("$!"); started+=("$c")
	defer_signals=0
	consume_interrupt
done

for i in "${!started[@]}"; do
	c="${started[$i]}"
	pid="${active_pids[0]}"
	if wait "$pid"; then check_status=0; else check_status=$?; fi
	# A completed wait removes the job from Bash's job table, which is the
	# completion/ownership handoff and not a PID liveness guess vulnerable to
	# reuse. Signals are not deferred here: Bash must wake this exact wait.
	if ! owned_job_is_running "$pid"; then
		forget_pid "$pid"
	fi
	printf '%s\n' "$check_status" >"$dir/$c.status"
	consume_interrupt
done

for c in typecheck test lint; do
	if [ "$(cat "$dir/$c.status" 2>/dev/null || echo 1)" = 0 ]; then
		printf 'PASS  web-%s\n' "$c"
	else
		printf 'FAIL  web-%s\n' "$c"; [ ! -f "$dir/$c.log" ] || cat "$dir/$c.log"; fail=1
	fi
done

complete=1
exit "$fail"
