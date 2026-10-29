#!/usr/bin/env bash
# list-context-roots.sh — list every non-test context.Background() / context.TODO()
# call and flag the ones that sit in a function which already has a context to
# inherit.
#
# Why this exists: a fresh root context inside a request or turn path means
# cancel and shutdown never reach the work. Each site is one of: a root owner
# (main, init, a daemon's lifetime), detached on purpose (cleanup that must
# outlive the caller's cancellation), or a bug (should inherit the caller's
# ctx). This script does the mechanical half: it finds the sites and says which
# have a ctx to inherit. Deciding root vs detached vs bug stays a human read.
#
# Usage:
#   scripts/ops/list-context-roots.sh            # every site, "INHERIT?" marks candidates
#   scripts/ops/list-context-roots.sh --inherit  # only the candidates (enclosing func takes a ctx)
#   scripts/ops/list-context-roots.sh --count    # per-category totals
#   scripts/ops/list-context-roots.sh --help
#
# Output columns: marker, file:line, enclosing func, source line.
# INHERIT? means the enclosing top-level func's signature mentions a
# context.Context parameter (a func literal inside it is covered too). It is
# a heuristic: a method that reads a ctx from its receiver is not detected.
set -uo pipefail

mode=all
case "${1:-}" in
  -h|--help) sed -n '2,/^set -uo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
  --inherit) mode=inherit ;;
  --count) mode=count ;;
  "") ;;
  *) echo "unknown argument: $1 (try --help)" >&2; exit 2 ;;
esac

root=$(git rev-parse --show-toplevel) || { echo "run inside the evener checkout" >&2; exit 1; }
cd "$root" || exit 1

git ls-files -z -- '*.go' ':!:*_test.go' ':!:**/testdata/**' \
  | xargs -0 awk '
    FNR == 1 { fn = ""; hasctx = 0 }
    /^func / { fn = $0; hasctx = (fn ~ /context\.Context/) }
    /context\.(Background|TODO)\(\)/ && $0 !~ /^[ \t]*\/\// {
      name = fn; sub(/^func /, "", name); sub(/\{[ \t]*$/, "", name)
      line = $0; gsub(/^[ \t]+/, "", line)
      printf "%s\t%s:%d\t%s\t%s\n", (hasctx ? "INHERIT?" : "root?"), FILENAME, FNR, name, line
    }' \
  | sort -t$'\t' -k2,2V \
  | {
    case $mode in
      all) cat ;;
      inherit) grep -P '^INHERIT\?\t' ;;
      count) cut -f1 | sort | uniq -c ;;
    esac
  }
