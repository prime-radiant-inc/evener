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

# Fail fast, with a specific diagnosis, if the Data volume is too tight to
# safely build/test (kata 98x9). Every module-test path funnels through here
# (make test, test-short, test-race, and direct invocation), so this is the one
# place a check catches every run instead of depending on someone remembering
# to run scripts/disk-reclaim.sh first. Silent when there's nothing to say;
# a single `df`, so the cost is unmeasurable next to the rest of this script.
scripts/disk-reclaim.sh --check || exit 1

MODULES=${MODULES:-". agent llm auth envvars invariant identifier"}
ROOT_FULL=${ROOT_FULL:-0}

# WEB controls the concurrent frontend gate. It is skipped automatically when
# the frontend directory is absent so this script still works in a checkout
# without it.
WEB=${WEB:-1}
SELFTEST=${SELFTEST:-0}
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

# The frontend stream reports and logs under the fixed name "web", so scheduling
# a Go module of the same name hands one name two owners: two verdict lines
# under it, two streams writing one log file, and a failure whose output the
# other stream overwrites before anyone reads it (kata mjzx). The two are
# genuinely ambiguous, so refuse the run rather than report it twice. Checked
# against the waves, not MODULES, because WAVE1/WAVE2 override MODULES and both
# routes reach the same collision.
for m in $WAVE1 $WAVE2; do
	if [ "$WEB" -ne 0 ] && [ "$m" = "web" ]; then
		echo "run-module-tests.sh: 'web' is the frontend stream's name, not a Go module." >&2
		echo "run-module-tests.sh: run 'make test-web' for the frontend alone, or pass WEB=0 to test a Go module named web." >&2
		exit 2
	fi
	if [ "$SELFTEST" -ne 0 ] && [ "$m" = "selftest" ]; then
		echo "run-module-tests.sh: 'selftest' is the tooling stream's name, not a Go module." >&2
		echo "run-module-tests.sh: run 'make selftest' for tooling alone, or pass SELFTEST=0 to test a Go module named selftest." >&2
		exit 2
	fi
done

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

# Root discovery is normally quick, but it can block forever when the configured
# Go caches live on a stalled volume. Keep that failure bounded without changing
# cache configuration: the operator gets the configured cache paths and an exact
# repair/retry command instead. This must be a positive integer in seconds.
ROOT_PACKAGE_LIST_TIMEOUT=${SERF_ROOT_PACKAGE_LIST_TIMEOUT:-30}
if [[ ! "$ROOT_PACKAGE_LIST_TIMEOUT" =~ ^[1-9][0-9]*$ ]]; then
	printf 'run-module-tests.sh: SERF_ROOT_PACKAGE_LIST_TIMEOUT must be a positive integer in seconds (got %q)\n' "$ROOT_PACKAGE_LIST_TIMEOUT" >&2
	exit 2
fi

# Fuzz-designated Test* functions are not part of the regular gate. Native Fuzz*
# targets are already excluded by -run; these names cover rapid/sequence fuzz
# tests and structured-generator reachability proofs that remain under make fuzz.
fuzz_test_skip='(SeqFuzz|SchemaFuzz|Structured.*Reach|LifecycleAdapter|ToolArgsAdapter|SeqAdapter|TurnPagingEquivalenceSanity|WireTypeRegistryCoverage|LineWindowExtractorsSanity|TranscriptReadersAgreeSanity|WriteListRoundTrip|LaunchConfigThreeStateRoundTrip|DifferentialSanity|StreamVsNonStreamSanity|FuzzBuildEnforces)'

flags="$*"
module_test_flags() {
	local m="$1" flag selected=""
	if [ "$m" != "." ] || [ "$ROOT_FULL" -eq 0 ]; then
		printf '%s' "$flags"
		return
	fi
	for flag in $flags; do
		[ "$flag" = "-short" ] && continue
		selected="$selected $flag"
	done
	printf '%s' "${selected# }"
}

logdir=""
keep_failed_logs=0
# A signal can arrive while a stream is running through /usr/bin/time and a
# shell subshell, so the job PID alone is not enough to stop the actual test
# process. Keep every stream job here and snapshot its descendants on exit.
# This stays false until all streams have been joined, so unexpected exits keep
# their logs even when no stream has reported a test failure.
normal_completion=0
active_pids=()

forget_pid() {
	local pid="$1" i
	for i in ${!active_pids[@]+"${!active_pids[@]}"}; do
		[ "${active_pids[$i]}" = "$pid" ] && active_pids[$i]=""
	done
}

process_descendants() {
	local parent="$1" child
	for child in $(ps -axo pid=,ppid= 2>/dev/null | awk -v parent="$parent" '$2 == parent {print $1}'); do
		process_descendants "$child"
		printf '%s\n' "$child"
	done
}

stop_process_tree() {
	local pid="$1" descendant
	local -a descendants=()
	for descendant in $(process_descendants "$pid"); do
		descendants+=("$descendant")
	done
	for descendant in ${descendants[@]+"${descendants[@]}"} "$pid"; do
		[ -n "$descendant" ] && kill -TERM "$descendant" 2>/dev/null || :
	done
	wait "$pid" 2>/dev/null || :
}

stop_children() {
	local pid descendant
	local -a descendants=()
	for pid in ${active_pids[@]+"${active_pids[@]}"}; do
		[ -n "$pid" ] || continue
		for descendant in $(process_descendants "$pid"); do
			descendants+=("$descendant")
		done
	done
	for pid in ${descendants[@]+"${descendants[@]}"} ${active_pids[@]+"${active_pids[@]}"}; do
		[ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null || :
	done
	for pid in ${active_pids[@]+"${active_pids[@]}"}; do
		[ -n "$pid" ] && wait "$pid" 2>/dev/null || :
	done
	active_pids=()
}

cleanup() {
	stop_children
	[ "$normal_completion" -eq 1 ] || keep_failed_logs=1
	if [ -n "$logdir" ] && [ "$keep_failed_logs" -eq 0 ]; then
		rm -rf "$logdir"
	fi
}

trap cleanup EXIT

interrupted() {
	local status="$1" signal="$2"
	keep_failed_logs=1
	stop_children
	printf 'run-module-tests.sh: interrupted by %s\n' "$signal" >&2
	[ -n "$logdir" ] && printf 'full logs: %s\n' "$logdir" >&2
	exit "$status"
}

trap 'interrupted 129 SIGHUP' HUP
trap 'interrupted 130 SIGINT' INT
trap 'interrupted 143 SIGTERM' TERM

logdir="$(mktemp -d -t serf-module-tests.XXXXXX)"
fail=0
failed_modules=()

logpath() { printf '%s/%s.log' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')"; }

root_package_list_timeout_diagnostic() {
	local package_list="$1" worktree gocache gomodcache
	worktree="$(pwd -P)"
	gocache="$(go env GOCACHE 2>/dev/null || printf '<unavailable>')"
	gomodcache="$(go env GOMODCACHE 2>/dev/null || printf '<unavailable>')"
	printf 'run-module-tests.sh: go list ./... timed out after %ss.\n' "$ROOT_PACKAGE_LIST_TIMEOUT" >&2
	printf 'run-module-tests.sh: worktree/module: %s (.)\n' "$worktree" >&2
	printf 'run-module-tests.sh: effective GOCACHE: %s\n' "$gocache" >&2
	printf 'run-module-tests.sh: effective GOMODCACHE: %s\n' "$gomodcache" >&2
	printf 'run-module-tests.sh: retained package-list log: %s\n' "$package_list" >&2
	printf 'run-module-tests.sh: repair the configured caches and retry:\n' >&2
	printf '  GOCACHE=%q GOMODCACHE=%q go clean -cache -modcache && GOCACHE=%q GOMODCACHE=%q scripts/run-module-tests.sh -short -count=1\n' \
		"$gocache" "$gomodcache" "$gocache" "$gomodcache" >&2
}

run_root_package_list() {
	local package_list="$1" list_pid started_at list_status
	( go list ./... >"$package_list" 2>&1 ) &
	list_pid="$!"
	started_at=$SECONDS
	while kill -0 "$list_pid" 2>/dev/null; do
		if [ $((SECONDS - started_at)) -ge "$ROOT_PACKAGE_LIST_TIMEOUT" ]; then
			stop_process_tree "$list_pid"
			root_package_list_timeout_diagnostic "$package_list"
			return 1
		fi
		sleep 0.1
	done
	if wait "$list_pid"; then
		return 0
	else
		list_status=$?
		cat "$package_list" >&2
		return "$list_status"
	fi
}

run_module() {
	local m="$1" extra="$2" test_flags
	test_flags="$(module_test_flags "$m")"
	# Word-split flags and extra intentionally so callers can pass multiple flags.
	# shellcheck disable=SC2086
	if [ "$m" = "." ]; then
		local -a packages=()
		local pkg package_list
		package_list="$logdir/root.packages"
		run_root_package_list "$package_list" || return $?
		while IFS= read -r pkg; do
			case "$pkg" in
				primeradiant.com/serf/cmd/serf-fuzzcov|primeradiant.com/serf/cmd/serf-fuzz-harvest)
					continue
					;;
			esac
			packages+=("$pkg")
		done <"$package_list"
		if [ "${#packages[@]}" -eq 0 ]; then
			printf 'run-module-tests.sh: go list ./... returned no test packages\n' >&2
			return 1
		fi
		if [ "$ROOT_FULL" -ne 0 ]; then
			# ROOT_FULL removes the runner's regular name filter so the root
			# module uses go test's complete non-fuzz test surface. Fuzz-owned
			# sanity functions stay under the explicit make fuzz gate.
			/usr/bin/time -p go test $test_flags $extra -skip "$fuzz_test_skip" "${packages[@]}"
		else
			/usr/bin/time -p go test $test_flags $extra -run '^(Test|Example)' -skip "$fuzz_test_skip" "${packages[@]}"
		fi
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
		(cd .. && ./scripts/agent-test-shards.sh $test_flags) || shardStatus=$?
		local subpkgs=()
		local pkg
		while IFS= read -r pkg; do
			[ "$pkg" = "primeradiant.com/serf/agent" ] || subpkgs+=("$pkg")
		done < <(go list ./...)
		if [ "${#subpkgs[@]}" -gt 0 ]; then
			/usr/bin/time -p go test $test_flags $extra -run '^(Test|Example)' -skip "$fuzz_test_skip" "${subpkgs[@]}" || shardStatus=$?
		fi
		return "$shardStatus"
	fi
	/usr/bin/time -p go test $test_flags $extra -run '^(Test|Example)' -skip "$fuzz_test_skip" ./...
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
		pids+=("$!"); names+=("$m"); active_pids+=("$!")
	done
	local i
	for i in "${!pids[@]}"; do
		m="${names[$i]}"; log="$(logpath "$m")"
		if wait "${pids[$i]}"; then
			printf 'PASS  %-8s %s\n' "$m" "$(awk '/^real /{print $2"s"}' "$log" | tail -1)"
		else
			printf 'FAIL  %-8s\n' "$m"; fail=1; failed_modules+=("$m")
		fi
		forget_pid "${pids[$i]}"
	done
}

finish_stream() {
	local name="$1" pid="$2" log
	log="$(logpath "$name")"
	if wait "$pid"; then
		printf 'PASS  %-8s %s\n' "$name" "$(awk '/^real /{print $2"s"}' "$log" | tail -1)"
	else
		printf 'FAIL  %-8s\n' "$name"
		fail=1
		failed_modules+=("$name")
	fi
	forget_pid "$pid"
}

# Start the frontend gate first so it runs across both Go waves. It is joined
# after wave 2, so its cost is hidden unless it outlives the Go work.
web_pid=""
if [ "$WEB" -ne 0 ]; then
	/usr/bin/time -p "${MAKE:-make}" test-web >"$(logpath web)" 2>&1 &
	web_pid="$!"
	active_pids+=("$web_pid")
fi

run_wave $WAVE1

selftest_pid=""
if [ "$SELFTEST" -ne 0 ]; then
	/usr/bin/time -p "${MAKE:-make}" selftest >"$(logpath selftest)" 2>&1 &
	selftest_pid="$!"
	active_pids+=("$selftest_pid")
fi

run_wave $WAVE2

[ -n "$selftest_pid" ] && finish_stream selftest "$selftest_pid"
[ -n "$web_pid" ] && finish_stream web "$web_pid"

if [ "$fail" -ne 0 ]; then
	echo
	echo "=== failing module output ==="
	# Dump by verdict, not by matching failure markers in the log. A module can
	# fail with no `go test` marker anywhere in its output — a build error, a
	# missing directory, a killed process — and marker matching dropped exactly
	# those, leaving the verdicts with the most to explain with nothing at all
	# behind them (kata mjzx). The web gate's output is vitest/tsc/biome rather
	# than `go test`, and reads the same way here.
	for m in ${failed_modules[@]+"${failed_modules[@]}"}; do
		log="$(logpath "$m")"
		echo "----- $m -----"
		if [ -f "$log" ]; then
			cat "$log"
		else
			echo "(no output captured: $log is missing)"
		fi
	done
	echo
	echo "full logs: $logdir"
	keep_failed_logs=1
fi

normal_completion=1
exit "$fail"
