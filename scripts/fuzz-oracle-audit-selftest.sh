#!/usr/bin/env bash
# fuzz-oracle-audit-selftest.sh — deterministic integration test for
# fuzz-oracle-audit.sh. Like the bisect self-test, the audit IS a real git
# worktree + real `go test`, so stubs would prove nothing: this builds a
# throwaway Go module with a fuzz target whose oracle catches one injected fault
# and is blind to another, plus a non-applying ("rotted") patch and an unaudited
# target, then asserts the audit classifies all four correctly. Only the registry
# source (run-fuzz.sh --list) is stubbed.
#
# Run: scripts/fuzz-oracle-audit-selftest.sh  (also: make fuzz-oracle-audit-selftest)
set -uo pipefail

audit="$(cd "$(dirname "$0")" && pwd)/fuzz-oracle-audit.sh"
work="$(mktemp -d -t fuzz-oracle-audit-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok()  { printf 'ok   - %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL - %s\n' "$1"; fail=$((fail + 1)); }
assert_has() { if printf '%s' "$1" | grep -qF -- "$2"; then ok "$3"; else bad "$3 (missing: $2)"; printf '%s\n' "$1" | sed 's/^/    | /'; fi; }
assert_eq()  { if [ "$1" = "$2" ]; then ok "$3"; else bad "$3 (want '$2', got '$1')"; fi; }

repo="$work/repo"
mkdir -p "$repo/fuzz/mutations"
git -C "$repo" init -q
git -C "$repo" config user.email selftest@example.com
git -C "$repo" config user.name selftest

cat >"$repo/go.mod" <<'EOF'
module example.com/audittest

go 1.25.0
EOF

# The system under test, written body-by-body so each mutation is a clean diff.
write_double() { printf 'package audittest\n\nfunc Double(n int) int {\n%s\n}\n' "$1" >"$repo/double.go"; }

# The fuzz target + its oracle: Double(n) must equal n*2. Seed corpus is {3}.
cat >"$repo/double_fuzz_test.go" <<'EOF'
package audittest

import "testing"

func FuzzDouble(f *testing.F) {
	f.Add(3)
	f.Fuzz(func(t *testing.T, n int) {
		if got := Double(n); got != n*2 {
			t.Fatalf("Double(%d) = %d, want %d", n, got, n*2)
		}
	})
}
EOF

write_double "	return n + n"
git -C "$repo" add -A && git -C "$repo" commit -q -m "base: correct Double + oracle"

# caught.patch: Double is wrong for ALL n. The seed {3} reaches it; the oracle
# reddens → the audit must report "caught".
write_double "	return n + 1"
git -C "$repo" diff >"$repo/fuzz/mutations/caught.patch"
git -C "$repo" checkout -q -- double.go

# blind.patch: Double is wrong ONLY for n==7, which no seed reaches, so the
# target stays green → the audit must report "BLIND" (the machinery correctly
# surfaces a non-reddening oracle; in real use the seed-pairing rule prevents it).
write_double "	if n == 7 {
		return 999
	}
	return n + n"
git -C "$repo" diff >"$repo/fuzz/mutations/blind.patch"
git -C "$repo" checkout -q -- double.go

# broken.patch: applies cleanly but does not COMPILE (calls an undefined symbol).
# It must score ERR, never "caught" — a non-zero `go test` from a build failure is
# not the oracle reddening.
write_double "	return undefinedSymbol(n)"
git -C "$repo" diff >"$repo/fuzz/mutations/broken.patch"
git -C "$repo" checkout -q -- double.go

# rot.patch: references context that does not exist → git apply fails → "ROT".
cat >"$repo/fuzz/mutations/rot.patch" <<'EOF'
diff --git a/double.go b/double.go
index 1111111..2222222 100644
--- a/double.go
+++ b/double.go
@@ -1,2 +1,2 @@
 package audittest
-func ThisLineDoesNotExist() {}
+func ThisLineDoesNotExist() { panic("x") }
EOF

# Manifest: id <TAB> module:FuzzName <TAB> patchfile <TAB> description
printf 'caught\t.:FuzzDouble\tcaught.patch\tDouble off by all\n'   >"$repo/fuzz/mutations/manifest.tsv"
printf 'blind\t.:FuzzDouble\tblind.patch\tDouble off only at n==7\n' >>"$repo/fuzz/mutations/manifest.tsv"
printf 'rot\t.:FuzzDouble\trot.patch\tstale patch\n'                >>"$repo/fuzz/mutations/manifest.tsv"
printf 'broken\t.:FuzzDouble\tbroken.patch\tdoes not compile\n'     >>"$repo/fuzz/mutations/manifest.tsv"

# Registry stub: FuzzDouble has mutations; FuzzUnaudited has none (gap report).
runner="$work/runner-stub.sh"
cat >"$runner" <<'STUB'
#!/usr/bin/env bash
for a in "$@"; do
	if [ "$a" = "--list" ]; then
		echo "native:.:.:FuzzDouble"
		echo "native:.:.:FuzzUnaudited"
		exit 0
	fi
done
exit 0
STUB
chmod +x "$runner"

run_audit() {
	SERF_FUZZ_REPO_ROOT="$repo" SERF_FUZZ_RUNNER="$runner" SERF_FUZZ_TAGS="" \
		bash "$audit" "$@" 2>&1
}

# --- full audit --------------------------------------------------------------
set +e
out="$(run_audit)"; rc=$?
set -e
echo "$out" | sed 's/^/    | /'
assert_has "$out" "ok   caught"  "caught mutation: oracle reddened"
assert_has "$out" "BLIND blind"  "blind mutation: non-reddening oracle reported"
assert_has "$out" "ROT  rot"     "rotted patch: reported, not silently skipped"
assert_has "$out" "ERR  broken"  "non-compiling mutation: scored ERR, not a false catch"
assert_has "$out" "does not compile" "non-compiling mutation: diagnosed as a build failure"
assert_has "$out" "UNAUDITED: .:FuzzUnaudited" "gap report flags the unaudited target"
assert_eq "$rc" "1" "audit exits non-zero when an oracle is blind or a patch rotted"
# The disposable worktree (mktemp'd as fuzz-oracle-audit.XXXX) is gone; only the
# throwaway repo's own main worktree should remain registered.
if git -C "$repo" worktree list | grep -q 'fuzz-oracle-audit\.'; then
	bad "audit left a worktree behind"
else
	ok "audit cleaned up its worktree"
fi

# --- gap-only mode -----------------------------------------------------------
set +e
out2="$(run_audit --gap-only)"; rc2=$?
set -e
assert_has "$out2" "UNAUDITED: .:FuzzUnaudited" "gap-only: lists the unaudited target"
assert_eq "$rc2" "0" "gap-only: exits zero (report, not a gate)"

echo "----"
echo "fuzz-oracle-audit-selftest: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
