#!/bin/sh
# api-package-preflight.sh — own the appwire-client/typescript node_modules
# install for `make test-api-package`, so the qualification run is either given
# the install it needs or told the command to run.
#
# A fresh checkout has no appwire-client/typescript/node_modules. The
# qualification runner then fails inside `npm pack`'s `prepack` -> `npm run
# build` -> `tsc -p tsconfig.build.json`, because the pinned tsc is not there,
# and its own top-level `import { WebSocketServer } from "ws"` cannot resolve
# either: two missing devDependencies (typescript and ws) read as a broken
# package rather than a missing install. This repairs or refuses before the
# runner starts, the way web-preflight does for the frontend.
#
# npm ci installs exactly what's pinned in the committed package-lock.json;
# skip it when a real (non-symlinked) node_modules is already newer than the
# lockfile (a missing node_modules or a changed lockfile both trigger a fresh
# npm ci).
#
# A symlinked node_modules is another worktree's shared install. npm ci deletes
# an existing node_modules before installing, so running it through the link
# would delete the shared install for every worktree using it. Preflight
# compares the symlink target's own package-lock.json with this worktree's and
# refuses when they differ, before the -nt freshness shortcut (which follows
# symlinks) can skip the comparison entirely.
#
# EVENER_API_PACKAGE_DIR overrides which appwire-client/typescript directory
# this preflights, for pointing it at a throwaway install instead of the real
# one, whose node_modules may be a shared install.
set -eu

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
package=${EVENER_API_PACKAGE_DIR:-$repo_root/appwire-client/typescript}

# Canonicalize before cd, so a relative override resolves against the caller's
# directory rather than one this process has already left.
if canonical=$( (CDPATH='' cd -- "$package" && pwd) ) 2>/dev/null; then
	package=$canonical
fi
cd "$package"

if [ ! -f package-lock.json ]; then
	echo "ERROR: $package/package-lock.json is missing." >&2
	echo "  The install can only be checked against the committed lockfile;" >&2
	echo "  restore it from git before running test-api-package." >&2
	exit 1
fi

if [ -L node_modules ]; then
	target=$(readlink node_modules)
	case "$target" in
	/*) ;;
	*) target=$package/$target ;;
	esac
	shared=$(dirname "$target")
	if [ ! -f "$shared/package-lock.json" ] || ! cmp -s "$shared/package-lock.json" package-lock.json; then
		echo "ERROR: node_modules is a symlink to $target," >&2
		echo "  and $shared/package-lock.json does not match this worktree's." >&2
		echo "  npm ci would DELETE that shared install for every worktree using it." >&2
		echo "  Refresh the shared install in $shared, or give this worktree its own" >&2
		echo "  real node_modules — never npm ci through the symlink." >&2
		exit 1
	fi
elif [ node_modules -nt package-lock.json ]; then
	:
else
	npm ci
fi

# Prove the install is real: the qualification runner shells out to the local
# tsc and imports ws at load time, so both must come from this install. Asking
# the local tsc for its version proves it is the pinned TypeScript rather than a
# leftover empty tree; a bare or npx tsc would resolve the unrelated tsc@2.0.4
# package.
v=$(./node_modules/.bin/tsc --version 2>&1) || {
	echo "ERROR: $package/node_modules/.bin/tsc failed: $v" >&2
	echo "  Reinstall appwire-client/typescript's dependencies:" >&2
	echo "    cd $package && npm ci" >&2
	exit 1
}
case "$v" in
"Version "*) ;;
*)
	echo "ERROR: $package/node_modules is unhealthy ($(ls node_modules | wc -l | tr -d ' ') entries)." >&2
	echo "  ./node_modules/.bin/tsc printed: $v" >&2
	echo "  Reinstall appwire-client/typescript's dependencies:" >&2
	echo "    cd $package && npm ci" >&2
	exit 1
	;;
esac

if [ ! -e node_modules/ws ]; then
	echo "ERROR: $package/node_modules is unhealthy: the ws devDependency is missing." >&2
	echo "  The qualification runner imports ws to start its WebSocketServer and" >&2
	echo "  cannot load without it." >&2
	echo "  Reinstall appwire-client/typescript's dependencies:" >&2
	echo "    cd $package && npm ci" >&2
	exit 1
fi
