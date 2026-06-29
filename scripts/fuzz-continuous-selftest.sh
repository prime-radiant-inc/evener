#!/usr/bin/env bash
# fuzz-continuous-selftest.sh — deterministic self-test for fuzz-continuous.sh.
#
# It exercises the loop's load-bearing logic — native-only rotation, round-robin
# vs sweep ordering, the target-subset filter, flag passthrough to the per-turn
# engine, the total/max-turns stop conditions, and the new-crasher session delta
# — WITHOUT any real fuzzing. The registry source and the triage engine are
# replaced by controllable stubs; a throwaway ledger stands in for real state.
#
# Run: scripts/fuzz-continuous-selftest.sh   (also: make fuzz-continuous-selftest)
set -uo pipefail

loop="$(cd "$(dirname "$0")" && pwd)/fuzz-continuous.sh"
work="$(mktemp -d -t fuzz-continuous-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok()  { printf 'ok   - %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL - %s\n' "$1"; fail=$((fail + 1)); }
assert_has() { if printf '%s' "$1" | grep -qF -- "$2"; then ok "$3"; else bad "$3 (missing: $2)"; printf '%s\n' "$1" | sed 's/^/    | /'; fi; }
assert_eq()  { if [ "$1" = "$2" ]; then ok "$3"; else bad "$3 (want '$2', got '$1')"; fi; }

# --- stubs -------------------------------------------------------------------
repo="$work/repo"
mkdir -p "$repo/fuzz/state"
echo '{}' >"$repo/fuzz/state/ledger.json"

# Stub registry: three native targets plus a rapid one (which must be excluded).
runner="$work/runner-stub.sh"
cat >"$runner" <<'STUB'
#!/usr/bin/env bash
for a in "$@"; do
	if [ "$a" = "--list" ]; then
		echo "native:agent:.:FuzzAlpha"
		echo "native:llm:./providers/x:FuzzBeta"
		echo "native:.:./appwire:FuzzGamma"
		echo "rapid:agent:.:TestDeltaSeqFuzz"
		exit 0
	fi
done
exit 0
STUB
chmod +x "$runner"

# Stub triage engine: logs its argument vector (one line per turn) and, when
# STUB_MAKE_CRASHER=1, records a crasher signature in the ledger so the loop's
# before/after delta fires.
triage="$work/triage-stub.sh"
cat >"$triage" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$TRIAGE_LOG"
if [ "${STUB_MAKE_CRASHER:-0}" = "1" ]; then
	led="$SERF_FUZZ_REPO_ROOT/fuzz/state/ledger.json"
	tmp="$(mktemp)"
	jq '. + {"sig-boom": {status: "found"}}' "$led" >"$tmp" && mv "$tmp" "$led"
fi
exit 0
STUB
chmod +x "$triage"

run_loop() {
	TRIAGE_LOG="$work/triage.log" \
	SERF_FUZZ_REPO_ROOT="$repo" \
	SERF_FUZZ_RUNNER="$runner" \
	SERF_FUZZ_TRIAGE="$triage" \
		bash "$loop" "$@" 2>&1
}
target_of() { sed -n "${1}p" "$work/triage.log" | awk '{print $NF}'; }

# --- scenario 1: round-robin rotation, rapid excluded ------------------------
: >"$work/triage.log"
out="$(run_loop --max-turns 5)"
assert_eq "$(wc -l <"$work/triage.log" | tr -d ' ')" "5" "round-robin: 5 turns ran"
assert_eq "$(target_of 1)" "agent:FuzzAlpha" "round-robin: turn 1 = Alpha"
assert_eq "$(target_of 2)" "llm:FuzzBeta" "round-robin: turn 2 = Beta"
assert_eq "$(target_of 3)" ".:FuzzGamma" "round-robin: turn 3 = Gamma"
assert_eq "$(target_of 4)" "agent:FuzzAlpha" "round-robin: turn 4 wraps to Alpha"
assert_eq "$(target_of 5)" "llm:FuzzBeta" "round-robin: turn 5 = Beta"
assert_has "$out" "no new crashers this session" "round-robin: clean session reported"
assert_eq "$(grep -c 'TestDeltaSeqFuzz' "$work/triage.log")" "0" "rapid target excluded from rotation"

# --- scenario 2: sweep mode runs each target once per round ------------------
: >"$work/triage.log"
run_loop --sweep --max-turns 3 >/dev/null
assert_eq "$(target_of 1)" "agent:FuzzAlpha" "sweep: Alpha first"
assert_eq "$(target_of 2)" "llm:FuzzBeta" "sweep: Beta second"
assert_eq "$(target_of 3)" ".:FuzzGamma" "sweep: Gamma third"

# --- scenario 3: flags pass through to the triage engine ---------------------
: >"$work/triage.log"
run_loop --time 5m --dry-run --no-pr --max-turns 1 >/dev/null
line1="$(sed -n '1p' "$work/triage.log")"
assert_has "$line1" "--time 5m" "passthrough: --time reaches triage"
assert_has "$line1" "--dry-run" "passthrough: --dry-run reaches triage"
assert_has "$line1" "--no-pr" "passthrough: --no-pr reaches triage"
assert_has "$line1" "--no-corpus" "passthrough: per-turn --no-corpus applied"

# --- scenario 4: target subset filter ----------------------------------------
: >"$work/triage.log"
run_loop --max-turns 2 agent:FuzzAlpha >/dev/null
assert_eq "$(target_of 1)" "agent:FuzzAlpha" "filter: turn 1 = Alpha"
assert_eq "$(target_of 2)" "agent:FuzzAlpha" "filter: only Alpha rotates"

# --- scenario 5: a new crasher is reported and exits non-zero ----------------
echo '{}' >"$repo/fuzz/state/ledger.json"
: >"$work/triage.log"
set +e
out="$(STUB_MAKE_CRASHER=1 run_loop --max-turns 1)"; rc=$?
set -e
assert_has "$out" "NEW crasher signature(s) this session" "crasher: session delta reported"
assert_has "$out" "sig-boom" "crasher: signature listed"
assert_eq "$rc" "1" "crasher: session exits non-zero"

# --- scenario 6: a zero total budget runs no turns ---------------------------
echo '{}' >"$repo/fuzz/state/ledger.json"
: >"$work/triage.log"
out="$(run_loop --total 0s)"
assert_eq "$(wc -l <"$work/triage.log" | tr -d ' ')" "0" "zero budget: no turns run"
assert_has "$out" "turns: 0" "zero budget: summary reports 0 turns"

# --- summary -----------------------------------------------------------------
echo "----"
echo "fuzz-continuous-selftest: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
