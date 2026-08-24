.PHONY: lint lint-naming lint-gofmt lint-evenerfuzz lint-eval lint-internal lint-golangci lint-generated lint-fuzz-registry lint-cache-clean secret-scan

# secret-scan runs gitleaks over the whole working tree using the committed
# .gitleaks.toml ruleset. Part of the gate (`make lint`); skips with a warning
# when gitleaks is not installed (required in CI).
## Run gitleaks over the whole working tree using the committed
## .gitleaks.toml ruleset.
## proves: No secret matches the committed gitleaks ruleset anywhere in the
##   working tree.
## trigger: Required CI (via make lint); local pre-merge.
## requires: gitleaks. Local absence warns and returns zero; CI sets
##   EVENER_GITLEAKS_REQUIRED=1.
## fails-when: A finding, or a required-tool absence under
##   EVENER_GITLEAKS_REQUIRED=1, is nonzero.
secret-scan:
	$(call run_quiet_lint,scripts/ops/gitleaks-scan.sh repo,preserve-gitleaks-warning)

# lint-naming enforces TOML=snake_case across every TOML data file in the
# repo. Go struct tags are tagliatelle's job (.golangci.yml): JSON and TOML
# tags are snake_case everywhere but the camelCase wire-protocol packages.
## Enforce snake_case naming across every TOML data file in the repo.
## proves: Every TOML file's keys use snake_case naming.
## trigger: Required CI (via make lint); local pre-merge.
## requires: None beyond the Go toolchain; deterministic, no provider calls.
## fails-when: Any TOML file has a non-snake_case key.
lint-naming:
	$(call run_quiet_lint,go run ./cmd/evener-dev/bin tomlcheck)

## The compile floor for the //go:build evenerfuzz sources: go vet under the
## tag, plus a tagliatelle-only golangci-lint pass. See "Why two tagged lint
## passes exist" in docs/developing-evener/linting.md for the full rationale.
## proves: Every evenerfuzz-tagged source across FUZZ_GO_MODULES still
##   compiles and passes its struct-tag casing floor, catching a production
##   signature change that strands a tagged call site.
## trigger: Required CI (via make lint); local pre-merge. ~4s warm across
##   the workspace.
## requires: golangci-lint. Reads .golangci.yml's casing rules, carve-outs,
##   and exclusions via --enable-only tagliatelle.
## fails-when: go vet -tags evenerfuzz fails for any module, or the
##   tagliatelle pass reports a casing violation.
lint-evenerfuzz:
	$(call run_quiet_lint,export GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)"; for m in $(FUZZ_GO_MODULES); do (cd $$m && go vet -tags evenerfuzz ./...) || exit 1; done)
	$(call run_quiet_lint,export GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)"; for m in $(FUZZ_GO_MODULES); do (cd $$m && golangci-lint run --allow-parallel-runners --build-tags evenerfuzz --enable-only tagliatelle ./...) || exit 1; done)

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
## The compile floor for the //go:build eval sources: go vet under the tag,
## plus a tagliatelle-only golangci-lint pass.
## proves: The eval-tagged live-provider suites (context-compaction quality,
##   forced notes) still compile.
## trigger: Required CI (via make lint); local pre-merge. ~3.5s warm.
## requires: golangci-lint. Covers all of FUZZ_GO_MODULES, since eval
##   sources could land in any module.
## fails-when: go vet -tags eval fails for any module, or the tagliatelle
##   pass reports a casing violation.
lint-eval:
	$(call run_quiet_lint,export GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)"; for m in $(FUZZ_GO_MODULES); do (cd $$m && go vet -tags eval ./...) || exit 1; done)
	$(call run_quiet_lint,export GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)"; for m in $(FUZZ_GO_MODULES); do (cd $$m && golangci-lint run --allow-parallel-runners --build-tags eval --enable-only tagliatelle ./...) || exit 1; done)

# lint-internal fails if any exported symbol in the agent/llm/providercfg
# libraries names a evener-internal type — keeping them externally importable.
## Fail if any exported symbol in the agent/llm/providercfg libraries names a
## evener-internal type.
## proves: The agent/llm/providercfg libraries stay externally importable —
##   no exported symbol leaks an internal type name.
## trigger: Required CI (via make lint); local pre-merge.
## requires: None beyond the Go toolchain.
## fails-when: cmd/evener-internalcheck finds an exported symbol naming an
##   internal type.
lint-internal:
	$(call run_quiet_lint,go run ./cmd/evener-dev/bin internalcheck)

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
## golangci-lint across every workspace module, plus the second
## appwire-specific camelCase pass over server/appwire_*.go. See "The
## server/appwire_*.go camelCase regime" in docs/developing-evener/linting.md.
## proves: golangci-lint's full ruleset (struct-tag casing, formatting,
##   exported-doc comments, and the rest) passes across every FUZZ_GO_MODULES
##   workspace module, and the appwire camelCase regime holds for
##   server/appwire_*.go.
## trigger: Required CI (via make lint); local pre-merge.
## requires: golangci-lint. Runs against FUZZ_GO_MODULES, not GO_MODULES, so
##   the fuzz module's ordinary Go is covered too.
## fails-when: Either golangci-lint run fails for any module.
lint-golangci:
	@MODULES="$(FUZZ_GO_MODULES)" GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" go run ./cmd/evener-dev/bin dev module-lint
	$(call run_quiet_lint,GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" golangci-lint run --allow-parallel-runners --config .golangci-appwire.yml ./server/...)

## Remove the current worktree's golangci-lint cache without touching sibling
## worktrees or the user's global golangci-lint cache.
## proves: The current worktree's isolated cache can be reclaimed on demand.
## trigger: Local cleanup after a tool upgrade or cache diagnosis.
## requires: golangci-lint.
## fails-when: golangci-lint cannot clean the configured worktree cache.
lint-cache-clean:
	@GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" golangci-lint cache clean

# lint-gofmt keeps EVERY tracked Go source formatter-clean, including the ~250
# files behind //go:build evenerfuzz / eval. It is not redundant with the gofmt
# formatter in .golangci.yml: golangci-lint formats only the files it compiles,
# and the tag-free lint run compiles none of the tagged ones.
## Keep every tracked Go source formatter-clean, including the tagged
## evenerfuzz/eval files golangci-lint's own gofmt pass never compiles.
## proves: `gofmt -l` reports nothing for any tracked .go file, tagged or
##   not.
## trigger: Required CI (via make lint); local pre-merge.
## requires: None beyond the Go toolchain.
## fails-when: Any tracked .go file is not gofmt-clean.
lint-gofmt:
	$(call run_quiet_lint,files="$$(git ls-files -z -- '*.go' | xargs -0 gofmt -l)"; status=$$?; if [ "$$status" -ne 0 ]; then if [ -n "$$files" ]; then printf '%s\n' "$$files"; fi; exit "$$status"; fi; if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi)

# lint-generated fails if any committed generated output is stale: the appwire
# catalog or a make/*.mk annotation changed without the outputs being
# regenerated.
#
# It invokes the `generate` target rather than carrying its own copy of the
# generate commands. That is the whole point: a diff list wider than what the
# recipe actually regenerates is a gate that reports green forever while
# checking nothing — the lint-fuzz-registry failure mode this repo was already
# bitten by once. The && chain also means a generator that FAILS fails this
# gate; it must never fall through to a clean diff over outputs nothing
# rewrote.
#
# The diff is against HEAD, not the index. A bare `git diff` compares the
# working tree to the INDEX, so `git add`ing a regeneration without committing
# it made the gate pass while the COMMITTED output was still stale — the gate
# reporting green over exactly the content it claims to check (ruling R26).
#
# Naming HEAD is strictly stricter, never looser: an uncommitted regeneration
# still fails, and staging no longer silences it. That is the intended cost.
# The unit this gate wants is one commit carrying both the changed annotation
# and its regenerated doc, which is why the message says "and commit".
# `git diff` does not report an untracked path, so the recipe first requires
# every expected output to remain tracked. Otherwise deleting an output from
# HEAD and letting the generator recreate it would make the gate pass. The
# output list is bound once so the trackedness and content checks cannot drift.
## Fail if any committed generated output is stale: the two AppWire outputs
## and the six docs/developing-evener/ target tables.
## proves: docs/appwire-protocol.md, the generated TypeScript protocol
##   types, and the marked target-table regions in docs/developing-evener/'s
##   six family docs all match what `make generate` produces right now.
## trigger: Required CI (via make lint); local pre-merge.
## requires: None beyond the Go toolchain.
## fails-when: `make generate` exits nonzero, an expected output is no longer
##   tracked, or regenerated output differs from what is committed.
lint-generated:
	$(call run_quiet_lint,$(MAKE) generate && { outputs='docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/developing-evener/README.md docs/developing-evener/building.md docs/developing-evener/testing.md docs/developing-evener/linting.md docs/developing-evener/fuzzing.md docs/developing-evener/coverage.md'; git ls-files --error-unmatch -- $$outputs >/dev/null && git diff --exit-code HEAD -- $$outputs || { echo "the paths above are untracked or differ from HEAD. make generate has already run; the fix is to commit that diff - it is either a regenerated table or a hand-written edit inside one of these files."; exit 1; }; })

# lint-fuzz-registry wraps the SAME check as `make fuzz-registry-check`
# (scripts/fuzz/fuzz-registry-check.sh) so a native/Rapid fuzz target that
# lands without its scripts/fuzz/fuzz-targets.txt row fails the required gate
# instead of sitting undetected: PR #273 added TestForceCompactDoubleLayerSeqFuzz
# without a registry row and nothing in CI or merge-approval-gate caught it,
# because fuzz-registry-check was wired to neither. This target is fast
# (well under a second) and safe to run as a separate `go vet`-style gate.
## Wrap `make fuzz-registry-check` so a fuzz target that lands without its
## registry row fails the required gate instead of sitting undetected.
## proves: Every native/Rapid fuzz target in the manifest
##   (scripts/fuzz/fuzz-targets.txt) matches AST-discovered workspace
##   declarations.
## trigger: Required CI (via make lint); local pre-merge. Well under a
##   second.
## requires: None beyond the Go toolchain; static AST analysis only.
## fails-when: A discovered fuzz target has no registry row, or a registry
##   row has no discovered target.
lint-fuzz-registry:
	$(call run_quiet_lint,scripts/fuzz/fuzz-registry-check.sh)

LINT_TARGETS := lint-naming lint-gofmt lint-evenerfuzz lint-eval lint-internal lint-golangci lint-generated lint-fuzz-registry secret-scan

## Go lint, formatting, tagged floors, generated outputs, and secrets.
## proves: TOML naming; gofmt over every tracked .go file; the evenerfuzz and
##   eval compile floors; the internal-type check; golangci-lint across every
##   workspace module; generated-output freshness; the fuzz registry check;
##   and the repo secret scan.
## trigger: Required CI; local pre-merge.
## requires: golangci-lint, gitleaks.
## fails-when: Any member of LINT_TARGETS exits nonzero.
lint: $(LINT_TARGETS)
