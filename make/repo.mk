.PHONY: tools tools-golangci tools-gitleaks generate clean refresh-model-catalog help

# tools installs the CI-pinned lint/scanner versions from .tool-versions, so
# a local `make lint` runs exactly what CI runs.
## Install the CI-pinned golangci-lint and gitleaks versions from
## .tool-versions, so a local make lint runs exactly what CI runs.
tools:
	@$(MAKE) --no-print-directory tools-golangci
	@$(MAKE) --no-print-directory tools-gitleaks

## Install the CI-pinned golangci-lint version from .tool-versions.
tools-golangci:
	@set -eu; \
	golangci=$$(awk '$$1=="golangci-lint" {print $$2}' .tool-versions); \
	url=https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh; \
	attempt=0; for delay in 3 10 30 0; do attempt=$$((attempt+1)); \
		if installer=$$(curl -sSfL --retry 5 --retry-delay 2 "$$url") && \
			printf '%s\n' "$$installer" | sh -s -- -b "$$(go env GOPATH)/bin" "v$$golangci"; then exit 0; fi; \
		echo "tools-golangci: attempt $$attempt of 4 failed" >&2; \
		if [ "$$delay" -gt 0 ]; then echo "tools-golangci: retrying in $${delay}s" >&2; sleep "$$delay"; fi; \
	done; \
	echo "tools-golangci: golangci-lint install failed after 4 attempts" >&2; exit 1

## Install the CI-pinned gitleaks version from .tool-versions.
tools-gitleaks:
	@set -eu; \
	gitleaks=$$(awk '$$1=="gitleaks" {print $$2}' .tool-versions); \
	go install github.com/zricethezav/gitleaks/v8@v$$gitleaks

# refresh-model-catalog replaces the embedded models.dev snapshot in
# llm/registry/data/ (models.dev.json.gz plus the meta recording when and
# with which ETag it was fetched) with the current upstream, then runs the
# converter tests and the overlay report. The snapshot is never hand-edited:
# evener's corrections live beside it in providers_overlay.toml, which the
# registry layers on at load. Run the script directly with `--check` for a
# dry-run delta report.
## Replace the embedded models.dev snapshot in llm/registry/data/ with the
## current upstream and run the converter tests and overlay report.
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
