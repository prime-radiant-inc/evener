#!/usr/bin/env bash
# test-overlap.sh — find expensive tests that cover almost nothing new.
#
# WHY: a suite grows by accretion, and nothing in `go test` tells you that a test
# you just paid a second for walked the same code as the test before it. Coverage
# percentage cannot tell you either — it is a union, so a redundant test never
# lowers it and never shows up.
#
# This measures, per test, its own coverage set and how much of that set is
# UNIQUE to it once the cheaper tests are accounted for. A test with a high cost
# and near-zero unique contribution is a candidate to fold into a table case, keep
# as a cheap unit test, or delete. Observed in this repo:
#
#   TestCreateDelegateForegroundTimeoutLeavesChildRunning  1.09s  1420 blocks
#   TestDriveWakeDuringInflightDriveReDrives               1.06s  1610 blocks
#   -> they share 1386 blocks: 98% of the first, 86% of the second
#
# WHAT IT DOES NOT MEAN: shared coverage is not proof of redundancy. Two tests can
# execute identical lines and assert different things — one checks a timeout leaves
# the child running, the other checks a re-drive happens. Coverage is a shape, not
# a contract. So this tool RANKS SUSPECTS; a human decides, by reading the
# assertions, whether the second test earns its cost.
#
# Deleting a test because this script flagged it, without reading what it asserts,
# is how you lose a regression guard while the coverage number stays flat.
#
# USAGE:
#   scripts/test-overlap.sh --dir agent [--min-ms 300] [--top 30]
#
#   --dir DIR      package directory (required)
#   --min-ms MS    only profile tests at least this expensive (default 300);
#                  cheap tests are not worth folding even when redundant
#   --top N        rows to print (default 30)
#   --run REGEX    restrict to matching tests
#
# Cost: one coverage run per candidate test, serially. Minutes, not seconds. This
# is an analysis tool, not a gate.
set -uo pipefail

dir=""; minMs=300; top=30; runRe='^Test'

die() { printf 'test-overlap: %s\n' "$1" >&2; exit 2; }

while [ $# -gt 0 ]; do
	case "$1" in
		--dir) dir="${2:-}"; shift 2 ;;
		--min-ms) minMs="${2:-}"; shift 2 ;;
		--top) top="${2:-}"; shift 2 ;;
		--run) runRe="${2:-}"; shift 2 ;;
		-h|--help) sed -n '2,40p' "$0"; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done
[ -n "$dir" ] || die "--dir is required (see --help)"
[ -d "$dir" ] || die "no such directory: $dir"

cd "$dir" || die "cannot enter $dir"
work="$(mktemp -d -t test-overlap.XXXXXX)"

printf 'test-overlap: surveying to find tests over %sms\n' "$minMs" >&2
go test -count=1 -short -parallel 6 -run "$runRe" -v . >"$work/survey.log" 2>&1

python3 - "$work" "$minMs" "$top" "$runRe" <<'PY'
import os, re, subprocess, sys

work, minMs, top, runRe = sys.argv[1], float(sys.argv[2]), int(sys.argv[3]), sys.argv[4]

survey = {}
for line in open(f'{work}/survey.log', errors='replace'):
    m = re.match(r'--- PASS: (\S+) \(([0-9.]+)s\)', line.strip())
    if m and '/' not in m.group(1):
        survey[m.group(1)] = float(m.group(2))

candidates = sorted((n for n, t in survey.items() if t * 1000 >= minMs),
                    key=lambda n: -survey[n])
if not candidates:
    print(f'test-overlap: no tests at or above {minMs:.0f}ms', file=sys.stderr)
    sys.exit(1)
print(f'test-overlap: profiling coverage for {len(candidates)} tests', file=sys.stderr)

def coverageBlocks(name):
    """The set of covered blocks for one test, run alone."""
    prof = f'{work}/{name}.cov'
    subprocess.run(['go', 'test', '-count=1', '-short', '-covermode=set',
                    f'-coverprofile={prof}', '-run', f'^{re.escape(name)}$', '.'],
                   capture_output=True, text=True)
    if not os.path.exists(prof):
        return set()
    blocks = set()
    for line in open(prof).read().splitlines()[1:]:
        loc, _, count = line.rsplit(' ', 2)
        if int(count) > 0:
            blocks.add(loc)
    return blocks

cov = {}
for i, name in enumerate(candidates, 1):
    if i % 25 == 0:
        print(f'  {i}/{len(candidates)}', file=sys.stderr)
    b = coverageBlocks(name)
    if b:
        cov[name] = b

# Marginal contribution, cheapest-first: a test's unique blocks are those no
# CHEAPER test already covers. Ordering by cost this way asks the question that
# matters — "does this expensive test earn its cost over what we already run?" —
# rather than rewarding whichever test happened to run first.
byCost = sorted(cov, key=lambda n: survey[n])
seen = set()
rows = []
for name in byCost:
    unique = cov[name] - seen
    rows.append((name, survey[name], len(cov[name]), len(unique)))
    seen |= cov[name]

rows.sort(key=lambda r: -(r[1] / max(r[3], 1)))

print()
print(f'{"cost":>7} {"blocks":>7} {"unique":>7} {"ms/unique":>10}  test')
for name, cost, total, uniq in rows[:top]:
    per = (cost * 1000 / uniq) if uniq else float('inf')
    perStr = f'{per:10.1f}' if uniq else '         -'
    print(f'{cost:6.2f}s {total:7} {uniq:7} {perStr}  {name}')

print()
zero = [r for r in rows if r[3] == 0]
print(f'{len(zero)} tests add NO blocks a cheaper test does not already cover, '
      f'costing {sum(r[1] for r in zero):.1f}s together.')
print()
print('ms/unique ranks suspects: high means much time for little new code reached.')
print('Coverage is a shape, not a contract — two tests can walk identical lines and')
print('assert different things. Read the assertions before touching anything.')
PY
