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

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
. "$script_dir/../lib/private-go-home.sh"
. "$script_dir/../lib/scratch-lib.sh"

# The load-aware budgets, and the effective -p/-parallel flags they become,
# live in scripts/lib/gate-budgets.sh so the wiring can be exercised directly
# instead of matched as script text. That library is part of this script's own
# commit, so failing to source it is a broken checkout and refusing is better
# than running the whole gate unbudgeted — and the source status is checked
# rather than just readability, because a failure while sourcing would otherwise
# leave the budget functions undefined and the flags silently empty. The helper
# it sizes through is guarded inside gate_source_helper: an unreadable helper
# degrades the budgets to their historical fixed values, and a budget left empty
# would be read by the -p guards as "pass no flag" and widened to go's
# GOMAXPROCS.
gate_budgets_lib="$script_dir/../lib/gate-budgets.sh"
if ! . "$gate_budgets_lib"; then
	printf 'run-module-tests.sh: cannot source %s; refusing to run unbudgeted\n' "$gate_budgets_lib" >&2
	exit 2
fi
gate_source_helper "$script_dir/../lib/load-aware-workers.sh"

MODULES=${MODULES:-". agent llm auth envvars invariant identifier"}
ROOT_FULL=${ROOT_FULL:-0}

# WEB controls the concurrent frontend gate. It is skipped automatically when
# the frontend directory is absent so this script still works in a checkout
# without it.
WEB=${WEB:-1}
WEB_DIR=${WEB_DIR:-cmd/evener-hub/frontend}
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
done

# Package/test parallelism controls for heavyweight modules. Explicit empty
# values mean "don't pass the flag" so go test uses its defaults; the -race gate
# explicitly keeps AGENT_PARALLEL at 6 to prevent host-wide concurrency from
# oversubscribing the agent test binary on high-core machines.
#
# AGENT_PARALLEL is deliberately modest. The agent suite's real work is ~13s of
# user CPU, so wall time is flat from -parallel 6 up to 32 while kernel time
# doubles in scheduler churn — and at 32 a test's reported elapsed becomes mostly
# runqueue wait (the same suite "weighs" 451s instead of 99s), which makes any
# cost ranking derived from it useless. See cmd/evener-dev/agentshards.go.
# AGENT_SHARDS=0 runs the agent module as a single `go test` invocation instead of
# the sharded split. The -race gate uses it: under -race everything is ~10x
# slower and CPU-bound, so two shards just oversubscribe each other.
AGENT_SHARDS=${AGENT_SHARDS:-1}
# The agent module's test count has grown past the point where 4 shards
# (the agentshards default) keep each shard's -run pattern under the OS
# argument-list limit. The shard runner now writes the -run regex to a file
# and hands the path via EVENER_SHARD_RUN_FILE (read by the test binary's
# TestMain), so the pattern never touches the execve argument list. 8 shards
# keep cost-balanced packing from putting too many cheap tests in one shard.
export AGENT_SHARD_COUNT=${AGENT_SHARD_COUNT:-8}

# These defaults are load-aware (scripts/lib/load-aware-workers.sh), not fixed.
# On an idle machine they are the historical budgets, 6/6/4 plus go's own
# default -p, so the wave design above is unchanged. As the 1-minute load
# average rises they shrink toward one, because this script is only one of
# several gate runs a busy host may be executing at once: agent worktree
# sessions, CI, and a hand-run `make test` all reach here, and a fixed budget
# let each of them claim the whole machine. An explicit environment override
# still wins, so test-race's AGENT_PARALLEL=6 is honored as written.
#
# The agent-shards runner does the agent module's real work and reads its own
# parallelism from the environment; AGENT_PARALLEL never reaches it. Without
# these the dominant agent workload stayed at a fixed width under load. The
# caps are the runner's own defaults, and a set value still wins.
gate_init_budgets
# Modules with no explicit -p are deliberately left alone. Go's default -p is
# GOMAXPROCS, which is cgroup-quota aware; an explicit -p derived from the
# host's online CPUs would oversubscribe a CPU-limited container and override a
# user-lowered GOMAXPROCS. The three budgets above only ever tighten values
# this script already passed.

# Root discovery is normally quick, but it can block forever when the configured
# Go caches live on a stalled volume. Keep that failure bounded without changing
# cache configuration: the operator gets the configured cache paths and an exact
# repair/retry command instead. This must be a positive integer in seconds.
ROOT_PACKAGE_LIST_TIMEOUT=${EVENER_ROOT_PACKAGE_LIST_TIMEOUT:-30}
if [[ ! "$ROOT_PACKAGE_LIST_TIMEOUT" =~ ^[1-9][0-9]*$ ]]; then
	printf 'run-module-tests.sh: EVENER_ROOT_PACKAGE_LIST_TIMEOUT must be a positive integer in seconds (got %q)\n' "$ROOT_PACKAGE_LIST_TIMEOUT" >&2
	exit 2
fi

# Deriving the enumeration flags runs `go run`, which compiles evener-dev before
# it prints anything. That is heavier than the `go list` those flags feed, so it
# gets its own, larger bound: enough for a cold build cache on a busy host, but
# still finite, so a stalled cache fails with the diagnostic instead of hanging
# the gate before its own timeout can speak.
LIST_BUILD_FLAGS_TIMEOUT=${EVENER_LIST_BUILD_FLAGS_TIMEOUT:-300}
if [[ ! "$LIST_BUILD_FLAGS_TIMEOUT" =~ ^[1-9][0-9]*$ ]]; then
	printf 'run-module-tests.sh: EVENER_LIST_BUILD_FLAGS_TIMEOUT must be a positive integer in seconds (got %q)\n' "$LIST_BUILD_FLAGS_TIMEOUT" >&2
	exit 2
fi

# The gate's test-selection surface lives in one shared file so the coverage
# ratchet can measure exactly what this gate proves; see gate-surface-lib.sh.
. "$(dirname "${BASH_SOURCE[0]}")/../lib/gate-surface-lib.sh"
fuzz_test_skip="$GATE_FUZZ_TEST_SKIP"

root_skip="$fuzz_test_skip"

flags="$*"
# The caller's argv, preserved for the two things that need values intact:
# evener-dev decides which flags the enumeration also needs, and a value with a
# space in it must reach it whole.
gate_args=("$@")
repo_root="$(CDPATH='' cd -- "$script_dir/../.." && pwd)"

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
	if [ "${#active_pids[@]}" -gt 0 ]; then
		for i in "${!active_pids[@]}"; do
			[ "${active_pids[$i]}" = "$pid" ] && active_pids[$i]=""
		done
	fi
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
	if [ "${#descendants[@]}" -gt 0 ]; then
		for descendant in "${descendants[@]}"; do
			[ -n "$descendant" ] && kill -TERM "$descendant" 2>/dev/null || :
		done
	fi
	kill -TERM "$pid" 2>/dev/null || :
	wait "$pid" 2>/dev/null || :
}

stop_children() {
	local pid descendant
	local -a descendants=()
	if [ "${#active_pids[@]}" -gt 0 ]; then
		for pid in "${active_pids[@]}"; do
			[ -n "$pid" ] || continue
			for descendant in $(process_descendants "$pid"); do
				descendants+=("$descendant")
			done
		done
	fi
	if [ "${#descendants[@]}" -gt 0 ]; then
		for pid in "${descendants[@]}"; do
			[ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null || :
		done
	fi
	if [ "${#active_pids[@]}" -gt 0 ]; then
		for pid in "${active_pids[@]}"; do
			[ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null || :
		done
		for pid in "${active_pids[@]}"; do
			[ -n "$pid" ] && wait "$pid" 2>/dev/null || :
		done
	fi
	active_pids=()
}

cleanup() {
	stop_children
	[ "$normal_completion" -eq 1 ] || keep_failed_logs=1
	if [ "$keep_failed_logs" -eq 0 ]; then
		scratch_rm
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

scratch_dir logdir evener-module-tests
fail=0
failed_modules=()

logpath() { printf '%s/%s.log' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')"; }
tmppath() { printf '%s/%s/%s' "$logdir" tmp "$(printf '%s' "$1" | tr '/.' '__')"; }

# package_list_timeout_diagnostic <what> <bound> <module> <log-file> — the
# cache-stall diagnostic for a bounded step. It names the module the step ran in
# and a retry command anchored at the repository root, so a timeout in the flag
# derivation or in any module's enumeration reads the same way.
package_list_timeout_diagnostic() {
	local what="$1" bound="$2" module="$3" log_file="$4" worktree gocache gomodcache retry
	worktree="$(pwd -P)"
	gocache="$(go env GOCACHE 2>/dev/null || printf '<unavailable>')"
	gomodcache="$(go env GOMODCACHE 2>/dev/null || printf '<unavailable>')"
	retry="$repo_root/scripts/gate/run-module-tests.sh"
	printf 'run-module-tests.sh: %s timed out after %ss.\n' "$what" "$bound" >&2
	printf 'run-module-tests.sh: worktree: %s (module %s)\n' "$worktree" "$module" >&2
	printf 'run-module-tests.sh: effective GOCACHE: %s\n' "$gocache" >&2
	printf 'run-module-tests.sh: effective GOMODCACHE: %s\n' "$gomodcache" >&2
	printf 'run-module-tests.sh: retained log: %s\n' "$log_file" >&2
	printf 'run-module-tests.sh: repair the configured caches and retry:\n' >&2
	printf '  GOCACHE=%q GOMODCACHE=%q go clean -cache -modcache && GOCACHE=%q GOMODCACHE=%q %q -short -count=1\n' \
		"$gocache" "$gomodcache" "$gocache" "$gomodcache" "$retry" >&2
}

# run_bounded <bound> <what> <module> <log-file> <cmd...> — run cmd in the
# background, capture its stdout in log-file, and bound it: a step that outlives
# <bound> has its whole process tree stopped and gets the cache diagnostic,
# rather than hanging the gate. stderr is kept as <log-file>.stderr and replayed
# on a nonzero exit. A stalled Go cache or filesystem must never hang the gate.
run_bounded() {
	local bound="$1" what="$2" module="$3" log_file="$4"; shift 4
	local pid started_at status
	"$@" >"$log_file" 2>"${log_file}.stderr" &
	pid="$!"
	started_at=$SECONDS
	while kill -0 "$pid" 2>/dev/null; do
		if [ $((SECONDS - started_at)) -ge "$bound" ]; then
			stop_process_tree "$pid"
			package_list_timeout_diagnostic "$what" "$bound" "$module" "${log_file}.stderr"
			return 1
		fi
		sleep 0.1
	done
	if wait "$pid"; then
		return 0
	else
		status=$?
		cat "${log_file}.stderr" >&2
		return "$status"
	fi
}

# run_list_build_flags runs the helper from the repository root, where the
# evener-dev package resolves, whatever module's directory the caller sits in.
run_list_build_flags() {
	( cd "$repo_root" && go run ./cmd/evener-dev/bin dev list-build-flags -- ${gate_args[@]+"${gate_args[@]}"} )
}

# derive_list_flags sets list_flags to the caller's flags that the `go list`
# enumeration also needs, one per line from evener-dev in name=value form, so a
# value with a space cannot be split and an empty value cannot vanish. It runs
# only on the enumeration paths (root and sharded agent), under its own bound:
# this `go run` compiles evener-dev first, and a stalled GOCACHE/GOMODCACHE must
# not hang the gate before its own timeout diagnostic can speak.
derive_list_flags() {
	local module="$1" out_file list_flag
	out_file="$logdir/$(printf '%s' "$module" | tr '/.' '__').list-build-flags"
	if ! run_bounded "$LIST_BUILD_FLAGS_TIMEOUT" 'evener-dev list-build-flags' "$module" "$out_file" run_list_build_flags; then
		printf 'run-module-tests.sh: could not derive the package-selection flags for go list\n' >&2
		return 1
	fi
	list_flags=()
	while IFS= read -r list_flag; do
		[ -n "$list_flag" ] && list_flags+=("$list_flag")
	done <"$out_file"
	return 0
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
		derive_list_flags "$m" || return $?
		run_bounded "$ROOT_PACKAGE_LIST_TIMEOUT" 'go list ./...' "$m" "$package_list" go list ${list_flags[@]+"${list_flags[@]}"} ./... || return $?
		while IFS= read -r pkg; do
			case "$pkg" in
				primeradiant.com/evener/cmd/evener-fuzzcov|primeradiant.com/evener/cmd/evener-fuzz-harvest)
					continue
					;;
			esac
			packages+=("$pkg")
		done <"$package_list"
		if [ "${#packages[@]}" -eq 0 ]; then
			printf 'run-module-tests.sh: go list ./... returned no test packages\n' >&2
			return 1
		fi
		# ROOT_FULL removes short mode through module_test_flags while retaining
		# the regular Test/Example name filter. Fuzz-owned targets and sanity
		# functions stay under the explicit make fuzz gate.
		/usr/bin/time -p go test $test_flags $extra -run "$GATE_TEST_RUN" -skip "$root_skip" "${packages[@]}"
		return
	fi
	if [ "$m" = "agent" ] && [ "$AGENT_SHARDS" -ne 0 ]; then
		# The agent module's wall time is dominated by its top-level package, one
		# binary holding ~3550 tests whose git-driving and CPU-bound halves want
		# opposite -parallel settings. evener dev agent-shards runs those halves as
		# two concurrently-scheduled invocations of one prebuilt binary (~32s ->
		# ~26s). Its subpackages are small and already concurrent internally, but
		# they run AFTER the shards finish, not alongside them (~22s shards then
		# ~8s subpackages, sequential). Overlapping the two phases was measured
		# and made things worse: the agent module shares WAVE2 with five other
		# modules, so the added contention stretched the shard phase by more
		# than the overlap saved (see kata fgqh).
		local shardStatus=0
		# `go run` collapses its child's exit code to 1 and reports the real
		# one as an "exit status N" line on stderr, so the runner's 129/130/143
		# signal exits survive in the binary but not through this call. Only
		# zero-vs-nonzero is read below, so nothing here depends on them.
		(cd .. && go run ./cmd/evener-dev/bin dev agent-shards $test_flags) || shardStatus=$?
		derive_list_flags "$m" || return $?
		local subpkgs=()
		local pkg agent_list
		agent_list="$logdir/agent.packages"
		# Same bound and exit-status check as the root enumeration: a failed or
		# hung go list must not pass as "no subpackages" over an already-green
		# shard run.
		run_bounded "$ROOT_PACKAGE_LIST_TIMEOUT" 'go list ./...' "$m" "$agent_list" go list ${list_flags[@]+"${list_flags[@]}"} ./... || return $?
		while IFS= read -r pkg; do
			[ -n "$pkg" ] || continue
			[ "$pkg" = "primeradiant.com/evener/agent" ] || subpkgs+=("$pkg")
		done <"$agent_list"
		if [ "${#subpkgs[@]}" -gt 0 ]; then
			/usr/bin/time -p go test $test_flags $extra -run "$GATE_TEST_RUN" -skip "$fuzz_test_skip" "${subpkgs[@]}" || shardStatus=$?
		fi
		return "$shardStatus"
	fi
	/usr/bin/time -p go test $test_flags $extra -run "$GATE_TEST_RUN" -skip "$fuzz_test_skip" ./...
}

# run_wave <module...> — run the modules concurrently, wait, and report each
# one's result; records failures in the global $fail.
run_wave() {
	[ "$#" -eq 0 ] && return 0
	local -a names=() pids=()
	local m log extra tmp
	for m in "$@"; do
		log="$(logpath "$m")"
		extra="$(gate_module_flags "$m")"
		tmp="$(tmppath "$m")"
		( mkdir -p "$tmp" && export TMPDIR="$tmp" && evener_prepare_private_go_home "$tmp" && cd "$m" && run_module "$m" "$extra" ) >"$log" 2>&1 &
		pids+=("$!"); names+=("$m"); active_pids+=("$!")
	done
	local i status
	for i in "${!pids[@]}"; do
		m="${names[$i]}"; log="$(logpath "$m")"
		if wait "${pids[$i]}"; then
			status=0
		else
			status=$?
		fi
		forget_pid "${pids[$i]}"
		if [ "$status" -eq 0 ]; then
			printf 'PASS  %-8s %s\n' "$m" "$(awk '/^real /{print $2"s"}' "$log" | tail -1)"
		else
			printf 'FAIL  %-8s\n' "$m"; fail=1; failed_modules+=("$m")
		fi
	done
}

finish_stream() {
	local name="$1" pid="$2" log status
	log="$(logpath "$name")"
	if wait "$pid"; then
		status=0
	else
		status=$?
	fi
	forget_pid "$pid"
	if [ "$status" -eq 0 ]; then
		printf 'PASS  %-8s %s\n' "$name" "$(awk '/^real /{print $2"s"}' "$log" | tail -1)"
	else
		printf 'FAIL  %-8s\n' "$name"
		fail=1
		failed_modules+=("$name")
	fi
}

# Start the frontend gate first so it runs across both Go waves. It is joined
# after wave 2, so its cost is hidden unless it outlives the Go work.
web_pid=""
if [ "$WEB" -ne 0 ]; then
	web_tmp="$(tmppath web)"
	( mkdir -p "$web_tmp" && TMPDIR="$web_tmp" XDG_CONFIG_HOME="$web_tmp/xdg-config" XDG_CACHE_HOME="$web_tmp/xdg-cache" XDG_STATE_HOME="$web_tmp/xdg-state" /usr/bin/time -p "${MAKE:-make}" test-web ) >"$(logpath web)" 2>&1 &
	web_pid="$!"
	active_pids+=("$web_pid")
fi

run_wave $WAVE1

run_wave $WAVE2

[ -n "$web_pid" ] && finish_stream web "$web_pid"

# A -run pattern that matches no test name is not an error to `go test`: every
# package reports "[no tests to run]" and exits 0, so every module reports PASS
# and the gate proves nothing. Go's own per-package status lines are the only
# evidence available here - a package that executed tests prints "ok <pkg>
# <time>" with no "[no tests to run]"/"[no test files]" note. If not one
# scheduled module has such a line, the run was a silent no-op. Checked only
# when nothing else failed (a failure already tells the reader to look) and only
# when Go work was actually scheduled (an explicitly web-only run has no Go
# tests to account for).
zero_test_run=0
if [ "$fail" -eq 0 ] && [ -n "$WAVE1$WAVE2" ]; then
	zero_test_run=1
	for m in $WAVE1 $WAVE2; do
		log="$(logpath "$m")"
		[ -f "$log" ] || continue
		if grep -E '^ok[[:space:]]' "$log" | grep -qv -e '\[no tests to run\]' -e '\[no test files\]'; then
			zero_test_run=0
			break
		fi
	done
fi

if [ "$fail" -ne 0 ]; then
	echo
	echo "=== failing module output ==="
	# Dump by verdict, not by matching failure markers in the log. A module can
	# fail with no `go test` marker anywhere in its output — a build error, a
	# missing directory, a killed process — and marker matching dropped exactly
	# those, leaving the verdicts with the most to explain with nothing at all
	# behind them (kata mjzx). The web gate's output is vitest/tsc/biome rather
	# than `go test`, and reads the same way here.
	for m in "${failed_modules[@]}"; do
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

if [ "$zero_test_run" -ne 0 ]; then
	echo
	echo "run-module-tests.sh: the Go waves ran zero tests, so this run proves nothing."
	echo "run-module-tests.sh: no scheduled module reported a package that executed any test."
	echo "full logs: $logdir"
	keep_failed_logs=1
	fail=1
fi

normal_completion=1
exit "$fail"
