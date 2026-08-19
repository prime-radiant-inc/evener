#!/usr/bin/env bash
# coverage-gaps-selftest.sh — exercises scripts/coverage-gaps.sh against a
# synthetic coverage profile with hand-computed answers. No go test, no
# compilation: the arithmetic (dedup, ranking, totals) is the whole contract.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/coverage-gaps.sh"
. "$(dirname "$0")/selftest-lib.sh"

scratch_dir work evener-covgaps-selftest
trap 'scratch_rm' EXIT

profile="$work/p.cov"
# pkg/a: 10 covered + 5 missing.
# pkg/b: the SAME block position twice, hit once — dedup must count it once and
#        treat it as covered, leaving pkg/b with no gap at all.
# pkg/c: wholly uncovered.
# pkg/d: the largest gap, and deliberately NOT the lowest percentage, so ranking
#        by uncovered count rather than by percentage is what the order proves.
cat >"$profile" <<'PROF'
mode: set
pkg/a/f1.go:1.1,2.1 10 1
pkg/a/f2.go:3.1,4.1 5 0
pkg/b/g1.go:1.1,2.1 20 0
pkg/b/g1.go:1.1,2.1 20 1
pkg/c/h1.go:1.1,2.1 3 0
pkg/d/i1.go:1.1,2.1 50 1
pkg/d/i2.go:5.1,6.1 100 0
PROF

out="$work/out.txt"
bash "$script" "$profile" >"$out" 2>&1
assert_eq "$?" "0" "a well-formed profile exits zero"

if grep -qE '^ *100 +150 +33\.3% +pkg/d' "$out"; then
	ok "pkg/d rolls up as 100 missing of 150 (33.3%)"
else
	bad "pkg/d row wrong"; sed 's/^/    | /' "$out"
fi
if grep -qE '^ *5 +15 +66\.7% +pkg/a' "$out"; then
	ok "pkg/a rolls up as 5 missing of 15 (66.7%)"
else
	bad "pkg/a row wrong"; sed 's/^/    | /' "$out"
fi
assert_not_has "$out" "pkg/b" "a duplicate block position is deduped and counted covered"

# Ranking is by uncovered COUNT: pkg/d (100 missing, 33.3%) must outrank pkg/c
# (3 missing, 0.0%) even though pkg/c has the worse percentage.
if [ "$(grep -n "pkg/d" "$out" | cut -d: -f1)" -lt "$(grep -n "pkg/c" "$out" | cut -d: -f1)" ]; then
	ok "ranking is by uncovered count, not by percentage"
else
	bad "pkg/c outranked pkg/d, so the sort is by percentage"; sed 's/^/    | /' "$out"
fi

# Totals: 80 covered of 188, so 108 uncovered and 42.6%.
assert_has "$out" "108 uncovered of 188 statements overall (42.6%)" "the totals line reconciles"

bash "$script" "$profile" --zero >"$out" 2>&1
assert_has "$out" "pkg/c" "--zero keeps a wholly uncovered package"
assert_not_has "$out" "pkg/d" "--zero drops a partly covered package"

bash "$script" "$profile" --by file >"$out" 2>&1
assert_has "$out" "pkg/d/i2.go" "--by file rolls up per file"
assert_not_has "$out" "pkg/d/i1.go" "--by file omits a file with no gap"

bash "$script" "$profile" --top 1 >"$out" 2>&1
assert_has "$out" "showing 1 of 3 packages" "--top limits the rows and says so"

# --in turns a known-bad file into the line ranges to go read.
bash "$script" "$profile" --in "pkg/d" >"$out" 2>&1
assert_has "$out" "pkg/d/i2.go:5-6" "--in reports an uncovered block's line range"
assert_not_has "$out" "i1.go" "--in omits covered blocks"
assert_has "$out" "100 statements" "--in totals the uncovered statements it found"
bash "$script" "$profile" --in "pkg/b" >"$out" 2>&1
assert_has "$out" "no uncovered blocks" "--in says so when a file has no gap"

bash "$script" "$work/nope.cov" >"$out" 2>&1
assert_eq "$?" "1" "a missing profile exits non-zero"
assert_has "$out" "no such profile" "the missing profile is named"

bash "$script" "$profile" --by bogus >"$out" 2>&1
assert_eq "$?" "2" "an unknown --by value is a usage error"

help_out="$(bash "$script" --help 2>&1)"
if echo "$help_out" | grep -q "^Usage:" && ! echo "$help_out" | grep -q "^set -uo pipefail"; then
	ok "--help prints the whole header and stops at the script body"
else
	bad "--help is truncated or overran the header: $help_out"
fi

selftest_summary
