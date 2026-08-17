#!/usr/bin/env bash
# coverage-gaps.sh — rank where a Go coverage profile's UNCOVERED statements are,
# so coverage work targets the largest real gaps instead of whatever file is open.
#
# Ranking by uncovered COUNT, not by percentage, is the point: a 40%-covered file
# holding 12 statements is noise next to a 90%-covered one holding 900. Percentage
# ranking sends you to the former every time.
#
# Usage:
#   scripts/coverage-gaps.sh PROFILE                  # top packages by uncovered stmts
#   scripts/coverage-gaps.sh PROFILE --by file        # ...by file
#   scripts/coverage-gaps.sh PROFILE --top 40
#   scripts/coverage-gaps.sh PROFILE --by file --zero # only wholly-uncovered units
#   scripts/coverage-gaps.sh PROFILE --in session.go  # uncovered blocks IN a file
#
# PROFILE is a `go test -coverprofile` file. Generate one with, e.g.
#   prof="$(mktemp "${TMPDIR:-/tmp}/serf-cov.XXXXXX")"
#   go test -count=1 -short -coverpkg="$(go list ./... | paste -sd, -)" \
#     -coverprofile="$prof" -run "$GATE_TEST_RUN" -skip "$GATE_FUZZ_TEST_SKIP" ./...
#   scripts/coverage-gaps.sh "$prof"
# (the same selection and module scoping scripts/test-coverage-floor.sh
# measures; see scripts/gate-surface-lib.sh).
#
# Duplicate blocks from -coverpkg are deduped by position, a block counting as
# covered if ANY test hit it — the same accounting test-coverage-floor.sh and
# fuzz-coverage-global.sh use, so the totals here reconcile with the floors.
set -uo pipefail

profile=""
by="package"
top="25"
zero_only=false
in_pattern=""
while [ $# -gt 0 ]; do
	case "$1" in
		--by) by="$2"; shift 2 ;;
		--top) top="$2"; shift 2 ;;
		--zero) zero_only=true; shift ;;
		--in) in_pattern="$2"; shift 2 ;;
		-h|--help) awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
		-*) echo "unknown flag: $1" >&2; exit 2 ;;
		*) profile="$1"; shift ;;
	esac
done

[ -n "$profile" ] || { echo "usage: coverage-gaps.sh PROFILE [--by file|package] [--top N] [--zero]" >&2; exit 2; }
[ -f "$profile" ] || { echo "no such profile: $profile" >&2; exit 1; }
case "$by" in package|file) ;; *) echo "--by must be package or file (got $by)" >&2; exit 2 ;; esac

python3 - "$profile" "$by" "$top" "$zero_only" "$in_pattern" <<'PY'
import re, sys
profile, by, top, zero_only = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4] == "true"
in_pattern = sys.argv[5]

# key -> (numstmts, covered) per unique block position, so -coverpkg duplicates
# collapse the same way the floor scripts collapse them.
blocks = {}
line_re = re.compile(r'^(.+?):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$')
for line in open(profile):
    m = line_re.match(line.strip())
    if not m:
        continue
    f, sl, sc, el, ec, ns, cnt = m.groups()
    key = (f, sl, sc, el, ec)
    prev = blocks.get(key, (0, False))
    blocks[key] = (int(ns), prev[1] or int(cnt) > 0)

# --in lists the uncovered BLOCKS inside matching files, biggest first, so a
# file with a known gap turns straight into a list of line ranges to go read.
# Aggregates say which file to work on; this says where in it.
if in_pattern:
    rows = []
    for (f, sl, sc, el, ec), (ns, covered) in blocks.items():
        if covered or in_pattern not in f:
            continue
        rows.append((ns, f, int(sl), int(el)))
    rows.sort(reverse=True)
    if not rows:
        print("no uncovered blocks in files matching %r" % in_pattern)
        sys.exit(0)
    print("%8s  %s" % ("STMTS", "location"))
    for ns, f, sl, el in rows[:top]:
        print("%8d  %s:%d-%d" % (ns, f, sl, el))
    print()
    print("showing %d of %d uncovered blocks (%d statements) in files matching %r"
          % (min(len(rows), top), len(rows), sum(r[0] for r in rows), in_pattern))
    sys.exit(0)

units = {}
for (f, *_), (ns, covered) in blocks.items():
    name = f if by == "file" else f.rsplit("/", 1)[0]
    cov, tot = units.get(name, (0, 0))
    units[name] = (cov + (ns if covered else 0), tot + ns)

rows = []
for name, (cov, tot) in units.items():
    missing = tot - cov
    if missing == 0:
        continue
    if zero_only and cov != 0:
        continue
    rows.append((missing, tot, cov, name))
rows.sort(reverse=True)

grand_missing = sum(t - c for c, t in ((c, t) for c, t in units.values()))
grand_total = sum(t for _, t in units.values())
print("%8s %8s %8s  %s" % ("MISSING", "total", "cov%", by))
for missing, tot, cov, name in rows[:top]:
    print("%8d %8d %7.1f%%  %s" % (missing, tot, 100.0 * cov / tot if tot else 0.0, name))
shown = min(len(rows), top)
print()
print("showing %d of %d %ss with gaps; %d uncovered of %d statements overall (%.1f%%)"
      % (shown, len(rows), by, grand_missing, grand_total,
         100.0 * (grand_total - grand_missing) / grand_total if grand_total else 0.0))
PY
