.PHONY: build build-hub build-tui build-doctor build-all build-linux build-namingcheck test test-short test-race vet lint lint-naming lint-internal lint-docs lint-golangci clean

LDFLAGS := -X primeradiant.com/serf/buildinfo.GitSHA=$$(git rev-parse --short HEAD) \
           -X primeradiant.com/serf/buildinfo.GitDirty=$$(git diff --quiet && echo "" || echo "true") \
           -X primeradiant.com/serf/buildinfo.BuildTime=$$(date -u +%Y-%m-%dT%H:%M:%SZ)

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

# Every Go module in the workspace: the app (.) plus the three published
# libraries. Under go.work, `./...` resolves per-module, so the gates must loop
# over each module to cover the whole repo (root-only `./...` silently skips the
# agent/llm/auth library test suites and lint).
GO_MODULES := . agent llm auth

test:
	@for m in $(GO_MODULES); do (cd $$m && go test -count=1 ./...) || exit 1; done

test-short:
	@for m in $(GO_MODULES); do (cd $$m && go test -short -count=1 ./...) || exit 1; done

# The permanent -race gate (CI), across every module.
test-race:
	@for m in $(GO_MODULES); do (cd $$m && go test -race -short -count=1 ./...) || exit 1; done

vet:
	@for m in $(GO_MODULES); do (cd $$m && go vet ./...) || exit 1; done

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

lint: lint-naming lint-internal lint-docs lint-golangci

clean:
	rm -f serf serf-hub serf-tui llmcall serf-namingcheck serf-internalcheck
