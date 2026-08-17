#!/usr/bin/env bash
# coverage-union.sh — how much of each module is exercised by ANY of this repo's
# deterministic tests: the union of the test track and the fuzz track.
#
# Neither existing ratchet answers that question, and both understate it on their
# own. scripts/test-coverage-floor.sh measures what `ROOT_FULL=1 make test`
# proves, whose -run '^(Test|Example)' filter deliberately excludes every fuzz
# target. scripts/fuzz-coverage-global.sh measures only what the fuzz corpus
# replays. The gap between them is not small and it is not noise: this repo keeps
# whole families of behavioural checks in `check*` functions that only a
# serffuzz-tagged "program" fuzz target calls, so the packages using that pattern
# read far lower on the test track than they really are (cmd/serf-hub/internal/
# appsource: 66.4% on the test track, 83.1% under its program target).
#
# Reading one track alone therefore either credits work that is not there or
# demands tests that already exist under the other tag. The union is the honest
# "is this code exercised at all" number, and the one to drive toward 100%.
#
# Both tracks are measured against the module's OWN packages (go list ./...), not
# `./...`, which under go.work is a filesystem pattern matching every nested
# module too. The two profiles are then concatenated and counted by position, so
# a block covered by either track counts once — see covstmt-lib.sh.
#
# Usage:
#   scripts/coverage-union.sh                     # measure + print
#   scripts/coverage-union.sh --check             # ratchet: non-zero on a drop
#   scripts/coverage-union.sh --bless             # raise floors to current %
#   scripts/coverage-union.sh --modules "agent llm"
#   scripts/coverage-union.sh --tolerance 0.5     # wobble band (default 0.5pp)
#
# Floors live in scripts/covunion-floors.txt ("<module> <pct>" per line), raised
# upward only. A partial --bless preserves every other module's floor, and the
# comment header is carried through so a hand-written note survives.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
floors_file="${SERF_COVUNION_FLOORS:-$repo_root/scripts/covunion-floors.txt}"
modules=". agent llm auth envvars invariant identifier fuzz"
tolerance="0.5"
check=false
bless=false
while [ $# -gt 0 ]; do
	case "$1" in
		--modules) modules="$2"; shift 2 ;;
		--tolerance) tolerance="$2"; shift 2 ;;
		--check) check=true; shift ;;
		--bless) bless=true; shift ;;
		-h|--help) awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

. "$(dirname "${BASH_SOURCE[0]}")/gate-surface-lib.sh"
. "$(dirname "${BASH_SOURCE[0]}")/covstmt-lib.sh"

floor_for() { awk -v m="$1" '$1==m {print $2}' "$floors_file" 2>/dev/null; }
measured_for() { awk -v m="$1" '$1==m {print $2}' "$measured_file" 2>/dev/null; }
# The parentheses are load-bearing: without them awk parses `t>0` inside printf's
# argument list as a redirection to a file named "0".
pct_of() { awk -v c="$1" -v t="$2" 'BEGIN{printf "%.1f", (t > 0 ? 100 * c / t : 0)}'; }

# An explicit path under TMPDIR, not `mktemp -t`: macOS's mktemp ignores TMPDIR
# for -t and uses the Darwin per-user temp directory instead, which put every
# run's scratch outside the dev-tooling wave's per-suite isolation — and so
# outside the leftover check that is supposed to catch exactly this.
tmpbase=${TMPDIR:-/tmp}
# The name is chosen and the trap armed BEFORE the directory exists; see
# test-coverage-floor.sh for the signal window this closes. $$ is unique among
# live processes, so concurrent runs cannot collide.
work_dir="${tmpbase%/}/serf-covunion.$$"
fail=0
# A clean run leaves nothing behind; a failed one keeps the profiles and logs,
# because the failure line printed their path.
cleanup_work() { [ "$fail" -eq 0 ] && rm -rf "$work_dir"; }
trap cleanup_work EXIT
mkdir "$work_dir" || exit 1
measured_file="$work_dir/measured.txt"
: >"$measured_file"

printf '%-10s %10s %10s %8s %8s %8s %8s\n' "module" "covered" "total" "union%" "test%" "fuzz%" "floor"
for m in $modules; do
	[ -f "$repo_root/$m/go.mod" ] || { printf '%-10s %s\n' "$m" "(no module)"; continue; }
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
	if ! ( cd "$repo_root/$m" && go test -tags serffuzz -count=1 -coverpkg="$pkgs" -coverprofile="$base.fuzz.cov" \
		-run '^Fuzz' ./... ) >"$base.fuzz.log" 2>&1; then
		printf '%-10s %s\n' "$m" "FUZZ TRACK FAILED (log: $base.fuzz.log)"
		fail=1; continue
	fi

	[ -f "$base.test.cov" ] || { printf '%-10s %s\n' "$m" "no test profile"; continue; }
	cat "$base.test.cov" "$base.fuzz.cov" 2>/dev/null >"$base.union.cov"

	read -r tc tt < <(stmt_counts "$base.test.cov")
	read -r fc ft < <(stmt_counts "$base.fuzz.cov")
	read -r uc ut < <(stmt_counts "$base.union.cov")
	[ "${ut:-0}" -gt 0 ] || { printf '%-10s %s\n' "$m" "no statements"; continue; }

	# A union denominator above both tracks means some block was counted twice
	# because the two builds split it differently. A handful is expected and
	# harmless: under -tags serffuzz invariant.Hold becomes a real call, which
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

if $bless; then
	tmp="$(mktemp "${TMPDIR:-/tmp}/serf-floors.XXXXXX")"
	{
		if grep -q '^#' "$floors_file" 2>/dev/null; then
			awk '/^#/{print; next} {exit}' "$floors_file"
		else
			echo "# Union (test track + fuzz track) whole-module statement-coverage floors."
			echo "# Managed by scripts/coverage-union.sh --bless. Raised upward only;"
			echo "# a downward reset (basis change, not a regression) is a hand edit."
		fi
		{ printf '%s\n' $modules; awk '!/^#/ && NF {print $1}' "$floors_file" 2>/dev/null; } | sort -u | while read -r m; do
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
