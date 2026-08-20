#!/usr/bin/env bash
# rapid-replay.sh — replays every registered rapid (seqfuzz/schemafuzz)
# surface against the fixed coverage seed bank: one run per target per seed in
# 1 2 3 5 8, with bounded checks and the failfile disabled, so the replay is
# deterministic. This is fuzz COVERAGE replay, not a search; it runs as one
# step of `make fuzz`.
#
# RUN_CAP, when set, names a repo-relative wrapper (scripts/fuzz/run-capped.sh)
# each per-seed go test runs under — the memory ceiling on hosts that have one.
set -eu

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
cd "$repo_root"

cap="${RUN_CAP:-}"
if [ -n "$cap" ]; then cap="$repo_root/$cap"; fi
go_work="$repo_root/go.work"

for target in $(scripts/fuzz/run-fuzz.sh --list | awk -F: '$1 == "rapid" { print $2 ":" $3 ":" $4 }'); do
	module=${target%%:*}
	rest=${target#*:}
	pkg=${rest%%:*}
	name=${rest#*:}
	for seed in 1 2 3 5 8; do
		echo "=== rapid replay $module:$name seed $seed ==="
		(cd "$module" && GOENV=off GOFLAGS= GOWORK="$go_work" env -u RAPID_FAILFILE EVENER_FUZZ_TESTS=1 RAPID_SEED="$seed" RAPID_CHECKS=100 RAPID_STEPS=30 RAPID_NOFAILFILE=true RAPID_LOG=false RAPID_V=false RAPID_DEBUG=false RAPID_DEBUGVIS=false RAPID_SHRINKTIME=30s ${cap:+"$cap"} go test -tags evenerfuzz -run "^${name}\$" -count=1 "$pkg")
	done
done
