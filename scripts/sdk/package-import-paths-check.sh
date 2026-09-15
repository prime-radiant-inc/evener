#!/usr/bin/env bash
# package-import-paths-check.sh fails if any file in the app trees names the
# AppWire TypeScript package by path instead of by its package name.
#
# It is a substring grep, not a parser. A quoted literal anywhere in a swept
# file that contains `appwire-client/typescript` (the package by path) or
# `/protocol/` (the directory the package used to live behind) fails the gate.
# The rewriter's TypeScript reader is precise about which quoted strings are
# imports; this gate is deliberately broader, because the cost of missing one
# is a path import that typechecks and that nothing else in the tree notices.
# The price of the breadth: a quoted path in a COMMENT -- a commented-out
# import, an example in a doc comment -- is refused too, since grep cannot tell
# it from a live import. Spell the package name, or move the string out of the
# swept trees, or (for a config or a test that legitimately names the path) add
# it to the exact-path exemptions below.
#
# --root points the sweep at a fixture tree instead of this checkout; only the
# gate's own Go test passes it.
set -euo pipefail

root="$(cd -- "$(dirname -- "$0")/../.." && pwd)"
while [ "$#" -gt 0 ]; do
	case "$1" in
	--root)
		# An empty value is rejected rather than defaulted: `cd ""` succeeds and
		# changes nothing, so the sweep would silently run against whatever
		# directory the caller happened to be in.
		[ "$#" -ge 2 ] && [ -n "$2" ] || { printf 'package-import-paths-check.sh: --root needs a directory\n' >&2; exit 2; }
		root="$2"
		shift 2
		;;
	*)
		printf 'package-import-paths-check.sh: unknown argument %s (usage: [--root DIR])\n' "$1" >&2
		exit 2
		;;
	esac
done
cd "$root" || { printf 'package-import-paths-check.sh: cannot enter %s\n' "$root" >&2; exit 2; }

trees=(cmd/evener-hub/frontend/src mobile-native mobile/src)

# The extensions swept and the directories skipped are the resolvers' lists,
# not the ones that happen to exist today: the file this gate exists to catch
# is one nobody has written yet. Kept equal to the shared SOURCE_EXTENSIONS and
# SKIPPED_DIRS in scripts/sdk/source-files.mjs.
extensions=(ts tsx mts cts js jsx mjs cjs)
skip_dirs=(node_modules dist build ios android __snapshots__ .git)
sources=()
for extension in "${extensions[@]}"; do sources+=(--include="*.${extension}"); done
for dir in "${skip_dirs[@]}"; do sources+=(--exclude-dir="${dir}"); done

# The files that legitimately carry the path: the resolver configs that map the
# name onto it, and the test that proves they do. Exempted by exact relative
# path, so an app file that merely shares a config's basename in a subdirectory
# is not. A new such file has to be added here, which is the point.
exempt_configs=(
	mobile-native/vitest.config.mts
	mobile-native/metro.config.js
	mobile-native/src/metroResolver.test.ts
)

# `/protocol/` rather than `protocol`: the seam is only ever reached through a
# relative path, and a bare `protocol` substring matches @modelcontextprotocol
# and the "protocol" terminal-reason literal the app really does use. The
# closing quote is an alternative to the slash so that a specifier naming the
# directory itself -- `"../../protocol"` -- is caught too, and for the same
# reason the package directory needs no trailing slash.
seam='/protocol(/|["'"'"'`])'
package='appwire-client/typescript'

status=0

report() {
	printf '%s\n' "$2" >&2
	printf '%s\n' "$1" >&2
	status=1
}

# A tree that is not there sweeps nothing and says so silently, which reads as
# a pass. Every tree this gate claims to cover has to exist.
for tree in "${trees[@]}"; do
	if [ ! -d "$tree" ]; then
		printf 'package-import-paths-check.sh: %s does not exist under %s; this gate claims to sweep it\n' "$tree" "$root" >&2
		exit 2
	fi
done

# grep exits 1 for "no matches" and 2 or more for a real error -- an unreadable
# file, a bad pattern. Swallowing everything with `|| true` turned a broken
# sweep into a pass, which is the one verdict this gate must never invent.
#
# One sweep, two verdicts. A specifier still naming the directory the package
# moved out of is a subset of "names the package by path" -- it matches the
# same pattern -- but it earns its own message, because the fix is not "import
# it by name" so much as "that directory is gone".
old_seam=cmd/evener-hub/frontend/src/protocol/
# The exempt files are dropped by the exact path grep prints, anchored at the
# line start so an app file of the same basename in a subdirectory is not.
exempt_pattern=""
for config in "${exempt_configs[@]}"; do
	escaped="$(printf '%s' "$config" | sed 's/\./\\./g')"
	exempt_pattern="${exempt_pattern:+${exempt_pattern}|}${escaped}"
done
if found="$(grep "${sources[@]}" -rnE "[\"'\`][^\"'\`]*(${seam}|${package})" "${trees[@]}")"; then
	found="$(printf '%s\n' "$found" | grep -vE "^(${exempt_pattern}):" || true)"
	old_path="$(printf '%s\n' "$found" | grep -F "$old_seam" || true)"
	if [ -n "$old_path" ]; then
		report "$old_path" "these imports name the protocol directory the package moved out of; it no longer exists:"
	fi
	by_path="$(printf '%s\n' "$found" | grep -vF "$old_seam" || true)"
	if [ -n "$by_path" ]; then
		report "$by_path" "these imports name the AppWire package by path; import it as @evener/appwire-client, @evener/appwire-client/docContent, or @evener/appwire-client/testing/<module>:"
	fi
elif [ "$?" -ne 1 ]; then
	printf 'package-import-paths-check.sh: the sweep over %s failed\n' "${trees[*]}" >&2
	exit 2
fi

exit "$status"
