#!/usr/bin/env bash
# fuzz-coverage-global.sh — Plan 12 Phase D: measure WHOLE-CODEBASE fuzz-reachable
# coverage and ratchet it.
#
# The focus-set tool (fuzz-coverage.sh) answers "does each target cover its own
# decode seam?"; this answers the bigger question "what fraction of an entire
# module does the committed fuzz corpus reach?". For each module it replays every
# Fuzz target's committed corpus in ONE `go test -run ^Fuzz` pass with
# -coverpkg=./... (Go merges the coverage of all targets into a single profile),
# then reports covered/total statements per module and across the repo.
#
# READ THIS NUMBER CORRECTLY: it is FUZZ-REACHABLE coverage — what the fuzz corpus
# ALONE drives — NOT total test coverage. It is intentionally a minority of the
# module: fuzzing targets decode/parse/dispatch seams, while UNIT TESTS cover the
# request-building, client, error-handling, UI, and CLI code fuzzing never touches.
# The full test suite covers far more (measured: llm 39% fuzz vs 85% full-suite;
# agent 26% vs 86%). Use --with-full to print both side by side. The fuzz number's
# job is as a RATCHET (don't let fuzz reach regress), not a quality score.
#
# It enforces a no-regression ratchet against scripts/fuzzcov-global-floors.txt
# (per-module floors, raised only upward) so new code that isn't fuzz-reachable
# visibly lowers the global number instead of slipping in unnoticed.
#
# This is HEAVY (whole-module instrumentation + the full seed corpus) and LOCAL /
# on-demand — not a CI gate. Memory-capped via run-capped.sh.
#
# REQUIREMENT: -coverpkg=./... needs Go's prebuilt `covdata` tool to attribute
# coverage to packages without tests. Some downloaded toolchains omit it; if a
# module reports `no such tool "covdata"`, build it once into a writable GOTOOLDIR:
#   go build -o "$(go env GOTOOLDIR)/covdata" cmd/covdata
#
# Usage:
#   scripts/fuzz-coverage-global.sh                 # advisory report (exit 0)
#   scripts/fuzz-coverage-global.sh --check         # ratchet: exit non-zero on a drop
#   scripts/fuzz-coverage-global.sh --bless         # raise floors to current %
#   scripts/fuzz-coverage-global.sh --modules ". agent llm"
#
#   SERF_FUZZ_GO       the go binary (default: go) — a self-test seam.
#   SERF_FUZZ_CAPPED   the memory-cap wrapper (default: run-capped.sh) — test seam.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
floors_file="$repo_root/scripts/fuzzcov-global-floors.txt"
modules=". agent llm"
tolerance="0.5" # percentage-point band absorbing nondeterministic wobble
check=false
bless=false
with_full=false
go_bin="${SERF_FUZZ_GO:-go}"
capped="${SERF_FUZZ_CAPPED:-$repo_root/scripts/run-capped.sh}"

while [ $# -gt 0 ]; do
	case "$1" in
		--modules) modules="$2"; shift 2 ;;
		--modules=*) modules="${1#*=}"; shift ;;
		--tolerance) tolerance="$2"; shift 2 ;;
		--check) check=true; shift ;;
		--bless) bless=true; shift ;;
		--with-full) with_full=true; shift ;;
		-h|--help) sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) echo "fuzz-coverage-global: unknown arg $1" >&2; exit 2 ;;
	esac
done

profiles_dir="$(mktemp -d -t serf-fuzzcov-global.XXXXXX)"
trap 'rm -rf "$profiles_dir"' EXIT

# floor_for echoes the recorded floor for a module, or empty if none.
floor_for() {
	[ -f "$floors_file" ] || return 0
	awk -v m="$1" '$1==m {print $2}' "$floors_file"
}

# stmt_counts parses a Go text coverprofile and prints "covered total" statements.
# A block reads "file:range numStmts count". Under -coverpkg=./..., go test emits
# the SAME block once per package test binary (a block at a given position recurs
# with different counts), so we MUST dedupe by position: count each block's
# statements once, and treat it covered if ANY occurrence ran (the union, matching
# `go tool cover`). Summing every line instead inflates the total ~Nx (N = number
# of test binaries) and badly understates coverage.
stmt_counts() {
	awk 'NR>1 { pos=$1; stmts[pos]=$2; if ($3>0) cov[pos]=1 }
	     END { for (p in stmts) { t+=stmts[p]; if (p in cov) c+=stmts[p] }
	           printf "%d %d", c+0, t+0 }' "$1"
}

echo "=== global FUZZ-REACHABLE coverage — what the corpus alone drives, NOT total"
echo "    test coverage (the full suite covers far more); modules: $modules ==="
if $with_full; then
	printf '%-10s %10s %10s %9s %9s %7s\n' "module" "fuzz%" "full%" "fuzz/full" "floor" ""
else
	printf '%-10s %12s %12s %8s %8s\n' "module" "covered" "total" "fuzz%" "floor"
fi

repo_c=0 repo_t=0 repo_fc=0 repo_ft=0 fail=0
declare -A measured=()
for m in $modules; do
	prof="$profiles_dir/${m//\//_}.cov"
	# Replay every Fuzz target's committed corpus across the whole module under one
	# whole-module coverage profile. -run '^Fuzz' replays seeds (no -fuzz search).
	# Whole-module instrumentation over the full corpus is heavy on big modules;
	# give it a generous test timeout (override the go default 10m).
	if ! out="$( cd "$repo_root/$m" && "$capped" "$go_bin" test -tags serffuzz \
			-timeout 45m -run '^Fuzz' -coverpkg=./... -coverprofile="$prof" ./... 2>&1 )"; then
		# A build/test failure is fatal to the measurement. Surface the actual
		# cause (the FAIL/panic/error line), not just the trailing output.
		printf '%-10s %s\n' "$m" "MEASUREMENT FAILED:"
		if printf '%s\n' "$out" | grep -q 'no such tool "covdata"'; then
			echo "    go is missing the prebuilt 'covdata' tool that -coverpkg=./... needs for" >&2
			echo "    packages without tests. Build it into the toolchain (writable GOTOOLDIR):" >&2
			echo "      go build -o \"\$(go env GOTOOLDIR)/covdata\" cmd/covdata" >&2
		fi
		printf '%s\n' "$out" | grep -iE '^(FAIL|--- FAIL|panic|# )|build failed|no such tool|signal: killed|timed out' | head -6 | sed 's/^/    /'
		fail=1
		continue
	fi
	[ -f "$prof" ] || { printf '%-10s %s\n' "$m" "no coverage profile (no fuzz targets?)"; continue; }

	read -r c t < <(stmt_counts "$prof")
	[ "$t" -gt 0 ] || { printf '%-10s %s\n' "$m" "no statements measured"; continue; }
	pct="$(awk -v c="$c" -v t="$t" 'BEGIN{printf "%.1f", 100*c/t}')"
	measured["$m"]="$pct"
	repo_c=$((repo_c + c)); repo_t=$((repo_t + t))
	floor="$(floor_for "$m")"

	if $with_full; then
		# Best-effort full-suite pass for CONTEXT (not ratcheted): a flaky/env-gated
		# test failing here must not block the fuzz ratchet, so on failure show "—".
		fullprof="$profiles_dir/${m//\//_}.full.cov"
		if ( cd "$repo_root/$m" && "$capped" "$go_bin" test -timeout 45m \
				-coverpkg=./... -coverprofile="$fullprof" ./... ) >/dev/null 2>&1 && [ -f "$fullprof" ]; then
			read -r fc ft < <(stmt_counts "$fullprof")
			full_pct="$(awk -v c="$fc" -v t="$ft" 'BEGIN{ printf "%.1f", (t>0)?(100*c/t):0 }')"
			ratio="$(awk -v f="$pct" -v u="$full_pct" 'BEGIN{ r=(u>0)?(100*f/u):0; printf "%.0f%%", r }')"
			repo_fc=$((repo_fc + fc)); repo_ft=$((repo_ft + ft))
		else
			full_pct="—"; ratio="—"
		fi
		printf '%-10s %9s%% %9s%% %9s %8s\n' "$m" "$pct" "$full_pct" "$ratio" "${floor:-—}"
	else
		printf '%-10s %12d %12d %7s%% %7s\n' "$m" "$c" "$t" "$pct" "${floor:-—}"
	fi

	if $check && [ -n "$floor" ]; then
		below="$(awk -v p="$pct" -v f="$floor" -v tol="$tolerance" 'BEGIN{print (p < f - tol) ? 1 : 0}')"
		if [ "$below" = "1" ]; then
			echo "    REGRESSION: $m global coverage ${pct}% < floor ${floor}% (tolerance ${tolerance}pp)" >&2
			fail=1
		fi
	fi
done

if [ "$repo_t" -gt 0 ]; then
	repo_pct="$(awk -v c="$repo_c" -v t="$repo_t" 'BEGIN{printf "%.1f", 100*c/t}')"
	if $with_full && [ "$repo_ft" -gt 0 ]; then
		repo_full="$(awk -v c="$repo_fc" -v t="$repo_ft" 'BEGIN{printf "%.1f", 100*c/t}')"
		repo_ratio="$(awk -v f="$repo_pct" -v u="$repo_full" 'BEGIN{ r=(u>0)?(100*f/u):0; printf "%.0f%%", r }')"
		printf '%-10s %9s%% %9s%% %9s\n' "REPO" "$repo_pct" "$repo_full" "$repo_ratio"
	else
		printf '%-10s %12d %12d %7s%%\n' "REPO" "$repo_c" "$repo_t" "$repo_pct"
	fi
fi

if $bless; then
	tmp="$(mktemp)"
	{
		echo "# Global fuzz-reachable coverage floors (whole-module statement %)."
		echo "# Managed by scripts/fuzz-coverage-global.sh --bless. Raised upward only."
		for m in $modules; do
			cur="${measured[$m]:-}"
			old="$(floor_for "$m")"
			# Keep the higher of old/current so --bless never lowers a floor.
			keep="$cur"
			[ -n "$old" ] && keep="$(awk -v a="$old" -v b="${cur:-0}" 'BEGIN{print (a>b)?a:b}')"
			[ -n "$keep" ] && echo "$m $keep"
		done
	} >"$tmp"
	mv "$tmp" "$floors_file"
	echo "blessed floors -> $floors_file"
fi

exit "$fail"
