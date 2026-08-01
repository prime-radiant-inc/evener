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

printf '%s\n' "merge-approval-gate-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
