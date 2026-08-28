#!/usr/bin/env bash
# coverage-floor.sh — the repo's one coverage ratchet: how much of each module
# (and the frontend) is exercised by ANY deterministic test.
#
# Per Go module it measures the UNION of the test track (the gate's Test/
# Example surface) and the fuzz track (committed seed-corpus replay under
# -tags evenerfuzz): each track alone understates, because the gate's
# -run '^(Test|Example)' filter excludes fuzz targets and whole families of
# behavioural checks live in `check*` functions only a tagged "program" target
# calls. The web row is the frontend's vitest line coverage.
#
# The union of the two Go profiles is counted by position via the Go covstmt
# primitive (`evener dev covstmt`), so a block covered by either track counts
# once. Both tracks are measured against the module's OWN packages
# (go list ./...), not `./...` — under go.work that is a filesystem pattern
# matching every nested module too.
#
# Usage:
#   scripts/coverage/coverage-floor.sh                     # measure + print
#   scripts/coverage/coverage-floor.sh --check             # ratchet: non-zero on a drop
#   scripts/coverage/coverage-floor.sh --bless             # raise floors to current %
#   scripts/coverage/coverage-floor.sh --modules "agent llm"
#   scripts/coverage/coverage-floor.sh --go-only | --web-only
#   scripts/coverage/coverage-floor.sh --tolerance 0.5     # wobble band (default 0.5pp)
#
# Floors live in scripts/coverage/coverage-floors.txt ("<module|web> <pct>" per
# line), raised upward only. A partial --bless preserves every other row's
# floor, and the comment header is carried through so a hand-written note
# survives.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
floors_file="${EVENER_COVFLOORS:-$repo_root/scripts/coverage/coverage-floors.txt}"
modules=". agent llm auth envvars invariant identifier fuzz"
tolerance="0.5"
check=false
bless=false
go_only=false
web_only=false
while [ $# -gt 0 ]; do
	case "$1" in
		--modules) modules="$2"; shift 2 ;;
		--tolerance) tolerance="$2"; shift 2 ;;
		--check) check=true; shift ;;
		--bless) bless=true; shift ;;
		--go-only) go_only=true; shift ;;
		--web-only) web_only=true; shift ;;
		-h|--help) awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done
if $go_only && $web_only; then
	echo "--go-only and --web-only are mutually exclusive" >&2
	exit 2
fi

. "$(dirname "${BASH_SOURCE[0]}")/../lib/gate-surface-lib.sh"
# How this run reclaims the leftovers of its own earlier runs; no janitor does.
. "$(dirname "${BASH_SOURCE[0]}")/../lib/covscratch-lib.sh"

# stmt_counts PROFILES... — print "covered total" per profile, one line each.
# A thin wrapper over the Go covstmt primitive (internal/devtool/covstmt), so
# this script's counting is the counting the repo's Go tests pin — not a
# Python duplicate free to drift from it. One invocation counts every track
# at once; the alternative is one `go run` per profile, three per module.
stmt_counts() {
	( cd "$repo_root" && go run ./cmd/evener-dev/bin dev covstmt "$@" )
}

floor_for() { awk -v m="$1" '$1==m {print $2}' "$floors_file" 2>/dev/null; }
measured_for() { awk -v m="$1" '$1==m {print $2}' "$measured_file" 2>/dev/null; }
# The parentheses are load-bearing: without them awk parses `t>0` inside printf's
# argument list as a redirection to a file named "0".
pct_of() { awk -v c="$1" -v t="$2" 'BEGIN{printf "%.1f", (t > 0 ? 100 * c / t : 0)}'; }

# check_measurable ROW WHY — a floored row that cannot be measured is an
# unenforced ratchet, not a skippable one. A row nobody floored keeps its
# advisory skip.
check_measurable() {
	$check || return 0
	local floor
	floor="$(floor_for "$1")"
	[ -n "$floor" ] || return 0
	echo "    UNMEASURED: $1 has a floor (${floor}%) but $2; the floor is not being enforced" >&2
	fail=1
}

# An explicit path under TMPDIR, not `mktemp -t`: macOS's mktemp ignores TMPDIR
# for -t and uses the Darwin per-user temp directory instead, which put every
# run's scratch outside the dev-tooling wave's per-suite isolation — and so
# outside the leftover check that is supposed to catch exactly this.
tmpbase=${TMPDIR:-/tmp}
# Reclaim what earlier runs of THIS script abandoned here, before taking a name
# of our own: a SIGKILLed run never reached its trap, and a failed run kept its
# scratch on purpose. Nothing else sweeps either. See covscratch-lib.sh.
reclaim_own_scratch "$tmpbase" evener-covfloor
# The name is chosen and the trap armed BEFORE the directory exists; see
# covscratch-lib.sh for the signal window this closes. $$ is unique among
# live processes, so concurrent runs cannot collide. A failed mkdir means a
# stale same-pid leftover this run does not own, so the trap is disarmed before
# exiting rather than deleting it.
work_dir="${tmpbase%/}/evener-covfloor.$$"
fail=0
# A clean run leaves nothing behind; a failed one keeps the profiles and logs,
# because the failure line printed their path.
cleanup_work() { [ "$fail" -eq 0 ] && rm -rf "$work_dir"; }
trap cleanup_work EXIT
mkdir "$work_dir" || { trap - EXIT; echo "coverage-floor: scratch $work_dir already exists or cannot be created" >&2; exit 1; }
measured_file="$work_dir/measured.txt"
: >"$measured_file"

printf '%-10s %10s %10s %8s %8s %8s %8s\n' "module" "covered" "total" "union%" "test%" "fuzz%" "floor"
if ! $web_only; then
for m in $modules; do
	[ -f "$repo_root/$m/go.mod" ] || { printf '%-10s %s\n' "$m" "(no module)"; check_measurable "$m" "no module exists at $m"; continue; }
	name="$m"
	[ "$name" = "." ] && name="root"
	base="$work_dir/$(printf '%s' "$name" | tr / _)"

	pkgs="$( cd "$repo_root/$m" && go list ./... 2>/dev/null | paste -sd, - )"
	if [ -z "$pkgs" ]; then
		printf '%-10s %s\n' "$m" "cannot list packages (see: cd $m && go list ./...)"
		fail=1; continue
	fi

	# -short everywhere except the root module, mirroring run-module-tests.sh
	# under ROOT_FULL=1.
	short="-short"
	[ "$m" = "." ] && short=""
	if ! ( cd "$repo_root/$m" && go test -count=1 $short -coverpkg="$pkgs" -coverprofile="$base.test.cov" \
		-run "$GATE_TEST_RUN" -skip "$GATE_FUZZ_TEST_SKIP" ./... ) >"$base.test.log" 2>&1; then
		printf '%-10s %s\n' "$m" "TEST TRACK FAILED (log: $base.test.log)"
		fail=1; continue
	fi
	# The fuzz track replays committed seed corpora only: `go test` without
	# -fuzz runs each target's seeds as ordinary subtests, which is deterministic
	# and is what make fuzz gates on.
	if ! ( cd "$repo_root/$m" && go test -tags evenerfuzz -count=1 -coverpkg="$pkgs" -coverprofile="$base.fuzz.cov" \
		-run '^Fuzz' ./... ) >"$base.fuzz.log" 2>&1; then
		printf '%-10s %s\n' "$m" "FUZZ TRACK FAILED (log: $base.fuzz.log)"
		fail=1; continue
	fi

	[ -f "$base.test.cov" ] || { printf '%-10s %s\n' "$m" "no test profile"; check_measurable "$m" "the test track wrote no coverage profile"; continue; }
	cat "$base.test.cov" "$base.fuzz.cov" 2>/dev/null >"$base.union.cov"

	# One Go invocation counts all three profiles (test, fuzz, union), in the
	# argument order the six read fields below consume. `read` stops at the
	# first newline, so the one-line-per-profile output is flattened to a
	# single line first.
	read -r tc tt fc ft uc ut < <(stmt_counts "$base.test.cov" "$base.fuzz.cov" "$base.union.cov" | tr '\n' ' ')
	[ "${ut:-0}" -gt 0 ] || { printf '%-10s %s\n' "$m" "no statements"; check_measurable "$m" "its profiles counted no statements"; continue; }

	# A union denominator above both tracks means some block was counted twice
	# because the two builds split it differently. A handful is expected and
	# harmless: under -tags evenerfuzz invariant.Hold becomes a real call, which
	# re-splits the blocks around it (8 statements in 33141 for the root module).
	# A MATERIAL divergence is a different basis, and its percentage would be
	# meaningless, so that still fails rather than being quietly reported.
	larger="$tt"
	[ "$ft" -gt "$larger" ] && larger="$ft"
	excess=$(( ut - larger ))
	variance=""
	if [ "$excess" -gt 0 ]; then
		if [ "$excess" -gt $(( larger / 100 )) ]; then
			printf '%-10s %s\n' "$m" "boundary mismatch: union counts $ut statements vs $tt test / $ft fuzz"
			fail=1; continue
		fi
		variance="  (+$excess boundary-variant)"
	fi

	pct="$(pct_of "$uc" "$ut")"
	echo "$m $pct" >>"$measured_file"
	floor="$(floor_for "$m")"
	printf '%-10s %10d %10d %7s%% %7s%% %7s%% %7s%s\n' \
		"$m" "$uc" "$ut" "$pct" "$(pct_of "$tc" "$tt")" "$(pct_of "$fc" "$ft")" "${floor:-—}" "$variance"

	if $check && [ -n "$floor" ]; then
		if [ "$(awk -v p="$pct" -v f="$floor" -v tol="$tolerance" 'BEGIN{print (p < f - tol) ? 1 : 0}')" = "1" ]; then
			echo "    REGRESSION: $m union coverage ${pct}% < floor ${floor}% (tolerance ${tolerance}pp)" >&2
			fail=1
		fi
	fi
done
fi

# The web row: the frontend's vitest line coverage, one total figure. The
# vitest config names the whole src/ tree in coverage.include, so a file no
# test ever loads counts as 0% instead of vanishing from the report.
if ! $go_only; then
	frontend="$repo_root/cmd/evener-hub/frontend"
	summary="$frontend/coverage/coverage-summary.json"
	if [ ! -d "$frontend" ]; then
		printf '%-10s %s\n' "web" "(no frontend)"
		check_measurable "web" "no frontend exists at $frontend"
	else
		# Delete the previous report before measuring: vitest writes a fresh one
		# on green and (via reportOnFailure) on red, so any summary present after
		# the run is this run's.
		rm -f "$summary"
		web_suite_failed=false
		if ! ( cd "$frontend" && npm run test:coverage ) >"$work_dir/web.log" 2>&1; then
			web_suite_failed=true
		fi
		if [ ! -f "$summary" ]; then
			printf '%-10s %s\n' "web" "SUITE FAILED (log: $work_dir/web.log)"
			fail=1
		else
			web_pct="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print("%.1f" % d["total"]["lines"]["pct"])' "$summary")"
			echo "web $web_pct" >>"$measured_file"
			floor="$(floor_for "web")"
			suffix=""
			$web_suite_failed && suffix=" (suite FAILED; reportOnFailure numbers)"
			printf '%-10s %19s %7s%% %17s %7s%s\n' "web" "—" "$web_pct" "" "${floor:-—}" "$suffix"
			if $web_suite_failed; then
				fail=1
			elif $check && [ -n "$floor" ]; then
				if [ "$(awk -v p="$web_pct" -v f="$floor" -v tol="$tolerance" 'BEGIN{print (p < f - tol) ? 1 : 0}')" = "1" ]; then
					echo "    REGRESSION: web line coverage ${web_pct}% < floor ${floor}% (tolerance ${tolerance}pp)" >&2
					fail=1
				fi
			fi
		fi
	fi
fi

if $bless; then
	tmp="$(mktemp "${TMPDIR:-/tmp}/evener-floors.XXXXXX")"
	{
		if grep -q '^#' "$floors_file" 2>/dev/null; then
			awk '/^#/{print; next} {exit}' "$floors_file"
		else
			echo "# Union (test track + fuzz track) whole-module statement-coverage floors, plus the frontend line-coverage row."
			echo "# Managed by scripts/coverage/coverage-floor.sh --bless. Raised upward only;"
			echo "# a downward reset (basis change, not a regression) is a hand edit."
		fi
		{ printf '%s\n' $modules; printf 'web\n'; awk '!/^#/ && NF {print $1}' "$floors_file" 2>/dev/null; } | sort -u | while read -r m; do
			[ -n "$m" ] || continue
			cur="$(measured_for "$m")"; old="$(floor_for "$m")"; keep="$cur"
			[ -n "$old" ] && keep="$(awk -v a="$old" -v b="${cur:-0}" 'BEGIN{print (a>b)?a:b}')"
			[ -n "$keep" ] && echo "$m $keep"
		done
	} >"$tmp"
	mv "$tmp" "$floors_file"
	echo "blessed floors -> $floors_file"
fi

exit $fail
