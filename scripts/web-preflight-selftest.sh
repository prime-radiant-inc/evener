#!/usr/bin/env bash
# web-preflight-selftest.sh — offline, deterministic test of
# scripts/web-preflight.sh (kata sf67), against synthetic frontend trees.
#
# The real cmd/serf-hub/frontend/node_modules is the fleet's ONE shared
# install, which this test must never read as fixture state and must never
# write to; SERF_WEB_FRONTEND_DIR exists so every scenario below runs against
# a throwaway tree under $work instead.
#
# `npm ci` is the command the script's whole refusal exists to fence, so a
# stub npm goes on PATH: it records the call and populates the install the
# way a real ci would, which lets the "did it try to install?" assertions be
# exact without a network install ever happening.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/web-preflight.sh"
checks=0 fails=0
ok() { checks=$((checks + 1)); printf '  ok: %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL: %s\n' "$1"; }

work="$(mktemp -d -t web-preflight-selftest.XXXXXX)"
work="$(cd "$work" && pwd -P)"
trap 'rm -rf "$work"' EXIT

stub_bin="$work/bin"
mkdir -p "$stub_bin"
cat >"$stub_bin/npm" <<'STUB'
#!/bin/sh
# Stand-in for `npm ci`: record the call, then populate node_modules the way
# a real install would so the health check downstream has a tsc to ask.
echo "npm $*" >>"$NPM_CALLS"
mkdir -p node_modules/.bin
printf '#!/bin/sh\necho "Version 5.9.2"\n' >node_modules/.bin/tsc
chmod +x node_modules/.bin/tsc
STUB
chmod +x "$stub_bin/npm"

# A synthetic frontend: the lockfile the script compares, and nothing else.
# $2 is the lockfile's content, so two frontends can be given identical or
# differing lockfiles by hand.
new_frontend() {
	local dir="$work/$1"
	mkdir -p "$dir"
	printf '%s\n' "${2:-lockfile-A}" >"$dir/package-lock.json"
	printf '%s\n' '{"name":"fixture"}' >"$dir/package.json"
	printf '%s' "$dir"
}

# A populated real node_modules inside <dir>, whose tsc reports $2 (default: a
# real-looking version). Backdated so it is OLDER than the lockfile beside it,
# which is the state every scenario here starts from — writing files into a
# directory bumps its mtime, so this has to come after populating it.
install_at() {
	mkdir -p "$1/node_modules/.bin"
	printf '#!/bin/sh\necho "%s"\n' "${2:-Version 5.9.2}" >"$1/node_modules/.bin/tsc"
	chmod +x "$1/node_modules/.bin/tsc"
	touch -t 202001010000 "$1/node_modules"
}

# Runs the script against <frontend>. Sets $status and $out, and leaves the
# recorded npm calls in $work/npm-calls.txt.
run_preflight() {
	: >"$work/npm-calls.txt"
	out="$(PATH="$stub_bin:$PATH" NPM_CALLS="$work/npm-calls.txt" \
		SERF_WEB_FRONTEND_DIR="$1" "$script" 2>&1)"
	status=$?
}

npm_was_called() { [ -s "$work/npm-calls.txt" ]; }

# --- 1. Symlinked install, identical lockfiles: current, nothing to do. -----
# The fresh-worktree case (kata sf67). The shared install predates this
# worktree's checkout-time lockfile, so mtime says "stale" and only the
# lockfile content can say otherwise.
shared="$(new_frontend shared-identical)"
install_at "$shared"
mine="$(new_frontend mine-identical)"
ln -s "$shared/node_modules" "$mine/node_modules"
run_preflight "$mine"
if [ "$status" -eq 0 ]; then
	ok "symlinked install with an identical lockfile passes"
else
	bad "symlinked install with an identical lockfile was refused ($status): $out"
fi
if npm_was_called; then
	bad "symlinked install with an identical lockfile still ran npm: $(cat "$work/npm-calls.txt")"
else
	ok "symlinked install with an identical lockfile never invokes npm"
fi

# --- 2. Symlinked install, differing lockfiles: refuse. ---------------------
# The shared install does not contain what this worktree pins, and npm ci
# through the symlink would empty it for every other worktree.
shared="$(new_frontend shared-differing "lockfile-B")"
install_at "$shared"
mine="$(new_frontend mine-differing "lockfile-A")"
ln -s "$shared/node_modules" "$mine/node_modules"
run_preflight "$mine"
if [ "$status" -eq 1 ]; then
	ok "symlinked install with a differing lockfile is refused"
else
	bad "symlinked install with a differing lockfile exited $status: $out"
fi
if echo "$out" | grep -q "does not match this worktree's"; then
	ok "the refusal names the lockfile mismatch as the reason"
else
	bad "the refusal does not name the lockfile mismatch: $out"
fi
if echo "$out" | grep -q "$shared"; then
	ok "the refusal names the shared install to refresh"
else
	bad "the refusal does not name the shared install: $out"
fi
if npm_was_called; then
	bad "the refusal still ran npm: $(cat "$work/npm-calls.txt")"
else
	ok "the refusal never invokes npm"
fi

# --- 3. Symlink to an install with no lockfile beside it: refuse. -----------
# An absent lockfile is not evidence of a matching install, and reading it as
# one would hand the shared install to npm ci on the strength of a missing
# file.
shared="$(new_frontend shared-nolock)"
install_at "$shared"
rm "$shared/package-lock.json"
mine="$(new_frontend mine-nolock)"
ln -s "$shared/node_modules" "$mine/node_modules"
run_preflight "$mine"
if [ "$status" -eq 1 ]; then
	ok "symlinked install with no lockfile beside it is refused"
else
	bad "symlinked install with no lockfile beside it exited $status: $out"
fi
if npm_was_called; then
	bad "the no-lockfile refusal still ran npm: $(cat "$work/npm-calls.txt")"
else
	ok "the no-lockfile refusal never invokes npm"
fi

# --- 4. Real directory, older than the lockfile: npm ci, as before. ---------
mine="$(new_frontend mine-real-stale)"
install_at "$mine"
run_preflight "$mine"
if grep -q "^npm ci$" "$work/npm-calls.txt"; then
	ok "a real node_modules older than the lockfile is reinstalled with npm ci"
else
	bad "a real stale node_modules did not run npm ci (calls: $(cat "$work/npm-calls.txt")): $out"
fi
if [ "$status" -eq 0 ]; then
	ok "a real stale node_modules ends healthy after the reinstall"
else
	bad "a real stale node_modules exited $status after the reinstall: $out"
fi

# --- 5. Real directory, newer than the lockfile: nothing to do. ------------
# Backdating the lockfile, rather than touching node_modules to "now", keeps
# the two mtimes an unambiguous distance apart: `-nt` compares whole seconds,
# so an install touched in the same second as the lockfile reads as older.
mine="$(new_frontend mine-real-fresh)"
install_at "$mine"
touch -t 201901010000 "$mine/package-lock.json"
run_preflight "$mine"
if [ "$status" -eq 0 ] && ! npm_was_called; then
	ok "a real node_modules newer than the lockfile is left alone"
else
	bad "a real fresh node_modules exited $status / npm calls: $(cat "$work/npm-calls.txt")"
fi

# --- 6. Missing node_modules: npm ci. --------------------------------------
mine="$(new_frontend mine-missing)"
run_preflight "$mine"
if grep -q "^npm ci$" "$work/npm-calls.txt" && [ "$status" -eq 0 ]; then
	ok "a missing node_modules is installed with npm ci"
else
	bad "a missing node_modules exited $status / npm calls: $(cat "$work/npm-calls.txt")"
fi

# --- 7. The health guard survives the lockfile pass. -----------------------
# Passing on identical lockfiles must not skip the tsc check: a shared install
# that is present but empty resolves `tsc` to the unrelated tsc@2.0.4 package,
# which prints a bare version and is not the TypeScript compiler.
shared="$(new_frontend shared-unhealthy)"
install_at "$shared" "2.0.4"
mine="$(new_frontend mine-unhealthy)"
ln -s "$shared/node_modules" "$mine/node_modules"
run_preflight "$mine"
if [ "$status" -eq 1 ] && echo "$out" | grep -q "unhealthy"; then
	ok "an identical-lockfile install still fails the tsc health check when broken"
else
	bad "an unhealthy identical-lockfile install exited $status: $out"
fi

echo
echo "$checks checks, $fails failed"
[ "$fails" -eq 0 ]
