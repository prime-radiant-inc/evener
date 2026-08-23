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

# generate runs the two packages that carry `go generate` directives. appwire
# produces the AppWire protocol reference and the frontend TypeScript
# declarations from the catalog in appwire/protocol.go; internal/maketargetsdoc
# produces the per-family target tables inside docs/developing-evener/'s marked
# regions from the `##` annotations in these make/*.mk files. lint-generated
# runs this target and diffs all of those outputs, so both are gated for
# staleness by the required lint gate.
## Run the appwire and maketargetsdoc `go generate` directives: the AppWire
## protocol reference and frontend TypeScript declarations from
## appwire/protocol.go, and the per-family make-target tables in
## docs/developing-evener/.
generate:
	go generate ./appwire/...
	go generate ./internal/maketargetsdoc/...

## Remove the built binaries from the repo root.
clean:
	rm -f evener evener-dev llmcall evener-linux-amd64

## Print every make target, grouped by family, with its one-line summary.
help:
	@go run ./internal/maketargetsdoc -mode help
