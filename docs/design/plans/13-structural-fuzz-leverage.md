# Plan 13 — Structural refactors for super-linear fuzz coverage

**Status: READY (2026-07-01) — decisions resolved, execution kickoff defined.**
Author: Bot, with Jesse. Follows Plan 12
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
| **HTTP (non-LLM + provider transport)** | not abstracted — adapters hold an `*http.Client` | **no new interface** — make the `*http.Client` (or its `http.RoundTripper`) injectable; fuzz passes a client with a fake `Transport` returning fuzzed wire bytes, so a provider's full `Complete/Stream` runs end to end |
| **Filesystem** | not abstracted — direct `os.ReadFile/WriteFile/MkdirAll` (TaskStore.save, apilog write, config/credential/plugin/skill loaders) | **adopt Afero (`afero.Fs`)** — production `afero.NewOsFs()`, fuzz `afero.NewMemMapFs()` wrapped in `afero.NewBasePathFs` to sandbox against path escape; threaded through the fs-touching code |
| **Process spawn** | not abstracted — MCP server spawn, subagent spawn, shell exec | a **tiny bespoke interface** (the 1–2 `os/exec` methods actually used), discovered from the call sites |
| Randomness | partly (ID minting) | finish where determinism matters |

**Design taste (D1, resolved):** the guiding rule is Pike's "the bigger the
interface, the weaker the abstraction / discover interfaces, don't design them" and
Francia's "reuse the established abstraction, let testability drive it." So: **reuse
the stdlib seam where one exists** (HTTP needs *no new type* — inject the
`*http.Client`/`http.RoundTripper`; reads can take `io/fs.FS`), and **invent only
tiny bespoke interfaces** for boundaries with no stdlib fit (process spawn). The one
place a full library beats a hand-rolled interface is the **filesystem**: rather
than a bespoke `Filesystem`, adopt **Afero** — it's Francia's os-mirroring drop-in
built for exactly this (fake the FS in tests), with `NewMemMapFs` for fuzzing and
`NewBasePathFs` to sandbox writes against escape. That's the deliberate exception to
"a little copying beats a little dependency," justified because serf's fs-write
surface is broad and Afero is the canonical, stable answer.

**Order (D2, resolved):** HTTP + Filesystem first — the *demonstrated* need (the
capped harnesses lanes 1/2/4 couldn't push past); spawn + the last randomness sites
are a second batch.

This is a **production refactor**, not test-only — it changes real constructors.
**Stage each seam behind a differential** (WS3): a fuzzer that runs the same inputs
through the old hard-coded path and the injected-seam path and asserts identical
behavior, so the refactor provably changes nothing. Land the seam + its differential
together, one subsystem at a time.

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

## Decisions (RESOLVED 2026-07-01)

- **D1 — seam interface style: explicit constructor injection; reuse stdlib where
  it exists, adopt Afero for the filesystem, invent tiny bespoke interfaces only
  where nothing fits.** No context-carried environment struct, no build-tag swaps
  (both hide the seam / add magic). Concretely: HTTP = inject `*http.Client` /
  `http.RoundTripper` (no new type); reads = `io/fs.FS`; **filesystem writes =
  `afero.Fs`** (production `NewOsFs`, fuzz `NewMemMapFs`+`NewBasePathFs`); process
  spawn = a ≤2-method bespoke interface discovered from usage.
- **D2 — order: HTTP + Filesystem (Afero) first**, spawn + randomness second. Driven
  by demonstrated need (the capped harnesses), not speculation.
- **D3 — functional core: opportunistic-only.** Extract a pure core whenever we
  touch a tangled high-value function; no standalone big-bang extraction program.

## First cut (execution kickoff)

Grounded in the actual surface (surveyed 2026-07-01): **~25 prod packages call
`os.WriteFile/MkdirAll/Rename/...` directly** (agent, internal/jobstore,
internal/credentials, providercfg, cmd/serf-hub/internal/launchconfig+hubcore,
cmdutil, selfupdate, …) — broad, which is what settles the Afero-vs-bespoke call in
Afero's favor. **~10 provider adapters each hold an `*http.Client`** (openai,
anthropic, google, openaicompat, kimi_anthropic, minimax, ollama) — a bounded
injection surface. Afero is **not yet a dependency**.

Start with two thin end-to-end slices that prove the pattern before fanning out:

1. **Afero slice — `agent/task` + `agent/internal/jobstore` + `internal/credentials`.**
   Add `github.com/spf13/afero`; give each store a constructor-injected `afero.Fs`
   (default `NewOsFs()`); replace the direct `os.*` calls. Differential guard: a
   fuzzer that runs the same store operations against a real temp-dir `NewOsFs` and a
   `NewMemMapFs`, asserting byte-identical persisted state — proves the refactor is
   behavior-preserving *and* is itself the new in-memory fuzz harness.
2. **HTTP slice — the `openai` adapter.** Make its `*http.Client` injectable (no new
   type). Add one entry-point fuzzer: fuzzed wire bytes → a fake `http.RoundTripper`
   → the adapter's full `Complete`/`Stream` → `Response`, asserting the request built
   is identical to the pre-refactor path and the decode never panics.

Each slice = one worktree-isolated lane, seam + differential landed together, ~1
subsystem. Once the pattern is proven, fan out the remaining fs packages and
adapters. In PARALLEL (no dependency): kick off WS5 (typegen-for-every-input +
oracle combinators) and WS3 (differential pairs — starting with the reference
implementations the Plan-12 lanes already wrote, e.g. jobstore `OutputMatcher`).

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

- Seams: HTTP → inject `*http.Client` in `llm/providers/*/adapter.go` (~10; no new
  type). Filesystem → `afero.Fs` (`github.com/spf13/afero`) threaded through the ~25
  `os.*`-writing packages, starting with `agent/task`, `agent/internal/jobstore`,
  `internal/credentials`. Process → a tiny bespoke exec interface at
  `agent/internal/mcp` + subagent spawn + shell tool. Existing seams to match:
  `agent/internal/clock`, `llm.ProviderAdapter`.
- Front doors: `agent` session loop (`FuzzLifecycleSeq`), `cmd/serf-hub`
  (`FuzzAppWireDispatch`/`FuzzWebHandler`), provider `Complete/Stream`.
- Infra: `fuzz/typegen`, `fuzz/schemagen`, `FuzzWireTypes`; differentials in
  `llm/providers/difftest`.
