#!/usr/bin/env bash
# test-web-browser.sh — the real browser-only frontend guards. They stay out
# of test-web because jsdom cannot evaluate the CSS cascade or browser
# geometry. Every guard runs so one missing browser or failing case does not
# hide the remaining guards' verdicts; the exit status is the first nonzero
# one.
set -u

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$script_dir/../lib/scratch-lib.sh"
. "$script_dir/../lib/load-aware-workers.sh"

cd "$script_dir/../../cmd/evener-hub/frontend" || exit 1

dir=""
# Parallel arrays (bash 3.2 has no associative arrays): the guards in verdict
# order, and the pid each one runs as while it is live.
guards=(layoutguard overflowguard shellguard spawnguard transcriptscrollguard retirementguard skillguard)
guard_pids=()
status=0; complete=0
# A signal that lands while a guard is being started is held until its pid is
# recorded (see start_guard), so stop_guards can never miss a live guard.
defer_signals=0; pending_signal=0

# owned_guard_running PID — whether PID is still one of this shell's running
# jobs. Bash 3.2 (macOS) has no `wait -n`, so completion is found by asking
# the job table, the same ownership test test-web.sh uses.
owned_guard_running() {
	local candidate
	for candidate in $(jobs -pr); do
		[ "$candidate" = "$1" ] && return 0
	done
	return 1
}

# stop_guards TERMs every guard still running and waits for each, so an
# interruption waits for the cleanup each guard owns. A recorded pid whose job
# has already exited is not signalled: the OS may have given that number to an
# unrelated process since. The skill guard is waited for but never signalled:
# it is a go test whose driver, Chrome and helper daemons are cleaned up by
# the test binary's own t.Cleanup, which a TERM to go test would skip, leaving
# them running.
stop_guards() {
	local i pid
	for i in "${!guards[@]}"; do
		pid=${guard_pids[$i]-}
		[ -n "$pid" ] && owned_guard_running "$pid" || continue
		[ "${guards[$i]}" != skillguard ] || continue
		kill -TERM "$pid" 2>/dev/null || :
	done
	for pid in ${guard_pids[@]+"${guard_pids[@]}"}; do
		[ -z "$pid" ] || wait "$pid" 2>/dev/null || :
	done
	guard_pids=()
}

finish_browser() {
	finish_status=$?; stop_guards
	if [ "$complete" -eq 1 ] && [ "$status" -eq 0 ] && [ "$finish_status" -eq 0 ]; then
		scratch_rm || { finish_status=1; [ -z "$dir" ] || printf 'full logs: %s\n' "$dir" >&2; }
	else
		[ -z "$dir" ] || printf 'full logs: %s\n' "$dir" >&2
	fi
	trap - 0; exit "$finish_status"
}

# A second signal while the gate is already stopping exits at once, without
# waiting any further for the skill guard, whose go test can run for minutes.
# That is the operator insisting: the skill guard's test binary, its Chrome
# and its helper daemons may then outlive the gate (they run in their own
# process groups, so no signal here reaches all of them), and the scratch is
# kept and named for whatever they left.
stopping=0
interrupted_browser() {
	if [ "$defer_signals" -eq 1 ]; then pending_signal=$1; return; fi
	if [ "$stopping" -eq 1 ]; then
		[ -z "$dir" ] || printf 'full logs: %s\n' "$dir" >&2
		trap - 0; exit "$1"
	fi
	stopping=1
	stop_guards; exit "$1"
}

# The trap is armed before any scratch exists: a crash between mint and arming
# would leak the directory (the trap-before-mkdir ordering the audit enforces).
trap finish_browser EXIT
trap 'interrupted_browser 129' 1; trap 'interrupted_browser 130' 2; trap 'interrupted_browser 143' 15

scratch_dir dir evener-test-web-browser

# web-skillguard is the full-stack browser guard: cmd/evener-hub's
# TestSkillComposerBrowser (browserguard build tag) drives the PRODUCTION web
# app in real Chrome through a real hub (roster, past index, auth) against two
# real `evener serve` helper daemons, with only the external LLM provider
# scripted. Unlike the pure-frontend guards it needs the Go toolchain and the
# BUILT frontend (the hub serves the embedded dist), so a missing dist is built
# before any guard starts, not skipped.
repo_root="$(cd "$script_dir/../.." && pwd -P)"
# A failed build fails only the skill guard: the other guards serve the
# frontend through their own Vite and still run to their verdicts.
build_status=0
if [ ! -f dist/index.html ]; then
	printf 'building the production frontend for web-skillguard…\n'
	NODE_DISABLE_COMPILE_CACHE=1 npm run build >"$dir/skillguard-build.log" 2>&1 || build_status=$?
fi

# start_guard INDEX — start guards[INDEX] in the background, its output in
# $dir/GUARD.log, and record its pid at the same index. Each guard's Vite gets
# a dep cache of its own: two Vite processes optimizing into one cache race
# (issue #1586), and the guards run side by side.
start_guard() {
	local index=$1 guard=${guards[$1]} guard_dir="$dir/${guards[$1]}"
	mkdir -p "$guard_dir/home" "$guard_dir/tmp" "$guard_dir/xdg-config" "$guard_dir/xdg-cache" "$guard_dir/xdg-state" "$guard_dir/vite-cache" || exit 1
	export BROWSER_GUARD_VITE_CACHE_DIR="$guard_dir/vite-cache"
	defer_signals=1
	case "$guard" in
	retirementguard)
		# retirementguard's contract is `npm run retirementguard`: it invokes the
		# isolated Go fixture (TestRetirementBrowser), which starts the fixture Hub
		# and drives scripts/retirementguard/run.mjs against it. That runner only
		# talks to the supplied fixture; it never starts another Go test.
		#
		# Because it runs go test, its private HOME must PRESERVE the user's Go
		# module/build caches (scripts/lib/private-go-home.sh): a bare private HOME
		# makes Go build a private module cache of hundreds of MB whose read-only
		# files then defeat this gate's scratch cleanup. `exec` keeps the
		# backgrounded pid on the real command so stop_guards can terminate it.
		# The subshell inherits finish_browser as its EXIT trap; drop it, so a
		# failure before exec reports only this guard's own status.
		(
			trap - 0
			. "$script_dir/../lib/private-go-home.sh"
			evener_prepare_private_go_home "$guard_dir" || exit 1
			TMPDIR="$guard_dir/tmp" NODE_DISABLE_COMPILE_CACHE=1 exec npm run retirementguard
		) >"$dir/$guard.log" 2>&1 &
		;;
	skillguard)
		# The TestSkillGuard* unit tests ride along: they cover the failure
		# reporting this guard leans on, they need no browser, and the
		# browserguard tag is the only build that compiles them.
		(trap - 0; cd "$repo_root" && exec go test -tags browserguard ./cmd/evener-hub -run '^TestSkillComposerBrowser$|^TestSkillGuard' -count=1) >"$dir/$guard.log" 2>&1 &
		;;
	*)
		HOME="$guard_dir/home" TMPDIR="$guard_dir/tmp" XDG_CONFIG_HOME="$guard_dir/xdg-config" XDG_CACHE_HOME="$guard_dir/xdg-cache" XDG_STATE_HOME="$guard_dir/xdg-state" NODE_DISABLE_COMPILE_CACHE=1 node "scripts/$guard/run.mjs" >"$dir/$guard.log" 2>&1 &
		;;
	esac
	guard_pids[$index]=$!
	defer_signals=0
	[ "$pending_signal" -eq 0 ] || interrupted_browser "$pending_signal"
}

# The guards are independent (each has its own Chrome profile, ephemeral
# ports, private roots and Vite cache), so up to $slots of them run at once,
# and a finished guard's slot goes straight to the next one waiting. Each
# guard is a real browser (and most a Vite dev server) whose tripwires assume
# it gets CPU, so the slots are the machine's spare cores: all at once on an
# idle CI runner, one at a time on a saturated one. BROWSER_GUARD_CONCURRENCY
# overrides that. Every guard runs to its verdict so one failure does not hide
# another; verdicts print in the fixed order above once all have finished,
# and the exit status is the first nonzero one in that order.
slots=${BROWSER_GUARD_CONCURRENCY:-$(load_aware_workers 0)}
# Digits only, read as decimal (08 is not octal, 00 is zero), at least one: a
# value [ could not compare would never start a guard and hang the gate.
case "$slots" in
''|*[!0-9]*) slots=1 ;;
*) slots=$((10#$slots)); [ "$slots" -ge 1 ] || slots=1 ;;
esac

guard_status=()
next=0 running=0 done_count=0
while [ "$done_count" -lt "${#guards[@]}" ]; do
	while [ "$running" -lt "$slots" ] && [ "$next" -lt "${#guards[@]}" ]; do
		if [ "${guards[$next]}" = skillguard ] && [ "$build_status" -ne 0 ]; then
			guard_status[$next]=$build_status
			next=$((next + 1)); done_count=$((done_count + 1))
			continue
		fi
		start_guard "$next"
		next=$((next + 1)); running=$((running + 1))
	done
	reaped=0
	for i in "${!guards[@]}"; do
		pid=${guard_pids[$i]-}
		[ -n "$pid" ] || continue
		owned_guard_running "$pid" && continue
		if wait "$pid"; then guard_status[$i]=0; else guard_status[$i]=$?; fi
		guard_pids[$i]=""
		running=$((running - 1)); done_count=$((done_count + 1)); reaped=1
	done
	# Nothing finished: look again shortly. The poll only paces the scheduler;
	# every guard's own result still comes from wait.
	[ "$reaped" -eq 1 ] || sleep 0.2
done

for i in "${!guards[@]}"; do
	guard=${guards[$i]}
	if [ "${guard_status[$i]}" -eq 0 ]; then
		printf 'PASS  web-%s\n' "$guard"
	elif [ "$guard" = skillguard ] && [ "$build_status" -ne 0 ]; then
		printf 'FAIL  web-skillguard (frontend build, exit %s)\n' "$build_status" >&2
		cat "$dir/skillguard-build.log"
		[ "$status" -ne 0 ] || status="$build_status"
	else
		printf 'FAIL  web-%s (exit %s)\n' "$guard" "${guard_status[$i]}" >&2
		cat "$dir/$guard.log"
		[ "$status" -ne 0 ] || status="${guard_status[$i]}"
	fi
done

complete=1
exit "$status"
