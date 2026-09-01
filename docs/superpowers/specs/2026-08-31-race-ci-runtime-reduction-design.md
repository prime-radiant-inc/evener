# Race CI Runtime Reduction Design

**Date:** 2026-08-31

## Problem

The permanent race-detector jobs are among the slowest required CI lanes. A
three-run baseline on `main` measured these medians:

| Lane | Job median | Test-step median |
| --- | ---: | ---: |
| `race-root` | 584s | 563s |
| `race-modules` | 371s | 352s |

Setup consumes seconds rather than minutes. The root lane repeatedly parses the
same embedded provider catalog and curated overlay through `registry.Load`.
The non-root lane launches independent module processes on one runner, where
the long `agent` module defines the lane while `llm` and the smaller modules
compete for the same CPU.

## Goals

1. Remove repeated parsing of immutable embedded registry inputs while keeping
   every registry's supported mutable runtime state independent.
2. Run `agent` separately from the other non-root modules using native GitHub
   Actions jobs.
3. Preserve every non-fuzz module, the `-race -short -count=1` contract, and the
   stable required check names `race-root` and `race-modules`.
4. Keep default tests deterministic and offline.
5. Use existing Go, Make, and GitHub Actions mechanisms only.

## Non-goals

- Do not remove tests, relax timeouts, or shard packages by test name.
- Do not increase in-process agent parallelism under `-race`.
- Do not add larger runners, a custom scheduler, or a timing service.
- Do not cache a loaded `Registry`; loaded registries remain caller-owned.
- Do not add a second timed test run to required CI.

## Design

### Cache parsed embedded inputs

`registry.Load` continues to allocate a fresh `Registry` for every call. Only
the immutable compiled-in catalog and overlay receive process-wide parsed
forms, loaded through `sync.OnceValues`.

The catalog cache contains providers plus snapshot metadata. The overlay cache
contains the parsed overlay layer. Both stay unexported and read-only. Each load
still creates its own user config, injected instances, environment and
credential results, warnings, records, rankings, live-listing map and mutex,
and refresh state.

`WithInstances` remains caller-owned option input rather than supported mutable
registry state. `Load` isolates injected membership and runtime state, but does
not recursively snapshot the reference-valued fields of a supplied `Provider`;
the exported option comment requires callers to keep those values immutable
while a loaded registry is in use. A caller that needs different injected data
constructs a new value and loads a new registry.

`Provider`, `Layer`, `Caps`, and `Resolved` contain reference-valued fields.
The existing `Resolved` API contract in `types.go` remains read-only: callers
must copy its reference-valued fields before mutation. `Provider()` has no such
contract, so it returns a recursively owned copy of its provider, model,
transport, and capability data. Mutating that view cannot change the registry
that returned it or a later registry loaded from the process-wide parsed
snapshot. Internal load and resolve paths also must not mutate the cached
parsed values; tests compare their serialized form before and after repeated
concurrent loads and resolutions.

This is source-compatible but observably changes ownership behavior: reads
return the same data, while mutating a returned `Provider` no longer mutates
registry state. The repository consumer audit covers the CLI provider/model
commands and the hub auth/instance listing paths; every call reads fields and
none uses the return value to modify the registry. The new ownership contract
is stated in the exported Go API comment. We intentionally provide no mutation
API or compatibility layer because registry changes flow through configuration
and reload, not undocumented aliases. The PR description must call out the
ownership change for external module consumers. This repository has no
release-note or changelog surface, so the implementation does not invent one;
the PR description is the release communication artifact.

Input selection remains independent:

- Without `WithSnapshot`, or with `WithSnapshot(nil)`, the embedded snapshot
  supplies the initial catalog and its metadata. A non-nil empty slice is a
  supplied custom snapshot and fails parsing rather than selecting defaults.
- A non-nil `WithSnapshot` parses the supplied snapshot as the initial catalog
  with zero snapshot metadata.
- Unless `WithoutCache` is present, a valid runtime cache whose `fetched_at` is
  newer than the selected snapshot metadata replaces either initial catalog.
  Consequently, any valid dated runtime cache beats a custom snapshot; this is
  existing behavior pinned in `refresh_test.go`.
- Without `WithOverlay`, or with `WithOverlay(nil)`, the embedded overlay is
  used. A non-nil `WithOverlay` parses the supplied overlay for that load; an
  empty but non-nil overlay is a valid empty custom overlay.
- A corrupt runtime cache preserves the warning and embedded fallback.
- `WithoutCache` skips runtime-cache selection, so the selected custom or
  embedded snapshot remains authoritative for that load.

`EmbeddedOverlay()` returns an owned byte slice so callers cannot mutate the
package-global source used by the parsed cache. A valid runtime catalog is
parsed once within each `Load` call and reused between validation and selection
in that call. Runtime cache files are never cached across `Load` calls, so a
later load observes a refreshed or replaced file.

`sync.OnceValues` also retains an initialization error. That is intentional:
the inputs are compiled into the binary and cannot recover during the process
lifetime, so retrying the same invalid embedded bytes would add work without a
recovery path.

### Preserve ownership with behavioral tests

Tests cover the contracts that make parsed-source caching safe:

- separate loads do not share live listings or injected instances;
- values returned by `Provider()` can be mutated without affecting the same or
  a later registry, including nested maps, slices, and pointers;
- custom snapshots and overlays bypass their corresponding defaults;
- returned embedded overlay bytes are independently owned;
- concurrent default loads resolve the same records under `-race` without
  sharing mutable state;
- existing runtime-cache fallback and warning behavior remains unchanged.

`BenchmarkLoadEmbeddedDefaults` measures the optimization without introducing
a wall-clock assertion into the default suite.

### Split non-root modules with native CI jobs

Make retains the existing scopes and adds two derived scopes:

```make
RACE_MODULES_all := $(GO_MODULES)
RACE_MODULES_root := .
RACE_MODULES_nonroot := $(filter-out .,$(GO_MODULES))
RACE_MODULES_agent := $(filter agent,$(GO_MODULES))
RACE_MODULES_nonagent := $(filter-out . agent,$(GO_MODULES))
```

`RACE_SCOPE=nonroot` remains the local aggregate reproduction. CI uses a
two-entry `agent`/`nonagent` matrix with `fail-fast: false` and
`max-parallel: 2`. The existing `race-modules` name becomes an `if: always()`
aggregator that succeeds only when every matrix entry succeeds.

Both scopes derive from `GO_MODULES`, and `make test-race` rejects an empty
selection. The workflow keeps `WEB=0`, `AGENT_SHARDS=0`, and
`AGENT_PARALLEL=`; the change adds runner-level concurrency without increasing
test-level concurrency.

## Validation and acceptance

The change must pass:

- `make generate`, `make lint`, `make vet`, and `make test`;
- `make test-race` for `all`, `root`, `nonroot`, `agent`, and `nonagent`;
- repeated focused registry ownership tests under `-race`;
- repeated hub remap, alias-delivery, and TUI capture regressions found while
  evaluating CI;
- three eligible CI attempts on one exact head/base/merge identity for the
  initial performance acceptance.

Selector verification covers seven cases: the five valid scopes `all`, `root`,
`nonroot`, `agent`, and `nonagent`; one unknown scope; and one forced empty
derived scope.

Performance acceptance requires:

| Measure | Threshold |
| --- | ---: |
| Embedded-load speedup | at least 2x |
| Retained heap increase | at most 24 MiB |
| Owned full-catalog provider view | at most 35% of cached-load time, 45% of cached-load bytes, and 8 MiB/op |
| Root job median | below 360s |
| End-to-end non-root required-check median | below 270s |
| Race critical-path median | below 480s |
| Total race runner-time median | at most 1,132s |
| Peak race workers | at most 3 |

These thresholds are one-time acceptance criteria for the final reconstructed
head. The benchmark, memory profiles, and three-attempt CI cohort must be
collected again after rebasing because changes elsewhere on `main` can change
the race workload even when the cache and workflow files are unchanged.
Ongoing monitoring is manual because the required jobs intentionally contain
no timing assertion. The author of a future change to this cache, topology, or
the packages on either lane must remeasure the affected benchmark or job
medians, record them in that change's PR, and have the approving reviewer check
them against these thresholds. Ordinary race jobs remain the correctness and
coarse timeout backstop.

## Current reconstructed-head evidence

The fresh current-base benchmark compares exact adjacent revisions on base
`08559860b08282e212e91bba1d94641b0b4898e6`: uncached
`140e3a396b918797655f6afdf9106ff3e49f008c` contains the ownership behavior
and benchmark harness, and cached
`390ab245c39e1d98691019ce8bf701586ba3ebe9` adds only parsed-source caching.
Both ran on Darwin arm64 on an Apple M5 Max with the commands below.

| Measure | Uncached | Cached | Result |
| --- | ---: | ---: | ---: |
| Median load time | 35.24ms | 8.27ms | 4.26x faster |
| Median allocated bytes | 33,907,431 B/op | 11,240,324 B/op | 66.9% fewer |
| Median allocations | 225,897 | 69,728 | 69.1% fewer |
| Retained heap | 211.38 KiB | 5,941.27 KiB | +5.60 MiB |

At benchmark commit `5d7285bf66f0bb60b8ab4511ae144b9fe0a14d14`,
`BenchmarkProviderCatalogViews` reads all 212 curated providers through the
owned `Provider()` API and through the former shallow-reference behavior in the
same process. Five samples measured an owned-view median of 1.87ms and
4,153,562 B/op versus 6.33us and 0 B/op for the shallow reference. On the same
machine and toolchain, that is 22.6% of the cached-load median and 36.9% of its
allocated bytes. The relative increase over the shallow reference is expected
because the old path copied nothing; the normalized and 8 MiB absolute bounds
both pass.

```bash
cd llm
go test ./registry -run '^$' \
  -bench '^BenchmarkProviderCatalogViews$' -benchmem -count=5
```

At the time this document was committed, the local benchmark and memory
criteria above passed. Exact-candidate functional/race verification and the
three-attempt exact-identity CI cohort remained pending. Their commands,
results, head/base/merge identity, run attempts, medians, critical path, runner
total, and any eligible startup exclusions are recorded in the PR description
without changing the evaluated Git SHA.

## Historical evidence

The baseline used three successful `ubuntu-latest` workflow runs:

| Run | Commit | Root job | Non-root job |
| --- | --- | ---: | ---: |
| `33420943036` | `1e09b5c4c` | 584s | 359s |
| `33424893416` | `6c6d5395f` | 480s | 371s |
| `33427813648` | `1d5d9e210` | 588s | 399s |

The originally accepted candidate identity was head
`b80f458ff2b3101d97513027993724a9e484df84`, event base
`82531f43e07ae43ea89ec7f21fe8c906101d38cb`, and event merge
`064138c7c2499204b7dbd3eb2531dc3af4956196`. Run `33471000122`
attempts 1, 2, and 3 were the first three eligible successes; no startup
failure exclusion was used. Jobs ran on `ubuntu-latest`.

That three-attempt evaluation run `33471000122` measured:

| Attempt | Root job | Agent job | Non-agent job | Critical path | Runner total |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 264s | 213s | 109s | 264s | 588s |
| 2 | 249s | 226s | 133s | 249s | 611s |
| 3 | 250s | 207s | 125s | 250s | 586s |
| **Median** | **250s** | **213s** | **125s** | **250s** | **588s** |

The `race-modules` aggregator took 2s, 3s, and 4s. End-to-end non-root
required-check latency, from the first shard start through aggregator
completion, was 217s, 232s, and 214s, with a 217s median. The root worker still
completed later on every attempt, so the overall critical-path values above are
unchanged. Runner totals include the aggregator exactly once.

The historical parsed-source benchmark improved from a 32.85ms median to
7.65ms, a 4.30x speedup. Retained heap increased by 5.57 MiB, and allocation
count fell from approximately 225,884 to 69,713 per load. Every acceptance
threshold passed.

The uncached benchmark revision was
`4d91d0f03968c4c00ff192fd48e3d04a0097315d`; it contains the ownership tests
and benchmark harness but not parsed-source caching. The cached revision was
`59fbdb99b258c459531154b006b89d888748d065`, the immediately following cache
implementation using the identical harness. Both used the same clean toolchain
and these commands:

```bash
cd llm
go test ./registry -run '^$' \
  -bench '^BenchmarkLoadEmbeddedDefaults$' -benchmem -count=5
go test ./registry -run '^$' \
  -bench '^BenchmarkLoadEmbeddedDefaults$' -benchtime=1x -count=1 \
  -memprofile=heap.pprof -memprofilerate=1
go tool pprof -sample_index=inuse_space -top heap.pprof
```

The timing value is the median `ns/op` across five samples. Heap retention is
the after-minus-before total `inuse_space` from the one-iteration post-GC
profiles. Critical path is the interval from the earliest race worker start
through the later of root completion or `race-modules` aggregator completion.
Runner total is the sum of root, agent, nonagent, and aggregator job durations,
including setup and teardown.

The baseline deliberately uses the last three successful `main` runs before
the experiment rather than one repeated revision. None changed registry loading
or race topology, and all used the same checked-in Go/toolchain setup and
`ubuntu-latest` workflow label. The mutable hosted image is not controllable;
three samples plus thresholds requiring large improvements reduce that noise.
Each candidate cohort uses one exact identity. Attempts are considered in
order; only a GitHub `startup_failure` before tests run is excludable, with at
most two exclusions and five total attempts. Test failures, cancellations after
startup, and ambiguous results terminate the sample rather than being
discarded.

## Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| Cached parsed values are mutated | Keep them unexported, return owned overlay bytes, and test concurrent independent loads under `-race`. |
| Caller mutates reused `WithInstances` input | Document the input as immutable while loaded registries use it; runtime state and membership remain per load. |
| An override accidentally uses a default cache | Select snapshot and overlay sources independently and test each override. |
| Runtime-cache semantics change | Preserve fetched-at selection, warnings, and fallback behavior. |
| A module disappears from CI | Derive scopes from `GO_MODULES`, reject empty scopes, and retain the aggregate local scope. |
| Required check names change | Keep `race-modules` as the stable aggregator. |
| Matrix fail-fast hides failures | Disable fail-fast and require every result in the aggregator. |
| Lower latency spends excessive capacity | Limit the matrix to two entries and cap accepted total runner time and peak workers. |

## Decision

Adopt both changes conditionally. The local speed, memory, and ownership-cost
criteria pass, but final acceptance remains pending until exact-candidate
functional/race verification and the fresh three-attempt CI cohort are recorded
in the PR description and satisfy every remaining limit.
