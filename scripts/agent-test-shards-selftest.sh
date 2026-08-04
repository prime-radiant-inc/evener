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

wait_for_file() {
	local path="$1" attempts=300
	while [ "$attempts" -gt 0 ]; do
		[ -f "$path" ] && return 0
		sleep 0.01
		attempts=$((attempts - 1))
	done
	return 1
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
FAKE_MODE=hold run_case_async >"$out" 2>&1 &
runner_pid="$!"
if wait_for_file "$state/alpha.child.pid" && wait_for_file "$state/beta.child.pid"; then
	tracked="$(descendants "$runner_pid")"
	if kill -TERM "$runner_pid" 2>/dev/null; then ok "an active shard run accepts SIGTERM"; else bad "an active shard run cannot be signaled"; fi
else
	bad "the interruption fixture starts both held shard children"
	tracked="$(descendants "$runner_pid")"
	kill -KILL "$runner_pid" 2>/dev/null || :
fi
if wait "$runner_pid"; then rc=0; else rc=$?; fi
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
