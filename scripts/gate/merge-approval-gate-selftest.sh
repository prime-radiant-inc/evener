#!/usr/bin/env bash
# merge-approval-gate-selftest.sh - behavioral tests for the canonical serial gate.
set -uo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
real_make="$(command -v make)"
. "$(dirname "$0")/../lib/selftest-lib.sh"

scratch_dir work evener-merge-approval-gate-selftest
trap 'scratch_rm' EXIT

assert_before() {
	first="$(grep -n -m 1 -F -- "$2" "$1" 2>/dev/null | cut -d: -f1)"
	second="$(grep -n -m 1 -F -- "$3" "$1" 2>/dev/null | cut -d: -f1)"
	if [ -n "$first" ] && [ -n "$second" ] && [ "$first" -lt "$second" ]; then
		ok "$4"
	else
		bad "$4 (want '$2' before '$3')"
		sed 's/^/    | /' "$1" 2>/dev/null || :
	fi
}

fake_make="$work/fake-make"
cat >"$fake_make" <<'FAKE_MAKE'
#!/usr/bin/env bash
set -u
target="${!#}"
printf '%s\t%s\n' "$target" "${ROOT_FULL:-}" >>"$FAKE_STATE/calls"
printf 'recursive stdout: %s\n' "$target"
printf 'recursive stderr: %s\n' "$target" >&2
if [ "${FAKE_FAIL_TARGET:-}" = "$target" ]; then
	exit 23
fi
exit 0
FAKE_MAKE
chmod +x "$fake_make"

run_gate() {
	failure="$1"
	output="$2"
	rm -f "$work/calls"
	if env -u ROOT_FULL FAKE_STATE="$work" FAKE_FAIL_TARGET="$failure" \
		"$real_make" -C "$repo_root" -j 4 MAKE="$fake_make" merge-approval-gate \
		>"$output" 2>&1; then
		gate_rc=0
	else
		gate_rc=$?
	fi
}

run_gate "" "$work/success.out"
assert_eq "$gate_rc" "0" "successful gate exits zero"
expected_calls=$(printf 'lint\t\nbuild\t\ntest\t1\ntest-dev-tooling\t\n')
actual_calls="$(cat "$work/calls" 2>/dev/null || :)"
assert_eq "$actual_calls" "$expected_calls" "success runs lint, build, test, then test-dev-tooling"
assert_has "$work/success.out" "recursive stdout: lint" "child stdout is visible"
assert_has "$work/success.out" "recursive stderr: test" "child stderr is visible"
assert_before "$work/calls" "test	" "test-dev-tooling	" "test-dev-tooling is invoked after test"

run_gate lint "$work/lint-failure.out"
if [ "$gate_rc" -ne 0 ]; then
	ok "lint failure exits nonzero"
else
	bad "lint failure exits nonzero"
fi
actual_calls="$(cat "$work/calls" 2>/dev/null || :)"
assert_eq "$actual_calls" "lint$(printf '\t')" "lint failure stops later phases"

run_gate build "$work/build-failure.out"
if [ "$gate_rc" -ne 0 ]; then
	ok "build failure exits nonzero"
else
	bad "build failure exits nonzero"
fi
expected_calls=$(printf 'lint\t\nbuild\t\n')
actual_calls="$(cat "$work/calls" 2>/dev/null || :)"
assert_eq "$actual_calls" "$expected_calls" "build failure stops test"

# kata 5gvk: FAKE_GATE_PROBE_BLOCKED forces scripts/gate-capability-preflight.sh
# to report the named capabilities BLOCKED without touching the real host, so
# the gate's classify-once/skip-not-fail/structured-summary contract is
# provable without an actually restricted sandbox.
run_gate_blocked() {
	blocked="$1"
	failure="$2"
	output="$3"
	rm -f "$work/calls"
	if env -u ROOT_FULL FAKE_STATE="$work" FAKE_FAIL_TARGET="$failure" FAKE_GATE_PROBE_BLOCKED="$blocked" \
		"$real_make" -C "$repo_root" -j 4 MAKE="$fake_make" merge-approval-gate \
		>"$output" 2>&1; then
		gate_rc=0
	else
		gate_rc=$?
	fi
}

# The all-available path, forced: an EMPTY FAKE_GATE_PROBE_BLOCKED reports
# every capability AVAILABLE without consulting the host. Asserting this
# against the real probe instead would make the assertion a claim about the
# machine running the selftest - a host with no Chrome, or no writable git
# cache directory, genuinely has a blocked capability, and this suite runs
# inside `make test-dev-tooling`, a merge-approval-gate phase. The gate would
# then fail on exactly the restricted hosts kata 5gvk exists to keep it green
# on. run_gate above still drives the REAL probe end to end, so the
# evener-gate-probe -> preflight-parser wire contract stays covered.
run_gate_blocked "" "" "$work/all-available.out"
assert_eq "$gate_rc" "0" "the all-available green path exits zero"
assert_not_has "$work/all-available.out" "BLOCKED" "the all-available green path reports nothing blocked"

run_gate_blocked "loopback-bind" "" "$work/blocked-loopback.out"
assert_eq "$gate_rc" "0" "a blocked capability alone does not fail the gate"
expected_calls=$(printf 'lint\t\nbuild\t\ntest\t1\ntest-dev-tooling\t\n')
actual_calls="$(cat "$work/calls" 2>/dev/null || :)"
assert_eq "$actual_calls" "$expected_calls" "a blocked capability still runs every feasible phase, in order"
assert_has "$work/blocked-loopback.out" "BLOCKED loopback-bind" "the blocked capability is named in the structured summary"
assert_has "$work/blocked-loopback.out" "go run ./cmd/evener-gate-probe -only=loopback-bind" "the summary carries an exact reprobe command"
assert_has "$work/blocked-loopback.out" "rerun once fixed" "the summary carries an exact rerun command for the skipped tests"
blocked_lines="$(grep -c 'BLOCKED loopback-bind' "$work/blocked-loopback.out" 2>/dev/null || echo 0)"
assert_eq "$blocked_lines" "1" "the capability is classified once, not once per phase"

# Two capabilities the kata names have no known gate consumer yet
# (scripts/gate-surface-lib.sh's registry, kata 5gvk's premise check). They
# must still classify and report honestly - never silently dropped.
run_gate_blocked "chrome-cdp git-cache" "" "$work/blocked-no-consumer.out"
assert_eq "$gate_rc" "0" "capabilities with no gate consumer still do not fail the gate"
assert_has "$work/blocked-no-consumer.out" "BLOCKED chrome-cdp" "chrome-cdp is classified and reported even with nothing to skip"
assert_has "$work/blocked-no-consumer.out" "BLOCKED git-cache" "git-cache is classified and reported even with nothing to skip"
assert_has "$work/blocked-no-consumer.out" "no gate component currently depends on this" "a capability with no consumer says so honestly"

# A blocked capability must never mask a genuine failure elsewhere - the
# structured summary and a real FAIL are reported together, not one instead
# of the other.
run_gate_blocked "loopback-bind" "build" "$work/blocked-and-failed.out"
if [ "$gate_rc" -ne 0 ]; then
	ok "a genuine failure alongside a blocked capability still fails the gate"
else
	bad "a genuine failure alongside a blocked capability still fails the gate"
fi
expected_calls=$(printf 'lint\t\nbuild\t\n')
actual_calls="$(cat "$work/calls" 2>/dev/null || :)"
assert_eq "$actual_calls" "$expected_calls" "a genuine failure still stops later phases even when something is blocked"
assert_has "$work/blocked-and-failed.out" "BLOCKED loopback-bind" "the blocked-capability summary still appears alongside a real failure"

# Run the real Makefile in a throwaway tree, faking only the external
# commands (go, npm, git) and the scripts it shells out to, so it proves
# make-level target wiring — web-preflight before the frontend build, the
# frontend build before runtime Go work, dist's default-platform discovery —
# independent of what run-module-tests-selftest.sh and the evener-dev
# module-lint tests already cover for their own targets.
make_case="$work/make-wiring"
make_repo="$make_case/repo"
make_state="$make_case/state"
make_bin="$make_case/bin"
mkdir -p "$make_repo/scripts/web" "$make_repo/scripts/ops" "$make_repo/cmd/evener-hub/frontend" "$make_state" "$make_bin"
for module in agent llm auth envvars invariant identifier fuzz; do
	mkdir -p "$make_repo/$module"
done

cat >"$make_repo/scripts/web/web-preflight.sh" <<'FAKE_WEB_PREFLIGHT'
#!/usr/bin/env bash
set -u
printf 'web-preflight\n' >>"$FAKE_STATE/calls"
FAKE_WEB_PREFLIGHT
cat >"$make_repo/scripts/ops/build-runtime-pair.sh" <<'FAKE_RUNTIME_BUILD'
#!/usr/bin/env bash
set -u
printf 'go-build-runtime\n' >>"$FAKE_STATE/calls"
FAKE_RUNTIME_BUILD
cat >"$make_repo/scripts/ops/gitleaks-scan.sh" <<'FAKE_GITLEAKS'
#!/usr/bin/env bash
set -u
printf 'gitleaks\t%s\n' "$*" >>"$FAKE_STATE/calls"
FAKE_GITLEAKS
chmod +x "$make_repo/scripts/web/web-preflight.sh" "$make_repo/scripts/ops/build-runtime-pair.sh" \
	"$make_repo/scripts/ops/gitleaks-scan.sh"

cat >"$make_bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -u
if [ "${1:-}" = env ]; then
	printf 'go-env\t%s\n' "$*" >>"$FAKE_STATE/calls"
	case "${2:-}" in
		GOOS) printf 'darwin\n' ;;
		GOARCH) printf 'arm64\n' ;;
	esac
	exit 0
fi
printf 'go\t%s\n' "$*" >>"$FAKE_STATE/calls"
exit "${FAKE_GO_STATUS:-0}"
FAKE_GO
cat >"$make_bin/gofmt" <<'FAKE_GOFMT'
#!/usr/bin/env bash
set -u
printf 'gofmt\t%s\n' "$*" >>"$FAKE_STATE/calls"
FAKE_GOFMT
cat >"$make_bin/npm" <<'FAKE_NPM'
#!/usr/bin/env bash
set -u
printf 'npm\t%s\n' "$*" >>"$FAKE_STATE/calls"
FAKE_NPM
cat >"$make_bin/git" <<'FAKE_GIT'
#!/usr/bin/env bash
set -u
printf 'git\t%s\n' "$*" >>"$FAKE_STATE/calls"
case "${1:-}" in
	rev-parse) printf 'fixture\n' ;;
esac
FAKE_GIT
chmod +x "$make_bin/go" "$make_bin/gofmt" "$make_bin/npm" "$make_bin/git"

run_make_target() {
	target="$1"
	output="$2"
	: >"$make_state/calls"
	if (
		cd "$make_repo" || exit 1
		PATH="$make_bin:/usr/bin:/bin" FAKE_STATE="$make_state" \
			"$real_make" -f "$repo_root/Makefile" --no-print-directory -j 4 "$target"
	) >"$output" 2>&1; then
		make_rc=0
	else
		make_rc=$?
	fi
}

run_make_dry_target() {
	target="$1"
	output="$2"
	: >"$make_state/calls"
	if (
		cd "$make_repo" || exit 1
		PATH="$make_bin:/usr/bin:/bin" FAKE_STATE="$make_state" \
			"$real_make" -f "$repo_root/Makefile" --no-print-directory -n "$target"
	) >"$output" 2>&1; then
		make_rc=0
	else
		make_rc=$?
	fi
}

run_make_target build "$make_case/build.out"
assert_eq "$make_rc" "0" "build runs through the real Makefile"
assert_before "$make_state/calls" "web-preflight" "npm	run build" "build-web keeps web-preflight before the frontend build"
assert_before "$make_state/calls" "npm	run build" "go-build-runtime" "build waits for the frontend build before runtime Go work"

run_make_dry_target dist "$make_case/dist-dry-run.out"
assert_eq "$make_rc" "0" "dist still resolves its default target platform"
assert_has "$make_state/calls" "go-env	env GOOS" "dist discovers its default operating system"
assert_has "$make_state/calls" "go-env	env GOARCH" "dist discovers its default architecture"

# Issue #181: when scripts/gate/gate-capability-preflight.sh cannot execute
# at all (missing, or present but not executable), the merge-approval-gate
# recipe's eval of its output must STOP the gate rather than silently
# proceeding on an empty eval. Without the guard the gate FAILS OPEN: the
# command substitution yields empty (the shell prints an error to stderr and
# returns nonzero, but that never reaches the `&&`), `eval ""` returns 0, the
# `&&` chain continues into lint/build/test/test-dev-tooling, and an
# inherited EVENER_GATE_CAPABILITY_SKIP survives unoverwritten. The script's
# own doc comment promises the opposite, which only holds once the script
# runs.
#
# These assertions prove the fail-open by running the REAL Makefile recipe
# against a throwaway tree whose scripts/gate/ lacks
# gate-capability-preflight.sh, so the relative path the recipe evals
# resolves to a missing file. MAKE is faked (fake_make) so the recursive
# phases only record their target name to $FAKE_STATE/calls; the gate must
# stop at the preflight, so calls stays empty. The GREEN fix adds an explicit
# existence/executability guard that exits 1.
failopen_case="$work/failopen"
failopen_repo="$failopen_case/repo"
mkdir -p "$failopen_repo/scripts/gate"
# The throwaway tree needs none of the other scripts merge-approval-gate
# reaches: fake_make intercepts every recursive lint/build/test/test-dev-tooling
# call, and gate-capability-preflight.sh (the only script the recipe evals
# directly) is deliberately the one thing absent.

run_gate_failopen() {
	output="$1"
	rm -f "$work/calls"
	if env -u ROOT_FULL FAKE_STATE="$work" \
		"$real_make" -C "$failopen_repo" -f "$repo_root/Makefile" \
		--no-print-directory -j 4 MAKE="$fake_make" merge-approval-gate \
		>"$output" 2>&1; then
		gate_rc=0
	else
		gate_rc=$?
	fi
}

# Primary fail-open trigger: the preflight script is absent from the tree.
run_gate_failopen "$work/failopen-missing.out"
if [ "$gate_rc" -ne 0 ]; then
	ok "missing preflight script fails the gate closed (issue #181)"
else
	bad "missing preflight script fails the gate closed (issue #181) - gate continued with exit 0 (fail-open)"
fi
actual_calls="$(cat "$work/calls" 2>/dev/null || :)"
assert_eq "$actual_calls" "" "missing preflight script stops the gate before any phase runs (issue #181)"

# Second fail-open trigger: the preflight script is present but not
# executable, so the shell cannot run it and the command substitution yields
# empty exactly as for a missing file. An existence/executability guard must
# catch both.
cat >"$failopen_repo/scripts/gate/gate-capability-preflight.sh" <<'NONEXEC_PREFLIGHT'
#!/usr/bin/env bash
echo "this body must never run" >&2
exit 0
NONEXEC_PREFLIGHT
chmod -x "$failopen_repo/scripts/gate/gate-capability-preflight.sh"
run_gate_failopen "$work/failopen-nonexec.out"
if [ "$gate_rc" -ne 0 ]; then
	ok "non-executable preflight script fails the gate closed (issue #181)"
else
	bad "non-executable preflight script fails the gate closed (issue #181) - gate continued with exit 0 (fail-open)"
fi
actual_calls="$(cat "$work/calls" 2>/dev/null || :)"
assert_eq "$actual_calls" "" "non-executable preflight script stops the gate before any phase runs (issue #181)"

selftest_summary
