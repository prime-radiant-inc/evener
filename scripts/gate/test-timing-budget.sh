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
# skip. A Go package's duration is its own wall time, read from the package-level
# terminal event `go test -json` emits for it (the Elapsed field); the per-test
# and per-subtest Elapsed values feed only the per-test ceiling below, never the
# per-package number (issue #172). `evener-dev timing-parse` reads the stream.
# The frontend has no per-package shape, so its whole vitest run rolls up under
# the single key "web", read from the vitest JSON reporter.
#
# Two independent things are checked, both from the same measurement:
#
#   - Per-package budget: fail at 1.5x the checked-in budget, warn at 1.1x.
#   - Per-test ceiling, SEGMENTED BY TEST CLASS (testing-budget.json's
#     perTestCeilingClasses; ruling, 2026-10-13, issue #4601):
#       * A test named in a class — or a subtest of one (Name/sub) — is compared
#         strictly against that class's ceiling; a breach FAILs regardless of its
#         package's budget. The classes and their ceilings were drawn from the
#         breaches #4601 recorded, each ceiling that class's largest CI observation
#         across two runs + 25% rounded up to a multiple of five seconds, so one
#         runaway test is a defect on its own.
#       * A test in no class keeps the flat perTestCeilingSeconds (currently 3s;
#         2s when a budget file does not name one at all) and stays WARN-ONLY in
#         every mode, --strict included: the unclassified backlog never ran under
#         a ceiling, so enforcing the flat 3s would red the gate on tests that
#         were always over it. Give the test a class to enforce it.
#     A 2026-10-14 ruling (issue #4615) removed four runner-bound classes whose
#     wall time tracks how busy the runner is, not the code - dev-tool
#     compile/CLI, install, transcript large-fixture and gate-runner integration.
#     Their members now keep the flat perTestCeilingSeconds and stay warn-only,
#     like the rest of the unclassified backlog; the sighting was #4615's
#     TestCrossCompileWindows at 52.26s over its 15s class ceiling on a 67-line
#     diff in internal/transcriptindex that could not cost 37 seconds. A
#     2026-10-16 follow-up (issue #4699) moved one member of a KEPT class,
#     TestLoadSessionJobActivityTree_TuesdayFullyFoldsAndFullyPaginates, onto
#     that same flat warn-only ceiling: it folds 200k events and runs
#     t.Parallel() in the agent package, so the package's parallel load, not
#     its own code, sets its wall time (7.6-9.6s measured alone at load 17
#     against 30.09-32.28s in CI), and it sat on either side of its 30s class
#     ceiling run to run. The class keeps its other two members. Two further
#     classes are deliberately absent: bounded-wait unit has no class yet
#     by ruling, so its members stay on the flat ceiling; the web repo-audit test
#     was a class of one and #4608 split it into one case per slice rather than
#     blessing it.
#
# A package the budget file does not mention is a WARN, not silently skipped —
# see kata b6rv's stop condition. When the budget file is missing or its
# "packages" map is empty, the WHOLE run is that same warn state and --check
# always exits 0: the tree must stay green until a real baseline is blessed
# (`make test-rebaseline`), never fail on a budget nobody has measured yet.
# A MALFORMED budget file is different: only a missing (or empty) file is the
# deliberate warn state. An unparseable file is a broken config, not an absent
# one, so it fails in every mode naming the file and the parse error — otherwise
# a stray comma would read as no baseline and silently disarm every ratio and
# every perTestCeilingClasses ceiling (issue #4609). A budget whose TOP level is
# not an object, a `packages` value that is present but not an object, the values
# INSIDE that map, a `perTestCeilingSeconds` that is present but not a finite
# number, and a `perTestCeilingClasses` that is present but not an object are the
# same broken config reached different ways (issues #4614, #4618, #4622): a
# hand-edit `"packages": []` would read as no baseline and disarm the whole
# ratchet with a green gate; `"packages": "x"` and a string or null
# `perTestCeilingSeconds` would crash with a traceback; a bool IS an int, so the
# ratio is computed against True and `true` reads as a 1-second ceiling; an
# Infinity package budget makes m/inf read 0 and a non-finite or huge-integer
# ceiling never fires (silent disarms); and a null class map silently drops every
# class ceiling. A non-object TOP level crashed with an AttributeError traceback —
# #4609 left that shape alone because it already exited nonzero, but #4622's
# contract is a failure that NAMES the file — so it takes this path too. All now
# fail in every mode through the same path, naming the file, the entry or the
# field.
#
# A broken measurement also fails in EVERY mode, --check or not: `go list` or
# `go test` exiting nonzero, or a package go list reported that the go test
# -json stream never mentions (issue #172 — a package that fails to build used
# to contribute no rows, so --bless quietly wrote a budget without it). A failed
# measurement exits before the comparison and before any --bless: there is no
# honest baseline to write from a stream nobody vouches for.
#
# Usage:
#   scripts/gate/test-timing-budget.sh                # measure + print (fails
#                                                  # only on a broken measurement)
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
#                                                   # "TEST\t<pkg>\t<name>\t<secs>" /
#                                                   # "PKG\t<pkg>" rows — how a
#                                                   # caller drives
#                                                   # the comparison contract with
#                                                   # fixture durations, exactly like
#                                                   # the coverage-floor web row's reuse of the vitest report
#
# --check's exit policy: CI is strict (a $CI-set environment, or --strict);
# everywhere else is warn-only (--local, or --check with $CI unset). Strict fails
# on a per-package ratio over 1.5x, on a blessed-class per-test ceiling breach,
# and on a broken measurement; an unclassified test over the flat ceiling warns
# in every mode (see above, #4601). Warn-only prints the identical
# diagnostics, labeled, and always exits 0 except for a broken measurement. Bare
# measurement (no --check) fails only on a broken measurement; its ratio and
# ceiling verdicts are a report, like `evener dev coverage-floor` with neither
# --check nor --bless.
#
# The budget file is a hand-reviewed artifact: raising a package's number is an
# edit to testing-budget.json in the same commit as whatever earned it, exactly
# like the coverage floor files. --bless / make test-rebaseline is the tool for
# that edit, not an automatic ratchet — unlike the coverage floors, a timing
# budget legitimately gets SMALLER as the suite gets faster, so blessing resets
# every measured package to the fresh number rather than keeping the max.
#
# A bless writes every package it measured and leaves the rest of the file
# alone: a narrowed run (--modules, --no-web, or a frontend that is not checked
# out) refreshes the packages it measured instead of deleting the ones it did
# not, so a narrowed rebaseline can never drop a budget nobody measured this
# time. A full rebaseline is the exception: it drops entries go list no longer
# reports, and records the metric marker the ratio check reads.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
budget_file="${repo_root}/testing-budget.json"
modules=". agent llm auth envvars invariant identifier execsupport"
web_dir="${repo_root}/cmd/evener-hub/frontend"
web=true
modules_overridden=0
check=false
bless=false
strict_override=""
measured_override=""

while [ $# -gt 0 ]; do
	case "$1" in
		--modules) modules="$2"; modules_overridden=1; shift 2 ;;
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

# --measured skips the producers, so nothing vouches for the packages a bless
# would record; a rebaseline has to come from a real measurement.
if [ -n "$measured_override" ] && $bless; then
	echo "test-timing-budget: --measured skips the measurement, so there is no package completeness to bless from; run a real measurement to rebaseline" >&2
	exit 2
fi

# The gate's test-selection surface, shared with run-module-tests.sh and
# `evener dev coverage-floor` so this ratchet cannot drift into measuring a
# surface no gate reproduces.
. "$(dirname "${BASH_SOURCE[0]}")/../lib/gate-surface-lib.sh"
. "$(dirname "${BASH_SOURCE[0]}")/../lib/scratch-lib.sh"
# The row contract (go_test_json_to_tsv / vitest_json_to_tsv) is shared with
# run-module-tests.sh, the gate's single measured producer (issue #144).
. "$(dirname "${BASH_SOURCE[0]}")/../lib/timing-tsv-lib.sh"

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

module_short_flag() {
	[ "$1" = "." ] && printf '' || printf -- '-short'
}

go_measure_failed=0
module_missing=0
if [ -n "$measured_override" ]; then
	[ -f "$measured_override" ] || { echo "test-timing-budget: --measured file not found: $measured_override" >&2; run_failed=1; exit 1; }
	cp "$measured_override" "$measured"
else
	for m in $modules; do
		if [ ! -f "$repo_root/$m/go.mod" ]; then
			echo "test-timing-budget: no module at $m, skipping" >&2
			module_missing=1
			continue
		fi
		name="$m"; [ "$name" = "." ] && name="root"
		base="$work/$(printf '%s' "$name" | tr / _)"
		log="$base.jsonl"
		pkglist="$base.packages"
		short="$(module_short_flag "$m")"
		pkgs=()
		# Status captured BEFORE any negation: under bash 3.2 (macOS stock) an
		# `if ! ( ... ) >file` whose OUTER redirection fails (unwritable scratch,
		# ENOSPC) has the failure eaten by the negation and the guard silently
		# skipped. Capturing $? first attributes every nonzero — producer exit
		# or redirection failure — to this branch.
		( cd "$repo_root/$m" && go list ./... ) >"$pkglist.raw" 2>"$base.go-list.stderr"
		gl_status=$?
		if [ "$gl_status" -ne 0 ]; then
			echo "test-timing-budget: go list ./... in $m exited nonzero (see $base.go-list.stderr)" >&2
			go_measure_failed=1; continue
		fi
		while IFS= read -r pkg; do
			case "$pkg" in
				*/cmd/evener-fuzzcov|*/cmd/evener-fuzz-harvest) continue ;;
			esac
			pkgs+=("$pkg")
		done <"$pkglist.raw"
		if [ "${#pkgs[@]}" -eq 0 ]; then
			echo "test-timing-budget: go list ./... in $m returned no packages" >&2
			go_measure_failed=1; continue
		fi
		printf '%s\n' "${pkgs[@]}" >"$pkglist"
		# Same capture-before-negation shape as the go list block above, for
		# the same bash 3.2 reason: a failed outer redirection must land here,
		# not be eaten by `!`.
		( cd "$repo_root/$m" && go test -json -count=1 $short \
			-run "$GATE_TEST_RUN" -skip "$GATE_FUZZ_TEST_SKIP" "${pkgs[@]}" ) >"$log" 2>"$log.stderr"
		gt_status=$?
		if [ "$gt_status" -ne 0 ]; then
			echo "test-timing-budget: go test in $m exited nonzero (see $log and $log.stderr)" >&2
			go_measure_failed=1; continue
		fi
		# The package-completeness oracle (issue #172) runs inside the parser:
		# a package in $pkglist with no terminal event in the stream is a
		# silent drop, and the parse exits nonzero rather than bless around it.
		go_rows="$base.rows"
		if go_test_json_to_tsv "$log" "$pkglist" >"$go_rows" && cat "$go_rows" >>"$measured"; then
			: # parsed and merged
		else
			echo "test-timing-budget: rows for $m could not be parsed and merged into the measurement (see $base.rows and $log.stderr)" >&2
			go_measure_failed=1; continue
		fi
	done

	if $web; then
		if [ -d "$web_dir" ]; then
			report="$work/vitest-report.json"
			if ( cd "$web_dir" && PATH="$PWD/node_modules/.bin:$PATH" \
				vitest run --configLoader runner --reporter=json --outputFile="$report" \
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
				if vitest_json_to_tsv "$report" >"$web_rows" && cat "$web_rows" >>"$measured"; then
					: # parsed and merged
				else
					echo "test-timing-budget: vitest rows could not be parsed and merged into the measurement (see $report and $web_rows)" >&2
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

# bless_full records whether THIS run measured the whole budgeted surface: the
# default module list (no --modules), every default module present (a sparse
# checkout that skipped one is not full), the frontend included, and a real
# measurement rather than a --measured replay. A bless needs it to decide
# whether every number left in the file is in the metric this run just measured
# — see the metric-marker logic in compare.py below. It is computed here, after
# the module loop, because that loop is what records a skipped module.
bless_full=false
if [ -z "$measured_override" ] && [ "$modules_overridden" -eq 0 ] && [ "$module_missing" -eq 0 ] && $web && [ -d "$web_dir" ]; then
	bless_full=true
fi

# compare.py is the whole comparison contract: package ratios against the
# checked-in budget, the per-class per-test ceilings (strict) plus the flat
# per-test ceiling (warn-only for an unclassified test), the missing-budget-entry
# warn, and the global no-baseline-yet warn. It can be exercised entirely through
# --budget/--modules/measured.tsv, with no go test or vitest run involved —
# the fixture IS the input this step reads.
compare_out="$work/compare.txt"
python3 - "$measured" "$budget_file" "$bless" "$check" "$strict" "$compare_out" "$bless_full" <<'PY'
import json, math, sys

measured_path, budget_path, bless, check, strict, out_path, bless_full = sys.argv[1:8]
bless = bless == "true"
check = check == "true"
strict = strict == "true"
bless_full = bless_full == "true"

DEFAULT_CEILING = 2.0
FAIL_RATIO = 1.5
WARN_RATIO = 1.1

# The metric the recorded numbers were blessed under. A budget whose "metric"
# is not this was measured under the pre-#172 sum-of-test-Elapsed metric, whose
# numbers are not comparable to the package wall time measured now: comparing
# them can read high (setup/TestMain overhead) or low (parallel overlap). Until
# a rebaseline rewrites them, a would-be ratio FAIL is a WARNING, never fatal —
# enforcing numbers in the wrong units would be worse than not enforcing them
# (issue #172 review).
WALL_METRIC = "package-wall-seconds"

# The frontend rolls up under this one key. Its number is the vitest reporter's
# assertion durations, the same metric issue #172 left unchanged, but the
# checked-in entry was blessed on a different host than a CI runner, so its
# measurement is not comparable to this run's either and it warns with the Go
# rows until #141 re-blesses the baseline for the host class CI uses.
WEB_KEY = "web"

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
		elif parts[0] == "PKG":
			# A package-level terminal event. It seeds the package at 0 so a
			# package whose every test was filtered out (or that has no test
			# files at all) still appears in the report and in a blessed
			# budget — the completeness contract's other half: what the
			# producer vouches for must be recorded, not just what ran.
			if parts[1]:
				sums.setdefault(parts[1], 0.0)

def fail_malformed(what):
	# A malformed budget is a broken config, not an absent one. The missing-file
	# warn state below is deliberate (kata b6rv): the tree stays green until a
	# measured baseline is blessed. A syntax error must not ride that path —
	# collapsing it into `budget = {}` reads as no baseline and silently disarms
	# every package ratio and every perTestCeilingClasses ceiling, with nothing
	# naming the syntax error (issue #4609). Fail in every mode, before any bless,
	# naming the file and the problem.
	with open(out_path, "w") as fh:
		fh.write("FAIL  MALFORMED BUDGET: testing-budget.json " + what
			+ " (" + budget_path + ")\n")
	sys.exit(1)

def seconds_error(value):
	# Why `value` cannot serve as a number of seconds, or None if it can. bool is an
	# int subclass in Python, so it is rejected explicitly (True would otherwise read
	# as 1). A JSON float can be Infinity or NaN, and a JSON integer can be too large
	# to convert to a float — a 400-digit literal, which would crash the ratio
	# arithmetic below and is no more a usable seconds value than Infinity is. The
	# package map and the flat perTestCeilingSeconds share this one definition.
	if isinstance(value, bool) or not isinstance(value, (int, float)):
		return "got " + type(value).__name__
	try:
		if math.isfinite(value):
			return None
	except OverflowError:
		return "too large for a float"
	return "got " + json.dumps(value)

try:
	with open(budget_path) as fh:
		budget = json.load(fh)
except FileNotFoundError:
	budget = {}
except ValueError as exc:
	fail_malformed("could not be parsed: " + str(exc))

# A budget whose TOP level is not an object is a broken config, not an empty one:
# budget.get below would crash a list, number, string, null or bool with an
# AttributeError traceback. #4609 left this shape alone because it already exited
# nonzero (through the traceback), but issue #4622 requires every malformed shape
# to fail loudly NAMING the file, so it takes the same path as the rest
# (#4609/#4614/#4618) instead of an unnamed crash.
if not isinstance(budget, dict):
	fail_malformed("must be a JSON object (got " + type(budget).__name__ + ")")

# A `packages` value present but not an object is a broken config, not an empty
# map: `packages or {}` swallowed the falsy mistakes ([], 0, false, "") into the
# NO BASELINE warn state (exit 0, nothing enforced) and crashed on the truthy
# ones (issue #4614). Test the KEY, not the value — `budget.get` cannot tell an
# absent key from an explicit `null`, and `{"packages": null}` disarms the same
# way — and route it through the malformed-config path above (#4609), which fails
# in every mode naming the file. `budget` is an object by the guard above.
if "packages" in budget and not isinstance(budget["packages"], dict):
	fail_malformed("packages must be an object mapping each package to its budget seconds (got "
		+ type(budget["packages"]).__name__ + ")")
packages = budget.get("packages", {})

# Every package VALUE must be a usable number, not only the map's type (issue
# #4618). A string crashes the ratio below with a TypeError traceback, a bool IS
# an int in Python (True/False) so the ratio is computed against 1 or 0, and a
# null reads as "no budget entry" — a WARN that leaves the package silently
# unenforced. A non-finite value is worse than either: m/inf reads 0, so the
# package prints OK and the run exits 0 with the package silently disarmed, and a
# 400-digit integer tracebacks the arithmetic (issue #4622). Route each through
# the malformed-config path above, which fails in every mode naming the entry.
# The loop runs only for an object `packages` (the top-level guard above).
for pkg, budget_secs in packages.items():
	problem = seconds_error(budget_secs)
	if problem:
		fail_malformed("packages[" + json.dumps(pkg) + "] must be a number of seconds (" + problem + ")")

# The flat ceiling is read with budget.get, so a present null cannot be told from
# an absent key — test the KEY, as `packages` and the class map do. A string,
# null, list or object crashes the `secs > ceiling` comparison with a TypeError
# traceback; a bool IS an int so `true` silently becomes a 1-second ceiling; and a
# non-finite value silently disarms the ceiling instead of bounding it. Each is a
# broken config reached the same way as the package values above (issue #4622),
# so each takes the malformed-config path and fails in every mode naming the field.
if "perTestCeilingSeconds" in budget:
	problem = seconds_error(budget["perTestCeilingSeconds"])
	if problem:
		fail_malformed("perTestCeilingSeconds must be a number of seconds (" + problem + ")")
ceiling = budget.get("perTestCeilingSeconds", DEFAULT_CEILING)

# The per-class ceilings (#4601, ruling 2026-10-13). A test whose name is listed
# in a class, or is a subtest of one (Name/sub), is compared STRICTLY against
# that class's ceiling; a test in no class keeps the flat `ceiling` above and
# stays warn-only in every mode, so the unclassified backlog cannot red the
# gate. Membership is by test name, not by package, because one package holds
# several classes (issue #4601). A name matched by more than one class is a
# config error, reported as a FAIL: the map is hand-edited, and silently
# picking the first class would enforce the wrong number.
# A perTestCeilingClasses present but not an object is a broken config, not "no
# classes": `budget.get` cannot tell an absent key from an explicit `null`, so
# `{"perTestCeilingClasses": null}` read as an empty map and silently disarmed
# every class ceiling, while the other non-object values (`[]`, `false`, `0`,
# `""`) failed only under the strict posture (issue #4618). Test the KEY and
# route all of them through the malformed-config path above, as the `packages`
# value does (#4614): they fail in every mode, not only strict. An absent key
# still means no classes, which is fine. The `packages` read above has already
# made `budget` an object, so no isinstance guard is needed here.
if "perTestCeilingClasses" in budget and not isinstance(budget["perTestCeilingClasses"], dict):
	fail_malformed("perTestCeilingClasses must be an object mapping each class to "
		"{ceilingSeconds, tests} (got " + type(budget["perTestCeilingClasses"]).__name__ + ")")
ceiling_classes = budget.get("perTestCeilingClasses", {})

def class_memberships(name):
	found = []
	for class_name, spec in ceiling_classes.items():
		for member in spec.get("tests", []):
			if name == member or name.startswith(member + "/"):
				found.append((class_name, spec.get("ceilingSeconds")))
				break
	return found

no_baseline = len(packages) == 0
had_metric = budget.get("metric") == WALL_METRIC
stale_units = not no_baseline and not had_metric

lines = []
worst = "ok"  # ok < warn < fail
def raise_worst(level):
	global worst
	order = {"ok": 0, "warn": 1, "fail": 2}
	if order[level] > order[worst]:
		worst = level

# A class must be an object naming a usable (non-bool) ceiling and a non-empty
# list of string test names. The ceiling goes through the shared seconds_error
# definition and the malformed-config path above — every mode, naming the class
# and the field — like the package values and the flat perTestCeilingSeconds
# (#4629). The remaining faults (a non-object spec, or a `tests` string that
# iterates as characters and matches nothing) FAIL naming the class and drop the
# class, leaving its members on the flat warn-only default below rather than
# crashing the check. (The map itself being a non-object is the harder error
# above, handled before any class is read.)
valid_classes = {}
for class_name, spec in ceiling_classes.items():
	bad_spec = (f"FAIL  ceiling class '{class_name}' must be an object with a "
		f"non-empty tests list of strings (testing-budget.json perTestCeilingClasses)")
	if not isinstance(spec, dict):
		lines.append(bad_spec)
		raise_worst("fail")
		continue
	# seconds_error is the one definition of "cannot serve as seconds", and it is
	# overflow-safe: a JSON integer too large to convert to a float used to reach
	# math.isfinite first and raise OverflowError — a traceback in every mode, from
	# a shape the strict-only FAIL line below would only have downgraded (#4629).
	# Routing its problem through fail_malformed names the class and the field in
	# every mode. A huge but FINITE ceiling stays accepted: a ceiling is an upper
	# bound, so a very large finite value simply disables that class.
	problem = seconds_error(spec.get("ceilingSeconds"))
	if problem:
		fail_malformed("perTestCeilingClasses[" + json.dumps(class_name)
			+ "].ceilingSeconds must be a number of seconds (" + problem + ")")
	if (not isinstance(spec.get("tests"), list)
			or not spec.get("tests")
			or not all(isinstance(t, str) for t in spec.get("tests"))):
		lines.append(bad_spec)
		raise_worst("fail")
		continue
	valid_classes[class_name] = spec
ceiling_classes = valid_classes

if no_baseline:
	lines.append("NO BASELINE: testing-budget.json has no packages recorded yet; every "
		"result below is informational only and --check always exits 0 until "
		"`make test-rebaseline` lands a measured baseline (kata b6rv).")
elif stale_units:
	lines.append("STALE UNITS: testing-budget.json's Go-package numbers were blessed "
		"under the pre-#172 sum-of-test-Elapsed metric and its "
		f"\"{WEB_KEY}\" entry on a different host than this run, so no ratio in it is "
		"comparable to what this run measured; every ratio failure below warns, with its "
		"numbers, until #141 re-blesses the baseline for the host class CI uses "
		f"(a rebaseline records \"metric\": \"{WALL_METRIC}\").")

for pkg in sorted(sums):
	m = sums[pkg]
	b = packages.get(pkg)
	if b is None:
		lines.append(f"WARN  {pkg}: {m:.2f}s, no budget entry (add one to testing-budget.json)")
		raise_worst("warn")
		continue
	ratio = (m / b) if b > 0 else (float("inf") if m > 0 else 0.0)
	if ratio > FAIL_RATIO:
		if stale_units:
			lines.append(f"WARN  {pkg}: {m:.2f}s over stale budget {b:.2f}s "
				f"({ratio:.2f}x > {FAIL_RATIO}x; not enforced until the #141 rebaseline)")
			raise_worst("warn")
		else:
			lines.append(f"FAIL  {pkg}: {m:.2f}s over budget {b:.2f}s ({ratio:.2f}x > {FAIL_RATIO}x)")
			raise_worst("fail")
	elif ratio > WARN_RATIO:
		lines.append(f"WARN  {pkg}: {m:.2f}s over budget {b:.2f}s ({ratio:.2f}x > {WARN_RATIO}x)")
		raise_worst("warn")
	else:
		lines.append(f"OK    {pkg}: {m:.2f}s (budget {b:.2f}s)")

for pkg, name, secs in tests:
	# A test in a class is compared STRICTLY against that class's ceiling
	# (#4601, ruling 2026-10-13): the classes are drawn, so a breach is a real
	# regression and FAILs — nonzero under --check in a CI-shaped environment or
	# --strict. Membership is by name; a subtest inherits its parent's class.
	memberships = class_memberships(name)
	if len(memberships) > 1:
		listed = ", ".join(f"'{c}'" for c, _ in memberships)
		lines.append(f"FAIL  {pkg}: test {name} is listed in more than one ceiling class ({listed}); "
			f"a test must belong to exactly one class in testing-budget.json perTestCeilingClasses")
		raise_worst("fail")
		continue
	if memberships:
		class_name, class_ceiling = memberships[0]
		if secs > class_ceiling:
			lines.append(f"FAIL  {pkg}: test {name} took {secs:.2f}s, over its {class_ceiling:g}s "
				f"'{class_name}' class ceiling")
			raise_worst("fail")
		else:
			lines.append(f"OK    {pkg}: test {name} {secs:.2f}s (class '{class_name}', ceiling {class_ceiling:g}s)")
		continue
	if secs > ceiling:
		# A test in no class keeps the flat ceiling and stays warn-only in EVERY
		# mode, --strict included (#4601): the unclassified backlog has never run
		# under a ceiling, so enforcing the flat 3s would red the gate on tests
		# that were always over it. Give the test a class to enforce it.
		lines.append(f"WARN  {pkg}: test {name} took {secs:.2f}s, over the {ceiling:.2f}s per-test ceiling "
			f"(unclassified; warn-only, #4601)")
		raise_worst("warn")

# no_baseline is the ONE place the "tree must stay green" rule is enforced
# (below, in exit_code): severity above is computed the same way regardless of
# whether a baseline exists, so this override is the single point that can
# make it a no-op rather than three scattered ones that could drift apart.

if bless:
	# Key order is a function of the checked-in file, not of the run: the
	# terminal events arrive in completion order, which varies run to run, so a
	# re-bless of an unchanged tree has to reproduce the file. Recorded packages
	# keep the order the file already has - re-sorting them would reorder nearly
	# all of testing-budget.json's packages in a diff that says nothing about
	# timing - and only packages the file does not mention are ordered by name.
	#
	# A bless records what this run measured and never deletes an entry it did
	# not measure: a narrowed run (--modules, --no-web, or a frontend that is
	# not checked out) would otherwise silently drop every package it skipped
	# from the one file whose whole job is to be the checked-in baseline. A FULL
	# bless is different — go list enumerated every package, so an entry with no
	# measurement this run is a package gone from the tree, not one skipped, and
	# is dropped below.
	order = list(packages) + sorted(pkg for pkg in sums if pkg not in packages)
	measured = {pkg: round(sums[pkg], 2) for pkg in sums}
	if bless_full:
		# Full surface: every recorded package was measured, so the whole file is
		# package wall time. Drop entries go list no longer reports (they hold
		# the old units, and keeping one would either enforce it as wall time or
		# pin the file to the old units forever) and stamp the marker.
		budget["packages"] = {pkg: measured[pkg] for pkg in order if pkg in measured}
		budget["metric"] = WALL_METRIC
	else:
		budget["packages"] = {
			pkg: (measured[pkg] if pkg in measured else packages[pkg]) for pkg in order
		}
		# A narrowed bless keeps an existing marker — the entries it preserves
		# are already wall time — but never adds one: stamping a stale file here
		# would enforce ratios against the old-unit entries it preserved (issue
		# #172 review).
		if had_metric:
			budget["metric"] = WALL_METRIC
	budget.setdefault("perTestCeilingSeconds", DEFAULT_CEILING)
	with open(budget_path, "w") as fh:
		# indent=1 (spaces) is the checked-in file's format, so a rebaseline
		# does not reformat all ~130 lines and bury the real change in
		# whitespace (issue #172).
		json.dump(budget, fh, indent=1)
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
		if worst == "fail":
			lines.append(f"warn-only run: would exit non-zero under --strict/CI ({worst})")
		elif worst == "warn":
			lines.append("warn-only run: the warnings above are reported but not enforced")

with open(out_path, "w") as fh:
	fh.write("\n".join(lines) + ("\n" if lines else ""))
sys.exit(exit_code)
PY
status=$?
cat "$compare_out"
[ "$status" -eq 0 ] || run_failed=1
exit "$status"
