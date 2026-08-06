#!/usr/bin/env bash
# make-selftest-selftest.sh - exercise the real Makefile selftest wave's
# normal and interrupted lifecycle with controllable worker suites.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd -P)"
real_make="$(command -v make)"
work="$(mktemp -d -t serf-make-selftest-selftest.XXXXXX)"
if [ "${MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD:-}" = 1 ] && [ -n "${MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD_WORK:-}" ]; then
	# Child-mode instance: use the parent's case dir instead of a fresh mktemp, so nothing leaks.
	rmdir "$work" 2>/dev/null || :
	work="$MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD_WORK"
	mkdir -p "$work"
fi
makefile="$repo_root/Makefile"
make_args=(-f "$makefile" -C "$work" --no-print-directory "SELFTEST_SCRIPTS=probe-one probe-two" selftest)
checks=0
fails=0
make_pid=""
make_status=""
unrelated_pid=""
interrupt_child_pid=""
reaped_fifo=""
reap_release_fifo=""
fixture_reapers_before_make_kill=0
ready_failure_event=""
make_record_delay=""
make_publish_delay=""

kill_if_alive() {
	local pid="${1:-}"
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		kill -KILL "$pid" 2>/dev/null || :
		wait "$pid" 2>/dev/null || :
	fi
}

cleanup() {
	# Terminate Make through its owning wrapper and wait on it (a real child of this
	# process) instead of signalling the make-child pid directly: that way the wrapper's
	# own `wait` reaps it, so a later `kill -0` from a caller can't observe a zombie.
	stop_make_with_readiness_events
	if [ -n "${case_state:-}" ]; then
		kill_fixture_workers
	fi
	if [ -n "$interrupt_child_pid" ]; then
		kill -TERM "$interrupt_child_pid" 2>/dev/null || :
		wait "$interrupt_child_pid" 2>/dev/null || :
	fi
	kill_if_alive "$unrelated_pid"
	rm -rf "$work"
}
trap cleanup EXIT

ok() { checks=$((checks + 1)); printf 'ok   - %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL - %s\n' "$1"; }
assert_eq() {
	if [ "$1" = "$2" ]; then
		ok "$3"
	else
		bad "$3 (want '$2', got '$1')"
	fi
}

new_case() {
	case_dir="$work/case.$RANDOM"
	case_state="$case_dir/state"
	case_tmp="$case_dir/tmp"
	case_bin="$case_dir/bin"
	reaped_fifo=""
	reap_release_fifo=""
	fixture_reapers_before_make_kill=0
	make_record_delay=""
	make_publish_delay=""
	interrupt_child_pid=""
	mkdir -p "$case_state" "$case_tmp" "$case_bin" "$work/scripts"
	mkfifo "$case_state/ready" "$case_state/stopped" "$case_state/reaped" "$case_state/reap-release"
	cat >"$case_bin/mktemp" <<'STUB'
#!/usr/bin/env bash
set -u
if [ "${1:-}" = "-d" ] && [ "${2:-}" = "-t" ]; then
	dir="$(/usr/bin/mktemp -d "$TMPDIR/$3.XXXXXX")" || exit 1
printf '%s\n' "$dir" >"$PROBE_STATE/logdir"
printf '%s\n' "$dir"
exit 0
fi
exec /usr/bin/mktemp "$@"
STUB
	chmod +x "$case_bin/mktemp"
	cat >"$work/scripts/probe-one-selftest.sh" <<'STUB'
#!/usr/bin/env bash
set -u
name="${0##*/}"
name="${name%-selftest.sh}"
worker_pid="$$"
if [ "${PROBE_MODE:-pass}" = hold ] && [ -n "${PROBE_REAP_FIFO:-}" ]; then
	# Keep this wrapper alive until the harness releases the observed reaping event.
	trap '' HUP INT TERM
	if [ "${PROBE_IGNORE_TERM:-}" = "$name" ]; then
		( trap '' HUP INT TERM; exec sleep 30 ) &
	else
		sleep 30 &
	fi
	worker_pid="$!"
fi
printf '%s\n' "$worker_pid" >"$PROBE_STATE/$name.pid"
: >"$PROBE_STATE/$name.started"
if [ -n "${PROBE_READY_FIFO:-}" ]; then
	# A terminal event lets the reader fail even though the FIFO stays open read/write.
	trap 'printf "worker-exited:%s\n" "$name" >"$PROBE_READY_FIFO"' EXIT
	case " ${PROBE_SKIP_READY:-} " in
		*" $name "*) ;;
		*)
			[ -n "${PROBE_READY_DELAY:-}" ] && sleep "$PROBE_READY_DELAY"
			printf '%s\n' "$name" >"$PROBE_READY_FIFO"
			;;
	esac
fi
if [ "${PROBE_MODE:-pass}" = hold ] && [ "${PROBE_SPAWN_DESCENDANT:-}" = "$name" ]; then
	# A forked (not exec'd) descendant: run_worker's forwarding trap only ever signals the
	# probe's own pid ($child), so this process is reachable only if Make signals the whole
	# process group -- a direct, non-forwarded kernel delivery this descendant does not ignore.
	sleep 30 &
	printf '%s\n' "$!" >"$PROBE_STATE/$name.descendant.pid"
fi
case "${PROBE_MODE:-pass}" in
	hold)
		if [ "${PROBE_EXIT_FAST:-}" = "$name" ]; then
			# Exits (not held) once released, so the harness can control exactly when this
			# worker is reaped while the other one stays up -- the window the recipe's
			# per-worker pid-file removal must close before any interrupt can read it.
			if [ -n "${PROBE_REAP_RELEASE_FIFO:-}" ]; then
				IFS= read -r <"$PROBE_REAP_RELEASE_FIFO" || :
			fi
			exit 0
		fi
		if [ -n "${PROBE_REAP_FIFO:-}" ]; then
			wait "$worker_pid" 2>/dev/null || :
			IFS= read -r <"$PROBE_REAP_RELEASE_FIFO" || exit 1
			trap - EXIT
			if [ -e "$PROBE_MAKE_KILLED_MARKER" ]; then phase=after; else phase=before; fi
			printf 'worker-reaped-%s:%s\n' "$phase" "$name" >"$PROBE_REAP_FIFO"
			exit 0
		fi
		if [ "${PROBE_IGNORE_TERM:-}" = "$name" ]; then
			trap '' HUP INT TERM
			exec sleep 30
		fi
		exec sleep 30
		;;
	fail)
		if [ "$name" = "${PROBE_FAIL:-}" ]; then
			printf '%s failed\n' "$name" >&2
			exit 1
		fi
		;;
esac
exit 0
STUB
	cp "$work/scripts/probe-one-selftest.sh" "$work/scripts/probe-two-selftest.sh"
	chmod +x "$work/scripts/probe-one-selftest.sh" "$work/scripts/probe-two-selftest.sh"
}

run_make() {
	local mode="$1" output="$2" fail_name="${3:-}"
	PROBE_STATE="$case_state" PROBE_MODE="$mode" PROBE_FAIL="$fail_name" PROBE_READY_FIFO= PROBE_SKIP_READY= PROBE_READY_DELAY= PROBE_MAKE_RECORD_DELAY= PROBE_MAKE_PUBLISH_DELAY= PROBE_SPAWN_DESCENDANT= PROBE_EXIT_FAST= TMPDIR="$case_tmp" PATH="$case_bin:/usr/bin:/bin" \
		"$real_make" "${make_args[@]}" >"$output" 2>&1
}

run_make_with_readiness_events() {
	local mode="$1" output="$2" skip_ready="${3:-}" ignore_term="${4:-}" fail_name="${5:-}" ready_delay="${6:-}" spawn_descendant="${7:-}" exit_fast="${8:-}"
	(
		# The wrapper converts Make exit into a terminal FIFO event and forwards interrupts.
		export PROBE_STATE="$case_state" PROBE_MODE="$mode" PROBE_FAIL="$fail_name" \
			PROBE_SKIP_READY="$skip_ready" PROBE_IGNORE_TERM="$ignore_term" PROBE_READY_FIFO="$ready_fifo" PROBE_STOP_FIFO="$stop_fifo" \
			PROBE_READY_DELAY="$ready_delay" PROBE_SPAWN_DESCENDANT="$spawn_descendant" PROBE_EXIT_FAST="$exit_fast" TMPDIR="$case_tmp" PATH="$case_bin:/usr/bin:/bin"
		export PROBE_REAP_FIFO="$reaped_fifo" PROBE_REAP_RELEASE_FIFO="$reap_release_fifo" \
			PROBE_MAKE_KILLED_MARKER="$case_state/make-child-killed" PROBE_MAKE_RECORD_DELAY="$make_record_delay" \
			PROBE_MAKE_PUBLISH_DELAY="$make_publish_delay"
		child_pid=""
		interrupted_at_startup=0
		forward_interrupt() {
			kill -TERM "$child_pid" 2>/dev/null || :
			if wait "$child_pid" 2>/dev/null; then rc=0; else rc=$?; fi
			printf 'make-stopped:%s\n' "$rc" >"$PROBE_STOP_FIFO"
			exit 143
		}
		# Until Make's pid is in hand there is nothing to forward an interrupt to, so a signal
		# arriving in that window is recorded and acted on below rather than running the
		# handler empty-handed, which would report a stop the wrapper never performed.
		trap 'interrupted_at_startup=1' HUP INT TERM
		# Make records its own pid before exec'ing itself, so the pid file exists before Make
		# can run for even an instant: a missing pid file therefore proves Make never started,
		# which is what lets the stop ladder treat "nothing recorded" as "nothing to kill".
		# Fixture knob: hold the trampoline before it publishes, and mark that it is holding, so
		# the pre-publication interval can be interrupted deliberately instead of raced. The hold
		# is an in-process read against a FIFO nobody ever writes: a `sleep` helper would be a
		# second process to orphan when the trampoline is terminated, and one no survivor check
		# could see, since its command line names neither the case nor the work dir.
		bash -c '[ -n "$1" ] && { mkfifo "$2.publishing"; exec 5<>"$2.publishing"; read -t "$1" -u 5 || :; }; printf "%s\n" "$$" >"$2"; shift 2; exec "$@"' \
			_ "$PROBE_MAKE_PUBLISH_DELAY" "$PROBE_STATE/make-child.pid" \
			"$real_make" "${make_args[@]}" >"$output" 2>&1 &
		# Fixture knob: widen the wrapper's own spawn-to-record window for the startup-boundary check.
		[ -n "$PROBE_MAKE_RECORD_DELAY" ] && sleep "$PROBE_MAKE_RECORD_DELAY"
		child_pid="$!"
		trap forward_interrupt HUP INT TERM
		[ "$interrupted_at_startup" -eq 1 ] && forward_interrupt
		if wait "$child_pid"; then rc=0; else rc=$?; fi
		printf 'make-exited:%s\n' "$rc" >"$PROBE_READY_FIFO"
		exit "$rc"
	) &
	make_pid="$!"
}

# Give the wrapper one bounded chance to report Make's exit and be reaped normally.
# The report may legitimately never come (a worker that ignores TERM stalls Make
# indefinitely), so this event -- and only this kind of event -- gets a timeout;
# a caller that gets nonzero back must escalate rather than assume reaping happened.
await_wrapper_stop() {
	local pid="$1" stop_event=""
	IFS= read -r -t 1 -u 8 stop_event || return 1
	if wait "$pid"; then make_status=0; else make_status=$?; fi
}

# The ladder's last tier: SIGKILL the recorded Make child, then wait -- with no timeout --
# for the wrapper, Make's real parent, to reap it and exit.
# This wait needs no bound because the event is guaranteed. Once Make is SIGKILLed the
# wrapper's own `wait` must return, and the terminal FIFO write that follows cannot block:
# every fixture FIFO is held open read/write both here and through the wrapper's inherited
# fds, so the open always finds a reader and a one-line write always fits the pipe buffer.
# Racing a bounded window here instead would let us SIGKILL the wrapper mid-reap and leave
# Make briefly observable as an unreaped zombie. No recorded child pid means Make never
# started -- it records its own pid before exec'ing -- so there is nothing to orphan and
# nothing whose death would release the wrapper; SIGKILLing the wrapper itself is then
# both safe and equally certain to end the wait.
kill_make_child_and_reap_wrapper() {
	local pid="$1" child_pid=""
	child_pid="$(cat "$case_state/make-child.pid" 2>/dev/null || :)"
	: >"$case_state/make-child-killed"
	if [ -n "$child_pid" ]; then
		kill -KILL "$child_pid" 2>/dev/null || :
	else
		kill -KILL "$pid" 2>/dev/null || :
	fi
	wait "$pid" 2>/dev/null || :
	make_status=137
}

stop_make_with_readiness_events() {
	local pid="$make_pid"
	[ -n "$pid" ] || return 0
	kill -TERM "$pid" 2>/dev/null || :
	if ! await_wrapper_stop "$pid"; then
		# Keep Make alive while fixture wrappers reap their recorded workers.
		kill_fixture_workers
		if ! wait_for_fixture_reapers; then
			# The reap-ack deadline was exhausted: stop trusting the graceful ladder as if
			# reaping had been confirmed, and take the recorded hierarchy down through its
			# own ownership chain instead.
			kill_fixture_workers
			kill_make_child_and_reap_wrapper "$pid"
		elif ! await_wrapper_stop "$pid"; then
			kill_make_child_and_reap_wrapper "$pid"
		fi
	fi
	make_pid=""
}

kill_fixture_workers() {
	local name pid
	for name in probe-one probe-two; do
		pid="$(cat "$case_state/$name.pid" 2>/dev/null || :)"
		[ -n "$pid" ] && kill -KILL "$pid" 2>/dev/null || :
	done
}

release_fixture_reapers() {
	[ -n "$reap_release_fifo" ] || return 0
	printf 'release\nrelease\n' >&6
}

wait_for_fixture_reapers() {
	local reaped="" probe_one_reaped=0 probe_two_reaped=0 deadline_elapsed=0
	[ -n "$reaped_fifo" ] || return 0
	release_fixture_reapers
	# 10 * 1s reads = 10s total, the same contention-tolerant allowance as the readiness wait:
	# one slow scheduler tick on a loaded host must not be mistaken for a stuck reaper.
	while [ "$((probe_one_reaped + probe_two_reaped))" -lt 2 ] && [ "$deadline_elapsed" -lt 10 ]; do
		if ! IFS= read -r -t 1 -u 7 reaped; then
			deadline_elapsed=$((deadline_elapsed + 1))
			continue
		fi
		case "$reaped" in
			worker-reaped-before:probe-one)
				[ "$probe_one_reaped" -eq 0 ] || return 1
				probe_one_reaped=1
				;;
			worker-reaped-before:probe-two)
				[ "$probe_two_reaped" -eq 0 ] || return 1
				probe_two_reaped=1
				;;
			*) return 1 ;;
		esac
	done
	[ "$((probe_one_reaped + probe_two_reaped))" -eq 2 ] || return 1
	fixture_reapers_before_make_kill=1
}

wait_for_ready_workers() {
	local ready="" probe_one_ready=0 probe_two_ready=0
	ready_failure_event=""
	while [ "$((probe_one_ready + probe_two_ready))" -lt 2 ]; do
		# Bound a hung worker without polling; readiness and terminal events still drive progress.
		# 10s covers full-gate contention startup delay; shorter timeouts regressed a healthy-but-slow worker to "hung".
		if ! IFS= read -r -t 10 -u 9 ready; then
			ready_failure_event=timeout
			stop_make_with_readiness_events
			return 1
		fi
		case "$ready" in
			probe-one)
				[ "$probe_one_ready" -eq 0 ] || { ready_failure_event="$ready"; stop_make_with_readiness_events; return 1; }
				probe_one_ready=1
				;;
			probe-two)
				[ "$probe_two_ready" -eq 0 ] || { ready_failure_event="$ready"; stop_make_with_readiness_events; return 1; }
				probe_two_ready=1
				;;
			worker-exited:probe-one)
				# A ready worker's own exit isn't a readiness failure for anyone.
				[ "$probe_one_ready" -eq 1 ] || { ready_failure_event="$ready"; stop_make_with_readiness_events; return 1; }
				;;
			worker-exited:probe-two)
				[ "$probe_two_ready" -eq 1 ] || { ready_failure_event="$ready"; stop_make_with_readiness_events; return 1; }
				;;
			worker-exited:*|make-exited:*) ready_failure_event="$ready"; stop_make_with_readiness_events; return 1 ;;
			*) ready_failure_event="$ready"; stop_make_with_readiness_events; return 1 ;;
		esac
	done
}

if [ "${MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD:-}" = 1 ]; then
	# Child-mode instance for the interruption-reaping check below: set up one hung-readiness
	# wave and block in the readiness wait until the parent sends TERM. Never spawns a child itself.
	# Reap-fifo plumbing (the same handshake the TERM-ignoring case uses) makes this instance's
	# own cleanup() wait for an actual probe-reaped event instead of leaving it to be inferred.
	new_case
	# The parent sets this to hold the wrapper inside its spawn-to-record window while the
	# interrupt lands, so the startup boundary is exercised rather than raced.
	make_record_delay="${MAKE_SELFTEST_SELFTEST_MAKE_RECORD_DELAY:-}"
	make_publish_delay="${MAKE_SELFTEST_SELFTEST_MAKE_PUBLISH_DELAY:-}"
	ready_fifo="$case_state/ready"
	stop_fifo="$case_state/stopped"
	reaped_fifo="$case_state/reaped"
	reap_release_fifo="$case_state/reap-release"
	exec 9<>"$ready_fifo"
	exec 8<>"$stop_fifo"
	exec 7<>"$reaped_fifo"
	exec 6<>"$reap_release_fifo"
	run_make_with_readiness_events hold "$case_dir/child.out" probe-two
	wait_for_ready_workers || :
	exit 0
fi

new_case
normal_out="$case_dir/normal.out"
if run_make pass "$normal_out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "a successful selftest wave exits zero"
for name in probe-one probe-two; do
	count="$(grep -c "^PASS  $name" "$normal_out" 2>/dev/null || :)"
	assert_eq "$count" "1" "the successful wave reports $name once"
done
normal_logdir="$(cat "$case_state/logdir" 2>/dev/null || :)"
[ -n "$normal_logdir" ] && [ ! -e "$normal_logdir" ] && ok "a successful wave removes its temporary logs" || bad "a successful wave leaves its temporary logs"

new_case
failure_out="$case_dir/failure.out"
if run_make fail "$failure_out" probe-one; then rc=0; else rc=$?; fi
[ "$rc" -ne 0 ] && ok "a failed selftest wave exits nonzero" || bad "a failed selftest wave exits zero"
for name in probe-one probe-two; do
	if [ "$name" = probe-one ]; then expected=FAIL; else expected=PASS; fi
	count="$(grep -c "^$expected  $name" "$failure_out" 2>/dev/null || :)"
	assert_eq "$count" "1" "the failed wave reports $expected $name once"
done
failure_logdir="$(cat "$case_state/logdir" 2>/dev/null || :)"
[ -n "$failure_logdir" ] && [ ! -e "$failure_logdir" ] && ok "a failed wave removes its temporary logs" || bad "a failed wave leaves its temporary logs"

new_case
missing_ready_out="$case_dir/missing-ready.out"
ready_fifo="$case_state/ready"
stop_fifo="$case_state/stopped"
exec 9<>"$ready_fifo"
exec 8<>"$stop_fifo"
run_make_with_readiness_events pass "$missing_ready_out" probe-two
if wait_for_ready_workers; then
	bad "the readiness fixture rejects a worker that exits before readiness"
else
	ok "the readiness fixture rejects a worker that exits before readiness"
fi
assert_eq "$ready_failure_event" "worker-exited:probe-two" "the missing-readiness rejection is caused by probe-two's own exit"
exec 9>&-
exec 8>&-
assert_eq "$make_pid" "" "the missing-readiness wave reaps its Make wrapper"

new_case
hung_ready_out="$case_dir/hung-ready.out"
ready_fifo="$case_state/ready"
stop_fifo="$case_state/stopped"
exec 9<>"$ready_fifo"
exec 8<>"$stop_fifo"
run_make_with_readiness_events hold "$hung_ready_out" probe-two
if wait_for_ready_workers; then
	bad "the readiness fixture rejects a hung worker before readiness"
else
	ok "the readiness fixture rejects a hung worker before readiness"
fi
assert_eq "$ready_failure_event" "timeout" "the hung-readiness rejection is a timeout"
exec 9>&-
exec 8>&-
assert_eq "$make_pid" "" "the hung-readiness wave reaps its Make wrapper"

new_case
delayed_ready_out="$case_dir/delayed-ready.out"
ready_fifo="$case_state/ready"
stop_fifo="$case_state/stopped"
exec 9<>"$ready_fifo"
exec 8<>"$stop_fifo"
run_make_with_readiness_events hold "$delayed_ready_out" "" "" "" 6
if wait_for_ready_workers; then
	ok "the readiness wait tolerates workers that take 6s to signal readiness"
else
	bad "the readiness wait tolerates workers that take 6s to signal readiness"
fi
exec 9>&-
stop_make_with_readiness_events
exec 8>&-
for name in probe-one probe-two; do
	pid="$(cat "$case_state/$name.pid" 2>/dev/null || :)"
	[ -n "$pid" ] && kill -KILL "$pid" 2>/dev/null || :
done

new_case
term_ignore_out="$case_dir/term-ignore.out"
ready_fifo="$case_state/ready"
stop_fifo="$case_state/stopped"
reaped_fifo="$case_state/reaped"
reap_release_fifo="$case_state/reap-release"
exec 9<>"$ready_fifo"
exec 8<>"$stop_fifo"
exec 7<>"$reaped_fifo"
exec 6<>"$reap_release_fifo"
run_make_with_readiness_events hold "$term_ignore_out" probe-two probe-two
if wait_for_ready_workers; then
	bad "the readiness fixture rejects a TERM-ignoring worker before readiness"
else
	ok "the readiness fixture rejects a TERM-ignoring worker before readiness"
fi
assert_eq "$ready_failure_event" "timeout" "the TERM-ignoring rejection is a timeout"
exec 9>&-
exec 8>&-
if [ "$fixture_reapers_before_make_kill" -eq 1 ]; then
	ok "the TERM-ignoring wave reaps both recorded workers before Make is killed"
else
	bad "the TERM-ignoring wave reaps both recorded workers before Make is killed"
fi
release_fixture_reapers
exec 7>&-
exec 6>&-
assert_eq "$make_pid" "" "the TERM-ignoring wave reaps its Make wrapper"
# `kill -0` succeeds for a zombie too, so this also catches a Make child that was killed
# but left unreaped by the wrapper that owns it.
term_ignore_child_pid="$(cat "$case_state/make-child.pid" 2>/dev/null || :)"
if [ -n "$term_ignore_child_pid" ] && kill -0 "$term_ignore_child_pid" 2>/dev/null; then
	bad "a TERM-ignoring wave reaps its Make child"
	kill -KILL "$term_ignore_child_pid" 2>/dev/null || :
else
	ok "a TERM-ignoring wave reaps its Make child"
fi
for name in probe-one probe-two; do
	pid="$(cat "$case_state/$name.pid" 2>/dev/null || :)"
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		bad "a TERM-ignoring wave leaves $name alive"
		kill -KILL "$pid" 2>/dev/null || :
	else
		ok "a TERM-ignoring wave reaps $name"
	fi
done

new_case
interrupt_out="$case_dir/interrupted.out"
ready_fifo="$case_state/ready"
stop_fifo="$case_state/stopped"
exec 9<>"$ready_fifo"
exec 8<>"$stop_fifo"
run_make_with_readiness_events hold "$interrupt_out"
if wait_for_ready_workers; then
	ok "the interruption fixture receives both worker readiness events"
else
	bad "the interruption fixture receives both worker readiness events"
fi
exec 9>&-
if [ -f "$case_state/probe-one.started" ] && [ -f "$case_state/probe-two.started" ]; then
	ok "the interruption fixture records both workers as started"
else
	bad "the interruption fixture did not record both workers as started"
fi
sleep 30 &
unrelated_pid="$!"
stop_make_with_readiness_events
exec 8>&-
[ "$make_status" -ne 0 ] && ok "an interrupted wave exits nonzero" || bad "an interrupted wave exits zero"
interrupted_child_pid="$(cat "$case_state/make-child.pid" 2>/dev/null || :)"
if [ -n "$interrupted_child_pid" ] && kill -0 "$interrupted_child_pid" 2>/dev/null; then
	bad "an interrupted wave reaps its Make child"
	kill -KILL "$interrupted_child_pid" 2>/dev/null || :
else
	ok "an interrupted wave reaps its Make child"
fi
interrupted_logdir="$(cat "$case_state/logdir" 2>/dev/null || :)"
[ -n "$interrupted_logdir" ] && [ ! -e "$interrupted_logdir" ] && ok "an interrupted wave removes its temporary logs" || bad "an interrupted wave leaves its temporary logs"
for name in probe-one probe-two; do
	pid="$(cat "$case_state/$name.pid" 2>/dev/null || :)"
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		bad "an interrupted wave leaves $name alive"
		kill -KILL "$pid" 2>/dev/null || :
	else
		ok "an interrupted wave reaps $name"
	fi
done
if kill -0 "$unrelated_pid" 2>/dev/null; then
	ok "an interrupted wave leaves unrelated processes alone"
else
	bad "an interrupted wave killed an unrelated process"
fi
kill -TERM "$unrelated_pid" 2>/dev/null || :
wait "$unrelated_pid" 2>/dev/null || :
unrelated_pid=""

new_case
descendant_out="$case_dir/descendant.out"
ready_fifo="$case_state/ready"
stop_fifo="$case_state/stopped"
exec 9<>"$ready_fifo"
exec 8<>"$stop_fifo"
run_make_with_readiness_events hold "$descendant_out" "" "" "" "" probe-two
if wait_for_ready_workers; then
	ok "the descendant-escape fixture receives both worker readiness events"
else
	bad "the descendant-escape fixture receives both worker readiness events"
fi
exec 9>&-
descendant_pid="$(cat "$case_state/probe-two.descendant.pid" 2>/dev/null || :)"
if [ -n "$descendant_pid" ] && kill -0 "$descendant_pid" 2>/dev/null; then
	ok "the descendant-escape fixture records a live forked descendant before interruption"
else
	bad "the descendant-escape fixture records a live forked descendant before interruption"
fi
stop_make_with_readiness_events
exec 8>&-
[ "$make_status" -ne 0 ] && ok "an interrupted wave with a forked descendant exits nonzero" || bad "an interrupted wave with a forked descendant exits nonzero"
for name in probe-one probe-two; do
	pid="$(cat "$case_state/$name.pid" 2>/dev/null || :)"
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		bad "an interrupted wave with a forked descendant leaves $name alive"
		kill -KILL "$pid" 2>/dev/null || :
	else
		ok "an interrupted wave with a forked descendant reaps $name"
	fi
done
if [ -n "$descendant_pid" ] && kill -0 "$descendant_pid" 2>/dev/null; then
	bad "an interrupted wave reaches a probe's forked (not exec'd) descendant"
	kill -KILL "$descendant_pid" 2>/dev/null || :
else
	ok "an interrupted wave reaches a probe's forked (not exec'd) descendant"
fi

new_case
stale_pid_out="$case_dir/stale-pid.out"
ready_fifo="$case_state/ready"
stop_fifo="$case_state/stopped"
reap_release_fifo="$case_state/reap-release"
exec 9<>"$ready_fifo"
exec 8<>"$stop_fifo"
exec 6<>"$reap_release_fifo"
sleep 30 &
sentinel_pid="$!"
run_make_with_readiness_events hold "$stale_pid_out" "" "" "" "" "" probe-one
if wait_for_ready_workers; then
	ok "the stale-pid fixture receives both worker readiness events"
else
	bad "the stale-pid fixture receives both worker readiness events"
fi
logdir=""
attempt=0
while [ "$attempt" -lt 200 ]; do
	logdir="$(cat "$case_state/logdir" 2>/dev/null || :)"
	[ -n "$logdir" ] && [ -f "$logdir/probe-one.pid" ] && break
	attempt=$((attempt + 1))
	sleep 0.05
done
real_probe_one_pid=""
if [ -n "$logdir" ] && [ -f "$logdir/probe-one.pid" ]; then
	real_probe_one_pid="$(cat "$logdir/probe-one.pid" 2>/dev/null || :)"
fi
if [ -n "$logdir" ] && [ -n "$real_probe_one_pid" ]; then
	ok "the stale-pid fixture discovers the recipe's own recorded worker pid"
else
	bad "the stale-pid fixture discovers the recipe's own recorded worker pid"
fi
if [ -z "$logdir" ] || [ -z "$real_probe_one_pid" ]; then
	# Discovery failed: abort rather than let an empty logdir turn the overwrite below into a
	# write against "/probe-one.pid" at the filesystem root instead of the worker's real dir.
	for contract in "the stale-pid fixture's released worker actually exits" \
		"the recipe removes a reaped worker's pid file promptly" \
		"an interrupted wave during a stale pid file exits nonzero" \
		"an interrupted wave never signals a sentinel behind a reaped worker's stale pid file" \
		"an interrupted wave during a stale pid file reaps probe-two"; do
		bad "$contract (logdir discovery failed)"
	done
	printf 'release\n' >&6
	exec 9>&-
	exec 6>&-
	stop_make_with_readiness_events
	exec 8>&-
else
	# probe-one is blocked on the reap-release FIFO (PROBE_EXIT_FAST) rather than exiting on its
	# own, so this overwrite lands strictly before run_worker reaps it -- a causal order, not a
	# race the harness has to win. The recipe's own rm -f removes the file by name once it reaps
	# probe-one, regardless of the value currently inside; that is what this case exercises.
	printf '%s\n' "$sentinel_pid" >"$logdir/probe-one.pid"
	printf 'release\n' >&6
	exited_event=""
	IFS= read -r -t 10 -u 9 exited_event || :
	assert_eq "$exited_event" "worker-exited:probe-one" "the stale-pid fixture's released worker actually exits"
	exec 9>&-
	exec 6>&-
	# The worker's own process exiting and run_worker (a different process) removing its pid file
	# are two separate events; proving the first happened does not prove the second already has.
	# Poll for the file's actual disappearance -- the observable this case exists to check -- before
	# sending the interrupt, so the sentinel assertion below is not itself racing that removal.
	attempt=0
	pid_file_removed=0
	while [ "$attempt" -lt 100 ]; do
		[ -e "$logdir/probe-one.pid" ] || { pid_file_removed=1; break; }
		attempt=$((attempt + 1))
		sleep 0.05
	done
	if [ "$pid_file_removed" -eq 1 ]; then
		ok "the recipe removes a reaped worker's pid file promptly"
	else
		bad "the recipe removes a reaped worker's pid file promptly"
	fi
	stop_make_with_readiness_events
	exec 8>&-
	[ "$make_status" -ne 0 ] && ok "an interrupted wave during a stale pid file exits nonzero" || bad "an interrupted wave during a stale pid file exits nonzero"
	if kill -0 "$sentinel_pid" 2>/dev/null; then
		ok "an interrupted wave never signals a sentinel behind a reaped worker's stale pid file"
	else
		bad "an interrupted wave never signals a sentinel behind a reaped worker's stale pid file"
	fi
	pid="$(cat "$case_state/probe-two.pid" 2>/dev/null || :)"
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		bad "an interrupted wave during a stale pid file leaves probe-two alive"
		kill -KILL "$pid" 2>/dev/null || :
	else
		ok "an interrupted wave during a stale pid file reaps probe-two"
	fi
fi
kill -TERM "$sentinel_pid" 2>/dev/null || :
wait "$sentinel_pid" 2>/dev/null || :

new_case
child_work="$case_dir/interrupt-child"
mkdir -p "$child_work"
MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD=1 MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD_WORK="$child_work" bash "$0" >"$case_dir/interrupt-child.out" 2>&1 &
interrupt_child_pid="$!"
child_case_state=""
attempt=0
# 200 * 0.05s = 10s, matching the readiness wait's own full-gate-contention startup allowance.
while [ "$attempt" -lt 200 ]; do
	child_case_state="$(ls -d "$child_work"/case.*/state 2>/dev/null | head -n1 || :)"
	if [ -n "$child_case_state" ] && [ -f "$child_case_state/make-child.pid" ] \
		&& [ -f "$child_case_state/probe-one.pid" ] && [ -f "$child_case_state/probe-two.pid" ]; then
		break
	fi
	attempt=$((attempt + 1))
	sleep 0.05
done
if [ -n "$child_case_state" ] && [ -f "$child_case_state/make-child.pid" ] \
	&& [ -f "$child_case_state/probe-one.pid" ] && [ -f "$child_case_state/probe-two.pid" ]; then
	make_child_pid="$(cat "$child_case_state/make-child.pid")"
	probe_one_pid="$(cat "$child_case_state/probe-one.pid")"
	probe_two_pid="$(cat "$child_case_state/probe-two.pid")"
	kill -TERM "$interrupt_child_pid" 2>/dev/null || :
	# The child's own cleanup() reaps its whole hierarchy through real completion events before
	# it exits: Make through its wrapper's `wait`, and the probes (reap-fifo plumbed above, so
	# their wrapper survives Make's death and only reports reaped once its own bg job is waited
	# on) through wait_for_fixture_reapers' blocking FIFO read. So by the time `wait` returns
	# here, nothing below is a race -- it's a single confirmation, not a poll.
	wait "$interrupt_child_pid" 2>/dev/null || :
	interrupt_child_pid=""
	if kill -0 "$make_child_pid" 2>/dev/null || kill -0 "$probe_one_pid" 2>/dev/null || kill -0 "$probe_two_pid" 2>/dev/null; then
		bad "interrupting the selftest during a readiness wait reaps the Make child and probe descendants"
		kill -KILL "$make_child_pid" "$probe_one_pid" "$probe_two_pid" 2>/dev/null || :
	else
		ok "interrupting the selftest during a readiness wait reaps the Make child and probe descendants"
	fi
else
	bad "interrupting the selftest during a readiness wait reaps the Make child and probe descendants (fixture did not start)"
	# TERM, not KILL: let the child's own EXIT cleanup reap whatever it managed to start.
	kill -TERM "$interrupt_child_pid" 2>/dev/null || :
	wait "$interrupt_child_pid" 2>/dev/null || :
	interrupt_child_pid=""
fi
rm -rf "$child_work"

new_case
startup_work="$case_dir/startup-child"
mkdir -p "$startup_work"
# Interrupt a child instance while its wrapper still has Make spawned but unrecorded: the
# record delay holds the wrapper in that window, and waiting for both probes to report
# started proves Make is genuinely running inside it.
MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD=1 MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD_WORK="$startup_work" \
	MAKE_SELFTEST_SELFTEST_MAKE_RECORD_DELAY=5 bash "$0" >"$case_dir/startup-child.out" 2>&1 &
interrupt_child_pid="$!"
startup_case_state=""
attempt=0
while [ "$attempt" -lt 200 ]; do
	startup_case_state="$(ls -d "$startup_work"/case.*/state 2>/dev/null | head -n1 || :)"
	if [ -n "$startup_case_state" ] && [ -f "$startup_case_state/probe-one.started" ] \
		&& [ -f "$startup_case_state/probe-two.started" ]; then
		break
	fi
	attempt=$((attempt + 1))
	sleep 0.05
done
if [ -n "$startup_case_state" ] && [ -f "$startup_case_state/probe-one.started" ] \
	&& [ -f "$startup_case_state/probe-two.started" ]; then
	# Read the pids while the wave is still up: the child's cleanup removes its own tree, and
	# reading them here is the stronger claim anyway -- Make is recorded from the instant it
	# can run, even though the wrapper has not reached its own recording step yet.
	startup_make_pid="$(cat "$startup_case_state/make-child.pid" 2>/dev/null || :)"
	startup_probe_one="$(cat "$startup_case_state/probe-one.pid" 2>/dev/null || :)"
	startup_probe_two="$(cat "$startup_case_state/probe-two.pid" 2>/dev/null || :)"
	# Non-vacuity first: an absent pid file would make every liveness check below pass for
	# the wrong reason, which is exactly what an unrecorded Make child looks like.
	if [ -n "$startup_make_pid" ] && [ -n "$startup_probe_one" ] && [ -n "$startup_probe_two" ]; then
		ok "interrupting the wrapper's startup window still records the Make child and probe pids"
	else
		bad "interrupting the wrapper's startup window still records the Make child and probe pids"
	fi
	kill -TERM "$interrupt_child_pid" 2>/dev/null || :
	wait "$interrupt_child_pid" 2>/dev/null || :
	interrupt_child_pid=""
	if [ -n "$startup_make_pid" ] && kill -0 "$startup_make_pid" 2>/dev/null; then
		bad "interrupting the wrapper's startup window reaps the Make child"
		kill -KILL "$startup_make_pid" 2>/dev/null || :
	else
		ok "interrupting the wrapper's startup window reaps the Make child"
	fi
	if { [ -n "$startup_probe_one" ] && kill -0 "$startup_probe_one" 2>/dev/null; } \
		|| { [ -n "$startup_probe_two" ] && kill -0 "$startup_probe_two" 2>/dev/null; }; then
		bad "interrupting the wrapper's startup window reaps both probe workers"
		kill -KILL "$startup_probe_one" "$startup_probe_two" 2>/dev/null || :
	else
		ok "interrupting the wrapper's startup window reaps both probe workers"
	fi
	# Independent of what was recorded: no process may still be running against the child's tree.
	startup_survivors="$(pgrep -f "$startup_work" 2>/dev/null || :)"
	if [ -n "$startup_survivors" ]; then
		bad "interrupting the wrapper's startup window leaves nothing running against the child's tree"
		kill -KILL $startup_survivors 2>/dev/null || :
	else
		ok "interrupting the wrapper's startup window leaves nothing running against the child's tree"
	fi
else
	for contract in "still records the Make child and probe pids" "reaps the Make child" \
		"reaps both probe workers" "leaves nothing running against the child's tree"; do
		bad "interrupting the wrapper's startup window $contract (fixture did not start)"
	done
	# TERM, not KILL: let the child's own EXIT cleanup reap whatever it managed to start.
	kill -TERM "$interrupt_child_pid" 2>/dev/null || :
	wait "$interrupt_child_pid" 2>/dev/null || :
	interrupt_child_pid=""
fi
rm -rf "$startup_work"

new_case
publish_work="$case_dir/publish-child"
mkdir -p "$publish_work"
# Interrupt a child instance while the trampoline exists but has not yet published Make's pid.
# The publish delay holds it in that interval and the marker it writes first proves the check
# lands inside the interval rather than racing it.
MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD=1 MAKE_SELFTEST_SELFTEST_INTERRUPT_CHILD_WORK="$publish_work" \
	MAKE_SELFTEST_SELFTEST_MAKE_PUBLISH_DELAY=5 bash "$0" >"$case_dir/publish-child.out" 2>&1 &
interrupt_child_pid="$!"
publish_case_state=""
attempt=0
while [ "$attempt" -lt 200 ]; do
	publish_case_state="$(ls -d "$publish_work"/case.*/state 2>/dev/null | head -n1 || :)"
	if [ -n "$publish_case_state" ] && [ -e "$publish_case_state/make-child.pid.publishing" ]; then
		break
	fi
	attempt=$((attempt + 1))
	sleep 0.05
done
if [ -n "$publish_case_state" ] && [ -e "$publish_case_state/make-child.pid.publishing" ]; then
	# The trampoline already carries Make's argv, so it is findable by the child's work dir while
	# still unpublished -- which is what makes the assertions below non-vacuous.
	pre_publish_pids="$(pgrep -f "$publish_work" 2>/dev/null || :)"
	if [ -n "$pre_publish_pids" ] && [ ! -f "$publish_case_state/make-child.pid" ]; then
		ok "the fixture interrupts a live trampoline that has not published its pid"
	else
		bad "the fixture interrupts a live trampoline that has not published its pid"
	fi
	stop_started="$SECONDS"
	kill -TERM "$interrupt_child_pid" 2>/dev/null || :
	wait "$interrupt_child_pid" 2>/dev/null || :
	interrupt_child_pid=""
	stop_elapsed=$((SECONDS - stop_started))
	publish_survivor=""
	for pid in $pre_publish_pids; do
		kill -0 "$pid" 2>/dev/null && publish_survivor="$pid"
	done
	publish_survivors="$(pgrep -f "$publish_work" 2>/dev/null || :)"
	if [ -z "$publish_survivor" ] && [ -z "$publish_survivors" ]; then
		ok "interrupting before pid publication orphans no trampoline, Make, or probe"
	else
		bad "interrupting before pid publication orphans no trampoline, Make, or probe"
		kill -KILL $pre_publish_pids $publish_survivors 2>/dev/null || :
	fi
	# The wrapper's own handler owns this interval, so the ladder never reaches the reap-ack
	# deadline it would sit out if it had fallen through to the no-pid escalation branch.
	if [ "$stop_elapsed" -lt 10 ]; then
		ok "the stop ladder returns promptly when interrupted before pid publication"
	else
		bad "the stop ladder returns promptly when interrupted before pid publication (took ${stop_elapsed}s)"
	fi
else
	for contract in "the fixture interrupts a live trampoline that has not published its pid" \
		"interrupting before pid publication orphans no trampoline, Make, or probe" \
		"the stop ladder returns promptly when interrupted before pid publication"; do
		bad "$contract (fixture did not start)"
	done
	kill -TERM "$interrupt_child_pid" 2>/dev/null || :
	wait "$interrupt_child_pid" 2>/dev/null || :
	interrupt_child_pid=""
fi
rm -rf "$publish_work"

echo "----"
echo "make-selftest-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
