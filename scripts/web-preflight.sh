#!/bin/sh
# web-preflight.sh — own the cmd/serf-hub/frontend node_modules install for
# every web target, so `make build-web` and `make test-web` share one
# definition of "the install is ready".
#
# npm ci installs exactly what's pinned in the committed package-lock.json;
# skip it when node_modules is already newer than the lockfile (a missing
# node_modules or a changed lockfile both trigger a fresh npm ci). (`-nt` is
# a POSIX test(1) primitive, supported by /bin/sh on macOS and dash on
# Linux; note it follows symlinks, so a symlinked node_modules compares the
# SHARED target's mtime against this worktree's own lockfile — a comparison
# every fresh worktree loses, which is why the symlink branch settles that
# case on lockfile content instead.)
#
# Two guards, both from real incidents:
#
# 1. Never npm ci through a symlinked node_modules. Agent worktrees symlink
#    node_modules to one shared install, and npm ci deletes an existing
#    node_modules before installing — through a symlink that means deleting
#    the shared install out from under every other worktree, which has
#    emptied it repeatedly. So a symlinked install is either already the one
#    this worktree wants (identical lockfiles) or a loud refusal; npm ci is
#    not on the menu. The silent "self-heal" costs every other worktree.
# 2. Prove the install is real by asking the LOCAL tsc for its version. A
#    bare or npx `tsc` resolves to the unrelated tsc@2.0.4 package, which is
#    not the TypeScript compiler — so an empty install can otherwise read as
#    a working toolchain.
#
# SERF_WEB_FRONTEND_DIR exists so scripts/web-preflight-selftest.sh can point
# this at a throwaway frontend instead of the real one, whose node_modules is
# the fleet's single shared install.
set -eu

repo_root=$(cd "$(dirname "$0")/.." && pwd)
frontend=${SERF_WEB_FRONTEND_DIR:-$repo_root/cmd/serf-hub/frontend}
cd "$frontend"

if [ node_modules -nt package-lock.json ]; then
	:
elif [ -L node_modules ]; then
	# A shared install's mtime says nothing about this worktree: the worktree
	# checks its lockfile out long after the install was populated, so mtime
	# calls every fresh worktree stale. What settles it is the lockfile the
	# install was built from, sitting beside it — byte-identical to this
	# worktree's means the install already contains exactly what's wanted.
	target=$(readlink node_modules)
	shared=$(dirname "$target")
	if ! cmp -s "$shared/package-lock.json" package-lock.json; then
		echo "ERROR: node_modules is a symlink to $target," >&2
		echo "  and $shared/package-lock.json does not match this worktree's." >&2
		echo "  npm ci would DELETE that shared install for every worktree using it." >&2
		echo "  Refresh the shared install in $shared, or give this worktree its own" >&2
		echo "  real node_modules — never npm ci through the symlink." >&2
		exit 1
	fi
else
	npm ci
fi

v=$(./node_modules/.bin/tsc --version 2>&1) || {
	echo "ERROR: node_modules/.bin/tsc failed: $v" >&2
	exit 1
}
case "$v" in
"Version "*) ;;
*)
	echo "ERROR: frontend node_modules is unhealthy ($(ls node_modules | wc -l | tr -d ' ') entries)." >&2
	echo "  ./node_modules/.bin/tsc printed: $v" >&2
	exit 1
	;;
esac
