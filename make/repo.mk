.PHONY: tools generate clean refresh-model-catalog help

# tools installs the CI-pinned lint/scanner versions from .tool-versions, so
# a local `make lint` runs exactly what CI runs.
## Install the CI-pinned golangci-lint and gitleaks versions from
## .tool-versions, so a local make lint runs exactly what CI runs.
tools:
	@set -eu; \
	golangci=$$(awk '$$1=="golangci-lint" {print $$2}' .tool-versions); \
	gitleaks=$$(awk '$$1=="gitleaks" {print $$2}' .tool-versions); \
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b "$$(go env GOPATH)/bin" "v$$golangci"; \
	go install github.com/zricethezav/gitleaks/v8@v$$gitleaks

# refresh-model-catalog replaces the vendored LiteLLM model-catalog snapshot
# with the current upstream and runs the catalog sanity tests. The vendored
# file must never be hand-edited (evener-curated data lives in
# evener_model_catalog_overrides.json); use `--check` via the script directly
# for a dry-run delta report.
## Replace the vendored LiteLLM model-catalog snapshot with the current
## upstream and run the catalog sanity tests.
refresh-model-catalog:
	@scripts/ops/refresh-model-catalog.sh

# generate runs all `go generate` directives. The AppWire protocol reference
# and frontend TypeScript declarations come from the catalog in appwire/protocol.go.
## Run every `go generate` directive: the AppWire protocol reference and
## frontend TypeScript declarations, generated from the catalog in
## appwire/protocol.go.
generate:
	go generate ./appwire/...

## Remove the built binaries from the repo root.
clean:
	rm -f evener evener-hub evener-tui evener-doctor llmcall evener-migrate evener-linux-amd64

## Print every make target, grouped by family, with its one-line summary.
help:
	@go run ./internal/maketargetsdoc -mode help
