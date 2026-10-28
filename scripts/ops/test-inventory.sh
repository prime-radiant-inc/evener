#!/usr/bin/env bash
# test-inventory.sh: list the test names a Go package or a set of vitest files
# defines, and diff two such lists.
#
# Use it to prove a test-file split or move lost no test: save the list
# before, move the tests, save it again, and diff. Attach the (empty) diff to
# the PR. A moved test keeps its name, so the name set must be identical.
#
#   scripts/ops/test-inventory.sh go PKGDIR [-tags TAGS]   > before.txt
#   scripts/ops/test-inventory.sh vitest FILE...           > before.txt
#   scripts/ops/test-inventory.sh diff before.txt after.txt
#
# go     runs `go test -list` in PKGDIR and prints one Test/Fuzz/Example/
#        Benchmark name per line, sorted.
# vitest runs `npx vitest list --json` from the frontend directory on the
#        FILEs (paths relative to the repo root) and prints
#        one test name per line, sorted, with file names left out so a move
#        between files does not show up as a change.
# diff   prints names only in the first file as "-" and only in the second as
#        "+". Exit 0 when identical, 1 when they differ. Duplicate names count
#        (the lists are compared as multisets).
# Exit 2: usage error or the listing failed (the message says why).
set -euo pipefail

usage() { sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
die() { echo "test-inventory: $*" >&2; exit 2; }

root="$(cd "$(dirname "$0")/../.." && pwd)"

[ $# -ge 1 ] || usage
cmd="$1"; shift
case "$cmd" in
  -h|--help) usage ;;
  go)
    [ $# -ge 1 ] || usage
    pkg="$1"; shift
    tags=()
    if [ "${1:-}" = "-tags" ]; then [ $# -ge 2 ] || usage; tags=(-tags "$2"); shift 2; fi
    [ $# -eq 0 ] || usage
    [ -d "$root/$pkg" ] || [ -d "$pkg" ] || die "no such directory: $pkg"
    dir="$pkg"; [ -d "$dir" ] || dir="$root/$pkg"
    out="$(cd "$dir" && go test ${tags[@]+"${tags[@]}"} -list '.*' . 2>&1)" || die "go test -list failed:
$out"
    printf '%s\n' "$out" | grep -E '^(Test|Fuzz|Example|Benchmark)' | LC_ALL=C sort
    ;;
  vitest)
    [ $# -ge 1 ] || usage
    command -v jq >/dev/null || die "jq is required"
    files=()
    for f in "$@"; do
      [ -f "$root/$f" ] || die "no such file (relative to the repo root): $f"
      files+=("$root/$f")
    done
    # `--json` takes an output path, not a flag: without `=FILE` vitest would
    # treat the first test file as the path and overwrite it.
    tmp="$(mktemp)"
    trap 'rm -f "$tmp"' EXIT
    (cd "$root/cmd/evener-hub/frontend" && npx vitest list --json="$tmp" "${files[@]}" >/dev/null) || die "vitest list failed"
    jq -r '.[].name' "$tmp" | LC_ALL=C sort
    ;;
  diff)
    [ $# -eq 2 ] || usage
    [ -f "$1" ] && [ -f "$2" ] || die "both arguments must be files"
    d="$(LC_ALL=C diff <(LC_ALL=C sort "$1") <(LC_ALL=C sort "$2") | sed -n -e 's/^< /- /p' -e 's/^> /+ /p' || true)"
    if [ -n "$d" ]; then printf '%s\n' "$d"; exit 1; fi
    ;;
  *) echo "unknown command: $cmd" >&2; usage ;;
esac
