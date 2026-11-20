#!/usr/bin/env bash
# install-runtime-pair.sh PAIR_DIR IDENTITY_JSON DEST_DIR — verify the runtime
# pair the setup job built and copy it where the journeys run.
#
# WHY: the workflow's setup job builds this tree's runtime pair on its own
# runner while the app build runs, and hands the directory over. A pair built
# from another revision, or whose bytes changed after its manifest was written,
# must never run. The revision is compared with the build identity's
# sourceRevision (the commit the qualification runner checked out), then a
# fresh checksum of both binaries is compared with the manifest that shipped
# beside them. The copy is deliberate: the hub's daemons are exec'd from the
# run directory and carry its basename in argv, which is how cleanup proves it
# owns them.
#
# Both directories are absolute paths the workflow supplies: the extracted
# artifact directory and the run directory run-ui-tests.sh minted. A relative
# path is refused rather than resolved, because resolving one would mean
# trusting the caller's exported state (CDPATH, a symlink) for a handoff this
# workflow never makes.
#
# Exits 2 on usage errors, 1 on a pair that must not run, 0 once both binaries
# are in DEST_DIR.
set -euo pipefail

script_name=install-runtime-pair

if [ "$#" -ne 3 ]; then
	echo "usage: $0 PAIR_DIR IDENTITY_JSON DEST_DIR" >&2
	exit 2
fi
pair="$1"
identity="$2"
dest="$3"
for path in "$pair" "$dest"; do
	case "$path" in
	/*) ;;
	*)
		echo "$script_name: $path must be an absolute path" >&2
		exit 2
		;;
	esac
	if [ ! -d "$path" ]; then
		echo "$script_name: no directory at $path" >&2
		exit 2
	fi
done

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

# One directory for every step below: the manifest is compared and the copy is
# taken from the same place.
cd "$pair"
for binary in evener fixture-provider; do
	if [ ! -x "$binary" ]; then
		echo "$script_name: the runtime pair has no executable $binary in $pair" >&2
		exit 1
	fi
done
# The shipped manifest against a fresh sum of both binaries. A manifest that
# omits one of them and a pair whose bytes changed after it was written both
# fail the comparison, and its diff names the offending line.
fresh="$(shasum -a 256 evener fixture-provider)"
if [ "$(sort <<<"$fresh")" != "$(sort <SHA256SUMS)" ]; then
	echo "$script_name: the runtime pair does not match its SHA256SUMS manifest in $pair:" >&2
	diff -u <(sort <SHA256SUMS) <(sort <<<"$fresh") >&2 || true
	exit 1
fi

cp evener fixture-provider "$dest/"
