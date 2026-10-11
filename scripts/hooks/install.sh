#!/bin/sh
# install.sh — point this clone's git at the checked-in hooks in scripts/hooks.
# Run it through `make hooks`; the setting lives in the shared git config, so
# every worktree of the clone picks it up (and each runs its own checkout's
# copy of the hook, since the path is relative).
#
# core.hooksPath replaces .git/hooks wholesale, so this refuses rather than
# bypass anything: a different core.hooksPath, or an installed hook other than
# pre-commit (the checked-in pre-commit chains onto .git/hooks/pre-commit
# itself). Move such hooks into scripts/hooks first.
set -eu

case "${1:-}" in
-h | --help)
	echo "usage: install.sh   (sets core.hooksPath to scripts/hooks; refuses to bypass existing hooks)"
	exit 0
	;;
esac

current=$(git config --get core.hooksPath || true)
if [ -n "$current" ] && [ "$current" != scripts/hooks ]; then
	echo "install.sh: core.hooksPath is already '$current'; refusing to replace it." >&2
	echo "  Fix: move those hooks into scripts/hooks, then: git config --unset core.hooksPath (add --global if it is set there) && make hooks" >&2
	exit 1
fi

hooks_dir="$(git rev-parse --git-common-dir)/hooks"
others=$(find -L "$hooks_dir" -maxdepth 1 -type f -perm -u+x ! -name '*.sample' ! -name pre-commit 2>/dev/null || true)
if [ -n "$others" ]; then
	echo "install.sh: these installed hooks would stop running under core.hooksPath:" >&2
	printf '%s\n' "$others" | sed 's/^/  /' >&2
	echo "  Fix: move them into scripts/hooks, then run make hooks again." >&2
	exit 1
fi

git config core.hooksPath scripts/hooks
echo "hooks installed: core.hooksPath=scripts/hooks"
