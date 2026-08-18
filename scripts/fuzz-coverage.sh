#!/usr/bin/env bash
# fuzz-coverage.sh — measure each fuzz target's FOCUS-SET coverage by replaying
# its COMMITTED corpus under -coverprofile (no -fuzz, so deterministic), then
# hand the profiles to cmd/serf-fuzzcov for the report, ratchet, and gap map.
#
# The target list is NOT redefined here: it is read verbatim from
# `scripts/run-fuzz.sh --list`, the single source of truth. Each entry is
# "tag:module:pkg:name[:coverpkg[:focus]]"; coverpkg defaults to pkg. Only the
# "native" (testing.F) targets carry a focus-set coverage % and ratchet floor, so
# rapid surfaces are skipped here.
#
# Usage:
#   scripts/fuzz-coverage.sh            # advisory: print the report, exit 0
#   scripts/fuzz-coverage.sh --check    # ratchet + gap floor: exit non-zero on a breach
#   scripts/fuzz-coverage.sh --bless    # raise the ratchet floors to the current %
# Any flags are forwarded to serf-fuzzcov.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
# How this run reclaims the leftovers of its own earlier runs; no janitor does.
. "$(dirname "$0")/covscratch-lib.sh"
# An explicit path under TMPDIR, not `mktemp -t`: macOS's mktemp ignores TMPDIR
# for -t and uses the Darwin per-user temp directory instead, which puts the
# scratch outside the dev-tooling wave's per-suite isolation — and so outside
# the leftover check the trap below is written to satisfy.
tmpbase=${TMPDIR:-/tmp}
# Reclaim what earlier runs of THIS script abandoned here, before taking a name
# of our own. The trap below covers every exit a shell can observe; SIGKILL, an
# OOM kill and a power cut are not among them, and no janitor sweeps what they
# leave. See covscratch-lib.sh for the pid rules.
reclaim_own_scratch "$tmpbase" serf-fuzzcov
# The name is chosen and the trap armed BEFORE the directory exists; see
# test-coverage-floor.sh for the signal window this closes. $$ is unique among
# live processes, so concurrent runs cannot collide. A failed mkdir means a
# stale same-pid leftover this run does not own, so the trap is disarmed
# before exiting rather than deleting it.
profiles_dir="${tmpbase%/}/serf-fuzzcov.$$"
trap 'rm -rf "$profiles_dir"' EXIT
mkdir "$profiles_dir" || { trap - EXIT; echo "fuzz-coverage: cannot create scratch directory $profiles_dir" >&2; exit 1; }
manifest="$profiles_dir/manifest.tsv"

fail=0
while IFS=: read -r tag module pkg name cover focus; do
	[ "$tag" = native ] || continue
	[ -n "$name" ] || continue
	coverpkg="${cover:-$pkg}"
	profile="$profiles_dir/$name.cov"
	printf '=== %-44s ' "$module:$name"
	if out="$(cd "$repo_root/$module" && go test -tags serffuzz -run "^${name}\$" \
		-coverpkg="$coverpkg" -coverprofile="$profile" "$pkg" 2>&1)"; then
		# Echo just the "coverage: N% of statements" tail for a quick eyeball.
		printf '%s\n' "$(printf '%s\n' "$out" | grep -oE 'coverage: [0-9.]+% of statements[^,]*' | tail -1)"
	else
		printf 'FAIL\n%s\n' "$out"
		fail=1
	fi
	printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$module" "$pkg" "$name" "$coverpkg" "$focus" "$profile" >>"$manifest"
done < <(bash "$repo_root/scripts/run-fuzz.sh" --list)

if [ "$fail" -ne 0 ]; then
	echo "fuzz-coverage: one or more targets failed to replay; coverage numbers are incomplete." >&2
fi

echo
( cd "$repo_root" && go run ./cmd/serf-fuzzcov --manifest "$manifest" --repo-root "$repo_root" "$@" )
report=$?

# A target replay failure is itself a gate breach under --check.
if [ "$fail" -ne 0 ]; then
	for a in "$@"; do [ "$a" = "--check" ] && exit 1; done
fi
exit "$report"
