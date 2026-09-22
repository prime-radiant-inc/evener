#!/bin/sh
# gate-roots.sh — the durable per-worktree roots the Go test streams run in.
#
# Sourced, never executed, and POSIX sh so a test can drive it through `sh -c`.
# It touches nothing until a caller asks.
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
	if [ -z "$evener_gate_root_id" ]; then
		printf 'gate-roots: could not hash %s; refusing to derive a root every checkout would share\n' \
			"$evener_gate_root_repo" >&2
		return 1
	fi
	printf '%s/evener-gate-roots-%s\n' "$evener_gate_root_tmp" "$evener_gate_root_id"
}

# evener_gate_root_is_owned PATH — whether PATH is one this library may create,
# empty, or delete: absolute, inside an `evener-gate-roots-*` directory, and
# naming no parent.
#
# Every entry point checks it before touching the filesystem, so a clobbered,
# emptied, or relative argument cannot reach a recursive delete, a chmod, or a
# lock write somewhere else — the shapes kata 5hs2 reached.
evener_gate_root_is_owned() {
	case "${1:-}" in
	/*/evener-gate-roots-*/*) ;;
	*)
		printf 'gate-roots: refusing %s: not an absolute path under an evener-gate-roots-* directory\n' \
			"${1:-}" >&2
		return 1
		;;
	esac
	case "$1" in
	*..*)
		printf 'gate-roots: refusing %s: path names a parent directory\n' "$1" >&2
		return 1
		;;
	esac
}

# evener_remove_gate_root ROOT — delete a durable root. This is the one
# recursive delete in this file; the guard is what makes it reviewable.
evener_remove_gate_root() {
	evener_gate_root_is_owned "${1:-}" || return 1
	rm -rf "$1" || return 1
}

# evener_reset_gate_root ROOT — empty ROOT and make it private, creating it if
# absent. 0700 is what satisfies the temp-root trust check above, whatever the
# caller's umask would otherwise produce; mkdir -m sets it without the second
# fork chmod would cost.
evener_reset_gate_root() {
	evener_gate_root_dir=$1
	evener_remove_gate_root "$evener_gate_root_dir" || return 1
	mkdir -p -m 0700 "$evener_gate_root_dir" || return 1
}

# evener_gate_root_base_is_safe BASE — refuse a base that is a symlink or is not
# a directory.
#
# The base name is a predictable path under the caller's TMPDIR, normally /tmp,
# so another user can create that name first. Everything this library does to a
# root — mkdir, chmod 0700, and reset's recursive delete — resolves through the
# base, so a symlink left there would redirect all of it into whatever it points
# at: a planted `ln -s "$HOME" /tmp/evener-gate-roots-<hash>` would have the next
# `make test` tighten and delete directories inside the victim's home. Refusing
# sends the stream to its per-run fallback root instead, which is uncached but
# safe. The root inside the base needs no such check: it is removed before it is
# recreated, and removing a symlink never follows it.
evener_gate_root_base_is_safe() {
	if [ -L "$1" ]; then
		printf 'gate-roots: refusing %s: the base is a symlink\n' "$1" >&2
		return 1
	fi
	if [ -e "$1" ] && [ ! -d "$1" ]; then
		printf 'gate-roots: refusing %s: the base is not a directory\n' "$1" >&2
		return 1
	fi
}

# evener_take_gate_root_lock LOCK — create LOCK, holding this shell's pid, if it
# does not already exist. Returns 0 when this shell created it, 1 when it was
# already there.
#
# The pid goes into a private file that is then linked into place, because a
# plain redirect publishes the lock before its contents: a second run reading
# the empty file in that window would judge the owner dead and take a root a
# live run is using. link(2) installs a complete file or fails.
evener_take_gate_root_lock() {
	evener_gate_root_newlock="$1.$$"
	printf '%s\n' "$$" >"$evener_gate_root_newlock" || return 1
	if ln "$evener_gate_root_newlock" "$1" 2>/dev/null; then
		rm -f "$evener_gate_root_newlock"
		return 0
	fi
	rm -f "$evener_gate_root_newlock"
	return 1
}

# evener_claim_gate_root ROOT — claim ROOT for this run.
#
# Returns 0 when claimed, 1 when another live gate run in this worktree holds it
# or ROOT is not a path this library owns; the caller must then fall back to a
# per-run root, which is correct but cannot reuse Go's test cache, and should
# say so. A run killed outright leaves a lock whose pid no longer answers, which
# a later run reclaims.
evener_claim_gate_root() {
	evener_gate_root_dir=$1
	evener_gate_root_is_owned "$evener_gate_root_dir" || return 1
	evener_gate_root_lock="$1.lock"
	evener_gate_root_base=$(dirname -- "$evener_gate_root_dir")
	evener_gate_root_base_is_safe "$evener_gate_root_base" || return 1
	mkdir -p -m 0700 "$evener_gate_root_base" || return 1
	chmod 0700 "$evener_gate_root_base" || return 1
	evener_gate_root_attempt=0
	while :; do
		if evener_take_gate_root_lock "$evener_gate_root_lock"; then
			# On a failed reset the lock must go: this shell's pid is alive, so
			# leaving it would make every other gate run in this worktree fall
			# back to uncached per-run roots until this process exits.
			if ! evener_reset_gate_root "$evener_gate_root_dir"; then
				rm -f "$evener_gate_root_lock" || :
				return 1
			fi
			return 0
		fi
		# One retry: the lock may belong to a run that was killed outright. A
		# live owner is contention and is reported, not overridden.
		evener_gate_root_attempt=$((evener_gate_root_attempt + 1))
		[ "$evener_gate_root_attempt" -ge 2 ] && return 1
		evener_gate_root_owner=$(cat "$evener_gate_root_lock" 2>/dev/null || :)
		if [ -n "$evener_gate_root_owner" ] && kill -0 "$evener_gate_root_owner" 2>/dev/null; then
			return 1
		fi
		evener_reclaim_gate_root_lock "$evener_gate_root_lock" || return 1
	done
}

# evener_reclaim_gate_root_lock LOCK — remove a lock whose owner is gone, for the
# claim above; nonzero means the caller must treat the root as contended.
#
# Reclaiming is itself a claim (on LOCK.reclaim) and only removes the lock after
# re-reading the owner it observed, because an unconditional `rm -f` can delete
# a lock a competing run created in between — which would hand two runs the same
# root and let one run's scratch vanish mid-test. A crash while holding the
# reclaim lock leaves that root unreclaimed until the file is removed by hand,
# which costs cache reuse for one worktree rather than correctness.
evener_reclaim_gate_root_lock() {
	evener_reclaim_seen=$evener_gate_root_owner
	if ! evener_take_gate_root_lock "$1.reclaim"; then
		return 1
	fi
	evener_reclaim_now=$(cat "$1" 2>/dev/null || :)
	if [ -n "$evener_reclaim_now" ] && [ "$evener_reclaim_now" != "$evener_reclaim_seen" ]; then
		rm -f "$1.reclaim" || :
		return 1
	fi
	rm -f "$1" || :
	rm -f "$1.reclaim" || :
	return 0
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
	evener_gate_root_is_owned "$evener_gate_root_dir" || return 1
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
