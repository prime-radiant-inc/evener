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
#   scripts/gate/run-module-tests.sh <go-test-flags...>
#     scripts/gate/run-module-tests.sh -short -count=1
#     scripts/gate/run-module-tests.sh -race -short -count=1
#     WEB=0 scripts/gate/run-module-tests.sh -short -count=1   # Go modules only
#
# Every `go list ./...` this script runs to enumerate a module's packages — the
# root module's, and the agent module's subpackage list — is bounded:
# EVENER_ROOT_PACKAGE_LIST_TIMEOUT
# seconds per attempt (default 60) over EVENER_ROOT_PACKAGE_LIST_ATTEMPTS
# attempts (default 3), a timed-out attempt being the only one retried. Raise
# the per-attempt budget on a host slower than that; the failure diagnostic
# names both knobs. A timed-out attempt is stopped by process group, SIGTERM
# then SIGKILL, and reaped before the next one starts; an attempt that will not
# stop fails the run, naming its surviving pids and waiting on none of them,
# instead of being retried. Each attempt writes its own package list and only a
# completed one is used.
#
# Each of those attempts is exec'd through perl so it lands in its own process
# group and can be stopped as one, so perl has to be on PATH. It is on macOS
# and on the CI image; setsid(1), the usual tool for this, is not on macOS.
#
# Output: one PASS/FAIL line per module (with wall time) as each finishes; a
# failing module's full output is printed at the end. Exits non-zero on any
# failure.
set -uo pipefail

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
. "$script_dir/../lib/private-go-home.sh"
. "$script_dir/../lib/scratch-lib.sh"

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
ROOT_P=${ROOT_P-6}
AGENT_PARALLEL=${AGENT_PARALLEL-6}
AGENT_P=${AGENT_P-4}

# Root discovery is normally quick, and two different things make it slow: the
# configured Go caches can live on a stalled volume, where it blocks forever,
# and a cold, loaded CI runner can simply take longer than a tight budget. One
# 30s attempt could not tell those apart, so a merely slow runner failed the
# whole gate before a test ran and was told to clean its caches (GitHub run
# 34639098143). Each attempt now gets its own budget and a timed-out attempt is
# retried — the killed attempt still warmed GOCACHE/GOMODCACHE for the next one
# — while a run that never completes fails exactly as before, with the
# configured cache paths, the retained stderr log, and a repair command.
#
# Both must be positive integers. The worst case is ATTEMPTS x (TIMEOUT + two
# stop graces) plus a second of backoff between attempts, so the defaults below
# give a ~212s ceiling.
ROOT_PACKAGE_LIST_TIMEOUT=${EVENER_ROOT_PACKAGE_LIST_TIMEOUT:-60}
if [[ ! "$ROOT_PACKAGE_LIST_TIMEOUT" =~ ^[1-9][0-9]*$ ]]; then
	printf 'run-module-tests.sh: EVENER_ROOT_PACKAGE_LIST_TIMEOUT must be a positive integer in seconds (got %q)\n' "$ROOT_PACKAGE_LIST_TIMEOUT" >&2
	exit 2
fi
ROOT_PACKAGE_LIST_ATTEMPTS=${EVENER_ROOT_PACKAGE_LIST_ATTEMPTS:-3}
if [[ ! "$ROOT_PACKAGE_LIST_ATTEMPTS" =~ ^[1-9][0-9]*$ ]]; then
	printf 'run-module-tests.sh: EVENER_ROOT_PACKAGE_LIST_ATTEMPTS must be a positive integer (got %q)\n' "$ROOT_PACKAGE_LIST_ATTEMPTS" >&2
	exit 2
fi
# Seconds to wait for a stopped attempt's process group to empty after each of
# SIGTERM and SIGKILL. Only a member that ignores or cannot take the signal
# reaches the end of either wait, so the ordinary stop costs milliseconds. Not
# an environment knob: nothing a caller does should be able to shorten the
# window that proves the attempt is gone.
ROOT_PACKAGE_LIST_STOP_GRACE=5

# The gate's test-selection surface lives in one shared file so the coverage
# ratchet can measure exactly what this gate proves; see gate-surface-lib.sh.
. "$(dirname "${BASH_SOURCE[0]}")/../lib/gate-surface-lib.sh"
fuzz_test_skip="$GATE_FUZZ_TEST_SKIP"

root_skip="$fuzz_test_skip"

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

# A retried package list has to outlive the wave subshell that saw it: its
# stderr goes to the module log, which a green run deletes, so a run that only
# passed because of the retry would report a clean PASS and say nothing about
# the host that needed it. Each retry appends its line to the module's retry
# file and the report replays it.
logpath() { printf '%s/%s.log' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')"; }
tmppath() { printf '%s/%s/%s' "$logdir" tmp "$(printf '%s' "$1" | tr '/.' '__')"; }

# Where a module's enumerated package list lives, and where the notices from
# any retried attempt at producing it are recorded. Both the producer
# (run_bounded_package_list) and the reporter need the same mapping, so it is
# spelled once. The root module keeps the name it has always had.
package_list_path() {
	case "$1" in
	.) printf '%s/root.packages' "$logdir" ;;
	*) printf '%s/%s.packages' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')" ;;
	esac
}
package_list_retry_path() { printf '%s.retries' "$(package_list_path "$1")"; }

# package_list_timeout_diagnostic LOG ATTEMPTS_MADE MODULE — the failure report.
# ATTEMPTS_MADE is spelled out because the run can stop short of the budget: an
# attempt that will not die ends the run on the spot, and claiming every attempt
# timed out would misdescribe it.
package_list_timeout_diagnostic() {
	local package_list_log="$1" attempts_made="$2" module="$3"
	local worktree gocache gomodcache kept attempt_file
	worktree="$(pwd -P)"
	gocache="$(go env GOCACHE 2>/dev/null || printf '<unavailable>')"
	gomodcache="$(go env GOMODCACHE 2>/dev/null || printf '<unavailable>')"
	if [ "$attempts_made" -ge "$ROOT_PACKAGE_LIST_ATTEMPTS" ]; then
		printf 'run-module-tests.sh: go list ./... timed out after %ss on each of %s attempts.\n' \
			"$ROOT_PACKAGE_LIST_TIMEOUT" "$ROOT_PACKAGE_LIST_ATTEMPTS" >&2
	else
		printf 'run-module-tests.sh: go list ./... timed out after %ss on attempt %s of %s.\n' \
			"$ROOT_PACKAGE_LIST_TIMEOUT" "$attempts_made" "$ROOT_PACKAGE_LIST_ATTEMPTS" >&2
	fi
	printf 'run-module-tests.sh: worktree/module: %s (%s)\n' "$worktree" "$module" >&2
	printf 'run-module-tests.sh: effective GOCACHE: %s\n' "$gocache" >&2
	printf 'run-module-tests.sh: effective GOMODCACHE: %s\n' "$gomodcache" >&2
	printf 'run-module-tests.sh: retained package-list log: %s\n' "$package_list_log" >&2
	# The partial lists each stopped attempt wrote are the other half of the
	# evidence, and nothing else in the output names them: a stalled host's
	# half-written package list is what says how far discovery got. They are
	# kept, never removed, because a hard failure retains the whole log
	# directory.
	kept=""
	for attempt_file in "${package_list_log%.stderr}".attempt*; do
		[ -e "$attempt_file" ] || continue
		kept="$kept $attempt_file"
	done
	kept="${kept# }"
	printf 'run-module-tests.sh: retained partial package lists: %s\n' "${kept:-<none written>}" >&2
	printf 'run-module-tests.sh: a stalled cache volume is one cause; a host slower than the per-attempt budget is the other.\n' >&2
	printf 'run-module-tests.sh: repair the configured caches and retry:\n' >&2
	printf '  GOCACHE=%q GOMODCACHE=%q go clean -cache -modcache && GOCACHE=%q GOMODCACHE=%q scripts/gate/run-module-tests.sh -short -count=1\n' \
		"$gocache" "$gomodcache" "$gocache" "$gomodcache" >&2
	printf 'run-module-tests.sh: or give a slow host more room per attempt:\n' >&2
	printf '  EVENER_ROOT_PACKAGE_LIST_TIMEOUT=%s scripts/gate/run-module-tests.sh -short -count=1\n' \
		"$((ROOT_PACKAGE_LIST_TIMEOUT * 2))" >&2
}

# package_list_group_survivors PGID — print `pid(state)` for every live
# member of PGID, zombies excluded. Exit status 0 means the printed answer is
# trustworthy; 2 means the process listing itself failed, so nothing is known
# about the group and an empty answer must NOT be read as "gone".
#
# Zombies have to be excluded, and `kill -0 -- -PGID` cannot do it. A process
# the kernel has finished with stays a member of its own group until its parent
# reaps it, and a group signal is reported as delivered to it, so the leader
# this function is asked about would read as "still running" for as long as the
# shell had not got round to reaping it — a successful kill reported as a
# survivor, purely on reap timing. Asking `ps` for the state instead makes the
# answer independent of when anyone reaps.
#
# The status is separate from the output because the two failures look
# identical otherwise. A `ps` that cannot run prints nothing, and a caller
# reading that as "no live members" starts the next attempt while the timed-out
# one is still writing its package list and holding Go's cache locks — the
# exact race the stop exists to prevent.
package_list_group_survivors() {
	local pgid="$1" listing
	if ! listing="$(ps -axo pid=,pgid=,state= 2>/dev/null)" || [ -z "$listing" ]; then
		return 2
	fi
	printf '%s\n' "$listing" |
		awk -v pgid="$pgid" '$2 == pgid && $3 !~ /^[Zz]/ { printf "%s(%s) ", $1, $3 }'
}

# stop_package_list_group PGID — stop one package-list attempt and prove
# nothing of it is still running. Returns 0 when the group is confirmed empty,
# 1 when it still has a live member after the escalation, and 2 when liveness
# could not be determined at all. Only 0 permits a retry: both non-zero answers
# mean the caller cannot show the attempt is gone.
#
# By group, not by process tree: a snapshot of descendants plus a signal to
# what it showed misses a child forked after the snapshot, and one that
# outlives the leader survives into the next attempt — still writing a package
# list, still holding Go's build and module cache locks. A group signal reaches
# every member however late it appeared.
#
# The group is made by the spawn, not here: the attempt is exec'd through
# perl's setpgrp(0, 0), so the child becomes its own group leader and its pgid
# is its pid — which is why the caller can pass the pid it already holds.
# setsid(1) is the usual tool for that and is not present on macOS, which this
# script has to run on; perl is, and so is the CI image's.
stop_package_list_group() {
	local pgid="$1" signal waited ticks alive
	ticks=$((ROOT_PACKAGE_LIST_STOP_GRACE * 10))
	for signal in TERM KILL; do
		kill -"$signal" -- -"$pgid" 2>/dev/null || :
		waited=0
		while [ "$waited" -lt "$ticks" ]; do
			alive="$(package_list_group_survivors "$pgid")" || return 2
			if [ -z "$alive" ]; then
				return 0
			fi
			sleep 0.1
			waited=$((waited + 1))
		done
	done
	alive="$(package_list_group_survivors "$pgid")" || return 2
	if [ -z "$alive" ]; then
		return 0
	fi
	return 1
}

# run_bounded_package_list MODULE OUTPUT — enumerate MODULE's packages into
# OUTPUT under the bound. MODULE is the runner's name for the module (".", or a
# directory) and is used for the retry file and the diagnostic; the enumeration
# itself is `go list ./...` in the current directory, which the caller has
# already changed to that module.
run_bounded_package_list() {
	local module="$1" package_list="$2" package_list_stderr attempt attempt_list
	local list_pid list_pgid started_at list_status descendant survivors stop_status
	package_list_stderr="${package_list}.stderr"
	# Every attempt appends under its own heading, so the diagnostic still names
	# one retained log and whoever reads it sees what each attempt said.
	: >"$package_list_stderr"
	attempt=1
	while :; do
		# Each attempt writes its own file and only a completed one is promoted
		# to the path the rest of the script reads. A survivor of a stopped
		# attempt keeps writing to the file it opened, which no later attempt
		# names and nothing ever reads.
		attempt_list="${package_list}.attempt${attempt}"
		printf '=== go list ./... attempt %s of %s ===\n' "$attempt" "$ROOT_PACKAGE_LIST_ATTEMPTS" >>"$package_list_stderr"
		# The attempt is spawned into its own process group so that
		# stop_package_list_group can stop it as one. perl's setpgrp(0, 0)
		# does that inside the child, between fork and exec, where this shell
		# cannot see it. `set -m` would also have made the job its own group, but
		# only by turning job control on for the whole script: run_wave backgrounds
		# every module and stream with `( ... ) &` and records the pids in
		# active_pids, and stop_children and cleanup signal and wait on exactly
		# those — all of it shaped by whether monitor mode is on. Nothing here is
		# worth making the rest of the script run under different job semantics.
		perl -e 'setpgrp(0, 0); exec @ARGV or die "exec: $!\n"' \
			-- go list ./... >"$attempt_list" 2>>"$package_list_stderr" &
		list_pid="$!"
		started_at=$SECONDS
		while kill -0 "$list_pid" 2>/dev/null; do
			if [ $((SECONDS - started_at)) -ge "$ROOT_PACKAGE_LIST_TIMEOUT" ]; then
				if ! kill -0 "$list_pid" 2>/dev/null; then
					# It finished inside the last poll interval. Nothing to
					# stop; take the completion path below.
					break
				fi
				# Read the job's real process group only now, and only for a
				# process just confirmed alive. The child sets its own group
				# after the fork, so a read taken at spawn time races it and
				# would report the runner's own group — which is precisely the
				# group no signal below may ever be aimed at.
				list_pgid="$(ps -o pgid= -p "$list_pid" 2>/dev/null | tr -d '[:space:]')"
				if [ "$list_pgid" != "$list_pid" ]; then
					# No group to name, so signal what a snapshot shows and
					# stop. Nothing is waited on here: a retry would race
					# whatever is left, and a process wedged in
					# uninterruptible sleep would never be reaped, which
					# would hang the gate instead of failing it.
					for descendant in $(process_descendants "$list_pid"); do
						kill -KILL "$descendant" 2>/dev/null || :
					done
					kill -KILL "$list_pid" 2>/dev/null || :
					package_list_timeout_diagnostic "$package_list_stderr" "$attempt" "$module"
					printf 'run-module-tests.sh: attempt %s is not its own process group (pgid %s, pid %s), so it cannot be stopped as one. Not retrying.\n' \
						"$attempt" "${list_pgid:-<unreadable>}" "$list_pid" >&2
					return 1
				fi
				stop_status=0
				stop_package_list_group "$list_pid" || stop_status=$?
				if [ "$stop_status" -ne 0 ]; then
					# Deliberately no wait: SIGKILL does not land on a
					# process in uninterruptible sleep, which is exactly the
					# stalled-volume case this bound exists for, and waiting
					# on it would replace the bound with an indefinite hang.
					# Name what is known instead and fail.
					package_list_timeout_diagnostic "$package_list_stderr" "$attempt" "$module"
					if [ "$stop_status" -eq 2 ]; then
						printf 'run-module-tests.sh: attempt %s cannot be shown to have stopped: the process listing that answers "is process group %s empty" would not run. Not retrying, because a retry that cannot see the previous attempt would race it.\n' \
							"$attempt" "$list_pid" >&2
					else
						survivors="$(package_list_group_survivors "$list_pid")" || survivors=""
						printf 'run-module-tests.sh: attempt %s would not stop: process group %s still holds %s after SIGTERM and SIGKILL with %ss of grace each. Not retrying, and not waiting on it.\n' \
							"$attempt" "$list_pid" "${survivors:-<none at the final probe>}" "$ROOT_PACKAGE_LIST_STOP_GRACE" >&2
					fi
					return 1
				fi
				# The group has no live member, so the leader is a zombie or
				# already reaped and this reap cannot block.
				wait "$list_pid" 2>/dev/null || :
				if [ "$attempt" -ge "$ROOT_PACKAGE_LIST_ATTEMPTS" ]; then
					package_list_timeout_diagnostic "$package_list_stderr" "$attempt" "$module"
					return 1
				fi
				# Written once, fully formed, so the copy in the module log
				# and the copy the report replays are the same string to grep
				# for.
				printf 'run-module-tests.sh: %s: go list ./... attempt %s of %s timed out after %ss; retrying.\n' \
					"$module" "$attempt" "$ROOT_PACKAGE_LIST_ATTEMPTS" "$ROOT_PACKAGE_LIST_TIMEOUT" \
					| tee -a "$(package_list_retry_path "$module")" >&2
				sleep 1
				attempt=$((attempt + 1))
				continue 2
			fi
			sleep 0.1
		done
		# The status has to be read inside the else branch: after the `if`
		# compound closes, $? is the `if`'s own status, which is 0 when an
		# else-less condition fails.
		if wait "$list_pid"; then
			if ! mv "$attempt_list" "$package_list"; then
				printf 'run-module-tests.sh: could not promote %s to %s\n' "$attempt_list" "$package_list" >&2
				return 1
			fi
			return 0
		else
			list_status=$?
			# Only the timeout is retried. A `go list` that exits non-zero has
			# decided something about the package list itself — an unparseable
			# source, a missing module — and repeating it just repeats the
			# answer.
			cat "$package_list_stderr" >&2
			return "$list_status"
		fi
	done
}

run_module() {
	local m="$1" extra="$2" test_flags
	test_flags="$(module_test_flags "$m")"
	# Word-split flags and extra intentionally so callers can pass multiple flags.
	# shellcheck disable=SC2086
	if [ "$m" = "." ]; then
		local -a packages=()
		local pkg package_list
		package_list="$(package_list_path "$m")"
		run_bounded_package_list "$m" "$package_list" || return $?
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
		local subpkgs=()
		local pkg subpkg_list
		# Through the same bound as the root module's: this `go list` reads the
		# same GOCACHE/GOMODCACHE, so a stalled volume would hang it exactly as
		# it hangs root discovery — and it runs after the root bound has already
		# been reported, where an unbounded hang is the one thing that bound
		# cannot help with.
		subpkg_list="$(package_list_path "$m")"
		run_bounded_package_list "$m" "$subpkg_list" || return 1
		while IFS= read -r pkg; do
			[ "$pkg" = "primeradiant.com/evener/agent" ] || subpkgs+=("$pkg")
		done <"$subpkg_list"
		if [ "${#subpkgs[@]}" -gt 0 ]; then
			/usr/bin/time -p go test $test_flags $extra -run "$GATE_TEST_RUN" -skip "$fuzz_test_skip" "${subpkgs[@]}" || shardStatus=$?
		fi
		return "$shardStatus"
	fi
	/usr/bin/time -p go test $test_flags $extra -run "$GATE_TEST_RUN" -skip "$fuzz_test_skip" ./...
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
	local m log extra tmp
	for m in "$@"; do
		log="$(logpath "$m")"
		extra="$(module_extra "$m")"
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

for m in $WAVE1 $WAVE2; do
	retry_log="$(package_list_retry_path "$m")"
	[ -s "$retry_log" ] || continue
	while IFS= read -r retry_line; do
		printf '%s\n' "$retry_line" >&2
	done <"$retry_log"
done

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
