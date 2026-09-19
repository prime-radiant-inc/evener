#!/usr/bin/env bash
# gate-bounded.sh — the bounded process runner and process-tree stopper the test
# gate uses for its `go list` enumerations and its evener-dev flag derivation.
#
# Sourced, never executed. It sets no variables and launches nothing on its own,
# so a caller can source it and drive the functions directly.
#
# run_bounded runs a command in its own process group (bash job control) and
# stops that whole group on a timeout: one signal reaches every process the
# command forked, including one reparented while it is being stopped, so there is
# no PID snapshot to keep atomic and no descendant that can slip behind it. When
# the platform will not give the job its own group, stop_command falls back to
# walking descendants by PID.
#
# On a timeout, run_bounded calls run_bounded_timeout_diagnostic if the caller
# has defined it. The gate supplies its cache-stall diagnostic; a caller that has
# none (a test) simply gets the nonzero return.
#
# EVENER_STOP_TREE_GRACE_SECONDS bounds how long a stopped command is given to
# exit on TERM before KILL. It defaults to 5 and exists so a test can shrink it
# below the timing gate's per-test ceiling.

# process_descendants <pid> — print every descendant of pid, deepest last.
process_descendants() {
	local parent="$1" child
	for child in $(ps -axo pid=,ppid= 2>/dev/null | awk -v parent="$parent" '$2 == parent {print $1}'); do
		process_descendants "$child"
		printf '%s\n' "$child"
	done
}

# stop_process_tree <pid> — the fallback when the command does not lead its own
# process group. TERM each discovered pid once, rescanning for new descendants
# and honouring the grace for every pid ever seen, then KILL all of them. The
# parent is signalled last so its children stay discoverable while it lives.
stop_process_tree() {
	local pid="$1" p pass found grace alive seen termed deadline
	seen=""
	termed=""
	grace=${EVENER_STOP_TREE_GRACE_SECONDS:-5}
	deadline=$((SECONDS + grace))
	while [ "$SECONDS" -lt "$deadline" ]; do
		for p in $(process_descendants "$pid"); do
			seen="$seen $p"
			case " $termed " in
			*" $p "*) ;;
			*)
				kill -TERM "$p" 2>/dev/null || :
				termed="$termed $p"
				;;
			esac
		done
		case " $termed " in
		*" $pid "*) ;;
		*)
			kill -TERM "$pid" 2>/dev/null || :
			termed="$termed $pid"
			;;
		esac
		alive=0
		for p in "$pid" $seen; do
			[ -n "$p" ] || continue
			kill -0 "$p" 2>/dev/null && alive=1
		done
		[ "$alive" -eq 0 ] && break
		sleep 0.1
	done
	# Final phase, children before parents. Every tracked pid is rescanned, not
	# just the root: a child forked by an intermediate parent while that parent
	# is still alive is found here, before the parent's death can reparent it
	# out of view. The passes are bounded, so a pid cannot sit around long
	# enough to be reused under us.
	for pass in 1 2 3 4 5; do
		found=""
		for p in $seen; do
			found="$found $(process_descendants "$p")"
		done
		found=${found# }
		[ -n "$found" ] || break
		for p in $found; do
			seen="$seen $p"
			kill -KILL "$p" 2>/dev/null || :
		done
		sleep 0.05
	done
	for p in $seen; do
		[ "$p" = "$pid" ] && continue
		[ -n "$p" ] || continue
		kill -KILL "$p" 2>/dev/null || :
	done
	kill -KILL "$pid" 2>/dev/null || :
}

# stop_command <pid> — stop a bounded command and everything it forked. When the
# command leads its own process group, one TERM then one KILL reach the whole
# group; otherwise fall back to stop_process_tree. Never waits past the grace.
#
# The group path needs a real, non-initial process group: pid must be greater
# than 1 and be its own group leader. `kill -TERM -1` would signal every process
# the user may signal, so anything else takes the fallback.
stop_command() {
	local pid="$1" grace pgid deadline
	grace=${EVENER_STOP_TREE_GRACE_SECONDS:-5}
	if [ "$pid" -gt 1 ]; then
		pgid=$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d ' ')
		if [ -n "$pgid" ] && [ "$pgid" -gt 1 ] && [ "$pgid" = "$pid" ]; then
			kill -TERM -"$pid" 2>/dev/null || :
			deadline=$((SECONDS + grace))
			while [ "$SECONDS" -lt "$deadline" ]; do
				kill -0 -"$pid" 2>/dev/null || break
				sleep 0.1
			done
			kill -KILL -"$pid" 2>/dev/null || :
			return 0
		fi
	fi
	stop_process_tree "$pid"
}

# enumeration_argv prints, one per line, the argv of the package enumeration:
# `go list` with the selection flags the derivation produced (list_flags), then
# ./... . The line-per-arg form lets the caller read it into an array without
# splitting a value or pathname-expanding it, and keeping it a function lets a
# test assert the selection flags actually reach the command.
enumeration_argv() {
	local flag
	printf '%s\n' go list
	for flag in ${list_flags[@]+"${list_flags[@]}"}; do
		printf '%s\n' "$flag"
	done
	printf '%s\n' ./...
}

# run_enumeration <module> <out-file> runs the package enumeration with the
# selection flags in list_flags. Keeping both the argv construction and this
# call in the library lets a test assert the flags actually reach `go list`.
run_enumeration() {
	local module="$1" out_file="$2" enum_arg
	local -a enum_argv=()
	while IFS= read -r enum_arg; do
		enum_argv+=("$enum_arg")
	done < <(enumeration_argv)
	run_bounded "${ROOT_PACKAGE_LIST_TIMEOUT:-30}" 'go list ./...' "$module" "$out_file" "${enum_argv[@]}"
}

# run_bounded <bound> <what> <module> <log-file> <cmd...> — run cmd in the
# background, capture its stdout in log-file, and bound it: a step that outlives
# <bound> has its whole process group stopped and, if the caller defined
# run_bounded_timeout_diagnostic, gets that diagnostic, rather than hanging.
# stderr is kept as <log-file>.stderr and replayed on a nonzero exit.
#
# Completion is read from a status file the child publishes by writing a temp
# file and renaming it, so the status file is absent or complete, never a
# partial write, and it -- not kill -0 -- is what says "done": kill -0 still
# succeeds for a process that exited just before the deadline (until bash reaps
# it) and would report a successful command as a timeout. A status file absent
# at the deadline is final: a command that only exits zero while it is being
# stopped did not finish in time, and its post-deadline output is not consumed.
run_bounded() {
	local bound="$1" what="$2" module="$3" log_file="$4"; shift 4
	local pid started_at deadline status
	rm -f "${log_file}.status" "${log_file}.status.tmp"
	# Monitor mode puts this job in its own process group, so stop_command can
	# signal the whole tree at once. The subshell writes the status, so it, not
	# the command, is the group leader whose pid stop_command signals.
	set -m
	(
		# A caught no-op handler keeps the wrapper alive to publish the final
		# status while the command's own processes keep the default disposition,
		# so a command that handles TERM still gets a chance to exit on it. An
		# ignored signal (trap '') would be inherited and force every timeout to
		# the full grace then KILL.
		trap ':' TERM
		"$@" >"$log_file" 2>"${log_file}.stderr"
		printf '%s\n' "$?" >"${log_file}.status.tmp"
		mv "${log_file}.status.tmp" "${log_file}.status"
	) &
	pid="$!"
	set +m
	started_at=$SECONDS
	deadline=$((started_at + bound))
	# The deadline is evaluated together with the status: a status observed
	# strictly before the deadline is a success, and at or after it the timeout
	# is final even if the status landed just then -- otherwise a command that
	# finished after the bound could be accepted as a success.
	while :; do
		if [ -f "${log_file}.status" ] && [ "$SECONDS" -lt "$deadline" ]; then
			break
		fi
		if [ "$SECONDS" -ge "$deadline" ]; then
			stop_command "$pid"
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
