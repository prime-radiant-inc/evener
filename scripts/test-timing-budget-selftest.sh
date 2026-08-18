#!/usr/bin/env bash
# test-timing-budget-selftest.sh — exercises scripts/test-timing-budget.sh's
# comparison contract against fixture "already-measured" duration rows and
# fixture testing-budget.json files, via --measured (test-timing-budget.sh's
# own --reuse-shaped seam — see web-coverage-floor.sh's --reuse for the same
# idea). No go test, no vitest, no network: every check here is about the
# ratio/ceiling/missing-entry/no-baseline arithmetic and the strict-vs-warn-only
# exit policy, not about running a real suite. Run via
# `make test-timing-budget-selftest` or the dev-tooling wave.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/test-timing-budget.sh"
. "$(dirname "$0")/selftest-lib.sh"

scratch_dir work evener-testbudget-selftest
trap 'scratch_rm' EXIT

budget="$work/budget.json"
measured="$work/measured.tsv"
out="$work/out.txt"

# run ARGS... — always passes --no-web (no frontend fixture here — the vitest
# side is exercised through its own package key, "web", which the comparison
# step treats no differently from any Go package) and a private TMPDIR so a
# stray scratch dir would be caught by selftest's own hygiene below.
run() {
	TMPDIR="$work/tmp" bash "$script" --no-web --budget "$budget" --measured "$measured" "$@"
}
mkdir -p "$work/tmp"

# ---- 1. under budget: ratio <= 1.1 -> OK, never fails, in any policy. ----
printf 'SUM\tpkgA\t9.0\n' >"$measured"
printf '{"perTestCeilingSeconds": 2, "packages": {"pkgA": 10.0, "pkgB": 10.0, "pkgC": 10.0, "pkgD": 100.0}}\n' >"$budget"
run --check --strict >"$out" 2>&1
assert_eq "$?" "0" "under-budget package: --check --strict exits 0"
assert_has "$out" "OK    pkgA: 9.00s (budget 10.00s)" "under-budget package is reported OK with its ratio inputs"

# ---- 2. warn band: 1.1x < ratio <= 1.5x -> WARN, never fails either policy. ----
printf 'SUM\tpkgB\t11.5\n' >"$measured"
run --check --strict >"$out" 2>&1
assert_eq "$?" "0" "warn-band package: --check --strict still exits 0 (warn is not fatal)"
assert_has "$out" "WARN  pkgB: 11.50s over budget 10.00s (1.15x > 1.1x)" "warn-band package names its ratio against the 1.1x line"
run --check --local >"$out" 2>&1
assert_eq "$?" "0" "warn-band package: --check --local exits 0"

# ---- 3. fail band: ratio > 1.5x -> FAIL. Strict/CI fails; local warns only. ----
printf 'SUM\tpkgC\t16.0\n' >"$measured"
run --check --strict >"$out" 2>&1
assert_eq "$?" "1" "fail-band package: --check --strict exits 1"
assert_has "$out" "FAIL  pkgC: 16.00s over budget 10.00s (1.60x > 1.5x)" "fail-band package names its ratio against the 1.5x line"
run --check --local >"$out" 2>&1
assert_eq "$?" "0" "fail-band package: --check --local (warn-only) exits 0"
assert_has "$out" "warn-only run: would exit non-zero under --strict/CI (fail)" "local mode says what strict/CI would have done"

# ---- 4. per-test ceiling breach: independent of any package's ratio. ----
printf 'SUM\tpkgD\t1.0\nTEST\tpkgD\tTestSlow\t3.5\n' >"$measured"
run --check --strict >"$out" 2>&1
assert_eq "$?" "1" "per-test ceiling breach: --check --strict exits 1 even though pkgD is well under its sum budget"
assert_has "$out" "OK    pkgD: 1.00s (budget 100.00s)" "the ceiling breach does not change the package's own OK sum verdict"
assert_has "$out" "FAIL  pkgD: test TestSlow took 3.50s, over the 2.00s per-test ceiling" "the offending test is named with its duration and the ceiling"
run --check --local >"$out" 2>&1
assert_eq "$?" "0" "per-test ceiling breach: --check --local exits 0"

# ---- 5. package missing from budget: WARN, never fatal in either policy. ----
printf 'SUM\tpkgE\t5.0\n' >"$measured"
run --check --strict >"$out" 2>&1
assert_eq "$?" "0" "a package absent from a NON-empty budget is a warn, not a failure, even under --strict"
assert_has "$out" "WARN  pkgE: 5.00s, no budget entry (add one to testing-budget.json)" "the unbudgeted package names itself and what to do"

# ---- 6. budget file missing or empty: whole run is an explicit warn state, ----
#         --check ALWAYS exits 0 regardless of policy, even past a fail-band
#         ratio or a ceiling breach — the tree must stay green until a real
#         baseline lands (kata b6rv stop condition).
printf 'SUM\tpkgZ\t999.0\nTEST\tpkgZ\tTestHuge\t50.0\n' >"$measured"
printf '{}\n' >"$budget"
run --check --strict >"$out" 2>&1
assert_eq "$?" "0" "empty budget object: --check --strict still exits 0 past huge ratios and a ceiling breach"
assert_has "$out" "NO BASELINE" "an empty budget file is reported as an explicit no-baseline state"
rm -f "$budget"
run --check --strict >"$out" 2>&1
assert_eq "$?" "0" "a MISSING budget file behaves the same as an empty one: --check --strict exits 0"
assert_has "$out" "NO BASELINE" "a missing budget file is reported as no-baseline, not a setup error"
printf '{"packages": {}}\n' >"$budget"
run --check --strict >"$out" 2>&1
assert_eq "$?" "0" "a budget file with an explicitly empty packages map is also no-baseline"
assert_has "$out" "NO BASELINE" "an explicitly empty packages map is reported as no-baseline"

# ---- CI auto-detection: $CI alone (no --strict/--local) selects the policy. ----
printf 'SUM\tpkgC\t16.0\n' >"$measured"
printf '{"perTestCeilingSeconds": 2, "packages": {"pkgC": 10.0}}\n' >"$budget"
env TMPDIR="$work/tmp" CI=true bash "$script" --no-web --budget "$budget" --measured "$measured" --check >"$out" 2>&1
assert_eq "$?" "1" "CI=true with no --strict/--local flag is treated as strict"
env TMPDIR="$work/tmp" bash "$script" --no-web --budget "$budget" --measured "$measured" --check >"$out" 2>&1
assert_eq "$?" "0" "an unset CI with no --strict/--local flag is treated as warn-only (local)"
env TMPDIR="$work/tmp" CI=true bash "$script" --no-web --budget "$budget" --measured "$measured" --check --local >"$out" 2>&1
assert_eq "$?" "0" "--local overrides CI=true"

# ---- bare measurement (no --check): always exits 0, purely a report — even
#      with --strict passed, since --check is what turns enforcement on at all.
run --strict >"$out" 2>&1
assert_eq "$?" "0" "bare measurement with no --check never fails, even --strict at a fail-band ratio"
assert_has "$out" "FAIL  pkgC:" "bare measurement still PRINTS the fail-band line, it just does not enforce it"

# ---- --bless (make test-rebaseline): overwrites every measured package's ----
#      budget with the fresh sum, preserves the ceiling and packages this run
#      did not measure, and never touches per-test rows.
printf 'SUM\tpkgC\t7.25\nSUM\tpkgNew\t3.0\n' >"$measured"
printf '{"perTestCeilingSeconds": 4, "packages": {"pkgC": 10.0, "pkgStale": 1.0}}\n' >"$budget"
run --bless >"$out" 2>&1
assert_eq "$?" "0" "--bless exits 0"
assert_has "$budget" "\"pkgC\": 7.25" "bless overwrites a measured package with the fresh sum, not the max of old and new"
assert_has "$budget" "\"pkgNew\": 3.0" "bless adds a package this run measured for the first time"
assert_has "$budget" "\"perTestCeilingSeconds\": 4" "bless preserves an existing per-test ceiling"
assert_not_has "$budget" "pkgStale" "bless drops a package this run did not measure (a rebaseline reflects current reality, not history)"

# --bless seeds the default ceiling when the budget file had none.
printf 'SUM\tpkgOnly\t1.0\n' >"$measured"
printf '{"packages": {}}\n' >"$budget"
run --bless >"$out" 2>&1
assert_has "$budget" "\"perTestCeilingSeconds\": 2" "bless seeds the ~2s default ceiling when the file did not have one"

# ---- --measured missing file: reported as a setup failure, not a silent pass. ----
rm -f "$measured"
run --check --strict >"$out" 2>&1
assert_eq "$?" "1" "a missing --measured file is a hard failure"
assert_has "$out" "--measured file not found" "the missing-file diagnostic names the flag and the path"

# ---- a clean run leaves nothing behind under its private TMPDIR. ----
# Failing runs above retain their scratch on purpose (scratch-lib registers it,
# cleanup skips scratch_rm on failure so the operator keeps the logs), so give
# the successful run a fresh private TMPDIR of its own: the hygiene claim is
# about a SUCCESSFUL run only.
mkdir -p "$work/tmp-clean"
printf 'SUM\tpkgA\t1.0\n' >"$measured"
printf '{"packages": {"pkgA": 10.0}}\n' >"$budget"
TMPDIR="$work/tmp-clean" bash "$script" --no-web --budget "$budget" --measured "$measured" --check --strict >/dev/null 2>&1
assert_eq "$(ls -A "$work/tmp-clean")" "" "a clean run leaves no scratch directory behind"

help_out="$(bash "$script" --help 2>&1)"
if echo "$help_out" | grep -q "^Usage:" && ! echo "$help_out" | grep -q "^set -uo pipefail"; then
	ok "--help prints the whole header and stops at the script body"
else
	bad "--help is truncated or overran the header: $help_out"
fi

selftest_summary
