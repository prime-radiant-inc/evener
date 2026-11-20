#!/usr/bin/env bash
# install-runtime-pair.sh PAIR_DIR IDENTITY_JSON DEST_DIR — verify the runtime
# pair the setup job built and copy it where the journeys run.
#
# WHY: the workflow's setup job builds this tree's runtime pair on its own
# runner while the app build runs, and hands the directory over. A pair built
# from another revision, or whose bytes changed after its manifest was written,
# must never run, so the revision is compared with the build identity's
# sourceRevision (the commit the qualification runner checked out) and
# SHA256SUMS must name both binaries before shasum verifies them. The copy is
# deliberate: the hub's daemons are exec'd from the run directory and carry its
# basename in argv, which is how cleanup proves it owns them.
#
# Verification and copy use one physically resolved pair path, so a relative
# path, an exported CDPATH, or a symlink cannot make one step read a different
# directory from the other.
#
# Exits 2 on usage errors, 1 on a pair that must not run, 0 once both binaries
# are in DEST_DIR.
set -euo pipefail

script_name=install-runtime-pair

if [ "$#" -ne 3 ]; then
	echo "usage: $0 PAIR_DIR IDENTITY_JSON DEST_DIR" >&2
	exit 2
fi
identity="$2"

if [ ! -d "$1" ]; then
	echo "$script_name: no runtime pair directory at $1" >&2
	exit 1
fi
pair="$(CDPATH='' cd -P -- "$1" && pwd)"

if [ ! -d "$3" ]; then
	echo "$script_name: no destination directory at $3" >&2
	exit 2
fi
dest="$(CDPATH='' cd -P -- "$3" && pwd)"

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

cp "$pair/evener" "$dest/evener"
cp "$pair/fixture-provider" "$dest/fixture-provider"
