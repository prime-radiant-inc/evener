#!/usr/bin/env bash
# test-coverage-floor-selftest.sh — exercises scripts/test-coverage-floor.sh against
# a throwaway repo and a fake `go`, so the rollup and ratchet arithmetic is checked
# without compiling or running a real suite. Run via `make test-coverage-floor-selftest`
# or the dev-tooling wave.
#
# The regression that motivated this suite: the script accumulated measurements in a
# `declare -A` associative array, which bash 3.2 — the `env bash` every macOS host
# resolves — does not have. Worse, module "." made the fallback subscript an
# ARITHMETIC expression, so the run died mid-loop with "operand expected" after
# paying for a full coverage build. The script had no selftest, so it read as
# working for as long as nobody ran it on a Mac. The "." row assertions below are
# that tripwire: they fail loudly on any bash-4-only construct reintroduced here.
set -uo pipefail

real_script="$(cd "$(dirname "$0")" && pwd)/test-coverage-floor.sh"
. "$(dirname "$0")/selftest-lib.sh"

work="$(mktemp -d -t serf-testcov-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

# The script derives repo_root and the floors path from its OWN location, so the
# copy lives in the throwaway repo's scripts/ and both land inside it naturally —
# no production seam needed, and the real file's content is what runs.
repo="$work/repo"
mkdir -p "$repo/scripts" "$repo/agent"
cp "$real_script" "$repo/scripts/test-coverage-floor.sh"
script="$repo/scripts/test-coverage-floor.sh"
floors="$repo/scripts/testcov-global-floors.txt"
printf 'module fake\n\ngo 1.25\n' >"$repo/go.mod"
printf 'module fake/agent\n\ngo 1.25\n' >"$repo/agent/go.mod"

# A fake `go` that writes a synthetic coverprofile instead of compiling. The
# per-module split keys off the directory it was invoked in, which is how the
# suite tells the "." row apart from the "agent" row.
fake_bin="$work/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/go" <<'FAKEGO'
#!/bin/sh
prof=""
for a in "$@"; do
	case "$a" in -coverprofile=*) prof="${a#-coverprofile=}" ;; esac
done
[ -n "$prof" ] || { echo "fake go: no -coverprofile in: $*" >&2; exit 2; }
case "$(basename "$PWD")" in
	repo)  cov=1 ;;   # the "." module: 1 of 4 statements -> 25.0%
	agent) cov=3 ;;   # 3 of 4 statements -> 75.0%
	*)     cov=2 ;;
esac
{
	echo "mode: set"
	echo "fake/a.go:1.1,2.1 $cov 1"
	echo "fake/b.go:3.1,4.1 $((4 - cov)) 0"
} >"$prof"
exit 0
FAKEGO
chmod +x "$fake_bin/go"

run() { PATH="$fake_bin:$PATH" bash "$script" --modules ". agent" "$@"; }

out="$work/out.txt"
: >"$floors"
run >"$out" 2>&1
status=$?
assert_eq "$status" "0" "measure-only run exits zero"

# The bash-3.2 tripwire: under the old declare -A the run emitted these and then
# died before printing a single module row.
assert_not_has "$out" "declare: -A" "no bash-4-only declare -A"
assert_not_has "$out" "operand expected" "module \".\" is not evaluated as arithmetic"

if grep -qE '^\. +1 +4 +25\.0%' "$out"; then
	ok "module \".\" is measured and printed (25.0%)"
else
	bad "the \".\" row is missing or wrong"; sed 's/^/    | /' "$out"
fi
if grep -qE '^agent +3 +4 +75\.0%' "$out"; then
	ok "module \"agent\" is measured and printed (75.0%)"
else
	bad "the agent row is missing or wrong"; sed 's/^/    | /' "$out"
fi

# --bless must carry the MEASURED percentages through; the associative-array bug
# silently wrote back stale floors here even when the rows above looked right.
run --bless >"$out" 2>&1
assert_has "$floors" ". 25.0" "bless records the measured \".\" floor"
assert_has "$floors" "agent 75.0" "bless records the measured agent floor"

run --check >"$out" 2>&1
assert_eq "$?" "0" "check passes at the blessed floors"

printf '. 90.0\nagent 75.0\n' >"$floors"
run --check >"$out" 2>&1
assert_eq "$?" "1" "check fails when a module drops below its floor"
assert_has "$out" "REGRESSION: ." "the failing module is named"

printf '. 25.3\nagent 75.0\n' >"$floors"
run --check >"$out" 2>&1
assert_eq "$?" "0" "a drop within tolerance is not a regression"

printf '. 99.0\nagent 75.0\n' >"$floors"
run --bless >"$out" 2>&1
assert_has "$floors" ". 99.0" "bless keeps a higher existing floor"

# A module directory with no go.mod is reported, not silently skipped.
run --modules "nosuch" >"$out" 2>&1
assert_has "$out" "(no module)" "a missing module is reported"

help_out="$(bash "$script" --help 2>&1)"
if echo "$help_out" | grep -q "^Usage:" && ! echo "$help_out" | grep -q "^set -uo pipefail"; then
	ok "--help prints the whole header and stops at the script body"
else
	bad "--help is truncated or overran the header: $help_out"
fi

selftest_summary
