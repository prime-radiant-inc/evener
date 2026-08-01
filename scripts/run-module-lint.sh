#!/usr/bin/env bash
# run-module-lint.sh - lint all non-fuzz Go modules with bounded concurrency.
set -uo pipefail

# A run owes its reader exactly one summary line, so every failing exit routes
# through here and none of them can end silently. The leading category is the
# vocabulary a human or a log scraper reads the failure by: setup for a run that
# never got as far as a module, not-checked for an unusable linter, findings for
# real lint output, results-lost for checks whose verdicts did not survive the
# run, interrupted for a signal.
fail_lint() {
	printf 'FAIL lint (%s)\n' "$1"
	exit "$2"
}

MODULES=${MODULES:-". agent llm auth envvars invariant identifier"}
LINT_PARALLEL=${LINT_PARALLEL:-4}
case "$LINT_PARALLEL" in
	''|*[!0-9]*|0*)
		printf 'lint: LINT_PARALLEL must be a positive integer without leading zeroes (got %s)\n' "$LINT_PARALLEL" >&2
		fail_lint 'setup: LINT_PARALLEL must be a positive integer without leading zeroes' 2
		;;
esac

# Whitespace splitting is the MODULES interface. Indexed arrays retain caller
# order without requiring Bash 4 associative arrays.
# shellcheck disable=SC2206
modules=($MODULES)
module_count=${#modules[@]}
start=$SECONDS
printf 'lint: checking %d modules\n' "$module_count"

logdir=""
keep_failed_logs=0
active_pids=()

stop_children() {
	local pid
	[ "${#active_pids[@]}" -eq 0 ] && return 0
	for pid in "${active_pids[@]}"; do
		[ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null || :
	done
	for pid in "${active_pids[@]}"; do
		[ -n "$pid" ] && wait "$pid" 2>/dev/null || :
	done
	active_pids=()
}

cleanup() {
	stop_children
	if [ -n "$logdir" ] && [ "$keep_failed_logs" -eq 0 ]; then
		rm -rf "$logdir"
	fi
}

interrupted() {
	local status="$1" signal="$2"
	stop_children
	fail_lint "interrupted: $signal" "$status"
}

# Nothing in this run deletes anything under $logdir before cleanup, so an
# ENOENT under it is the scratch space going away rather than a lint finding.
# Left unreported, every step that touches it fails with its own bare Bash
# diagnostic and the retained-log pointer names a directory that is gone.
scratch_vanished() {
	printf 'lint: %s disappeared mid-run: %s\n' "$1" "$2" >&2
	printf 'lint: nothing in this run removes it before cleanup, so something outside did; a TMPDIR reaper under disk pressure is the usual suspect on macOS\n' >&2
	fail_lint "results-lost: $module_count modules: $MODULES" 1
}

trap cleanup EXIT
trap 'interrupted 129 SIGHUP' HUP
trap 'interrupted 130 SIGINT' INT
trap 'interrupted 143 SIGTERM' TERM

if ! logdir="$(mktemp -d -t serf-module-lint.XXXXXX)"; then
	printf 'lint: unable to create temporary log directory\n' >&2
	fail_lint 'setup: unable to create temporary log directory' 1
fi

# Invoke an absent command once so Bash retains its original diagnostic without
# duplicating it for every module.
if ! command -v golangci-lint >/dev/null 2>&1; then
	( golangci-lint run ./... ) >"$logdir/setup.log" 2>&1
	status=$?
	cat "$logdir/setup.log"
	fail_lint "not-checked: $module_count modules: $MODULES" "$status"
fi

run_wave() {
	local first="$1" last="$2" i j pid status module log gate
	local -a indexes=()
	active_pids=()
	[ -d "$logdir" ] || scratch_vanished 'the temporary log directory' "$logdir"
	gate="$logdir/wave.start"
	if ! mkfifo "$gate"; then
		printf 'lint: unable to create module start gate\n' >&2
		fail_lint 'setup: unable to create module start gate' 1
	fi
	# Without the gate open, every child of this wave blocks forever opening a
	# FIFO nobody will write, so refuse the wave rather than hang in wait.
	if ! exec 7<>"$gate"; then
		[ -d "$logdir" ] || scratch_vanished 'the temporary log directory' "$logdir"
		printf 'lint: unable to open module start gate: %s\n' "$gate" >&2
		fail_lint 'setup: unable to open module start gate' 1
	fi
	for ((i = first; i < last; i++)); do
		module="${modules[$i]}"
		log="$logdir/$i.log"
		(
			exec 7>&-
			IFS= read -r _ || exit 1
			cd "$module" || exit 1
			# This runner already bounds child concurrency; disable the linter's
			# process-global exclusion so every child can perform its module check.
			exec golangci-lint run --allow-parallel-runners ./...
		) <"$gate" >"$log" 2>&1 &
		active_pids+=("$!")
		indexes+=("$i")
	done
	for _ in "${active_pids[@]}"; do
		printf 'start\n' >&7
	done
	exec 7>&-
	for j in "${!active_pids[@]}"; do
		pid="${active_pids[$j]}"
		if wait "$pid"; then status=0; else status=$?; fi
		active_pids[$j]=""
		if ! { printf '%s\n' "$status" >"$logdir/${indexes[$j]}.status"; } 2>/dev/null; then
			[ -d "$logdir" ] || scratch_vanished 'the temporary log directory' "$logdir"
			printf 'lint: unable to record the result for module %s\n' "${modules[${indexes[$j]}]}" >&2
			fail_lint "results-lost: unable to record the result for module ${modules[${indexes[$j]}]}" 1
		fi
	done
	active_pids=()
	# Each child opens this gate through an inherited redirection, which happens
	# whenever that child is first scheduled. Unlinking it before the whole wave
	# is reaped hands a late child ENOENT on the gate and no log at all.
	[ -p "$gate" ] || scratch_vanished 'the module start gate' "$gate"
	rm -f "$gate"
}

for ((first = 0; first < module_count; first += LINT_PARALLEL)); do
	last=$((first + LINT_PARALLEL))
	[ "$last" -le "$module_count" ] || last=$module_count
	run_wave "$first" "$last"
done

failed_modules=()
for ((i = 0; i < module_count; i++)); do
	if ! status="$(cat "$logdir/$i.status" 2>/dev/null)"; then
		[ -d "$logdir" ] || scratch_vanished 'the temporary log directory' "$logdir"
		printf 'lint: unable to read the result for module %s\n' "${modules[$i]}" >&2
		fail_lint "results-lost: unable to read the result for module ${modules[$i]}" 1
	fi
	if [ "$status" -ne 0 ]; then
		failed_modules+=("${modules[$i]}")
	else
		rm -f "$logdir/$i.log"
	fi
	rm -f "$logdir/$i.status"
done

if [ "${#failed_modules[@]}" -eq 0 ]; then
	printf 'PASS lint (%d modules, %ds)\n' "$module_count" "$((SECONDS - start))"
	exit 0
fi

for ((i = 0; i < module_count; i++)); do
	[ -f "$logdir/$i.log" ] || continue
	printf '%s\n' "----- ${modules[$i]} -----"
	cat "$logdir/$i.log"
done
printf 'full logs: %s\n' "$logdir"
keep_failed_logs=1
fail_lint "findings: ${#failed_modules[@]}/$module_count modules: ${failed_modules[*]}" 1
