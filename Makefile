.PHONY: build test test-short vet clean

build:
	go build -o serf ./cmd/serf/

test:
	go test -count=1 ./...

test-short:
	go test -short -count=1 ./...

vet:
	go vet ./...

clean:
	rm -f serf
