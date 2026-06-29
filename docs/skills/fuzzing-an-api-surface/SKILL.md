---
name: fuzzing-an-api-surface
description: Use when a surface parses untrusted or model-generated input — a wire protocol, tool-call JSON, upstream SSE, an HTTP body — or after a parser/dispatch bug, to fuzz that surface and turn any deterministic failure into a permanent, flake-guarded regression test. Covers picking a seam, choosing oracles beyond "no panic", the portable promoter/schema-generator core, and the safety bounds that keep a fuzzer from executing dangerous handlers.
---

# Fuzzing an API surface

Use this skill when you want to systematically hammer a surface that decodes
input you do not control, and have every real failure become a named regression
test rather than a transient console reproduction. Serf is the proving ground;
the portable core (`fuzz/promoter`, `fuzz/schemagen`) carries unchanged to any
project, so the methodology below is project-agnostic and the serf targets are
worked examples.

Read `docs/research/api-fuzzing-toolkit.md` (§3 oracle soft spots, §5 the
promote discipline, §8 the portable split) and `docs/design/fuzzing-toolkit-design.md`
before extending the toolkit.

## When to reach for this

A surface is fuzz-ready when it has:

- **A single dispatch choke point** — one function every input flows through
  (`tool.Registry.ExecuteCall`, `Router.Dispatch`, `WebServer.Handler()`). One
  harness then covers the whole surface by varying which operation it drives.
- **A machine-readable schema** — per-operation JSON Schema, reflected param
  types, OpenAPI, protobuf. Schema-driven generation reaches deep validator
  paths raw bytes never hit (serf hands us tool `Definition.Parameters` and the
  `appwire.Methods` catalog for free).
- **A decode / parse seam** — a byte-level frame/stream parser (`llm.ParseSSE`,
  `appwire.Message.UnmarshalJSON`). These are the cheapest, highest-value
  targets: a Go `testing.F` byte fuzzer auto-promotes its own crashers.
- **A metamorphic relationship** — two ways to feed the same logical input that
  must agree (re-chunked reads, reordered/whitespace-padded frames, blocking vs
  timeout code paths). The relationship *is* the oracle.

Do NOT reach for this when the seam's only effect is to **execute** something
dangerous (see Safety bounds), when no determinism is achievable offline (the
real behavior needs a live LLM/network/subprocess), or when there is no oracle
richer than "it returned" — a fuzzer with only a floor oracle on a trivial
surface buys little.

## Core rules (non-negotiable)

- **Safety bound: never let a fuzzer EXECUTE a dangerous handler.** Shell
  (`bash -c`), network fetch, file read/write under attacker-controlled paths,
  subprocess/agent spawn — a temp dir does NOT sandbox these. Fuzz the **decode
  + validate boundary** instead, or restrict execution to a vetted allowlist of
  genuinely sandboxable operations. Serf's `FuzzToolArgsValidate` drives
  decode+`Schema.Validate` and never calls a tool handler; `FuzzWebHandler`
  allowlists GET-only, non-mutating, non-networked routes and excludes spawn,
  shell, and provider-probe endpoints.
- **Determinism is non-negotiable.** Seed the generator; run offline (no real
  LLM, network, or subprocess); a flaky target — or a promoter that *could*
  promote a flake — is a defect, not a nuisance.
- **"No panic" is the floor, never the only oracle.** Invest in semantic
  oracles; the real bugs live there.
- **A thin-but-real model beats a broad fake one.** Model only what you can
  drive deterministically against the real seam, and report the boundary you
  could not reach. Never fake the unreachable part and assert on the mock — that
  tests mocked behavior. `TestRouterSeqFuzz` models exactly the transport
  initialize-gate it can reach offline and states in its doc comment that
  turn/job *status* lifecycle needs a live agent and is out of scope.

## Procedure: pick a surface and seam (research §8, 7 steps)

1. **Pick a surface and its seam.** Find the single choke point all input flows
   through. That is what the harness drives.
2. **Find or derive the schema.** Point generation at whatever machine-readable
   structure exists (`Definition.Parameters`, reflected `appwire.Methods`,
   OpenAPI). No schema → fall back to byte fuzzing.
3. **Write the thinnest `testing.F` first** and harvest the free auto-promotion:
   a single `[]byte` or typed-tuple target whose crashers Go saves to
   `testdata/fuzz/` and re-runs forever. This is ~60–200 LoC and finds the
   crash-class bugs before you build anything else.
4. **Add oracles beyond "no panic"** (next section). This is where valid-but-wrong
   bugs surface.
5. **For stateful surfaces, model the state machine** and drive it with
   property-based testing (`rapid`). Keep the model declarative — serf's
   `liveOpTable` is a transition table, the reusable artifact; the `rapid`
   machine just draws op sequences.
6. **Wire the promoter with flake-guard before promote** so non-`testing.F`
   failures (`rapid` sequences, HTTP op-sequences) become permanent.
7. **Gate it in CI:** seed corpus + saved crashers under `-short` on every run
   (`make fuzz` / `make test`); the unbounded coverage-guided search runs nightly
   (`make fuzz-nightly`, 60s/target).

## The oracle taxonomy

The portable tags are `promoter.OracleTag`: `Panic`, `Invariant`, `ErrorShape`,
`Wedge`, `HTTP5xx`, `PathEscape`. How to spot which fits, each tied to a real
serf target:

- **Floor — never panic** (`Panic`). The seam crashes on some input. Always
  present; never sufficient alone. Live panic-hunt wherever validation is *not*
  `recover()`-wrapped at runtime — e.g. `Schema.Validate` in
  `FuzzToolArgsValidate`, and `Router.Dispatch` (which propagates handler panics)
  in `TestRouterSeqFuzz`.
- **Metamorphic** (`Invariant`). Two equivalent inputs must produce the same
  output. Spot it when one logical input has more than one delivery shape:
  re-chunked reads or blocking-vs-timeout paths (`FuzzParseSSE`), one-byte-at-a-time
  SSE accumulation (`FuzzOpenAIResponsesMetamorphic`), decode→encode→decode fixed
  point (`FuzzMessageDecode`, `FuzzMethodParams`).
- **Divergence** (`ErrorShape`). A validator says clean but the handler
  disagrees, or vice versa. Spot it when validation and use are separate steps:
  `TestToolArgsSchemaFuzz` flags a schema-VALID generated value the real
  validator rejects — generator and schema disagree.
- **Invariant / monotonicity** (`Invariant`). A modeled property must hold across
  a sequence. Spot it on stateful surfaces with an ordering contract:
  `TestRouterSeqFuzz` asserts the connection's initialize-gate is monotonic
  (fresh → initialized, never reverts).
- **Never 5xx** (`HTTP5xx`). Bad input may legitimately 4xx, but a 500 from your
  own handler is a fault. Spot it on HTTP surfaces: `FuzzWebHandler` (it excludes
  the bare stdlib `http.FileServer`, whose `fs.ErrInvalid`→500 is documented
  net/http behavior, not handler logic).
- **Path escape** (`PathEscape`). Attacker-controlled path/query escapes its
  intended root. Spot it on any file/path resolver: `FuzzWebHandler` plants a
  secret one level above the session cwd and fails if any response body contains
  it.
- **Never wedge** (`Wedge`). A call that should return makes no progress. Spot it
  on synchronous seams that could hang: `TestRouterSeqFuzz` runs each dispatch
  under a bounded context in a goroutine and reports a wedge on timeout.
- **Internal invariant** (`Panic`, under `-tags serffuzz`). A load-bearing
  assumption asserted *inside* production code with `invariant.Hold()` so a logic
  bug trips at its origin, not at a distant surface. Zero-cost in a normal build;
  panics under `-tags serffuzz` (the fuzz build), so it surfaces via the Panic
  oracle. Spot it wherever a subsystem relies on a property a reader assumes —
  e.g. a folded job status never leaves a terminal state; an emitted item carries
  its turn id. Conditions must be side-effect-free; verify the invariant is TRUE
  and reached before trusting it.
- **Differential** (`Invariant`/`ErrorShape`). Two code paths that must compute the
  same thing, driven from one input and compared modulo an allow-list. The
  strongest class — it found both real decoder bugs this codebase has caught. Spot
  it wherever a value is produced two ways: per-provider streaming vs non-streaming
  decode (`FuzzStreamVsNonStreamDifferential`), one logical response across all
  providers (`FuzzCrossProviderDifferential`), a decoder vs a committed snapshot of
  its own output (`appwire/golden_test.go`), or independent readers of one format
  (`FuzzTranscriptReadersAgree`). A divergence outside the allow-list is a real
  bug — never widen the allow-list to silence it.

**Generation technique — structure-aware inputs.** Independent of which oracle you
pick: if the surface has a rich grammar, most raw-byte execs die at the first
parse. Drive the target from a deterministic byte-`Source` generator
(`fuzz/schemagen`) that emits *valid-but-adversarial* inputs (the `Fuzz…Structured`
targets), so coverage-guided search reaches deep logic. It typically lifts input
acceptance from ~0% to ~90%+.

## The portable core

Two packages in the `primeradiant.com/serf/fuzz` module are the travelling
tooling. **Nothing in them imports the project under test** — `fuzz/go.mod`
declares no serf dependency, so the module will not build if that boundary is
violated. That structural guarantee *is* the portability test.

**`schemagen.FromJSONSchema(schema map[string]any) *rapid.Generator[any]`** walks
the JSON Schema subset a surface actually uses (`type`/`properties`/`required`/
`enum`/`additionalProperties`/`items`) and yields both schema-`Valid` and
schema-`Adjacent` (wrong-type, missing-required, out-of-enum, extra-when-closed)
values. Use `schemagen.Generator(schema, mode)` when an oracle needs known-valid
input.

**`promoter.Adapter`** is the four-hook seam a project implements; everything else
(flake-guard loop, bucket store, emitter, commit) is generic:

```go
type Adapter interface {
    Minimize(Failure) Failure                                    // usually a passthrough
    Signature(Failure) Signature                                 // dedup key
    Replay(context.Context, Failure) (failed bool, sameSignature bool)
    Emit(Failure) (path string, err error)
}
```

A project implements it by carrying enough state to deterministically reproduce a
failure. `toolArgsAdapter` holds each tool's compiled validator; `seqAdapter`
holds the transition table + handler registrar. `Replay` re-runs the *same* oracle
function that found the failure (`toolArgsFailure` / `seqOracleRun`), so the live
property and the replay classify identically. `Emit` calls
`promoter.WriteGoTest(dir, promoter.GoTest{...})` with an adapter-rendered
`ReplayBody` that decodes the minimized artifact and asserts the bug is fixed.

`promoter.New(adapter, store, quarantiner, K)` then runs `Promote`.

## The flake-guard / dedup rubric (the load-bearing discipline)

`Promoter.Promote` is the whole point. In order:

1. **Dedup first.** `store.Has(sig)` against the committed `BucketStore` (a JSON
   file mapping `Signature` → test path). A known bug returns `AlreadyKnown` and
   is never re-promoted.
2. **Flake-guard.** Replay the minimized artifact **K times** (default 5). Promote
   ONLY if it fails all K with the **same** signature. Any non-reproduction or
   signature shift → `Quarantined`: logged via the `Quarantiner`, **never written
   as a test**. This is the rule that keeps the gate trustworthy; budget it
   generously on timing-sensitive seams.
3. **Emit-only by default.** A surviving failure writes a `TestRegression_*`
   file with a provenance header (`// Code generated by fuzz/promoter ... DO NOT
   EDIT`) and records its bucket. Promotion *into the tree* is the human/opt-in
   step — review the diff before committing.

Signatures must distinguish distinct bugs: `Signature{Oracle, Key}` where for
`Panic` the Key is the top-N normalized stack frames, and for semantic oracles
(`Invariant`/`ErrorShape`) the adapter folds the named invariant / `Detail` into
Key. Collapse them and distinct bugs silently dedup as already-known.

## Hard-won lessons

- **rapid owns its shrink loop.** `pgregory.net/rapid` reports failures through
  `testing.T` and Goexits during shrinking; it does **not** hand you the minimized
  artifact as a return value. Capture the failing artifact into a closure variable
  inside `rapid.Check`, and promote it in `t.Cleanup` (which runs during the Goexit
  unwind). `Adapter.Minimize` is then a passthrough — rapid already minimized. Both
  `TestToolArgsSchemaFuzz` and `TestRouterSeqFuzz` use this `captured *Failure` +
  cleanup pattern.
- **Liberal-in / conservative-out — the fix location matters.** `FuzzMessageDecode`
  found a real codec asymmetry: the decoder accepted an id-less response/error
  frame the encoder then re-rendered unreadable, breaking the round-trip fixed
  point. The fix (commit `6c7f4eac`) was on the **producer** side —
  `Response.MarshalJSON`/`ErrorResponse.MarshalJSON` omit the empty id — keeping
  the decoder liberal. On a transport, tightening a decoder turns a tolerable frame
  into a connection-fatal error; tighten the producer instead.
- **Model only what you can drive for real.** When the real status lifecycle needs
  a live LLM, model the transport/dispatch layer you *can* reach offline and report
  the boundary in the target's doc comment. Do not fabricate the unreachable part.

## Per-language portability

The loop, the triage/promotion discipline, schema-driven generation, and the
oracle taxonomy generalize. Only the engine, corpus format, and emitter syntax
are per-ecosystem, all behind the same `Adapter`:

- **Go:** `testing.F` (coverage-guided, free crasher auto-promotion) for byte/tuple
  targets; `pgregory.net/rapid` for stateful/structured PBT. Emitter writes a
  `*_test.go`; corpus is `f.Add` seeds + `testdata/fuzz/`.
- **JS:** `fast-check` (property/model-based) for a renderer/protocol surface; its
  emitter writes a `test-*.js`.
- **Python:** `hypothesis` (+ `schemathesis` for HTTP); its emitter writes a
  `test_*.py`.

## Cross-module wiring (Go specifics)

A local `v0.0.0` workspace module is not `go get`-able. To consume `fuzz/promoter`
and `fuzz/schemagen` from another module in the same `go.work`:

1. Add `require primeradiant.com/serf/fuzz v0.0.0` (and `pgregory.net/rapid`) to
   the consumer's `go.mod`.
2. Add `replace primeradiant.com/serf/fuzz v0.0.0 => ./fuzz` in `go.work`, then
   `go work sync`.
3. Seed third-party checksums the workspace replace hides:
   `GOFLAGS=-mod=mod GOWORK=off go mod download pgregory.net/rapid`.

Add `fuzz` to `GO_MODULES` so its tests are gated. `make fuzz` runs every
`Fuzz*` target's seed corpus + saved crashers per module as deterministic tests
(no `-fuzz` flag = no random search); `make fuzz-nightly` runs the bounded search.
