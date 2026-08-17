#!/usr/bin/env bash
# test-coverage-floor.sh — a no-regression ratchet on whole-module FULL-SUITE
# (unit+integration) statement coverage. The companion to fuzz-coverage-global.sh
# (which ratchets fuzz-REACHABLE coverage): this one guards the coverage the whole
# `go test` suite drives, so a PR that deletes tests or adds untested code fails
# the gate instead of silently eroding the numbers the coverage campaign won.
#
# It measures the SAME surface `ROOT_FULL=1 make test` proves — the contract
# make merge-approval-gate runs — by reusing the gate's own test selection from
# gate-surface-lib.sh: ordinary Test/Example functions, fuzz-owned names skipped,
# and -short on every module except the root. Matching the gate is the whole
# point: a floor blessed against a surface no gate reproduces cannot be defended
# when it moves, and bare `go test ./...` here also ran every native Fuzz seed
# corpus and the rapid/seqfuzz family, which are make fuzz's job.
#
# Each module is measured against ITS OWN packages (`go list ./...`), not against
# the `./...` filesystem pattern, which under go.work also matches every nested
# module beneath the root and made the root row a whole-repo number.
#
# The -count=1 is REQUIRED — cached coverage profiles report stale numbers. It
# dedups the -coverpkg duplicate blocks by position (a block is covered if ANY
# test hit it) before computing the percentage, the same way
# fuzz-coverage-global.sh does. A failing module keeps its full `go test` output
# in a log whose path is printed, rather than discarding it.
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
modules=". agent llm auth envvars invariant identifier"
tolerance="0.5"
check=false
bless=false
while [ $# -gt 0 ]; do
	case "$1" in
		--modules) modules="$2"; shift 2 ;;
		--tolerance) tolerance="$2"; shift 2 ;;
		--check) check=true; shift ;;
		--bless) bless=true; shift ;;
		# Print the header by scanning for its end, not by a hardcoded line range:
		# a range goes stale silently the first time the header grows or shrinks,
		# and this one had already drifted into printing the script body.
		-h|--help) awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

# The gate's test-selection surface, shared with run-module-tests.sh so this
# ratchet cannot drift into measuring something no gate proves.
. "$(dirname "${BASH_SOURCE[0]}")/gate-surface-lib.sh"
# How a coverage number is counted, shared with every other script that
# reports one; see covstmt-lib.sh for why a second copy is a hazard.
. "$(dirname "${BASH_SOURCE[0]}")/covstmt-lib.sh"

floor_for() { awk -v m="$1" '$1==m {print $2}' "$floors_file" 2>/dev/null; }

# Measured percentages accumulate in a "<module> <pct>" file rather than an
# associative array: macOS ships bash 3.2 and `env bash` is that, so `declare -A`
# is unavailable. Reusing the floor_for lookup shape keeps both reads identical.
measured_for() { awk -v m="$1" '$1==m {print $2}' "$measured_file" 2>/dev/null; }


# An explicit path under TMPDIR, not `mktemp -t`: macOS's mktemp ignores TMPDIR
# for -t and uses the Darwin per-user temp directory instead, which put every
# run's scratch outside the dev-tooling wave's per-suite isolation — and so
# outside the leftover check that is supposed to catch exactly this.
tmpbase=${TMPDIR:-/tmp}
# The name is chosen and the trap armed BEFORE the directory exists. With
# `mktemp -d` first and the trap a few lines later, a signal could land after
# the directory was created but before any cleanup knew about it — mid-mktemp
# the shell does not even hold the name yet — and the scratch was abandoned.
# The kill selftests signal a run the instant its scratch appears, which is
# exactly that window. $$ is unique among live processes, so concurrent runs
# cannot collide; a stale same-pid leftover makes the mkdir fail loudly.
profiles_dir="${tmpbase%/}/serf-testcov.$$"
fail=0
# A clean run leaves nothing behind; a failed one keeps the profiles and the
# per-module go test logs, because the failure line just printed their path and
# they are the only record of why. Without this the directory leaked on every
# invocation — including every selftest run, which the dev-tooling wave fails a
# suite for.
cleanup_profiles() { [ "$fail" -eq 0 ] && rm -rf "$profiles_dir"; }
trap cleanup_profiles EXIT
mkdir "$profiles_dir" || exit 1
measured_file="$profiles_dir/measured.txt"
: >"$measured_file"
printf '%-10s %12s %12s %8s %8s\n' "module" "covered" "total" "cov%" "floor"
for m in $modules; do
	[ -f "$repo_root/$m/go.mod" ] || { printf '%-10s %s\n' "$m" "(no module)"; continue; }
	name="$m"
	[ "$name" = "." ] && name="root"   # or the profile and log land as dotfiles
	prof="$profiles_dir/$(printf '%s' "$name" | tr / _).cov"
	log="$profiles_dir/$(printf '%s' "$name" | tr / _).log"
	# -coverpkg takes FILESYSTEM patterns, and under go.work `./...` matches every
	# package in the tree below the module — which for the root module means
	# agent/, llm/, auth/ and every other nested module too. The root row was
	# therefore a whole-repo figure diluted by code the root module's own tests
	# never run (50.9% against a 82603-statement denominator, versus 79.7% of the
	# 33141 statements it actually owns). `go list ./...` resolves within the
	# module, so it names this module's packages and nothing else.
	pkgs="$( cd "$repo_root/$m" && go list ./... 2>/dev/null | paste -sd, - )"
	if [ -z "$pkgs" ]; then
		printf '%-10s %s\n' "$m" "cannot list packages (see: cd $m && go list ./...)"
		fail=1; continue
	fi
	# -short everywhere except the root module, mirroring run-module-tests.sh's
	# module_test_flags under ROOT_FULL=1 — the surface make merge-approval-gate
	# proves. Bare `go test ./...` would additionally run every native Fuzz
	# target's seed corpus and the rapid/seqfuzz family, which belong to
	# make fuzz; measuring those here inflated the floor against a surface no
	# gate reproduces, and on this repo it simply failed.
	short="-short"
	[ "$m" = "." ] && short=""
	if ! ( cd "$repo_root/$m" && go test -count=1 $short -coverpkg="$pkgs" -coverprofile="$prof" \
		-run "$GATE_TEST_RUN" -skip "$GATE_FUZZ_TEST_SKIP" ./... ) >"$log" 2>&1; then
		printf '%-10s %s\n' "$m" "TEST FAILED (log: $log)"
		fail=1; continue
	fi
	[ -f "$prof" ] || { printf '%-10s %s\n' "$m" "no profile"; continue; }
	read -r c t < <(stmt_counts "$prof")
	[ "${t:-0}" -gt 0 ] || { printf '%-10s %s\n' "$m" "no statements"; continue; }
	pct="$(awk -v c="$c" -v t="$t" 'BEGIN{printf "%.1f", 100*c/t}')"
	echo "$m $pct" >>"$measured_file"
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
	tmp="$(mktemp "${TMPDIR:-/tmp}/serf-floors.XXXXXX")"
	{
		# Carry the file's existing comment header through instead of restating a
		# fixed one. A downward reset is a hand edit whose comment records WHY the
		# basis changed, and rewriting the header on every bless deleted that
		# reason the next time anyone raised a floor.
		if grep -q '^#' "$floors_file" 2>/dev/null; then
			awk '/^#/{print; next} {exit}' "$floors_file"
		else
			echo "# Full-suite (unit+integration) whole-module statement-coverage floors."
			echo "# Managed by scripts/test-coverage-floor.sh --bless. Raised upward only;"
			echo "# a downward reset (denominator change, not a regression) is a hand edit."
		fi
		# Bless the union of what was measured and what the file already holds, so
		# `--bless --modules agent` raises agent and LEAVES every other module's
		# floor intact. Restricting this loop to $modules silently deleted the
		# unmeasured floors, which made improving one module at a time — the normal
		# way coverage work happens — quietly drop the ratchet everywhere else.
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
