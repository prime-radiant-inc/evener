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
	# The runner's disk guard is the first thing it does and it shells out to a
	# sibling script; the fixture answers for it so no case depends on how full
	# the real Data volume happens to be.
	cat >"$repo/scripts/disk-reclaim.sh" <<'FAKE_DISK_RECLAIM'
#!/usr/bin/env bash
exit 0
FAKE_DISK_RECLAIM
	chmod +x "$repo/scripts/disk-reclaim.sh"
	cat >"$bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -u
module="$(basename "$PWD")"
[ "$PWD" = "$FAKE_REPO" ] && module=.
case "${1:-}" in
	list) printf 'primeradiant.com/serf/%s\n' "$module"; exit 0 ;;
esac
printf '%s\t%s\n' "$module" "$*" >>"$FAKE_STATE/calls"
printf 'go-stdout:%s\n' "$module"
printf 'go-stderr:%s\n' "$module" >&2
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
exit 0
FAKE_GO
	chmod +x "$bin/go"
	cat >"$bin/make" <<'FAKE_MAKE'
#!/usr/bin/env bash
set -u
printf 'web\t%s\n' "$*" >>"$FAKE_STATE/calls"
printf 'web-stdout: 5308 tests passed\n'
case "${FAKE_WEB_FAIL:-0}" in
	0) exit 0 ;;
esac
printf 'web-stderr: 1 test failed\n' >&2
exit 1
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
		env TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
			MODULES="$modules" AGENT_SHARDS=0 MAKE="$bin/make" "$@" "$runner" -short -count=1
	) >"$output" 2>&1
}

started_streams() {
	cut -f1 "$state/calls" 2>/dev/null | sort | tr '\n' ' ' | sed 's/ *$//'
}

new_case
out="$case_dir/all-pass.out"
if run_tests ". agent llm" "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "all-passing run exits zero"
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" ". agent llm web" "every stream reports once, in wave order"
assert_one_verdict_per_name "$out" "all-passing run reports no name twice"
assert_eq "$(started_streams)" ". agent llm web" "every requested stream ran"
assert_not_has "$out" "=== failing module output ===" "all-passing run prints no failure section"
assert_not_has "$out" "go-stdout:" "passing suite chatter stays hidden"

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
llm_line="$(grep -nF -- '----- llm -----' "$out" | cut -d: -f1)"
auth_line="$(grep -nF -- '----- auth -----' "$out" | cut -d: -f1)"
if [ -n "$llm_line" ] && [ -n "$auth_line" ] && [ "$llm_line" -lt "$auth_line" ]; then
	ok "failure dumps follow MODULES order"
else
	bad "failure dumps are out of MODULES order"
fi

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
out="$case_dir/web-failure.out"
if FAKE_WEB_FAIL=1 run_tests "agent" "$out"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "a failing frontend stream exits nonzero"; else bad "a failing frontend stream unexpectedly exits zero"; fi
assert_has "$out" "FAIL  web" "the frontend stream reports its own verdict"
assert_one_verdict_per_name "$out" "frontend failure reports no name twice"
assert_dump_nonempty "$out" "a failing frontend stream dumps output"
assert_has "$out" "----- web -----" "the frontend log is dumped under its own name"
assert_has "$out" "web-stderr: 1 test failed" "the frontend's failure output reaches the reader"
assert_not_has "$out" "----- agent -----" "the passing module is not dumped alongside the frontend"

echo "----"
echo "run-module-tests-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
