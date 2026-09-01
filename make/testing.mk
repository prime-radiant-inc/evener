.PHONY: test-web test-web-browser test test-short test-race merge-approval-gate vet test-timing-budget test-rebaseline

# test-web is the frontend's single gate entry point: typecheck, unit tests,
# then lint. The three checks are independent readers of the same sources, so
# the script runs them concurrently with per-check private HOME/TMPDIR/XDG
# roots; wall time is the slowest one (vitest) instead of the sum. A failure
# replays exactly the failing check's log.
## The frontend's single gate entry point: typecheck, unit tests, then lint,
## run concurrently.
## proves: jsdom/unit-level frontend behavior, type safety, and source lint.
## trigger: Local pre-merge; required CI web job.
## requires: Deterministic after Node dependencies are installed; each check
##   owns a private process home plus temporary/XDG roots and disables
##   Node's compile cache; no real browser, provider, or network service.
## fails-when: Any of the three streams is nonzero; a missing or unhealthy
##   frontend install fails preflight.
test-web: web-preflight
	@scripts/web/test-web.sh

# test-web-browser runs the real browser-only frontend guards. They stay out
# of test-web because jsdom cannot evaluate the CSS cascade or browser geometry.
# The script runs every guard so one missing browser or failing case does not
# hide the remaining guard's verdict; exit status is the first nonzero one.
## The real browser-only frontend guards (layoutguard, overflowguard,
## shellguard, spawnguard) that jsdom cannot evaluate.
## proves: Headless Chrome evaluates real CSS geometry, the real Session
##   reducer/tree, and the real Spawn staging/breakpoint path.
## trigger: Required CI web job; local pre-merge on a Chrome-capable host.
## requires: Chrome/Chromium; each guard gets a private process home,
##   temporary/XDG roots, and a private browser profile. No WebKit/Safari
##   runner.
## fails-when: Any guard error, Vite failure, cleanup failure, or missing
##   Chrome/Chromium is nonzero.
test-web-browser: web-preflight
	@scripts/web/test-web-browser.sh

# test covers the Go modules AND the frontend. The frontend gate runs as a third
# concurrent stream inside run-module-tests.sh (MAKE is passed through so it can
# re-enter this Makefile's test-web target); it is node work, so it overlaps the
# Go waves instead of adding its runtime on the end. WEB=0 skips it.
# run-module-tests.sh's own contract — including that every test stream gets a
# private HOME and TMPDIR — is currently unpinned: the shell suite that once
# proved it faked `go` and `mktemp` on PATH, which docs/developing-evener/testing.md's
# rule against faking the toolchain in a test bans outright, and was deleted.
# The port that would pin this contract honestly is tracked as issue #293.
## The default local test gate: Go modules (short mode) plus the frontend,
## run concurrently.
## proves: Root short-mode tests, other module tests, and frontend
##   typecheck/Vitest/Biome all pass.
## trigger: Local quick check; included by the merge gate.
## requires: Scripted/fake external boundaries for default tests; runs ZERO
##   fuzz-family tests, even at reduced depth. WEB=0 skips the frontend
##   stream.
## fails-when: Any module, frontend stream, or setup failure is nonzero.
test:
	@MODULES="$(GO_MODULES)" MAKE="$(MAKE)" scripts/gate/run-module-tests.sh -short -count=1

## Alias for `make test`.
test-short:
	@$(MAKE) test

# merge-approval-gate is the canonical serial post-merge gate. Keep the
# explicit expansion in docs/developing-evener/testing.md for diagnosis and evidence. Sandboxed
# hosts are handled inside the tests themselves: the live/e2e families probe
# their own capabilities and t.Skip (internal/e2ecap).
## The canonical serial post-merge gate: lint, build, then the full test
## suite.
## proves: make lint, make build, then ROOT_FULL=1 make test all pass, in
##   that order.
## trigger: Local pre-merge/post-merge; CI keeps equivalent checks in
##   separate named jobs.
## requires: Does not run fuzz search, race testing, provider calls, or
##   browser guards; those have separate owners.
## fails-when: The first failing phase stops the gate and returns nonzero;
##   do not infer a verdict from partial logs.
merge-approval-gate:
	@$(MAKE) lint && \
		$(MAKE) build && \
		ROOT_FULL=1 $(MAKE) test

# The permanent -race gate (CI), across every non-fuzz module. AGENT_PARALLEL=
# leaves the agent wave at GOMAXPROCS: under -race (~10x slower) extra
# parallelism just oversubscribes few-core CI and starves real per-test work past
# its timeouts. WEB=0: -race is a Go-toolchain gate, and the frontend suite is
# unaffected by it, so `make test` owns the web stream instead of paying it twice.
RACE_SCOPE ?= all
RACE_MODULES_all := $(GO_MODULES)
RACE_MODULES_root := .
RACE_MODULES_nonroot := $(filter-out .,$(GO_MODULES))
## The permanent -race gate across every non-fuzz module.
## proves: Data races in the non-fuzz modules surface; frontend is
##   intentionally not duplicated.
## trigger: Required CI; local diagnostic.
## requires: A race-capable Go toolchain and more CPU/memory; WEB=0,
##   AGENT_SHARDS=0, AGENT_PARALLEL= to avoid oversubscribing few-core CI
##   under -race's ~10x slowdown. RACE_SCOPE defaults to all; CI uses root
##   and nonroot on separate runners, both derived from GO_MODULES.
## fails-when: Any race report, test failure, or setup failure is nonzero.
test-race:
	@case "$(RACE_SCOPE)" in all|root|nonroot) ;; *) echo "make test-race: RACE_SCOPE must be all, root, or nonroot (got $(RACE_SCOPE))" >&2; exit 2;; esac; \
		MODULES="$(RACE_MODULES_$(RACE_SCOPE))" WEB=0 AGENT_SHARDS=0 AGENT_PARALLEL= scripts/gate/run-module-tests.sh -race -short -count=1

## go vet across every non-fuzz workspace module.
## proves: go vet diagnostics for every module, independent of the tagged
##   lint floors.
## trigger: Required CI; local diagnostic.
## requires: Deterministic Go analysis; no provider calls.
## fails-when: Any module's vet failure is nonzero.
vet:
	@for m in $(GO_MODULES); do (cd $$m && go vet ./...) || exit 1; done

# test-timing-budget ratchets per-package test wall time against
# testing-budget.json (kata b6rv): fail at 1.5x the budget, warn at 1.1x, plus
# a flat per-test ceiling, so an unexamined timing regression cannot silently
# erode the wins docs/superpowers/specs/2026-08-01-test-gate-runtime-design.md
# recorded. CHECK=1 enforces (strict in CI, warn-only on a local run); bare
# invocation only measures and prints. Companion to coverage-floor,
# same heavy + local posture.
## Ratchet per-package test wall time against testing-budget.json.
## proves: A timing regression does not silently erode the suite's runtime
##   wins — fail at 1.5x the checked-in budget, warn at 1.1x, plus a flat
##   per-test ceiling.
## trigger: Local/on-demand; not required CI — deliberately not part of make
##   merge-approval-gate, since measuring durations means a second full test
##   run. CHECK=1 enforces; bare invocation only measures and prints.
## requires: Deterministic; no provider calls. Reuses gate-surface-lib.sh, so
##   it measures the same surface ROOT_FULL=1 make test proves.
## fails-when: Under CHECK=1 in a CI-shaped environment, a package over 1.5x
##   its budget or any per-test ceiling breach is nonzero; a missing or
##   empty budget file always exits zero.
test-timing-budget:
	@scripts/gate/test-timing-budget.sh $(if $(CHECK),--check) $(TIMING_ARGS)

# test-rebaseline resets testing-budget.json to what a clean-host run just
# measured (kata b6rv). Run it deliberately, on an otherwise idle box, and
# review the diff in the same commit as whatever change earned it — this is
# NOT part of any gate, and nothing here should run it to invent a baseline;
# see docs/developing-evener/testing.md.
## Reset testing-budget.json to what a clean-host run just measured. Run
## deliberately, on an otherwise idle box, and review the diff in the same
## commit as whatever change earned it — never to invent a baseline. Not
## part of any gate.
test-rebaseline:
	@scripts/gate/test-timing-budget.sh --bless $(TIMING_ARGS)
