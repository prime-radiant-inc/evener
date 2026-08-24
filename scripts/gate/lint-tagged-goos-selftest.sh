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

casing_fixture="$work/casing-fixture"
mkdir -p "$casing_fixture"

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

# Keep this fixture separate from the undefined-symbol fixture above: the
# casing files must compile and vet cleanly so a tagliatelle result, not vet,
# is what decides the case. Copy the production config because this module is
# outside the repository tree where golangci-lint normally discovers it.
cp "$repo/.golangci.yml" "$casing_fixture/.golangci.yml"
cat >"$casing_fixture/go.mod" <<'EOF'
module evener-lint-tagged-goos-casing-fixture

go 1.27.0
EOF
cat >"$casing_fixture/fixture.go" <<'EOF'
package fixture
EOF
cat >"$casing_fixture/evenerfuzz_linux.go" <<'EOF'
//go:build linux && evenerfuzz

package fixture

type EvenerfuzzConfig struct {
	BadName string `json:"badName"`
}
EOF
cat >"$casing_fixture/eval_linux.go" <<'EOF'
//go:build linux && eval

package fixture

type EvalConfig struct {
	BadName string `json:"badName"`
}
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

# These sources are valid Go and vet cleanly on Linux, but the Linux-only
# tagliatelle controls reject their deliberately bad JSON casing. The host
# pass must miss them on Darwin; the production targets must not.
for tag in evenerfuzz eval; do
	vet_out="$work/${tag}-casing-linux-vet.out"
	(
		cd "$casing_fixture" || exit 1
		env GOENV=off GOFLAGS= GOWORK=off GOOS=linux go vet -tags "$tag" ./...
	) >"$vet_out" 2>&1
	status=$?
	assert_eq "$status" "0" "Linux vet accepts the clean $tag casing fixture"

	host_out="$work/${tag}-casing-darwin-lint.out"
	(
		cd "$casing_fixture" || exit 1
		env GOENV=off GOFLAGS= GOWORK=off GOOS=darwin \
			golangci-lint run --allow-parallel-runners --build-tags "$tag" \
				--enable-only tagliatelle ./...
	) >"$host_out" 2>&1
	status=$?
	assert_eq "$status" "0" "pre-fix Darwin tagliatelle misses Linux-only $tag casing"

	linux_out="$work/${tag}-casing-linux-lint.out"
	(
		cd "$casing_fixture" || exit 1
		env GOENV=off GOFLAGS= GOWORK=off GOOS=linux \
			golangci-lint run --allow-parallel-runners --build-tags "$tag" \
				--enable-only tagliatelle ./...
	) >"$linux_out" 2>&1
	status=$?
	assert_nonzero "$status" "Linux tagliatelle rejects the bad $tag JSON casing"
	assert_has "$linux_out" "json(snake): got 'badName' want 'bad_name'" \
		"Linux tagliatelle reports the $tag casing violation"

	target_out="$work/${tag}-casing-target.out"
	env GOENV=off GOFLAGS= GOWORK=off GOOS=darwin \
		make -C "$repo" --no-print-directory FUZZ_GO_MODULES="$casing_fixture" \
			"lint-${tag}" >"$target_out" 2>&1
	status=$?
	assert_nonzero "$status" "lint-${tag} catches Linux-only JSON casing"
	assert_has "$target_out" "json(snake): got 'badName' want 'bad_name'" \
		"lint-${tag} reports the Linux-only casing violation"
done

selftest_summary
