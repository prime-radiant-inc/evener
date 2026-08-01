.PHONY: build build-runtime build-hub web-preflight build-web test-web build-tui build-doctor build-all build-linux build-namingcheck dist install install-home install-system test-install test test-short test-race vet lint lint-naming lint-serffuzz lint-internal lint-docs lint-golangci clean fuzz fuzz-seeds fuzz-nightly fuzz-triage fuzz-triage-selftest fuzz-continuous fuzz-continuous-selftest fuzz-drive fuzz-drive-selftest fuzz-coverage-global fuzz-coverage-global-selftest fuzz-bisect fuzz-bisect-selftest fuzz-oracle-audit fuzz-oracle-audit-selftest fuzz-mutation-score fuzz-ledger fuzz-coverage fuzz-gap-check fuzz-registry-check fuzz-goldens secret-scan fuzz-corpus-scan refresh-model-catalog

LDFLAGS := -X primeradiant.com/serf/buildinfo.GitSHA=$$(git rev-parse --short HEAD) \
           -X primeradiant.com/serf/buildinfo.GitDirty=$$(git diff --quiet && echo "" || echo "true") \
           -X primeradiant.com/serf/buildinfo.BuildTime=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
           -X primeradiant.com/serf/buildinfo.Channel=$(BUILD_CHANNEL)

PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
SERF_SHARE_BINDIR ?= $(PREFIX)/share/serf/bin
INSTALL_BUILD_DIR ?= .build/install
SERF_INSTALL_BINS := serf serf-hub serf-tui serf-doctor
DIST_DIR ?= dist
DIST_GOOS ?= $(shell go env GOOS)
DIST_GOARCH ?= $(shell go env GOARCH)
BUILD_CHANNEL ?=
SERF_DIST_NAME := serf_$(DIST_GOOS)_$(DIST_GOARCH)
SERF_DIST_BIN_DIR := $(DIST_DIR)/$(SERF_DIST_NAME)
SERF_DIST_ARCHIVE := $(DIST_DIR)/$(SERF_DIST_NAME).tar.gz

build: build-runtime

# build-runtime depends on build-web (not the reverse): make guarantees a
# target's prerequisites COMPLETE before its recipe runs — even under
# parallel make (-j) — so hanging build-web off build-runtime structurally
# guarantees every serf/serf-hub pair build embeds the dist build-web just
# produced. No target may ship a serf-hub binary with a stale or empty
# embedded web UI.
build-runtime: build-web
	LDFLAGS="$(LDFLAGS)" scripts/build-runtime-pair.sh

# Cross-compile for Linux (eval deployments). Invalidates the agent package
# cache to ensure embedded files (templates, sections, agent .md) are fresh.
build-linux:
	go clean -cache -x ./agent/ 2>/dev/null; \
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o serf-linux-amd64 ./cmd/serf/

build-hub: build-runtime

# web-preflight owns the frontend node_modules install for every web target,
# so build-web and test-web share one definition of "the install is ready".
# The install rules and the two guards they exist for live in the script;
# scripts/web-preflight-selftest.sh exercises them against throwaway trees.
web-preflight:
	@scripts/web-preflight.sh

# build-web builds the frontend TypeScript/React app (cmd/serf-hub/frontend)
# into frontend/dist, which build-hub embeds via go:embed. The vite build
# itself stays unconditional — dist freshness is the entire point, the
# install step is the only cacheable part. vite's emptyOutDir wipes the
# tracked dist/PLACEHOLDER on every build; restore it from git so
# `git status` stays clean after a build.
build-web: web-preflight
	cd cmd/serf-hub/frontend && npm run build

# test-web is the frontend's single gate entry point: typecheck, unit tests,
# then lint (mirrors the Go test+lint split, but the frontend toolchain
# doesn't need separate targets per check).
test-web: web-preflight
	cd cmd/serf-hub/frontend && npm run typecheck && npm run test && npm run lint

build-tui:
	go build -o serf-tui ./cmd/serf-tui/

# serf-doctor: the read-only forensic inspector (data plane of the doctoring system).
build-doctor:
	go build -o serf-doctor ./cmd/serf-doctor/

build-all: build-runtime build-tui build-doctor

build-llmcall:
	go build -o llmcall ./cmd/llmcall/

# A shipped dist archive must embed a fresh SPA, not the tracked PLACEHOLDER.
dist: build-web
	rm -rf "$(SERF_DIST_BIN_DIR)" "$(SERF_DIST_ARCHIVE)"
	install -d "$(SERF_DIST_BIN_DIR)"
	CGO_ENABLED=0 GOOS=$(DIST_GOOS) GOARCH=$(DIST_GOARCH) go build -ldflags "$(LDFLAGS)" -o "$(SERF_DIST_BIN_DIR)/serf" ./cmd/serf/
	CGO_ENABLED=0 GOOS=$(DIST_GOOS) GOARCH=$(DIST_GOARCH) go build -o "$(SERF_DIST_BIN_DIR)/serf-hub" ./cmd/serf-hub/
	CGO_ENABLED=0 GOOS=$(DIST_GOOS) GOARCH=$(DIST_GOARCH) go build -o "$(SERF_DIST_BIN_DIR)/serf-tui" ./cmd/serf-tui/
	CGO_ENABLED=0 GOOS=$(DIST_GOOS) GOARCH=$(DIST_GOARCH) go build -o "$(SERF_DIST_BIN_DIR)/serf-doctor" ./cmd/serf-doctor/
	tar -C "$(DIST_DIR)" -czf "$(SERF_DIST_ARCHIVE)" "$(SERF_DIST_NAME)"

# An installed hub must embed a fresh SPA, not the tracked PLACEHOLDER (install-home/install-system inherit via install).
install: build-web
	install -d "$(INSTALL_BUILD_DIR)"
	go build -ldflags "$(LDFLAGS)" -o "$(INSTALL_BUILD_DIR)/serf" ./cmd/serf/
	go build -o "$(INSTALL_BUILD_DIR)/serf-hub" ./cmd/serf-hub/
	go build -o "$(INSTALL_BUILD_DIR)/serf-tui" ./cmd/serf-tui/
	go build -o "$(INSTALL_BUILD_DIR)/serf-doctor" ./cmd/serf-doctor/
	install -d "$(SERF_SHARE_BINDIR)" "$(BINDIR)"
	@for bin in $(SERF_INSTALL_BINS); do \
		install -m 0755 "$(INSTALL_BUILD_DIR)/$$bin" "$(SERF_SHARE_BINDIR)/$$bin"; \
		ln -sfn "$(SERF_SHARE_BINDIR)/$$bin" "$(BINDIR)/$$bin"; \
	done

install-home: PREFIX := $(HOME)/.local
install-home: install

install-system: PREFIX := /usr/local
install-system: install

test-install:
	go test -count=1 -run '^TestInstallHomeGeneratedHome$$' .

# Every non-fuzz Go module in the workspace. Under go.work, `./...` resolves
# per-module, so gates and lint must loop modules explicitly. Fuzz targets and
# the fuzz toolkit module run through the explicit fuzz targets below, not the
# regular test gate.
GO_MODULES := . agent llm auth envvars invariant identifier
FUZZ_GO_MODULES := $(GO_MODULES) fuzz

# MEMCAP runs a recipe under a hard per-run memory ceiling (a systemd user scope)
# so a leaky test or fuzz run is OOM-killed individually instead of firing the
# kernel's global OOM killer and taking the whole host — and its network — down.
# Tune or disable via SERF_MEM_MAX (default 16G; SERF_MEM_MAX=0 turns it off).
# Degrades to running uncapped (with a warning) where user scopes are unavailable.
# See scripts/run-capped.sh and docs/fuzzing.md ("Memory safety").
MEMCAP := scripts/run-capped.sh
# Fuzz replay is a deterministic evidence gate: never inherit a developer's
# persisted Go configuration or GOFLAGS, and always use this checkout's workspace.
override FUZZ_GOWORK := $(abspath $(CURDIR)/go.work)

# test covers the Go modules AND the frontend. The frontend gate runs as a third
# concurrent stream inside run-module-tests.sh (MAKE is passed through so it can
# re-enter this Makefile's test-web target); it is node work, so it overlaps the
# Go waves instead of adding its runtime on the end. WEB=0 skips it.
test:
	@MODULES="$(GO_MODULES)" MAKE="$(MAKE)" $(MEMCAP) scripts/run-module-tests.sh -short -count=1

test-short:
	@$(MAKE) test

# The permanent -race gate (CI), across every non-fuzz module. AGENT_PARALLEL=
# leaves the agent wave at GOMAXPROCS: under -race (~10x slower) extra
# parallelism just oversubscribes few-core CI and starves real per-test work past
# its timeouts. WEB=0: -race is a Go-toolchain gate, and the frontend suite is
# unaffected by it, so `make test` owns the web stream instead of paying it twice.
test-race:
	@MODULES="$(GO_MODULES)" WEB=0 AGENT_SHARDS=0 AGENT_PARALLEL= $(MEMCAP) scripts/run-module-tests.sh -race -short -count=1

# e2e-cover measures END-TO-END coverage of the real serf/serf-tui binaries via
# `go build -cover` + GOCOVERDIR — the main()/CLI/dispatch/serve paths unit tests
# structurally can't reach. --merge-unit unions it with the unit profile for a
# combined whole-repo number; SERF_E2E_LIVE=1 additionally runs the live provider
# scripts (needs real credentials). Local/on-demand, not a gate.
e2e-cover:
	@scripts/e2e-cover.sh --merge-unit

vet:
	@for m in $(GO_MODULES); do (cd $$m && go vet ./...) || exit 1; done

# FUZZ_SEED_REPLAY replays every module's COMMITTED seed corpus (and saved
# crashers) under the serffuzz tag. `go test -run '^Fuzz'` with no `-fuzz` does
# not search, so this is deterministic. `make fuzz` runs it as one of its steps;
# fuzz-seeds is the same work on its own.
FUZZ_SEED_REPLAY = for m in $(FUZZ_GO_MODULES); do (GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" $(MEMCAP) sh -c "cd $$m && go test -run '^Fuzz' -tags serffuzz -count=1 ./...") || exit 1; done

# fuzz-seeds is the RUNTIME half of the tagged-source gate whose compile half is
# `make lint-serffuzz`. It stays out of `make test` on measured cost: 144s
# across the workspace (the root module ~39s and agent ~65s of that) against a
# ~70s `make test`, for a class `make fuzz` and CI already replay. lint-serffuzz
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
# Everything here builds with -tags serffuzz so the internal/invariant assertions
# (primeradiant.com/serf/invariant) are live: a tripped invariant panics and the
# never-panic oracle catches it. The first step verifies the mechanism itself
# fires under the tag; production builds and `make test` stay tag-free.
fuzz:
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" $(MEMCAP) sh -c 'cd agent && go test -run "^$$" -tags serffuzz -count=1 ./...'
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" $(MEMCAP) sh -c 'cd invariant && go test -tags serffuzz ./...'
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" $(MEMCAP) sh -c 'cd fuzz && go test -tags serffuzz ./...'
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" $(MEMCAP) sh -c 'go test ./cmd/serf-fuzzcov ./cmd/serf-fuzz-harvest'
	@$(FUZZ_SEED_REPLAY)
	@set -eu; cap="$$(pwd)/$(MEMCAP)"; go_work="$(FUZZ_GOWORK)"; for target in $$(scripts/run-fuzz.sh --list | awk -F: '$$1 == "rapid" { print $$2 ":" $$3 ":" $$4 }'); do module=$${target%%:*}; rest=$${target#*:}; pkg=$${rest%%:*}; name=$${rest#*:}; for seed in 1 2 3 5 8; do echo "=== rapid replay $$module:$$name seed $$seed ==="; (cd "$$module" && GOENV=off GOFLAGS= GOWORK="$$go_work" env -u RAPID_FAILFILE RAPID_SEED="$$seed" RAPID_CHECKS=100 RAPID_STEPS=30 RAPID_NOFAILFILE=true RAPID_LOG=false RAPID_V=false RAPID_DEBUG=false RAPID_DEBUGVIS=false RAPID_SHRINKTIME=30s "$$cap" go test -tags serffuzz -run "^$${name}\$$" -count=1 "$$pkg"); done; done
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" $(MEMCAP) sh -c "go test -run '^Test.*Golden\$$' ./appwire"

# fuzz-goldens regenerates the decode SNAPSHOT goldens — serf's differential
# oracle. Each decode target's committed seed corpus is replayed and its decoded
# output canonically re-encoded into appwire/testdata/golden/. A code change that
# silently alters a decoder's output (no panic, round-trip still holds) fails the
# `make fuzz` golden check; run this ONLY after an INTENDED decoder change, then
# commit the diff. See docs/fuzzing.md ("Choosing an oracle").
fuzz-goldens:
	@$(MEMCAP) sh -c "go test -run '^Test.*Golden\$$' ./appwire -update-goldens"
	@$(MEMCAP) sh -c "cd llm && go test -run '^Test.*Golden\$$' ./providers/difftest -update-goldens"

# fuzz-nightly runs the unbounded coverage-guided search per target, bounded by a
# per-target time budget. Manual / nightly only — never in the gate.
fuzz-nightly:
	@$(MEMCAP) scripts/run-fuzz.sh $(FUZZ_ARGS)

# fuzz-triage is the local, on-demand campaign + auto-triage tool (8.7): it
# searches each surface, flake-guards and dedups any crasher, and opens ONE
# reviewable PR per distinct deterministic bug via the developer's local `gh`.
# Pass flags through FUZZ_ARGS, e.g. `make fuzz-triage FUZZ_ARGS="--time 5m"`,
# `make fuzz-triage FUZZ_ARGS=--no-pr`, or `make fuzz-triage FUZZ_ARGS=--dry-run`.
fuzz-triage:
	@$(MEMCAP) scripts/fuzz-triage.sh $(FUZZ_ARGS)

# fuzz-triage-selftest verifies the triage flake-guard / dedup / ledger / PR
# logic deterministically with synthetic failures and stubbed go/gh — no real
# search, crash, or PR. Run it after editing scripts/fuzz-triage.sh.
fuzz-triage-selftest:
	@scripts/fuzz-triage-selftest.sh

# fuzz-continuous is the LOCAL, on-demand continuous loop: it rotates over every
# native target, giving each a bounded search turn round after round (the corpus
# deepens across turns via $GOCACHE/fuzz), and routes any new crasher through
# fuzz-triage's flake-guard / dedup / PR pipeline. Runs until Ctrl-C or --total.
# Pass flags through FUZZ_ARGS, e.g. `make fuzz-continuous FUZZ_ARGS="--total 2h"`.
fuzz-continuous:
	@scripts/fuzz-continuous.sh $(FUZZ_ARGS)

# fuzz-continuous-selftest verifies the loop's rotation / stop-condition /
# crasher-delta logic with stubbed runner+triage — no real fuzzing.
fuzz-continuous-selftest:
	@scripts/fuzz-continuous-selftest.sh

# fuzz-drive generates REAL provider traffic (varied coding tasks through the
# serf one-shot CLI, recorders on) and harvests it into the seed corpus. Makes
# live, paid provider calls — run on demand, not in CI. Flags via FUZZ_ARGS, e.g.
# `make fuzz-drive FUZZ_ARGS="--providers openai/gpt-5.4-mini --runs 5"`.
fuzz-drive:
	@scripts/fuzz-drive.sh $(FUZZ_ARGS)

# fuzz-drive-selftest exercises the driver's contract (drive/retry/skip/harvest/
# PR) against a throwaway repo with stubbed serf+harvest+gh — no real calls.
fuzz-drive-selftest:
	@scripts/fuzz-drive-selftest.sh

# fuzz-coverage-global validates the registered target plan, requires a local
# native/Rapid fuzz surface for every production package, then replays each target
# into package-local profiles for strict whole-module accounting. Heavy + local.
# CHECK=1 enforces the raw >95% threshold and floors; BLESS=1 raises floors only
# after every measured module clears that threshold.
fuzz-coverage-global:
	@scripts/fuzz-coverage-global.sh $(if $(CHECK),--check) $(if $(BLESS),--bless) $(FUZZ_ARGS)

# fuzz-coverage-global-selftest verifies registry-gated replay/profile accounting
# wiring with a fake go executable and no real compilation.
fuzz-coverage-global-selftest:
	@scripts/fuzz-coverage-global-selftest.sh

# test-coverage-floor ratchets whole-module FULL-SUITE (unit+integration) coverage
# against scripts/testcov-global-floors.txt — the companion to fuzz-coverage-global
# (fuzz-reachable). CHECK=1 fails on a drop; BLESS=1 raises floors. Heavy + local.
test-coverage-floor:
	@scripts/test-coverage-floor.sh $(if $(CHECK),--check) $(if $(BLESS),--bless) $(COV_ARGS)

# mutation-floor gates the gremlins kill score: MIN=95 fails any curated package
# whose test efficacy drops below 95%. Slow (nightly). No MIN = report only.
mutation-floor:
	@scripts/fuzz-mutation-score.sh $(if $(MIN),--min-efficacy $(MIN)) $(MUT_ARGS)

# fuzz-bisect pinpoints the commit that introduced a crasher via git bisect,
# replaying one saved corpus entry per step. Supply args through FUZZ_ARGS, e.g.
# `make fuzz-bisect FUZZ_ARGS="--target llm:FuzzParseSSE --crasher <file> --good <ref>"`.
fuzz-bisect:
	@scripts/fuzz-bisect.sh $(FUZZ_ARGS)

# fuzz-bisect-selftest verifies bisection end-to-end against a throwaway git repo
# whose fuzz target crashes only after a known commit (real git bisect + replay).
fuzz-bisect-selftest:
	@scripts/fuzz-bisect-selftest.sh

# fuzz-oracle-audit proves every fuzz oracle reddens on its bug class (Phase 9 W1):
# each mutation in fuzz/mutations/ reintroduces a known fault in a throwaway
# worktree and the audit asserts the target FAILS. `FUZZ_ARGS=--gap-only` lists
# native targets that have no mutation yet; pass ids to audit only those.
fuzz-oracle-audit:
	@scripts/fuzz-oracle-audit.sh $(FUZZ_ARGS)

# fuzz-oracle-audit-selftest verifies the audit's caught/blind/rot/build-failure
# classification against a throwaway module (real worktree + go test, stubbed
# registry).
fuzz-oracle-audit-selftest:
	@scripts/fuzz-oracle-audit-selftest.sh

# fuzz-mutation-score (Phase 10 W5) measures detection sufficiency with gremlins:
# the per-package kill rate, and the surviving (LIVED) mutants are the weak-oracle
# worklist. Nightly/manual (slow); needs gremlins installed. Pass FUZZ_ARGS to
# score specific packages, e.g. `make fuzz-mutation-score FUZZ_ARGS="llm:./providercfg"`.
fuzz-mutation-score:
	@$(MEMCAP) scripts/fuzz-mutation-score.sh $(FUZZ_ARGS)

# fuzz-ledger pretty-prints the triage ledger (found/fixed/quarantined counts and
# the open-bug list) from fuzz/state/ledger.json.
fuzz-ledger:
	@jq -r 'to_entries | "found:     \([.[]|select(.value.status=="found")]|length)\nfixed:     \([.[]|select(.value.status=="fixed")]|length)\nquarantined: \([.[]|select(.value.status=="quarantined")]|length)"' fuzz/state/ledger.json
	@echo "--- open bugs (found) ---"
	@jq -r 'to_entries[] | select(.value.status=="found") | "  \(.key)\t\(.value.pr // "(no PR)")"' fuzz/state/ledger.json

# FUZZCOV_ARGS forwards CHECK=1 -> --check and BLESS=1 -> --bless to the reporter.
FUZZCOV_ARGS := $(if $(CHECK),--check) $(if $(BLESS),--bless)

# fuzz-coverage replays every fuzz target's COMMITTED corpus under -coverprofile
# (no -fuzz, so deterministic), computes each target's FOCUS-SET coverage %
# (primary, drivable to 100%) plus its whole-package % (secondary), enforces the
# no-regression ratchet against scripts/fuzzcov-floors.txt, and prints the gap map
# (decode/parse packages with zero fuzz coverage). Advisory by default; pass
# CHECK=1 to fail on a ratchet regression or a gap breach, BLESS=1 to raise floors.
fuzz-coverage:
	@$(MEMCAP) scripts/fuzz-coverage.sh $(FUZZCOV_ARGS)

# fuzz-gap-check is the FAST, STATIC gap gate (the blocking CI floor): it asserts
# every decode/parse package has a registered fuzz target (or a reasoned ignore),
# derived from scripts/run-fuzz.sh --list without replaying any corpus. Seconds,
# deterministic. The slow ratchet (fuzz-coverage CHECK=1) stays local/manual.
fuzz-gap-check:
	@scripts/fuzz-gap-check.sh

# fuzz-registry-check compares native and explicitly marked rapid targets in the
# authoritative manifest with AST-discovered workspace declarations. It does not
# run ordinary tests, a fuzz search, or any network activity.
fuzz-registry-check:
	@scripts/fuzz-registry-check.sh

# refresh-model-catalog replaces the vendored LiteLLM model-catalog snapshot
# with the current upstream and runs the catalog sanity tests. The vendored
# file must never be hand-edited (serf-curated data lives in
# serf_model_catalog_overrides.json); use `--check` via the script directly
# for a dry-run delta report.
refresh-model-catalog:
	@scripts/refresh-model-catalog.sh

# secret-scan runs gitleaks over the whole working tree using the committed
# .gitleaks.toml ruleset. Part of the gate (`make lint`); skips with a warning
# when gitleaks is not installed (required in CI).
secret-scan:
	$(call run_quiet_lint,scripts/gitleaks-scan.sh repo,preserve-gitleaks-warning)

# fuzz-corpus-scan runs gitleaks over only the fuzz seed corpora — the
# corpus-scoped subset of secret-scan, for fast harvester feedback.
fuzz-corpus-scan:
	@scripts/gitleaks-scan.sh corpus

# lint-naming enforces JSON=snake_case, TOML=snake_case across every Go
# struct tag and TOML file in the repo. Fast (well under a second) and
# safe to run as a separate `go vet`-style gate.
define run_quiet_lint
	@set -u; log="$$(mktemp -t serf-lint-check.XXXXXX)" || exit 1; \
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
	$(call run_quiet_lint,go run ./cmd/serf-namingcheck)

# lint-serffuzz is the compile floor for the //go:build serffuzz sources. Every
# other gate is tag-free — `make test`, `make lint`, `make vet` and
# `go build ./...` never compile those 250 files — so a production signature
# change that strands a tagged call site rots there until someone runs
# `make fuzz` by hand. `go vet` type-checks each module's test packages under
# the tag, which is what catches a stranded call site. Running the corpora
# themselves is 144s and stays in `make fuzz` / `make fuzz-seeds`; this pass is
# ~4s warm across the workspace, which is why it can sit in the gate.
lint-serffuzz:
	$(call run_quiet_lint,for m in $(FUZZ_GO_MODULES); do (cd $$m && go vet -tags serffuzz ./...) || exit 1; done)

# lint-internal fails if any exported symbol in the agent/llm/providercfg
# libraries names a serf-internal type — keeping them externally importable.
lint-internal:
	$(call run_quiet_lint,go run ./cmd/serf-internalcheck)

# lint-docs fails if any exported package-level declaration in the published
# library packages (llm, agent, agent/events, auth/openai) lacks a doc comment.
lint-docs:
	$(call run_quiet_lint,go run ./cmd/serf-docscheck)

build-namingcheck:
	go build -o serf-namingcheck ./cmd/serf-namingcheck/

# golangci-lint across every module (./... is per-module under go.work).
lint-golangci:
	@MODULES="$(GO_MODULES)" scripts/run-module-lint.sh

# generate runs all `go generate` directives. Currently the AppWire protocol
# reference (docs/appwire-protocol.md) from the catalog in appwire/protocol.go.
generate:
	go generate ./appwire/...

# lint-generated fails if a committed generated file is stale — i.e. the
# AppWire catalog changed without regenerating the protocol doc.
lint-generated:
	$(call run_quiet_lint,go generate ./appwire/... && { git diff --exit-code -- docs/appwire-protocol.md || { echo "docs/appwire-protocol.md is stale; run 'make generate' and commit."; exit 1; }; })

lint: lint-naming lint-serffuzz lint-internal lint-docs lint-golangci lint-generated secret-scan

clean:
	rm -f serf serf-hub serf-tui serf-doctor llmcall serf-namingcheck serf-internalcheck serf-fuzzcov
