#!/usr/bin/env bash
# merge-approval-gate-selftest.sh - behavioral tests for the canonical serial gate.
set -uo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
real_make="$(command -v make)"
work="$(mktemp -d -t serf-merge-approval-gate-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

checks=0
fails=0
ok() { checks=$((checks + 1)); printf 'ok   - %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL - %s\n' "$1"; }
assert_eq() {
	if [ "$1" = "$2" ]; then
		ok "$3"
	else
		bad "$3 (want '$2', got '$1')"
	fi
}
assert_has() {
	if grep -qF -- "$2" "$1"; then
		ok "$3"
	else
		bad "$3 (missing '$2')"
		sed 's/^/    | /' "$1" 2>/dev/null || :
	fi
}
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
expected_calls=$(printf 'lint\t\nbuild\t\ntest\t1\n')
actual_calls="$(cat "$work/calls" 2>/dev/null || :)"
assert_eq "$actual_calls" "$expected_calls" "success runs lint, build, then test"
assert_has "$work/success.out" "recursive stdout: lint" "child stdout is visible"
assert_has "$work/success.out" "recursive stderr: test" "child stderr is visible"

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

# Run the real Makefile in a throwaway tree, faking only the external
# commands (go, npm, git) and the scripts it shells out to, so it proves
# make-level target wiring — web-preflight before the frontend build, the
# frontend build before runtime Go work, dist's default-platform discovery —
# independent of what run-module-lint-selftest.sh and
# run-module-tests-selftest.sh already cover for their own targets.
make_case="$work/make-wiring"
make_repo="$make_case/repo"
make_state="$make_case/state"
make_bin="$make_case/bin"
mkdir -p "$make_repo/scripts" "$make_repo/cmd/serf-hub/frontend" "$make_state" "$make_bin"
for module in agent llm auth envvars invariant identifier fuzz; do
	mkdir -p "$make_repo/$module"
done

cat >"$make_repo/scripts/web-preflight.sh" <<'FAKE_WEB_PREFLIGHT'
#!/usr/bin/env bash
set -u
printf 'web-preflight\n' >>"$FAKE_STATE/calls"
FAKE_WEB_PREFLIGHT
cat >"$make_repo/scripts/build-runtime-pair.sh" <<'FAKE_RUNTIME_BUILD'
#!/usr/bin/env bash
set -u
printf 'go-build-runtime\n' >>"$FAKE_STATE/calls"
FAKE_RUNTIME_BUILD
cat >"$make_repo/scripts/run-module-lint.sh" <<'FAKE_MODULE_LINT'
#!/usr/bin/env bash
set -u
printf 'module-lint\n' >>"$FAKE_STATE/calls"
FAKE_MODULE_LINT
cat >"$make_repo/scripts/gitleaks-scan.sh" <<'FAKE_GITLEAKS'
#!/usr/bin/env bash
set -u
printf 'gitleaks\t%s\n' "$*" >>"$FAKE_STATE/calls"
FAKE_GITLEAKS
chmod +x "$make_repo/scripts/web-preflight.sh" "$make_repo/scripts/build-runtime-pair.sh" \
	"$make_repo/scripts/run-module-lint.sh" "$make_repo/scripts/gitleaks-scan.sh"

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

printf '%s\n' "merge-approval-gate-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
