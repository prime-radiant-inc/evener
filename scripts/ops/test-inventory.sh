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
# vitest runs `npx vitest list --json` on the FILEs (paths relative to the
#        repo root) from the frontend directory, or from mobile-native for
#        files under it, and prints one test name per line, sorted, with file
#        names left out so a move between files does not show up as a
#        change. It fails when a FILE contributes no tests (for example a file
#        its vitest config excludes), so a lost file cannot read as equality.
# diff   prints names only in the first file as "-" and only in the second as
#        "+". Exit 0 when identical, 1 when they differ. Duplicate names count
#        (the lists are compared as multisets).
# Exit 2: usage error or the listing failed (the message says why).
set -euo pipefail

usage() { sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
die() { echo "test-inventory: $*" >&2; exit 2; }

root="$(cd "$(dirname "$0")/../.." && pwd -P)"

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
    scratch="${TMPDIR:-/tmp}"
    tmp="$(mktemp "$scratch/test-inventory.XXXXXX")"; names="$(mktemp "$scratch/test-inventory.XXXXXX")"
    trap 'rm -f "$tmp" "$names"' EXIT
    # Absolute, normalized paths: vitest reports absolute file names, and the
    # tree is chosen by prefix.
    abs=()
    for f in "$@"; do
      [ -f "$root/$f" ] || die "no such file (relative to the repo root): $f"
      abs+=("$(cd "$(dirname "$root/$f")" && pwd -P)/$(basename "$f")")
    done
    # Each test tree has its own vitest config, and a config silently skips
    # files outside its include globs, so list per tree and require that every
    # requested file shows up in its tree's listing.
    for tree in mobile-native cmd/evener-hub/frontend; do
      files=()
      for f in "${abs[@]}"; do
        case "$f" in
          "$root"/mobile-native/*) [ "$tree" = mobile-native ] && files+=("$f") ;;
          *) [ "$tree" = cmd/evener-hub/frontend ] && files+=("$f") ;;
        esac
      done
      [ ${#files[@]} -gt 0 ] || continue
      # `--json` takes an output path, not a flag: without `=FILE` vitest would
      # treat the first test file as the path and overwrite it.
      (cd "$root/$tree" && npx vitest list --json="$tmp" "${files[@]}" >/dev/null) || die "vitest list failed in $tree"
      for f in "${files[@]}"; do
        jq -e --arg f "$f" 'any(.[]; .file == $f)' "$tmp" >/dev/null || die "vitest listed no tests from $f (excluded by $tree's vitest config, or it has no tests)"
      done
      jq -r '.[] | select(.file as $f | $ARGS.positional | index($f)) | .name' "$tmp" --args "${files[@]}" >> "$names"
    done
    LC_ALL=C sort "$names"
    ;;
  diff)
    [ $# -eq 2 ] || usage
    [ -f "$1" ] && [ -f "$2" ] || die "both arguments must be files"
    scratch="${TMPDIR:-/tmp}"
    tmp1="$(mktemp "$scratch/test-inventory.XXXXXX")"; tmp2="$(mktemp "$scratch/test-inventory.XXXXXX")"
    trap 'rm -f "$tmp1" "$tmp2"' EXIT
    LC_ALL=C sort "$1" > "$tmp1" || die "cannot read $1"
    LC_ALL=C sort "$2" > "$tmp2" || die "cannot read $2"
    status=0
    d="$(LC_ALL=C diff "$tmp1" "$tmp2")" || status=$?
    [ "$status" -le 1 ] || die "diff failed"
    if [ "$status" -eq 1 ]; then printf '%s\n' "$d" | sed -n -e 's/^< /- /p' -e 's/^> /+ /p'; exit 1; fi
    ;;
  *) echo "unknown command: $cmd" >&2; usage ;;
esac
