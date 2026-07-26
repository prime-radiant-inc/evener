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
# The frontend gate (vitest/typecheck/lint via `make test-web`) is a third
# stream started before wave 1 and joined at the end, so its ~40s runs across
# the Go waves instead of being added onto them. Measured on an idle 10-core
# box: 64s Go-only -> 70s with the frontend included, i.e. full frontend
# coverage for ~6s rather than ~40s. Set WEB=0 to skip it.
#
# Usage:
#   scripts/run-module-tests.sh <go-test-flags...>
#     scripts/run-module-tests.sh -short -count=1
#     scripts/run-module-tests.sh -race -short -count=1
#     WEB=0 scripts/run-module-tests.sh -short -count=1   # Go modules only
#
# Output: one PASS/FAIL line per module (with wall time) as each finishes; a
# failing module's full output is printed at the end. Exits non-zero on any
# failure.
set -uo pipefail

MODULES=${MODULES:-". agent llm auth envvars invariant"}

# WEB controls the concurrent frontend gate. It is skipped automatically when
# the frontend directory is absent so this script still works in a checkout
# without it.
WEB=${WEB:-1}
WEB_DIR=${WEB_DIR:-cmd/serf-hub/frontend}
[ -d "$WEB_DIR" ] || WEB=0

if [ -z "${WAVE1+x}" ] && [ -z "${WAVE2+x}" ]; then
	# Wave 1 is the root module alone; wave 2 is everything else, concurrently.
	#
	# Giving root the machine to itself looks like idle capacity, but measured on
	# an idle 10-core box it is the faster arrangement. Moving the five small
	# modules into wave 1 (12.5s of work against root's ~21s window) slowed the
	# *total* gate from 64s to 78s: root's timing-sensitive TUI tests stretched
	# 21s -> 37s under the added contention, which cost more than the overlap
	# saved. Root is latency-sensitive, not throughput-bound — do not "fill" its
	# wave without re-measuring end to end.
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
#
# AGENT_PARALLEL is deliberately modest. The agent suite's real work is ~13s of
# user CPU, so wall time is flat from -parallel 6 up to 32 while kernel time
# doubles in scheduler churn — and at 32 a test's reported elapsed becomes mostly
# runqueue wait (the same suite "weighs" 451s instead of 99s), which makes any
# cost ranking derived from it useless. See scripts/agent-test-shards.sh.
# AGENT_SHARDS=0 runs the agent module as a single `go test` invocation instead of
# the sharded split. The -race gate uses it: under -race everything is ~10x
# slower and CPU-bound, so two shards just oversubscribe each other.
AGENT_SHARDS=${AGENT_SHARDS:-1}
ROOT_P=${ROOT_P-6}
AGENT_PARALLEL=${AGENT_PARALLEL-6}
AGENT_P=${AGENT_P-4}

# Fuzz-designated Test* functions are not part of the regular gate. Native Fuzz*
# targets are already excluded by -run; these names cover rapid/sequence fuzz
# tests and structured-generator reachability proofs that remain under make fuzz.
fuzz_test_skip='(SeqFuzz|SchemaFuzz|Structured.*Reach|LifecycleAdapter|ToolArgsAdapter|SeqAdapter|TurnPagingEquivalenceSanity|WireTypeRegistryCoverage|LineWindowExtractorsSanity|TranscriptReadersAgreeSanity|WriteListRoundTrip|LaunchConfigThreeStateRoundTrip|DifferentialSanity|StreamVsNonStreamSanity|FuzzBuildEnforces)'

flags="$*"
logdir="$(mktemp -d -t serf-module-tests.XXXXXX)"
fail=0

logpath() { printf '%s/%s.log' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')"; }

run_module() {
	local m="$1" extra="$2"
	# Word-split flags and extra intentionally so callers can pass multiple flags.
	# shellcheck disable=SC2086
	if [ "$m" = "." ]; then
		local -a packages=()
		local pkg
		while IFS= read -r pkg; do
			case "$pkg" in
				primeradiant.com/serf/cmd/serf-fuzzcov|primeradiant.com/serf/cmd/serf-fuzz-harvest)
					continue
					;;
			esac
			packages+=("$pkg")
		done < <(go list ./...)
		/usr/bin/time -p go test $flags $extra -run '^(Test|Example)' -skip "$fuzz_test_skip" "${packages[@]}"
		return
	fi
	if [ "$m" = "agent" ] && [ "$AGENT_SHARDS" -ne 0 ]; then
		# The agent module's wall time is dominated by its top-level package, one
		# binary holding ~3550 tests whose git-driving and CPU-bound halves want
		# opposite -parallel settings. agent-test-shards.sh runs those halves as
		# two concurrently-scheduled invocations of one prebuilt binary (~32s ->
		# ~26s). Its subpackages are small and already concurrent internally, but
		# they run AFTER the shards finish, not alongside them (~22s shards then
		# ~8s subpackages, sequential). Overlapping the two phases was measured
		# and made things worse: the agent module shares WAVE2 with five other
		# modules, so the added contention stretched the shard phase by more
		# than the overlap saved (see kata fgqh).
		local shardStatus=0
		(cd .. && ./scripts/agent-test-shards.sh $flags) || shardStatus=$?
		local subpkgs=()
		local pkg
		while IFS= read -r pkg; do
			[ "$pkg" = "primeradiant.com/serf/agent" ] || subpkgs+=("$pkg")
		done < <(go list ./...)
		if [ "${#subpkgs[@]}" -gt 0 ]; then
			/usr/bin/time -p go test $flags $extra -run '^(Test|Example)' -skip "$fuzz_test_skip" "${subpkgs[@]}" || shardStatus=$?
		fi
		return "$shardStatus"
	fi
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

# Start the frontend gate first so it runs across both Go waves. It is joined
# after wave 2, so its cost is hidden unless it outlives the Go work.
web_pid=""
web_failed=0
if [ "$WEB" -ne 0 ]; then
	/usr/bin/time -p "${MAKE:-make}" test-web >"$(logpath web)" 2>&1 &
	web_pid="$!"
fi

run_wave $WAVE1
run_wave $WAVE2

if [ -n "$web_pid" ]; then
	if wait "$web_pid"; then
		printf 'PASS  %-8s %s\n' "web" "$(awk '/^real /{print $2"s"}' "$(logpath web)" | tail -1)"
	else
		printf 'FAIL  %-8s\n' "web"; fail=1; web_failed=1
	fi
fi

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
	# The web gate's output is vitest/tsc/biome, not `go test`, so it is dumped
	# on its own exit status rather than by matching Go failure markers.
	if [ "$web_failed" -ne 0 ]; then
		echo "----- web -----"; cat "$(logpath web)"
	fi
	echo
	echo "full logs: $logdir"
fi

exit "$fail"
