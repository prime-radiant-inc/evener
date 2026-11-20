#!/usr/bin/env bash
# verify-runtime-pair.sh PAIR_DIR IDENTITY_JSON — refuse a runtime pair that was
# not built from the tree the journeys are about to run.
#
# WHY: the workflow's setup job builds this tree's runtime pair on its own
# runner while the app build runs, and hands the directory over. A pair built
# from another revision, or whose bytes changed after its manifest was written,
# must never run, so the revision is compared with the build identity's
# sourceRevision (the commit the qualification runner checked out) and
# SHA256SUMS must name both binaries before shasum verifies them.
#
# Exits 2 on usage errors, 1 on a pair that must not run, 0 when it is safe.
set -euo pipefail

script_name=verify-runtime-pair

if [ "$#" -ne 2 ]; then
	echo "usage: $0 PAIR_DIR IDENTITY_JSON" >&2
	exit 2
fi
pair="$1"
identity="$2"

head_revision="$(jq -r '.sourceRevision // empty' "$identity")"
pair_revision="$(cat "$pair/revision" 2>/dev/null || true)"
if [ -z "$head_revision" ] || [ "$pair_revision" != "$head_revision" ]; then
	echo "$script_name: the runtime pair was built from '${pair_revision:-no revision file}', not this tree's '${head_revision:-no sourceRevision in the build identity}'" >&2
	exit 1
fi
if [ ! -f "$pair/SHA256SUMS" ]; then
	echo "$script_name: the runtime pair has no SHA256SUMS manifest in $pair" >&2
	exit 1
fi
for binary in evener fixture-provider; do
	if [ ! -x "$pair/$binary" ]; then
		echo "$script_name: the runtime pair has no executable $binary in $pair" >&2
		exit 1
	fi
	# shasum -c only proves the entries it finds; a manifest that omits a
	# binary would leave that binary unverified.
	if ! grep -Eq "^[0-9a-f]{64} [ *]$binary$" "$pair/SHA256SUMS"; then
		echo "$script_name: the runtime pair's SHA256SUMS manifest has no checksum for $binary" >&2
		exit 1
	fi
done
if ! checksums="$( cd "$pair" && shasum -a 256 -c SHA256SUMS 2>&1 )"; then
	echo "$script_name: the runtime pair does not match its SHA256SUMS manifest in $pair:" >&2
	echo "$checksums" >&2
	exit 1
fi
