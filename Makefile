# Every non-fuzz Go module in the workspace. Under go.work, `./...` resolves
# per-module, so gates and lint must loop modules explicitly. Fuzz targets and
# the fuzz toolkit module run through the explicit fuzz targets below, not the
# regular test gate.
GO_MODULES := . agent llm auth envvars invariant identifier
FUZZ_GO_MODULES := $(GO_MODULES) fuzz

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
DEV_TOOLING_TEST_SCRIPTS := lib/private-go-home gate/merge-approval-gate ops/setup-gocache web/web-preflight lib/live-eval-isolation fuzz/fuzz-bisect fuzz/fuzz-oracle-audit coverage/coverage-gaps gate/test-timing-budget lib/scratch-lib

define run_quiet_lint
	@set -u; log="$$(mktemp "$${TMPDIR:-/tmp}/evener-lint-check.XXXXXX")" || exit 1; \
	trap 'rm -f "$$log"' EXIT HUP INT TERM; \
	if ( $(1) ) >"$$log" 2>&1; then \
		if [ "$(2)" = preserve-gitleaks-warning ]; then \
			grep -F 'warning: gitleaks not installed; skipping repo secret scan' "$$log" >&2 || :; \
		fi; \
	else \
		status=$$?; cat "$$log"; exit $$status; \
	fi
endef

.DEFAULT_GOAL := build

include $(dir $(lastword $(MAKEFILE_LIST)))make/*.mk
