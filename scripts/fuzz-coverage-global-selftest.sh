#!/usr/bin/env bash
# fuzz-coverage-global-selftest.sh — deterministic test of fuzz-coverage-global.sh.
#
# Measuring real coverage is heavy and nondeterministic, so this stubs `go` to
# emit known coverprofiles and a passthrough cap wrapper, then asserts the parse
# (covered/total statements), the repo total, and the ratchet (--check pass/fail,
# --bless raise-only) — the load-bearing logic — with no real test runs.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/fuzz-coverage-global.sh"
work="$(mktemp -d -t fuzzcov-global-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT
checks=0 fails=0
ok()  { checks=$((checks+1)); printf 'ok   - %s\n' "$1"; }
bad() { checks=$((checks+1)); fails=$((fails+1)); printf 'FAIL - %s\n' "$1"; }
has() { if printf '%s' "$1" | grep -qF -- "$2"; then ok "$3"; else bad "$3 (missing: $2)"; fi; }

# Passthrough cap wrapper.
cap="$work/cap.sh"; printf '#!/usr/bin/env bash\nexec "$@"\n' >"$cap"; chmod +x "$cap"

# Stub go: on `test ... -coverprofile=PATH ...`, write a profile whose covered/
# total statements are controlled per-module by $COV (a "mod=cov/total,..." map
# read from the cwd's basename). Emits 1 covered block and 1 uncovered block whose
# numStmts sum to the requested totals.
gobin="$work/go.sh"
cat >"$gobin" <<'STUB'
#!/usr/bin/env bash
prof=""
for a in "$@"; do case "$a" in -coverprofile=*) prof="${a#*=}";; esac; done
[ -n "$prof" ] || exit 0
mod="$(basename "$PWD")"
# COV maps a module dir basename to "covered total"; default 0 0.
pair="$(printf '%s\n' "$COV" | tr ',' '\n' | awk -F= -v m="$mod" '$1==m{print $2}')"
cov="${pair%% *}"; tot="${pair##* }"
[ -n "$cov" ] || { cov=0; tot=0; }
uncov=$((tot - cov))
{
  echo "mode: set"
  # Emit each block TWICE, as `go test -coverpkg=./... ./...` does (once per
  # package test binary): the covered block recurs with count 0 from a binary
  # that didn't run it. The parser MUST dedupe by position (count once, covered
  # if any occurrence ran) — a naive per-line sum would inflate these totals and
  # fail the percentage assertions below. This is the regression guard for that.
  [ "$cov" -gt 0 ] && { echo "x/$mod/a.go:1.1,2.1 $cov 1"; echo "x/$mod/a.go:1.1,2.1 $cov 0"; }
  [ "$uncov" -gt 0 ] && { echo "x/$mod/b.go:3.1,4.1 $uncov 0"; echo "x/$mod/b.go:3.1,4.1 $uncov 0"; }
} >"$prof"
exit 0
STUB
chmod +x "$gobin"

# Build a throwaway module layout so cwd basenames are stable (alpha, beta).
mkdir -p "$work/repo/scripts" "$work/repo/alpha" "$work/repo/beta"
cp "$script" "$work/repo/scripts/fuzz-coverage-global.sh"

run() { # COV-map, args...
	local cov="$1"; shift
	COV="$cov" SERF_FUZZ_GO="$gobin" SERF_FUZZ_CAPPED="$cap" \
		bash "$work/repo/scripts/fuzz-coverage-global.sh" --modules "alpha beta" "$@" 2>&1
}

echo "== parse + repo total =="
out="$(run "alpha=8 10,beta=3 10")"
has "$out" "alpha" "alpha row present"
# alpha 8/10=80.0%, beta 3/10=30.0%, repo 11/20=55.0%
has "$out" "80.0%" "alpha pct = 80.0%"
has "$out" "30.0%" "beta pct = 30.0%"
has "$out" "55.0%" "repo total = 55.0%"

echo "== bless writes raise-only floors =="
run "alpha=8 10,beta=3 10" --bless >/dev/null
floors="$work/repo/scripts/fuzzcov-global-floors.txt"
has "$(cat "$floors")" "alpha 80.0" "blessed alpha floor 80.0"
has "$(cat "$floors")" "beta 30.0" "blessed beta floor 30.0"
# Re-bless with LOWER current must not lower the floor.
run "alpha=5 10,beta=3 10" --bless >/dev/null
has "$(cat "$floors")" "alpha 80.0" "bless never lowers (alpha stays 80.0 despite 50%)"

echo "== check passes at/above floor, fails below =="
set +e; run "alpha=8 10,beta=3 10" --check >/dev/null 2>&1; rc=$?; set -e
[ "$rc" -eq 0 ] && ok "check passes when coverage meets floors" || bad "check should pass at floor (rc=$rc)"
set +e; out="$(run "alpha=5 10,beta=3 10" --check)"; rc=$?; set -e
[ "$rc" -ne 0 ] && ok "check fails on a regression below floor" || bad "check should fail below floor"
has "$out" "REGRESSION" "regression reported for alpha"
# Within tolerance (floor 80.0, value 79.7 with default 0.5pp) must still pass.
set +e; run "alpha=797 1000,beta=3 10" --check >/dev/null 2>&1; rc=$?; set -e
[ "$rc" -eq 0 ] && ok "check tolerates sub-floor wobble within 0.5pp" || bad "tolerance band not applied (rc=$rc)"

echo "== --with-full prints the context column =="
# The stub returns the same coverage for both passes, so fuzz==full -> ratio 100%.
out="$(run "alpha=8 10,beta=3 10" --with-full)"
has "$out" "fuzz/full" "with-full: header has the fuzz/full column"
has "$out" "100%" "with-full: fuzz/full ratio computed (100% when fuzz==full in the stub)"

echo "----"
echo "fuzz-coverage-global-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
