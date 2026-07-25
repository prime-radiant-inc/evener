#!/usr/bin/env bash
# test-cost.sh — rank a package's tests by what they actually cost.
#
# WHY THIS EXISTS: `go test -json`'s Elapsed is wall time, so under a parallel run
# it is mostly time the test spent on the runqueue, not working. The distortion is
# not small and it is not uniform:
#
#   TestJobReadOutputHeadLinesReadsFromStart   0.05s alone   2.08s in-suite   42x
#   TestRegressionIdleDelegatesNeverBlockSpawn 0.67s alone   9.87s in-suite   15x
#   TestDriveWakeDuringInflightDriveReDrives   1.07s alone   2.03s in-suite    2x
#
# The same agent suite "weighs" 99s at -parallel 6 and 451s at -parallel 32 —
# identical work. So a ranking taken from a parallel run does not tell you where
# the time goes; it tells you which tests lost the scheduler lottery. Acting on it
# means optimizing tests that are already fast (I did exactly that: spent a
# session on a family whose top test costs 0.67s, not the 9.87s it reported).
#
# This measures each test ALONE, one at a time, so the number is the test's own
# cost. That is slow by construction — it is a profiling tool, not a gate.
#
# USAGE:
#   scripts/test-cost.sh --dir agent [--top 40] [--run REGEX] [--min-ms 50]
#
#   --dir DIR       package directory to profile (required)
#   --run REGEX     only profile tests matching this (default: all Test*)
#   --top N         how many rows to print (default 40)
#   --min-ms MS     skip tests whose first-pass cost is under this, so the
#                   expensive part of the run is spent on tests that matter
#                   (default 0 = measure everything)
#   --reps N        repetitions per test, reporting the MINIMUM (default 1).
#                   The minimum is the honest floor: noise only ever adds.
#   --json FILE     also write the full ranking as JSON
#
# OUTPUT: a ranked table of isolated cost, plus the totals that matter — the sum
# of isolated costs (the suite's real work) against its observed wall time, which
# is what tells you whether there is headroom left to schedule or genuine work to
# remove.
set -uo pipefail

dir=""; runRe='^Test'; top=40; minMs=0; reps=1; jsonOut=""

die() { printf 'test-cost: %s\n' "$1" >&2; exit 2; }

while [ $# -gt 0 ]; do
	case "$1" in
		--dir) dir="${2:-}"; shift 2 ;;
		--run) runRe="${2:-}"; shift 2 ;;
		--top) top="${2:-}"; shift 2 ;;
		--min-ms) minMs="${2:-}"; shift 2 ;;
		--reps) reps="${2:-}"; shift 2 ;;
		--json) jsonOut="${2:-}"; shift 2 ;;
		-h|--help) sed -n '2,40p' "$0"; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done
[ -n "$dir" ] || die "--dir is required (see --help)"
[ -d "$dir" ] || die "no such directory: $dir"
[ -n "$jsonOut" ] && jsonOut="$(cd "$(dirname "$jsonOut")" && pwd)/$(basename "$jsonOut")"

cd "$dir" || die "cannot enter $dir"
work="$(mktemp -d -t test-cost.XXXXXX)"
bin="$work/pkg.test"

printf 'test-cost: building %s\n' "$dir" >&2
go test -c -o "$bin" . >"$work/build.log" 2>&1 || { cat "$work/build.log"; die "build failed"; }

# One parallel pass gives the test list and a cheap first-pass cost, used only to
# skip trivia via --min-ms. Its durations are NOT reported: that is the distortion
# this tool exists to correct.
printf 'test-cost: listing tests\n' >&2
"$bin" -test.short -test.count=1 -test.parallel 6 -test.run "$runRe" -test.v >"$work/survey.log" 2>&1

python3 - "$work" "$bin" "$runRe" "$top" "$minMs" "$reps" "$jsonOut" <<'PY'
import json, re, subprocess, sys

work, binPath, runRe, top, minMs, reps, jsonOut = sys.argv[1:8]
top, minMs, reps = int(top), float(minMs), int(reps)

survey = {}
for line in open(f'{work}/survey.log', errors='replace'):
    m = re.match(r'--- (?:PASS|FAIL): (\S+) \(([0-9.]+)s\)', line.strip())
    if m and '/' not in m.group(1):
        survey[m.group(1)] = float(m.group(2))
if not survey:
    print('test-cost: no tests found', file=sys.stderr)
    sys.exit(1)

candidates = [n for n, t in survey.items() if t * 1000 >= minMs]
print(f'test-cost: measuring {len(candidates)} of {len(survey)} tests in isolation'
      f'{"" if reps == 1 else f", {reps} reps each"}', file=sys.stderr)

def isolatedCost(name):
    """Run one test alone and return its own reported duration."""
    best = None
    for _ in range(reps):
        out = subprocess.run(
            [binPath, '-test.short', '-test.count=1', '-test.parallel', '1',
             '-test.run', f'^{re.escape(name)}$', '-test.v'],
            capture_output=True, text=True, errors='replace').stdout
        m = re.search(rf'--- (?:PASS|FAIL): {re.escape(name)} \(([0-9.]+)s\)', out)
        if m:
            v = float(m.group(1))
            best = v if best is None else min(best, v)
    return best

costs = {}
for i, name in enumerate(sorted(candidates), 1):
    if i % 100 == 0:
        print(f'  {i}/{len(candidates)}', file=sys.stderr)
    c = isolatedCost(name)
    if c is not None:
        costs[name] = c

ranked = sorted(costs.items(), key=lambda kv: -kv[1])
isolatedTotal = sum(costs.values())
surveyTotal = sum(survey[n] for n in costs)

print()
print(f'{"isolated":>9}  {"in-suite":>9}  {"stretch":>7}  test')
for name, c in ranked[:top]:
    s = survey.get(name, 0.0)
    stretch = f'{s / c:.1f}x' if c > 0 else '-'
    print(f'{c:8.3f}s  {s:8.3f}s  {stretch:>7}  {name}')

print()
print(f'isolated total (real work) : {isolatedTotal:8.1f}s across {len(costs)} tests')
print(f'in-suite total (as reported): {surveyTotal:8.1f}s'
      f'  -> inflated {surveyTotal / isolatedTotal:.1f}x' if isolatedTotal else '')
print()
print('Read the stretch column, not the in-suite column. A high stretch means the')
print('test is STARVED, not slow — fix that by scheduling, never by rewriting it.')
print('Only the isolated column justifies changing a test.')

if jsonOut:
    with open(jsonOut, 'w') as fh:
        json.dump([{'test': n, 'isolated_s': c, 'in_suite_s': survey.get(n, 0.0)}
                   for n, c in ranked], fh, indent=1)
    print(f'\nfull ranking: {jsonOut}')
PY
