#!/usr/bin/env bash
# run-module-tests.sh — run the non-fuzz Go module test suites with wave scheduling.
#
# The repo is a go.work workspace of independent modules. `go test ./...` does
# not span modules, so the suites must be invoked per module. The regular gate
# intentionally runs only Test/Example entry points; native Fuzz targets, saved
# fuzz corpus replay, and the fuzz toolkit module are owned by `make fuzz`.
#
# The default wave split gives the root module's timing-sensitive TUI tests the
# machine to themselves, then runs the remaining modules concurrently. Override
# MODULES, WAVE1, WAVE2, or AGENT_PARALLEL when a caller needs a different local
# schedule without changing the coverage boundary.
#
# Usage:
#   scripts/run-module-tests.sh <go-test-flags...>
#     scripts/run-module-tests.sh -short -count=1
#     scripts/run-module-tests.sh -race -short -count=1
#
# Output: one PASS/FAIL line per module (with wall time) as each finishes; a
# failing module's full output is printed at the end. Exits non-zero on any
# failure.
set -uo pipefail

MODULES=${MODULES:-". agent llm auth envvars invariant"}
if [ -z "${WAVE1+x}" ] && [ -z "${WAVE2+x}" ]; then
	WAVE1=""
	WAVE2=""
	for m in $MODULES; do
		if [ "$m" = "." ]; then
			WAVE1="$WAVE1 $m"
		else
			WAVE2="$WAVE2 $m"
		fi
	done
	WAVE1=${WAVE1# }
	WAVE2=${WAVE2# }
else
	WAVE1=${WAVE1:-}
	WAVE2=${WAVE2:-}
fi

# Extra -parallel for the agent wave. An explicit empty value means "don't pass
# -parallel" so go test uses GOMAXPROCS; the -race gate sets this to avoid
# oversubscribing few-core CI.
AGENT_PARALLEL=${AGENT_PARALLEL-32}

# Keep rapid.Check surfaces as deterministic smoke coverage in the default
# module gate. scripts/run-fuzz.sh owns the full rapid campaign unless
# RAPID_CHECKS is explicitly overridden by the caller.
RAPID_CHECKS=${RAPID_CHECKS:-1}
export RAPID_CHECKS

flags="$*"
logdir="$(mktemp -d -t serf-module-tests.XXXXXX)"
fail=0

logpath() { printf '%s/%s.log' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')"; }

run_module() {
	local m="$1" extra="$2"
	# Word-split flags and extra intentionally so callers can pass multiple flags.
	# shellcheck disable=SC2086
	/usr/bin/time -p go test $flags $extra -run '^(Test|Example)' ./...
}

# run_wave <extra-flags> <module...> — run the modules concurrently, wait, and
# report each one's result; records failures in the global $fail.
run_wave() {
	local extra="$1"; shift
	[ "$#" -eq 0 ] && return 0
	local -a names=() pids=()
	local m log
	for m in "$@"; do
		log="$(logpath "$m")"
		( cd "$m" && run_module "$m" "$extra" ) >"$log" 2>&1 &
		pids+=("$!"); names+=("$m")
	done
	local i
	for i in "${!pids[@]}"; do
		m="${names[$i]}"; log="$(logpath "$m")"
		if wait "${pids[$i]}"; then
			printf 'PASS  %-8s %s\n' "$m" "$(awk '/^real /{print $2"s"}' "$log" | tail -1)"
		else
			printf 'FAIL  %-8s\n' "$m"; fail=1
		fi
	done
}

agentExtra=""
[ -n "$AGENT_PARALLEL" ] && agentExtra="-parallel $AGENT_PARALLEL"
run_wave "" $WAVE1
run_wave "$agentExtra" $WAVE2

if [ "$fail" -ne 0 ]; then
	echo
	echo "=== failing module output ==="
	for m in $WAVE1 $WAVE2; do
		log="$(logpath "$m")"
		[ -f "$log" ] || continue
		if grep -qE "^(FAIL|--- FAIL|panic:)" "$log"; then
			echo "----- $m -----"; cat "$log"
		fi
	done
	echo
	echo "full logs: $logdir"
fi

exit "$fail"
