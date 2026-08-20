.PHONY: build build-runtime build-go build-hub web-preflight build-web build-tui build-doctor build-all build-linux build-llmcall build-migrate dist install install-home install-system test-install

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

test-install:
	go test -count=1 -run '^TestInstallHomeGeneratedHome$$' .

# build-go compiles every non-fuzz Go workspace module. Keep it separate from
# build: the runtime pair owns the embedded frontend, while this target makes
# the workspace-wide compile contract explicit for CI and local diagnostics.
build-go:
	@for m in $(GO_MODULES); do (cd $$m && go build ./...) || exit 1; done
