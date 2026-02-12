.PHONY: build test test-short vet lint clean

build:
	go build -o serf ./cmd/serf/

test:
	go test -count=1 ./...

test-short:
	go test -short -count=1 ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

clean:
	rm -f serf
