#!/usr/bin/env bash
# fuzz-gap-check.sh — the FAST, STATIC gap gate. It asserts that every
# decode/parse package in the workspace has a registered fuzz target (or a
# reasoned ignore-list entry), WITHOUT replaying any corpus.
#
# Unlike `make fuzz-coverage CHECK=1` (which replays every target's committed
# corpus under -coverprofile — slow, and is the local/manual ratchet tool), this
# derives the fuzzed package set purely from scripts/run-fuzz.sh --list, so it
# runs in seconds and is deterministic — safe as a blocking PR gate. It fails the
# moment a new parse package lands without a target.
#
# Usage:
#   scripts/fuzz-gap-check.sh        # exit non-zero on an un-targeted parse package
# Any extra flags are forwarded to serf-fuzzcov.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
registry="$(mktemp -t serf-fuzz-registry.XXXXXX)"
trap 'rm -f "$registry"' EXIT

bash "$repo_root/scripts/run-fuzz.sh" --list >"$registry"
( cd "$repo_root" && go run ./cmd/serf-fuzzcov -gap-only -registry "$registry" -repo-root "$repo_root" "$@" )
