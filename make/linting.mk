.PHONY: lint lint-naming lint-gofmt lint-evenerfuzz lint-eval lint-internal lint-golangci lint-generated lint-fuzz-registry secret-scan

# secret-scan runs gitleaks over the whole working tree using the committed
# .gitleaks.toml ruleset. Part of the gate (`make lint`); skips with a warning
# when gitleaks is not installed (required in CI).
secret-scan:
	$(call run_quiet_lint,scripts/ops/gitleaks-scan.sh repo,preserve-gitleaks-warning)

# lint-naming enforces TOML=snake_case across every TOML data file in the
# repo. Go struct tags are tagliatelle's job (.golangci.yml): JSON and TOML
# tags are snake_case everywhere but the camelCase wire-protocol packages.

lint-naming:
	$(call run_quiet_lint,go run ./cmd/evener-tomlcheck)

# lint-evenerfuzz is the floor for the //go:build evenerfuzz sources. Every
# other gate is tag-free — `make test`, `make lint`, `make vet` and
# `go build ./...` never compile those 250 files — so a production signature
# change that strands a tagged call site rots there until someone runs
# `make fuzz` by hand. `go vet` type-checks each module's test packages under
# the tag, which is what catches a stranded call site. Running the corpora
# themselves is 144s and stays in `make fuzz` / `make fuzz-seeds`; this pass is
# ~4s warm across the workspace, which is why it can sit in the gate.
#
# The tagliatelle pass is the struct-tag half of the same floor: golangci-lint
# only analyses the files it compiles, and the tag-free run in lint-golangci
# compiles none of these. --enable-only keeps the tagged pass to exactly the
# gate the tagged sources had before (evener-namingcheck parsed every .go file
# in the tree, tags or not) while reading its casing rules, carve-outs and
# exclusions from the one .golangci.yml. Widening the tagged pass to the whole
# linter set is a separate change: measured at 180 findings across the
# workspace (99 modernize, 20 revive, 13 staticcheck, ...) that these files
# have never been held to.
lint-evenerfuzz:
	$(call run_quiet_lint,for m in $(FUZZ_GO_MODULES); do (cd $$m && go vet -tags evenerfuzz ./...) || exit 1; done)
	$(call run_quiet_lint,for m in $(FUZZ_GO_MODULES); do (cd $$m && golangci-lint run --allow-parallel-runners --build-tags evenerfuzz --enable-only tagliatelle ./...) || exit 1; done)

# lint-eval is the same compile floor for the //go:build eval sources: the
# live-provider eval suites (context-compaction quality, forced notes). This tag
# never had a floor at all and duly rotted — a []string that became a
# []summarizationRoute in June stranded the comparison eval's judge, and nothing
# said so for six weeks. RUNNING these is not gate-shaped at any price: they
# spend real money against a real provider, so compilation is the whole gate.
# FUZZ_GO_MODULES is the full workspace list, and the floor wants all of it: eval
# sources sit only under agent/ today, and a floor is worth nothing in the module
# where the next one lands. ~3.5s warm, the same order as the evenerfuzz pass.
# It carries the same tagliatelle pass under its own tag, for the reason
# lint-evenerfuzz's comment gives.
lint-eval:
	$(call run_quiet_lint,for m in $(FUZZ_GO_MODULES); do (cd $$m && go vet -tags eval ./...) || exit 1; done)
	$(call run_quiet_lint,for m in $(FUZZ_GO_MODULES); do (cd $$m && golangci-lint run --allow-parallel-runners --build-tags eval --enable-only tagliatelle ./...) || exit 1; done)

# lint-internal fails if any exported symbol in the agent/llm/providercfg
# libraries names a evener-internal type — keeping them externally importable.
lint-internal:
	$(call run_quiet_lint,go run ./cmd/evener-internalcheck)

# golangci-lint across every module (./... is per-module under go.work).
# The runner lives in Go (cmd/evener-dev); MODULES and LINT_PARALLEL keep the
# interface run-module-lint.sh shipped with.
#
# FUZZ_GO_MODULES, not GO_MODULES: the sweep has to cover the fuzz module too.
# It is excluded from the test gate (its targets run through `make fuzz`), but
# its 29 struct-tag sites and everything else in it are ordinary Go that the
# linter must see — evener-namingcheck reached them by walking the filesystem
# from the repo root, and a module-list-driven linter only reaches what the
# list names.
#
# The second run is the server/appwire_*.go camelCase regime. tagliatelle's
# overrides are per-package and those files share package `server` with 119
# snake_case tags, so the split cannot live in one config; .golangci-appwire.yml
# holds the other half and explains itself.
lint-golangci:
	@MODULES="$(FUZZ_GO_MODULES)" go run ./cmd/evener-dev module-lint
	$(call run_quiet_lint,golangci-lint run --allow-parallel-runners --config .golangci-appwire.yml ./server/...)

# lint-gofmt keeps EVERY tracked Go source formatter-clean, including the ~250
# files behind //go:build evenerfuzz / eval. It is not redundant with the gofmt
# formatter in .golangci.yml: golangci-lint formats only the files it compiles,
# and the tag-free lint run compiles none of the tagged ones.
lint-gofmt:
	$(call run_quiet_lint,files="$$(git ls-files -z -- '*.go' | xargs -0 gofmt -l)"; status=$$?; if [ "$$status" -ne 0 ]; then if [ -n "$$files" ]; then printf '%s\n' "$$files"; fi; exit "$$status"; fi; if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi)

# lint-generated fails if either committed AppWire output is stale — i.e. the
# catalog changed without regenerating the protocol doc and TypeScript types.
lint-generated:
	$(call run_quiet_lint,go generate ./appwire/... && { git diff --exit-code -- docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts || { echo "generated AppWire outputs are stale; run 'make generate' and commit."; exit 1; }; })

# lint-fuzz-registry wraps the SAME check as `make fuzz-registry-check`
# (scripts/fuzz/fuzz-registry-check.sh) so a native/Rapid fuzz target that
# lands without its scripts/fuzz/fuzz-targets.txt row fails the required gate
# instead of sitting undetected: PR #273 added TestForceCompactDoubleLayerSeqFuzz
# without a registry row and nothing in CI or merge-approval-gate caught it,
# because fuzz-registry-check was wired to neither. This target is fast
# (well under a second) and safe to run as a separate `go vet`-style gate.
lint-fuzz-registry:
	$(call run_quiet_lint,scripts/fuzz/fuzz-registry-check.sh)

LINT_TARGETS := lint-naming lint-gofmt lint-evenerfuzz lint-eval lint-internal lint-golangci lint-generated lint-fuzz-registry secret-scan

lint: $(LINT_TARGETS)
