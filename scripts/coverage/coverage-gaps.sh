#!/usr/bin/env bash
# coverage-gaps.sh — rank where a Go coverage profile's UNCOVERED statements are,
# so coverage work targets the largest real gaps instead of whatever file is open.
#
# Ranking by uncovered COUNT, not by percentage, is the point: a 40%-covered file
# holding 12 statements is noise next to a 90%-covered one holding 900. Percentage
# ranking sends you to the former every time.
#
# Usage:
#   scripts/coverage/coverage-gaps.sh PROFILE                  # top packages by uncovered stmts
#   scripts/coverage/coverage-gaps.sh PROFILE --by file        # ...by file
#   scripts/coverage/coverage-gaps.sh PROFILE --top 40
#   scripts/coverage/coverage-gaps.sh PROFILE --by file --zero # only wholly-uncovered units
#   scripts/coverage/coverage-gaps.sh PROFILE --in session.go  # uncovered blocks IN a file
#
# PROFILE is a `go test -coverprofile` file. Generate one with, e.g.
#   prof="$(mktemp "${TMPDIR:-/tmp}/evener-cov.XXXXXX")"
#   go test -count=1 -short -coverpkg="$(go list ./... | paste -sd, -)" \
#     -coverprofile="$prof" -run "$GATE_TEST_RUN" -skip "$GATE_FUZZ_TEST_SKIP" ./...
#   scripts/coverage/coverage-gaps.sh "$prof"
# (the same selection and module scoping `evener dev coverage-floor` measures;
# see scripts/lib/gate-surface-lib.sh).
#
# Duplicate blocks from -coverpkg are deduped by position, a block counting as
# covered if ANY test hit it — the same accounting `coverage-floor.sh` uses.
# Both scripts count through the Go covstmt primitive (`evener dev covstmt`),
# so the totals here reconcile with the floors and a block's coverage is decided
# in exactly one place. This script keeps the flags, usage, and validation; the
# ranking report itself is `evener dev covstmt --gaps`.
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

# CDPATH='' and `--`: an inherited CDPATH makes `cd` ECHO the resolved directory
# into the command substitution (a multiline path), and `--` keeps a path
# beginning with `-` from being read as an option. Each resolution is checked so
# a failed `cd` aborts with a clear error instead of a empty/garbled path.
repo_root="$(CDPATH='' cd -- "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)" \
	|| { echo "coverage-gaps.sh: cannot resolve the repo root" >&2; exit 1; }

# The Go subcommand runs with repo_root as its cwd (so `go run` finds the
# module), which would reinterpret a relative profile path; make it absolute
# here, while the caller's cwd is still in effect.
profile_dir="$(CDPATH='' cd -- "$(dirname "$profile")" && pwd)" \
	|| { echo "coverage-gaps.sh: cannot resolve the directory of profile $profile" >&2; exit 1; }
profile="$profile_dir/$(basename "$profile")"

gaps_args=(--gaps "--by=$by" "--top=$top")
if $zero_only; then gaps_args+=(--zero); fi
if [ -n "$in_pattern" ]; then gaps_args+=("--in=$in_pattern"); fi

( cd "$repo_root" && go run ./cmd/evener-dev/bin dev covstmt "${gaps_args[@]}" "$profile" )
