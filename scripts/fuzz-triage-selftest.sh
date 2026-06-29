#!/usr/bin/env bash
# fuzz-triage-selftest.sh — deterministic self-test for scripts/fuzz-triage.sh.
#
# It exercises the load-bearing triage logic (ledger reconcile, crasher
# discovery, the Go-native flake-guard, the three dedup layers, the PR-open
# decision, and the ledger record) WITHOUT any real side effects: no real fuzz
# search, no real crash, no real `gh` push/PR. Every external the tool touches —
# the search runner, the `go` toolchain (flake-guard + reconcile replays), and
# `gh` — is replaced by a controllable stub on PATH, against a throwaway git
# repo. Synthetic failures stand in for real crashers (the test-without-side-
# effects discipline the 8.7 plan mandates).
#
# Run: scripts/fuzz-triage-selftest.sh   (also: make fuzz-triage-selftest)
set -uo pipefail

triage="$(cd "$(dirname "$0")" && pwd)/fuzz-triage.sh"
work="$(mktemp -d -t fuzz-triage-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok()   { printf 'ok   - %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf 'FAIL - %s\n' "$1"; fail=$((fail + 1)); }

assert_has()  { if printf '%s' "$1" | grep -qF -- "$2"; then ok "$3"; else bad "$3 (missing: $2)"; printf '%s\n' "$1" | sed 's/^/    | /'; fi; }
assert_not()  { if printf '%s' "$1" | grep -qF -- "$2"; then bad "$3 (unexpected: $2)"; printf '%s\n' "$1" | sed 's/^/    | /'; else ok "$3"; fi; }

# --- stubs -------------------------------------------------------------------

bindir="$work/bin"
mkdir -p "$bindir"

# Stub run-fuzz.sh: prints the target list for --list; otherwise (when
# STUB_MAKE_CRASHER=1) writes a synthetic Go-native crasher into the repo so the
# snapshot-diff discovers it.
cat >"$bindir/run-fuzz-stub.sh" <<'STUB'
#!/usr/bin/env bash
for a in "$@"; do [ "$a" = "--list" ] && { echo "agent:.:FuzzFoo"; exit 0; }; done
if [ "${STUB_MAKE_CRASHER:-0}" = "1" ]; then
	d="$STUB_REPO/agent/testdata/fuzz/FuzzFoo"
	mkdir -p "$d"
	printf 'go test fuzz v1\n[]byte("boom")\n' >"$d/abc123def456789"
fi
exit 0
STUB

# Stub go: env/list feed the (skipped-in-dry-run) corpus step; `test` is the
# flake-guard + reconcile replay whose exit code each scenario controls.
cat >"$bindir/go" <<'STUB'
#!/usr/bin/env bash
case "$1" in
	env)  echo "${STUB_GOCACHE:-/nonexistent-gocache}"; exit 0 ;;
	list) echo "example.com/stub/pkg"; exit 0 ;;
	test)
		if [ -n "${STUB_GO_FLAKY:-}" ]; then
			cnt="${STUB_COUNTER:-/tmp/stubgo.cnt}"
			n=$(cat "$cnt" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$cnt"
			# fail (1) on odd run, pass (0) on even -> passes on replay 2.
			[ $((n % 2)) -eq 0 ] && exit 0 || exit 1
		fi
		exit "${STUB_GO_TEST_EXIT:-1}"
		;;
esac
exit 0
STUB

# Stub gh: auth ok; `pr list` returns STUB_GH_PRLIST; `pr create` a fixed URL.
cat >"$bindir/gh" <<'STUB'
#!/usr/bin/env bash
[ "$1 $2" = "auth status" ] && exit "${STUB_GH_AUTH:-0}"
if [ "$1" = "pr" ]; then
	case "$2" in
		list)   echo "${STUB_GH_PRLIST:-[]}" ;;
		create) echo "https://github.com/serf/serf/pull/777" ;;
	esac
fi
exit 0
STUB

chmod +x "$bindir/go" "$bindir/gh" "$bindir/run-fuzz-stub.sh"

# fresh_repo: a throwaway git repo with a committed empty ledger/buckets and a
# committed FuzzFoo seed dir (so a NEW crasher shows as an individual untracked
# file, mirroring a real corpus directory).
fresh_repo() {
	local r="$work/repo-$1"
	rm -rf "$r"; mkdir -p "$r/fuzz/state" "$r/agent/testdata/fuzz/FuzzFoo"
	git -C "$r" init -q -b main
	git -C "$r" config user.email selftest@example.com
	git -C "$r" config user.name selftest
	echo '{}' >"$r/fuzz/state/ledger.json"
	echo '{}' >"$r/fuzz/state/buckets.json"
	printf 'go test fuzz v1\n[]byte("seed")\n' >"$r/agent/testdata/fuzz/FuzzFoo/seed0000"
	git -C "$r" add -A
	git -C "$r" commit -q -m init
	printf '%s' "$r"
}

run_triage() { # repo, extra-env..., -- args...
	local repo="$1"; shift
	local -a env=()
	while [ "$1" != "--" ]; do env+=("$1"); shift; done
	shift
	env -i PATH="$bindir:$PATH" HOME="$work" \
		SERF_FUZZ_REPO_ROOT="$repo" \
		SERF_FUZZ_RUNNER="$bindir/run-fuzz-stub.sh" \
		SERF_FUZZ_GH="$bindir/gh" \
		STUB_REPO="$repo" \
		"${env[@]}" \
		bash "$triage" "$@" 2>&1
}

echo "== fuzz-triage self-test =="

# Scenario 1: deterministic native crash -> would open exactly one PR (dry-run).
r=$(fresh_repo s1)
out=$(run_triage "$r" STUB_MAKE_CRASHER=1 STUB_GO_TEST_EXIT=1 -- --dry-run agent:FuzzFoo)
assert_has "$out" "crasher (native)" "s1: discovers the native crasher"
assert_has "$out" "would open PR fuzz/crash-abc123def456" "s1: deterministic crash -> open PR (dry-run)"
assert_not "$out" "dedup" "s1: a novel crash is not deduped"

# Scenario 2: dedup via the ledger (signature already filed) -> no PR.
r=$(fresh_repo s2)
jq '.["FuzzFoo:abc123def456789"] = {status:"found", sig:"abc123def456", pkg:"agent", run:"FuzzFoo/abc123def456789"}' \
	"$r/fuzz/state/ledger.json" >"$r/fuzz/state/ledger.json.tmp" && mv "$r/fuzz/state/ledger.json.tmp" "$r/fuzz/state/ledger.json"
# STUB_GO_TEST_EXIT=1 so reconcile keeps it `found` (replay still fails).
out=$(run_triage "$r" STUB_MAKE_CRASHER=1 STUB_GO_TEST_EXIT=1 -- --dry-run agent:FuzzFoo)
assert_has "$out" "dedup (ledger)" "s2: known signature deduped via ledger"
assert_not "$out" "would open PR" "s2: no PR for a known signature"

# Scenario 3: dedup via an existing PR (gh pr list non-empty) -> no PR.
r=$(fresh_repo s3)
out=$(run_triage "$r" STUB_MAKE_CRASHER=1 STUB_GO_TEST_EXIT=1 STUB_GH_PRLIST='[{"number":12}]' -- --dry-run agent:FuzzFoo)
assert_has "$out" "dedup (pr-exists): fuzz/crash-abc123def456" "s3: open PR deduped via gh pr list"
assert_not "$out" "would open PR" "s3: no second PR while one is open"

# Scenario 4: flaky failure (passes a replay within K) -> quarantine, no PR.
r=$(fresh_repo s4)
out=$(run_triage "$r" STUB_MAKE_CRASHER=1 STUB_GO_FLAKY=1 STUB_COUNTER="$work/s4.cnt" -- --dry-run agent:FuzzFoo)
assert_has "$out" "quarantine: survived a replay" "s4: flaky crash is quarantined"
assert_not "$out" "would open PR" "s4: no PR for a flaky crash"

# Scenario 5: reconcile flips a stale `found` entry to `fixed` (replay passes).
r=$(fresh_repo s5)
jq '.["FuzzFoo:old"] = {status:"found", sig:"oldsig000000", pkg:"agent", run:"FuzzFoo/old"}' \
	"$r/fuzz/state/ledger.json" >"$r/fuzz/state/ledger.json.tmp" && mv "$r/fuzz/state/ledger.json.tmp" "$r/fuzz/state/ledger.json"
# STUB_GO_TEST_EXIT=0: the bug is fixed, the replay passes. No new crasher.
out=$(run_triage "$r" STUB_MAKE_CRASHER=0 STUB_GO_TEST_EXIT=0 -- agent:FuzzFoo)
assert_has "$out" "reconcile FuzzFoo:old -> fixed" "s5: fixed bug reconciled to fixed"
status=$(jq -r '.["FuzzFoo:old"].status' "$r/fuzz/state/ledger.json")
[ "$status" = "fixed" ] && ok "s5: ledger persisted status=fixed" || bad "s5: ledger status=$status, want fixed"

# Scenario 6: non-dry --no-pr deterministic crash -> real branch + commit, found
# ledger entry, NO push (no remote needed). Exercises the write path.
r=$(fresh_repo s6)
out=$(run_triage "$r" STUB_MAKE_CRASHER=1 STUB_GO_TEST_EXIT=1 -- --no-pr agent:FuzzFoo)
assert_has "$out" "committed to local branch fuzz/crash-abc123def456" "s6: --no-pr commits to a local branch"
if git -C "$r" rev-parse --verify -q fuzz/crash-abc123def456 >/dev/null; then ok "s6: crasher branch created"; else bad "s6: crasher branch missing"; fi
status=$(jq -r '.["FuzzFoo:abc123def456789"].status' "$r/fuzz/state/ledger.json" 2>/dev/null || echo "")
[ "$status" = "found" ] && ok "s6: ledger records status=found" || bad "s6: ledger status=$status, want found"

# Scenario 7: gh unauthenticated -> degrade gracefully (no push), default (PR) mode.
r=$(fresh_repo s7)
out=$(run_triage "$r" STUB_MAKE_CRASHER=1 STUB_GO_TEST_EXIT=1 STUB_GH_AUTH=1 -- agent:FuzzFoo)
assert_has "$out" "gh unavailable/unauthenticated" "s7: missing gh auth degrades gracefully"
assert_not "$out" "opened PR" "s7: no PR opened without gh auth"

echo "== $pass passed, $fail failed =="
[ "$fail" -eq 0 ]
