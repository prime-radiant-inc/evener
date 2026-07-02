#!/usr/bin/env bash
# test-coverage-floor.sh — a no-regression ratchet on whole-module FULL-SUITE
# (unit+integration) statement coverage. The companion to fuzz-coverage-global.sh
# (which ratchets fuzz-REACHABLE coverage): this one guards the coverage the whole
# `go test` suite drives, so a PR that deletes tests or adds untested code fails
# the gate instead of silently eroding the numbers the coverage campaign won.
#
# It measures each module with `go test -count=1 -coverpkg=./... ./...` (the
# -count=1 is REQUIRED — cached coverage profiles report stale numbers) and dedups
# the -coverpkg duplicate blocks by position (a block is covered if ANY test hit
# it) before computing the percentage, the same way fuzz-coverage-global.sh does.
#
# Usage:
#   scripts/test-coverage-floor.sh                     # measure + print (all modules)
#   scripts/test-coverage-floor.sh --check             # ratchet: exit non-zero on a drop
#   scripts/test-coverage-floor.sh --bless             # raise floors to current %
#   scripts/test-coverage-floor.sh --modules "agent llm"
#   scripts/test-coverage-floor.sh --tolerance 0.5     # wobble band (default 0.5pp)
#
# Floors live in scripts/testcov-global-floors.txt ("<module> <pct>" per line).
# Raised upward only by --bless; a deliberate downward reset (a denominator change,
# not a coverage regression) is a hand edit with a comment, exactly like the fuzz
# floor file. Local/CI gate — heavier than `make test` (whole-module -coverpkg).
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
floors_file="$repo_root/scripts/testcov-global-floors.txt"
modules=". agent llm auth envvars"
tolerance="0.5"
check=false
bless=false
while [ $# -gt 0 ]; do
	case "$1" in
		--modules) modules="$2"; shift 2 ;;
		--tolerance) tolerance="$2"; shift 2 ;;
		--check) check=true; shift ;;
		--bless) bless=true; shift ;;
		-h|--help) sed -n '2,26p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

floor_for() { awk -v m="$1" '$1==m {print $2}' "$floors_file" 2>/dev/null; }

# stmt_counts dedups -coverpkg duplicate blocks by position and prints "covered total".
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

profiles_dir="$(mktemp -d -t serf-testcov.XXXXXX)"
fail=0
declare -A measured
printf '%-10s %12s %12s %8s %8s\n' "module" "covered" "total" "cov%" "floor"
for m in $modules; do
	[ -f "$repo_root/$m/go.mod" ] || { printf '%-10s %s\n' "$m" "(no module)"; continue; }
	prof="$profiles_dir/${m//\//_}.cov"
	if ! ( cd "$repo_root/$m" && go test -count=1 -coverpkg=./... -coverprofile="$prof" ./... ) >/dev/null 2>&1; then
		printf '%-10s %s\n' "$m" "TEST FAILED (see: cd $m && go test -coverpkg=./... ./...)"
		fail=1; continue
	fi
	[ -f "$prof" ] || { printf '%-10s %s\n' "$m" "no profile"; continue; }
	read -r c t < <(stmt_counts "$prof")
	[ "${t:-0}" -gt 0 ] || { printf '%-10s %s\n' "$m" "no statements"; continue; }
	pct="$(awk -v c="$c" -v t="$t" 'BEGIN{printf "%.1f", 100*c/t}')"
	measured["$m"]="$pct"
	floor="$(floor_for "$m")"
	printf '%-10s %12d %12d %7s%% %7s\n' "$m" "$c" "$t" "$pct" "${floor:-—}"
	if $check && [ -n "$floor" ]; then
		if [ "$(awk -v p="$pct" -v f="$floor" -v tol="$tolerance" 'BEGIN{print (p < f - tol) ? 1 : 0}')" = "1" ]; then
			echo "    REGRESSION: $m test coverage ${pct}% < floor ${floor}% (tolerance ${tolerance}pp)" >&2
			fail=1
		fi
	fi
done

if $bless; then
	tmp="$(mktemp)"
	{
		echo "# Full-suite (unit+integration) whole-module statement-coverage floors."
		echo "# Managed by scripts/test-coverage-floor.sh --bless. Raised upward only;"
		echo "# a downward reset (denominator change, not a regression) is a hand edit."
		for m in $modules; do
			cur="${measured[$m]:-}"; old="$(floor_for "$m")"; keep="$cur"
			[ -n "$old" ] && keep="$(awk -v a="$old" -v b="${cur:-0}" 'BEGIN{print (a>b)?a:b}')"
			[ -n "$keep" ] && echo "$m $keep"
		done
	} >"$tmp"
	mv "$tmp" "$floors_file"
	echo "blessed floors -> $floors_file"
fi

exit $fail
