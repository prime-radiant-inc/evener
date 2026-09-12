# Linting

Static checks that gate merges without running tests: formatting, generated-
output freshness, compile floors, and the repo secret scan. `make lint` is
`LINT_TARGETS`: `lint-naming`, `lint-gofmt`, `lint-evenerfuzz`, `lint-eval`,
`lint-internal`, `lint-golangci`, `lint-generated`, `lint-fuzz-registry`, and
`secret-scan`. Every one of them is required CI.

`golangci-lint` and `gitleaks` are the only external tools the gate needs;
`make tools` installs the CI-pinned versions from `.tool-versions`. They
behave differently when absent: a missing `golangci-lint` fails the gate
outright, while a missing local `gitleaks` warns and returns zero — CI sets
`EVENER_GITLEAKS_REQUIRED=1` so absence there is a failure rather than a
silent skip. Read that local warning as a limitation, never as evidence that a
scan ran and found nothing — the same applies to `make fuzz-corpus-scan`,
which points the same tool at the committed fuzz corpora.

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
test packages under the tag, which is what catches a stranded call site. On a
non-Linux host it repeats that vet pass with `GOOS=linux`, because files tagged
`linux && evenerfuzz` are otherwise invisible to a macOS or other host-OS
checkout. Linux hosts reuse their native pass rather than paying for the same
analysis twice. Running the fuzz corpora themselves is 144s across the
workspace and stays in `make fuzz` / `make fuzz-seeds`; this compile-only pass
is ~4s warm on Linux and adds only the cross-GOOS pass off Linux, cheap enough
to sit in the required gate. `lint-eval` is the same idea for the
`eval`-tagged live-provider suites (context-compaction quality, forced
notes), including the same Linux cross-check for `linux && eval`. That tag had no floor at all for a while and duly rotted — a
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

## Why `lint-generated` runs `make generate`

`lint-generated` invokes the `generate` target rather than carrying its own
copy of the generate commands, and that is structural rather than tidiness:
the set of paths it diffs and the set it regenerates have to be the same set.
A diff list wider than what the recipe regenerates is a gate that reports
green forever while checking nothing — the state `lint-fuzz-registry` was in
when its recipe went missing and `make lint` kept reporting PASS across every
module.

The generator's exit status has to reach the gate for the same reason. A
family whose annotations no longer parse leaves its doc untouched, so the diff
over all eight outputs comes back clean; only the nonzero status says anything
is wrong. `lint-generated` runs `make generate` and the diff as one `&&`
chain, so a generator failure fails the gate before the diff is ever
consulted.

The diff names `HEAD` for a third instance of the same principle. A bare
`git diff` compares the working tree to the **index**, so `git add`ing a
regeneration without committing it satisfied the gate while the committed
output stayed stale — green over exactly the content the gate exists to check.
`git diff --exit-code HEAD` compares against what is committed, which is what
the gate claims to be about.

That is strictly stricter, never looser. Change an annotation and `make lint`
stays red until you commit the regenerated doc alongside it; staging is no
longer enough. Committing the two together is the unit this gate wants
anyway — an annotation and the doc generated from it are one change, and a
commit that carries only half of it leaves `HEAD` in the state the gate
exists to refuse.

## The golangci-lint cross-checkout cache hazard (issue #290)

A content-identical checkout of this repo at a second path — a git worktree,
in particular — can poison a shared `golangci-lint` cache (issue #290): the
cache keys on content, not path, so a lint run from one checkout can leave
cache entries a second, path-sensitive checkout then reads as valid and
silently produces wrong results. This affects both dropped `//nolint`
suppression and phantom findings from path exclusions.

Every Make-invoked golangci-lint process now gets a durable cache under
`${XDG_CACHE_HOME:-$HOME/.cache}/evener/golangci-lint/`, keyed by the absolute
worktree root. The cache is outside the repository, so gitleaks never scans
it, and it is reused by later lint runs in the same worktree without being
shared with siblings. `GOLANGCI_LINT_CACHE=/path make lint-golangci` remains an
explicit escape hatch when a caller needs a particular cache. Reclaim only the
current worktree's cache with `make lint-cache-clean`; this does not clean the
user's global golangci-lint cache or sibling worktrees.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/linting.mk, then run `make generate`. -->
| Command | Summary | What it proves | Trigger | Requires | Fails when |
| --- | --- | --- | --- | --- | --- |
| `make secret-scan` | Run gitleaks over the whole working tree using the committed .gitleaks.toml ruleset. | No secret matches the committed gitleaks ruleset anywhere in the working tree. | Required CI (via make lint); local pre-merge. | gitleaks. Local absence warns and returns zero; CI sets EVENER_GITLEAKS_REQUIRED=1. | A finding, or a required-tool absence under EVENER_GITLEAKS_REQUIRED=1, is nonzero. |
| `make lint-naming` | Enforce snake_case naming across every TOML data file in the repo. | Every TOML file's keys use snake_case naming. | Required CI (via make lint); local pre-merge. | None beyond the Go toolchain; deterministic, no provider calls. | Any TOML file has a non-snake_case key. |
| `make lint-evenerfuzz` | The compile floor for the //go:build evenerfuzz sources: host go vet and a host tagliatelle-only golangci-lint pass, plus GOOS=linux repeats of both on non-Linux hosts and a GOOS=windows go vet that holds the build-tag discipline the untagged sources get from static-build's cross-vet. See "Why two tagged lint passes exist" in docs/developing-evener/linting.md for the full rationale. | Every evenerfuzz-tagged source across FUZZ_GO_MODULES still compiles for the host, Linux, and Windows and passes its struct-tag casing floor, catching a production signature change that strands a tagged call site, and a Unix-only tagged source that never declared its constraint. | Required CI (via make lint); local pre-merge. ~4s warm across the workspace on Linux; the GOOS=linux pass runs only off Linux, the GOOS=windows vet everywhere. | golangci-lint. Reads .golangci.yml's casing rules, carve-outs, and exclusions via --enable-only tagliatelle. | host, GOOS=linux, or GOOS=windows go vet -tags evenerfuzz fails for any module, or either host or GOOS=linux tagliatelle reports a casing violation. |
| `make lint-eval` | The compile floor for the //go:build eval sources: host go vet and a host tagliatelle-only golangci-lint pass, plus GOOS=linux repeats of both on non-Linux hosts. | The eval-tagged live-provider suites (context-compaction quality, forced notes) still compile for the host and Linux. | Required CI (via make lint); local pre-merge. ~3.5s warm on Linux; the extra cross-GOOS pass runs only off Linux. | golangci-lint. Covers all of FUZZ_GO_MODULES, since eval sources could land in any module. | host or GOOS=linux go vet -tags eval fails for any module, or either host or GOOS=linux tagliatelle reports a casing violation. |
| `make lint-internal` | Fail if any exported symbol in the `agent` (plus its `diagnostic`/`execenv`/`mcpconfig`/`plugin`/`provider`/`schema`/`skill`/`task`/`transcript` subpackages), `llm`, or `llm/registry` libraries names a evener-internal type. | Those libraries stay externally importable — no exported symbol leaks an internal type name. | Required CI (via make lint); local pre-merge. | None beyond the Go toolchain. | cmd/evener-internalcheck finds an exported symbol naming an internal type. |
| `make lint-golangci` | golangci-lint across every workspace module, plus the second appwire-specific camelCase pass over server/appwire_*.go. See "The server/appwire_*.go camelCase regime" in docs/developing-evener/linting.md. | golangci-lint's full ruleset (struct-tag casing, formatting, exported-doc comments, and the rest) passes across every FUZZ_GO_MODULES workspace module, and the appwire camelCase regime holds for server/appwire_*.go. | Required CI (via make lint); local pre-merge. | golangci-lint. Runs against FUZZ_GO_MODULES, not GO_MODULES, so the fuzz module's ordinary Go is covered too. | Either golangci-lint run fails for any module. |
| `make lint-cache-clean` | Remove the current worktree's golangci-lint cache without touching sibling worktrees or the user's global golangci-lint cache. | The current worktree's isolated cache can be reclaimed on demand. | Local cleanup after a tool upgrade or cache diagnosis. | golangci-lint. | golangci-lint cannot clean the configured worktree cache. |
| `make lint-gofmt` | Keep every tracked Go source formatter-clean, including the tagged evenerfuzz/eval files golangci-lint's own gofmt pass never compiles. | `gofmt -l` reports nothing for any tracked .go file, tagged or not. | Required CI (via make lint); local pre-merge. | None beyond the Go toolchain. | Any tracked .go file is not gofmt-clean. |
| `make lint-generated` | Fail if any committed generated output is stale: the two AppWire outputs and the six docs/developing-evener/ target tables. | docs/appwire-protocol.md, the generated TypeScript protocol types, and the marked target-table regions in docs/developing-evener/'s six family docs all match what `make generate` produces right now. | Required CI (via make lint); local pre-merge. | None beyond the Go toolchain. | `make generate` exits nonzero, an expected output is no longer tracked, or regenerated output differs from what is committed. |
| `make lint-fuzz-registry` | Wrap `make fuzz-registry-check` so a fuzz target that lands without its registry row fails the required gate instead of sitting undetected. | Every native/Rapid fuzz target in the manifest (scripts/fuzz/fuzz-targets.txt) matches AST-discovered workspace declarations. | Required CI (via make lint); local pre-merge. Well under a second. | None beyond the Go toolchain; static AST analysis only. | A discovered fuzz target has no registry row, or a registry row has no discovered target. |
| `make lint` | Go lint, formatting, tagged floors, generated outputs, and secrets. | TOML naming; gofmt over every tracked .go file; the evenerfuzz and eval compile floors; the internal-type check; golangci-lint across every workspace module; generated-output freshness; the fuzz registry check; and the repo secret scan. | Required CI; local pre-merge. | golangci-lint, gitleaks. | Any member of LINT_TARGETS exits nonzero. |
<!-- END GENERATED -->
