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
# Every module's packages are enumerated with a bounded `go list ./...` and the
# result handed to `go test`, so discovery happens in one place for every module
# scheduled and never inside `go test ./...`, which has no bound of its own. The
# bound is EVENER_PACKAGE_LIST_TIMEOUT seconds per attempt (default 60) over
# EVENER_PACKAGE_LIST_ATTEMPTS attempts (default 3), a timed-out attempt being
# the only one retried. Raise
# the per-attempt budget on a host slower than that; the failure diagnostic
# names both knobs. A timed-out attempt is stopped by process group, SIGTERM
# then SIGKILL, and reaped before the next one starts; an attempt that will not
# stop fails the run, naming its surviving pids and waiting on none of them,
# instead of being retried. Each attempt writes its own package list and only a
# completed one is used.
#
# Each of those attempts is exec'd through perl so it lands in its own process
# group and can be stopped as one, so perl has to be on PATH for any run that
# schedules a module at all. It is on macOS and on the CI image; setsid(1), the
# usual tool for this, is not on macOS.
#
# Output: one PASS/FAIL line per module (with wall time) as each finishes; a
# failing module's full output is printed at the end. Exits non-zero on any
# failure.
set -uo pipefail

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# The diagnostics are printed from inside a module's directory, and the runner
# only works from the repository root (MODULES names directories relative to
# it), so every command they suggest has to say where to run it from.
repo_root="$(CDPATH='' cd -- "$script_dir/../.." && pwd)"
. "$script_dir/../lib/private-go-home.sh"
. "$script_dir/../lib/scratch-lib.sh"
. "$script_dir/../lib/process-group-lib.sh"

# The load-aware budgets below degrade to their historical fixed values when
# the helper is unreadable or answers with nothing. An unguarded source would
# abort this script outright, and an unguarded call would leave a budget empty,
# which the -p guards read as "pass no flag" and widen to go's GOMAXPROCS.
have_load_aware=0
load_aware_helper="$script_dir/../lib/load-aware-workers.sh"
if [ -r "$load_aware_helper" ]; then
	. "$load_aware_helper"
	have_load_aware=1
fi

# gate_budget CAP DEFAULT — the load-aware worker count for CAP, or DEFAULT
# when the helper is absent or its answer is not a positive integer.
gate_budget() {
	_gb_cap=$1
	_gb_default=$2
	_gb_value=
	if [ "$have_load_aware" -eq 1 ]; then
		_gb_value="$(load_aware_workers "$_gb_cap" 2>/dev/null)" || _gb_value=
	fi
	case "$_gb_value" in
	''|*[!0-9]*) _gb_value=$_gb_default ;;
	esac
	printf '%s' "$_gb_value"
}

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
ROOT_P=${ROOT_P-$(gate_budget 6 6)}
AGENT_PARALLEL=${AGENT_PARALLEL-$(gate_budget 6 6)}
AGENT_P=${AGENT_P-$(gate_budget 4 4)}
# The agent-shards runner does the agent module's real work and reads its own
# parallelism from the environment; AGENT_PARALLEL never reaches it. Without
# these the dominant agent workload stayed at a fixed width under load. The
# caps are the runner's own defaults, and a set value still wins.
export AGENT_SHARD_PARALLEL=${AGENT_SHARD_PARALLEL-$(gate_budget 3 3)}
export AGENT_SHARD_SURVEY_PARALLEL=${AGENT_SHARD_SURVEY_PARALLEL-$(gate_budget 6 6)}
# Modules with no explicit -p are deliberately left alone. Go's default -p is
# GOMAXPROCS, which is cgroup-quota aware; an explicit -p derived from the
# host's online CPUs would oversubscribe a CPU-limited container and override a
# user-lowered GOMAXPROCS. The three budgets above only ever tighten values
# this script already passed.

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
# Both must be positive integers, and the cost has two cases rather than one
# ceiling. An attempt that times out and whose group then dies cleanly costs
# TIMEOUT plus the second of backoff before the next one, so exhausting the
# attempts at the defaults below is 3 x 60s + 2 x 1s = 182s. A group that will
# not die costs up to two stop graces on top (PACKAGE_LIST_STOP_GRACE for
# SIGTERM, then the same for SIGKILL) and ends the run on that attempt instead
# of retrying: 60s + 10s = 70s if it happens on the first attempt, 192s if it
# happens on the last. Nothing reaches the sum of both, because the two cases
# are alternatives.
# The knobs were EVENER_ROOT_PACKAGE_LIST_* until the agent module's enumeration
# started going through the same bound. A run that still sets the old spelling
# means to change the budget and would instead get the default, silently — and
# the place that spelling is most likely to survive is a CI job or a script
# someone copied from a diagnostic. Say so and stop; there is deliberately no
# alias, so there is exactly one name for each of these.
for stale_knob in EVENER_ROOT_PACKAGE_LIST_TIMEOUT EVENER_ROOT_PACKAGE_LIST_ATTEMPTS; do
	if [ -n "${!stale_knob:-}" ]; then
		printf 'run-module-tests.sh: %s is no longer read; the knobs are EVENER_PACKAGE_LIST_TIMEOUT and EVENER_PACKAGE_LIST_ATTEMPTS, because the bound is not the root module'"'"'s alone any more.\n' \
			"$stale_knob" >&2
		exit 2
	fi
done
# Read in base ten and kept that way. Every use below is arithmetic — the
# deadline comparison, the doubled budget in the diagnostic, the tick counts —
# and bash reads a leading zero as octal, so `08` is a shell error at the point
# of use rather than eight seconds. Normalising here means the rest of the
# script never sees the spelling the caller typed.
PACKAGE_LIST_TIMEOUT=${EVENER_PACKAGE_LIST_TIMEOUT:-60}
if [[ ! "$PACKAGE_LIST_TIMEOUT" =~ ^[0-9]+$ ]] || [ "$((10#$PACKAGE_LIST_TIMEOUT))" -lt 1 ]; then
	printf 'run-module-tests.sh: EVENER_PACKAGE_LIST_TIMEOUT must be a positive integer in seconds (got %q)\n' "$PACKAGE_LIST_TIMEOUT" >&2
	exit 2
fi
PACKAGE_LIST_TIMEOUT=$((10#$PACKAGE_LIST_TIMEOUT))
PACKAGE_LIST_ATTEMPTS=${EVENER_PACKAGE_LIST_ATTEMPTS:-3}
if [[ ! "$PACKAGE_LIST_ATTEMPTS" =~ ^[0-9]+$ ]] || [ "$((10#$PACKAGE_LIST_ATTEMPTS))" -lt 1 ]; then
	printf 'run-module-tests.sh: EVENER_PACKAGE_LIST_ATTEMPTS must be a positive integer (got %q)\n' "$PACKAGE_LIST_ATTEMPTS" >&2
	exit 2
fi
PACKAGE_LIST_ATTEMPTS=$((10#$PACKAGE_LIST_ATTEMPTS))
# Seconds to wait for a stopped attempt's process group to empty after each of
# SIGTERM and SIGKILL. Only a member that ignores or cannot take the signal
# reaches the end of either wait, so the ordinary stop costs milliseconds. Not
# an environment knob: nothing a caller does should be able to shorten the
# window that proves the attempt is gone.
PACKAGE_LIST_STOP_GRACE=5

# The gate's test-selection surface lives in one shared file so the coverage
# ratchet can measure exactly what this gate proves; see gate-surface-lib.sh.
. "$(dirname "${BASH_SOURCE[0]}")/../lib/gate-surface-lib.sh"
fuzz_test_skip="$GATE_FUZZ_TEST_SKIP"

root_skip="$fuzz_test_skip"

flags="$*"
# Refused rather than forwarded: -C changes directory before the command runs,
# and every module here is enumerated and tested from its own directory.
for flag in $flags; do
	case "$flag" in
	-C | -C=*)
		printf 'run-module-tests.sh: -C is not supported here. Each module is enumerated and tested from its own directory, and a -C would move both commands somewhere this runner does not expect. Run the gate from the repository root instead.\n' >&2
		exit 2
		;;
	esac
done
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
# The runner's own process group, read once. An attempt that has recorded itself
# but not yet split reports this group rather than one of its own, and telling
# those two apart is what says whether the attempt may be signalled by pid.
runner_pgid="$(ps -o pgid= -p $$ 2>/dev/null | tr -d '[:space:]')"
# Set by stop_package_list_attempt: why it could not show an attempt stopped.
package_list_stop_reason=""
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
	stop_recorded_package_list_groups
}

# stop_recorded_package_list_groups — stop any package-list attempt that was
# still running when the runner was told to die.
#
# The walk above cannot reach one. An attempt runs in a process group of its
# own, so a signal sent to the runner's group never reaches it; and when that
# signal kills the wave subshell holding it, the attempt is reparented to init
# and stops being a descendant of anything in active_pids. Measured before this
# existed: after `kill -TERM -- -<runner pgid>` the runner exited and the
# stalled `go list` was left running with ppid 1, holding the GOCACHE and
# GOMODCACHE locks that every later run on the host needs.
stop_recorded_package_list_groups() {
	local pgid_file recorded members stop_status marker ownership
	[ -n "$logdir" ] || return 0
	# A record is dropped only once it has been acted on: a file removed before
	# the probe takes the only name anyone had for a survivor with it, and a
	# probe that cannot run is exactly when that name is worth keeping — the
	# retained logs then say which group was left holding Go's cache locks.
	for pgid_file in "$logdir"/*.pgid; do
		[ -e "$pgid_file" ] || continue
		recorded="$(cat "$pgid_file" 2>/dev/null)"
		if [ -z "$recorded" ]; then
			# An attempt caught mid-spawn: the file is created before the fork
			# and filled in by the child before it splits, so waiting for the pid
			# is the only way to reach what that child is about to become.
			# Nothing else can name it — it has no group of its own yet, and a
			# descendant walk that missed the fork will not find it either. The
			# wait is the stop's own grace, for the same reason: it is how long
			# this script is willing to spend proving an attempt is not running.
			if ! recorded="$(pgroup_record_value "$pgid_file" "$PACKAGE_LIST_STOP_GRACE")"; then
				# Still nothing, and the record cannot be dropped on the strength
				# of a signal that may never have been sent: this cleanup also
				# runs from the plain EXIT trap of a run that merely failed,
				# where nothing was signalled to anything. A child that was only
				# slow to record itself would be forgotten here and then split
				# off and run `go list` untracked. Nothing here can name it —
				# that is what the empty record means — so what is left is to
				# keep the record and say so, in the logs a failed run retains.
				printf 'run-module-tests.sh: the attempt recorded at %s never named its process group, so it cannot be shown to have stopped. Its record is kept.\n' \
					"$pgid_file" >&2
				continue
			fi
		fi
		if [ "${recorded#survivor:}" != "$recorded" ]; then
			# Already taken SIGTERM and SIGKILL with a full grace each, and seen
			# alive after both. Repeating that here would cost two more graces to
			# learn what is already known, so the record is left standing where
			# whoever runs next on this host can read it.
			printf 'run-module-tests.sh: process group %s was still alive after SIGTERM and SIGKILL; its record is kept at %s.\n' \
				"${recorded#survivor:}" "$pgid_file" >&2
			continue
		fi
		marker="${recorded##*:}"
		recorded="${recorded%:*}"
		if [ "${recorded#pid:}" != "$recorded" ]; then
			# A pid, not a group: the attempt had not split when it wrote this,
			# so it is in the runner's own group and nothing here may signal that
			# group. The deadline's own decision covers exactly this — pid stop,
			# then the group it may have formed since — so it is asked to make it.
			#
			# Here the marker does answer, and it is the one place it can: a pid
			# names one process, and one process either carries the marker or is
			# somebody the kernel has since given the number to. Unknown is not a
			# licence to signal, so it keeps the record and says so.
			ownership=0
			pid_owned_by "${recorded#pid:}" "$marker" || ownership=$?
			if [ "$ownership" -eq 1 ]; then
				rm -f "$pgid_file"
				continue
			fi
			if [ "$ownership" -ne 0 ]; then
				printf 'run-module-tests.sh: the attempt recorded at %s could not be shown to have stopped: the process listing that says whether pid %s is still this attempt would not run. Its record is kept.\n' \
					"$pgid_file" "${recorded#pid:}" >&2
				continue
			fi
			stop_status=0
			stop_package_list_attempt "${recorded#pid:}" || stop_status=$?
			if [ "$stop_status" -eq 0 ] || [ "$stop_status" -eq 3 ]; then
				rm -f "$pgid_file"
			else
				printf 'run-module-tests.sh: the attempt recorded at %s could not be shown to have stopped: %s Its record is kept.\n' \
					"$pgid_file" "$package_list_stop_reason" >&2
			fi
			continue
		fi
		recorded="${recorded#pgid:}"
		# Survivors are asked about first, and they decide. The marker cannot
		# speak for a whole group: `go list` spawns compilers and a child outlives
		# its parent, so a group whose marked process has exited still holds
		# children with the cache locks — and asking the marker first read that as
		# somebody else's group and dropped the record without stopping them.
		# Anything alive under this number gets stopped; the number is refused
		# outright by the library if it is 0, this runner's own group, or its
		# session, which is what keeps "stop whatever is there" safe.
		# What is recorded is a group, so ask about the group rather than
		# about its leader alone: `go list` can exit with a child of the
		# attempt still running in it, and a leader-only check would leave
		# that child writing its package list and holding Go's cache locks.
		# A probe that cannot run knows nothing about the group, and blind is
		# the one state in which -PID could name a stranger, so it is left.
		if ! members="$(pgroup_survivors "$recorded")"; then
			# Nothing can be seen, and saying nothing is how a `go list` is left
			# holding the cache locks: the deadline gives a blind probe a blind
			# signal, and this is the same probe failing at the last moment anyone
			# is looking. The record stays, because blind is exactly when the only
			# name for the group is worth keeping.
			escalate_blind "$recorded" "$PACKAGE_LIST_STOP_GRACE"
			printf 'run-module-tests.sh: process group %s could not be shown to have stopped: the process listing that answers whether it is empty would not run. It has been signalled blind; its record is kept at %s.\n' \
				"$recorded" "$pgid_file" >&2
			continue
		fi
		if [ -z "$members" ]; then
			rm -f "$pgid_file"
			continue
		fi
		# Live members settle ownership on their own, and nothing the leader is
		# doing can overrule them. A process group exists for as long as it has a
		# member, and its number stays reserved for that whole time, so a group
		# numbered as this record with anything alive in it is this attempt's —
		# whether the leader is a zombie, already reaped, or a pid the kernel has
		# since handed to a process in some other group. Asking the leader instead
		# threw the record away on that last reading and left `go list`'s children
		# writing a package list and holding Go's cache locks with no name left for
		# them.
		stop_status=0
		stop_pgroup "$recorded" "$PACKAGE_LIST_STOP_GRACE" || stop_status=$?
		if [ "$stop_status" -eq 0 ]; then
			rm -f "$pgid_file"
			continue
		fi
		# The stop could not show the group empty, so the record stays for the
		# same reason a failed probe keeps it: it is the only name for whatever is
		# still holding Go's cache locks, and this is the last moment anyone is
		# looking. Said out loud too, because the retained logs are where the next
		# person on this host starts.
		printf 'run-module-tests.sh: process group %s could not be shown to have stopped; it still holds %s. Its record is kept at %s.\n' \
			"$recorded" "$(pgroup_survivor_report "$recorded")" "$pgid_file" >&2
	done
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
# Where an attempt records itself, and what became of it: created empty by the
# parent before the fork, written by the child as `pid:N` before it splits and
# rewritten as `pgid:N`
# once it has, and removed when the attempt is reaped. The two spellings are not
# decoration. The number is the same either way, but a pid in the runner's own
# group and a group of the attempt's own are opposite things to a cleanup: the
# first must be signalled by pid, and reading it as a group finds no members and
# throws the record away while the attempt is still about to run. An empty one
# means an attempt is spawning, and `survivor:N` one that took SIGTERM and
# SIGKILL and was still there afterwards — a record kept deliberately, for the
# next person on the host, and not signalled again.
# That group is the one thing the runner's signal cleanup cannot otherwise
# reach: the attempt is deliberately in a group of its own, so a signal aimed at
# the runner's group never touches it, and once the wave subshell holding it
# dies the attempt is reparented to init and stops being anyone's descendant.
package_list_pgid_path() { printf '%s.pgid' "$(package_list_path "$1")"; }

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
	if [ "$attempts_made" -ge "$PACKAGE_LIST_ATTEMPTS" ]; then
		printf 'run-module-tests.sh: go list ./... timed out after %ss on each of %s attempts.\n' \
			"$PACKAGE_LIST_TIMEOUT" "$PACKAGE_LIST_ATTEMPTS" >&2
	else
		printf 'run-module-tests.sh: go list ./... timed out after %ss on attempt %s of %s.\n' \
			"$PACKAGE_LIST_TIMEOUT" "$attempts_made" "$PACKAGE_LIST_ATTEMPTS" >&2
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
	printf '  cd %q && GOCACHE=%q GOMODCACHE=%q go clean -cache -modcache && GOCACHE=%q GOMODCACHE=%q scripts/gate/run-module-tests.sh -short -count=1\n' \
		"$repo_root" "$gocache" "$gomodcache" "$gocache" "$gomodcache" >&2
	printf 'run-module-tests.sh: or give a slow host more room per attempt:\n' >&2
	printf '  cd %q && EVENER_PACKAGE_LIST_TIMEOUT=%s scripts/gate/run-module-tests.sh -short -count=1\n' \
		"$repo_root" "$((PACKAGE_LIST_TIMEOUT * 2))" >&2
}

# stop_package_list_attempt PID — stop a timed-out attempt, whatever state its
# process group is in, and say what happened. PID is the pid the runner spawned,
# which is also the attempt's group number once it has split into one.
#
# Every case the deadline has to tell apart lives here: an attempt in a group of
# its own, one that has recorded itself and not split yet and so is still in the
# runner's group, one whose group cannot be read at all, and the group an attempt
# forms in the instant between the read and the signal. Keeping them together is
# the point. Spread across the caller they drifted: two copies of the same
# pre-stop probe, one of which quietly stopped escalating.
#
# Exit status:
#   0  it was running and is now confirmed gone
#   1  it would not stop; the reason names what survived, which is everything a
#      kept record would have carried
#   2  it cannot be shown to have stopped, so the record has to be kept
#   3  nothing of it was running: it had already finished on its own
# For 1 and 2 the reason is left in package_list_stop_reason, a sentence the
# caller frames with the attempt it is reporting.
stop_package_list_attempt() {
	local pid="$1" pgid live listing_failed status=0
	package_list_stop_reason=""
	listing_failed="$(printf 'the process listing that answers "is process group %s empty" would not run.' "$pid")"
	# Read the group only for a process just confirmed alive: the child sets its
	# own group after the fork, so a read taken at spawn time races it and would
	# report the runner's group — the one group nothing here may ever signal.
	pgid="$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d '[:space:]')"
	# An unreadable group is ambiguous, and the ambiguity is a race: the attempt
	# can exit and be reaped between the caller's liveness check and this read,
	# and then `ps` reports nothing for a process that finished rather than one
	# that cannot be stopped. Ask the kernel again before deciding.
	if [ -z "$pgid" ] && ! kill -0 "$pid" 2>/dev/null; then
		return 3
	fi
	if [ "$pgid" = "$pid" ]; then
		# Ask who is actually running before stopping anything. `kill -0`
		# answers for a zombie too, so an attempt that has already exited would
		# otherwise be "stopped", reaped, and its own status read as the stop's
		# — a verdict about the package list retried to the end of the budget
		# and then reported as a timeout.
		if ! live="$(pgroup_survivors "$pid")"; then
			escalate_blind "$pid" "$PACKAGE_LIST_STOP_GRACE"
			package_list_stop_reason="$listing_failed"
			return 2
		fi
		if [ -z "$live" ]; then
			return 3
		fi
		stop_pgroup "$pid" "$PACKAGE_LIST_STOP_GRACE" || status=$?
	elif [ -n "$runner_pgid" ] && [ "$pgid" = "$runner_pgid" ]; then
		# Recorded itself, not split yet: still in the runner's own group, so
		# the pid is the only aim there is. It has to be taken now — left alone
		# it splits off a moment later and runs `go list` with nobody tracking
		# it, because the cleanup's group probe finds that group empty while the
		# pid is still the runner's. It has spawned nothing yet either, since it
		# becomes `go` only at the exec after the split.
		stop_pid "$pid" "$PACKAGE_LIST_STOP_GRACE" || status=$?
		if [ "$status" -eq 2 ]; then
			escalate_blind "$pid" "$PACKAGE_LIST_STOP_GRACE"
			package_list_stop_reason="$listing_failed"
			return 2
		fi
		if [ "$status" -ne 0 ]; then
			package_list_stop_reason="$(printf 'pid %s had not split into a group of its own and did not go after SIGTERM and SIGKILL with %ss of grace each.' \
				"$pid" "$PACKAGE_LIST_STOP_GRACE")"
			return 1
		fi
		# The group read above is a snapshot and the attempt splits off the
		# instant after it, in which case the signal just sent took only the
		# leader and left `go list`'s children in a group of their own. The pid
		# is that group's number too, so ask whether one formed.
		if ! live="$(pgroup_survivors "$pid")"; then
			escalate_blind "$pid" "$PACKAGE_LIST_STOP_GRACE"
			package_list_stop_reason="$listing_failed"
			return 2
		fi
		if [ -n "$live" ]; then
			stop_pgroup "$pid" "$PACKAGE_LIST_STOP_GRACE" || status=$?
		fi
	else
		# No group here this script may aim at, and every fallback is worse than
		# saying so: a descendant walk reads the same listing that just failed,
		# and killing the leader alone leaves whatever `go` spawned holding the
		# build and module cache locks.
		package_list_stop_reason="$(printf 'its process group reads as %s, which is neither its own pid %s nor this runner.' \
			"${pgid:-<unreadable>}" "$pid")"
		return 2
	fi
	case "$status" in
	0) return 0 ;;
	2)
		# The stop escalated inside itself before answering 2; nothing more to
		# send, and its own SIGKILL has already gone to the group.
		package_list_stop_reason="$listing_failed"
		return 2
		;;
	esac
	package_list_stop_reason="$(printf 'process group %s still holds %s after SIGTERM and SIGKILL with %ss of grace each.' \
		"$pid" "$(pgroup_survivor_report "$pid")" "$PACKAGE_LIST_STOP_GRACE")"
	return 1
}

# package_list_build_flags FLAG... — the subset of a module's flags that decides
# which files build, and therefore which packages exist.
#
# The enumeration and the `go test` that consumes it have to agree about that or
# the gate silently tests less than it reports: `-tags integration` selects files
# `go list ./...` without it never sees, so those packages would be missing from
# the list handed to a `go test` that does build them. The split is the rule —
# build flags go to both commands, test-only flags (-run, -skip, -count,
# -timeout, -short, -parallel, -p, -coverprofile) belong to `go test` alone and
# `go list` rejects several of them.
#
# `-C` is not on the list and is refused at startup instead: it changes directory
# before the command runs, and this runner has already changed into the module's
# own directory to enumerate and test it, so honouring a caller's `-C` would move
# both commands somewhere the rest of the script does not expect. Refusing says
# that; forwarding it would not. Today's callers pass only -short, -count,
# -race, -p and -parallel, so nothing here is forwarded in practice; the rule is
# what keeps the two commands agreeing when that changes.
package_list_build_flags() {
	local flag out="" expect_value=0
	for flag in "$@"; do
		if [ "$expect_value" -eq 1 ]; then
			out="$out $flag"
			expect_value=0
			continue
		fi
		case "$flag" in
		-tags | -mod | -modfile | -overlay | -pgo | -workfile)
			out="$out $flag"
			expect_value=1
			;;
		-tags=* | -mod=* | -modfile=* | -overlay=* | -pgo=* | -workfile=* | -trimpath)
			out="$out $flag"
			;;
		esac
	done
	printf '%s' "${out# }"
}

# run_bounded_package_list MODULE OUTPUT — enumerate MODULE's packages into
# OUTPUT under the bound. MODULE is the runner's name for the module (".", or a
# directory) and is used for the retry file and the diagnostic; the enumeration
# itself is `go list ./...` in the current directory, which the caller has
# already changed to that module.
run_bounded_package_list() {
	local module="$1" package_list="$2" package_list_stderr attempt attempt_list build_flags
	local list_pid started_at list_status stop_status
	# Every attempt below is exec'd through perl so it lands in its own process
	# group. Named here rather than discovered at the spawn, where it would fail
	# as an exec error buried in a package-list log that says nothing about what
	# is missing — and asked here rather than at startup, because only the
	# modules that enumerate under the bound need perl at all and a run
	# scheduling none of them must not be refused for its absence.
	if ! command -v perl >/dev/null 2>&1; then
		printf 'run-module-tests.sh: perl is not on PATH. Each bounded go list attempt runs through perl setpgrp(0, 0) so the attempt can be stopped as a process group; setsid(1) would serve as well but is not on macOS. Install perl, or run a module directly with go test.\n' >&2
		return 2
	fi
	package_list_stderr="${package_list}.stderr"
	# The module's own flags, as `go test` will be given them, filtered down to
	# what changes which packages exist. Derived here rather than at the three
	# call sites, so there is one answer per module and no copy to drift.
	# shellcheck disable=SC2046
	build_flags="$(package_list_build_flags $(module_test_flags "$module") $(module_extra "$module"))"
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
		printf '=== go list ./... attempt %s of %s ===\n' "$attempt" "$PACKAGE_LIST_ATTEMPTS" >>"$package_list_stderr"
		# The attempt is spawned into its own process group so that
		# stop_pgroup can stop it as one. perl's setpgrp(0, 0)
		# does that inside the child, between fork and exec, where this shell
		# cannot see it. `set -m` would also have made the job its own group, but
		# only by turning job control on for the whole script: run_wave backgrounds
		# every module and stream with `( ... ) &` and records the pids in
		# active_pids, and stop_children and cleanup signal and wait on exactly
		# those — all of it shaped by whether monitor mode is on. Nothing here is
		# worth making the rest of the script run under different job semantics.
		# The attempt records its own group, and does it before the group exists:
		# the number is the child's pid either way, and the two orderings differ in
		# what a signal arriving mid-spawn leaves behind. Recording first, a signal
		# before setpgrp still reaches the child through the runner's own group,
		# and after setpgrp the record is already there for the cleanup to stop —
		# no instant exists in which a split-off group is unrecorded. The record is
		# renamed into place so a reader sees the whole pid or no file at all; a
		# marker that cannot be written fails the attempt in its stderr log rather
		# than leaving an untracked group running.
		# The record exists before the attempt does. A cleanup that walks
		# descendants can miss a child forked a moment ago, kill the wave subshell
		# holding it, and find nothing to stop — the child then fills in its pid,
		# splits into its own group and runs on. So the file is created here,
		# before the fork, and an empty one means "an attempt is spawning": the
		# cleanup waits for the pid rather than deciding there is nothing to stop.
		if ! : >"$(package_list_pgid_path "$module")"; then
			printf 'run-module-tests.sh: could not create the process-group record %s for attempt %s; not spawning a package list that nothing could stop.\n' \
				"$(package_list_pgid_path "$module")" "$attempt" >&2
			return 1
		fi
		# Word-split deliberately, as everywhere else the flags are passed on.
		# shellcheck disable=SC2086
		perl -e "$PGROUP_SPAWN_PERL" \
			-- "$(package_list_pgid_path "$module")" go \
			go list $build_flags ./... >"$attempt_list" 2>>"$package_list_stderr" &
		list_pid="$!"
		# Said from this side too, so no instant passes with an attempt running
		# and a record that names nothing.
		pgroup_record_spawned "$(package_list_pgid_path "$module")" "$list_pid" go
		started_at=$SECONDS
		while kill -0 "$list_pid" 2>/dev/null; do
			if [ $((SECONDS - started_at)) -ge "$PACKAGE_LIST_TIMEOUT" ]; then
				if ! kill -0 "$list_pid" 2>/dev/null; then
					# It finished inside the last poll interval. Nothing to
					# stop; take the completion path below.
					break
				fi
				stop_status=0
				stop_package_list_attempt "$list_pid" || stop_status=$?
				if [ "$stop_status" -eq 3 ]; then
					# It finished on its own. The completion path below reports
					# whatever it decided about the package list.
					break
				fi
				if [ "$stop_status" -ne 0 ]; then
					# Deliberately no wait: SIGKILL does not land on a process in
					# uninterruptible sleep, which is exactly the stalled-volume case
					# this bound exists for, and waiting on it would replace the bound
					# with an indefinite hang. Name what is known instead and fail.
					# One rule for both answers: what has not been shown to have
					# stopped keeps its record, and the line below says where that
					# record is. A group seen alive after SIGKILL is the case where
					# the name matters most — it is the only thing that tells whoever
					# runs next on this host what is holding the cache locks.
					package_list_timeout_diagnostic "$package_list_stderr" "$attempt" "$module"
					if [ "$stop_status" -eq 2 ]; then
						printf 'run-module-tests.sh: attempt %s cannot be shown to have stopped: %s Not retrying, because a retry that cannot see the previous attempt would race it. Its record is kept at %s.\n' \
							"$attempt" "$package_list_stop_reason" "$(package_list_pgid_path "$module")" >&2
					else
						# Marked as what it is, so the EXIT cleanup keeps the record
						# without spending two more graces re-signalling a group this
						# attempt has already taken SIGTERM and SIGKILL to.
						printf 'survivor:%s' "$list_pid" >"$(package_list_pgid_path "$module")"
						printf 'run-module-tests.sh: attempt %s would not stop: %s Not retrying, and not waiting on it. Its record is kept at %s.\n' \
							"$attempt" "$package_list_stop_reason" "$(package_list_pgid_path "$module")" >&2
					fi
					return 1
				fi
				# The group has no live member, so the leader is a zombie or
				# already reaped and this reap cannot block. Its status is the
				# one thing that says whether this was a timeout at all: an
				# attempt that finished between the deadline and the stop
				# completed, and discarding a package list it successfully
				# produced would turn a slow-but-working host into a failure.
				# bash keeps a reaped job's status, so this answers even when
				# the race above already removed the process.
				rm -f "$(package_list_pgid_path "$module")"
				if wait "$list_pid"; then
					if ! mv "$attempt_list" "$package_list"; then
						printf 'run-module-tests.sh: could not promote %s to %s\n' "$attempt_list" "$package_list" >&2
						return 1
					fi
					return 0
				else
					# Read inside the else branch: once the `if` compound
					# closes, $? is the compound's own status, not the reap's.
					list_status=$?
				fi
				# Only an attempt this script killed is a timeout. The probe above
				# is what establishes that: reaching here means members of the group
				# were running and the stop then signalled them. What is left for
				# the status to catch is the sliver between the two — an attempt that
				# exited in it reaches the reap with a status of its own, a verdict
				# about the package list that retrying repeats and a timeout
				# diagnostic buries. The stop sends SIGTERM and then SIGKILL, and
				# bash reports a signalled child as 128 plus the signal number, so
				# those two numbers are what a stopped attempt looks like and
				# anything else is the attempt speaking for itself.
				case "$list_status" in
					143 | 137) ;;
					*)
						cat "$package_list_stderr" >&2
						return "$list_status"
						;;
				esac
				if [ "$attempt" -ge "$PACKAGE_LIST_ATTEMPTS" ]; then
					package_list_timeout_diagnostic "$package_list_stderr" "$attempt" "$module"
					return 1
				fi
				# Written once, fully formed, so the copy in the module log
				# and the copy the report replays are the same string to grep
				# for.
				printf 'run-module-tests.sh: %s: go list ./... attempt %s of %s timed out after %ss; retrying.\n' \
					"$module" "$attempt" "$PACKAGE_LIST_ATTEMPTS" "$PACKAGE_LIST_TIMEOUT" \
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
		rm -f "$(package_list_pgid_path "$module")"
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

# require_packages MODULE COUNT — refuse to run a module whose enumeration left
# nothing to test.
#
# Nothing to test is not a pass: an empty list reaches `go test` as no package
# arguments at all, which tests the current directory or nothing and reports
# success either way. Every branch below filters its list differently — the root
# module drops the fuzz-coverage commands, the sharded agent path drops its own
# top-level package — so the check belongs after each filter, and the sentence
# it prints belongs in one place.
require_packages() {
	[ "$2" -gt 0 ] && return 0
	printf 'run-module-tests.sh: %s: go list ./... returned no test packages\n' "$1" >&2
	return 1
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
		require_packages "$m" "${#packages[@]}" || return 1
		# ROOT_FULL removes short mode through module_test_flags while retaining
		# the regular Test/Example name filter. Fuzz-owned targets and sanity
		# functions stay under the explicit make fuzz gate.
		/usr/bin/time -p go test $test_flags $extra -run "$GATE_TEST_RUN" -skip "$root_skip" "${packages[@]}"
		return
	fi
	if [ "$m" = "agent" ]; then
		# Both agent modes come through here, because both need the bounded
		# enumeration; only the sharded one splits the top-level package.
		#
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
		local subpkgs=()
		local pkg subpkg_list
		# The bounded enumeration runs before the shards, not after, and the
		# order is the bound. What this module has to survive is a stalled cache
		# volume, where every go invocation blocks, so whichever go invocation
		# runs first decides whether the module fails inside the bound or hangs
		# outside it. `go run ./cmd/evener-dev/bin` reads the same GOCACHE and
		# GOMODCACHE and has no bound of its own, and it cannot borrow this one:
		# it runs the agent test suite rather than discovering packages, so a
		# discovery-sized budget would turn a slow test run into a failure — a
		# new flake in place of the one this bound removes. Ordering costs
		# nothing, because the two are independent and the list is not read
		# until the shards have finished.
		subpkg_list="$(package_list_path "$m")"
		# The status is the enumeration's own, as on every other module: a `go list`
	# that failed on the package list says so with its exit code, and flattening
	# it to 1 threw that away here alone.
	run_bounded_package_list "$m" "$subpkg_list" || return $?
		if [ "$AGENT_SHARDS" -eq 0 ]; then
			# The unsharded mode, which is what make test-race uses. It used to
			# fall through to `go test ./...`, whose own package discovery reads
			# the same GOCACHE and GOMODCACHE with no bound at all — so the race
			# gate could hang on exactly the stall the bound above exists for.
			# Handing it the enumerated list makes the bounded walk the only one.
			local racepkgs=()
			while IFS= read -r pkg; do
				racepkgs+=("$pkg")
			done <"$subpkg_list"
			require_packages "$m" "${#racepkgs[@]}" || return 1
			/usr/bin/time -p go test $test_flags $extra -run "$GATE_TEST_RUN" -skip "$fuzz_test_skip" "${racepkgs[@]}"
			return
		fi
		# `go run` collapses its child's exit code to 1 and reports the real
		# one as an "exit status N" line on stderr, so the runner's 129/130/143
		# signal exits survive in the binary but not through this call. Only
		# zero-vs-nonzero is read below, so nothing here depends on them.
		(cd .. && go run ./cmd/evener-dev/bin dev agent-shards $test_flags) || shardStatus=$?
		while IFS= read -r pkg; do
			[ "$pkg" = "primeradiant.com/evener/agent" ] || subpkgs+=("$pkg")
		done <"$subpkg_list"
		if [ "${#subpkgs[@]}" -gt 0 ]; then
			/usr/bin/time -p go test $test_flags $extra -run "$GATE_TEST_RUN" -skip "$fuzz_test_skip" "${subpkgs[@]}" || shardStatus=$?
		fi
		return "$shardStatus"
	fi
	# Every other module. Their `go test ./...` did its own package discovery,
	# which reads the same GOCACHE and GOMODCACHE the bound exists for and has no
	# bound of its own — so a stalled cache volume hung the gate here exactly as
	# it once did on the root module, just later in the run. The enumeration is a
	# module-neutral helper now, so there is one discovery path for every module
	# and no module outside it.
	local pkgs=() pkg module_list
	module_list="$(package_list_path "$m")"
	run_bounded_package_list "$m" "$module_list" || return $?
	while IFS= read -r pkg; do
		pkgs+=("$pkg")
	done <"$module_list"
	require_packages "$m" "${#pkgs[@]}" || return 1
	/usr/bin/time -p go test $test_flags $extra -run "$GATE_TEST_RUN" -skip "$fuzz_test_skip" "${pkgs[@]}"
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

# replay_package_list_retries MODULE — put any retry notices this module
# recorded in front of its own verdict. They are written inside the wave
# subshell, whose output goes to a module log that a green run deletes, so this
# is the only place they survive a passing run; printing them before the
# verdict is what keeps the report in the order the events happened, instead of
# announcing a retry after the PASS it explains.
replay_package_list_retries() {
	local retry_log retry_line
	retry_log="$(package_list_retry_path "$1")"
	[ -s "$retry_log" ] || return 0
	while IFS= read -r retry_line; do
		printf 'run-module-tests.sh: WARNING: %s\n' "${retry_line#run-module-tests.sh: }" >&2
	done <"$retry_log"
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
		replay_package_list_retries "$m"
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
