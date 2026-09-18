#!/usr/bin/env bash
# gate-bounded.sh — the bounded process runner and process-tree stopper the test
# gate uses for its `go list` enumerations and its evener-dev flag derivation.
#
# Sourced, never executed. It sets no variables and launches nothing on its own,
# so a caller can source it and drive the functions directly.
#
# On a timeout, run_bounded calls run_bounded_timeout_diagnostic if the caller
# has defined it. The gate supplies its cache-stall diagnostic; a caller that
# has none (a test) simply gets the nonzero return.

# process_descendants <pid> — print every descendant of pid, deepest last.
process_descendants() {
	local parent="$1" child
	for child in $(ps -axo pid=,ppid= 2>/dev/null | awk -v parent="$parent" '$2 == parent {print $1}'); do
		process_descendants "$child"
		printf '%s\n' "$child"
	done
}

# stop_process_tree <pid> — stop a command and everything it forked. TERM first
# for a clean exit, then KILL whatever ignored it after a short grace. Bounded:
# nothing here waits on a process past the grace, so a TERM-ignoring or
# uninterruptible descendant cannot hold the caller open, and a survivor is left
# to init rather than waited on.
#
# Descendants are rescanned on every pass and the parent is signalled last in
# the final sweep: a process that forks during cleanup puts its children behind
# a one-shot snapshot, and killing the parent first would reparent the rest to
# init where no rescan can find them.
stop_process_tree() {
	local pid="$1" descendant grace alive seen
	seen=""
	grace=$((SECONDS + 5))
	while [ "$SECONDS" -lt "$grace" ]; do
		alive=0
		for descendant in $(process_descendants "$pid"); do
			seen="$seen $descendant"
			kill -TERM "$descendant" 2>/dev/null && alive=1
		done
		kill -TERM "$pid" 2>/dev/null && alive=1
		[ "$alive" -eq 0 ] && break
		sleep 0.1
	done
	# KILL every pid ever discovered, not just the ones a final rescan can still
	# see: when the parent dies on TERM, a child that ignored it is reparented
	# and vanishes from later scans, and a one-shot list would have missed it
	# too. The parent goes last so its children stay discoverable as long as it
	# lives.
	for descendant in $seen; do
		kill -KILL "$descendant" 2>/dev/null || :
	done
	for descendant in $(process_descendants "$pid"); do
		kill -KILL "$descendant" 2>/dev/null || :
	done
	kill -KILL "$pid" 2>/dev/null || :
}

# run_bounded <bound> <what> <module> <log-file> <cmd...> — run cmd in the
# background, capture its stdout in log-file, and bound it: a step that outlives
# <bound> has its whole process tree stopped and, if the caller defined
# run_bounded_timeout_diagnostic, gets that diagnostic, rather than hanging.
# stderr is kept as <log-file>.stderr and replayed on a nonzero exit.
#
# Completion is read from a status file the child publishes by writing a temp
# file and renaming it, so the status file is either absent or complete, never a
# partial write, and it -- not kill -0 -- is what says "done": kill -0 still
# succeeds for a process that exited just before the deadline (until bash reaps
# it) and would report a successful command as a timeout.
run_bounded() {
	local bound="$1" what="$2" module="$3" log_file="$4"; shift 4
	local pid started_at status
	rm -f "${log_file}.status" "${log_file}.status.tmp"
	(
		"$@" >"$log_file" 2>"${log_file}.stderr"
		printf '%s\n' "$?" >"${log_file}.status.tmp"
		mv "${log_file}.status.tmp" "${log_file}.status"
	) &
	pid="$!"
	started_at=$SECONDS
	while [ ! -f "${log_file}.status" ]; do
		if [ $((SECONDS - started_at)) -ge "$bound" ]; then
			stop_process_tree "$pid"
			# A complete status file that landed while the tree was being
			# stopped means the command had already finished; trust it over the
			# clock. Nothing is waited on here, so a survivor in an
			# uninterruptible wait cannot hold the bound open.
			[ -f "${log_file}.status" ] && break
			if declare -F run_bounded_timeout_diagnostic >/dev/null 2>&1; then
				run_bounded_timeout_diagnostic "$what" "$bound" "$module" "${log_file}.stderr"
			fi
			return 1
		fi
		sleep 0.1
	done
	status="$(cat "${log_file}.status" 2>/dev/null)"
	case "$status" in
	''|*[!0-9]*) status=1 ;;
	esac
	if [ "$status" -eq 0 ]; then
		return 0
	fi
	cat "${log_file}.stderr" >&2
	return "$status"
}
