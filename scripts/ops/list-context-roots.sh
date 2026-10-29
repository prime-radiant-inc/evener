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
# INHERIT? means the enclosing top-level func takes a context.Context
# parameter (receiver and result types do not count; a func literal inside it
# is covered too). It is a heuristic: a method that reads a ctx from its
# receiver or a one-line func literal parameter is not detected.
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

# paramList returns the parenthesized parameter list of a Go func signature:
# it skips a method receiver group, then the name, then reads one balanced
# group, so a context.Context in the receiver or the result types does not
# count.
git ls-files -z -- '*.go' ':!:*_test.go' ':!:**/testdata/**' \
  | xargs -0 awk '
    function paramList(sig,    i, depth, start, c, skipped) {
      i = 6                                  # past "func "
      skipped = 0
      while (i <= length(sig)) {
        c = substr(sig, i, 1)
        if (c == "(") {
          start = i; depth = 0
          for (; i <= length(sig); i++) {
            c = substr(sig, i, 1)
            if (c == "(") depth++
            else if (c == ")" && --depth == 0) break
          }
          if (skipped || substr(sig, 6, 1) != "(") return substr(sig, start, i - start + 1)
          skipped = 1                        # that group was the receiver
        }
        i++
      }
      return ""
    }
    FNR == 1 { fn = ""; hasctx = 0; insig = 0 }
    /^func / { sig = ""; insig = 1; fn = "" }
    insig {
      sig = sig (sig == "" ? "" : " ") $0
      if ($0 ~ /\{/ && $0 !~ /^\t/ && ($0 ~ /\{[ \t]*$/ || $0 ~ /\}[ \t]*$/)) {
        insig = 0
        hasctx = (paramList(sig) ~ /context\.Context/)
        fn = sig; sub(/^func /, "", fn); sub(/[ \t]*\{.*$/, "", fn)
      }
    }
    /context\.(Background|TODO)\(\)/ && $0 !~ /^[ \t]*\/\// {
      line = $0; gsub(/^[ \t]+/, "", line)
      printf "%s\t%s:%d\t%s\t%s\n", (hasctx ? "INHERIT?" : "root?"), FILENAME, FNR, fn, line
    }' \
  | {
    case $mode in
      all) cat ;;
      inherit) awk -F '\t' '$1 == "INHERIT?"' ;;
      count) cut -f1 | sort | uniq -c ;;
    esac
  }
