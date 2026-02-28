.PHONY: build build-tui build-all test test-short vet lint clean

LDFLAGS := -X primeradiant.com/serf/buildinfo.GitSHA=$$(git rev-parse --short HEAD) \
           -X primeradiant.com/serf/buildinfo.GitDirty=$$(git diff --quiet && echo "" || echo "true") \
           -X primeradiant.com/serf/buildinfo.BuildTime=$$(date -u +%Y-%m-%dT%H:%M:%SZ)

build:
	go build -ldflags "$(LDFLAGS)" -o serf ./cmd/serf/

build-tui:
	go build -o serf-tui ./cmd/serf-tui/

build-all: build build-tui

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
	rm -f serf serf-tui llmcall
