# 8.6 — Coverage measurement — implementation plan

**Status:** PLANNED. **Date:** 2026-06-28. **Branch:** `wip/fuzzing-toolkit`.
**Charter:** [`fuzzing-toolkit-design.md`](../fuzzing-toolkit-design.md) §8.6 (roadmap item D). **Depends on:** Phases 0–5 (built) — specifically `scripts/run-fuzz.sh` (the `TARGETS` array, lines 22–33), the `make fuzz` / `fuzz-nightly` wiring (`Makefile:101-107`), `GO_MODULES` (`Makefile:79`), and the existing PR gate `.github/workflows/ci.yml`.

> **Decisions folded in (Jesse, 2026-06-28) — settled, do not re-litigate.** The eventual goal is **PERFECT (100%) coverage**; every primary metric must therefore be drivable to 100%.
> 1. **Build the focus-set machinery now** (do *not* defer). Attribute coverage only to the decode/parse functions actually under test per target, so each surface's % is meaningful and can be driven to 100%. Whole-package % stays as a secondary visibility number only.
> 2. **The gate is a no-regression ratchet on the focus-set %** (a surface's coverage may never decrease), plus the absolute focus-set % displayed climbing toward 100, plus the **gap map** (any decode/parse package with ZERO fuzz coverage). Raise the floors over time as a ratchet.
> 3. **The corpus measured is the COMMITTED corpus only** (reproducible from a clean checkout). Per 8.4/8.7 the committed corpus now *includes* the minimized, diversity-capped, coverage-expanding inputs that local fuzz searches discover (discovered corpus is committed into `testdata/fuzz/<FuzzName>/`, not left in the cache). So the number reflects all committed seeds + discovered corpus + crashers.
> 4. **Enforcement is advisory in the local coverage tool now.** There is **no** scheduled CI / nightly (per 8.7 — everything is local, on-demand, plus the existing PR gate). When the gap floor is promoted to blocking it goes into the **existing `ci.yml` PR gate**, not a nonexistent nightly. Per-target focus-set % stays advisory.
> 5. **Gap-map ignore-list:** each entry requires a reason comment and is reviewed like code.

## 0. Goal & non-goals

**Goal.** The honesty tool, pointed at 100%. Today `make fuzz` proves every target's corpus *runs* clean; it says nothing about *how much of each parse surface the corpus actually exercises*. Without that number, "we fuzzed the codebase" is a claim no one can check. This item produces, for each of the 10 fuzz targets, a **focus-set coverage %** — the line coverage its committed corpus drives into the specific decode/parse seam it is meant to fuzz — drivable to 100%; rolls those up into a per-surface report alongside a secondary whole-package number; ratchets each focus-set % so it can never regress; and emits a **gap map**: which decode/parse packages have *zero* fuzz coverage.

**Non-goals (now).** Coverage-guided *search* metrics (that is `-fuzz`'s internal job, not a profile we consume). Coverage of the JS renderer (jstest isn't gated — design §0). Branch/path coverage (Go `-coverprofile` is statement coverage; that is what we report). A scheduled nightly job (per decision 4 — there is none; this tool is local + the PR gate). Making the per-target focus-set % a *blocking* gate on day one — it ships advisory (§4); only the no-regression ratchet and the gap floor are candidates for blocking, and only via `ci.yml`.

**Guiding distinction.** "Fuzzed nominally" (a target exists and its corpus is green) vs "actually exercised to N% of its seam, climbing to 100%". §8.6 makes the second number visible *and* drivable. A surface whose focus set is at 30% is 70% un-fuzzed work remaining; a parse package with *no* target is the real hole the gap map names.

## 1. What "focus-set coverage" means here, and the exact command

**Focus-set coverage** (the primary metric) = the fraction of statements in a target's declared **focus set** — the file(s) and/or function(s) that *are* the decode/parse seam under test — that are executed when that target's **committed corpus** (the `f.Add` seeds plus the saved `testdata/fuzz/<FuzzName>/` entries, which now include harvested/discovered corpus per 8.4/8.7) is replayed deterministically. Because the focus set is exactly the seam, 100% is a real, reachable target: every statement in the seam should be coverable by some input.

**Whole-package coverage** (secondary, visibility only) = the same replay, but the fraction of statements in the target's whole SUT package. Reported for context (and to spot zeros), never gated — a narrow decoder legitimately scores low against a big package (§5).

The load-bearing toolchain fact (verified, this worktree):

```
$ go test -run '^FuzzMessageDecode$' -coverpkg=./appwire -coverprofile=cov.out ./appwire
ok  primeradiant.com/serf/appwire  coverage: 14.6% of statements in ./appwire
$ head -1 cov.out
mode: set
```

- **`-run '^Fuzz…$'` with NO `-fuzz`** runs the fuzz target as an ordinary test: it executes the seed corpus + saved `testdata/fuzz` entries as subtests, deterministically, and — because it is a normal test run — writes a normal `-coverprofile`. This is exactly the corpus `make fuzz` already replays (`Makefile:101-102`), now instrumented.
- **`-fuzz '^Fuzz…$'`** is the *search* mode (`scripts/run-fuzz.sh:62`). Its coverage is libFuzzer-style internal instrumentation used to steer mutation; it is **not** emitted as a consumable per-line `-coverprofile`. We deliberately do **not** use `-fuzz` for measurement. We measure the *committed corpus*. Because discovered coverage-expanding inputs are committed into `testdata/fuzz` (decision 3, per 8.4/8.7), growing the corpus is exactly how the focus-set % is driven up toward 100 — and the gain is captured the moment those inputs are committed, reproducibly from a clean checkout.
- **`-coverpkg`** is required because by default `go test -coverprofile` attributes coverage only to the package under test. For most targets the test lives *in* its SUT package (`package appwire`, `package llm`, `package main` for the hub), so default attribution already lands on the SUT. The exception is **`FuzzToolArgsValidate`** (`agent/tool_args_fuzz_test.go`, `package agent`), whose real SUT is the validate seam in `agent/internal/tool` (verified: that package exists — `agent/internal/tool/{definitions,registry,apply_patch}.go`); it needs `-coverpkg=./internal/tool,.` to attribute coverage to the tool package rather than to `agent` alone. The focus-set filter (§2.4) then narrows that profile to the validate function(s).

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

## 2. Aggregation, reporting, the focus set, and the gap map

### 2.1 Where the tooling lives

Two pieces, mirroring the existing "small Go lint tool driven by a Makefile target" pattern (`cmd/serf-namingcheck`, `cmd/serf-internalcheck`, `cmd/serf-docscheck`):

- **`scripts/fuzz-coverage.sh`** — orchestrator. Enumerates targets, runs each module's coverage command into a temp profile dir, then invokes the reporter. ~60–90 LoC.
- **`cmd/serf-fuzzcov/main.go`** (root module) — reporter. Parses the per-target profiles, computes each target's **focus-set %** and its whole-package %, runs the gap-map scan, enforces the **ratchet** against the committed floors file, prints the report, and (with `--check`) exits non-zero on a regression / gap breach. ~140–200 LoC (larger than the original estimate because the focus-set machinery and ratchet are now built, not deferred).

A Go reporter (not awk) because it has to parse `mode: set` blocks, resolve focus functions to line ranges via `go/parser`, roll statements up, and walk the source tree for the gap scan — all tedious and error-prone in shell, and the repo already standardises on Go for this class of check.

### 2.2 One source of truth for the target list — plus the focus set

`TARGETS` lives in `scripts/run-fuzz.sh` (lines 22–33) as `"module:package-relpath:FuzzName"` (verified; the parse at line 59 is `IFS=: read -r module pkg name`). To avoid a second drifting copy (the design's anti-duplication rule), **extend that one array and re-expose it**:

1. Add two optional trailing fields: `coverpkg` and `focus` → `"module:pkg:name[:coverpkg[:focus]]"`. `run-fuzz.sh`'s parse becomes `IFS=: read -r module pkg name cover focus` — backward compatible (3-field entries leave `cover`/`focus` empty; `run-fuzz.sh` ignores both). `coverpkg` defaults to `pkg`; only `FuzzToolArgsValidate` sets it (`:./internal/tool,.`). `focus` defaults to "whole package" (every `.go` file in the SUT package) when empty.
2. The `focus` field is a `;`-separated list of focus specs, each either a file (`jsonrpc.go`, relative to the SUT package) or a function (`jsonrpc.go#decodeMessage`). The reporter resolves function specs to line ranges with `go/parser` and filters coverage blocks to the focus files/ranges.
3. Add a `--list` flag to `run-fuzz.sh` that prints the `TARGETS` array verbatim and exits. `fuzz-coverage.sh` consumes `scripts/run-fuzz.sh --list` instead of redefining the list.

This keeps the campaign runner and the coverage runner reading the identical target set; adding a target in one place updates both. Because `:` is the field separator and `coverpkg` values can contain `,` but never `:`, no quoting hazard is introduced; the `focus` list uses `;` internally for the same reason.

**Declaring focus sets (turnkey).** The implementer sets each target's `focus` to the file(s)/function(s) that are its decode/parse seam — the code a corpus *should* be able to drive to 100%. Start with the file holding the entry function the `Fuzz…` body calls (e.g. `appwire`'s frame decoder for `FuzzMessageDecode`), confirm by reading the target body, and narrow to `file.go#Func` only if the file mixes seam and non-seam code. A focus set that still contains genuinely-unreachable statements (e.g. an `impossible` default panic) is the signal to either narrow the focus or add a corpus input — never to lower the goal below 100%.

### 2.3 Merging profiles across modules (for the secondary number and the gap map)

Each profile is full-import-path qualified (`primeradiant.com/serf/appwire/jsonrpc.go:113.x,…` vs `primeradiant.com/serf/llm/sse.go:…`) and all use `mode: set`. Merging is therefore concatenation with one header, deduping identical blocks and taking the **union** of covered statements (a block is covered if *any* target's corpus hit it; under `mode: set` count is 0/1, so union = max). No `gocovmerge`/`go tool covdata` dependency is needed — those target binary (`GOCOVERDIR`) coverage, not text profiles. The reporter must assert every profile's first line is `mode: set` and refuse to merge mixed modes.

The reporter groups blocks by package (the import path up to the last `/<file>.go`) and reports the **merged** per-package number (the union across all targets — "is this package fuzzed at all, and how much") for the gap map and the secondary visibility column.

### 2.4 Computing the focus-set % (primary)

For each target the reporter takes that target's own profile (not the merged union), keeps only the blocks whose file is in the target's focus set (and, for `file.go#Func` specs, whose line range falls inside the resolved function), and computes `covered_stmts / total_stmts` over just those blocks. That is the number that must climb to 100 and that the ratchet binds to. Resolving function ranges: parse the SUT package source with `go/parser`/`go/ast`, map each focus `#Func` to its `FuncDecl` start/end lines, and keep blocks within. (~40–60 LoC including the file-only fast path.)

### 2.5 Report shape

```
FUZZ SURFACE COVERAGE  (committed corpus, deterministic replay — goal: 100%)

  TARGET                                FOCUS SET                          FOCUS %   FLOOR    PKG %
  FuzzParseSSE                          sse.go#ParseSSE                      68.0% ↑   60.0%   41.2%
  FuzzMessageDecode                     jsonrpc.go (decode)                  91.3% ↑   88.0%   14.6%
  FuzzMethodParams                      jsonrpc.go (params)                  77.0% =   77.0%   22.0%
  FuzzToolArgsValidate                  internal/tool (validate)            …
  FuzzOpenAIResponsesMetamorphic        providers/openai (stream)          …
  …                                                                        …

  (↑ above floor — ratchet will rise; = at floor; FOCUS % < FLOOR fails --check)

GAP MAP — decode/parse packages with ZERO fuzz coverage
  primeradiant.com/serf/agent/schema          (Unmarshal/Decode found, no fuzz target)
  primeradiant.com/serf/providercfg           (providers.toml decode, no fuzz target)
  …
```

**FOCUS %** is primary (drivable to 100). **FLOOR** is the committed ratchet value (§4). **PKG %** is the secondary whole-package number, retained for visibility/zero-spotting only.

### 2.6 The gap map — enumerating decode/parse packages

The honest part. Build the universe of "parse surfaces" and subtract the fuzzed ones.

- **Universe:** every package (across all `GO_MODULES` — `.`, `agent`, `llm`, `auth`, `fuzz`; `Makefile:79`) that contains a decode/parse function. Enumerate by scanning non-test `.go` for the signatures that mark a wire-decode seam: `func … UnmarshalJSON`, `func … UnmarshalText`, `json.Unmarshal(` / `json.NewDecoder(`, `func Parse…`, `func …Decode…`, and `toml.Decode`/`toml.Unmarshal`. Map each hit to its package import path; dedupe. (Heuristic, not a proof — see Risks. Tuned to over-include, so the gap map errs toward flagging.)
- **Fuzzed set:** the packages that appear (with ≥1 covered statement) in the merged profile.
- **Gap = Universe − Fuzzed.** Print each gap package with the signature that put it in the universe, so the reader can judge whether it warrants a target. This is the artifact that makes "fuzz the whole codebase" auditable: it names every parse package no corpus touches.

**Ignore-list (decision 5).** The reporter reads a committed `scripts/fuzzcov-ignore.txt`. Each line is one package import path **followed by a reason comment** (`<import-path>  # <reason>`); the reporter requires the reason and errors on a bare entry, so the file is reviewed like code. Used for genuinely out-of-scope packages (test helpers, generated code). Entries keep the gap map signal, not noise.

## 3. The `make fuzz-coverage` target

```makefile
# fuzz-coverage replays every fuzz target's COMMITTED corpus under -coverprofile
# (no -fuzz, so deterministic), computes each target's FOCUS-SET coverage %
# (primary, drivable to 100%) plus its whole-package % (secondary), enforces the
# no-regression ratchet against scripts/fuzzcov-floors.txt, and prints the gap
# map (decode/parse packages with zero fuzz coverage). Advisory by default; pass
# CHECK=1 to fail on a ratchet regression or a gap breach (see §4).
fuzz-coverage:
	@scripts/fuzz-coverage.sh $(FUZZCOV_ARGS)
```

Mirrors the existing `fuzz-nightly` wiring (`Makefile:106-107`, `fuzz-nightly: ; @scripts/run-fuzz.sh $(FUZZ_ARGS)`). Add `fuzz-coverage` to the `.PHONY` line (`Makefile:1`). It is **not** added to `make test`/`test-race` initially — it runs on demand locally. The `cmd/serf-fuzzcov` binary, being a normal package under the root module, is already gated by `make test`/`vet`/`lint` like the other `cmd/serf-*check` tools.

## 4. Gating — ratchet + gap floor; advisory now, blocking via `ci.yml` later

Per decision 4 there is **no nightly**; enforcement is the local tool now and the existing PR gate later. Two enforceable conditions, both surfaced by `make fuzz-coverage CHECK=1` (wired as `scripts/fuzz-coverage.sh --check`, which makes the reporter exit non-zero on a breach; without it the report prints and exits 0):

1. **No-regression ratchet on the focus-set % (decision 2).** A committed floors file `scripts/fuzzcov-floors.txt` maps each target → its current focus-set % floor. `--check` fails if any target's focus % drops below its floor. The floors are a **ratchet**: a `--bless` mode rewrites each floor *upward* to the current measured % (and refuses to lower any floor), so as the corpus grows (decision 3) and focus % climbs toward 100, the floor climbs with it and locks the gain in. Raising floors over time is the mechanism that drives the campaign to perfect coverage. The absolute focus % is always displayed next to the floor so progress toward 100 is visible.
2. **Gap floor (decision 4).** Fail if any package in the gap map (minus the ignore-list) is non-empty — i.e. a new parse package landed with no fuzz target. This is the highest-value, least-ambiguous gate: "did anyone add a decoder without fuzzing it."

**Per-target absolute focus % stays advisory** (decision 1/4): the report shows it climbing to 100, but no fixed threshold blocks. The *ratchet* (it may not go down) and the *gap floor* are the blocking conditions.

**Where blocking lives (decision 4).** When promoted to blocking, the gap floor (and, once floors are trustworthy, the ratchet) is added as a step to the **existing `.github/workflows/ci.yml`** PR gate — modeled on the existing `serf-*check` steps (`ci.yml:28-35`), e.g. a `make fuzz-coverage CHECK=1` step after the lint steps. There is **no** nightly/`schedule:` workflow to put it in (verified: none exists in `.github/workflows/`). Roll-out: ship advisory (local only); once the floors and gap map are stable, add the `CHECK=1` step to `ci.yml`.

## 5. The decoder-vs-package precision problem — resolved by the focus set

`FuzzMessageDecode` exercising 14.6% of *all of* `appwire` is **not** a 14.6%-bad result — the package also contains the client, transport, ping/recv, and router glue that a *frame decoder* target has no business covering. The focus set (decision 1) is the resolution: by attributing coverage only to the decode seam, the primary % becomes a meaningful, drivable-to-100 number, while the 14.6% whole-package figure is kept solely as a secondary visibility column (and a zero-spotter). This is why the focus-set machinery is built now, not deferred: without it there is no metric that can honestly be driven to 100%.

## 6. Open questions — resolved

All four prior open questions are settled by Jesse's decisions (top of file). Recorded here for traceability:

1. **Threshold / floor mode.** Resolved (decision 2): **no-regression ratchet on the focus-set %** + the gap floor, with floors raised over time toward 100. No fixed absolute per-target threshold; per-target absolute % is advisory.
2. **Advisory vs blocking, and where.** Resolved (decision 4): advisory in the local tool now; **no nightly exists**; when blocking, the gap floor (then the ratchet) goes into the **existing `ci.yml` PR gate**.
3. **What corpus counts.** Resolved (decision 3): the **committed corpus only** — which now includes the minimized, diversity-capped, discovered coverage-expanding inputs committed into `testdata/fuzz` (per 8.4/8.7). Reproducible from a clean checkout.
4. **Gap-map ignore-list policy.** Resolved (decision 5): every entry requires a reason comment and is reviewed like code; the reporter errors on a reasonless entry.

## 7. Build steps, size, dependencies, risks, acceptance

### 7.1 Build steps (suggested order)
1. `run-fuzz.sh`: add the optional trailing `coverpkg` and `focus` fields to `TARGETS` (only `FuzzToolArgsValidate` sets `coverpkg`; set a sensible `focus` per target) and a `--list` flag. (~20 LoC; verify `make fuzz-nightly` still runs unchanged — the extra fields are ignored by the 3-field consumers.)
2. `cmd/serf-fuzzcov/main.go`: profile parser + `mode: set` merge (union) + per-package rollup + report printer (focus %, floor, pkg %). (~80 LoC)
3. `cmd/serf-fuzzcov`: focus-set machinery — parse `focus` specs, resolve `file.go#Func` to line ranges via `go/parser`, compute per-target focus %. (~40–60 LoC)
4. `cmd/serf-fuzzcov`: ratchet (`scripts/fuzzcov-floors.txt` read + `--bless` upward-only rewrite) + gap-map scan (signature grep → package set; subtract fuzzed set; apply reason-required ignore-list) + `--check` exit logic. (~70–100 LoC)
5. `scripts/fuzz-coverage.sh`: consume `run-fuzz.sh --list`, run each module's coverage command into a temp dir, call `serf-fuzzcov`. (~60–90 LoC)
6. `Makefile`: `fuzz-coverage` target + `.PHONY` (`Makefile:1`).
7. `fuzz/README.md`: document `make fuzz-coverage`, the focus-set/ratchet model, and `--bless`, next to `make fuzz` / `fuzz-nightly`.
8. Commit the initial `scripts/fuzzcov-floors.txt` (measured current focus %s as the starting ratchet) and `scripts/fuzzcov-ignore.txt`.

### 7.2 Size & dependencies
- **~280–360 LoC total** — above the charter's 150–300 band because the focus-set machinery and ratchet are built now (decision 1), not deferred. Flagged deliberately; the extra LoC is the cost of a metric drivable to 100%.
- **No new third-party deps** — `go test -coverprofile`, `go tool cover` (spot-checks), and stdlib (`go/parser`, `go/ast`) only. Merge is hand-rolled over `mode: set`; no `gocovmerge`/`covdata`.
- Depends on the built Phase 0–5 targets and `run-fuzz.sh`'s `TARGETS` being the single source of truth.

### 7.3 Risks
- **Focus set drawn too wide → unreachable statements cap the % below 100.** Mitigation: narrow `focus` to `file.go#Func`; if a statement is genuinely unreachable, that is a signal to add a corpus input or tighten the seam, not to lower the goal. The ratchet still protects against regression in the meantime.
- **Merging across modules.** Safe *because* profiles are full-import-path qualified and uniformly `mode: set`; concatenate + union. Risk only if some run emits a different mode — the reporter must assert `mode: set` and refuse mixed modes rather than silently miscount.
- **Flaky coverage from nondeterministic branches.** Corpus inputs are fixed, so the statement *set* is stable for pure decoders. But `FuzzParseSSE`'s metamorphic oracle spins a timeout goroutine, and map-iteration / time-dependent branches could touch a different statement run-to-run. Mitigation: `mode: set` (presence, not count) damps most of it; for the `--check` ratchet, measure over the **union of two runs** (or accept a small tolerance band) so a one-statement wobble can't trip the ratchet.
- **Gap-map heuristic over/under-reach.** The signature grep can miss a decoder behind an indirection (a custom `Reader`) or over-include a package that only re-marshals. Mitigation: tune signatures toward over-inclusion + the curated reason-required ignore-list; the gap map is a prompt for human judgement, not a proof.
- **`-coverpkg` blow-up.** A wide `-coverpkg` (e.g. `./...`) instruments far more than the SUT and slows the run. Keep `coverpkg` tight to the SUT package(s) per target.

### 7.4 Acceptance
- `make fuzz-coverage` prints **one line per fuzz target** with its focus set, **focus-set coverage % (primary, with its ratchet floor)** and whole-package % (secondary), plus a **gap map naming every decode/parse package with zero fuzz coverage**.
- The 10 lines correspond exactly to `run-fuzz.sh`'s `TARGETS` (no hand-maintained second list; verified by `run-fuzz.sh --list` driving the run).
- Re-running is reproducible from a clean checkout (committed corpus only, including committed discovered corpus); the appwire decode target's whole-package number reports ~14.6% as a sanity anchor.
- `make fuzz-coverage CHECK=1` exits non-zero when (a) any target's focus % drops below its committed ratchet floor, or (b) the gap map is non-empty beyond the reason-required ignore-list — and exits 0 otherwise. `--bless` raises floors upward only.
- A reasonless entry in `scripts/fuzzcov-ignore.txt` makes the reporter error.
- `make fuzz`, `make fuzz-nightly`, and the existing `ci.yml` gate are unchanged until the gap floor is deliberately promoted into `ci.yml`.
</content>
</invoke>
