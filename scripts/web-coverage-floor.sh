#!/usr/bin/env bash
# web-coverage-floor.sh — a no-regression ratchet on the frontend's line coverage,
# the web counterpart to scripts/test-coverage-floor.sh (Go full-suite) and
# scripts/fuzz-coverage-global.sh (Go fuzz-reachable). It guards the coverage the
# vitest suite drives, so a PR that deletes tests or adds untested UI fails the
# gate instead of silently eroding the number.
#
# It measures with `npm run test:coverage` (vitest + the v8 provider) and rolls
# the per-file report up per source AREA — the top-level directories under src/,
# plus "(root)" for the files directly in src/ and "total" for the whole app.
# Per-area is the point: one whole-app number lets a well-tested store subsidise
# an untested pane, which is exactly the erosion this gate exists to catch.
#
# The vitest config names the whole source tree in coverage.include, so a file no
# test ever loads counts as 0% instead of vanishing from the report.
#
# Usage:
#   scripts/web-coverage-floor.sh                  # measure + print
#   scripts/web-coverage-floor.sh --check          # ratchet: exit non-zero on a drop
#   scripts/web-coverage-floor.sh --bless          # raise floors to current %
#   scripts/web-coverage-floor.sh --reuse          # parse the existing report, no re-run
#   scripts/web-coverage-floor.sh --tolerance 0.5  # wobble band (default 0.5pp)
#
# Floors live in scripts/webcov-floors.txt ("<area> <pct>" per line). Raised
# upward only by --bless; a deliberate downward reset (a denominator change, not
# a coverage regression) is a hand edit with a comment, exactly like the two Go
# floor files. --bless always rewrites EVERY area, so unlike the Go scripts there
# is no partial-bless footgun to undo afterward.
#
# SERF_WEB_FRONTEND_DIR points this at a throwaway frontend instead of the real
# one, which is how scripts/web-coverage-floor-selftest.sh exercises it.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
floors_file="${SERF_WEBCOV_FLOORS:-$repo_root/scripts/webcov-floors.txt}"
frontend="${SERF_WEB_FRONTEND_DIR:-$repo_root/cmd/serf-hub/frontend}"
tolerance="0.5"
check=false
bless=false
reuse=false
while [ $# -gt 0 ]; do
	case "$1" in
		--tolerance) tolerance="$2"; shift 2 ;;
		--check) check=true; shift ;;
		--bless) bless=true; shift ;;
		--reuse) reuse=true; shift ;;
		# Scan for the header's end rather than hardcoding a line range, which
		# goes stale silently the first time the header changes length.
		-h|--help) awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

summary="$frontend/coverage/coverage-summary.json"
floor_for() { awk -v a="$1" '$1==a {print $2}' "$floors_file" 2>/dev/null; }

if ! $reuse; then
	if ! ( cd "$frontend" && npm run test:coverage ) >/dev/null 2>&1; then
		echo "web coverage: SUITE FAILED (see: cd $frontend && npm run test:coverage)" >&2
		# reportOnFailure keeps a usable report on a red suite, so fall through and
		# still report the numbers rather than losing the whole measurement.
		[ -f "$summary" ] || exit 1
	fi
fi

[ -f "$summary" ] || { echo "no coverage summary at $summary (drop --reuse to measure)" >&2; exit 1; }

# area_rollup prints "<area> <covered> <total> <pct>" per area, then "total ...".
# Areas come from the path segment under src/; files sitting directly in src/
# roll up as "(root)" so nothing in the report is silently unattributed.
area_rollup() {
	python3 - "$1" "$frontend" <<'PY'
import json, os, sys
report, frontend = sys.argv[1], os.path.realpath(sys.argv[2])
data = json.load(open(report))
src = os.path.join(frontend, "src") + os.sep
areas = {}
for path, m in data.items():
	if path == "total":
		continue
	full = os.path.realpath(path)
	if not full.startswith(src):
		continue
	rest = full[len(src):]
	head, sep, _ = rest.partition(os.sep)
	area = head if sep else "(root)"
	cov, tot = areas.get(area, (0, 0))
	areas[area] = (cov + m["lines"]["covered"], tot + m["lines"]["total"])
grand_c = grand_t = 0
for area in sorted(areas):
	c, t = areas[area]
	grand_c += c
	grand_t += t
	print(area, c, t, "%.1f" % (100.0 * c / t) if t else "0.0")
print("total", grand_c, grand_t, "%.1f" % (100.0 * grand_c / grand_t) if grand_t else "0.0")
PY
}

rollup_file="$(mktemp -t serf-webcov.XXXXXX)"
area_rollup "$summary" >"$rollup_file" || { echo "failed to parse $summary" >&2; exit 1; }
[ -s "$rollup_file" ] || { echo "coverage summary named no files under src/ — check coverage.include" >&2; exit 1; }

fail=0
printf '%-14s %12s %12s %8s %8s\n' "area" "covered" "total" "cov%" "floor"
while read -r area c t pct; do
	floor="$(floor_for "$area")"
	printf '%-14s %12d %12d %7s%% %7s\n' "$area" "$c" "$t" "$pct" "${floor:-—}"
	if $check && [ -n "$floor" ]; then
		if [ "$(awk -v p="$pct" -v f="$floor" -v tol="$tolerance" 'BEGIN{print (p < f - tol) ? 1 : 0}')" = "1" ]; then
			echo "    REGRESSION: $area web coverage ${pct}% < floor ${floor}% (tolerance ${tolerance}pp)" >&2
			fail=1
		fi
	fi
done <"$rollup_file"

if $bless; then
	tmp="$(mktemp)"
	{
		echo "# Frontend (vitest) per-area LINE coverage floors."
		echo "# Managed by scripts/web-coverage-floor.sh --bless. Raised upward only;"
		echo "# a downward reset (denominator change, not a regression) is a hand edit."
		while read -r area c t pct; do
			old="$(floor_for "$area")"; keep="$pct"
			[ -n "$old" ] && keep="$(awk -v a="$old" -v b="$pct" 'BEGIN{print (a>b)?a:b}')"
			echo "$area $keep"
		done <"$rollup_file"
	} >"$tmp"
	mv "$tmp" "$floors_file"
	echo "blessed floors -> $floors_file"
fi

rm -f "$rollup_file"
exit $fail
