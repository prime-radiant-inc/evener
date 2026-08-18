#!/usr/bin/env bash
# web-coverage-floor-selftest.sh — exercises scripts/web-coverage-floor.sh against
# a throwaway frontend and a synthetic coverage summary. No vitest run, no npm, no
# network: --reuse feeds the rollup a hand-written coverage-summary.json, so every
# check here is about the rollup and ratchet arithmetic rather than about the
# frontend suite. Run via `make web-coverage-floor-selftest` or the dev-tooling wave.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/web-coverage-floor.sh"
. "$(dirname "$0")/selftest-lib.sh"

scratch_dir work evener-webcov-selftest
trap 'scratch_rm' EXIT

frontend="$work/frontend"
mkdir -p "$frontend/coverage"
floors="$work/floors.txt"

# A synthetic report: two panes (one wholly untested), one store, and one file
# sitting directly in src/. The untested pane is the point — it must land in the
# denominator as 0%, not vanish, which is the false green coverage.include buys.
# It is kept as a template too, so the measuring-path checks below can hand it
# to the fake npm as "the report this suite run wrote".
cat >"$work/summary.json" <<JSON
{
  "total": {"lines": {"total": 0, "covered": 0, "skipped": 0, "pct": 0}},
  "$frontend/src/panes/alpha.tsx": {"lines": {"total": 100, "covered": 50, "skipped": 0, "pct": 50}},
  "$frontend/src/panes/beta.tsx":  {"lines": {"total": 100, "covered": 0,  "skipped": 0, "pct": 0}},
  "$frontend/src/stores/one.ts":   {"lines": {"total": 50,  "covered": 45, "skipped": 0, "pct": 90}},
  "$frontend/src/auth.ts":         {"lines": {"total": 10,  "covered": 8,  "skipped": 0, "pct": 80}}
}
JSON
cp "$work/summary.json" "$frontend/coverage/coverage-summary.json"

run() { EVENER_WEB_FRONTEND_DIR="$frontend" EVENER_WEBCOV_FLOORS="$floors" bash "$script" --reuse "$@"; }

: >"$floors"
out="$work/out.txt"
run >"$out" 2>&1
assert_eq "$?" "0" "measure-only run exits zero"
# panes: (50+0)/(100+100) = 25.0 — the untested file dilutes, rather than absents.
assert_has "$out" "panes" "rollup names the panes area"
if grep -qE '^panes +50 +200 +25\.0%' "$out"; then
	ok "untested file stays in the denominator (panes = 25.0%)"
else
	bad "panes rollup wrong"; sed 's/^/    | /' "$out"
fi
if grep -qE '^\(root\) +8 +10 +80\.0%' "$out"; then
	ok "files directly in src/ roll up as (root)"
else
	bad "(root) rollup missing or wrong"; sed 's/^/    | /' "$out"
fi
# total: (50+0+45+8)/(100+100+50+10) = 103/260 = 39.6
if grep -qE '^total +103 +260 +39\.6%' "$out"; then
	ok "total row sums every area"
else
	bad "total rollup wrong"; sed 's/^/    | /' "$out"
fi

# --bless records the measured numbers.
run --bless >"$out" 2>&1
assert_has "$floors" "panes 25.0" "bless records the measured panes floor"
assert_has "$floors" "stores 90.0" "bless records the measured stores floor"
assert_has "$floors" "total 39.6" "bless records the measured total floor"

# --check against the just-blessed floors passes.
run --check >"$out" 2>&1
assert_eq "$?" "0" "check passes at the blessed floors"

# A floor above the measurement is a regression: non-zero exit, named area.
printf 'panes 90.0\n' >"$floors"
run --check >"$out" 2>&1
assert_eq "$?" "1" "check fails when an area drops below its floor"
assert_has "$out" "REGRESSION: panes" "the failing area is named"

# A drop inside the tolerance band is not a regression.
printf 'panes 25.3\n' >"$floors"
run --check >"$out" 2>&1
assert_eq "$?" "0" "a drop within tolerance is not a regression"

# bless never lowers a floor that is already higher than the measurement.
printf 'panes 99.0\n' >"$floors"
run --bless >"$out" 2>&1
assert_has "$floors" "panes 99.0" "bless keeps a higher existing floor"

# A hand-written basis note explains why a floor was reset downward, exactly
# like the two Go floor files. Rewriting a fixed header on every bless deleted
# that reason the next time anyone raised a floor.
printf '# why this basis changed: the denominator moved\npanes 25.0\n' >"$floors"
run --bless >"$out" 2>&1
assert_has "$floors" "why this basis changed" "bless preserves a hand-written header note"
assert_has "$floors" "stores 90.0" "bless still records floors alongside the preserved note"

# A missing report under --reuse is a clear failure, not a silent zero.
rm -f "$frontend/coverage/coverage-summary.json"
run >"$out" 2>&1
assert_eq "$?" "1" "a missing coverage summary exits non-zero"
assert_has "$out" "no coverage summary" "the missing summary is explained"

# ---- the measuring (non --reuse) path, against a fake npm ----
# The fake stands in for `npm run test:coverage`: FAKE_NPM_REPORT names the
# summary the run "writes" (vitest's reportOnFailure writes one on red too),
# and FAKE_NPM_EXIT is the suite's status. No report env means the run died —
# or reported somewhere else — before writing one.
fake_bin="$work/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/npm" <<'FAKENPM'
#!/bin/sh
if [ -n "${FAKE_NPM_REPORT:-}" ]; then
	mkdir -p coverage
	cp "$FAKE_NPM_REPORT" coverage/coverage-summary.json
fi
exit "${FAKE_NPM_EXIT:-0}"
FAKENPM
chmod +x "$fake_bin/npm"
measure() { PATH="$fake_bin:$PATH" EVENER_WEB_FRONTEND_DIR="$frontend" EVENER_WEBCOV_FLOORS="$floors" bash "$script" "$@"; }

# A green suite that writes a fresh report clears its floors.
printf 'panes 25.0\n' >"$floors"
FAKE_NPM_REPORT="$work/summary.json" measure --check >"$out" 2>&1
assert_eq "$?" "0" "a green suite with a fresh report passes check"

# A red suite must fail --check even when the report it left clears every
# floor: numbers measured under failing tests are not a pass.
FAKE_NPM_REPORT="$work/summary.json" FAKE_NPM_EXIT=1 measure --check >"$out" 2>&1
assert_eq "$?" "1" "a red suite fails check even when the numbers clear the floors"
assert_has "$out" "SUITE FAILED" "the red suite is named as the reason"

# A run that produced no fresh report must not measure the previous run's:
# the stale summary here clears the floors, and used to pass check silently.
cp "$work/summary.json" "$frontend/coverage/coverage-summary.json"
measure --check >"$out" 2>&1
assert_eq "$?" "1" "a run that wrote no fresh report fails check instead of measuring a stale one"
assert_has "$out" "no coverage summary" "the missing fresh report is explained"

# --bless from a red suite would ratchet floors onto numbers the tests do not
# stand behind; it must refuse and leave the floor file alone.
printf 'panes 25.0\n' >"$floors"
FAKE_NPM_REPORT="$work/summary.json" FAKE_NPM_EXIT=1 measure --bless >"$out" 2>&1
assert_eq "$?" "1" "bless refuses when the suite is red"
assert_has "$out" "refusing" "the refusal is explained"
assert_eq "$(cat "$floors")" "panes 25.0" "a refused bless leaves the floor file untouched"

# --help prints the whole header and stops at the script body.
help_out="$(bash "$script" --help 2>&1)"
if echo "$help_out" | grep -q "^Usage:" && ! echo "$help_out" | grep -q "^set -uo pipefail"; then
	ok "--help prints the whole header and stops at the script body"
else
	bad "--help is truncated or overran the header: $help_out"
fi

selftest_summary
