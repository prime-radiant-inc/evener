.PHONY: test-web test-web-browser test-native test-native-bundle check-podfile-lock native-preflight api-package-preflight test-api-package test test-short test-race merge-approval-gate vet test-timing-budget test-rebaseline

# test-web is the frontend's single gate entry point: typecheck, unit tests,
# then lint. The three checks are independent readers of the same sources, so
# the gate (`evener-dev dev web-checks`, run from the prebuilt evener-dev binary so
# an interrupt reaches it) runs them concurrently with per-check private
# HOME/TMPDIR/XDG roots; wall time is the slowest one (vitest) instead of the
# sum. A failure replays exactly the failing check's log.
## The frontend's single gate entry point: typecheck, unit tests, then lint,
## run concurrently.
## proves: jsdom/unit-level frontend behavior, type safety, and source lint.
## trigger: Local pre-merge; required CI web job.
## requires: The Go toolchain (the gate is the prebuilt evener-dev) and the
##   installed Node dependencies; deterministic after those. Each check owns a
##   private process home plus temporary/XDG roots and disables Node's compile
##   cache; no real browser, provider, or network service.
## fails-when: Any of the three streams is nonzero; a missing or unhealthy
##   frontend install fails preflight.
test-web: web-preflight build-dev
	@scripts/web/test-web.sh

# test-web-browser runs the real browser-only frontend guards. They stay out
# of test-web because jsdom cannot evaluate the CSS cascade or browser geometry.
# The gate (`evener-dev dev web-browser-guards`, run from the prebuilt evener-dev
# binary so an interrupt reaches it) runs every guard so one missing browser or
# failing case does not hide the remaining guard's verdict; exit status is the
# first nonzero one.
## The real browser-only frontend guards (layoutguard, overflowguard,
## shellguard, spawnguard, transcriptscrollguard, sessioncacheguard,
## retirementguard, mermaidguard) plus the
##   full-stack `web-skillguard` (TestSkillComposerBrowser behind the
##   `browserguard` tag) that jsdom cannot evaluate.
## proves: Headless Chrome evaluates real CSS geometry, the real Session
##   reducer/tree, the real Spawn staging/breakpoint path, the real transcript
##   scroll/jump-to-latest path, session-cache deletion healing, clear-epoch
##   suppression and envelope filtering across two real tabs with native
##   BroadcastChannel and IndexedDB, and the real selected-thread recovery contract
##   when its daemon retires and is replaced; the skill guard additionally
##   drives the production composer through a REAL hub and two REAL
##   `evener serve` daemons with only the LLM provider scripted.
## trigger: Required CI web job; local pre-merge on a Chrome-capable host.
## requires: Chrome/Chromium and the Go toolchain (the gate is the prebuilt
##   evener-dev); each guard gets a private process home,
##   temporary/XDG roots, and a private browser profile. No WebKit/Safari
##   runner. retirementguard also needs the Go toolchain: its npm script runs
##   the isolated TestRetirementBrowser fixture, which starts the Hub and
##   drives the guard against it. The skill guard also needs the Go toolchain
##   and the built frontend, which this target builds first (`build-web`); the
##   gate still builds one when the dist is missing.
## fails-when: Any guard error, Vite failure, cleanup failure, or missing
##   Chrome/Chromium is nonzero.
test-web-browser: build-web build-dev
	@scripts/web/test-web-browser.sh

# check:scripts is separate from check because they answer different questions
# with different resolvers. `tsc --noEmit` reads tsconfig.check.json and proves
# the types line up; check:scripts asks Node's own resolver, through tsx, the
# only thing that reads tsconfig.json, whether the hand-run scripts/*.mts tools
# can still load their module graph. The scripts run under no gate, so nothing
# noticed when they stopped resolving.
## The native iPhone app and its shared session core gate.
## proves: Metro bundles the real iOS entry point, the native and
##   shared-session Vitest suites plus strict native TypeScript compilation
##   pass against the checked-in Expo/React Native sources, the
##   `mobile-native/src` and `mobile/src` sources match the native Biome
##   formatter config (`mobile-native/biome.jsonc`, the `npm run lint` step),
##   and the hand-run scripts/*.mts tools still resolve their module graph
##   under tsx.
## trigger: Native CI; local pre-merge when native or shared mobile sources change.
## requires: Node 22.13+ and an already-installed mobile-native dependency tree;
##   does not contact a hub or provider - script resolution is checked without
##   loading anything, since every one of those scripts opens a socket the
##   moment its body runs.
## fails-when: Bundling, native tests, shared-session tests, native
##   typechecking, formatting, or script module resolution fail.
test-native: test-native-bundle
	@cd mobile-native && NODE_DISABLE_COMPILE_CACHE=1 npm test && NODE_DISABLE_COMPILE_CACHE=1 npm run test:shared && NODE_DISABLE_COMPILE_CACHE=1 npm run check && NODE_DISABLE_COMPILE_CACHE=1 npm run lint && NODE_DISABLE_COMPILE_CACHE=1 npm run check:scripts

# native-preflight turns the misleading Metro failure a fresh worktree gets into
# a message naming the missing install and the command to run. A symlinked
# install is refused outright: the bundler resolves no module through one, so
# nothing else about it can make the native targets ready. The check and the
# cases it settles live in the script.
## Ensure the mobile-native dependency install is present, real, and
## lockfile-compatible before any native target runs.
## proves: mobile-native/node_modules exists as a real directory, matches
##   package-lock.json, and holds an executable .bin/expo, so Metro bundles
##   with the pinned Expo instead of whatever `npx` finds on PATH.
## trigger: Setup prerequisite for the native gates.
## requires: Node 22.13+; never installs, refusing instead with the command to
##   run.
## fails-when: node_modules is missing, is a symlink (the bundler resolves no
##   module through one, whatever the lockfiles say), is older than
##   package-lock.json, or has no executable .bin/expo; the message names
##   `cd mobile-native && npm ci`.
native-preflight:
	@scripts/native/native-preflight.sh

# The only gate that runs Metro. Vitest resolves through Vite and `tsc` through
# TypeScript's own resolver; neither reads metro.config.js, so a resolver
# regression there passes every other native check and fails first on a device.
# It runs ahead of the suites because a bundle that does not build is the
# cheaper failure to read.
## The native app's Metro bundling gate.
## proves: Metro resolves every specifier the real iOS entry point reaches —
##   the app's own sources, the shared mobile/ and frontend sources its
##   resolveRequest redirects, and the AppWire client wherever that package
##   lives — and the export writes an iOS bundle.
## trigger: Native CI (via make test-native); local pre-merge when native
##   sources or metro.config.js change.
## requires: Node 22.13+ and an already-installed mobile-native dependency
##   tree; no device, simulator, packager, hub, or provider. Runs with a
##   private process home plus temporary and XDG roots and passes --clear, so
##   the verdict never comes from a warm Metro cache. ~10s on a developer Mac,
##   bounded by `timeout 900` where coreutils provides it (the ubuntu runner,
##   or a Mac with gtimeout); without it the run is unbounded and the CI
##   step's timeout-minutes is the backstop.
## fails-when: Metro cannot resolve a module, the export fails, or the export
##   writes no iOS bundle.
test-native-bundle: native-preflight
	@scripts/native/test-native-bundle.sh

# `pod install --deployment` refuses a lock missing a pod the Podfile asks for,
# and only the TestFlight workflow runs it, on a tag: #3252 added a native
# dependency without its pod and every PR check stayed green (#3294). This
# compares the lock with what autolinking resolves, on any host.
## Check that mobile-native/Podfile.lock locks exactly the iOS pods
## autolinking resolves.
## proves: every pod the Expo and React Native autolinking the generated
##   Podfile runs would link, Expo's companion pods included, is in the lock's
##   DEPENDENCIES under the same name and directory, and every autolinked pod
##   the lock lists is still linked.
## trigger: Native CI; local pre-merge when mobile-native/package.json,
##   package-lock.json or Podfile.lock change.
## requires: Node 22.13+ and an already-installed, real (not symlinked)
##   mobile-native dependency tree; no macOS, CocoaPods, Xcode or generated
##   ios/ project. Expo's precompiled mode and extraPods are not handled.
## fails-when: an autolinked pod is not locked, or the lock lists an
##   autolinked pod nothing links (a version change inside an already locked
##   pod is not checked), or the script's own tests
##   (check-podfile-lock.test.mjs) fail.
check-podfile-lock: native-preflight
	@status=0; scripts/native/check-podfile-lock.mjs || status=1; node --test scripts/native/check-podfile-lock.test.mjs || status=1; exit $$status

# api-package-preflight turns the misleading failure a fresh checkout gets into
# a message naming the missing install and the command to run (or repairs a
# real, non-symlinked install), the way web-preflight does for the frontend. A
# symlinked shared install whose lockfile differs is refused rather than deleted.
## Ensure the appwire-client/typescript dependency install is present and
## healthy before the qualification runner starts.
## proves: node_modules carries the pinned tsc and the ws the qualification
##   runner imports, or is repaired with npm ci.
## trigger: Setup prerequisite for test-api-package.
## requires: Node 22+; never runs npm ci through a symlinked node_modules.
## fails-when: node_modules is missing and npm ci fails, is a mismatched
##   symlink, or lacks a working tsc / ws; on a real install the message names
##   `cd appwire-client/typescript && npm ci`, and on a symlinked one it names
##   the shared install instead.
api-package-preflight:
	@scripts/sdk/api-package-preflight.sh

## The independently consumable AppWire package qualification gate.
## proves: A packed package installs outside the checkout, exposes ESM and
##   CommonJS runtime/type entry points, and executes its shipped read-only
##   example against a scripted local WebSocket server.
## trigger: Package CI; local pre-merge when protocol sources change.
## requires: Node 22+ and the protocol package's installed development
##   dependencies (installed by api-package-preflight); qualification makes no
##   external network requests.
## fails-when: Build, pack, outside-checkout install, runtime import/require,
##   declaration checking, example protocol exchange or output validation fails.
test-api-package: api-package-preflight
	@cd appwire-client/typescript && NODE_DISABLE_COMPILE_CACHE=1 npm run qualification

# The module sets a scope selects, shared by `make test` (TEST_SCOPE) and
# `make test-race` (RACE_SCOPE). Every set derives from GO_MODULES, so a new
# module joins each scope it belongs to without an edit here.
SCOPE_MODULES_all := $(GO_MODULES)
SCOPE_MODULES_root := .
SCOPE_MODULES_nonroot := $(filter-out .,$(GO_MODULES))
SCOPE_MODULES_agent := $(filter agent,$(GO_MODULES))
SCOPE_MODULES_nonagent := $(filter-out . agent,$(GO_MODULES))
TEST_SCOPE ?= all

# test covers the Go modules AND the frontend. The frontend gate runs as a third
# concurrent stream inside run-module-tests.sh (MAKE is passed through so it can
# re-enter this Makefile's test-web target); it is node work, so it overlaps the
# Go waves instead of adding its runtime on the end. WEB=0 skips it.
# run-module-tests.sh's own contract — module selection, the explicit
# WAVE1/WAVE2 overrides, the private per-stream HOME and TMPDIR, caller flags
# reaching `go test`, failure propagation, cleanup, and the zero-test refusal —
# is pinned by gatemodulerunner_test.go, which drives the real script against
# tiny local modules with the installed toolchain. An earlier shell suite that
# faked `go` and `mktemp` on PATH was deleted, because
# docs/developing-evener/testing.md bans faking the toolchain in a test.
## The default local test gate: Go modules (short mode) plus the frontend,
## run concurrently.
## proves: Root short-mode tests, other module tests, and frontend
##   typecheck/Vitest/Biome all pass.
## trigger: Local quick check; included by the merge gate.
## requires: Scripted/fake external boundaries for default tests; runs ZERO
##   fuzz-family tests, even at reduced depth. WEB=0 skips the frontend
##   stream.
##   TEST_SCOPE picks the Go modules: all (default), root (the root module
##   alone) or nonroot (every other module); CI runs root and nonroot on
##   separate runners.
## fails-when: Any module, frontend stream, or setup failure is nonzero.
test:
	@case "$(TEST_SCOPE)" in all|root|nonroot) ;; *) echo "make test: TEST_SCOPE must be all, root, or nonroot (got $(TEST_SCOPE))" >&2; exit 2;; esac; \
		MODULES="$(strip $(SCOPE_MODULES_$(TEST_SCOPE)))" MAKE="$(MAKE)" scripts/gate/run-module-tests.sh -short -count=1

## Alias for `make test`.
test-short:
	@$(MAKE) test

# merge-approval-gate is the canonical serial post-merge gate. Keep the
# explicit expansion in docs/developing-evener/testing.md for diagnosis and evidence. Sandboxed
# hosts are handled inside the tests themselves: the live/e2e families probe
# their own capabilities and t.Skip (internal/e2ecap).
## The canonical serial post-merge gate: lint, build, full tests, and native/package qualification.
## proves: make lint, make build, ROOT_FULL=1 make test, make test-native and
##   make test-api-package all pass, in that order.
## trigger: Local pre-merge/post-merge; CI keeps equivalent checks in
##   separate named jobs.
## requires: Does not run fuzz search, race testing, provider calls, or
##   browser guards; those have separate owners.
## fails-when: The first failing phase stops the gate and returns nonzero;
##   do not infer a verdict from partial logs.
merge-approval-gate:
	@$(MAKE) lint && \
		$(MAKE) build && \
		ROOT_FULL=1 $(MAKE) test && \
		$(MAKE) test-native test-api-package

# The permanent -race gate (CI), across every non-fuzz module. AGENT_PARALLEL=6
# keeps the agent wave at the runner's measured cap: under -race (~10x slower),
# host-wide parallelism causes fork and scheduler contention that can starve real
# per-test work past the package timeout. WEB=0: -race is a Go-toolchain gate,
# and the frontend suite is unaffected by it, so `make test` owns the web stream
# instead of paying it twice.
RACE_SCOPE ?= all
RACE_ROOT_PART ?= all
## The permanent -race gate across every non-fuzz module.
## proves: Data races in the non-fuzz modules surface; frontend is
##   intentionally not duplicated.
## trigger: Required CI; local diagnostic.
## requires: A race-capable Go toolchain and more CPU/memory; WEB=0,
##   AGENT_SHARDS=0 and AGENT_PARALLEL=6 to cap test concurrency under -race's
##   ~10x slowdown, while cmd/evener-hub and cmd/evener stay sharded (12 hub
##   shards, no cost survey: under -race the survey costs as much as the run).
##   RACE_SCOPE defaults to all; CI uses the
##   explicit root scope plus agent and nonagent on separate runners. The two
##   new scopes derive from GO_MODULES; nonroot remains the local aggregate.
##   RACE_ROOT_PART (root scope only) splits the root module across runners:
##   all (default), hub (only cmd/evener-hub's shards), or rest (everything
##   else in the root module).
## fails-when: Any race report, test failure, or setup failure is nonzero.
test-race:
	@case "$(RACE_SCOPE)" in all|root|nonroot|agent|nonagent) ;; *) echo "make test-race: RACE_SCOPE must be all, root, nonroot, agent, or nonagent (got $(RACE_SCOPE))" >&2; exit 2;; esac; \
		case "$(RACE_ROOT_PART)" in all) hub=1; cli=1; rest=1;; hub) hub=1; cli=elsewhere; rest=0;; rest) hub=elsewhere; cli=1; rest=1;; *) echo "make test-race: RACE_ROOT_PART must be all, hub, or rest (got $(RACE_ROOT_PART))" >&2; exit 2;; esac; \
		test "$(RACE_ROOT_PART)" = all || test "$(RACE_SCOPE)" = root || { echo "make test-race: RACE_ROOT_PART=$(RACE_ROOT_PART) needs RACE_SCOPE=root" >&2; exit 2; }; \
		modules="$(strip $(SCOPE_MODULES_$(RACE_SCOPE)))"; \
		test -n "$$modules" || { echo "make test-race: RACE_SCOPE=$(RACE_SCOPE) selects no modules from GO_MODULES" >&2; exit 2; }; \
		MODULES="$$modules" WEB=0 AGENT_SHARDS=0 AGENT_PARALLEL=6 \
		HUB_SHARDS=$$hub CLI_SHARDS=$$cli ROOT_REST=$$rest HUB_SHARD_COUNT=12 HUB_SHARD_NO_SURVEY=1 CLI_SHARD_NO_SURVEY=1 \
		scripts/gate/run-module-tests.sh -race -short -count=1

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
##   per-test ceiling. While the budget file's metric marker is absent (the
##   checked-in pre-#172 baseline, until a rebaseline rewrites it under package
##   wall time), the Go-package ratio check is suspended to a warning; the
##   frontend ("web") row and the per-test ceiling keep their unchanged metrics
##   and stay enforced.
## trigger: Local/on-demand; not required CI — deliberately not part of make
##   merge-approval-gate, since measuring durations means a second full test
##   run. CHECK=1 enforces the ratios; bare invocation only measures and
##   prints them, except for a broken measurement, which is nonzero either way.
## requires: Deterministic; no provider calls. Reuses gate-surface-lib.sh, so
##   it measures the same surface ROOT_FULL=1 make test proves.
## fails-when: A broken measurement — go list or go test exiting nonzero, or a
##   go list package with no terminal event in the stream — is nonzero in every
##   mode, and --bless refuses it. A narrowed bless refreshes the packages it
##   measured and preserves the rest of the file instead of deleting entries it
##   did not measure; a full rebaseline also drops entries go list no longer
##   reports. Under CHECK=1 in a CI-shaped environment a Go package over 1.5x
##   its budget is nonzero once the budget file carries the wall-time metric
##   marker; while the marker is absent those ratios are warnings. The "web" row
##   and any per-test ceiling breach are metric-independent and nonzero
##   regardless, and a missing or empty budget file always exits zero.
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
