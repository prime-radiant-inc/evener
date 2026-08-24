#!/usr/bin/env bash
# lint-tagged-goos-selftest.sh — proves the tagged lint floors compile files
# behind both tag and GOOS constraints. The fixture uses the real Go toolchain
# and the real Make targets; no go binary or linter is faked.
set -uo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/../lib/selftest-lib.sh"

trap 'scratch_rm' EXIT
scratch_dir work evener-lint-tagged-goos

repo="$(cd "$(dirname "$0")/../.." && pwd -P)"
fixture="$work/fixture"
mkdir -p "$fixture"

cat >"$fixture/go.mod" <<'EOF'
module evener-lint-tagged-goos-fixture

go 1.27.0
EOF
cat >"$fixture/fixture.go" <<'EOF'
package fixture
EOF
cat >"$fixture/evenerfuzz_linux.go" <<'EOF'
//go:build linux && evenerfuzz

package fixture

var _ = missingEvenerfuzzSymbol
EOF
cat >"$fixture/eval_linux.go" <<'EOF'
//go:build linux && eval

package fixture

var _ = missingEvalSymbol
EOF

assert_nonzero() {
	if [ "$1" -ne 0 ]; then
		ok "$2"
	else
		bad "$2 (expected a nonzero status)"
	fi
}

symbol_for_tag() {
	case "$1" in
		evenerfuzz) printf '%s\n' missingEvenerfuzzSymbol ;;
		eval) printf '%s\n' missingEvalSymbol ;;
		*) return 1 ;;
	esac
}

# These are the exact pre-fix vet commands from lint-evenerfuzz/lint-eval:
# on macOS they select no files from this fixture, despite the Linux builds
# being broken. Running them for real proves the old target's blind spot rather
# than merely asserting that the Makefile contains a command.
for tag in evenerfuzz eval; do
	out="$work/${tag}-darwin.out"
	symbol="$(symbol_for_tag "$tag")"
	(
		cd "$fixture" || exit 1
		env GOENV=off GOFLAGS= GOWORK=off GOOS=darwin go vet -tags "$tag" ./...
	) >"$out" 2>&1
	status=$?
	assert_eq "$status" "0" "pre-fix Darwin vet misses the broken linux && $tag fixture"
	assert_not_has "$out" "$symbol" "pre-fix Darwin vet emits no Linux-only $tag diagnostic"
done

# The production targets must now select Linux explicitly and expose the real
# type errors. Use the actual Makefile and real toolchain, with only its module
# list redirected to this throwaway fixture.
for target_tag in evenerfuzz eval; do
	out="$work/${target_tag}-target.out"
	symbol="$(symbol_for_tag "$target_tag")"
	env GOENV=off GOFLAGS= GOWORK=off GOOS=darwin \
		make -C "$repo" --no-print-directory FUZZ_GO_MODULES="$fixture" "lint-${target_tag}" \
		>"$out" 2>&1
	status=$?
	assert_nonzero "$status" "lint-${target_tag} catches a broken linux && ${target_tag} source"
	assert_has "$out" "undefined: $symbol" "lint-${target_tag} reports the Linux-only ${target_tag} diagnostic"
done

selftest_summary
