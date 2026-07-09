#!/usr/bin/env bash
# seatbelt-smoke.sh — live macOS Seatbelt enforcement smoke test.
#
# When to use: on a Mac (paradise-park) after building M6, to confirm serf's
# generated SBPL is actually enforced by the real kernel — not just that the
# policy text looks right. Run it from the repo root of a serf worktree.
#
# What it does:
#   1. Sanity-checks that /usr/bin/sandbox-exec exists and enforces a trivial
#      deny-default policy (a raw allow/deny pair, independent of serf's Go code).
#   2. Runs serf's gated Go parity suite (SERF_SEATBELT_LIVE=1), which generates
#      real policies from ResolvedPolicy and asserts the kernel's verdict for
#      network denial, worktree/secret confinement, and git-config protection.
#
# Output is deliberately terse: it prints PASS/FAIL per stage and, on failure,
# the failing command's output. Exit non-zero on any failure.
set -euo pipefail

SEATBELT=/usr/bin/sandbox-exec
fail() {
	echo "FAIL: $*" >&2
	exit 1
}

[[ "$(uname -s)" == "Darwin" ]] || fail "this smoke test only runs on macOS"
[[ -x "$SEATBELT" ]] || fail "$SEATBELT not found or not executable"

echo "== stage 1: raw sandbox-exec deny-default enforcement =="

# A minimal deny-default policy that allows exec/fork + reading a param root but
# NOT writing it. Proves the kernel honors (deny default) and (param ...).
ROOT="$(mktemp -d)"
# macOS mktemp returns /var/folders/... but /var is a symlink to /private/var, so
# canonicalize: the -D W=$ROOT/allowed param must carry the kernel's real path or
# the writable-param write below is denied and the smoke test false-FAILs.
ROOT="$(cd "$ROOT" && pwd -P)"
trap 'rm -rf "$ROOT"' EXIT
echo hello >"$ROOT/readme"

# A self-contained deny-default policy that exercises read-allow / write-deny.
selfcontained='(version 1)(deny default)(allow process*)(allow file-read* file-read-metadata)(allow file-write* (subpath (param "W")))'

# 1a: a write OUTSIDE the single writable param must be denied.
if "$SEATBELT" -p "$selfcontained" -D "W=$ROOT/allowed" -- /bin/sh -c "echo x > $ROOT/denied" 2>/dev/null; then
	fail "raw sandbox-exec allowed a write outside the writable param (deny-default not enforced)"
fi
mkdir -p "$ROOT/allowed"
# 1b: a write INSIDE the writable param must be allowed.
if ! "$SEATBELT" -p "$selfcontained" -D "W=$ROOT/allowed" -- /bin/sh -c "echo x > $ROOT/allowed/ok" 2>/dev/null; then
	fail "raw sandbox-exec denied a write inside the writable param (over-restrictive)"
fi
echo "PASS: raw deny-default + writable-param enforcement"

echo "== stage 2: serf generated-policy parity suite =="
if ! SERF_SEATBELT_LIVE=1 go test ./agent/sandbox/ -run TestSeatbeltLive -count=1 -v; then
	fail "serf live parity suite (TestSeatbeltLive) failed"
fi
echo "PASS: serf generated-policy parity suite"

echo "ALL SEATBELT SMOKE CHECKS PASSED"
