# Race CI Runtime Reduction Implementation Plan

**Goal:** Reduce the required race-detector critical path without reducing
coverage or adding custom scheduling machinery.

**Design:** Cache only parsed immutable registry inputs, keep each loaded
registry independently owned, and split already-independent non-root modules
across a native two-entry GitHub Actions matrix.

**Spec:** `docs/superpowers/specs/2026-08-31-race-ci-runtime-reduction-design.md`

## Constraints

- Preserve every module in `GO_MODULES` and `-race -short -count=1`.
- Keep `RACE_SCOPE=nonroot` available for local aggregate reproduction.
- Keep `WEB=0`, `AGENT_SHARDS=0`, and `AGENT_PARALLEL=` under `-race`.
- Do not shard packages by test name, increase test parallelism, relax
  timeouts, or add larger runners.
- Cache parsed embedded inputs only; never share supported mutable runtime
  state between loaded registries. `Provider()` returns an independently owned
  view; `Resolved` reference fields retain their documented
  read-only/copy-before-mutation contract.
- Keep custom snapshots, overlays, runtime caches, user configuration,
  credentials, injected instances, refresh state, and live listings per load.
- Treat reference-valued fields passed through `WithInstances` as immutable
  caller-owned option input; per-load isolation covers membership and runtime
  state, not recursive snapshots of reused inputs.
- Use behavior tests rather than assertions over rendered scripts or workflow
  text.
- Run `make generate` after changing annotated Make targets.

## Task 1: Pin registry ownership

Files:

- `llm/registry/load.go`
- `llm/registry/overlay.go`
- `llm/registry/overlay_test.go`
- `llm/registry/load_test.go`

Steps:

1. Add a test proving `EmbeddedOverlay()` returns independently owned bytes.
2. Add helpers for deterministic offline embedded loads.
3. Prove separate loads do not share live listings or injected instances.
4. Prove custom snapshots and overlays bypass their corresponding defaults.
5. Prove mutating a `Provider()` result, including nested reference values,
   cannot affect the same registry or a later embedded load.
6. Deep-copy every reference-valued field at the `Provider()` public boundary.
7. Add concurrent default-load coverage under `-race`.
8. Add `BenchmarkLoadEmbeddedDefaults` as a diagnostic benchmark.
9. Change `EmbeddedOverlay()` to return `bytes.Clone(embeddedOverlay)`.
10. Audit every repository `Provider()` consumer, document the owned return
    value in the exported API comment, and verify no caller mutates it.

Acceptance:

- Ownership tests fail against shared overlay bytes and pass after cloning.
- Existing runtime-cache fallback and warning tests remain green.
- No exported signature or schema changes; `EmbeddedOverlay()` and
  `Provider()` intentionally strengthen their ownership semantics.
- Same-registry and later-load `Provider()` calls remain unchanged after a
  returned value's nested maps, slices, and pointers are mutated.
- Separate loads retain independent live listings, injected membership, and
  runtime state; tests do not claim recursive ownership of reused option input.

## Task 2: Cache parsed embedded sources

Files:

- `llm/registry/load.go`
- `llm/registry/load_test.go`

Steps:

1. Add unexported `sync.OnceValues` loaders for the parsed embedded catalog and
   overlay.
2. Keep snapshot and overlay selection independent so either override can
   bypass only its corresponding cache.
3. Reuse the already-parsed valid runtime catalog after validation.
4. Construct fresh mutable registry state for every `Load` call.
5. Serialize the cached parsed catalog and overlay before and after repeated
   concurrent loads and resolutions to prove internal code does not mutate
   them.
6. Run focused load, override, refresh, ownership, and concurrent race tests.
   The refresh cases must prove that a newer runtime cache beats
   `WithSnapshot`, while `WithSnapshot` plus `WithoutCache` retains the custom
   snapshot.
7. Set `UNCACHED_SHA` to the ownership-and-benchmark commit immediately before
   caching and `CACHED_SHA` to its child containing only the cache
   implementation and cache-specific mutation test. Both must descend from the
   current base. The present pair is
   `140e3a396b918797655f6afdf9106ff3e49f008c` and
   `390ab245c39e1d98691019ce8bf701586ba3ebe9`; replace and record both after
   every rebase. On each revision, run five samples from the registry module
   with `(cd llm && go test ./registry -run '^$' -bench
   '^BenchmarkLoadEmbeddedDefaults$' -benchmem -count=5)` and use median
   `ns/op`.
8. On both `UNCACHED_SHA` and `CACHED_SHA`, run `(cd llm && go test ./registry
   -run '^$' -bench
   '^BenchmarkLoadEmbeddedDefaults$' -benchtime=1x -count=1
   -memprofile=heap.pprof -memprofilerate=1 && go tool pprof
   -sample_index=inuse_space -top heap.pprof)` and read total `inuse_space`.
9. Add and run `BenchmarkProviderCatalogViews` to compare the full curated
   catalog through the owned public view and the former shallow-reference
   behavior in the same process.

Acceptance:

- Median embedded-load speedup is at least 2x.
- Retained heap increase is at most 24 MiB.
- Concurrent and independent-load tests pass under `-race`.
- The owned full-catalog provider view takes at most 35% of cached-load time,
  45% of cached-load allocated bytes, and 8 MiB per operation in the same
  benchmark session.

## Task 3: Expose logical race scopes

Files:

- `make/testing.mk`
- `docs/developing-evener/testing.md`

Steps:

1. Add `agent` and `nonagent` selectors derived from `GO_MODULES`.
2. Preserve `all`, `root`, and `nonroot` behavior.
3. Reject unknown scopes and derived scopes that select no modules.
4. Run `make generate` and verify the generated testing table.
5. Run the five valid scopes: `all`, `root`, `nonroot`, `agent`, and
   `nonagent`.
6. Verify one unknown scope and one forced empty derived scope both fail.

Acceptance:

- `agent` selects only `agent`.
- `nonagent` selects every non-root module except `agent`.
- The union of `agent` and `nonagent` equals `nonroot` with no overlap.

## Task 4: Split the CI lane

File:

- `.github/workflows/ci.yml`

Steps:

1. Replace the single non-root worker with a two-entry `agent`/`nonagent`
   matrix.
2. Set `fail-fast: false` and `max-parallel: 2`.
3. Use the existing checkout, toolchain, and `make test-race` path.
4. Preserve `race-modules` as an `if: always()` aggregate required check.
5. Keep downstream dependencies pointed at `race-modules`.
6. Run `actionlint .github/workflows/ci.yml`, inspect the checked-in aggregator
   job's `needs` and jq predicate, and manually evaluate its truth table for
   all-success and one `failure`, `cancelled`, or `skipped` result. This is a
   workflow review, not a brittle test over rendered YAML.
7. In the live PR run, verify the matrix produces `race-modules / agent` and
   `race-modules / nonagent`, the aggregator succeeds after both, and GitHub's
   resulting required-check display name is exactly `race-modules`.

Acceptance:

- Both workers run the intended derived scope.
- `race-modules` cannot succeed unless the matrix succeeds: all-success makes
  the predicate true, and each non-success result makes it false.
- The live GitHub check display is `race-modules`, and downstream dependencies
  and branch-protection naming remain stable.

## Task 5: Repair evaluation-only synchronization failures

File:

- `cmd/evener-hub/app_relay_test.go`

During evaluation, the remap delivery test could assert before the replacement
read's relay publication had completed. The production path was not at fault;
the test lacked a causal observation barrier.

Steps:

1. Wait for the replacement read to acknowledge remap publication.
2. Enqueue a marker on the same connection after publication.
3. Count the target notification before that marker.
4. Run the focused test repeatedly with and without `-race`.

Acceptance:

- Notification ordering no longer depends on elapsed time; the two existing
  one-second waits remain bounded liveness deadlines.
- Production behavior remains unchanged.

## Task 6: Verify the complete candidate

For this branch reconstruction, the exact base is
`08559860b08282e212e91bba1d94641b0b4898e6`. Run on one clean head containing
that base:

```bash
make generate
make merge-approval-gate
make vet
make test-race RACE_SCOPE=root
make test-race RACE_SCOPE=all
make test-race RACE_SCOPE=nonroot
make test-race RACE_SCOPE=agent
make test-race RACE_SCOPE=nonagent
(cd llm && go test -race ./registry -count=10)
```

Repeat the focused regressions with these exact commands:

```bash
go test ./cmd/evener-hub \
  -run 'TestHubRelay(SharedSessionAliasesDeliverEachNotificationOnce|RemapRetainsAuthoritativeRouteDuringReplacementRead)$' \
  -count=100
go test ./cmd/evener-tui \
  -run '^TestTUITmuxE2E_CaptureStableDuringStream$' -count=20
go test -race ./cmd/evener-hub \
  -run 'TestHubRelay(SharedSessionAliasesDeliverEachNotificationOnce|RemapRetainsAuthoritativeRouteDuringReplacementRead)$' \
  -count=100
go test -race ./cmd/evener-tui \
  -run '^TestTUITmuxE2E_CaptureStableDuringStream$' -count=10
```

Before final acceptance, compare current-base uncached and cached benchmark
trees with the identical harness and collect three new eligible CI attempts on
one exact head/base/merge identity. Compare their medians against every design
threshold. The historical cohort is context only; it is not evidence for the
rebased workload.

On the exact candidate head, also run `(cd llm && go test ./registry -run '^$'
-bench '^BenchmarkProviderCatalogViews$' -benchmem -count=5)` in the same
machine/toolchain session as the cached-load benchmark. Record its median time
and bytes as percentages of the cached-load values and apply the 35%, 45%, and
8 MiB bounds. Rerun this after every rebase.

Before starting the cohort, define `EVALUATED_SHA` as the pushed head containing
all code and committed documentation. Make no further commits while collecting
evidence; any commit invalidates the cohort and restarts Task 6. Record the
exact benchmark SHAs, machine, medians, heap totals, functional/race results,
CI head/base/merge identity, run and attempt numbers, calculated job medians,
critical path, runner total, and exclusions only in the PR description so the
evaluated SHA remains immutable. The PR author gathers the evidence; the
approving reviewer verifies the identities and arithmetic before merge.

The PR author also adds an explicit `Provider()` ownership-change notice to the
PR description. This repository has no release-note or changelog file; record
that fact in the PR description and do not invent a new release surface.

These commands are the reusable verification contract. If the branch is later
rebased onto a newer `main`, rerun them on the new exact head before pushing.

## Historical result

The accepted evaluation run `33471000122` passed all functional and performance
thresholds:

- parsed-source speedup: 4.30x;
- retained heap increase: 5.57 MiB;
- root job median: 250s;
- end-to-end non-root required-check median: 217s;
- race critical-path median: 250s;
- total race runner-time median: 588s;
- peak race workers: 3.

This historical cohort accepted the original cache-plus-matrix topology. It is
not acceptance evidence for the rebased SHA; Task 6 requires fresh benchmark,
memory, functional, and three-attempt CI evidence.
