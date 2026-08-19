#!/usr/bin/env bash
# covstmt-lib.sh — the one definition of how this repo counts statements in a Go
# coverage profile, shared by every script that reports a coverage number.
#
# Sourced, never executed. Deliberately pure declaration: it defines one function
# and does nothing else, so it is safe for the dev-tooling wave's leak check.
#
# Two properties matter and are easy to get subtly different in a second copy:
#
#   - Blocks are deduped BY POSITION. A -coverpkg run emits the same block once
#     per test binary, so summing raw lines multiplies the denominator.
#   - A block counts as covered if ANY occurrence hit it. That is what makes it
#     valid to concatenate several profiles — the per-binary duplicates of one
#     run, or the test-track and fuzz-track profiles of the same package — and
#     count the union by reading the concatenation.

# stmt_counts PROFILE — prints "covered total".
stmt_counts() {
	python3 - "$1" <<'PY'
import re, sys
seen = {}
for l in open(sys.argv[1]):
	m = re.match(r'^(.+?):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$', l)
	if not m:
		continue
	f, sl, sc, el, ec, ns, cnt = m.groups()
	key = (f, sl, sc, el, ec)
	seen[key] = (int(ns), seen.get(key, (0, False))[1] or int(cnt) > 0)
tot = sum(n for n, _ in seen.values())
cov = sum(n for n, c in seen.values() if c)
print(cov, tot)
PY
}
