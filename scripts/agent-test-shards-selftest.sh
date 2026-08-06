#!/usr/bin/env bash
# agent-test-shards-selftest.sh - deterministic lifecycle tests for shard runs.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/agent-test-shards.sh"
work="$(mktemp -d -t agent-test-shards-selftest.XXXXXX)"
work="$(cd "$work" && pwd -P)"
trap 'rm -rf "$work"' EXIT

checks=0
fails=0
ok() { checks=$((checks + 1)); printf '  ok: %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL: %s\n' "$1"; }
assert_eq() {
	if [ "$1" = "$2" ]; then ok "$3"; else bad "$3 (want '$2', got '$1')"; fi
}
assert_has() {
	if grep -qF -- "$2" "$1"; then ok "$3"; else bad "$3 (missing '$2')"; fi
}
assert_absent() {
	if [ ! -e "$1" ]; then ok "$2"; else bad "$2 (still present: $1)"; fi
}

write_fake_mktemp() {
	cat >"$1/mktemp" <<'FAKE_MKTEMP'
#!/usr/bin/env bash
case "${1:-}" in
	-d) [ "${2:-}" = "-t" ] && exec /usr/bin/mktemp -d "$TMPDIR/$3" ;;
	-t) exec /usr/bin/mktemp "$TMPDIR/$2" ;;
esac
exec /usr/bin/mktemp "$@"
FAKE_MKTEMP
	chmod +x "$1/mktemp"
}

write_fake_go() {
	cat >"$1/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -u
if [ "${1:-}" = "env" ] && [ "${2:-}" = "GOCACHE" ]; then
	printf '%s\n' "${GOCACHE:-}"
	exit 0
fi
if [ "${1:-}" = "test" ] && [ "${2:-}" = "-c" ]; then
	output=""
	previous=""
	for arg in "$@"; do
		[ "$previous" = "-o" ] && output="$arg"
		previous="$arg"
	done
	cat >"$output" <<'FAKE_TEST_BINARY'
#!/usr/bin/env bash
set -u
if [ "${1:-}" = "-test.list" ]; then
	printf 'TestAlpha\nTestBeta\n'
	exit 0
fi
label=unknown
case " $* " in
	*TestAlpha*) label=alpha ;;
	*TestBeta*) label=beta ;;
esac
printf '%s\n' "$$" >"$FAKE_STATE/$label.pid"
case "${FAKE_MODE:-green}" in
	hold)
		sleep 1000 &
		child="$!"
		printf '%s\n' "$child" >"$FAKE_STATE/$label.child.pid"
		[ -n "${FAKE_READY_FIFO:-}" ] && printf 'ready:%s\n' "$label" >"$FAKE_READY_FIFO"
		wait "$child"
		exit "$?"
		;;
	fail)
		if [ "$label" = beta ]; then
			printf '%s\n' '--- FAIL: TestBeta (0.00s)'
			exit 1
		fi
		;;
esac
printf '%s\n' "ok $label"
exit 0
FAKE_TEST_BINARY
	chmod +x "$output"
	exit 0
fi
exit 64
FAKE_GO
	chmod +x "$1/go"
}

new_case() {
	case_root="$work/case.$RANDOM"
	tmp="$case_root/tmp"
	state="$case_root/state"
	bin="$case_root/bin"
	cache="$case_root/cache"
	mkdir -p "$tmp" "$state" "$bin" "$cache" "$case_root/agent"
	write_fake_mktemp "$bin"
	write_fake_go "$bin"
}

run_case() (
	TMPDIR="$tmp" PATH="$bin:/usr/bin:/bin" FAKE_STATE="$state" \
	AGENT_SHARD_CACHE_DIR="$cache" AGENT_SHARD_COUNT=2 \
	AGENT_SHARD_PARALLEL=1 AGENT_SHARD_NO_SURVEY=1 \
	bash "$script" -short -count=1
)

run_case_async() {
	TMPDIR="$tmp" PATH="$bin:/usr/bin:/bin" FAKE_STATE="$state" \
	AGENT_SHARD_CACHE_DIR="$cache" AGENT_SHARD_COUNT=2 \
	AGENT_SHARD_PARALLEL=1 AGENT_SHARD_NO_SURVEY=1 \
	exec env bash "$script" -short -count=1
}

full_logs_path() {
	awk '/^full logs: / { print substr($0, 12); exit }' "$1"
}

descendants() {
	local frontier="$1" found="" next
	while [ -n "$frontier" ]; do
		next="$(ps -axo pid=,ppid= | awk -v parents="$frontier" '
			BEGIN { n = split(parents, a, " "); for (i = 1; i <= n; i++) want[a[i]] = 1 }
			$2 in want { print $1 }')"
		[ -n "$next" ] || break
		found="$found $next"
		frontier="$(printf '%s\n' "$next" | tr '\n' ' ')"
	done
	printf '%s\n' "$found"
}

assert_pids_gone() {
	local pids="$1" label="$2" pid command
	for pid in $pids; do
		command="$(ps -p "$pid" -o command= 2>/dev/null || :)"
		if [ -n "$command" ]; then
			bad "$label leaves process $pid alive: $command"
			kill -KILL "$pid" 2>/dev/null || :
		else
			ok "$label reaps process $pid"
		fi
	done
}

new_case
out="$case_root/green.out"
if run_case >"$out" 2>&1; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "a fully passing shard run exits zero"
assert_has "$out" "PASS  agent:0" "the first passing shard reports PASS"
assert_has "$out" "PASS  agent:1" "the second passing shard reports PASS"
logs="$(full_logs_path "$out")"
if [ -z "$logs" ]; then
	ok "a passing run reports no retained log directory"
else
	bad "a passing run reports retained logs: $logs"
fi
if find "$tmp" -maxdepth 1 -type d -name 'agent-test-shards.*' -print | grep -q .; then
	bad "a passing run removes no shard run directory"
else
	ok "a passing run removes its entire shard run directory"
fi

new_case
out="$case_root/failure.out"
if FAKE_MODE=fail run_case >"$out" 2>&1; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "a failed shard run exits nonzero"; else bad "a failed shard run exits zero"; fi
logs="$(full_logs_path "$out")"
if [ -n "$logs" ] && [ -d "$logs" ]; then
	ok "a failed shard run retains its absolute log directory"
	[ -f "$logs/shard0.log" ] && ok "a failed run retains shard zero evidence" || bad "a failed run loses shard zero evidence"
	[ -f "$logs/shard1.log" ] && ok "a failed run retains shard one evidence" || bad "a failed run loses shard one evidence"
	assert_absent "$logs/.heartbeat" "a failed run removes its heartbeat marker"
else
	bad "a failed shard run does not retain the named log directory"
fi

new_case
out="$case_root/interrupt.out"
runner_pid_file="$state/runner.pid"
ready_fifo="$state/ready"
stop_fifo="$state/stopped"
mkfifo "$ready_fifo" "$stop_fifo"
exec 9<>"$ready_fifo"
exec 8<>"$stop_fifo"

# Wrap the runner so its own death is also a FIFO event: the readiness read below
# always receives *something* (a ready:<shard> per held child, or a terminal
# event), so it never has to guess "still starting" from "never coming".
# Unsolicited death (before anyone signaled it) is reported on ready_fifo, the
# same channel readiness comes in on, since the startup loop must recognize it
# without a separate wait; a TERM-forwarded, confirmed stop is reported on the
# dedicated stop_fifo instead, so the escalation ladder's confirmation read
# below can never be satisfied by a stray leftover readiness event.
(
	child_pid=""
	spawn_interrupted=0
	# Until child_pid is in hand there is nothing to forward a signal to, so a
	# TERM arriving in that window is recorded rather than run through a handler
	# that would report a stop the wrapper never actually performed (the same
	# spawn-window cover make-selftest-selftest.sh's wrapper carries).
	trap 'spawn_interrupted=1' TERM
	FAKE_MODE=hold FAKE_READY_FIFO="$ready_fifo" run_case_async >"$out" 2>&1 &
	child_pid="$!"
	printf '%s\n' "$child_pid" >"$runner_pid_file"
	# A trapped signal makes bash's own `wait` return early without the real exit
	# status, so the forward-then-reap-for-real must happen inside the handler
	# itself (which then exits directly) rather than after a bare `trap ... TERM`.
	forward_term() {
		kill -TERM "$child_pid" 2>/dev/null || :
		if wait "$child_pid" 2>/dev/null; then rc=0; else rc=$?; fi
		printf 'runner-stopped:%s\n' "$rc" >"$stop_fifo"
		exit "$rc"
	}
	trap forward_term TERM
	[ "$spawn_interrupted" -eq 1 ] && forward_term
	if wait "$child_pid"; then rc=0; else rc=$?; fi
	printf 'runner-exited:%s\n' "$rc" >"$ready_fifo"
	exit "$rc"
) &
runner_pid="$!"

alpha_ready=0
beta_ready=0
fixture_state=ok
died_event=""
while [ "$((alpha_ready + beta_ready))" -lt 2 ]; do
	# 10s covers go test -c + -test.list under real contention (the diagnosed flake:
	# a fixed 3s poll budget for this same event, timing out under load and taking a
	# destructive SIGKILL fallback). This ceiling only distinguishes "hung" from
	# "still starting" -- readiness and the runner's own terminal event still drive
	# progress and can satisfy the loop well before it.
	if ! IFS= read -r -t 10 -u 9 event; then
		fixture_state=wedged
		break
	fi
	case "$event" in
		ready:alpha) alpha_ready=1 ;;
		ready:beta) beta_ready=1 ;;
		runner-exited:*)
			fixture_state=died
			died_event="$event"
			break
			;;
	esac
done

tracked="$(descendants "$runner_pid")"
case "$fixture_state" in
	ok)
		if kill -TERM "$runner_pid" 2>/dev/null; then ok "an active shard run accepts SIGTERM"; else bad "an active shard run cannot be signaled"; fi
		;;
	died)
		bad "the interruption fixture starts both held shard children (runner died before shards held: $died_event)"
		;;
	wedged)
		bad "the interruption fixture starts both held shard children (timed out waiting for shard readiness)"
		# TERM-first with reaping -- the runner has its own SIGTERM cleanup (agent-test-shards.sh's
		# own trap), so give it a generous bounded chance to confirm through the dedicated stop_fifo
		# before escalating. KILL is the last tier, not the only one, and it targets the recorded
		# real runner pid, not the wrapper -- a wrapper stuck in its own `wait` would otherwise be
		# reaped without ever touching the process this fixture exists to check for leaks.
		kill -TERM "$runner_pid" 2>/dev/null || :
		if ! IFS= read -r -t 5 -u 8 stop_event; then
			real_runner_pid="$(cat "$runner_pid_file" 2>/dev/null || :)"
			if [ -n "$real_runner_pid" ]; then
				kill -KILL "$real_runner_pid" 2>/dev/null || :
			else
				kill -KILL "$runner_pid" 2>/dev/null || :
			fi
		fi
		;;
esac
if wait "$runner_pid"; then rc=0; else rc=$?; fi
exec 9>&-
exec 8>&-
assert_eq "$rc" "143" "an interrupted shard run exits with signal status"
assert_has "$out" "interrupted by SIGTERM" "an interrupted shard run explains its exit"
logs="$(full_logs_path "$out")"
if [ -n "$logs" ] && [ -d "$logs" ]; then
	ok "an interrupted run retains its absolute log directory"
	assert_absent "$logs/.heartbeat" "an interrupted run removes its heartbeat marker"
else
	bad "an interrupted run removes or fails to name its log directory"
fi
assert_pids_gone "$tracked" "an interrupted shard run"

echo "----"
echo "agent-test-shards-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
