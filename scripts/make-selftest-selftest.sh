#!/usr/bin/env bash
# make-selftest-selftest.sh - exercise the real Makefile selftest wave's
# normal and interrupted lifecycle with controllable worker suites.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd -P)"
real_make="$(command -v make)"
work="$(mktemp -d -t serf-make-selftest-selftest.XXXXXX)"
makefile="$repo_root/Makefile"
make_args=(-f "$makefile" -C "$work" --no-print-directory "SELFTEST_SCRIPTS=probe-one probe-two" selftest)
checks=0
fails=0
make_pid=""
unrelated_pid=""

kill_if_alive() {
	local pid="${1:-}"
	[ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && kill -KILL "$pid" 2>/dev/null || :
}

cleanup() {
	kill_if_alive "$make_pid"
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
	mkdir -p "$case_state" "$case_tmp" "$case_bin" "$work/scripts"
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
printf '%s\n' "$$" >"$PROBE_STATE/$name.pid"
: >"$PROBE_STATE/$name.started"
case "${PROBE_MODE:-pass}" in
	hold) exec sleep 30 ;;
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
	PROBE_STATE="$case_state" PROBE_MODE="$mode" PROBE_FAIL="$fail_name" TMPDIR="$case_tmp" PATH="$case_bin:/usr/bin:/bin" \
		"$real_make" "${make_args[@]}" >"$output" 2>&1
}

wait_for_file() {
	local path="$1" attempts=1000
	# The full selftest wave starts this fixture alongside many other workers.
	# Give both probe processes time to start before testing interruption cleanup.
	while [ "$attempts" -gt 0 ]; do
		[ -f "$path" ] && return 0
		sleep 0.01
		attempts=$((attempts - 1))
	done
	return 1
}

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
interrupt_out="$case_dir/interrupted.out"
PROBE_STATE="$case_state" PROBE_MODE=hold TMPDIR="$case_tmp" PATH="$case_bin:/usr/bin:/bin" \
	"$real_make" "${make_args[@]}" >"$interrupt_out" 2>&1 &
make_pid="$!"
if wait_for_file "$case_state/probe-one.started" && wait_for_file "$case_state/probe-two.started"; then
	ok "the interruption fixture starts both workers"
else
	bad "the interruption fixture did not start both workers"
fi
sleep 30 &
unrelated_pid="$!"
kill -TERM "$make_pid" 2>/dev/null || :
if wait "$make_pid"; then rc=0; else rc=$?; fi
make_pid=""
[ "$rc" -ne 0 ] && ok "an interrupted wave exits nonzero" || bad "an interrupted wave exits zero"
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

echo "----"
echo "make-selftest-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
