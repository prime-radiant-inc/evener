#!/usr/bin/env bash
# fuzz-coverage.sh — measure each fuzz target's FOCUS-SET coverage by replaying
# its COMMITTED corpus under -coverprofile (no -fuzz, so deterministic), then
# hand the profiles to cmd/serf-fuzzcov for the report, ratchet, and gap map.
#
# The target list is NOT redefined here: it is read verbatim from
# `scripts/run-fuzz.sh --list`, the single source of truth. Each entry is
# "module:pkg:name[:coverpkg[:focus]]"; coverpkg defaults to pkg.
#
# Usage:
#   scripts/fuzz-coverage.sh            # advisory: print the report, exit 0
#   scripts/fuzz-coverage.sh --check    # ratchet + gap floor: exit non-zero on a breach
#   scripts/fuzz-coverage.sh --bless    # raise the ratchet floors to the current %
# Any flags are forwarded to serf-fuzzcov.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
profiles_dir="$(mktemp -d -t serf-fuzzcov.XXXXXX)"
manifest="$profiles_dir/manifest.tsv"
trap 'rm -rf "$profiles_dir"' EXIT

fail=0
while IFS=: read -r module pkg name cover focus; do
	[ -n "$name" ] || continue
	coverpkg="${cover:-$pkg}"
	profile="$profiles_dir/$name.cov"
	printf '=== %-44s ' "$module:$name"
	if out="$(cd "$repo_root/$module" && go test -run "^${name}\$" \
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
