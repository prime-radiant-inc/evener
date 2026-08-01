# Working in the Go workspace

serf is a `go.work` workspace of eight modules: the root plus `agent`,
`llm`, `auth`, `envvars`, `invariant`, `identifier`, and `fuzz`. Most of
what is surprising about working here follows from that one fact.

## `./...` covers one module, not the repo

`go build ./...`, `go vet ./...`, and `go test ./...` all resolve
per-module. Run from the root, they say nothing about `agent` or `llm`.
A change that breaks a sibling module passes every one of them.

`make lint` and `make test` loop the modules explicitly
(`scripts/run-module-lint.sh`, driven by `GO_MODULES` in the Makefile).
**Those are the gates.** A green `./...` is not evidence that the
workspace builds.

This has bitten before in CI, where a per-module `./...` silently skipped
the library test suites entirely for a period — the suites existed,
passed locally, and gated nothing.

## Intra-repo module edges need a versioned `replace`

`go.work use` alone is not enough once a module has external
dependencies: sibling imports 404 against the module proxy. The fix is a
versioned `replace` in `go.work` itself:

```
replace primeradiant.com/serf/agent v0.0.0 => ./agent
```

Keeping the replace in `go.work` rather than in each `go.mod` leaves the
committed `go.mod` files clean and publishable.

Before assuming a module edge does not exist, check: the root `go.mod`
already requires `agent`, and `cmd/serf-hub/internal/hubcore` already
imports `agent/schema` in several files. A wrapper type introduced to
"avoid a dependency" that already exists is pure cost — that is exactly
what the hub's `ReplayTurn` mirror turned out to be, and deleting it
recovered thirteen turn-level fields it had been quietly dropping.

## `go mod tidy` cannot maintain these `go.mod` files

Tidy ignores `go.work`. Run it in a module here and it tries to download
`primeradiant.com/serf/llm v0.0.0` and friends from the proxy instead of
using the sibling directories, so it cannot be the thing that keeps a
`go.mod` honest. Every require in them is hand-written.

That matters because each module is at go 1.17+, where **module graph
pruning makes a module's `go.mod` the complete statement of what its
packages need**: a consumer that requires only `primeradiant.com/serf/agent`
sees agent's requires and nothing deeper. A missing indirect is a build
failure for that consumer even though the workspace is green — the
workspace fills the gap from a sibling's `go.mod`, and a consumer has no
sibling. Pin each indirect at the version the workspace itself selects
(`go list -m <module>` from the root), so a consumer resolves the code
serf tested.

To check a module, build a consumer OUTSIDE the workspace: a scratch
module whose `go.mod` requires just that module, plus a directory
`replace` for every serf module, and no other requires. Then

```
GOWORK=off GOFLAGS= GOPROXY=off go list -deps primeradiant.com/serf/agent/...
```

`-mod=readonly` is the default and is the point: it reports "no required
module provides package X" instead of silently repairing the gap.
`GOFLAGS=` keeps an ambient `-mod=mod` from turning the probe into a
rewrite. Never point a `-mod=mod` probe at the tree — it edits `go.mod`
and `go.sum` in place; check `git status` after any probe.

## The compiler is the completeness net. Grep is not.

When removing or renaming a symbol, delete it and rebuild. Do not trust
a reference search, and do not trust an editor's "unused" analyzer.

Go type inference hides qualified references from a text search, and
build-tagged files are invisible to both grep and a default build. A
recent audit listed five unreferenced declarations; two were live,
referenced from a `-tags serffuzz` test file. Deleting them on the
strength of that list would have removed working code, and every tool
short of the compiler agreed the list was right.

Delete one declaration at a time so a failure names the culprit:

```
go build ./... && go vet ./...
```

Remember that a build tag hides a whole compilation unit. `make lint`
now type-checks the two tagged trees for you — `lint-serffuzz` and
`lint-eval` each run `go vet -tags <tag> ./...` across every module — so
a rename that strands a tagged call site fails the gate. Before those
floors existed it did not: renaming a test that
`cmd/serf-tui/root_factories_fuzz_test.go` replays by function value left
that build broken while `make lint` returned 0, and a `[]string` that
became a `[]summarizationRoute` stranded a `//go:build eval` caller for
six weeks.

**`golangci-lint` still does not look inside a tagged file.**
`scripts/run-module-lint.sh` runs a bare `golangci-lint run ./...` per
module and `.golangci.yml` sets no `build-tags`, so the lint rules
proper — unused, staticcheck, the rest — never see a `//go:build
serffuzz` or `//go:build eval` source. The floors are type-checking, not
linting. When a tagged file needs the full treatment, run it by hand:

```
golangci-lint run --build-tags serffuzz ./...
```

## `gofmt -r` is a blunt instrument

Rewrite rules are useful for mechanical renames across a package, with
three known failure modes:

- It **over-matches a field named after its type**. `go vet` catches the
  resulting breakage; nothing catches it earlier.
- It **skips `pkg.X` selectors**, so cross-package call sites need a
  separate pass.
- It **does not touch comments or strings**, which then disagree with
  the code — the most durable kind of wrong, because it reads as
  authoritative and never fails a test.

Gate every cluster with build + vet + `-race` before moving on.

## Fuzz seeds are a registration list

A `check*` or `fuzzScenario*` function that is never added to its seed
table never runs. It compiles, it looks like coverage, and it tests
nothing. `go vet` does not notice; `golangci-lint`'s `unused` does.

When adding a scenario function, add it to the table in the same commit.

## Generated files are gated

`make generate` regenerates the AppWire protocol reference
(`docs/appwire-protocol.md`) and frontend TypeScript declarations
(`cmd/serf-hub/frontend/src/protocol/types.gen.ts`) from the catalog in
`appwire/protocol.go`, and `make lint-generated` fails if either committed
output is stale.
Change the catalog, run `make generate`, commit both.
