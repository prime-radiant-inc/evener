.PHONY: build build-hub build-tui build-all build-linux test test-short vet lint clean

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

build-all: build build-hub build-tui

build-llmcall:
	go build -o llmcall ./cmd/llmcall/

test:
	go test -count=1 ./...

test-short:
	go test -short -count=1 ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

clean:
	rm -f serf serf-hub serf-tui llmcall
