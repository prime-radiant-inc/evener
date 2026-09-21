#!/bin/sh
# gate-roots.sh — the durable per-worktree roots the Go test streams run in.
#
# Sourced, never executed, and POSIX sh so a test can drive it through `sh -c`.
# It defines five functions and touches nothing until a caller asks.
#
# Why these roots are durable rather than minted per run: Go's test cache keys
# on the *values* of the environment variables a test consults. The test binary
# logs each `getenv NAME` it performs and cmd/go folds that variable's value
# into the cache key (internal/testlog; cmd/go/internal/test's
# computeTestInputsID). The gate hands every Go stream a private HOME, TMPDIR,
# and XDG roots, so those are exactly the values in the key — and when they are
# minted fresh per run, every package that consults one re-runs no matter how
# unchanged the code is. (t.TempDir alone is enough: it reads TMPDIR.) Keeping
# the *paths* the same across runs makes the cache reusable, while the contents
# are still emptied on every claim, so a run still starts pristine.
#
# Only the path participates in the key. cmd/go hashes opened files only inside
# the module, GOPATH, or GOROOT root and explicitly skips anything outside them,
# so scratch written under these roots never dirties a cache entry — which is
# why a stable path with emptied contents is the whole fix.
#
# They live under the caller's TMPDIR, not under a cache directory: these roots
# become TMPDIR for the streams that use them, and the repo's own skill-cache
# trust check (agent/skill/ensureTrustedAncestors) refuses a temp root whose
# ancestor chain any other user can write to. The caller's TMPDIR is a chain
# this gate already runs under, so deriving from it adds no new trust
# dependency. Every directory here is created 0700 for the same reason, and the
# base name carries a hash of the worktree root so sibling checkouts never share
# — or reclaim — each other's roots.

# evener_durable_gate_root REPO_ROOT — print the durable root directory for the
# Go test streams of the worktree at REPO_ROOT. The path is canonicalized, so a
# TMPDIR reached through a symlink still yields the same string on every run.
evener_durable_gate_root() {
	evener_gate_root_repo=$1
	evener_gate_root_tmp=${TMPDIR:-/tmp}
	if [ -d "$evener_gate_root_tmp" ]; then
		evener_gate_root_tmp=$(cd "$evener_gate_root_tmp" && pwd -P) || return 1
	fi
	evener_gate_root_id=$(printf '%s' "$evener_gate_root_repo" | shasum -a 256 | cut -c1-16)
	printf '%s/evener-gate-roots-%s\n' "$evener_gate_root_tmp" "$evener_gate_root_id"
}

# evener_remove_gate_root ROOT — delete a durable root.
#
# This is the one recursive delete in this file, and the guard is what makes it
# reviewable: ROOT must sit inside an `evener-gate-roots-*` directory and must
# not name a parent, so an emptied, clobbered, or relative path fails loudly
# instead of deleting somewhere else.
evener_remove_gate_root() {
	evener_gate_root_dir=$1
	case "$evener_gate_root_dir" in
	/*/evener-gate-roots-*/*) ;;
	*)
		printf 'gate-roots: refusing to remove %s: not an absolute path under an evener-gate-roots-* directory\n' \
			"$evener_gate_root_dir" >&2
		return 1
		;;
	esac
	case "$evener_gate_root_dir" in
	*..*)
		printf 'gate-roots: refusing to remove %s: path names a parent directory\n' \
			"$evener_gate_root_dir" >&2
		return 1
		;;
	esac
	rm -rf "$evener_gate_root_dir" || return 1
}

# evener_reset_gate_root ROOT — empty ROOT and make it private, creating it if
# absent. 0700 is what satisfies the temp-root trust check above, whatever the
# caller's umask would otherwise produce.
evener_reset_gate_root() {
	evener_gate_root_dir=$1
	evener_remove_gate_root "$evener_gate_root_dir" || return 1
	mkdir -p "$evener_gate_root_dir" || return 1
	chmod 0700 "$evener_gate_root_dir" || return 1
}

# evener_claim_gate_root ROOT — claim ROOT for this run.
#
# Returns 0 when claimed, 1 when another live gate run in this worktree already
# holds it; the caller must then fall back to a per-run root, which is correct
# but cannot reuse Go's test cache, and should say so. The claim is a lock file
# beside ROOT holding this shell's pid, so a run killed outright leaves a lock
# whose pid no longer answers, which a later run reclaims.
evener_claim_gate_root() {
	evener_gate_root_dir=$1
	evener_gate_root_lock="$1.lock"
	evener_gate_root_base=$(dirname -- "$evener_gate_root_dir")
	mkdir -p "$evener_gate_root_base" || return 1
	chmod 0700 "$evener_gate_root_base" || return 1
	if (set -C; printf '%s\n' "$$" >"$evener_gate_root_lock") 2>/dev/null; then
		evener_reset_gate_root "$evener_gate_root_dir" || return 1
		return 0
	fi
	evener_gate_root_owner=$(cat "$evener_gate_root_lock" 2>/dev/null || :)
	if [ -n "$evener_gate_root_owner" ] && kill -0 "$evener_gate_root_owner" 2>/dev/null; then
		return 1
	fi
	rm -f "$evener_gate_root_lock" || return 1
	if (set -C; printf '%s\n' "$$" >"$evener_gate_root_lock") 2>/dev/null; then
		evener_reset_gate_root "$evener_gate_root_dir" || return 1
		return 0
	fi
	return 1
}

# evener_release_gate_root ROOT KEEP_DIR — give ROOT back.
#
# An empty KEEP_DIR is a green run: ROOT is removed outright and the base
# directory is pruned if it is now empty, so a green run leaves nothing under
# the caller's TMPDIR to find. A set KEEP_DIR is a red or interrupted run: ROOT
# is moved into KEEP_DIR (the runner's retained log directory) so the scratch a
# failure left behind survives beside its logs; if the move fails it is left
# where it is, which still keeps it readable. Either way the lock goes, because
# it exists only while a run holds the root.
evener_release_gate_root() {
	evener_gate_root_dir=$1
	evener_gate_root_keep=${2:-}
	evener_gate_root_lock="$1.lock"
	if [ -z "$evener_gate_root_keep" ]; then
		evener_remove_gate_root "$evener_gate_root_dir" || :
	else
		mkdir -p "$evener_gate_root_keep" 2>/dev/null || :
		if [ -d "$evener_gate_root_dir" ]; then
			mv "$evener_gate_root_dir" "$evener_gate_root_keep/" 2>/dev/null || :
		fi
	fi
	rm -f "$evener_gate_root_lock" 2>/dev/null || :
	rmdir "$(dirname -- "$evener_gate_root_dir")" 2>/dev/null || :
}
