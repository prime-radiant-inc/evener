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
# Every file in the three trees is swept; nothing is exempt but the resolver
# configs named below.
#
# On --include/--exclude/--exclude-dir being GNU-only: measured 2026-09-14 on
# macOS 26 against `grep (BSD grep, GNU compatible) 2.6.0-FreeBSD`, the /usr/bin
# grep this script invokes -- all three are accepted and filter correctly, and
# CI's ubuntu runners use GNU grep, where they originate.
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
# The resolver configs are excluded by name, and only they: mapping the package
# name onto its path is exactly what a resolver config is for, so a path there
# is the fix rather than the defect. Named rather than matched as *.config.*,
# which would also excuse any app module someone happened to call
# something.config.ts. Only mobile-native/vitest.config.mts is inside these
# trees today; the frontend's vite and browser-guard configs sit beside src/,
# not in it, and are not swept at all.
sources=(
	--include='*.ts' --include='*.tsx' --include='*.mts'
	--exclude='vite.config.*' --exclude='vitest.config.*' --exclude='metro.config.*'
	--exclude-dir=node_modules
)

# A module specifier, not a mention in prose. Three positions carry one:
# `from "x"`, a bare `import "x"`, and a call taking the path as its first
# argument -- `import("x")`, `require("x")`, `vi.mock("x")`, and the rest of
# vitest's mocking family, which is why the call form is an identifier chain
# rather than a fixed list of names. Any of the three quotes: a specifier with
# nothing to interpolate is as spellable in backticks as in quotes.
# The last alternative is the specifier that opens its own line, which is what
# a call or an import broken across lines leaves behind:
#
#     vi.mock(
#       "../../protocol/reducer",
#
# grep is line-oriented and the portable flags it has cannot see the line
# above, so the continuation is matched on its own shape instead. In these
# trees a line that begins with a quoted path IS a module specifier; the sweep
# finds none today that is not.
opener='(from[[:space:]]+|import[[:space:]]+|[A-Za-z_$][A-Za-z0-9_$.]*[[:space:]]*\([[:space:]]*|^[[:space:]]*)'
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
# Written inline rather than through a helper, because a helper runs inside a
# command substitution, where `exit` leaves only the subshell and the failure
# reads as "no matches" again.
#
# The option arrays come before the pattern and the operands: BSD and GNU grep
# both accept options after operands, but only by permuting them, and a file
# named like an option would be read as one.
#
# One sweep, two verdicts. A specifier still naming the directory the package
# moved out of is a subset of "names the package by path" -- it matches the
# same pattern -- but it earns its own message, because the fix is not "import
# it by name" so much as "that directory is gone".
old_seam=cmd/evener-hub/frontend/src/protocol/
if found="$(grep "${sources[@]}" -rnE "${opener}[\"'\`][^\"'\`]*(${seam}|${package})" "${trees[@]}")"; then
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
