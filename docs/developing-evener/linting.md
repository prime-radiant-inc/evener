# Linting

Static checks that gate merges without running tests: formatting, generated-
output freshness, compile floors, and the repo secret scan. `make lint` is
`LINT_TARGETS`: `lint-naming`, `lint-gofmt`, `lint-evenerfuzz`, `lint-eval`,
`lint-internal`, `lint-golangci`, `lint-generated`, `lint-fuzz-registry`, and
`secret-scan`. Every one of them is required CI.

## Why two tagged lint passes exist

`lint-evenerfuzz` and `lint-eval` are compile floors for source trees that no
other gate ever touches. Every other gate is tag-free — `make test`,
`make lint`'s own `lint-golangci`, `make vet`, and a bare `go build ./...`
never compile the ~250 files behind `//go:build evenerfuzz` or the handful
behind `//go:build eval` — so a production signature change that strands a
tagged call site would otherwise rot there undetected until someone happened
to run `make fuzz` or a live eval suite by hand.

`lint-evenerfuzz` is the floor for the fuzz-tagged sources: `go vet -tags
evenerfuzz` across every `FUZZ_GO_MODULES` module type-checks each module's
test packages under the tag, which is what catches a stranded call site.
Running the fuzz corpora themselves is 144s across the workspace and stays in
`make fuzz` / `make fuzz-seeds`; this compile-only pass is ~4s warm, cheap
enough to sit in the required gate. `lint-eval` is the same idea for the
`eval`-tagged live-provider suites (context-compaction quality, forced
notes). That tag had no floor at all for a while and duly rotted — a
`[]string` that became a `[]summarizationRoute` in June stranded the
comparison eval's judge, and nothing said so for six weeks. Running eval
suites is never gate-shaped at any price: they spend real money against a
real provider, so compilation is the whole gate. Both passes sweep all of
`FUZZ_GO_MODULES` rather than just the modules known to carry tagged sources
today, because a floor is worth nothing in the module where the next tagged
file lands.

Each also carries a second run: a `golangci-lint` pass scoped to
`--enable-only tagliatelle`, the struct-tag-casing linter. `golangci-lint`
only analyses the files it actually compiles, and the tag-free `lint-golangci`
run compiles none of the tagged sources, so their struct-tag casing would
otherwise go unchecked. `--enable-only tagliatelle` keeps each tagged pass to
exactly the gate the tagged sources had before the naming check was tagliatelle-based,
while reading its casing rules, carve-outs, and exclusions from the one
`.golangci.yml`. Widening either tagged pass to the full linter set is a
separate, larger change — measured at 180 findings across the workspace (99
modernize, 20 revive, 13 staticcheck, ...) that these files have never been
held to.

## The `server/appwire_*.go` camelCase regime

`lint-golangci` runs `golangci-lint` across every `FUZZ_GO_MODULES` module —
not `GO_MODULES` — because the fuzz module's ordinary (untagged) Go needs the
same linting as everything else even though its fuzz targets run through
`make fuzz`, not the test gate.

It then runs a second, narrower pass: `golangci-lint` with
`.golangci-appwire.yml` against `./server/...` only. `server/appwire_*.go`
carries JSON/TOML struct tags in camelCase, the opposite of every other
package in the repo, because they mirror the AppWire wire protocol's own
naming. `tagliatelle`'s casing overrides are per-package, and these files
share the `server` package with 119 ordinary snake_case tags, so the two
casing regimes cannot live in one config. `.golangci-appwire.yml` holds the
camelCase half of the split and explains itself; the root `.golangci.yml`
holds everything else.

## The golangci-lint cross-checkout cache hazard (issue #290)

A content-identical checkout of this repo at a second path — a git worktree,
in particular — poisons the shared `golangci-lint` cache (issue #290): the
cache keys on content, not path, so a lint run from one checkout can leave
cache entries a second, path-sensitive checkout then reads as valid and
silently produces wrong results. A worktree that runs `make lint` without an
isolated cache does not just risk its own results — it can break the *main*
checkout's `make lint` the moment the worktree is removed. Point
`GOLANGCI_LINT_CACHE` at a private, worktree-scoped directory before running
any lint command from a second checkout of this repo.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/linting.mk, then run `make generate`. -->
<!-- END GENERATED -->
