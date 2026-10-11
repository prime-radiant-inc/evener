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
# A symlinked node_modules is an agent worktree's shared install — and it is
# not a usable one here, however well its content matches: the bundler
# resolves no module through the link. Metro 0.84 has no symlink-following
# resolution, and the controlled case is the same tree bundled green as a real
# directory failing at one module behind a symlink ("expo could not be found
# within the project"), so a symlink is refused outright and the message names
# the fix: remove the link and give this worktree its own real install. The
# refusal never suggests npm ci through the link: that would delete the
# shared install for every worktree using it.
#
# Existence and freshness are not health. An empty or half-installed tree, or a
# symlink whose target directory is gone, passes both and still makes Metro fail
# with the error above, so the last check proves the toolchain the bundler will
# use: the pinned expo's own bin entry, `node_modules/.bin/expo`, present as a
# regular executable file. This mirrors web-preflight's `tsc` health probe.
#
# Both freshness tests read a real package-lock.json, so it is required up
# front: `test -nt` treats an absent second file as older under some shells and
# newer under others, so deciding the lockfile's presence explicitly is what
# makes dash and bash behave the same.
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

# Canonicalize before cd: a relative override would otherwise be re-prefixed
# against a directory the process has already left when a relative symlink
# target is resolved below. Logical pwd, so a platform whose /tmp is a symlink
# does not rewrite the path the user set. If the directory is not enterable
# this falls through and the cd below still fails.
if canonical=$( (CDPATH='' cd -- "$native" && pwd) ) 2>/dev/null; then
	native=$canonical
fi
cd "$native"

if [ ! -f package-lock.json ]; then
	echo "ERROR: $native/package-lock.json is missing." >&2
	echo "  The install can only be checked for freshness against the committed" >&2
	echo "  lockfile; restore it from git before running a native target." >&2
	exit 1
fi

# Canonicalize a symlinked shared install once, so the refusal names one
# absolute path. A relative link
# target is resolved against $native — the directory the symlink lives in — and
# the shared directory is settled on an absolute, ".."-free path when it still
# exists (logical pwd, so a platform whose /tmp is a symlink does not rewrite
# the path the user set).
link_target=""
link_shared=""
if [ -L node_modules ]; then
	link_target=$(readlink node_modules)
	case "$link_target" in
	/*) ;;
	*) link_target=$native/$link_target ;;
	esac
	if resolved=$( (CDPATH='' cd -- "$(dirname "$link_target")" && pwd) 2>/dev/null ) && [ -n "$resolved" ]; then
		link_shared=$resolved
	else
		link_shared=$(dirname "$link_target")
	fi
	link_target=$link_shared/$(basename "$link_target")
fi

if [ -n "$link_shared" ]; then
	echo "ERROR: $native/node_modules is a symlink to $link_target." >&2
	echo "  The bundle targets cannot run against it: the bundler resolves no" >&2
	echo "  module through a symlinked install, however well its content matches" >&2
	echo "  this worktree's lockfile — Metro fails with \"could not be found within" >&2
	echo "  the project\" on a tree that bundles green as a real directory." >&2
	echo "  Give this worktree its own real install; never npm ci through the symlink," >&2
	echo "  which would delete the shared install every worktree uses. Remove the" >&2
	echo "  link, then:" >&2
	echo "    rm $native/node_modules && cd $native && npm ci" >&2
	exit 1
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

if [ ! -f node_modules/.bin/expo ] || [ ! -x node_modules/.bin/expo ]; then
	echo "ERROR: $native/node_modules is unhealthy: node_modules/.bin/expo is not" >&2
	echo "  a regular executable file (an empty or half-installed tree)." >&2
	echo "  Reinstall mobile-native's dependencies:" >&2
	echo "    cd $native && npm ci" >&2
	exit 1
fi
