# Plan 13 — Structural refactors for super-linear fuzz coverage

**Status: PROPOSED (2026-06-30).** Author: Bot, with Jesse. Follows Plan 12
(which raised fuzz-reachable coverage via per-function harnesses: llm 52%, agent
29.5%, root 9.8% as of `46514302`). Point-in-time design doc.

## Why this plan

The harness sweep works but is **linear**: one harness per function/cluster. The
four lanes already hit the ceiling where the next functions need a *seam* the code
doesn't have — the image-request builders capped below 100% refusing `os.ReadFile`;
the apilog write path and the provider clients need a file/network fake. To get
*super-linear* leverage we change the **structure** so fuzzing stops being blocked,
or so one harness covers thousands of functions. Five workstreams, ordered by
dependency.

Coverage today is **fuzz-reachable**, a minority of the well-tested whole (llm
fuzz 52% vs full-suite 86%). The goal of this plan is to drive fuzz-reachable
*toward* full-suite by removing the structural blockers, not by writing 280 more
harnesses by hand.

---

## WS1 — Inject the four effect seams (FOUNDATIONAL; do first)

Almost all un-fuzzable code touches one of four effects. serf injects two well;
finish the set.

| Effect | serf state | What to add |
| --- | --- | --- |
| Clock | **done** — `agent/internal/clock`, advanceable fake | thread it into the remaining direct `time.Now()` sites the lanes worked around |
| LLM network | **done** — `llm.ProviderAdapter` + stub adapters | — |
| **HTTP (non-LLM + provider transport)** | not abstracted — adapters hold an `*http.Client`; the hub holds clients | an injectable `HTTPDoer` (`Do(*http.Request)`), so a provider's full `Complete/Stream` runs over a **fake transport returning fuzzed wire bytes** |
| **Filesystem** | not abstracted — direct `os.ReadFile/WriteFile/MkdirAll` (TaskStore.save, apilog write, config/credential/plugin/skill loaders) | a `Filesystem` interface (or `io/fs` + a writable fake), so the fs-bound config/store/discovery code fuzzes against an in-memory tree |
| **Process spawn** | not abstracted — MCP server spawn, subagent spawn, shell exec | a `ProcessSpawner`/exec seam, so the MCP manager, subagent orchestration, and shell-tool arg handling fuzz without real processes |
| Randomness | partly (ID minting) | finish where determinism matters |

**Highest-leverage first: the `HTTPDoer`.** It unlocks fuzzing the *entire*
provider round-trip (request build → transport → response/stream decode) end to
end from one harness per provider — the single biggest reachable block, and the
exact wall lane 2 hit. **Filesystem** is second (unblocks the fs-bound logic lanes
1 & 4 couldn't reach). Spawn and the last randomness sites are a later batch.

This is a **production refactor**, not test-only — it changes real APIs. Design
the interface and get agreement before threading it broadly (see Execution).

## WS2 — Entry-point ("front door") fuzzers (cash in WS1)

Once the seams exist, stop fuzzing 280 functions and fuzz the handful of front
doors that cover them transitively. serf already proves this pattern works
(`FuzzLifecycleSeq` = whole session machine; `FuzzAppWireDispatch` = all 46 RPC
handlers; `FuzzWebHandler`). Add/deepen:

- **Provider round-trip** (needs WS1 HTTPDoer): one harness per provider drives
  `llm.Request → adapter over a fake transport → Response`, covering request build
  + transport + decode together. The highest-yield new front door.
- **Agent turn loop**: extend `FuzzLifecycleSeq` with the now-seamed fs/spawn so it
  drives real config load, MCP, and subagent paths (all faked) instead of stubbing
  around them.
- **Hub request handler / CLI action path**: broaden `FuzzAppWireDispatch` /
  `FuzzWebHandler` and add a CLI `argv → action` fuzzer with fs/spawn faked.

Each front-door harness moves the coverage number far more per harness than a
leaf-function harness.

## WS3 — Differential pairs (INDEPENDENT, cheap, continuous)

Every bug serf actually *found* came from a differential oracle, and a differential
needs **no hand-written oracle** — "these two agree" is the oracle. Build one
wherever two paths should match:

- existing: cross-provider, stream-vs-nonstream, encode∘decode, the conformance
  goldens.
- new, serf-concrete: an optimized path vs a naive reference (the lanes already
  wrote reference implementations — e.g. jobstore's `OutputMatcher` vs the hand
  reference splitter; token estimation fast path vs a recompute); old-version vs
  new (golden snapshots); two providers' request builders for the same logical
  request.

No WS1 dependency. Highest bug-found-per-effort. Run continuously alongside
everything else.

## WS4 — Functional core / imperative shell (OPPORTUNISTIC, highest quality)

The root cause of low fuzz-reachability is **logic tangled with effects**
(read → parse → decide → write). Extract the pure decision into a pure function;
leave a thin I/O shell. The core fuzzes with **zero seams**; the shell is too thin
to need fuzzing. serf's decoders are already this shape — that's *why* they fuzz
well. Push it into the business logic (agent turn decisions, hub routing decisions,
provider request assembly).

Apply **opportunistically** whenever touching a tangled high-value function — NOT a
big-bang rewrite. It compounds: every extracted core is permanently fuzzable and
every future harness over it is cheaper.

## WS5 — Generation + oracle infrastructure (PARALLEL investment)

The two costs of a harness are *generating a valid input* and *writing the oracle*.
Drive both toward zero:

- **Auto-generate every public input type.** Extend `fuzz/typegen`+`schemagen`
  (reflect → valid value) to cover `llm.Request`/`Response`, the config types, the
  task/plugin/mcp types. `FuzzWireTypes` already proves a generic driver over 99
  types; generalize it.
- **Oracle combinator library.** `RoundTrip(enc,dec)`, `Deterministic(f)`,
  `Idempotent(f)`, `Preserves(f, measure)`, `AgreesWith(f, g)`. A new harness
  becomes ~3 lines, so the per-function approach scales to the whole codebase
  cheaply (and makes WS3 differentials one-liners).

No WS1 dependency. Pure additive infra.

---

## Sequencing

1. **WS1 seams — HTTPDoer then Filesystem** (design-first, then thread). Unblocks
   the most and is the precondition for the highest-value WS2 front door.
2. **WS2 entry-point fuzzers** over the new seams — where the number jumps.
3. **WS3 differentials + WS5 infra** run in PARALLEL from day one (no WS1 dep).
4. **WS4 functional-core** is continuous/opportunistic, not a phase.

## Execution strategy (what fans out vs what doesn't)

- **WS1 is production refactoring** — design the interface, get Jesse's sign-off,
  then the *threading* can fan out (worktree-isolated, one subsystem per lane). The
  interface design itself is NOT a blind fan-out.
- **WS2** fans out once WS1 lands (one front door per lane).
- **WS3 and WS5** fan out immediately (worktree-isolated; WS5 first so WS3 lanes
  use the combinators).
- **WS4** is inline in normal work.
- All parallel *editing* lanes use `isolation: "worktree"` (the Plan-12 lesson — a
  non-isolated lane wrote to the wrong tree and another's destructive git command
  risk). Lanes report registry lines + any prod-seam need; the parent serializes
  `scripts/run-fuzz.sh`, `go.mod`, and the floors.

## Decisions to resolve before WS1

- **D1 — seam interface style.** Explicit interfaces threaded through call sites
  (clear, but API churn) vs a context-carried environment struct (less churn, more
  magic) vs build-tag-swapped implementations (zero prod-API change, but two code
  paths). The Clock used explicit injection; recommend matching it (explicit
  interface, constructor-injected) for consistency and testability.
- **D2 — WS1 scope/order.** Confirm HTTPDoer + Filesystem first; spawn + randomness
  as a second batch.
- **D3 — WS4 aggressiveness.** Opportunistic-only (recommended) vs a dedicated
  extraction pass on the few worst tangled hotspots.

## Risks / honest caveats

- WS1 touches load-bearing production code (the HTTP transport, every fs write). It
  needs careful review and good test coverage *of the refactor itself* — ironically
  the thing it enables. Stage it behind the existing tests + a differential
  (old-path vs seamed-path) so the refactor can't silently change behavior.
- The whole-module fuzz % will keep reading low next to full-suite even after this
  — that's correct (UI/CLI/glue isn't fuzz's job). Track fuzz/full ratio, not the
  bare number (Plan 12's `--with-full`).
- Don't let WS5 infra become gold-plating — build the combinators the WS3/WS2 lanes
  actually need, not a speculative framework.

## Anchors

- Seams to add against: `llm/providers/*/adapter.go` (http.Client), `agent/task/
  task_store.go` + `llm/apilog.go` + the config/credential/plugin loaders (fs),
  `agent/internal/mcp` + subagent spawn + shell tool (process). Existing seams to
  match: `agent/internal/clock`, `llm.ProviderAdapter`.
- Front doors: `agent` session loop (`FuzzLifecycleSeq`), `cmd/serf-hub`
  (`FuzzAppWireDispatch`/`FuzzWebHandler`), provider `Complete/Stream`.
- Infra: `fuzz/typegen`, `fuzz/schemagen`, `FuzzWireTypes`; differentials in
  `llm/providers/difftest`.
