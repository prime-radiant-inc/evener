#!/usr/bin/env bash
# test-timing-budget-selftest.sh — exercises scripts/gate/test-timing-budget.sh's
# comparison contract against fixture "already-measured" duration rows and
# fixture testing-budget.json files, via --measured (test-timing-budget.sh's
# own reuse-shaped seam — see coverage-floor.sh's web row for the same
# idea), plus its measurement contract against a stub `go` binary (the
# web-preflight selftest's stub pattern). No real go test, no vitest, no
# network: every check here is about the ratio/ceiling/missing-entry/
# no-baseline arithmetic, the strict-vs-warn-only exit policy, and the
# producer's own failure handling, not about running a real suite. Run via
# `make test-timing-budget-selftest` or the dev-tooling wave.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/test-timing-budget.sh"
. "$(dirname "$0")/../lib/selftest-lib.sh"

trap 'scratch_rm' EXIT
scratch_dir work evener-testbudget-selftest

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
env -u CI TMPDIR="$work/tmp" bash "$script" --no-web --budget "$budget" --measured "$measured" --check >"$out" 2>&1
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

help_header_ok() {
	local help=$1
	[[ "$help" == Usage:* || "$help" == *$'\nUsage:'* ]] &&
		[[ "$help" != "set -uo pipefail"* && "$help" != *$'\nset -uo pipefail'* ]]
}

help_out="$(bash "$script" --help 2>&1)"
if help_header_ok "$help_out"; then
	ok "--help prints the whole header and stops at the script body"
else
	bad "--help is truncated or overran the header: $help_out"
fi

large_help_out=$'Usage: test-timing-budget.sh [options]\n'"$(printf 'valid help documentation\n%.0s' {1..4096})"
large_help_status=0
help_header_ok "$large_help_out" || large_help_status=$?
assert_eq "$large_help_status" "0" "a large valid help stream is accepted without SIGPIPE status 141"

malformed_help_out=$'Usage: test-timing-budget.sh [options]\nset -uo pipefail\n'
malformed_help_status=0
help_header_ok "$malformed_help_out" || malformed_help_status=$?
assert_eq "$malformed_help_status" "1" "help containing the script body marker is rejected"

missing_usage_help_out=$'test-timing-budget.sh [options]\n'
missing_usage_help_status=0
help_header_ok "$missing_usage_help_out" || missing_usage_help_status=$?
assert_eq "$missing_usage_help_status" "1" "help without a Usage header is rejected"

# ---- measurement contract, against a real toolchain on fixture modules. ----
#      The producer side (exit status capture + the package-completeness
#      oracle) needs a real `go` to build and run, so the runner is pointed
#      at a tiny fixture repo via --repo-root: one healthy module and, per
#      scenario, a module that breaks in exactly the way under test. Real
#      `go test -json` streams, real exit statuses, no faked binaries on
#      PATH (fake-toolchain selftests are banned by
#      docs/developing-evener/testing.md).
fixture_repo="$work/repo"
mkdir -p "$fixture_repo/healthymod/pkgwithtests" "$fixture_repo/healthymod/pkgnotests"

# The healthy module: one package with a trivial passing test, one package
# with no test files at all — the terminal-event shapes the oracle relies on.
printf 'module fixture.test/healthy\n\ngo 1.27\n' >"$fixture_repo/healthymod/go.mod"
printf 'package pkgwithtests\n\nimport "testing"\n\nfunc TestFixture(t *testing.T) {}\n' \
	>"$fixture_repo/healthymod/pkgwithtests/a_test.go"
printf 'package pkgnotests\n\nfunc Helper() {}\n' >"$fixture_repo/healthymod/pkgnotests/n.go"

# make_broken_mod SHAPE — install a second module under $fixture_repo that
# breaks in exactly the way under test:
#   nobuild — a test file that does not compile: `go test` exits nonzero and
#             its package contributes no rows (issue #172's silent-drop:
#             absent the fix, --bless writes a budget without the package and
#             exits 0).
#   failing — a test that fails at runtime: complete stream, `go test` exits
#             nonzero, and the runner must refuse the measurement rather
#             than read plausible rows as one.
make_broken_mod() {
	rm -rf "$fixture_repo/brokenmod"
	mkdir -p "$fixture_repo/brokenmod/pkgbroken"
	printf 'module fixture.test/broken\n\ngo 1.27\n' >"$fixture_repo/brokenmod/go.mod"
	case "$1" in
		nobuild)
			printf 'package pkgbroken\n\nthis does not compile\n' \
				>"$fixture_repo/brokenmod/pkgbroken/b_test.go"
			;;
		failing)
			printf 'package pkgbroken\n\nimport "testing"\n\nfunc TestFixture(t *testing.T) { t.Fatal("boom") }\n' \
				>"$fixture_repo/brokenmod/pkgbroken/b_test.go"
			;;
		*) bad "make_broken_mod: unknown shape $1" ;;
	esac
}

# run_measure ARGS... — invoke the runner against the fixture repo's modules.
# --repo-root points its module discovery at the fixture; --budget keeps the
# budget file in the selftest's scratch.
run_measure() {
	TMPDIR="$work/tmp" bash "$script" \
		--no-web --repo-root "$fixture_repo" --budget "$budget" \
		--modules 'healthymod brokenmod' "$@" \
		>"$out" 2>&1
}

# ---- 7. the healthy shape still measures and blesses every listed package. ----
rm -rf "$fixture_repo/brokenmod"
printf '{"perTestCeilingSeconds": 2, "packages": {}}\n' >"$budget"
run_measure --bless
assert_eq "$?" "0" "a healthy go test stream blesses cleanly (no silent package loss)"
assert_has "$budget" '"fixture.test/healthy/pkgwithtests"' "the package with tests lands in the blessed budget"
assert_has "$budget" '"fixture.test/healthy/pkgnotests"' "a package with no test files still lands in the blessed budget"

# ---- 8. a package go list reported that the stream never mentions is a ----
#         hard measurement failure, never a silent drop (#172): a module whose
#         test file does not compile makes go test exit nonzero AND its
#         packages contribute no rows — the run must fail, and --bless must
#         leave the budget untouched rather than quietly delete the module.
make_broken_mod nobuild
printf '{"perTestCeilingSeconds": 2, "packages": {"fixture.test/healthy/pkgwithtests": 9.0}}\n' >"$budget"
run_measure --bless
assert_eq "$?" "1" "a package that failed to build fails the run, not blesses around it"
assert_has "$out" "brokenmod" "the failing module is named in the diagnostic"
assert_has "$budget" '"fixture.test/healthy/pkgwithtests": 9.0' "a refused bless leaves the budget file untouched"

# ---- 9. a nonzero go test exit status is a failure, not an empty measurement ----
#         — even after rows were parsed and look plausible (#172).
make_broken_mod failing
printf '{"perTestCeilingSeconds": 2, "packages": {"fixture.test/healthy/pkgwithtests": 9.0}}\n' >"$budget"
run_measure --bless
assert_eq "$?" "1" "a nonzero go test exit fails the run even when the healthy module's rows were parsed"
assert_has "$out" "go test in brokenmod" "the failed producer's module is named in the diagnostic"
assert_has "$budget" '"fixture.test/healthy/pkgwithtests": 9.0' "a refused bless leaves the budget file untouched after a failed producer"

# A failed producer must not read as a clean report: --check exits nonzero
# even in the warn-only policy, because there is nothing trustworthy to
# compare against a failed measurement.
make_broken_mod failing
run_measure --check --local
assert_eq "$?" "1" "a failed producer fails --check even in warn-only mode: no verdict is better than a wrong one"

selftest_summary
