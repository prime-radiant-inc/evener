#!/usr/bin/env bash
# package-import-paths-check.sh fails if any app-tree import reaches the
# AppWire TypeScript package by path instead of by its package name.
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
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$repo_root"

trees=(cmd/evener-hub/frontend/src mobile-native mobile/src)
sources=(--include='*.ts' --include='*.tsx' --include='*.mts' --exclude-dir=node_modules)

# A module specifier, not a mention in prose: a quoted string immediately
# after `from`, `import` or `require`.
specifier_prefix='(from|import|require)[[:space:]]*\(?[[:space:]]*"[^"]*'

status=0

report() {
	printf '%s\n' "$2" >&2
	printf '%s\n' "$1" >&2
	status=1
}

by_path="$(grep -rnE "${specifier_prefix}(protocol/|appwire-client/typescript/)" "${trees[@]}" "${sources[@]}" |
	grep -v '^mobile-native/scripts/' || true)"
if [ -n "$by_path" ]; then
	report "$by_path" "these imports name the AppWire package by path; import it as @evener/appwire-client, @evener/appwire-client/docContent, or @evener/appwire-client/testing/<module>:"
fi

old_path="$(grep -rnE "${specifier_prefix}cmd/evener-hub/frontend/src/protocol/" mobile-native/scripts "${sources[@]}" || true)"
if [ -n "$old_path" ]; then
	report "$old_path" "these imports name the protocol directory the package moved out of; it no longer exists:"
fi

exit "$status"
