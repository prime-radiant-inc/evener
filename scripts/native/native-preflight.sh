#!/bin/sh
# native-preflight.sh — own the mobile-native node_modules check for the native
# targets, so `make test-native` and `make test-native-bundle` share one
# definition of "the install is ready".
#
# In a fresh worktree with no `npm ci` in mobile-native, `npx expo` resolves to
# whatever expo is on the PATH (a global 57) instead of the pinned one, and
# Metro fails with `CommandError: No platforms are configured to use the Metro
# bundler` — an error about platforms, not about the install that is missing.
# This refuses before Metro runs, naming the directory and the command to run.
#
# A symlinked node_modules is an agent worktree's shared install. Its mtime says
# nothing about this worktree (a fresh worktree's lockfile is always newer than
# the shared install it points at), so a symlink is settled on its shared
# lockfile's CONTENT, and that comparison runs before any mtime test: mtime must
# never wave a mismatched shared tree through. The symlink branch never suggests
# npm ci through the link — that would delete the shared install for every
# worktree using it.
#
# Existence and freshness are not health. An empty or half-installed tree, or a
# symlink whose target directory is gone, passes both and still makes Metro fail
# with the error above, so the last check proves the toolchain the bundler will
# use: the pinned expo's own bin entry, `node_modules/.bin/expo`, present and
# executable. This mirrors web-preflight's `tsc` health probe.
#
# Unlike web-preflight this never runs npm. mobile-native's install is the
# developer's to create, and the failure the issue reports is a confusing error
# message, not a missing self-heal; the fix is a message naming the command.
#
# EVENER_NATIVE_DIR overrides which mobile-native directory this preflights, for
# pointing it at a throwaway install instead of the real one.
set -eu

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
native=${EVENER_NATIVE_DIR:-$repo_root/mobile-native}
cd "$native"

if [ -L node_modules ]; then
	target=$(readlink node_modules)
	shared=$(dirname "$target")
	if ! cmp -s "$shared/package-lock.json" package-lock.json; then
		echo "ERROR: $native/node_modules is a symlink to $target," >&2
		echo "  and $shared/package-lock.json does not match this worktree's." >&2
		echo "  Install an up-to-date tree in $shared, or replace this worktree's" >&2
		echo "  symlink with its own real node_modules — never npm ci through the" >&2
		echo "  symlink, which would delete the shared install every worktree uses." >&2
		exit 1
	fi
elif [ -e node_modules ]; then
	if [ ! node_modules -nt package-lock.json ]; then
		echo "ERROR: $native/node_modules is older than package-lock.json." >&2
		echo "  Reinstall mobile-native's dependencies:" >&2
		echo "    cd $native && npm ci" >&2
		exit 1
	fi
else
	echo "ERROR: $native/node_modules is missing." >&2
	echo "  The native targets bundle with the Expo pinned in mobile-native, so" >&2
	echo "  without this install npx falls back to a global expo and Metro fails" >&2
	echo "  with \"No platforms are configured to use the Metro bundler\"." >&2
	echo "  Install it first:" >&2
	echo "    cd $native && npm ci" >&2
	exit 1
fi

if [ ! -x node_modules/.bin/expo ]; then
	echo "ERROR: $native/node_modules is unhealthy: node_modules/.bin/expo is not" >&2
	echo "  an executable file (an empty or half-installed tree)." >&2
	if [ -L node_modules ]; then
		echo "  Install an up-to-date tree in $(dirname "$(readlink node_modules)"), or" >&2
		echo "  replace this worktree's symlink with its own real node_modules —" >&2
		echo "  never npm ci through the symlink." >&2
	else
		echo "  Reinstall mobile-native's dependencies:" >&2
		echo "    cd $native && npm ci" >&2
	fi
	exit 1
fi
