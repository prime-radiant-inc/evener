# Every non-fuzz Go module in the workspace. Under go.work, `./...` resolves
# per-module, so gates and lint must loop modules explicitly. Fuzz targets and
# the fuzz toolkit module run through the explicit fuzz targets below, not the
# regular test gate.
GO_MODULES := . agent llm auth envvars invariant identifier
FUZZ_GO_MODULES := $(GO_MODULES) fuzz

# golangci-lint caches raw findings before path-based suppressions and
# exclusions run. Keep that cache durable within this worktree, but never let
# sibling checkouts share it. `?=` deliberately preserves an operator's
# explicit GOLANGCI_LINT_CACHE override.
GOLANGCI_LINT_CACHE ?= $(shell scripts/lib/golangci-lint-cache.sh)

# DEV_TOOLING_TEST_SCRIPTS are the scripts/<name>-selftest.sh suites that pin
# the behaviour of evener's own tooling. Each is offline, deterministic and works
# only in throwaway fixtures, and each is the ONLY thing that pins its script's
# contract. A suite earns its slot by pinning outcomes of a tool the gate or CI
# depends on; hand-run conveniences fail loudly in front of whoever ran them
# and get no suite (docs/developing-evener/testing.md). scratch-lib tests the shared scratch
# guard directly, once — that every script's scratch stays inside TMPDIR and
# none of its recursive deletes takes a clobberable argument, whether it uses
# the guard or the pid-suffixed covscratch pattern, is enforced statically by
# the audits in scriptmktemp_audit_test.go, not by re-running suites under
# sabotage (kata 5hs2).
DEV_TOOLING_TEST_SCRIPTS := lib/private-go-home ops/setup-gocache web/web-preflight lib/live-eval-isolation fuzz/fuzz-bisect fuzz/fuzz-oracle-audit coverage/coverage-gaps gate/test-timing-budget lib/scratch-lib gate/lint-tagged-goos lib/golangci-lint-cache

define run_quiet_lint
	@set -u; start="$$(date +%s)"; log="$$(mktemp "$${TMPDIR:-/tmp}/evener-lint-check.XXXXXX")" || exit 1; \
	trap 'rm -f "$$log"' EXIT HUP INT TERM; \
	( $(1) ) >"$$log" 2>&1; status=$$?; \
	if [ "$$status" -eq 0 ]; then \
		if [ "$(2)" = preserve-gitleaks-warning ]; then \
			grep -F 'warning: gitleaks not installed; skipping repo secret scan' "$$log" >&2 || :; \
		fi; \
		elapsed="$$(($$(date +%s) - $$start))"; printf 'PASS %s (%ss)\n' "$@" "$$elapsed"; \
	else \
		cat "$$log"; elapsed="$$(($$(date +%s) - $$start))"; printf 'FAIL %s (%ss)\n' "$@" "$$elapsed" >&2; exit $$status; \
	fi
endef

.DEFAULT_GOAL := build

include $(dir $(lastword $(MAKEFILE_LIST)))make/*.mk
