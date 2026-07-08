#!/usr/bin/env bash
# run-module-tests.sh — run the non-fuzz Go module test suites with wave scheduling.
#
# The repo is a go.work workspace of independent modules. `go test ./...` does
# not span modules, so the suites must be invoked per module. The regular gate
# intentionally runs only non-fuzz Test/Example entry points; native Fuzz
# targets, rapid/sequence fuzz tests, structured fuzz reachability checks, saved
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

# Package/test parallelism controls for heavyweight modules. Explicit empty
# values mean "don't pass the flag" so go test uses its defaults; the -race gate
# sets AGENT_PARALLEL empty to avoid oversubscribing few-core CI.
ROOT_P=${ROOT_P-12}
AGENT_PARALLEL=${AGENT_PARALLEL-32}
AGENT_P=${AGENT_P-4}

# Fuzz-designated Test* functions are not part of the regular gate. Native Fuzz*
# targets are already excluded by -run; these names cover rapid/sequence fuzz
# tests and structured-generator reachability proofs that remain under make fuzz.
fuzz_test_skip='(SeqFuzz|SchemaFuzz|Structured.*Reach|LifecycleAdapter|ToolArgsAdapter|TurnPagingEquivalenceSanity|WireTypeRegistryCoverage|LineWindowExtractorsSanity|TranscriptReadersAgreeSanity|WriteListRoundTrip|LaunchConfigThreeStateRoundTrip|DifferentialSanity|StreamVsNonStreamSanity)'

flags="$*"
logdir="$(mktemp -d -t serf-module-tests.XXXXXX)"
fail=0

logpath() { printf '%s/%s.log' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')"; }

run_module() {
	local m="$1" extra="$2"
	# Word-split flags and extra intentionally so callers can pass multiple flags.
	# shellcheck disable=SC2086
		/usr/bin/time -p go test $flags $extra -run '^(Test|Example)' -skip "$fuzz_test_skip" ./...
}

# run_wave <module...> — run the modules concurrently, wait, and report each
# one's result; records failures in the global $fail.
module_extra() {
	case "$1" in
		.)
			local extra=""
			[ -n "$ROOT_P" ] && extra="$extra -p $ROOT_P"
			printf '%s' "$extra"
			;;
		agent)
			local extra=""
			[ -n "$AGENT_P" ] && extra="$extra -p $AGENT_P"
			[ -n "$AGENT_PARALLEL" ] && extra="$extra -parallel $AGENT_PARALLEL"
			printf '%s' "$extra"
			;;
		*)
			printf ''
			;;
	esac
}

run_wave() {
	[ "$#" -eq 0 ] && return 0
	local -a names=() pids=()
	local m log extra
	for m in "$@"; do
		log="$(logpath "$m")"
		extra="$(module_extra "$m")"
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

run_wave $WAVE1
run_wave $WAVE2

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
