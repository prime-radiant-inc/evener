#!/usr/bin/env bash
# run-module-tests-selftest.sh - offline behavioral tests for module testing.
#
# The real gate is minutes of `go test` and a full frontend build, so nothing
# here runs either: each case builds a throwaway module tree and puts a stub
# `go` and `make` on PATH, which lets the assertions be about what the runner
# reported rather than about what the suites found.
#
# The contract being pinned: every stream the runner starts owes the reader
# exactly one verdict line, and a FAIL owes the reader that stream's output.
# MODULES=web broke both at once (kata mjzx) - the frontend stream and a Go
# module of the same name each claimed the name "web", so the run printed both
# FAIL and PASS for it, the two streams wrote one log file, and the failure
# dump came out empty.
set -uo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
runner="$script_dir/run-module-tests.sh"
work="$(mktemp -d -t serf-module-tests-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

checks=0
fails=0
ok() { checks=$((checks + 1)); printf 'ok   - %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL - %s\n' "$1"; }
assert_eq() {
	if [ "$1" = "$2" ]; then ok "$3"; else bad "$3 (want '$2', got '$1')"; fi
}
assert_has() {
	if grep -qF -- "$2" "$1"; then
		ok "$3"
	else
		bad "$3 (missing '$2')"
		sed 's/^/    | /' "$1"
	fi
}
assert_not_has() {
	if grep -qF -- "$2" "$1"; then
		bad "$3 (unexpected '$2')"
		sed 's/^/    | /' "$1"
	else
		ok "$3"
	fi
}

assert_has_word() {
	case " $1 " in
		*" $2 "*) ok "$3" ;;
		*) bad "$3 (missing word '$2' in '$1')" ;;
	esac
}

assert_not_has_word() {
	case " $1 " in
		*" $2 "*) bad "$3 (unexpected word '$2' in '$1')" ;;
		*) ok "$3" ;;
	esac
}

arguments_for() {
	awk -F '\t' -v stream="$1" '$1 == stream { print $2; exit }' "$state/calls"
}

# The verdict block is everything the runner prints before the failure dump.
# Reading it separately keeps replayed suite output - which carries PASS/FAIL
# lines of its own - from being counted as a verdict.
verdicts() {
	awk '/^=== failing module output ===$/{exit} /^(PASS|FAIL)  /{print $2}' "$1"
}

assert_one_verdict_per_name() {
	dupes="$(verdicts "$1" | sort | uniq -d | tr '\n' ' ' | sed 's/ *$//')"
	if [ -z "$dupes" ]; then
		ok "$2"
	else
		bad "$2 (reported twice: $dupes)"
		sed 's/^/    | /' "$1"
	fi
}

# A FAIL whose output the reader cannot see is a verdict with no evidence.
dump_section() {
	awk '/^=== failing module output ===$/{on = 1; next} /^full logs: /{on = 0} on' "$1"
}

assert_dump_nonempty() {
	if [ -n "$(dump_section "$1" | tr -d '[:space:]')" ]; then
		ok "$2"
	else
		bad "$2 (the failing-output section is empty)"
		sed 's/^/    | /' "$1"
	fi
}

# macOS mktemp resolves -t against the per-user temp path and ignores TMPDIR, so
# an unfaked mktemp puts every temporary directory outside the case: assertions
# about those paths can then never fail, and the run litters the real TMPDIR.
# Redirect only the -t forms and pass everything else through.
write_fake_mktemp() {
	cat >"$1/mktemp" <<'FAKE_TMPDIR_MKTEMP'
#!/usr/bin/env bash
set -u
case "${1:-}" in
	-d) [ "${2:-}" = "-t" ] && exec /usr/bin/mktemp -d "$TMPDIR/$3.XXXXXX" ;;
	-t) exec /usr/bin/mktemp "$TMPDIR/$2.XXXXXX" ;;
esac
exec /usr/bin/mktemp "$@"
FAKE_TMPDIR_MKTEMP
	chmod +x "$1/mktemp"
}

new_case() {
	case_dir="$(mktemp -d "$work/case.XXXXXX")"
	repo="$case_dir/repo"
	state="$case_dir/state"
	bin="$case_dir/bin"
	mkdir -p "$repo/scripts" "$repo/cmd/serf-hub/frontend" "$state" "$bin"
	write_fake_mktemp "$bin"
	for module in agent llm auth envvars invariant identifier; do
		mkdir -p "$repo/$module"
	done
	cat >"$bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -u
module="$(basename "$PWD")"
[ "$PWD" = "$FAKE_REPO" ] && module=.
case "${1:-}" in
	env)
		case "${2:-}" in
			GOCACHE) printf '%s\n' "${GOCACHE:-}" ;;
			GOMODCACHE) printf '%s\n' "${GOMODCACHE:-}" ;;
			*) exit 1 ;;
		esac
		exit 0
		;;
	list)
		if [ "${FAKE_LIST_FAIL:-0}" -ne 0 ]; then
			printf 'go: cannot load module\n' >&2
			exit 1
		fi
		if [ "${FAKE_LIST_HOLD:-0}" -ne 0 ]; then
			if [ -n "${FAKE_READY_FIFO:-}" ] && [ -p "$FAKE_READY_FIFO" ]; then
				printf 'go-list-started\n' >"$FAKE_READY_FIFO"
			fi
			: >"$FAKE_STATE/root-list.started"
			sleep 1000 &
			printf '%s\n' "$!" >"$FAKE_STATE/root-list-child.pid"
			wait "$!"
			exit $?
		fi
		: >"$FAKE_STATE/root.listed"
		printf 'primeradiant.com/serf/%s\n' "$module"
		exit 0
		;;
esac
printf '%s\t%s\n' "$module" "$*" >>"$FAKE_STATE/calls"
printf 'go-stdout:%s\n' "$module"
printf 'go-stderr:%s\n' "$module" >&2
if [ "$module" = "agent" ] && [ "${FAKE_HOLD_STREAMS:-0}" -ne 0 ]; then
	printf '%s\n' "$$" >"$FAKE_STATE/agent.pid"
	: >"$FAKE_STATE/agent.started"
	exec sleep 1000
fi
if [ "$module" = "agent" ] && [ "${FAKE_AGENT_AWAITS_SELFTEST:-0}" -ne 0 ]; then
	: >"$FAKE_STATE/agent.started"
	attempt=0
	while [ ! -f "$FAKE_STATE/selftest.started" ]; do
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 200 ]; then
			printf 'agent started without the selftest stream\n' >&2
			exit 3
		fi
		sleep 0.01
	done
fi
# A build failure carries none of the markers `go test` prints for a failing
# test, which is the shape a marker-matching failure dump drops on the floor.
case " ${FAKE_BUILD_FAIL:-} " in
	*" $module "*) printf 'boom:%s: undefined: Thing\n' "$module" >&2; exit 2 ;;
esac
case " ${FAKE_TEST_FAIL:-} " in
	*" $module "*)
		printf -- '--- FAIL: TestThing (0.00s)\n'
		printf 'FAIL\tprimeradiant.com/serf/%s\t0.01s\n' "$module"
		exit 1
		;;
esac
[ "$module" = "." ] && : >"$FAKE_STATE/root.finished"
exit 0
FAKE_GO
	chmod +x "$bin/go"
	cat >"$bin/make" <<'FAKE_MAKE'
#!/usr/bin/env bash
set -u
stream=web
[ "${1:-}" = "selftest" ] && stream=selftest
printf '%s\t%s\n' "$stream" "$*" >>"$FAKE_STATE/calls"
if [ "$stream" = "web" ] && [ "${FAKE_HOLD_STREAMS:-0}" -ne 0 ]; then
	printf 'web-stdout: interrupted-run marker\n'
	printf 'web-stderr: interrupted-run marker\n' >&2
	printf '%s\n' "$$" >"$FAKE_STATE/web.pid"
	: >"$FAKE_STATE/web.started"
	exec sleep 1000
fi
if [ "$stream" = "selftest" ]; then
	if [ "${FAKE_SELFTEST_REQUIRES_ROOT:-0}" -ne 0 ] && [ ! -f "$FAKE_STATE/root.finished" ]; then
		printf 'selftest started before root finished\n' >&2
		exit 4
	fi
	: >"$FAKE_STATE/selftest.started"
	if [ "${FAKE_SELFTEST_AWAITS_AGENT:-0}" -ne 0 ]; then
		attempt=0
		while [ ! -f "$FAKE_STATE/agent.started" ]; do
			attempt=$((attempt + 1))
			if [ "$attempt" -ge 200 ]; then
				printf 'selftest started without the wave-two agent\n' >&2
				exit 5
			fi
			sleep 0.01
		done
	fi
	printf 'selftest-stdout: tooling contracts passed\n'
	if [ "${FAKE_SELFTEST_FAIL:-0}" -ne 0 ]; then
		printf 'selftest-stderr: tooling contract failed\n' >&2
		exit 1
	fi
	exit 0
fi
printf 'web-stdout: 5308 tests passed\n'
if [ "${FAKE_WEB_FAIL:-0}" -ne 0 ]; then
	printf 'web-stderr: 1 test failed\n' >&2
	exit 1
fi
exit 0
FAKE_MAKE
	chmod +x "$bin/make"
}

# MAKE is set explicitly rather than left to the runner's `${MAKE:-make}`
# default: `make selftest` exports MAKE into every recipe, so an inherited one
# would send the frontend stream to the real make in a fixture with no Makefile
# and this suite would pass standalone while failing under the gate that runs it.
run_tests() {
	modules="$1"
	output="$2"
	shift 2
	(
		cd "$repo" || exit 1
		env -u WAVE1 -u WAVE2 TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
			GOCACHE="$case_dir/gocache" GOMODCACHE="$case_dir/gomodcache" \
			MODULES="$modules" AGENT_SHARDS=0 SELFTEST=0 WEB=1 WEB_DIR="$repo/cmd/serf-hub/frontend" MAKE="$bin/make" "$@" "$runner" -short -count=1
	) >"$output" 2>&1
}

run_tests_async() {
	modules="$1"
	output="$2"
	shift 2
	ready_fifo=""
	for argument in "$@"; do
		case "$argument" in
			FAKE_READY_FIFO=*) ready_fifo="${argument#FAKE_READY_FIFO=}" ;;
		esac
	done
	(
		cd "$repo" || exit 1
		if [ -n "$ready_fifo" ]; then
			printf 'runner-started\n' >"$ready_fifo"
		env -u WAVE1 -u WAVE2 TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
			GOCACHE="$case_dir/gocache" GOMODCACHE="$case_dir/gomodcache" \
			MODULES="$modules" AGENT_SHARDS=0 SELFTEST=0 WEB=1 WEB_DIR="$repo/cmd/serf-hub/frontend" MAKE="$bin/make" "$@" "$runner" -short -count=1
			runner_status="$?"
			printf 'runner-exited:%s\n' "$runner_status" >"$ready_fifo"
			exit "$runner_status"
		fi
		exec env -u WAVE1 -u WAVE2 TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
			GOCACHE="$case_dir/gocache" GOMODCACHE="$case_dir/gomodcache" \
			MODULES="$modules" AGENT_SHARDS=0 SELFTEST=0 WEB=1 WEB_DIR="$repo/cmd/serf-hub/frontend" MAKE="$bin/make" "$@" "$runner" -short -count=1
	) >"$output" 2>&1 &
	runner_pid="$!"
}

run_tests_default_modules() {
	output="$1"
	shift
	(
		cd "$repo" || exit 1
		env -u MODULES -u WAVE1 -u WAVE2 TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
			GOCACHE="$case_dir/gocache" GOMODCACHE="$case_dir/gomodcache" \
			AGENT_SHARDS=0 SELFTEST=0 WEB=1 WEB_DIR="$repo/cmd/serf-hub/frontend" MAKE="$bin/make" "$@" "$runner" -short -count=1
	) >"$output" 2>&1
}

started_streams() {
	cut -f1 "$state/calls" 2>/dev/null | sort | tr '\n' ' ' | sed 's/ *$//'
}

runner_logdirs() {
	find "$case_dir" -maxdepth 1 -type d -name 'serf-module-tests.*' -print
}

wait_for_file() {
	local path="$1" attempts="${2:-200}"
	while [ "$attempts" -gt 0 ]; do
		[ -f "$path" ] && return 0
		sleep 0.01
		attempts=$((attempts - 1))
	done
	return 1
}

wait_for_process_exit() {
	local pid="$1" attempts=200
	while [ "$attempts" -gt 0 ]; do
		kill -0 "$pid" 2>/dev/null || return 0
		sleep 0.01
		attempts=$((attempts - 1))
	done
	return 1
}

start_fixture_watchdog() {
	local target_pid="$1" delay="$2" marker="$3" event_fifo="$4" event="$5"
	(
		watchdog_sleep_pid=""
		stop_watchdog_sleep() {
			if [ -n "$watchdog_sleep_pid" ]; then
				kill -TERM "$watchdog_sleep_pid" 2>/dev/null || :
				wait "$watchdog_sleep_pid" 2>/dev/null || :
				watchdog_sleep_pid=""
			fi
		}
		watchdog_interrupted() {
			stop_watchdog_sleep
			exit 143
		}
		trap watchdog_interrupted HUP INT TERM
		trap stop_watchdog_sleep EXIT
		sleep "$delay" &
		watchdog_sleep_pid="$!"
		wait "$watchdog_sleep_pid" || exit 0
		watchdog_sleep_pid=""
		if kill -0 "$target_pid" 2>/dev/null; then
			[ -n "$marker" ] && : >"$marker"
			kill -TERM "$target_pid" 2>/dev/null || :
		fi
		[ -n "$event_fifo" ] && printf '%s\n' "$event" >"$event_fifo"
	) &
	fixture_watchdog_pid="$!"
}

stop_fixture_watchdog() {
	local pid="$1"
	if kill -0 "$pid" 2>/dev/null; then
		kill -TERM "$pid" 2>/dev/null || :
	fi
	wait "$pid" 2>/dev/null || :
}

full_logs_path() {
	awk '/^full logs: / { print substr($0, 12); exit }' "$1"
}

new_case
out="$case_dir/inherited-selftest-disabled.out"
if SELFTEST=1 run_tests "agent" "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "an inherited SELFTEST value does not fail an ordinary fixture case"
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" "agent web" "an inherited SELFTEST value does not add a tooling verdict"
assert_eq "$(started_streams)" "agent web" "an inherited SELFTEST value does not start a tooling stream"

new_case
out="$case_dir/all-pass.out"
if run_tests ". agent llm" "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "all-passing run exits zero"
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" ". agent llm web" "every stream reports once, in wave order"
assert_one_verdict_per_name "$out" "all-passing run reports no name twice"
assert_eq "$(started_streams)" ". agent llm web" "every requested stream ran"
if [ -f "$state/root.listed" ]; then ok "a root package-discovery success reaches the root test invocation"; else bad "a root package-discovery success never listed root packages"; fi
assert_not_has "$out" "=== failing module output ===" "all-passing run prints no failure section"
assert_not_has "$out" "go-stdout:" "passing suite chatter stays hidden"
assert_eq "$(runner_logdirs)" "" "a successful run removes its temporary logs"

new_case
out="$case_dir/ambient-overrides.out"
if MODULES="nosuch" WAVE1= WAVE2= run_tests ". agent" "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "ambient module and wave overrides do not affect a fixture case"
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" ". agent web" "a fixture case retains its requested wave schedule"

new_case
out="$case_dir/ambient-web-overrides.out"
if WEB=0 WEB_DIR="$work/not-the-fixture" run_tests "agent" "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "ambient web controls do not affect a fixture case"
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" "agent web" "a fixture case retains its frontend stream"
assert_eq "$(started_streams)" "agent web" "ambient web controls do not redirect the frontend stream"

new_case
out="$case_dir/default-modules.out"
if run_tests_default_modules "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "a default-module run exits zero"
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" ". agent llm auth envvars invariant identifier web" "a default-module run covers every non-fuzz Makefile module"

new_case
out="$case_dir/root-full.out"
if run_tests ". agent" "$out" ROOT_FULL=1; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "full-root run exits zero"
root_args="$(arguments_for .)"
agent_args="$(arguments_for agent)"
assert_not_has_word "$root_args" "-short" "full-root mode removes exact -short from root"
assert_has_word "$root_args" "-run" "full-root mode retains the non-fuzz test selection"
assert_has_word "$root_args" "-count=1" "full-root mode preserves root's other flags"
assert_has_word "$agent_args" "-short" "full-root mode keeps -short on non-root modules"
assert_has_word "$agent_args" "-count=1" "full-root mode preserves non-root flags"

new_case
out="$case_dir/selftest-overlap.out"
if FAKE_SELFTEST_REQUIRES_ROOT=1 FAKE_AGENT_AWAITS_SELFTEST=1 \
	FAKE_SELFTEST_AWAITS_AGENT=1 run_tests ". agent" "$out" SELFTEST=1; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "selftest waits for root and overlaps wave two"
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" ". agent selftest web" "selftest reports once after wave two"
assert_one_verdict_per_name "$out" "selftest overlap reports no name twice"
assert_eq "$(started_streams)" ". agent selftest web" "selftest overlap starts every requested stream"

# kata mjzx: the frontend stream already reports and logs under the name "web",
# so a MODULES entry of the same name gave one label two owners - two verdict
# lines, two writers on one log, and a failure dump with nothing in it.
new_case
out="$case_dir/web-name-conflict.out"
if run_tests "web" "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "2" "a MODULES entry colliding with the frontend stream is refused"
assert_eq "$(verdicts "$out" | wc -l | tr -d ' ')" "0" "the refused run reports no verdict at all"
assert_one_verdict_per_name "$out" "the refused run reports no name twice"
assert_has "$out" "make test-web" "the refusal names the frontend's own entry point"
assert_has "$out" "WEB=0" "the refusal names the other way out"
assert_not_has "$out" "=== failing module output ===" "the refused run prints no empty failure section"
assert_eq "$(started_streams)" "" "the refused run starts no stream"

new_case
out="$case_dir/selftest-name-conflict.out"
if run_tests "selftest" "$out" SELFTEST=1; then rc=0; else rc=$?; fi
assert_eq "$rc" "2" "a module colliding with the selftest stream is refused"
assert_eq "$(verdicts "$out" | wc -l | tr -d ' ')" "0" "the refused selftest collision reports no verdict"
assert_has "$out" "make selftest" "the refusal names the selftest entry point"
assert_has "$out" "SELFTEST=0" "the refusal names the explicit opt-out"
assert_eq "$(started_streams)" "" "the refused selftest collision starts no stream"

# WAVE1/WAVE2 are a documented override that bypasses MODULES entirely, so the
# same collision arrives by a second route and has to be refused by the same
# rule: what matters is the name actually being scheduled.
new_case
out="$case_dir/web-wave-conflict.out"
if run_tests "agent" "$out" WAVE2=web; then rc=0; else rc=$?; fi
assert_eq "$rc" "2" "a WAVE entry colliding with the frontend stream is refused"
assert_eq "$(verdicts "$out" | wc -l | tr -d ' ')" "0" "the refused wave reports no verdict at all"
assert_eq "$(started_streams)" "" "the refused wave starts no stream"

new_case
out="$case_dir/mixed-failures.out"
if FAKE_BUILD_FAIL="llm" FAKE_TEST_FAIL="auth" run_tests "agent llm auth" "$out"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "a failing module exits nonzero"; else bad "a failing module unexpectedly exits zero"; fi
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" "agent llm auth web" "mixed run reports each stream once"
assert_one_verdict_per_name "$out" "mixed run reports no name twice"
assert_dump_nonempty "$out" "a FAIL dumps output"
assert_has "$out" "----- llm -----" "the build-failed module is dumped"
assert_has "$out" "boom:llm: undefined: Thing" "a failure with no go-test marker is dumped in full"
assert_has "$out" "----- auth -----" "the test-failed module is dumped"
assert_has "$out" "--- FAIL: TestThing" "a failure with a go-test marker is still dumped"
assert_not_has "$out" "----- agent -----" "the passing module is not dumped"
assert_not_has "$out" "go-stdout:agent" "passing module chatter stays hidden"
if [ -d "$(full_logs_path "$out")" ]; then ok "a failing run retains the logs it names"; else bad "a failing run removes the logs it names"; fi
llm_line="$(grep -nF -- '----- llm -----' "$out" | cut -d: -f1)"
auth_line="$(grep -nF -- '----- auth -----' "$out" | cut -d: -f1)"
if [ -n "$llm_line" ] && [ -n "$auth_line" ] && [ "$llm_line" -lt "$auth_line" ]; then
	ok "failure dumps follow MODULES order"
else
	bad "failure dumps are out of MODULES order"
fi

new_case
out="$case_dir/root-failure-still-covers-later-streams.out"
if FAKE_TEST_FAIL="." run_tests ". agent" "$out" SELFTEST=1; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "root failure keeps the aggregate red"; else bad "root failure unexpectedly exits zero"; fi
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" ". agent selftest web" "root failure still runs every later stream"
assert_eq "$(started_streams)" ". agent selftest web" "root failure does not skip later coverage"
assert_has "$out" "----- . -----" "the root failure is dumped"
assert_not_has "$out" "----- agent -----" "the later passing module is not dumped"
assert_not_has "$out" "----- selftest -----" "the later passing selftest is not dumped"

# A module whose directory is gone fails in `cd`, long before `go test` prints
# any marker at all. The verdict is worthless to the reader without the reason.
new_case
out="$case_dir/missing-module.out"
if run_tests "agent nosuch" "$out"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "a missing module directory exits nonzero"; else bad "a missing module directory unexpectedly exits zero"; fi
assert_one_verdict_per_name "$out" "missing-module run reports no name twice"
assert_dump_nonempty "$out" "a missing module directory dumps its reason"
assert_has "$out" "----- nosuch -----" "the missing module is named in the failure dump"
assert_has "$out" "No such file or directory" "the missing module's own diagnostic reaches the reader"

new_case
out="$case_dir/root-package-discovery-failure.out"
if FAKE_LIST_FAIL=1 run_tests "." "$out"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "a root package-discovery failure exits nonzero"; else bad "a root package-discovery failure unexpectedly exits zero"; fi
assert_has "$out" "FAIL  ." "a root package-discovery failure contributes a root verdict"
assert_has "$out" "----- . -----" "a root package-discovery failure is included in aggregate output"
assert_has "$out" "go: cannot load module" "a root package-discovery diagnostic reaches the reader"
assert_not_has "$out" "packages[@]: unbound variable" "a root package-discovery failure does not fall into an empty-array error"

# Root package discovery happens before the root test command. A wedged cache
# can therefore freeze the entire gate while the concurrent frontend stream
# remains live. The watchdog makes the pre-timeout behavior deterministic: it
# records that the runner needed outside intervention instead of leaving this
# selftest itself stuck forever.
new_case
out="$case_dir/root-package-discovery-timeout.out"
root_worktree="$(cd "$repo" && pwd -P)"
ready_fifo="$state/root-list.ready"
mkfifo "$ready_fifo"
(
	exec 8>"$ready_fifo"
	exec sleep 20
) &
ready_keeper_pid="$!"
exec 9<"$ready_fifo"
run_tests_async "." "$out" FAKE_LIST_HOLD=1 SERF_ROOT_PACKAGE_LIST_TIMEOUT=5 FAKE_READY_FIFO="$ready_fifo"
start_fixture_watchdog "$runner_pid" 10 "$state/root-list.watchdog" "$ready_fifo" startup-watchdog
startup_watchdog_pid="$fixture_watchdog_pid"
ready_signal=""
startup_ready=0
if IFS= read -r -u 9 ready_signal && [ "$ready_signal" = "runner-started" ]; then
	if IFS= read -r -u 9 ready_signal && [ "$ready_signal" = "go-list-started" ]; then
		startup_ready=1
	fi
fi
if [ "$startup_ready" -eq 1 ]; then
	stop_fixture_watchdog "$startup_watchdog_pid"
	exec 9<&-
	kill "$ready_keeper_pid" 2>/dev/null || :
	wait "$ready_keeper_pid" 2>/dev/null || :
	start_fixture_watchdog "$runner_pid" 8 "$state/root-list.watchdog" "" ""
	watchdog_pid="$fixture_watchdog_pid"
	if wait "$runner_pid"; then rc=0; else rc=$?; fi
	stop_fixture_watchdog "$watchdog_pid"
	assert_eq "$rc" "1" "a root package-discovery timeout exits nonzero"
	if [ -f "$state/root-list.watchdog" ]; then
		bad "root package discovery required the watchdog despite its configured timeout"
	else
		ok "root package discovery finishes before the watchdog"
	fi
	assert_has "$out" "go list ./... timed out" "a root package-discovery timeout names the blocked command"
	assert_has "$out" "effective GOCACHE: $case_dir/gocache" "a root package-discovery timeout reports the effective GOCACHE"
	assert_has "$out" "effective GOMODCACHE: $case_dir/gomodcache" "a root package-discovery timeout reports the effective GOMODCACHE"
	assert_has "$out" "worktree/module: $root_worktree (.)" "a root package-discovery timeout reports its worktree and module"
	assert_has "$out" "retained package-list log:" "a root package-discovery timeout names the retained package-list log"
	timeout_logs="$(full_logs_path "$out")"
	timeout_package_list="$(awk '/^run-module-tests\.sh: retained package-list log: / { sub(/^run-module-tests\.sh: retained package-list log: /, ""); print; exit }' "$out")"
	if [ -n "$timeout_logs" ] && [ "$timeout_package_list" = "$timeout_logs/root.packages" ] && [ -f "$timeout_package_list" ]; then
		ok "a root package-discovery timeout retains the reported root package-list log"
	else
		bad "a root package-discovery timeout names a missing or wrong package-list log (logs '$timeout_logs', package list '$timeout_package_list')"
	fi
	assert_has "$out" "go clean -cache -modcache" "a root package-discovery timeout gives a cache repair command"
	assert_has "$out" "scripts/run-module-tests.sh -short -count=1" "a root package-discovery timeout gives an exact retry command"
	child_pid="$(cat "$state/root-list-child.pid" 2>/dev/null || :)"
	if [ -n "$child_pid" ] && wait_for_process_exit "$child_pid"; then
		ok "a root package-discovery timeout reaps its descendant"
	else
		bad "a root package-discovery timeout leaves its descendant alive"
		[ -n "$child_pid" ] && kill -KILL "$child_pid" 2>/dev/null || :
	fi
else
	bad "the root package-discovery timeout fixture did not reach held go list (last event '$ready_signal')"
	if kill -0 "$runner_pid" 2>/dev/null; then kill -TERM "$runner_pid" 2>/dev/null || :; fi
	wait "$runner_pid" 2>/dev/null || :
	stop_fixture_watchdog "$startup_watchdog_pid"
	exec 9<&-
	kill "$ready_keeper_pid" 2>/dev/null || :
	wait "$ready_keeper_pid" 2>/dev/null || :
fi

new_case
out="$case_dir/web-failure.out"
if FAKE_WEB_FAIL=1 run_tests "agent" "$out"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "a failing frontend stream exits nonzero"; else bad "a failing frontend stream unexpectedly exits zero"; fi
assert_has "$out" "FAIL  web" "the frontend stream reports its own verdict"
assert_one_verdict_per_name "$out" "frontend failure reports no name twice"
assert_dump_nonempty "$out" "a failing frontend stream dumps output"
assert_has "$out" "----- web -----" "the frontend log is dumped under its own name"
assert_has "$out" "web-stderr: 1 test failed" "the frontend's failure output reaches the reader"
assert_not_has "$out" "----- agent -----" "the passing module is not dumped alongside the frontend"

new_case
out="$case_dir/selftest-failure.out"
if FAKE_SELFTEST_FAIL=1 run_tests "agent" "$out" SELFTEST=1; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "a failing selftest stream exits nonzero"; else bad "a failing selftest stream unexpectedly exits zero"; fi
assert_has "$out" "FAIL  selftest" "the selftest stream reports its own verdict"
assert_one_verdict_per_name "$out" "selftest failure reports no name twice"
assert_dump_nonempty "$out" "a failing selftest stream dumps output"
assert_has "$out" "----- selftest -----" "the selftest log is dumped under its own name"
assert_has "$out" "selftest-stderr: tooling contract failed" "the selftest diagnostic reaches the reader"
assert_not_has "$out" "----- agent -----" "the passing module is not dumped with selftest"

new_case
out="$case_dir/interrupted-run.out"
FAKE_HOLD_STREAMS=1 run_tests_async "agent" "$out"
if wait_for_file "$state/agent.started" && wait_for_file "$state/web.started"; then
	if kill -TERM "$runner_pid"; then
		ok "an interrupted run can be signaled while streams are active"
	else
		bad "an interrupted run could not be signaled"
	fi
else
	bad "the interruption fixture started both child streams"
	kill -KILL "$runner_pid" 2>/dev/null || :
fi
if wait "$runner_pid"; then rc=0; else rc=$?; fi
assert_eq "$rc" "143" "an interrupted run exits with the signal status"
assert_has "$out" "interrupted by SIGTERM" "an interrupted run explains its exit"
logs="$(full_logs_path "$out")"
	if [ -n "$logs" ] && [ -d "$logs" ]; then
		if [ -f "$logs/agent.log" ] && [ -f "$logs/web.log" ]; then
			ok "an interrupted run preserves the diagnostic logs it names"
			assert_has "$logs/agent.log" "go-stdout:agent" "the interrupted agent log keeps stdout diagnostics"
			assert_has "$logs/agent.log" "go-stderr:agent" "the interrupted agent log keeps stderr diagnostics"
			assert_has "$logs/web.log" "web-stdout: interrupted-run marker" "the interrupted web log keeps stdout diagnostics"
			assert_has "$logs/web.log" "web-stderr: interrupted-run marker" "the interrupted web log keeps stderr diagnostics"
		else
			bad "an interrupted run preserves the log directory but loses a stream log"
	fi
else
	bad "an interrupted run removes or fails to name its diagnostic logs"
fi
for stream in agent web; do
	pid="$(cat "$state/$stream.pid" 2>/dev/null || :)"
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		bad "an interrupted run leaves the $stream child alive"
		kill -KILL "$pid" 2>/dev/null || :
	else
		ok "an interrupted run reaps the $stream child"
	fi
done

echo "----"
echo "run-module-tests-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
