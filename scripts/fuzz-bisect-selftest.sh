#!/usr/bin/env bash
# fuzz-bisect-selftest.sh — deterministic integration test for fuzz-bisect.sh.
#
# A bisect tool cannot be honestly tested with stubs — it IS git bisect plus a
# real replay — so this builds a throwaway Go module whose fuzz target crashes
# on one input only AFTER a specific commit, then asserts fuzz-bisect names that
# commit. The only stub is the registry source (run-fuzz.sh --list); the git
# history, the `go test` replays, and git bisect itself are all real.
#
# Run: scripts/fuzz-bisect-selftest.sh   (also: make fuzz-bisect-selftest)
set -uo pipefail

bisect="$(cd "$(dirname "$0")" && pwd)/fuzz-bisect.sh"
work="$(mktemp -d -t fuzz-bisect-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok()  { printf 'ok   - %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL - %s\n' "$1"; fail=$((fail + 1)); }

repo="$work/repo"
mkdir -p "$repo"
git -C "$repo" init -q
git -C "$repo" config user.email selftest@example.com
git -C "$repo" config user.name selftest

cat >"$repo/go.mod" <<'EOF'
module example.com/bisecttest

go 1.25.0
EOF
cat >"$repo/doc.go" <<'EOF'
package bisecttest
EOF

# A fuzz target that, in the base revision, never crashes.
write_target() { # $1 = crash body
	cat >"$repo/fuzz_test.go" <<EOF
package bisecttest

import "testing"

func FuzzBoom(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		$1
	})
}
EOF
}

write_target "_ = b"
git -C "$repo" add -A && git -C "$repo" commit -q -m "base: harmless fuzz target"
good_sha="$(git -C "$repo" rev-parse HEAD)"

write_target 'if string(b) == "boom" { panic("boom") }'
git -C "$repo" add -A && git -C "$repo" commit -q -m "introduce the boom crash"
culprit_sha="$(git -C "$repo" rev-parse HEAD)"

# A couple of later, innocent commits so the bad end is not the culprit itself.
echo "// later" >>"$repo/doc.go"
git -C "$repo" add -A && git -C "$repo" commit -q -m "later: unrelated change"
echo "// later2" >>"$repo/doc.go"
git -C "$repo" add -A && git -C "$repo" commit -q -m "later2: unrelated change"

# The crasher corpus file (the boom input) in Go's fuzz format.
crasher="$work/crasher"
printf 'go test fuzz v1\n[]byte("boom")\n' >"$crasher"

# Stub registry: the one native target, module "." package "." name FuzzBoom.
runner="$work/runner-stub.sh"
cat >"$runner" <<'STUB'
#!/usr/bin/env bash
for a in "$@"; do [ "$a" = "--list" ] && { echo "native:.:.:FuzzBoom"; exit 0; }; done
exit 0
STUB
chmod +x "$runner"

# Run the bisect. SERF_FUZZ_TAGS is emptied: the throwaway module has no build
# tags, and the real default (serffuzz) would still build, but empty keeps the
# test independent of that tag.
out="$(SERF_FUZZ_REPO_ROOT="$repo" SERF_FUZZ_RUNNER="$runner" SERF_FUZZ_TAGS="" \
	bash "$bisect" --target .:FuzzBoom --crasher "$crasher" --good "$good_sha" --bad HEAD 2>&1)"

echo "$out" | sed 's/^/    | /'

if printf '%s' "$out" | grep -q "$culprit_sha"; then
	ok "bisect identified the introducing commit ($culprit_sha)"
else
	bad "bisect did not name the introducing commit ($culprit_sha)"
fi
if printf '%s' "$out" | grep -qi "is the first bad commit"; then
	ok "bisect converged to a first bad commit"
else
	bad "bisect did not converge"
fi
# The tree is restored afterwards (no lingering bisect).
if git -C "$repo" bisect log >/dev/null 2>&1; then
	bad "bisect state left behind (not reset)"
else
	ok "working tree restored (bisect reset)"
fi

echo "----"
echo "fuzz-bisect-selftest: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
