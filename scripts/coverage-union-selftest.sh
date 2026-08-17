#!/usr/bin/env bash
# coverage-union-selftest.sh — exercises scripts/coverage-union.sh against a
# throwaway repo and a fake `go` that emits two DIFFERENT profiles depending on
# whether it was invoked with -tags serffuzz. No compilation, no real suite.
#
# The fixture is chosen so the union cannot be mistaken for either input: the
# test track covers only block A, the fuzz track covers only block B, so a
# correct union reports 100% where each track alone reports 40% and 60%.
set -uo pipefail

real_script="$(cd "$(dirname "$0")" && pwd)/coverage-union.sh"
. "$(dirname "$0")/selftest-lib.sh"

work="$(mktemp -d -t serf-covunion-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

repo="$work/repo"
mkdir -p "$repo/scripts" "$repo/agent"
cp "$real_script" "$repo/scripts/coverage-union.sh"
cp "$(dirname "$0")/gate-surface-lib.sh" "$repo/scripts/gate-surface-lib.sh"
cp "$(dirname "$0")/covstmt-lib.sh" "$repo/scripts/covstmt-lib.sh"
script="$repo/scripts/coverage-union.sh"
floors="$repo/scripts/covunion-floors.txt"
printf 'module fake\n\ngo 1.25\n' >"$repo/go.mod"
printf 'module fake/agent\n\ngo 1.25\n' >"$repo/agent/go.mod"

fake_bin="$work/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/go" <<'FAKEGO'
#!/bin/sh
if [ "$1" = list ]; then
	echo "fake/$(basename "$PWD")/alpha"
	exit 0
fi
prof=""
tagged=0
for a in "$@"; do
	case "$a" in
		-coverprofile=*) prof="${a#-coverprofile=}" ;;
		serffuzz) tagged=1 ;;
	esac
done
[ -n "$prof" ] || { echo "fake go: no -coverprofile in: $*" >&2; exit 2; }
{
	echo "mode: set"
	if [ "$tagged" = 1 ]; then
		# The fuzz track reaches block B only.
		echo "fake/a.go:1.1,2.1 2 0"
		if [ -n "${FAKE_GO_SHIFT_FUZZ_BLOCKS:-}" ]; then
			# Same code, different block boundaries — the shape that makes a
			# union denominator meaningless.
			echo "fake/b.go:3.1,4.9 3 1"
		else
			echo "fake/b.go:3.1,4.1 3 1"
		fi
	else
		# The test track reaches block A only.
		echo "fake/a.go:1.1,2.1 2 1"
		echo "fake/b.go:3.1,4.1 3 0"
	fi
} >"$prof"
exit 0
FAKEGO
chmod +x "$fake_bin/go"

tmphome="$work/tmp"
mkdir -p "$tmphome"
run() { PATH="$fake_bin:$PATH" TMPDIR="$tmphome" bash "$script" --modules "agent" "$@"; }

out="$work/out.txt"
: >"$floors"
run >"$out" 2>&1
assert_eq "$?" "0" "measure-only run exits zero"

# 5 of 5 statements, where neither track alone reaches more than 3.
if grep -qE '^agent +5 +5 +100\.0% +40\.0% +60\.0%' "$out"; then
	ok "union counts a block covered by EITHER track (100% from 40% and 60%)"
else
	bad "union row wrong"; sed 's/^/    | /' "$out"
fi
assert_eq "$(ls -A "$tmphome")" "" "a clean run leaves no scratch directory behind"

run --bless >"$out" 2>&1
assert_has "$floors" "agent 100.0" "bless records the measured union floor"

run --check >"$out" 2>&1
assert_eq "$?" "0" "check passes at the blessed floor"

printf '# keep this note\nagent 100.0\nllm 77.0\n' >"$floors"
run --bless >"$out" 2>&1
assert_has "$floors" "keep this note" "bless preserves a hand-written header note"
assert_has "$floors" "llm 77.0" "a partial bless keeps an unmeasured module's floor"

printf 'agent 100.0\n' >"$floors"
PATH="$fake_bin:$PATH" TMPDIR="$tmphome" FAKE_GO_FAIL_ALL=1 bash "$script" --modules "nosuch" >"$out" 2>&1
assert_has "$out" "(no module)" "a missing module is reported"

# A union denominator larger than both tracks means the tagged and untagged
# builds disagree about block boundaries; the percentage would be nonsense.
PATH="$fake_bin:$PATH" TMPDIR="$tmphome" FAKE_GO_SHIFT_FUZZ_BLOCKS=1 bash "$script" --modules "agent" >"$out" 2>&1
assert_eq "$?" "1" "a boundary mismatch fails rather than reporting a nonsense percentage"
assert_has "$out" "boundary mismatch" "the boundary mismatch is named"

help_out="$(bash "$script" --help 2>&1)"
if echo "$help_out" | grep -q "^Usage:" && ! echo "$help_out" | grep -q "^set -uo pipefail"; then
	ok "--help prints the whole header and stops at the script body"
else
	bad "--help is truncated or overran the header: $help_out"
fi

selftest_summary
