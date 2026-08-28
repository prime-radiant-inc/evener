.PHONY: build build-runtime build-go build-hub web-preflight build-web build-dev build-all build-linux build-llmcall dist install install-home install-system test-install

LDFLAGS := -X primeradiant.com/evener/buildinfo.GitSHA=$$(git rev-parse --short HEAD) \
           -X primeradiant.com/evener/buildinfo.GitDirty=$$(git --no-optional-locks diff-files --quiet && echo "" || echo "true") \
           -X primeradiant.com/evener/buildinfo.BuildTime=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
           -X primeradiant.com/evener/buildinfo.Channel=$(BUILD_CHANNEL)

PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
EVENER_SHARE_BINDIR ?= $(PREFIX)/share/evener/bin
INSTALL_BUILD_DIR ?= .build/install
EVENER_INSTALL_BINS := evener evener-dev
BUILD_CHANNEL ?=

## Build the evener binary with a fresh embedded SPA. The default goal.
## proves: build-web completes before evener is built, so the binary contains
##   the fresh SPA embedded by the hub subcommand.
## trigger: Local pre-merge; required CI together with make build-go.
## requires: Go, Node/npm, the frontend install, and enough disk. Build
##   metadata includes the current SHA, dirty state, time, and channel.
## fails-when: Frontend preflight, build, or pair-script failure is nonzero;
##   stale or failed embedding is not a pass.
build: build-runtime

# build-runtime depends on build-web (not the reverse): make guarantees a
# target's prerequisites COMPLETE before its recipe runs — even under
# parallel make (-j) — so hanging build-web off build-runtime structurally
# guarantees every evener build embeds the dist build-web just produced. No
# target may ship an evener binary with a stale or empty embedded web UI.
## The actual recipe `build` runs: builds evener via
## scripts/ops/build-runtime-pair.sh.
build-runtime: build-web
	LDFLAGS="$(LDFLAGS)" scripts/ops/build-runtime-pair.sh

# Cross-compile for Linux (eval deployments). Invalidates the agent package
# cache to ensure embedded files (templates, sections, agent .md) are fresh.
## Cross-compile evener-linux-amd64 for Linux eval deployments. Starts by
## running `go clean -cache`, which wipes the whole Go build cache.
build-linux:
	go clean -cache 2>/dev/null && \
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o evener-linux-amd64 ./cmd/evener/

## Alias for build-runtime.
build-hub: build-runtime

# web-preflight owns the frontend node_modules install for every web target,
# so build-web and test-web share one definition of "the install is ready".
# The install rules and the two guards they exist for live in the script.
## Ensure the frontend dependency install is present, healthy, and
## lockfile-compatible before any web target runs.
## proves: The worktree has a lockfile-compatible install and a real local
##   TypeScript compiler.
## trigger: Setup prerequisite for the web/build/browser gates.
## requires: May run npm when the install is absent or stale; refuses an
##   unsafe npm ci through a mismatched shared symlink.
## fails-when: A missing, mismatched, or unhealthy install is nonzero;
##   npm/network unavailability is a setup failure.
web-preflight:
	@NODE_DISABLE_COMPILE_CACHE=1 scripts/web/web-preflight.sh

# build-web builds the frontend TypeScript/React app (cmd/evener-hub/frontend)
# into frontend/dist, which evener embeds via go:embed (the hub subcommand).
# The vite build itself stays unconditional — dist freshness is the entire
# point, the install step is the only cacheable part. vite's emptyOutDir wipes
# the tracked dist/PLACEHOLDER on every build; vite.config.ts writes it back
# at closeBundle, so no recipe here restores it and `git status` stays clean
# after a build however vite was invoked (kata 88nn). npm run build also
# cleans dist down to that PLACEHOLDER BEFORE the fallible typecheck runs
# (frontend scripts/clean-dist.mjs): emptyOutDir only fires inside
# `vite build`, so a failed typecheck used to leave the previous build's dist
# behind for go:embed to silently pick up — a failed build now leaves only
# the PLACEHOLDER, which embeds and serves the documented 503 instead of a
# stale SPA.
## Build the frontend (TypeScript typecheck + Vite production build) into
## frontend/dist for Go embedding.
## proves: TypeScript typecheck and the Vite production build complete and
##   refresh frontend/dist.
## trigger: Frontend CI; prerequisite of the runtime and release builds.
## requires: Node/npm; may run npm ci when the install is absent or stale.
##   No provider credentials. Node's automatic compile cache is disabled.
## fails-when: An npm, typecheck, or Vite failure is nonzero.
build-web: web-preflight
	cd cmd/evener-hub/frontend && NODE_DISABLE_COMPILE_CACHE=1 npm run build

## Build the llmcall standalone CLI binary.
build-llmcall:
	go build -o llmcall ./cmd/llmcall/

## Build the evener-dev dev/test infrastructure binary (agent-shards,
## module-lint, fuzz-harvest, fuzzcov, fuzzregistry, internalcheck,
## tomlcheck, transcript-v2-upgrade). Not installed for
## end users; used by make targets and go run ./cmd/evener-dev/bin.
build-dev:
	go build -o evener-dev ./cmd/evener-dev/bin/

# build-all builds both the runtime binary and the dev binary.
## Build every binary: the evener runtime binary and the evener-dev
## dev/test infrastructure binary.
build-all: build-runtime build-dev

# dist builds the release artifacts with goreleaser in snapshot mode (no tag,
# no publish): dist/ holds evener_<os>_<arch>.tar.gz plus checksums.txt — the
# same layout the release workflow ships and install.sh consumes. The web
# build runs as goreleaser's before hook, so the binary embeds a fresh SPA
# rather than the tracked PLACEHOLDER.
## Build release/distribution binaries with goreleaser in snapshot mode.
## proves: goreleaser builds evener for linux/amd64 and darwin/arm64 into
##   directory-wrapped archives plus checksums.txt, with a fresh SPA embedded
##   via the before hook.
## trigger: Release/snapshot CI; manual distribution verification.
## requires: Cross-compilation and frontend dependencies; release CI has
##   networked setup for tool/dependency installation.
## fails-when: Any build, archive, inspection, or checksum failure is
##   nonzero; unavailable release tooling blocks release.
dist:
	goreleaser release --snapshot --clean

# An installed evener must embed a fresh SPA, not the tracked PLACEHOLDER
# (install-home/install-system inherit via install).
## Install the evener and evener-dev binaries into PREFIX (default ~/.local),
## building a fresh SPA first so the installed evener never embeds the
## tracked placeholder.
install: build-web
	install -d "$(INSTALL_BUILD_DIR)"
	go build -ldflags "$(LDFLAGS)" -o "$(INSTALL_BUILD_DIR)/evener" ./cmd/evener/
	go build -o "$(INSTALL_BUILD_DIR)/evener-dev" ./cmd/evener-dev/bin/
	install -d "$(EVENER_SHARE_BINDIR)" "$(BINDIR)"
	@for bin in $(EVENER_INSTALL_BINS); do \
		install -m 0755 "$(INSTALL_BUILD_DIR)/$$bin" "$(EVENER_SHARE_BINDIR)/$$bin"; \
		ln -sfn "$(EVENER_SHARE_BINDIR)/$$bin" "$(BINDIR)/$$bin"; \
	done

install-home: PREFIX := $(HOME)/.local
## Install into the user prefix (~/.local).
install-home: install

install-system: PREFIX := /usr/local
## Install into the system prefix (/usr/local).
install-system: install

## Integration-test the install path end to end: copy the tracked working
## tree into a fixture, run the install target with a synthetic HOME, and
## verify the installed binaries and symlinks. Skipped under -short.
test-install:
	go test -count=1 -run '^TestInstallHomeGeneratedHome$$' .

# build-go compiles every non-fuzz Go workspace module. Keep it separate from
# build: the runtime binary owns the embedded frontend, while this target
# makes the workspace-wide compile contract explicit for CI and local
# diagnostics.
## Compile every non-fuzz Go workspace module.
## proves: All packages in the seven GO_MODULES compile, including packages
##   root-level `go build ./...` does not visit under go.work.
## trigger: Required CI build job; local compile diagnostic.
## requires: Deterministic Go compilation; no provider calls or
##   frontend/browser requirements.
## fails-when: Any module or package compilation failure is nonzero; the loop
##   stops at the first failing module.
build-go:
	@for m in $(GO_MODULES); do (cd $$m && go build ./...) || exit 1; done
