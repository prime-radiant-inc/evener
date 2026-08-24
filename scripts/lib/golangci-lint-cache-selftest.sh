#!/usr/bin/env bash
# golangci-lint-cache-selftest.sh — exercise cache isolation with the real
# pinned linter in two git worktrees and two configuration states.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
. "$repo_root/scripts/lib/selftest-lib.sh"

scratch_dir work golangci-lint-cache-selftest

seed="$work/seed"
tree_a="$work/worktree A"
tree_b="$work/worktree B"
cleanup() {
	if [ -d "$seed/.git" ]; then
		git -C "$seed" worktree remove --force "$tree_a" >/dev/null 2>&1 || :
		git -C "$seed" worktree remove --force "$tree_b" >/dev/null 2>&1 || :
	fi
	scratch_rm
}
trap cleanup EXIT

helper="$repo_root/scripts/lib/golangci-lint-cache.sh"
if [ ! -x "$helper" ]; then
	bad "the production cache-path helper is missing or not executable"
	selftest_summary
	exit $?
fi

linter="$(command -v golangci-lint 2>/dev/null || true)"
if [ -z "$linter" ]; then
	bad "golangci-lint is not installed"
	selftest_summary
	exit $?
fi
if ! golangci-lint version 2>"$work/version.err" | grep -qF 'version 2.13.1'; then
	bad "golangci-lint is not the pinned 2.13.1 version: $(cat "$work/version.err")"
	selftest_summary
	exit $?
fi

cache_home="$work/cache home"
mkdir -p "$cache_home"

make_worktrees() {
	mkdir -p "$seed"
	git init -q "$seed" || return 1
	git -C "$seed" config user.email selftest@example.invalid
	git -C "$seed" config user.name golangci-lint-cache-selftest
	cat >"$seed/go.mod" <<'EOF'
module example.com/golangci-cache-selftest

go 1.27
EOF
	cat >"$seed/x.go" <<'EOF'
package x

type T struct {
	MCPServers string `json:"mcpServers"`
}
EOF
	cat >"$seed/suppressed.go" <<'EOF'
package x

type S struct {
	MCPServers string `json:"mcpServers"` //nolint:tagliatelle // upstream wire key
}
EOF
	cat >"$seed/.golangci.yml" <<EOF
version: "2"
linters:
  default: none
  enable:
    - tagliatelle
  settings:
    tagliatelle:
      case:
        rules:
          json: snake
EOF
	git -C "$seed" add go.mod x.go suppressed.go .golangci.yml
	git -C "$seed" commit -qm fixture || return 1
	git -C "$seed" worktree add --detach -q "$tree_a" HEAD || return 1
	git -C "$seed" worktree add --detach -q "$tree_b" HEAD || return 1
	cat >>"$tree_a/.golangci.yml" <<'EOF'
  exclusions:
    rules:
      - path: ^x\.go$
        linters:
          - tagliatelle
EOF
}

if ! make_worktrees; then
	bad "could not create the two git worktree fixtures"
	selftest_summary
	exit $?
fi

cache_a="$(cd "$tree_a" && XDG_CACHE_HOME="$cache_home" "$helper")"
cache_a_again="$(cd "$tree_a" && XDG_CACHE_HOME="$cache_home" "$helper")"
cache_b="$(cd "$tree_b" && XDG_CACHE_HOME="$cache_home" "$helper")"
assert_eq "$cache_a_again" "$cache_a" "one worktree reuses its stable cache path"
if [ "$cache_a" != "$cache_b" ]; then
	ok "different worktrees receive different cache paths"
else
	bad "different worktrees received the same cache path"
fi
case "$cache_a$cache_b" in
	*" "*) ok "cache paths remain usable when worktree/cache roots contain spaces" ;;
	*) bad "fixture did not exercise a space-containing cache path" ;;
esac

run_lint() {
	local tree="$1" cache="$2" output="$3"
	(cd "$tree" && XDG_CACHE_HOME="$cache_home" GOLANGCI_LINT_CACHE="$cache" \
		"$linter" run --config .golangci.yml ./... >"$output" 2>&1)
}

if run_lint "$tree_a" "$cache_a" "$work/a.out"; then
	ok "the excluded finding passes in configuration A"
else
	bad "configuration A unexpectedly reported a finding: $(cat "$work/a.out")"
fi
if run_lint "$tree_b" "$cache_b" "$work/b.out"; then
	bad "configuration B unexpectedly passed without the exclusion"
else
	if grep -qF 'x.go:4:' "$work/b.out" && ! grep -qF 'suppressed.go' "$work/b.out" && ! grep -qF "$tree_a" "$work/b.out"; then
		ok "configuration B reports its own finding, not a sibling path"
	else
		bad "configuration B failed without naming its own worktree: $(cat "$work/b.out")"
	fi
fi
if [ -d "$cache_a" ] && [ -d "$cache_b" ] && [ "$cache_a" != "$cache_b" ]; then
	ok "both real linter runs populate separate reusable cache directories"
else
	bad "real linter runs did not leave both isolated cache directories"
fi
case "$cache_a" in
	"$tree_a"/*|"$tree_b"/*) bad "a cache directory is inside a worktree" ;;
	*) ok "isolated caches stay outside the worktrees and secret-scan surface" ;;
esac
if make -s -C "$repo_root" GOLANGCI_LINT_CACHE="$cache_a" lint-cache-clean >"$work/clean.out" 2>&1; then
	if [ ! -e "$cache_a" ] && [ -d "$cache_b" ]; then
		ok "the current worktree cache is cleanable without touching its sibling"
	else
		bad "lint-cache-clean exited 0 but left the current cache: $(cat "$work/clean.out")"
	fi
else
	bad "lint-cache-clean failed: $(cat "$work/clean.out")"
fi

selftest_summary
