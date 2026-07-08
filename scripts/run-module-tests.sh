#!/usr/bin/env bash
# run-module-tests.sh — run the Go module test suites with wave scheduling.
#
# The repo is a go.work workspace of independent modules. `go test ./...` does
# not span modules, so the suites must be invoked per module.
#
# SERF_TEST_MODE=fast is the default gate used by `make test`: it runs the root
# module's package-level smoke tests, agent subpackages, and selected lightweight
# library modules in one wave. The exhaustive agent root package, llm and fuzz
# module sweeps, and fuzz seed corpora stay available through
# SERF_TEST_MODE=exhaustive and the explicit fuzz targets.
#
# SERF_TEST_MODE=exhaustive preserves the old two-wave workspace sweep. It is
# intentionally not the default: running every package/test/fuzz seed in the
# workspace now takes far longer than the local fast-gate budget and starves the
# timing-sensitive TUI tests when overlapped with CPU/process-heavy packages.
#
# Usage:
#   scripts/run-module-tests.sh <go-test-flags...>
#     scripts/run-module-tests.sh -short -count=1
#     SERF_TEST_MODE=exhaustive scripts/run-module-tests.sh -count=1
#     scripts/run-module-tests.sh -race -short -count=1
#
# Output: one PASS/FAIL line per module (with wall time) as each finishes; a
# failing module's full output is printed at the end. Exits non-zero on any
# failure. Override WAVE1/WAVE2 to change the schedule, AGENT_PARALLEL to tune
# the agent wave's -parallel.
set -uo pipefail

SERF_TEST_MODE=${SERF_TEST_MODE:-fast}
case "$SERF_TEST_MODE" in
	fast)
		WAVE1=${WAVE1:-". agent auth envvars invariant"}
		WAVE2=${WAVE2-}
		;;
	exhaustive)
		WAVE1=${WAVE1:-"."}
		WAVE2=${WAVE2:-"agent llm auth envvars fuzz invariant"}
		;;
	*)
		echo "run-module-tests: unknown SERF_TEST_MODE '$SERF_TEST_MODE'" >&2
		exit 2
		;;
esac
# Extra -parallel for the agent module. Exhaustive mode keeps the old 32 default
# to overlap long timer waits. Fast mode defaults to Go's package default because
# the remaining subpackage gate is process-heavy and -parallel 32 adds enough
# scheduler/syscall contention to push the wall clock over the budget.
if [ "$SERF_TEST_MODE" = "fast" ]; then
	AGENT_PARALLEL=${AGENT_PARALLEL-}
else
	AGENT_PARALLEL=${AGENT_PARALLEL-32}
fi
# Keep rapid.Check surfaces as deterministic smoke coverage in the default module
# gate. scripts/run-fuzz.sh owns the full rapid campaign unless RAPID_CHECKS is
# explicitly overridden by the caller.
RAPID_CHECKS=${RAPID_CHECKS:-1}
export RAPID_CHECKS
flags="$*"
logdir="$(mktemp -d -t serf-module-tests.XXXXXX)"
fail=0

logpath() { printf '%s/%s.log' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')"; }

run_module() {
	local m="$1" extra="$2"
	case "$SERF_TEST_MODE:$m" in
		fast:.)
			# Word-split flags intentionally so callers can pass multiple flags.
			# shellcheck disable=SC2086
			/usr/bin/time -p go test $flags -run '^(Test|Example)' .
			;;
		fast:agent)
			local -a pkgs=()
			local pkg
			while IFS= read -r pkg; do
				pkgs+=("$pkg")
			done < <(go list ./... | grep -v '^primeradiant.com/serf/agent$')
			[ "${#pkgs[@]}" -eq 0 ] && return 0
			# Word-split flags and extra intentionally so callers can pass multiple flags.
			# shellcheck disable=SC2086
			/usr/bin/time -p go test $flags $extra -run '^(Test|Example)' "${pkgs[@]}"
			;;
		*)
			# Word-split flags intentionally so callers can pass multiple flags.
			# shellcheck disable=SC2086
			/usr/bin/time -p go test $flags $extra ./...
			;;
	esac
}

# run_wave <extra-flags> <module...> — run the modules concurrently, wait, and
# report each one's result; records failures in the global $fail.
run_wave() {
	local extra="$1"; shift
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
