#!/bin/sh
# Initial scripts/check.sh in both fixtures, invoked from the fixture root.
set -eu
test -f go.mod || { printf '%s\n' 'Run this checker from the repository root' >&2; exit 2; }
test "$#" -eq 0 || { printf '%s\n' 'This checker takes no arguments' >&2; exit 2; }
exec go test ./...
