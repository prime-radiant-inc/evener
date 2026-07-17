#!/usr/bin/env bash
# run-module-lint.sh - lint all non-fuzz Go modules with bounded concurrency.
set -uo pipefail

MODULES=${MODULES:-". agent llm auth envvars invariant identifier"}
LINT_PARALLEL=${LINT_PARALLEL:-4}
case "$LINT_PARALLEL" in
	''|*[!0-9]*|0*)
		printf 'lint: LINT_PARALLEL must be a positive integer without leading zeroes (got %s)\n' "$LINT_PARALLEL" >&2
		exit 2
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
	local status="$1"
	stop_children
	exit "$status"
}

trap cleanup EXIT
trap 'interrupted 129' HUP
trap 'interrupted 130' INT
trap 'interrupted 143' TERM

if ! logdir="$(mktemp -d -t serf-module-lint.XXXXXX)"; then
	printf 'lint: unable to create temporary log directory\n' >&2
	exit 1
fi

# Invoke an absent command once so Bash retains its original diagnostic without
# duplicating it for every module.
if ! command -v golangci-lint >/dev/null 2>&1; then
	( golangci-lint run ./... ) >"$logdir/setup.log" 2>&1
	status=$?
	cat "$logdir/setup.log"
	printf 'FAIL lint (%d modules not checked: %s)\n' "$module_count" "$MODULES"
	exit "$status"
fi

run_wave() {
	local first="$1" last="$2" i j pid status module log gate
	local -a indexes=()
	active_pids=()
	gate="$logdir/wave.start"
	if ! mkfifo "$gate"; then
		printf 'lint: unable to create module start gate\n' >&2
		exit 1
	fi
	exec 7<>"$gate"
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
	rm -f "$gate"
	for j in "${!active_pids[@]}"; do
		pid="${active_pids[$j]}"
		if wait "$pid"; then status=0; else status=$?; fi
		active_pids[$j]=""
		printf '%s\n' "$status" >"$logdir/${indexes[$j]}.status"
	done
	active_pids=()
}

for ((first = 0; first < module_count; first += LINT_PARALLEL)); do
	last=$((first + LINT_PARALLEL))
	[ "$last" -le "$module_count" ] || last=$module_count
	run_wave "$first" "$last"
done

failed_modules=()
for ((i = 0; i < module_count; i++)); do
	status="$(cat "$logdir/$i.status")"
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
printf 'FAIL lint (%d/%d modules: %s)\n' \
	"${#failed_modules[@]}" "$module_count" "${failed_modules[*]}"
exit 1
