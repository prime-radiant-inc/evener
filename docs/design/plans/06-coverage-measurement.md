# 8.6 — Coverage measurement — implementation plan

**Status:** PLANNED. **Date:** 2026-06-28. **Branch:** `wip/fuzzing-toolkit`.
**Charter:** [`fuzzing-toolkit-design.md`](../fuzzing-toolkit-design.md) §8.6 (roadmap item D). **Depends on:** Phases 0–5 (built) — specifically `scripts/run-fuzz.sh` (the `TARGETS` array), the `make fuzz` / `fuzz-nightly` wiring, and `GO_MODULES` in the `Makefile`.

## 0. Goal & non-goals

**Goal.** The honesty tool. Today `make fuzz` proves every target's corpus *runs* clean; it says nothing about *how much of each parse surface the corpus actually exercises*. Without that number, "we fuzzed the codebase" is a claim no one can check. This item produces, for each of the 10 fuzz targets, the line coverage its committed corpus drives into its system-under-test (SUT) package, rolls those up into a per-surface report, and — the part that matters most — emits a **gap map**: which decode/parse packages have *zero* fuzz coverage.

**Non-goals (now).** Coverage-guided *search* metrics (that's `-fuzz`'s internal job, not a profile we consume). Coverage of the JS renderer (jstest isn't gated — design §0). Branch/path coverage (Go `-coverprofile` is statement coverage; that is what we report). Making coverage a blocking gate on day one — it ships **advisory**, with the floor wired but off by default (§4).

**Guiding distinction.** "Fuzzed nominally" (a target exists and its corpus is green) vs "actually exercised" (the corpus reaches N% of the SUT). §8.6 exists to make the second number visible. A surface with a target but 3% coverage is barely fuzzed; a parse package with *no* target is the real hole.

## 1. What "surface coverage" means here, and the exact command

"Surface coverage" = the fraction of statements in a target's SUT package that are executed when that target's **committed corpus** (the `f.Add` seeds plus any saved `testdata/fuzz/<FuzzName>/` crashers) is replayed deterministically.

The load-bearing toolchain fact (verified, this worktree):

```
$ go test -run '^FuzzMessageDecode$' -coverpkg=./appwire -coverprofile=cov.out ./appwire
ok  primeradiant.com/serf/appwire  0.011s  coverage: 14.6% of statements in ./appwire
$ head -1 cov.out
mode: set
```

- **`-run '^Fuzz…$'` with NO `-fuzz`** runs the fuzz target as an ordinary test: it executes the seed corpus + saved `testdata/fuzz` entries as subtests, deterministically, and — because it is a normal test run — writes a normal `-coverprofile`. This is exactly the corpus `make fuzz` already replays (`Makefile:101`), now instrumented.
- **`-fuzz '^Fuzz…$'`** is the *search* mode (`scripts/run-fuzz.sh:62`). Its coverage is libFuzzer-style internal instrumentation used to steer mutation; it is **not** emitted as a consumable per-line `-coverprofile`. We deliberately do **not** use `-fuzz` for measurement. (Assumption to keep honest: we measure the *committed corpus*, not the theoretical reachability of the target. After a `fuzz-nightly` run grows `testdata/fuzz`, re-measuring will show higher coverage — that is correct and intended.)
- **`-coverpkg`** is required because by default `go test -coverprofile` attributes coverage only to the package under test. For most targets the test lives *in* its SUT package (`package appwire`, `package llm`, `package main` for the hub), so default attribution already lands on the SUT. The exception is **`FuzzToolArgsValidate`** (`agent/tool_args_fuzz_test.go`, `package agent`), whose real SUT is the validate seam in `agent/internal/tool`; it needs `-coverpkg=./internal/tool,.` to attribute coverage to the tool package rather than to `agent` alone.

**go.work does not span modules** (design §2.5; `go test ./...` is per-module). Every command therefore `cd`s into the target's go.work module before running. The 10 targets span three modules — `.` (root), `llm`, `agent` (not `auth`/`fuzz`, which hold no targets). The per-module commands, one per `run-fuzz.sh` `TARGETS` entry:

| module | package | target | coverage command (run from module dir) |
|---|---|---|---|
| `llm` | `.` | `FuzzParseSSE` | `go test -run '^FuzzParseSSE$' -coverpkg=. -coverprofile=P .` |
| `.` | `./appwire` | `FuzzMessageDecode` | `go test -run '^FuzzMessageDecode$' -coverpkg=./appwire -coverprofile=P ./appwire` |
| `.` | `./appwire` | `FuzzMethodParams` | `go test -run '^FuzzMethodParams$' -coverpkg=./appwire -coverprofile=P ./appwire` |
| `agent` | `.` | `FuzzToolArgsValidate` | `go test -run '^FuzzToolArgsValidate$' -coverpkg=./internal/tool,. -coverprofile=P .` |
| `llm` | `./providers/openai` | `FuzzOpenAIResponsesMetamorphic` | `go test -run '^FuzzOpenAIResponsesMetamorphic$' -coverpkg=./providers/openai -coverprofile=P ./providers/openai` |
| `llm` | `./providers/openai` | `FuzzOpenAIChatCompletionsMetamorphic` | same package, `-run '^FuzzOpenAIChatCompletionsMetamorphic$'` |
| `llm` | `./providers/anthropic` | `FuzzAnthropicStreamMetamorphic` | `-coverpkg=./providers/anthropic` |
| `llm` | `./providers/google` | `FuzzGeminiStreamMetamorphic` | `-coverpkg=./providers/google` |
| `llm` | `./providers/openaicompat` | `FuzzOpenAICompatStreamMetamorphic` | `-coverpkg=./providers/openaicompat` |
| `.` | `./cmd/serf-hub` | `FuzzWebHandler` | `go test -run '^FuzzWebHandler$' -coverpkg=./cmd/serf-hub -coverprofile=P ./cmd/serf-hub` |

(`P` = a per-target profile path in a temp dir, e.g. `$out/llm__FuzzParseSSE.cov`.)

## 2. Aggregation, reporting, and the gap map

### 2.1 Where the tooling lives

Two pieces, mirroring the existing "small Go lint tool driven by a Makefile target" pattern (`cmd/serf-namingcheck`, `cmd/serf-internalcheck`, `cmd/serf-docscheck`):

- **`scripts/fuzz-coverage.sh`** — orchestrator. Enumerates targets, runs each module's coverage command into a temp profile dir, then invokes the reporter. ~60–90 LoC.
- **`cmd/serf-fuzzcov/main.go`** (root module) — reporter. Parses the per-target profiles, merges per package, computes per-package %, runs the gap-map scan, prints the report, and (with `--check`) exits non-zero on a floor breach. ~120–180 LoC.

A Go reporter (not awk) because it has to parse `mode: set` blocks, roll statements up by package, and walk the source tree for the gap scan — all of which are tedious and error-prone in shell, and the repo already standardises on Go for this class of check.

### 2.2 One source of truth for the target list

`TARGETS` lives in `scripts/run-fuzz.sh` (lines 22–33) as `"module:package-relpath:FuzzName"`. To avoid a second drifting copy (the design's anti-duplication rule), **extend that one array and re-expose it**:

1. Add an optional 4th field, `coverpkg`, to each entry: `"module:pkg:name[:coverpkg]"`. `run-fuzz.sh`'s parse becomes `IFS=: read -r module pkg name cover` — backward compatible (3-field entries leave `cover` empty; `run-fuzz.sh` ignores it). Only `FuzzToolArgsValidate` needs it set (`agent:.:FuzzToolArgsValidate:./internal/tool,.`); every other entry defaults `coverpkg` to its `pkg`.
2. Add a `--list` flag to `run-fuzz.sh` that prints the `TARGETS` array verbatim and exits. `fuzz-coverage.sh` consumes `scripts/run-fuzz.sh --list` instead of redefining the list.

This keeps the campaign runner and the coverage runner reading the identical target set; adding a target in one place updates both.

### 2.3 Merging profiles across modules

Each profile is full-import-path qualified (`primeradiant.com/serf/appwire/jsonrpc.go:113.x,…` vs `primeradiant.com/serf/llm/sse.go:…`) and all use `mode: set`. Merging is therefore concatenation with one header, deduping identical blocks and taking the **union** of covered statements (a block is covered if *any* target's corpus hit it; under `mode: set` count is 0/1, so union = max). No `gocovmerge`/`go tool covdata` dependency is needed — those target binary (`GOCOVERDIR`) coverage, not text profiles. The reporter must assert every profile's first line is `mode: set` and refuse to merge mixed modes.

The reporter groups blocks by package (the import path up to the last `/<file>.go`) and reports both:
- the **merged** per-package number (the union across all targets — "is this package fuzzed at all, and how much"), and
- the **per-target** number (attributing each package's coverage to the target(s) that produced it).

### 2.4 Report shape

```
FUZZ SURFACE COVERAGE  (committed corpus, deterministic replay)

  TARGET                                SUT PACKAGE                         LINES
  FuzzParseSSE                          llm                                  41.2%   (covered by 1 target)
  FuzzMessageDecode                     appwire                              14.6%
  FuzzMethodParams                      appwire                              22.0%
  FuzzToolArgsValidate                  agent/internal/tool                  18.7%
  FuzzOpenAIResponsesMetamorphic        llm/providers/openai                 …
  …                                                                          …

GAP MAP — decode/parse packages with ZERO fuzz coverage
  primeradiant.com/serf/agent/schema          (Unmarshal/Decode found, no fuzz target)
  primeradiant.com/serf/providercfg           (providers.toml decode, no fuzz target)
  …
```

The percentages are deliberately the *whole-package* numbers, with the caveat in §5/§Risks that a narrow decoder target legitimately scores low on a big package.

### 2.5 The gap map — enumerating decode/parse packages

The honest part. Build the universe of "parse surfaces" and subtract the fuzzed ones.

- **Universe:** every package (across all `GO_MODULES`) that contains a decode/parse function. Enumerate by scanning non-test `.go` for the signatures that mark a wire-decode seam: `func … UnmarshalJSON`, `func … UnmarshalText`, `json.Unmarshal(` / `json.NewDecoder(`, `func Parse…`, `func …Decode…`, and `toml.Decode`/`toml.Unmarshal`. Map each hit to its package import path; dedupe. (This is a heuristic, not a proof — see Risks. It is tuned to over-include rather than miss, so the gap map errs toward flagging.)
- **Fuzzed set:** the packages that appear (with ≥1 covered statement) in the merged profile.
- **Gap = Universe − Fuzzed.** Print each gap package with the signature that put it in the universe, so the reader can judge whether it warrants a target. This is the artifact that makes "fuzz the whole codebase" auditable: it names every parse package no corpus touches.

The reporter ships an allow-list (a small committed file, e.g. `scripts/fuzzcov-ignore.txt`) for packages deliberately out of scope (e.g. test helpers, generated code) so the gap map stays signal, not noise.

## 3. The `make fuzz-coverage` target

```makefile
# fuzz-coverage replays every fuzz target's COMMITTED corpus under -coverprofile
# (no -fuzz, so deterministic), merges the profiles, and reports per-surface line
# coverage plus the gap map (decode/parse packages with zero fuzz coverage).
# Advisory by default; pass CHECK=1 to fail on a floor breach (see §4).
fuzz-coverage:
	@scripts/fuzz-coverage.sh $(FUZZCOV_ARGS)
```

Mirrors the existing `fuzz-nightly` wiring (`Makefile:106-107`, `fuzz-nightly: ; @scripts/run-fuzz.sh $(FUZZ_ARGS)`). Add `fuzz-coverage` to the `.PHONY` line. It is **not** added to `make test`/`test-race`/the CI gate initially — it runs on demand and in nightly (§8.7's job can call it). The `cmd/serf-fuzzcov` binary, being a normal package under the root module, is already gated by `make test`/`vet`/`lint` like the other `cmd/serf-*check` tools.

## 4. Gating — advisory first, floor later

Two floors, both **off by default**:

1. **Per-target floor:** flag any target whose SUT-package coverage is below a threshold `T` (a target that compiles and runs but barely exercises its surface). Because a narrow decoder in a large package scores legitimately low (appwire's frame decoder = 14.6% of the *whole* appwire package; §5), the floor is most defensible as a **focus-set** percentage rather than whole-package — see §5 — or as a *regression* check (coverage must not drop vs a committed baseline) rather than an absolute bar. Start advisory; pick the mode with Jesse (§6).
2. **Gap floor:** flag if any package in the gap map (minus the ignore-list) is non-empty — i.e. a new parse package landed with no fuzz target. This is the more valuable gate and the cheaper one to make blocking, because "did anyone add a decoder without fuzzing it" is unambiguous.

`scripts/fuzz-coverage.sh --check` (wired as `make fuzz-coverage CHECK=1`) makes the reporter exit non-zero on a breach; without it, the report prints and exits 0. Roll-out: ship advisory, watch a few nightly runs to set realistic thresholds, then flip `--check` on in the nightly job (§8.7) — not the PR gate — first.

## 5. The decoder-vs-package precision problem

`FuzzMessageDecode` exercising 14.6% of `appwire` is **not** a 14.6%-bad result — the package also contains the client, transport, ping/recv, and router glue that a *frame decoder* target has no business covering. Reporting whole-package % alone would make every narrow target look under-performing and make any absolute floor meaningless.

Mitigation, simplest first:
- **Default:** report whole-package %, and document plainly that low numbers for a narrow decoder are expected. The number's job is comparison over time and spotting *zeros*, not grading.
- **Optional focus set:** let a `TARGETS` entry declare the file(s) or function(s) that *are* the seam (e.g. `appwire/jsonrpc.go`), and have the reporter compute a second "focus %" over just those. This is the number a per-target floor should bind to. Defer building this until §6 settles whether we want an absolute floor at all (YAGNI — the gap map is the high-value output; per-target % is secondary).

## 6. Open questions for Jesse

1. **Threshold values.** What's `T` for the per-target floor — and should the floor be an *absolute* whole-package %, an absolute *focus-set* % (requires building the focus-set machinery, §5), or a *no-regression-vs-baseline* check (simplest, no magic number)? My lean: no-regression baseline + the gap floor; skip absolute per-target thresholds until we have data.
2. **Advisory vs blocking, and where.** Ship advisory (agreed in the charter). When it flips: gap floor blocking in the **nightly** job first (§8.7), never the PR gate? Or also gate PRs once stable? My lean: gap floor → nightly blocking soon; per-target % → advisory indefinitely.
3. **What corpus counts.** Measure only the *committed* corpus (deterministic, reproducible — recommended), or also fold in the nightly `testdata/fuzz` cache before it's committed? My lean: committed only, so the number is reproducible from a clean checkout.
4. **Gap-map ignore-list policy.** Who curates `scripts/fuzzcov-ignore.txt`, and does an entry require a reason comment? My lean: yes, reason required, reviewed like any code.

## 7. Build steps, size, dependencies, risks, acceptance

### 7.1 Build steps (suggested order)
1. `run-fuzz.sh`: add the optional 4th `coverpkg` field to `TARGETS` (only `FuzzToolArgsValidate` sets it) and a `--list` flag. (~15 LoC; verify `make fuzz-nightly` still runs unchanged.)
2. `cmd/serf-fuzzcov/main.go`: profile parser + `mode: set` merge (union) + per-package rollup + report printer. (~80 LoC)
3. `cmd/serf-fuzzcov`: gap-map scan (signature grep → package set; subtract fuzzed set; apply ignore-list) + `--check` exit logic. (~60–100 LoC)
4. `scripts/fuzz-coverage.sh`: consume `run-fuzz.sh --list`, run each module's coverage command into a temp dir, call `serf-fuzzcov`. (~60–90 LoC)
5. `Makefile`: `fuzz-coverage` target + `.PHONY`.
6. `fuzz/README.md`: document `make fuzz-coverage` next to `make fuzz` / `fuzz-nightly`.

### 7.2 Size & dependencies
- **~200–280 LoC total** (within the charter's 150–300).
- **No new third-party deps** — `go test -coverprofile`, `go tool cover` (for spot-checks), and stdlib parsing only. Merge is hand-rolled over `mode: set`; no `gocovmerge`/`covdata`.
- Depends on the built Phase 0–5 targets and `run-fuzz.sh`'s `TARGETS` being the single source of truth.

### 7.3 Risks
- **Decoder vs whole package** (verified: 14.6%). A narrow target scores low on a big package → an absolute per-target floor is misleading. Mitigation §5 (whole-package for visibility; focus-set or no-regression for any actual gate).
- **Merging across modules.** Safe *because* profiles are full-import-path qualified and uniformly `mode: set`; concatenate + union. Risk only if some run emits a different mode — the reporter must assert `mode: set` and refuse mixed modes rather than silently miscount.
- **Flaky coverage from nondeterministic branches.** Corpus inputs are fixed, so the statement *set* is stable for pure decoders. But `FuzzParseSSE`'s metamorphic oracle spins a timeout goroutine, and map-iteration / time-dependent branches could touch a different statement run-to-run. Mitigation: `mode: set` (presence, not count) damps most of it; for a `--check` gate, measure over the **union of two runs** (or accept a small tolerance band) so a one-statement wobble can't flip the gate.
- **Gap-map heuristic over/under-reach.** The signature grep can miss a decoder behind an indirection (a custom `Reader`) or over-include a package that only re-marshals. Mitigation: tune signatures toward over-inclusion + the curated ignore-list; the gap map is a prompt for human judgement, not a proof.
- **`-coverpkg` blow-up.** A wide `-coverpkg` (e.g. `./...`) instruments far more than the SUT and slows the run. Keep `coverpkg` tight to the SUT package(s) per target.

### 7.4 Acceptance
- `make fuzz-coverage` prints **one line per fuzz target** with its SUT package and corpus line-coverage %, plus a **gap map naming every decode/parse package with zero fuzz coverage**.
- The 10 lines correspond exactly to `run-fuzz.sh`'s `TARGETS` (no hand-maintained second list; verified by `run-fuzz.sh --list` driving the run).
- Re-running is reproducible from a clean checkout (committed corpus only); the appwire decode target reports ~14.6% as a sanity anchor.
- `make fuzz-coverage CHECK=1` exits non-zero when (a) the gap map is non-empty beyond the ignore-list, or (b) a target breaches its configured floor — and exits 0 otherwise.
- `make fuzz`, `make fuzz-nightly`, and the existing gate are unchanged.
