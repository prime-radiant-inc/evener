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
assert_not_has() {
	if grep -qF -- "$2" "$1"; then
		bad "$3 (unexpected '$2')"
		sed 's/^/    | /' "$1" 2>/dev/null || :
	else
		ok "$3"
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

# Run the real Makefile in a throwaway tree. The fixture replaces only the
# host-dependent cache probe and the commands beyond it, so it proves the
# public lint/build targets stop before Go work when the cache is unavailable.
cache_case="$work/cache-preflight"
cache_repo="$cache_case/repo"
cache_state="$cache_case/state"
cache_bin="$cache_case/bin"
mkdir -p "$cache_repo/scripts" "$cache_repo/cmd/serf-hub/frontend" "$cache_state" "$cache_bin"
for module in agent llm auth envvars invariant identifier fuzz; do
	mkdir -p "$cache_repo/$module"
done

cat >"$cache_repo/scripts/disk-reclaim.sh" <<'FAKE_DISK_RECLAIM'
#!/usr/bin/env bash
set -u
printf 'disk-reclaim\t%s\n' "$*" >>"$FAKE_STATE/calls"
if [ "${FAKE_DISK_STATUS:-0}" -ne 0 ]; then
	echo "disk-reclaim: fixture GOCACHE is STALLED and unavailable" >&2
fi
exit "${FAKE_DISK_STATUS:-0}"
FAKE_DISK_RECLAIM
cat >"$cache_repo/scripts/web-preflight.sh" <<'FAKE_WEB_PREFLIGHT'
#!/usr/bin/env bash
set -u
printf 'web-preflight\n' >>"$FAKE_STATE/calls"
FAKE_WEB_PREFLIGHT
cat >"$cache_repo/scripts/build-runtime-pair.sh" <<'FAKE_RUNTIME_BUILD'
#!/usr/bin/env bash
set -u
printf 'go-build-runtime\n' >>"$FAKE_STATE/calls"
FAKE_RUNTIME_BUILD
cat >"$cache_repo/scripts/run-module-lint.sh" <<'FAKE_MODULE_LINT'
#!/usr/bin/env bash
set -u
printf 'module-lint\n' >>"$FAKE_STATE/calls"
FAKE_MODULE_LINT
cat >"$cache_repo/scripts/gitleaks-scan.sh" <<'FAKE_GITLEAKS'
#!/usr/bin/env bash
set -u
printf 'gitleaks\t%s\n' "$*" >>"$FAKE_STATE/calls"
FAKE_GITLEAKS
chmod +x "$cache_repo/scripts/disk-reclaim.sh" "$cache_repo/scripts/web-preflight.sh" \
	"$cache_repo/scripts/build-runtime-pair.sh" "$cache_repo/scripts/run-module-lint.sh" \
	"$cache_repo/scripts/gitleaks-scan.sh"

cat >"$cache_bin/go" <<'FAKE_GO'
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
cat >"$cache_bin/gofmt" <<'FAKE_GOFMT'
#!/usr/bin/env bash
set -u
printf 'gofmt\t%s\n' "$*" >>"$FAKE_STATE/calls"
FAKE_GOFMT
cat >"$cache_bin/npm" <<'FAKE_NPM'
#!/usr/bin/env bash
set -u
printf 'npm\t%s\n' "$*" >>"$FAKE_STATE/calls"
FAKE_NPM
cat >"$cache_bin/git" <<'FAKE_GIT'
#!/usr/bin/env bash
set -u
printf 'git\t%s\n' "$*" >>"$FAKE_STATE/calls"
case "${1:-}" in
	rev-parse) printf 'fixture\n' ;;
esac
FAKE_GIT
chmod +x "$cache_bin/go" "$cache_bin/gofmt" "$cache_bin/npm" "$cache_bin/git"

run_cache_target() {
	target="$1"
	output="$2"
	: >"$cache_state/calls"
	if (
		cd "$cache_repo" || exit 1
		PATH="$cache_bin:/usr/bin:/bin" FAKE_STATE="$cache_state" FAKE_DISK_STATUS="${FAKE_DISK_STATUS:-0}" \
			"$real_make" -f "$repo_root/Makefile" --no-print-directory -j 4 "$target"
	) >"$output" 2>&1; then
		cache_rc=0
	else
		cache_rc=$?
	fi
}

run_cache_dry_target() {
	target="$1"
	output="$2"
	: >"$cache_state/calls"
	if (
		cd "$cache_repo" || exit 1
		PATH="$cache_bin:/usr/bin:/bin" FAKE_STATE="$cache_state" \
			"$real_make" -f "$repo_root/Makefile" --no-print-directory -n "$target"
	) >"$output" 2>&1; then
		cache_rc=0
	else
		cache_rc=$?
	fi
}

FAKE_DISK_STATUS=41 run_cache_target lint "$cache_case/lint-stalled.out"
assert_eq "$cache_rc" "2" "stalled cache makes aggregate lint fail through make"
assert_has "$cache_case/lint-stalled.out" "fixture GOCACHE is STALLED and unavailable" "lint keeps the cache diagnosis visible"
assert_eq "$(cat "$cache_state/calls")" "disk-reclaim	--check" "stalled cache stops lint before any Go command"
assert_not_has "$cache_state/calls" "go-env	env GOOS" "lint parsing does not invoke go env before its cache preflight"
assert_not_has "$cache_state/calls" "go-env	env GOARCH" "lint parsing does not invoke Go architecture discovery"

FAKE_DISK_STATUS=41 run_cache_target build "$cache_case/build-stalled.out"
assert_eq "$cache_rc" "2" "stalled cache makes build fail through make"
assert_has "$cache_case/build-stalled.out" "fixture GOCACHE is STALLED and unavailable" "build keeps the cache diagnosis visible"
assert_has "$cache_state/calls" "disk-reclaim	--check" "build runs the cache preflight"
assert_not_has "$cache_state/calls" "go-build-runtime" "stalled cache stops build before Go work"
assert_not_has "$cache_state/calls" "go-env	env GOOS" "build parsing does not invoke go env before its cache preflight"
assert_not_has "$cache_state/calls" "go-env	env GOARCH" "build parsing does not invoke Go architecture discovery"

FAKE_DISK_STATUS=0 run_cache_target lint "$cache_case/lint-reachable.out"
assert_eq "$cache_rc" "0" "reachable cache permits aggregate lint"
assert_before "$cache_state/calls" "disk-reclaim	--check" "go	" "lint checks the cache before Go work"

FAKE_DISK_STATUS=0 run_cache_target build "$cache_case/build-reachable.out"
assert_eq "$cache_rc" "0" "reachable cache permits build"
assert_before "$cache_state/calls" "disk-reclaim	--check" "go-build-runtime" "build checks the cache before runtime Go work"
assert_before "$cache_state/calls" "web-preflight" "npm	run build" "build-web keeps web-preflight before the frontend build"
assert_before "$cache_state/calls" "npm	run build" "go-build-runtime" "build waits for the frontend build before runtime Go work"

run_cache_dry_target dist "$cache_case/dist-dry-run.out"
assert_eq "$cache_rc" "0" "dist still resolves its default target platform"
assert_has "$cache_state/calls" "go-env	env GOOS" "dist discovers its default operating system"
assert_has "$cache_state/calls" "go-env	env GOARCH" "dist discovers its default architecture"

printf '%s\n' "merge-approval-gate-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
