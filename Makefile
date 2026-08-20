.PHONY: build build-runtime build-go build-hub web-preflight build-web test-web test-web-browser build-tui build-doctor build-all build-linux build-llmcall build-migrate dist install install-home install-system test-install tools test-dev-tooling test test-short test-fuzz test-race merge-approval-gate e2e-cover vet lint lint-naming lint-evenerfuzz lint-eval lint-internal lint-golangci lint-generated lint-fuzz-registry generate mutation-floor clean fuzz fuzz-seeds fuzz-nightly fuzz-triage fuzz-continuous fuzz-bisect fuzz-bisect-selftest fuzz-oracle-audit fuzz-oracle-audit-selftest fuzz-mutation-score fuzz-ledger fuzz-gap-check fuzz-registry-check fuzz-goldens secret-scan fuzz-corpus-scan refresh-model-catalog coverage-floor coverage-floor-selftest coverage-gaps coverage-gaps-selftest merge-into-branch merge-into-branch-selftest test-timing-budget test-timing-budget-selftest test-rebaseline

LDFLAGS := -X primeradiant.com/evener/buildinfo.GitSHA=$$(git rev-parse --short HEAD) \
           -X primeradiant.com/evener/buildinfo.GitDirty=$$(git --no-optional-locks diff-files --quiet && echo "" || echo "true") \
           -X primeradiant.com/evener/buildinfo.BuildTime=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
           -X primeradiant.com/evener/buildinfo.Channel=$(BUILD_CHANNEL)

PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
EVENER_SHARE_BINDIR ?= $(PREFIX)/share/evener/bin
INSTALL_BUILD_DIR ?= .build/install
EVENER_INSTALL_BINS := evener evener-hub evener-tui evener-doctor evener-migrate
BUILD_CHANNEL ?=

build: build-runtime

# build-runtime depends on build-web (not the reverse): make guarantees a
# target's prerequisites COMPLETE before its recipe runs — even under
# parallel make (-j) — so hanging build-web off build-runtime structurally
# guarantees every evener/evener-hub pair build embeds the dist build-web just
# produced. No target may ship a evener-hub binary with a stale or empty
# embedded web UI.
build-runtime: build-web
	LDFLAGS="$(LDFLAGS)" scripts/ops/build-runtime-pair.sh

# Cross-compile for Linux (eval deployments). Invalidates the agent package
# cache to ensure embedded files (templates, sections, agent .md) are fresh.
build-linux:
	go clean -cache 2>/dev/null && \
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o evener-linux-amd64 ./cmd/evener/

build-hub: build-runtime

# web-preflight owns the frontend node_modules install for every web target,
# so build-web and test-web share one definition of "the install is ready".
# The install rules and the two guards they exist for live in the script;
# scripts/web/web-preflight-selftest.sh exercises them against throwaway trees.
web-preflight:
	@NODE_DISABLE_COMPILE_CACHE=1 scripts/web/web-preflight.sh

# build-web builds the frontend TypeScript/React app (cmd/evener-hub/frontend)
# into frontend/dist, which build-hub embeds via go:embed. The vite build
# itself stays unconditional — dist freshness is the entire point, the
# install step is the only cacheable part. vite's emptyOutDir wipes the
# tracked dist/PLACEHOLDER on every build; vite.config.ts writes it back at
# closeBundle, so no recipe here restores it and `git status` stays clean
# after a build however vite was invoked (kata 88nn).
build-web: web-preflight
	cd cmd/evener-hub/frontend && NODE_DISABLE_COMPILE_CACHE=1 npm run build

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

build-tui:
	go build -o evener-tui ./cmd/evener-tui/

# evener-doctor: the read-only forensic inspector (data plane of the doctoring system).
build-doctor:
	go build -o evener-doctor ./cmd/evener-doctor/

build-all: build-runtime build-tui build-doctor build-migrate

build-llmcall:
	go build -o llmcall ./cmd/llmcall/

build-migrate:
	go build -o evener-migrate ./cmd/evener-migrate/

# dist builds the release artifacts with goreleaser in snapshot mode (no tag,
# no publish): dist/ holds evener_<os>_<arch>.tar.gz plus checksums.txt — the
# same layout the release workflow ships and install.sh consumes. The web
# build runs as goreleaser's before hook, so the hub binary embeds a fresh
# SPA rather than the tracked PLACEHOLDER.
dist:
	goreleaser release --snapshot --clean

# An installed hub must embed a fresh SPA, not the tracked PLACEHOLDER (install-home/install-system inherit via install).
install: build-web
	install -d "$(INSTALL_BUILD_DIR)"
	go build -ldflags "$(LDFLAGS)" -o "$(INSTALL_BUILD_DIR)/evener" ./cmd/evener/
	go build -o "$(INSTALL_BUILD_DIR)/evener-hub" ./cmd/evener-hub/
	go build -o "$(INSTALL_BUILD_DIR)/evener-tui" ./cmd/evener-tui/
	go build -o "$(INSTALL_BUILD_DIR)/evener-doctor" ./cmd/evener-doctor/
	go build -o "$(INSTALL_BUILD_DIR)/evener-migrate" ./cmd/evener-migrate/
	install -d "$(EVENER_SHARE_BINDIR)" "$(BINDIR)"
	@for bin in $(EVENER_INSTALL_BINS); do \
		install -m 0755 "$(INSTALL_BUILD_DIR)/$$bin" "$(EVENER_SHARE_BINDIR)/$$bin"; \
		ln -sfn "$(EVENER_SHARE_BINDIR)/$$bin" "$(BINDIR)/$$bin"; \
	done

install-home: PREFIX := $(HOME)/.local
install-home: install

install-system: PREFIX := /usr/local
install-system: install

# tools installs the CI-pinned lint/scanner versions from .tool-versions, so
# a local `make lint` runs exactly what CI runs.
tools:
	@set -eu; \
	golangci=$$(awk '$$1=="golangci-lint" {print $$2}' .tool-versions); \
	gitleaks=$$(awk '$$1=="gitleaks" {print $$2}' .tool-versions); \
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b "$$(go env GOPATH)/bin" "v$$golangci"; \
	go install github.com/zricethezav/gitleaks/v8@v$$gitleaks

test-install:
	go test -count=1 -run '^TestInstallHomeGeneratedHome$$' .

# Every non-fuzz Go module in the workspace. Under go.work, `./...` resolves
# per-module, so gates and lint must loop modules explicitly. Fuzz targets and
# the fuzz toolkit module run through the explicit fuzz targets below, not the
# regular test gate.
GO_MODULES := . agent llm auth envvars invariant identifier
FUZZ_GO_MODULES := $(GO_MODULES) fuzz

# build-go compiles every non-fuzz Go workspace module. Keep it separate from
# build: the runtime pair owns the embedded frontend, while this target makes
# the workspace-wide compile contract explicit for CI and local diagnostics.
build-go:
	@for m in $(GO_MODULES); do (cd $$m && go build ./...) || exit 1; done

# Fuzz replay is a deterministic evidence gate: never inherit a developer's
# persisted Go configuration or GOFLAGS, and always use this checkout's workspace.
override FUZZ_GOWORK := $(abspath $(CURDIR)/go.work)

# DEV_TOOLING_TEST_SCRIPTS are the scripts/<name>-selftest.sh suites that pin
# the behaviour of evener's own tooling. Each is offline, deterministic and works
# only in throwaway fixtures, and each is the ONLY thing that pins its script's
# contract. A suite earns its slot by pinning outcomes of a tool the gate or CI
# depends on; hand-run conveniences fail loudly in front of whoever ran them
# and get no suite (docs/testing.md). scratch-lib tests the shared scratch
# guard directly, once — that every script's scratch stays inside TMPDIR and
# none of its recursive deletes takes a clobberable argument, whether it uses
# the guard or the pid-suffixed covscratch pattern, is enforced statically by
# the audits in scriptmktemp_audit_test.go, not by re-running suites under
# sabotage (kata 5hs2).
DEV_TOOLING_TEST_SCRIPTS := gate/run-module-tests lib/private-go-home gate/merge-approval-gate ops/setup-gocache web/web-preflight lib/live-eval-isolation e2e/e2e-webui-turn-controls fuzz/fuzz-bisect fuzz/fuzz-oracle-audit coverage/coverage-floor coverage/coverage-gaps gate/test-timing-budget lib/scratch-lib gate/merge-into-branch

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

# test-fuzz runs the seqfuzz/schemafuzz stateful rapid.Check family (delegate,
# watch, lifecycle, jobs descendant-merge, tool-args schema, jobstore, context
# compaction x2, appserver router, appserver multi-session) at FULL depth:
# EVENER_FUZZ_TESTS=1 opts each t.Skip()-gated test back in, and the absence of
# -short lets rapid.Check run its full default check count instead of the
# reduced one it uses under -short. These tests never run inside `make test`
# (Jesse ruling: no fuzz-family test, including smoke iterations, belongs in
# the default suite). This is distinct from `make fuzz`, which replays native
# FuzzXxx corpora and this SAME rapid family against a fixed coverage seed
# bank (scripts/fuzz/run-fuzz.sh) for deterministic CI coverage, not a search.
test-fuzz:
	@cd agent && EVENER_FUZZ_TESTS=1 go test . ./internal/contextmgr ./internal/jobstore -run 'SeqFuzz|ToolArgsSchemaFuzz' -count=1 -v
	@EVENER_FUZZ_TESTS=1 go test ./internal/appserver -run 'SeqFuzz' -count=1 -v

# merge-approval-gate is the canonical serial post-merge gate. Keep the
# explicit expansion in docs/testing.md for diagnosis and evidence.
#
# The capability preflight (evener-dev capability-preflight) classifies
# sandbox-sensitive host capabilities ONCE, before any phase runs, and
# exports EVENER_GATE_CAPABILITY_SKIP so ROOT_FULL=1 make test skips exactly
# the known-infeasible live/e2e tests instead of repeatedly failing into
# them, and reports what it found blocked with exact rerun commands. All four
# steps run in ONE shell (chained with && instead of four separate
# `@$(MAKE)` lines) so that export reaches every phase; the gate still stops
# at the first failing phase either way.
#
# The assignment and the export are SEPARATE statements, and the assignment's
# own exit status is what the `if` tests. `export VAR="$$(preflight)"` would
# fail open (issue #181): export succeeds whatever the substitution did, so a
# preflight that never reached a verdict would run every phase with an empty or
# inherited skip pattern and call the result a pass.
merge-approval-gate:
	@if ! EVENER_GATE_CAPABILITY_SKIP="$$(go run ./cmd/evener-dev capability-preflight)"; then \
		echo 'merge-approval-gate: capability preflight failed before emitting its verdict: go run ./cmd/evener-dev capability-preflight' >&2; \
		exit 1; \
	fi; \
	export EVENER_GATE_CAPABILITY_SKIP; \
		$(MAKE) lint && \
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

# e2e-cover measures END-TO-END coverage of the real evener/evener-tui binaries via
# `go build -cover` + GOCOVERDIR — the main()/CLI/dispatch/serve paths unit tests
# structurally can't reach. --merge-unit unions it with the unit profile for a
# combined whole-repo number; EVENER_E2E_LIVE=1 additionally runs the live provider
# scripts (needs real credentials). Local/on-demand, not a gate.
e2e-cover:
	@scripts/coverage/e2e-cover.sh --merge-unit

vet:
	@for m in $(GO_MODULES); do (cd $$m && go vet ./...) || exit 1; done

# FUZZ_SEED_REPLAY replays every module's COMMITTED seed corpus (and saved
# crashers) under the evenerfuzz tag. `go test -run '^Fuzz'` with no `-fuzz` does
# not search, so this is deterministic. `make fuzz` runs it as one of its steps;
# fuzz-seeds is the same work on its own.
FUZZ_SEED_REPLAY = for m in $(FUZZ_GO_MODULES); do (GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c "cd $$m && go test -run '^Fuzz' -tags evenerfuzz -count=1 ./...") || exit 1; done

# fuzz-seeds is the RUNTIME half of the tagged-source gate whose compile half is
# `make lint-evenerfuzz`. It stays out of `make test` on measured cost: 144s
# across the workspace (the root module ~39s and agent ~65s of that) against a
# ~70s `make test`, for a class `make fuzz` and CI already replay. lint-evenerfuzz
# catches a tagged call site stranded by a signature change in ~4s; this catches
# the rest — a tagged seed that still compiles but no longer passes.
fuzz-seeds:
	@$(FUZZ_SEED_REPLAY)

# fuzz runs every native FuzzXxx target's seed corpus plus saved crashers as
# ordinary deterministic tests — `go test -run '^Fuzz'` with no `-fuzz` does not
# random-search. It also replays every registered Rapid property surface with
# the fixed coverage seed bank. It is still fuzz coverage, so it stays out of
# the regular test gate and runs through this explicit entry point.
#
# Everything here builds with -tags evenerfuzz so the internal/invariant assertions
# (primeradiant.com/evener/invariant) are live: a tripped invariant panics and the
# never-panic oracle catches it. The first step verifies the mechanism itself
# fires under the tag; production builds and `make test` stay tag-free.
fuzz:
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c 'cd agent && go test -run "^$$" -tags evenerfuzz -count=1 ./...'
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c 'cd invariant && go test -tags evenerfuzz ./...'
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c 'cd fuzz && go test -tags evenerfuzz ./...'
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c 'go test ./cmd/evener-fuzzcov ./cmd/evener-fuzz-harvest'
	@$(FUZZ_SEED_REPLAY)
	@scripts/fuzz/rapid-replay.sh
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c "go test -run '^Test.*Golden\$$' ./appwire"

# fuzz-goldens regenerates the decode SNAPSHOT goldens — evener's differential
# oracle. Each decode target's committed seed corpus is replayed and its decoded
# output canonically re-encoded into appwire/testdata/golden/. A code change that
# silently alters a decoder's output (no panic, round-trip still holds) fails the
# `make fuzz` golden check; run this ONLY after an INTENDED decoder change, then
# commit the diff. See docs/fuzzing.md ("Choosing an oracle").
fuzz-goldens:
	@sh -c "go test -run '^Test.*Golden\$$' ./appwire -update-goldens"
	@sh -c "cd llm && go test -run '^Test.*Golden\$$' ./providers/difftest -update-goldens"

# fuzz-nightly runs the unbounded coverage-guided search per target, bounded by a
# per-target time budget. Manual / nightly only — never in the gate.
fuzz-nightly:
	@scripts/fuzz/run-fuzz.sh $(FUZZ_ARGS)

# fuzz-triage is the local, on-demand campaign + auto-triage tool (8.7): it
# searches each surface, flake-guards and dedups any crasher, and opens ONE
# reviewable PR per distinct deterministic bug via the developer's local `gh`.
# Pass flags through FUZZ_ARGS, e.g. `make fuzz-triage FUZZ_ARGS="--time 5m"`,
# `make fuzz-triage FUZZ_ARGS=--no-pr`, or `make fuzz-triage FUZZ_ARGS=--dry-run`.
fuzz-triage:
	@scripts/fuzz/fuzz-triage.sh $(FUZZ_ARGS)

# fuzz-continuous is the LOCAL, on-demand continuous loop: it rotates over every
# native target, giving each a bounded search turn round after round (the corpus
# deepens across turns via $GOCACHE/fuzz), and routes any new crasher through
# fuzz-triage's flake-guard / dedup / PR pipeline. Runs until Ctrl-C or --total.
# Pass flags through FUZZ_ARGS, e.g. `make fuzz-continuous FUZZ_ARGS="--total 2h"`.
fuzz-continuous:
	@scripts/fuzz/fuzz-continuous.sh $(FUZZ_ARGS)

# fuzz-drive generates REAL provider traffic (varied coding tasks through the
# evener one-shot CLI, recorders on) and harvests it into the seed corpus. Makes
# live, paid provider calls — run on demand, not in CI. Flags via FUZZ_ARGS, e.g.
# `make fuzz-drive FUZZ_ARGS="--providers openai/gpt-5.4-mini --runs 5"`.
fuzz-drive:
	@scripts/fuzz/fuzz-drive.sh $(FUZZ_ARGS)

# coverage-floor is the repo's one coverage ratchet: per module, the union of
# the test track and the deterministic fuzz-replay track, plus the frontend's
# vitest line coverage, against scripts/coverage/coverage-floors.txt. Bare
# invocation reports; CHECK=1 fails on a drop; BLESS=1 raises floors. Heavy +
# local. Its contract is pinned by scripts/coverage/coverage-floor-selftest.sh
# in the dev-tooling wave.
coverage-floor:
	@scripts/coverage/coverage-floor.sh $(if $(CHECK),--check) $(if $(BLESS),--bless) $(COV_ARGS)

coverage-floor-selftest:
	@scripts/coverage/coverage-floor-selftest.sh

# coverage-gaps ranks where a coverage profile's UNCOVERED statements are, by
# count rather than percentage, so coverage work targets the largest real gaps.
# Takes a profile: `make coverage-gaps PROFILE=path/to.cov GAP_ARGS="--by file"`.
coverage-gaps:
	@scripts/coverage/coverage-gaps.sh $(PROFILE) $(GAP_ARGS)

coverage-gaps-selftest:
	@scripts/coverage/coverage-gaps-selftest.sh

# merge-into-branch merges SOURCE into TARGET by ref (refs/heads/TARGET),
# never through a live checkout: it builds the merge in a private disposable
# worktree and lands it with a `git update-ref` compare-and-swap, so a
# concurrent branch switch on a shared checkout cannot land the merge on the
# wrong branch (kata h2tb). `make merge-into-branch TARGET=main SOURCE=feature
# MERGE_ARGS="--no-ff"`; see scripts/gate/merge-into-branch.sh --help for flags.
merge-into-branch:
	@scripts/gate/merge-into-branch.sh $(MERGE_ARGS) $(TARGET) $(SOURCE)

merge-into-branch-selftest:
	@scripts/gate/merge-into-branch-selftest.sh

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
# see docs/testing.md.
test-rebaseline:
	@scripts/gate/test-timing-budget.sh --bless $(TIMING_ARGS)

# mutation-floor gates the gremlins kill score: MIN=95 fails any curated package
# whose test efficacy drops below 95%. Slow (nightly). No MIN = report only.
mutation-floor:
	@scripts/fuzz/fuzz-mutation-score.sh $(if $(MIN),--min-efficacy $(MIN)) $(MUT_ARGS)

# fuzz-bisect pinpoints the commit that introduced a crasher via git bisect,
# replaying one saved corpus entry per step. Supply args through FUZZ_ARGS, e.g.
# `make fuzz-bisect FUZZ_ARGS="--target llm:FuzzParseSSE --crasher <file> --good <ref>"`.
fuzz-bisect:
	@scripts/fuzz/fuzz-bisect.sh $(FUZZ_ARGS)

# fuzz-bisect-selftest verifies bisection end-to-end against a throwaway git repo
# whose fuzz target crashes only after a known commit (real git bisect + replay).
fuzz-bisect-selftest:
	@scripts/fuzz/fuzz-bisect-selftest.sh

# fuzz-oracle-audit proves every fuzz oracle reddens on its bug class (Phase 9 W1):
# each mutation in fuzz/mutations/ reintroduces a known fault in a throwaway
# worktree and the audit asserts the target FAILS. `FUZZ_ARGS=--gap-only` lists
# native targets that have no mutation yet; pass ids to audit only those.
fuzz-oracle-audit:
	@scripts/fuzz/fuzz-oracle-audit.sh $(FUZZ_ARGS)

# fuzz-oracle-audit-selftest verifies the audit's caught/blind/rot/build-failure
# classification against a throwaway module (real worktree + go test, stubbed
# registry).
fuzz-oracle-audit-selftest:
	@scripts/fuzz/fuzz-oracle-audit-selftest.sh

# fuzz-mutation-score (Phase 10 W5) measures detection sufficiency with gremlins:
# the per-package kill rate, and the surviving (LIVED) mutants are the weak-oracle
# worklist. Nightly/manual (slow); needs gremlins installed. Pass FUZZ_ARGS to
# score specific packages, e.g. `make fuzz-mutation-score FUZZ_ARGS="llm:./providercfg"`.
fuzz-mutation-score:
	@scripts/fuzz/fuzz-mutation-score.sh $(FUZZ_ARGS)

# fuzz-ledger pretty-prints the triage ledger (found/fixed/quarantined counts and
# the open-bug list) from fuzz/state/ledger.json.
fuzz-ledger:
	@jq -r 'to_entries | "found:     \([.[]|select(.value.status=="found")]|length)\nfixed:     \([.[]|select(.value.status=="fixed")]|length)\nquarantined: \([.[]|select(.value.status=="quarantined")]|length)"' fuzz/state/ledger.json
	@echo "--- open bugs (found) ---"
	@jq -r 'to_entries[] | select(.value.status=="found") | "  \(.key)\t\(.value.pr // "(no PR)")"' fuzz/state/ledger.json

# fuzz-gap-check is the FAST, STATIC gap gate (the blocking CI floor): it asserts
# every decode/parse package has a registered fuzz target (or a reasoned ignore),
# derived from scripts/fuzz/run-fuzz.sh --list without replaying any corpus. Seconds,
# deterministic.
fuzz-gap-check:
	@scripts/fuzz/fuzz-gap-check.sh

# fuzz-registry-check compares native and explicitly marked rapid targets in the
# authoritative manifest with AST-discovered workspace declarations. It does not
# run ordinary tests, a fuzz search, or any network activity.
fuzz-registry-check:
	@scripts/fuzz/fuzz-registry-check.sh

# refresh-model-catalog replaces the vendored LiteLLM model-catalog snapshot
# with the current upstream and runs the catalog sanity tests. The vendored
# file must never be hand-edited (evener-curated data lives in
# evener_model_catalog_overrides.json); use `--check` via the script directly
# for a dry-run delta report.
refresh-model-catalog:
	@scripts/ops/refresh-model-catalog.sh

# secret-scan runs gitleaks over the whole working tree using the committed
# .gitleaks.toml ruleset. Part of the gate (`make lint`); skips with a warning
# when gitleaks is not installed (required in CI).
secret-scan:
	$(call run_quiet_lint,scripts/ops/gitleaks-scan.sh repo,preserve-gitleaks-warning)

# fuzz-corpus-scan runs gitleaks over only the fuzz seed corpora — the
# corpus-scoped subset of secret-scan, for fast harvester feedback.
fuzz-corpus-scan:
	@scripts/ops/gitleaks-scan.sh corpus

# lint-naming enforces TOML=snake_case across every TOML data file in the
# repo. Go struct tags are tagliatelle's job (.golangci.yml): JSON and TOML
# tags are snake_case everywhere but the camelCase wire-protocol packages.
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

lint-naming:
	$(call run_quiet_lint,go run ./cmd/evener-tomlcheck)

# lint-evenerfuzz is the compile floor for the //go:build evenerfuzz sources. Every
# other gate is tag-free — `make test`, `make lint`, `make vet` and
# `go build ./...` never compile those 250 files — so a production signature
# change that strands a tagged call site rots there until someone runs
# `make fuzz` by hand. `go vet` type-checks each module's test packages under
# the tag, which is what catches a stranded call site. Running the corpora
# themselves is 144s and stays in `make fuzz` / `make fuzz-seeds`; this pass is
# ~4s warm across the workspace, which is why it can sit in the gate.
lint-evenerfuzz:
	$(call run_quiet_lint,for m in $(FUZZ_GO_MODULES); do (cd $$m && go vet -tags evenerfuzz ./...) || exit 1; done)

# lint-eval is the same compile floor for the //go:build eval sources: the
# live-provider eval suites (context-compaction quality, forced notes). This tag
# never had a floor at all and duly rotted — a []string that became a
# []summarizationRoute in June stranded the comparison eval's judge, and nothing
# said so for six weeks. RUNNING these is not gate-shaped at any price: they
# spend real money against a real provider, so compilation is the whole gate.
# FUZZ_GO_MODULES is the full workspace list, and the floor wants all of it: eval
# sources sit only under agent/ today, and a floor is worth nothing in the module
# where the next one lands. ~3.5s warm, the same order as the evenerfuzz pass.
lint-eval:
	$(call run_quiet_lint,for m in $(FUZZ_GO_MODULES); do (cd $$m && go vet -tags eval ./...) || exit 1; done)

# lint-internal fails if any exported symbol in the agent/llm/providercfg
# libraries names a evener-internal type — keeping them externally importable.
lint-internal:
	$(call run_quiet_lint,go run ./cmd/evener-internalcheck)

# golangci-lint across every module (./... is per-module under go.work).
# The runner lives in Go (cmd/evener-dev); MODULES and LINT_PARALLEL keep the
# interface run-module-lint.sh shipped with.
lint-golangci:
	@MODULES="$(GO_MODULES)" go run ./cmd/evener-dev module-lint

# generate runs all `go generate` directives. The AppWire protocol reference
# and frontend TypeScript declarations come from the catalog in appwire/protocol.go.
generate:
	go generate ./appwire/...

# lint-generated fails if either committed AppWire output is stale — i.e. the
# catalog changed without regenerating the protocol doc and TypeScript types.
lint-generated:
	$(call run_quiet_lint,go generate ./appwire/... && { git diff --exit-code -- docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts || { echo "generated AppWire outputs are stale; run 'make generate' and commit."; exit 1; }; })

LINT_TARGETS := lint-naming lint-evenerfuzz lint-eval lint-internal lint-golangci lint-generated lint-fuzz-registry secret-scan

lint: $(LINT_TARGETS)

clean:
	rm -f evener evener-hub evener-tui evener-doctor llmcall evener-migrate evener-linux-amd64
