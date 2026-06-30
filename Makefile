.PHONY: build build-hub build-tui build-doctor build-all build-linux build-namingcheck dist install install-home install-system test-install test test-short test-race vet lint lint-naming lint-internal lint-docs lint-golangci clean fuzz fuzz-nightly fuzz-triage fuzz-triage-selftest fuzz-continuous fuzz-continuous-selftest fuzz-bisect fuzz-bisect-selftest fuzz-oracle-audit fuzz-oracle-audit-selftest fuzz-mutation-score fuzz-ledger fuzz-coverage fuzz-gap-check fuzz-goldens secret-scan fuzz-corpus-scan

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

build:
	go build -ldflags "$(LDFLAGS)" -o serf ./cmd/serf/

# Cross-compile for Linux (eval deployments). Invalidates the agent package
# cache to ensure embedded files (templates, sections, agent .md) are fresh.
build-linux:
	go clean -cache -x ./agent/ 2>/dev/null; \
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o serf-linux-amd64 ./cmd/serf/

build-hub:
	go build -o serf-hub ./cmd/serf-hub/

build-tui:
	go build -o serf-tui ./cmd/serf-tui/

# serf-doctor: the read-only forensic inspector (data plane of the doctoring system).
build-doctor:
	go build -o serf-doctor ./cmd/serf-doctor/

build-all: build build-hub build-tui build-doctor

build-llmcall:
	go build -o llmcall ./cmd/llmcall/

dist:
	rm -rf "$(SERF_DIST_BIN_DIR)" "$(SERF_DIST_ARCHIVE)"
	install -d "$(SERF_DIST_BIN_DIR)"
	CGO_ENABLED=0 GOOS=$(DIST_GOOS) GOARCH=$(DIST_GOARCH) go build -ldflags "$(LDFLAGS)" -o "$(SERF_DIST_BIN_DIR)/serf" ./cmd/serf/
	CGO_ENABLED=0 GOOS=$(DIST_GOOS) GOARCH=$(DIST_GOARCH) go build -o "$(SERF_DIST_BIN_DIR)/serf-hub" ./cmd/serf-hub/
	CGO_ENABLED=0 GOOS=$(DIST_GOOS) GOARCH=$(DIST_GOARCH) go build -o "$(SERF_DIST_BIN_DIR)/serf-tui" ./cmd/serf-tui/
	CGO_ENABLED=0 GOOS=$(DIST_GOOS) GOARCH=$(DIST_GOARCH) go build -o "$(SERF_DIST_BIN_DIR)/serf-doctor" ./cmd/serf-doctor/
	tar -C "$(DIST_DIR)" -czf "$(SERF_DIST_ARCHIVE)" "$(SERF_DIST_NAME)"

install:
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

# Every Go module in the workspace: the app (.) plus the three published
# libraries. Under go.work, `./...` resolves per-module, so the gates must loop
# over each module to cover the whole repo (root-only `./...` silently skips the
# agent/llm/auth library test suites and lint).
GO_MODULES := . agent llm auth fuzz invariant

# MEMCAP runs a recipe under a hard per-run memory ceiling (a systemd user scope)
# so a leaky test or fuzz run is OOM-killed individually instead of firing the
# kernel's global OOM killer and taking the whole host — and its network — down.
# Tune or disable via SERF_MEM_MAX (default 16G; SERF_MEM_MAX=0 turns it off).
# Degrades to running uncapped (with a warning) where user scopes are unavailable.
# See scripts/run-capped.sh and docs/fuzzing.md ("Memory safety").
MEMCAP := scripts/run-capped.sh

test:
	@MODULES="$(GO_MODULES)" $(MEMCAP) scripts/run-module-tests.sh -count=1

test-short:
	@MODULES="$(GO_MODULES)" $(MEMCAP) scripts/run-module-tests.sh -short -count=1

# The permanent -race gate (CI), across every module. AGENT_PARALLEL= leaves the
# agent wave at GOMAXPROCS: under -race (~10x slower) extra parallelism just
# oversubscribes few-core CI and starves real per-test work past its timeouts.
test-race:
	@MODULES="$(GO_MODULES)" AGENT_PARALLEL= $(MEMCAP) scripts/run-module-tests.sh -race -short -count=1

vet:
	@for m in $(GO_MODULES); do (cd $$m && go vet ./...) || exit 1; done

# fuzz runs every FuzzXxx target's SEED CORPUS plus any saved testdata/fuzz
# crashers as ordinary deterministic tests — `go test -run '^Fuzz'` with no
# -fuzz does NOT random-search, so it is fast and safe for the gate. `make test`
# already executes these seeds (go test runs Fuzz seed corpora as subtests); this
# target is the explicit entry point and the one used to verify saved crashers.
#
# Everything here builds with -tags serffuzz so the internal/invariant assertions
# (primeradiant.com/serf/invariant) are live: a tripped invariant panics and the
# never-panic oracle catches it. The first step verifies the mechanism itself
# fires under the tag; production builds and `make test` stay tag-free.
fuzz:
	@$(MEMCAP) sh -c 'cd invariant && go test -tags serffuzz ./...'
	@for m in $(GO_MODULES); do ($(MEMCAP) sh -c "cd $$m && go test -run '^Fuzz' -tags serffuzz ./...") || exit 1; done
	@$(MEMCAP) sh -c "go test -run '^Test.*Golden\$$' ./appwire"

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

# secret-scan runs gitleaks over the whole working tree using the committed
# .gitleaks.toml ruleset. Part of the gate (`make lint`); skips with a warning
# when gitleaks is not installed (required in CI).
secret-scan:
	@scripts/gitleaks-scan.sh repo

# fuzz-corpus-scan runs gitleaks over only the fuzz seed corpora — the
# corpus-scoped subset of secret-scan, for fast harvester feedback.
fuzz-corpus-scan:
	@scripts/gitleaks-scan.sh corpus

# lint-naming enforces JSON=snake_case, TOML=snake_case across every Go
# struct tag and TOML file in the repo. Fast (well under a second) and
# safe to run as a separate `go vet`-style gate.
lint-naming:
	go run ./cmd/serf-namingcheck

# lint-internal fails if any exported symbol in the agent/llm/providercfg
# libraries names a serf-internal type — keeping them externally importable.
lint-internal:
	go run ./cmd/serf-internalcheck

# lint-docs fails if any exported package-level declaration in the published
# library packages (llm, agent, agent/events, auth/openai) lacks a doc comment.
lint-docs:
	go run ./cmd/serf-docscheck

build-namingcheck:
	go build -o serf-namingcheck ./cmd/serf-namingcheck/

# golangci-lint across every module (./... is per-module under go.work).
lint-golangci:
	@for m in $(GO_MODULES); do (cd $$m && golangci-lint run ./...) || exit 1; done

# generate runs all `go generate` directives. Currently the AppWire protocol
# reference (docs/appwire-protocol.md) from the catalog in appwire/protocol.go.
generate:
	go generate ./appwire/...

# lint-generated fails if a committed generated file is stale — i.e. the
# AppWire catalog changed without regenerating the protocol doc.
lint-generated: generate
	@git diff --exit-code -- docs/appwire-protocol.md || \
	  { echo "docs/appwire-protocol.md is stale; run 'make generate' and commit."; exit 1; }

lint: lint-naming lint-internal lint-docs lint-golangci lint-generated secret-scan

clean:
	rm -f serf serf-hub serf-tui serf-doctor llmcall serf-namingcheck serf-internalcheck serf-fuzzcov
