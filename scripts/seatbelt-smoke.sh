#!/usr/bin/env bash
# seatbelt-smoke.sh — live macOS Seatbelt enforcement smoke test.
#
# When to use: on a Mac (paradise-park) after building M6, to confirm evener's
# generated SBPL is actually enforced by the real kernel — not just that the
# policy text looks right. Run it from the repo root of a evener worktree.
#
# What it does:
#   1. Sanity-checks that /usr/bin/sandbox-exec exists and enforces a trivial
#      deny-default policy (a raw allow/deny pair, independent of evener's Go code).
#   2. Runs evener's gated Go parity suite (EVENER_SEATBELT_LIVE=1), which generates
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
. "$(dirname "$0")/scratch-lib.sh"
# scratch_dir canonicalizes, which matters here beyond hygiene: the -D
# W=$ROOT/allowed param must carry the kernel's real path (/private/var, not
# the /var symlink) or the writable-param write below is denied and the smoke
# test false-FAILs. The trap is armed first; scratch_rm with nothing
# registered is a no-op.
trap 'scratch_rm' EXIT
scratch_dir ROOT seatbelt-smoke
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

echo "== stage 2: evener generated-policy parity suite =="
if ! EVENER_SEATBELT_LIVE=1 go test ./agent/sandbox/ -run TestSeatbeltLive -count=1 -v; then
	fail "evener live parity suite (TestSeatbeltLive) failed"
fi
echo "PASS: evener generated-policy parity suite"

echo "ALL SEATBELT SMOKE CHECKS PASSED"
