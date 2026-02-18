.PHONY: build build-tui build-all test test-short vet lint clean

build:
	go build -o serf ./cmd/serf/

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
