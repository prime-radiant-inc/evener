#!/usr/bin/env bash
# package-import-paths-check.sh fails if any app-tree module specifier reaches
# the AppWire TypeScript package by path instead of by its package name.
#
# Both halves of the sweep are load-bearing. The mobile half catches a stale
# `cmd/evener-hub/frontend/src/protocol/` specifier, which resolves to nothing
# now that the directory is gone. The web half catches the old relative
# `protocol/<module>` spelling, which is the form a rebased branch or a copied
# import line reintroduces -- silently, because a path import typechecks just
# as well as the package name and nothing else in the tree would notice.
#
# mobile-native/scripts/*.mts is the plan's named carve-out: those files run
# under tsx, which reads no tsconfig `paths`, so they keep a relative import
# into the package directory. They must still never name the old path.
#
# Usage:
#   scripts/sdk/package-import-paths-check.sh [--root DIR]
#
# --root points the sweep at a tree other than this checkout. It exists so the
# gate's own test can hold it against fixtures; nothing else passes it.
set -euo pipefail

root="$(cd -- "$(dirname -- "$0")/../.." && pwd)"
while [ "$#" -gt 0 ]; do
	case "$1" in
	--root)
		[ "$#" -ge 2 ] || { printf 'package-import-paths-check.sh: --root needs a directory\n' >&2; exit 2; }
		root="$2"
		shift 2
		;;
	*)
		printf 'package-import-paths-check.sh: unknown argument %s (usage: [--root DIR])\n' "$1" >&2
		exit 2
		;;
	esac
done
cd "$root"

trees=(cmd/evener-hub/frontend/src mobile-native mobile/src)
carve_out=mobile-native/scripts
# The resolver configs are excluded, and only they: mapping the package name
# onto its path is exactly what they are for, so a path there is the fix rather
# than the defect. Everything else in these trees is app or test code.
sources=(--include='*.ts' --include='*.tsx' --include='*.mts' --exclude='*.config.*' --exclude-dir=node_modules)

# A module specifier, not a mention in prose. Three positions carry one:
# `from "x"`, a bare `import "x"`, and a call taking the path as its first
# argument -- `import("x")`, `require("x")`, `vi.mock("x")`, and the rest of
# vitest's mocking family, which is why the call form is an identifier chain
# rather than a fixed list of names. Either quote style.
opener='(from[[:space:]]+|import[[:space:]]+|[A-Za-z_$][A-Za-z0-9_$.]*[[:space:]]*\([[:space:]]*)'
# `/protocol/` rather than `protocol`: the seam is only ever reached through a
# relative path, and a bare `protocol` substring matches @modelcontextprotocol
# and the "protocol" terminal-reason literal the app really does use. The
# package directory needs no trailing slash -- a specifier can name the
# directory itself.
seam='/protocol/'
package='appwire-client/typescript'

status=0

present() {
	for candidate in "$@"; do
		[ -d "$candidate" ] && printf '%s\n' "$candidate"
	done
}

report() {
	printf '%s\n' "$2" >&2
	printf '%s\n' "$1" >&2
	status=1
}

swept="$(present "${trees[@]}")"
if [ -n "$swept" ]; then
	by_path="$(printf '%s\n' "$swept" | tr '\n' '\0' |
		xargs -0 grep -rnE "${opener}[\"'][^\"']*(${seam}|${package})" "${sources[@]}" |
		grep -v "^${carve_out}/" || true)"
	if [ -n "$by_path" ]; then
		report "$by_path" "these imports name the AppWire package by path; import it as @evener/appwire-client, @evener/appwire-client/docContent, or @evener/appwire-client/testing/<module>:"
	fi
fi

if [ -d "$carve_out" ]; then
	old_path="$(grep -rnE "${opener}[\"'][^\"']*cmd/evener-hub/frontend/src${seam}" "$carve_out" "${sources[@]}" || true)"
	if [ -n "$old_path" ]; then
		report "$old_path" "these imports name the protocol directory the package moved out of; it no longer exists:"
	fi
fi

exit "$status"
