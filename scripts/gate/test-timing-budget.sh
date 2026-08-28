#!/usr/bin/env bash
# test-timing-budget.sh — a no-regression ratchet on how long the non-fuzz test
# suite takes, per Go package plus one aggregate for the frontend. Companion to
# scripts/coverage/coverage-floor.sh (Go union and frontend)
# coverage): those guard how MUCH the suite proves, this guards how LONG it
# takes, so an unexamined regression cannot silently erode the wins recorded in
# docs/superpowers/specs/2026-08-01-test-gate-runtime-design.md (kata b6rv).
#
# It measures the SAME surface ROOT_FULL=1 make test proves, reusing
# gate-surface-lib.sh exactly like `evener dev coverage-floor`: -short on every
# module except root, the gate's Test/Example filter, and the fuzz-owned name
# skip. Go durations come from `go test -json`'s per-test Elapsed field, summed
# per PACKAGE (the import path go test -json already reports — no extra
# grouping). The frontend has no per-package shape, so its whole vitest run
# rolls up under the single key "web", read from the vitest JSON reporter.
#
# Two independent things are checked, both from the same measurement:
#
#   - Per-package budget: fail at 1.5x the checked-in budget, warn at 1.1x.
#   - Per-test ceiling (testing-budget.json's perTestCeilingSeconds, currently
#     3s; 2s when a budget file does not name one at all):
#     any single test or subtest over the ceiling is flagged regardless of its
#     package's budget — one runaway test is a defect on its own.
#
# A package the budget file does not mention is a WARN, not silently skipped —
# see kata b6rv's stop condition. When the budget file is missing or its
# "packages" map is empty, the WHOLE run is that same warn state and --check
# always exits 0: the tree must stay green until a real baseline is blessed
# (`make test-rebaseline`), never fail on a budget nobody has measured yet.
#
# Usage:
#   scripts/gate/test-timing-budget.sh                # measure + print, never fails
#   scripts/gate/test-timing-budget.sh --check         # also enforce (see policy below)
#   scripts/gate/test-timing-budget.sh --bless         # make test-rebaseline: overwrite
#                                                  # every measured package's budget
#                                                  # with what this run just measured
#   scripts/gate/test-timing-budget.sh --modules "..."  # override the Go module list
#   scripts/gate/test-timing-budget.sh --budget FILE    # override the budget file path
#   scripts/gate/test-timing-budget.sh --web-dir DIR    # override the frontend directory
#   scripts/gate/test-timing-budget.sh --no-web         # skip the frontend measurement
#   scripts/gate/test-timing-budget.sh --strict         # force CI's fail-on-1.5x policy
#   scripts/gate/test-timing-budget.sh --local          # force the warn-only policy
#   scripts/gate/test-timing-budget.sh --measured FILE  # skip go test/vitest entirely
#                                                   # and compare FILE's already-
#                                                   # measured "SUM\t<pkg>\t<secs>" /
#                                                   # "TEST\t<pkg>\t<name>\t<secs>"
#                                                   # rows — how a caller drives
#                                                   # the comparison contract with
#                                                   # fixture durations, exactly like
#                                                   # the coverage-floor web row's reuse of the vitest report
#
# --check's exit policy: CI is strict (a $CI-set environment, or --strict);
# everywhere else is warn-only (--local, or --check with $CI unset). Strict
# fails on a per-package ratio over 1.5x or any ceiling breach; warn-only
# prints the identical diagnostics, labeled, and always exits 0. Bare
# measurement (no --check) never fails either way — it is a report, like
# `evener dev coverage-floor` with neither --check nor --bless.
#
# The budget file is a hand-reviewed artifact: raising a package's number is an
# edit to testing-budget.json in the same commit as whatever earned it, exactly
# like the coverage floor files. --bless / make test-rebaseline is the tool for
# that edit, not an automatic ratchet — unlike the coverage floors, a timing
# budget legitimately gets SMALLER as the suite gets faster, so blessing resets
# every measured package to the fresh number rather than keeping the max.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
budget_file="${repo_root}/testing-budget.json"
modules=". agent llm auth envvars invariant identifier"
web_dir="${repo_root}/cmd/evener-hub/frontend"
web=true
check=false
bless=false
strict_override=""
measured_override=""

while [ $# -gt 0 ]; do
	case "$1" in
		--modules) modules="$2"; shift 2 ;;
		--budget) budget_file="$2"; shift 2 ;;
		--web-dir) web_dir="$2"; shift 2 ;;
		--no-web) web=false; shift ;;
		--check) check=true; shift ;;
		--bless) bless=true; shift ;;
		--strict) strict_override=1; shift ;;
		--local) strict_override=0; shift ;;
		--measured) measured_override="$2"; shift 2 ;;
		-h|--help) awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

# The gate's test-selection surface, shared with run-module-tests.sh and
# `evener dev coverage-floor` so this ratchet cannot drift into measuring a
# surface no gate reproduces.
. "$(dirname "${BASH_SOURCE[0]}")/../lib/gate-surface-lib.sh"
. "$(dirname "${BASH_SOURCE[0]}")/../lib/scratch-lib.sh"

if [ -n "$strict_override" ]; then
	strict=$([ "$strict_override" = "1" ] && echo true || echo false)
else
	strict=$([ -n "${CI:-}" ] && echo true || echo false)
fi

run_failed=0
cleanup() { [ "$run_failed" -eq 0 ] && scratch_rm; }
trap cleanup EXIT
scratch_dir work evener-testbudget

measured="$work/measured.tsv"
: >"$measured"

# go_test_json_to_tsv PACKAGE_JSON_LOG >> measured.tsv — appends one
# "SUM\t<package>\t<seconds>" line per package (summing only TOP-LEVEL
# Test/Example results, never subtests, so a parent's elapsed and its
# subtests' elapsed are not both counted) and one
# "TEST\t<package>\t<name>\t<seconds>" line per test AND subtest result, which
# is what the ceiling check needs to catch a slow subtest a top-level-only sum
# would hide.
go_test_json_to_tsv() {
	python3 - "$1" <<'PY'
import json, sys

sums = {}
with open(sys.argv[1]) as fh:
	for line in fh:
		line = line.strip()
		if not line:
			continue
		try:
			ev = json.loads(line)
		except ValueError:
			continue
		if ev.get("Action") not in ("pass", "fail", "skip"):
			continue
		test = ev.get("Test")
		if not test:
			continue
		pkg = ev.get("Package", "")
		elapsed = ev.get("Elapsed", 0.0)
		print(f"TEST\t{pkg}\t{test}\t{elapsed}")
		if "/" not in test:
			sums[pkg] = sums.get(pkg, 0.0) + elapsed
for pkg, total in sums.items():
	print(f"SUM\t{pkg}\t{total}")
PY
}

# vitest_json_to_tsv REPORT_JSON >> measured.tsv — rolls the whole frontend
# suite up under one package key, "web": vitest has no per-package shape the
# way a Go module does, and the budget's own doc comment names this as the
# design. duration is milliseconds in the vitest/Jest-compatible JSON reporter;
# everything else in this file is seconds.
vitest_json_to_tsv() {
	python3 - "$1" <<'PY'
import json, sys

with open(sys.argv[1]) as fh:
	report = json.load(fh)

total = 0.0
for suite in report.get("testResults", []):
	for a in suite.get("assertionResults", []):
		secs = a.get("duration") or 0
		secs = secs / 1000.0
		name = a.get("fullName") or a.get("title") or "(unnamed)"
		print(f"TEST\tweb\t{name}\t{secs}")
		total += secs
print(f"SUM\tweb\t{total}")
PY
}

module_short_flag() {
	[ "$1" = "." ] && printf '' || printf -- '-short'
}

go_measure_failed=0
if [ -n "$measured_override" ]; then
	[ -f "$measured_override" ] || { echo "test-timing-budget: --measured file not found: $measured_override" >&2; run_failed=1; exit 1; }
	cp "$measured_override" "$measured"
else
	for m in $modules; do
		[ -f "$repo_root/$m/go.mod" ] || { echo "test-timing-budget: no module at $m, skipping" >&2; continue; }
		name="$m"; [ "$name" = "." ] && name="root"
		log="$work/$(printf '%s' "$name" | tr / _).jsonl"
		short="$(module_short_flag "$m")"
		if [ "$m" = "." ]; then
			pkgs=()
			while IFS= read -r pkg; do
				case "$pkg" in
					*/cmd/evener-fuzzcov|*/cmd/evener-fuzz-harvest) continue ;;
				esac
				pkgs+=("$pkg")
			done < <(cd "$repo_root/$m" && go list ./... 2>/dev/null)
			if [ "${#pkgs[@]}" -eq 0 ]; then
				echo "test-timing-budget: go list ./... in $m returned no packages" >&2
				go_measure_failed=1; continue
			fi
			( cd "$repo_root/$m" && go test -json -count=1 $short \
				-run "$GATE_TEST_RUN" -skip "$GATE_FUZZ_TEST_SKIP" "${pkgs[@]}" ) >"$log" 2>"$log.stderr"
		else
			( cd "$repo_root/$m" && go test -json -count=1 $short \
				-run "$GATE_TEST_RUN" -skip "$GATE_FUZZ_TEST_SKIP" ./... ) >"$log" 2>"$log.stderr"
		fi
		go_test_json_to_tsv "$log" >>"$measured"
	done

	if $web; then
		if [ -d "$web_dir" ]; then
			report="$work/vitest-report.json"
			if ( cd "$web_dir" && PATH="$PWD/node_modules/.bin:$PATH" \
				vitest run --reporter=json --outputFile="$report" \
				--exclude scripts/browserGuardProcess.test.mjs ) >"$work/vitest.log" 2>&1; then
				:
			fi
			if [ -f "$report" ]; then
				# Parse into a scratch-private file first and merge only on
				# success: appending vitest_json_to_tsv straight to $measured
				# would let a mid-loop crash (issue #598 F3 -- valid top-level
				# JSON, then a malformed entry after at least one TEST row was
				# already printed) leave orphaned TEST rows with no SUM row in
				# the retained-on-failure scratch (below, once go_measure_failed
				# is nonzero), which would look like a clean, silently-incomplete
				# "web" measurement to a later --measured replay instead of the
				# incomplete run it is.
				web_rows="$work/vitest-rows.tsv"
				if vitest_json_to_tsv "$report" >"$web_rows"; then
					cat "$web_rows" >>"$measured"
				else
					echo "test-timing-budget: failed to parse vitest report at $report" >&2
					go_measure_failed=1
				fi
			else
				echo "test-timing-budget: no vitest report at $report (see $work/vitest.log)" >&2
				go_measure_failed=1
			fi
		else
			echo "test-timing-budget: no frontend at $web_dir, skipping web" >&2
		fi
	fi
fi

if [ "$go_measure_failed" -ne 0 ]; then
	run_failed=1
	echo "test-timing-budget: measurement incomplete; scratch retained at $work" >&2
	exit 1
fi

# compare.py is the whole comparison contract: package ratios against the
# checked-in budget, the flat per-test ceiling, the missing-budget-entry warn,
# and the global no-baseline-yet warn. It can be exercised entirely through
# --budget/--modules/measured.tsv, with no go test or vitest run involved —
# the fixture IS the input this step reads.
compare_out="$work/compare.txt"
python3 - "$measured" "$budget_file" "$bless" "$check" "$strict" "$compare_out" <<'PY'
import json, sys

measured_path, budget_path, bless, check, strict, out_path = sys.argv[1:7]
bless = bless == "true"
check = check == "true"
strict = strict == "true"

DEFAULT_CEILING = 2.0
FAIL_RATIO = 1.5
WARN_RATIO = 1.1

sums = {}
tests = []  # (package, name, seconds)
with open(measured_path) as fh:
	for line in fh:
		parts = line.rstrip("\n").split("\t")
		if parts[0] == "SUM":
			_, pkg, secs = parts
			sums[pkg] = sums.get(pkg, 0.0) + float(secs)
		elif parts[0] == "TEST":
			_, pkg, name, secs = parts
			tests.append((pkg, name, float(secs)))

try:
	with open(budget_path) as fh:
		budget = json.load(fh)
except (FileNotFoundError, ValueError):
	budget = {}

packages = budget.get("packages") or {}
ceiling = budget.get("perTestCeilingSeconds", DEFAULT_CEILING)
no_baseline = len(packages) == 0

lines = []
worst = "ok"  # ok < warn < fail
def raise_worst(level):
	global worst
	order = {"ok": 0, "warn": 1, "fail": 2}
	if order[level] > order[worst]:
		worst = level

if no_baseline:
	lines.append("NO BASELINE: testing-budget.json has no packages recorded yet; every "
		"result below is informational only and --check always exits 0 until "
		"`make test-rebaseline` lands a measured baseline (kata b6rv).")

for pkg in sorted(sums):
	m = sums[pkg]
	b = packages.get(pkg)
	if b is None:
		lines.append(f"WARN  {pkg}: {m:.2f}s, no budget entry (add one to testing-budget.json)")
		raise_worst("warn")
		continue
	ratio = (m / b) if b > 0 else (float("inf") if m > 0 else 0.0)
	if ratio > FAIL_RATIO:
		lines.append(f"FAIL  {pkg}: {m:.2f}s over budget {b:.2f}s ({ratio:.2f}x > {FAIL_RATIO}x)")
		raise_worst("fail")
	elif ratio > WARN_RATIO:
		lines.append(f"WARN  {pkg}: {m:.2f}s over budget {b:.2f}s ({ratio:.2f}x > {WARN_RATIO}x)")
		raise_worst("warn")
	else:
		lines.append(f"OK    {pkg}: {m:.2f}s (budget {b:.2f}s)")

for pkg, name, secs in tests:
	if secs > ceiling:
		lines.append(f"FAIL  {pkg}: test {name} took {secs:.2f}s, over the {ceiling:.2f}s per-test ceiling")
		raise_worst("fail")

# no_baseline is the ONE place the "tree must stay green" rule is enforced
# (below, in exit_code): severity above is computed the same way regardless of
# whether a baseline exists, so this override is the single point that can
# make it a no-op rather than three scattered ones that could drift apart.

if bless:
	budget["packages"] = {pkg: round(m, 2) for pkg, m in sums.items()}
	budget.setdefault("perTestCeilingSeconds", DEFAULT_CEILING)
	with open(budget_path, "w") as fh:
		json.dump(budget, fh, indent="\t")
		fh.write("\n")
	lines.append(f"blessed budget -> {budget_path}")

exit_code = 0
if check:
	if no_baseline:
		exit_code = 0
	elif strict:
		exit_code = 1 if worst == "fail" else 0
	else:
		exit_code = 0
		if worst != "ok":
			lines.append(f"warn-only run: would exit non-zero under --strict/CI ({worst})")

with open(out_path, "w") as fh:
	fh.write("\n".join(lines) + ("\n" if lines else ""))
sys.exit(exit_code)
PY
status=$?
cat "$compare_out"
[ "$status" -eq 0 ] || run_failed=1
exit "$status"
