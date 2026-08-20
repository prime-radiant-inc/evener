.PHONY: test-web test-web-browser test-dev-tooling test test-short test-race merge-approval-gate vet test-timing-budget test-timing-budget-selftest test-rebaseline

# test-web is the frontend's single gate entry point: typecheck, unit tests,
# then lint. The three checks are independent readers of the same sources, so
# the script runs them concurrently with per-check private HOME/TMPDIR/XDG
# roots; wall time is the slowest one (vitest) instead of the sum. A failure
# replays exactly the failing check's log.
test-web: web-preflight
	@scripts/web/test-web.sh

# test-web-browser runs the real browser-only frontend guards. They stay out
# of test-web because jsdom cannot evaluate the CSS cascade or browser geometry.
# The script runs every guard so one missing browser or failing case does not
# hide the remaining guard's verdict; exit status is the first nonzero one.
test-web-browser: web-preflight
	@scripts/web/test-web-browser.sh

# test-dev-tooling tests tooling, not the product, so it runs in
# `make merge-approval-gate` (where tooling regressions matter) and on demand
# — not in every inner-loop `make test` and not on `make lint`, which checks
# the product's code, not the tooling's behaviour.
# The wave runner (cmd/evener-test-dev-tooling) owns parallel
# spawn, signal forwarding to each suite's process group, per-suite TMPDIR
# isolation, and the leftover-files check that fails any suite that does not
# clean up after itself. Quiet on success; a failing suite's whole log is
# replayed. The runner's contract is pinned by
# cmd/evener-test-dev-tooling/wave_test.go, which runs in the ordinary Go test
# wave.
test-dev-tooling:
	@go run ./cmd/evener-test-dev-tooling $(DEV_TOOLING_TEST_SCRIPTS)

# test covers the Go modules AND the frontend. The frontend gate runs as a third
# concurrent stream inside run-module-tests.sh (MAKE is passed through so it can
# re-enter this Makefile's test-web target); it is node work, so it overlaps the
# Go waves instead of adding its runtime on the end. WEB=0 skips it.
test:
	@MODULES="$(GO_MODULES)" MAKE="$(MAKE)" scripts/gate/run-module-tests.sh -short -count=1

test-short:
	@$(MAKE) test

# merge-approval-gate is the canonical serial post-merge gate. Keep the
# explicit expansion in docs/developing-evener/testing.md for diagnosis and evidence. Sandboxed
# hosts are handled inside the tests themselves: the live/e2e families probe
# their own capabilities and t.Skip (internal/e2ecap).
merge-approval-gate:
	@$(MAKE) lint && \
		$(MAKE) build && \
		ROOT_FULL=1 $(MAKE) test && \
		$(MAKE) test-dev-tooling

# The permanent -race gate (CI), across every non-fuzz module. AGENT_PARALLEL=
# leaves the agent wave at GOMAXPROCS: under -race (~10x slower) extra
# parallelism just oversubscribes few-core CI and starves real per-test work past
# its timeouts. WEB=0: -race is a Go-toolchain gate, and the frontend suite is
# unaffected by it, so `make test` owns the web stream instead of paying it twice.
test-race:
	@MODULES="$(GO_MODULES)" WEB=0 AGENT_SHARDS=0 AGENT_PARALLEL= scripts/gate/run-module-tests.sh -race -short -count=1

vet:
	@for m in $(GO_MODULES); do (cd $$m && go vet ./...) || exit 1; done

# test-timing-budget ratchets per-package test wall time against
# testing-budget.json (kata b6rv): fail at 1.5x the budget, warn at 1.1x, plus
# a flat per-test ceiling, so an unexamined timing regression cannot silently
# erode the wins docs/superpowers/specs/2026-08-01-test-gate-runtime-design.md
# recorded. CHECK=1 enforces (strict in CI, warn-only on a local run); bare
# invocation only measures and prints. Companion to coverage-floor,
# same heavy + local posture.
test-timing-budget:
	@scripts/gate/test-timing-budget.sh $(if $(CHECK),--check) $(TIMING_ARGS)

# test-timing-budget-selftest exercises the comparison contract (ratio bands,
# the per-test ceiling, a missing budget entry, an absent/empty budget file,
# strict-vs-warn-only policy, and --bless) against fixture duration rows — no
# go test or vitest run.
test-timing-budget-selftest:
	@scripts/gate/test-timing-budget-selftest.sh

# test-rebaseline resets testing-budget.json to what a clean-host run just
# measured (kata b6rv). Run it deliberately, on an otherwise idle box, and
# review the diff in the same commit as whatever change earned it — this is
# NOT part of any gate, and nothing here should run it to invent a baseline;
# see docs/developing-evener/testing.md.
test-rebaseline:
	@scripts/gate/test-timing-budget.sh --bless $(TIMING_ARGS)
