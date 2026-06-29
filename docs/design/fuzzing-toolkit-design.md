# Fuzzing + failure-to-regression toolkit — detailed design

> Looking to *use* or *run* the fuzzer rather than understand its design? See the
> developer's guide [`docs/fuzzing.md`](../fuzzing.md). This doc is the architecture.

**Status:** Phases 0–5 BUILT on `wip/fuzzing-toolkit`; Phase 6+ (§8) is the full-coverage roadmap, each item planned under `docs/design/plans/`. **Date:** 2026-06-28. **Branch:** `wip/fuzzing-toolkit` (worktree `.worktrees/fuzzing-toolkit`).
**Builds on:** [`docs/research/api-fuzzing-toolkit.md`](../research/api-fuzzing-toolkit.md) — read that first for prior art, the four API surfaces, and why serf is unusually fuzz-ready. This doc is the *how*: package layout, Go signatures, the promoter internals, and a file-by-file build plan.

## 0. Goal & non-goals

**Goal.** A toolkit that (a) fuzzes serf's API surfaces at unit + integration granularity and (b) turns any *deterministic* failure into a permanent, named regression test — leaning on Go's free corpus auto-promotion where possible and building a promoter only where the failing artifact is a sequence. The portable core (the loop + the flake-guard-before-promote discipline + schema-driven generation + the oracle taxonomy) is later extracted into a general **superpowers skill**; serf is the proving ground.

**Non-goals (now).** External fuzzing infra (OSS-Fuzz/ClusterFuzz). Cross-provider *differential* testing (we do *metamorphic* — research §4). JS renderer fuzzing (jstest isn't gated — defer to Phase 5). Auto-commit without a human-reviewable diff.

**Guiding rules.**
- Every fuzz target is deterministic and fast (Go fuzzing best-practice); seed with `f.Add`.
- "No panic" is the *floor* oracle, never the only one — invest in semantic oracles (research §3 soft spots).
- **Never promote a flake.** A failure earns a regression test only if it reproduces deterministically K times. Non-deterministic failures are quarantined (logged), never committed.

## 1. Package layout

All new code under a single module-spanning tree so each module's `go test` picks up its own targets (go.work does not span modules — research §2):

```
fuzz/                              # NEW top-level dir; doc + shared, module-agnostic libs
  README.md                       # how to run: make fuzz, make fuzz-nightly
  schemagen/                      # schema -> value generator (Phase 1); pure, no serf deps
    schemagen.go                  #   JSONSchema -> rapid.Generator (valid + schema-adjacent)
    schemagen_test.go
  promoter/                       # the failure->regression harness (Phase 3); generic core
    promoter.go                   #   Promoter, Failure, Signature, Bucket, flake-guard loop
    bucket_store.go               #   on-disk bucket store (dedup memory)
    emit_go.go                    #   Go test emitter (text/template)
    promoter_test.go
  corpus/                         # checked-in seed corpora (OWASP fuzz vectors + edge values)
    sse/  appwire/  toolargs/  http/

# Per-surface fuzz targets live NEXT TO the code they test, in that module:
llm/sse_fuzz_test.go                                   # Phase 0 #1
appwire/jsonrpc_fuzz_test.go                           # Phase 0 #2
appwire/params_fuzz_test.go                            # Phase 0 #3 (table over appwire.Methods)
agent/tool_args_fuzz_test.go                           # Phase 0 #4 (package agent: needs NewSession; internal/tool would cycle)
agent/registry_schemafuzz_test.go                      # Phase 1 #5 (same reason)
internal/appserver/router_seqfuzz_test.go              # Phase 2 #6
llm/providers/openai/responses_fuzz_test.go            # Phase 4 #7
cmd/serf-hub/web_fuzz_test.go                           # Phase 4 #8
```

Rationale: `testing.F` targets must be in the package under test (they call unexported seams and their `testdata/fuzz/` corpus lives beside them). The `fuzz/` module holds only the *reusable* pieces (generator, promoter) that have no serf dependency — these become the skill's travelling tooling. `fuzz/` is added to `GO_MODULES` in the Makefile so it's gated.

## 2. Phase 0 — free Go-native wins (do first, ~250–450 LoC)

Four `testing.F` targets. Each: a tiny harness, a handful of `f.Add` seeds, an oracle, and (for free) crashers auto-saved to `testdata/fuzz/` that re-run forever. No promoter needed.

### 2.1 `llm/sse_fuzz_test.go` — SSE frame parser (#1)
Seam: `llm.ParseSSE(ctx, io.Reader, onEvent)` (`llm/sse.go:82`).
```go
func FuzzParseSSE(f *testing.F) {
    f.Add([]byte("data: {\"type\":\"x\"}\n\n"))
    f.Add([]byte("event: delta\ndata: \n\n: comment\n\n"))
    // seed from corpus/sse/*
    f.Fuzz(func(t *testing.T, raw []byte) {
        // Oracle 1 (floor): never panic.
        // Oracle 2 (metamorphic): blocking path vs timeout-goroutine path (the two code
        //   paths over the same bytes, research §3) must yield the same event slice.
        evBlocking := collect(parseBlocking(raw))
        evTimeout  := collect(parseWithTimeout(raw, longTimeout))
        if !reflect.DeepEqual(evBlocking, evTimeout) {
            t.Fatalf("SSE path divergence: %v vs %v", evBlocking, evTimeout)
        }
    })
}
```
Effort ~60–120 LoC. The metamorphic oracle is the real value (a parser bug that only one path hits).

### 2.2 `appwire/jsonrpc_fuzz_test.go` — frame decode (#2)
Seam: `appwire.Message.UnmarshalJSON` (`appwire/jsonrpc.go:113`) + `ID.UnmarshalJSON`. Oracle: never panic; round-trip stability (decode→encode→decode is a fixed point for any input that decodes cleanly); `ID.Int64()`/`String()` never panic on a decoded message. ~50–90 LoC.

### 2.3 `appwire/params_fuzz_test.go` — per-method param decode (#3)
One target, corpus tagged by method. Drive every method in `appwire.Methods` (`appwire/protocol.go:85`): for a fuzzed `(methodIndex, paramsBytes)`, look up the method's `Params` zero value and `json.Unmarshal` the bytes into a fresh copy. Oracle: never panic; a successful decode followed by re-marshal is stable. This exercises all 42 methods' param structs through one harness. ~120–200 LoC.

### 2.4 `agent/internal/tool/registry_fuzz_test.go` — tool-arg decode+validate (#4)
Seam: `Registry.ExecuteCall(ctx, env, llm.ToolCallData{Name, Arguments})` (`agent/internal/tool/registry.go:446`). Build a real registry via `registerCoreTools`. Fuzz `(toolNameIndex, argsBytes)`: pick a registered tool name, set `Arguments = argsBytes`, call `ExecuteCall` with a sandboxed temp `env`.
Oracles: (1) never panic — note `Schema.Validate(args)` is **not** `recover()`-wrapped at runtime (research §3), so this is a live panic-hunt; (2) the *clean-error* oracle — a schema-invalid input must produce a structured tool error, never a partial side effect. Use a read-only / temp-dir exec env so fuzzing can't touch the real FS. ~150–250 LoC.

### 2.5 Make + CI wiring
```makefile
# seed-corpus only (fast, deterministic) — safe for the gate
fuzz:        ; for m in $(GO_MODULES); do (cd $$m && go test -run '^Fuzz' ./...); done
# real fuzzing campaign (nightly / manual), bounded time per target
fuzz-nightly ; ./scripts/run-fuzz.sh --time 60s
```
`go test -run '^Fuzz'` runs each fuzz target's **seed corpus + saved crashers** as ordinary deterministic tests (no `-fuzz` = no random search) — this is what goes in `make test`/CI under `-short`. The unbounded search runs only in `fuzz-nightly`. Add `fuzz` to the `GO_MODULES` loop and the `envvars` caveat from research §10 (either add it to the gate or skip).

## 3. Phase 3 — the auto-promotion harness (do right after Phase 0, ~400–700 LoC)

This is the part Go gives free only for single-input `testing.F`. It turns `rapid`/sequence/HTTP failures (Phases 1/2/4) into permanent tests. Lives in `fuzz/promoter/`, generic, four project-supplied hooks.

### 3.1 Core types
```go
package promoter

// A discovered failure, surface-agnostic.
type Failure struct {
    Surface   string            // "appwire-seq", "toolargs", ...
    Oracle    OracleTag         // Panic | Invariant | ErrorShape | Wedge | HTTP5xx | PathEscape
    Stack     []string          // normalized frames (project-relative), for dedup
    Detail    string            // invariant name / panic message / etc.
    Artifact  json.RawMessage   // the MINIMIZED reproducer (op-list+seed+inputs), hook-defined
}

type OracleTag string
const ( Panic OracleTag="panic"; Invariant OracleTag="invariant"; /* ... */ )

// Signature buckets failures: same bug => same signature. (oracle, topN stack frames).
type Signature struct{ Oracle OracleTag; Frames string } // research §5.3, N≈3–5

// The four project hooks — this IS the portable adapter seam (research §8).
type Adapter interface {
    // Minimize is usually a passthrough — rapid/go-fuzz already minimized. Hook exists
    // for surfaces whose runner doesn't shrink.
    Minimize(Failure) Failure
    // Signature computes the dedup key (lets a project normalize stacks its own way).
    Signature(Failure) Signature
    // Replay rebuilds the seam and re-runs the artifact; returns (failedAgain, deterministic).
    Replay(context.Context, Failure) (failed bool, sameSignature bool)
    // Emit renders + writes the regression test; returns the path written.
    Emit(Failure) (path string, err error)
}
```

### 3.2 The promote decision (the load-bearing discipline)
```go
func (p *Promoter) Promote(ctx context.Context, f Failure) (Outcome, error) {
    f = p.adapter.Minimize(f)
    sig := p.adapter.Signature(f)
    if p.store.Has(sig) { return AlreadyKnown, nil }       // dedup vs committed buckets
    // FLAKE-GUARD: replay K times; promote ONLY if it fails all K with the same signature.
    for i := 0; i < p.K; i++ {                              // K≈5
        failed, same := p.adapter.Replay(ctx, f)
        if !failed || !same { return Quarantined, p.log.Quarantine(f, i) }
    }
    path, err := p.adapter.Emit(f)                          // deterministic TestRegression_...
    if err != nil { return 0, err }
    p.store.Add(sig, path)
    return Promoted, nil                                   // commit is the caller's choice (opt-in flag)
}
```
`Quarantined` failures are logged with their artifact, never written as a test. This is the rule that keeps the gate trustworthy (research §10, top risk). The store (`bucket_store.go`) is a small JSON file under `fuzz/promoter/buckets.json` mapping signature→test-path, so reruns skip known bugs.

### 3.3 Go emitter (`emit_go.go`)
A `text/template` producing:
```go
// Code generated by fuzz/promoter from a fuzzing failure. DO NOT EDIT by hand.
// Surface: {{.Surface}}  Oracle: {{.Oracle}}  Signature: {{.SigShort}}
func TestRegression_{{.Surface}}_{{.Oracle}}_{{.ShortHash}}(t *testing.T) {
    // Replays the minimized artifact against {{.Seam}} and asserts the bug is fixed.
    {{.ReplayBody}}   // hook-provided: decode artifact, drive seam, assert no-panic/invariant
}
```
Written next to the surface's tests (path from the Emit hook). Provenance trailer in the comment; opt-in `--commit` flag does `git add` + commit with a `Fuzz-Promoted:` trailer for a human-reviewable diff.

## 4. Phases 1, 2, 4 (interfaces only — full sketch in research §4/§9)

- **Phase 1 — schema→generator (`fuzz/schemagen`).** `func FromJSONSchema(schema map[string]any) *rapid.Generator[any]` walking `type/properties/required/enum/additionalProperties`, producing schema-valid *and* schema-adjacent values. Fed by tool `Definition.Parameters` (`agent/internal/tool/definitions.go`) and reflected `appwire.Methods` params. Drives #5 (adversarial-but-valid tool args); the divergence oracle (validated-clean but handler misbehaves — research §3) is the payoff. ~400–700 LoC.
- **Phase 2 — stateful appwire sequence (`router_seqfuzz_test.go`).** A `rapid` state machine modelling the session/turn/job lifecycle, driving `Router.Dispatch` (`internal/appserver/router.go`). Model = legal transitions (init→thread/start→turn/start→steer/interrupt/queue→clear). Oracles, weakest-first: never panic → never wedge → status monotonicity. Failures go through the Phase-3 promoter. The model is the hard part (research §10) — derive transitions from lifecycle docs; start with the thin invariants. ~500–900 LoC.
- **Phase 4 — HTTP + provider metamorphic.** httptest-level fuzz of `WebServer.Handler()` (`cmd/serf-hub/web.go:134`) with `AuthToken` empty; oracle = never 5xx/panic, never path-escape. Provider metamorphic harness: split/reorder/whitespace SSE frames must not change the accumulated `llm.Response`. Optional OpenAPI-gen + schemathesis add-on (HTTP only, later). ~400–700 LoC.

## 5. Generalization seam (the future skill)

The `promoter.Adapter` (four hooks) + `schemagen.FromJSONSchema` are the portable tooling. The skill (`docs/skills/fuzzing-an-api-surface/`, Phase 5) is methodology-first: when to reach for it, how to pick a surface+seam, the 7-step procedure (research §8), and the flake-guard/dedup rubric. Per-language fuzzers (Go `testing.F`+`rapid`, JS fast-check, Python hypothesis) sit behind the same Adapter; only the engine, corpus format, and emitter syntax differ. **Nothing in `fuzz/promoter` or `fuzz/schemagen` imports serf** — that's the test of whether the core is truly portable.

## 6. Build order & acceptance

Order (research §9): **0 → 3 → 1 → 2 → 4 → 5.** Promoter early so Phase-1/2 failures are permanent from day one.

Acceptance per phase:
- **Phase 0:** `make fuzz` green; a deliberately-injected parser bug is caught by a target and, after `go test -fuzz` finds it, the crasher file makes `make fuzz` red until fixed (demonstrate the free loop end-to-end on one target).
- **Phase 3:** unit tests for the promoter — a deterministic synthetic failure promotes once, dedups on the second sighting, and a synthetic *flaky* failure is quarantined (never emitted). This is the most important test in the whole toolkit.
- **Phases 1/2/4:** each finds ≥1 real issue or proves the surface clean to a stated depth; every promoted test replays green after the fix and red before.

## 7. Open decisions (RESOLVED)

These were the Phase 0–5 open decisions; all are now settled (Phases 0–5 built as below; the Phase-6+ refinements in §8/§9 supersede where they overlap — e.g. generated tests are filed via the 8.7 local triage tool's PR-by-default flow, K=5/N=4, and there is no scheduled nightly).
1. **Auto-commit of generated tests:** *emit-only* during the build; 8.7's local triage tool opens a PR by default carrying the generated test.
2. **Where `fuzz/` sits in `go.work`/`GO_MODULES`** and the `envvars`-not-gated caveat (research §10).
3. **K and N** for flake-guard / stack-hash (start K=5, N=4; tune).
4. **Nightly budget** per target in `fuzz-nightly` (start 60s).
5. Whether Phase 2's state model lives as code or is derived from a declarative transition table (prefer the table — it's the reusable artifact).

## 8. Beyond the toolkit — full-coverage roadmap (Phase 6+)

Phases 0–5 are **built** (see git history on `wip/fuzzing-toolkit`). They cover ~6–7 seams: SSE parse, appwire frame/param decode, tool-arg validate, appwire dispatch sequence, hub HTTP, and the openai responses decoder. That is a foundation, **not** whole-codebase coverage. "Fuzz the entire codebase" decomposes into three different investments — only one of which is hard — plus the automation that turns a one-shot into a standing capability.

Each work item below gets its own detailed implementation plan under `docs/design/plans/`. The item text here is the charter (scope + the real seams + rough size + dependencies); the plan doc is the *how*.

### A. More per-surface targets — no new infra, just more `testing.F` (mechanical, high-yield)
- **8.1 Persistence round-trip + replay-idempotence targets** → `docs/design/plans/01-persistence-roundtrip-targets.md`. Seams: `agent/schema/snapshot.go`, transcript write/replay, the jobstore event log, `agent/schema` session meta. Oracles: decode→encode→decode fixed point; **replay-idempotence** (replaying a persisted log reproduces the same in-memory state). Highest-yield next surface — serf's historical bugs cluster at reload/replay. ~300–500 LoC.
- **8.2 Codex-compat input/item + config decode targets** → `docs/design/plans/02-codex-compat-and-config-targets.md`. Seams: appwire `InputItem`/item parsing (the codex-compat path), `providers.toml`, plugin manifests, session config. Same decode-surface pattern as Phase 0. ~250–450 LoC.

### B. The stateful agent core — the one real infrastructure investment
- **8.3 Deterministic offline agent harness + first stateful agent-core target** → `docs/design/plans/03-offline-agent-harness.md`. The 47K-LoC `agent` core is unfuzzed because driving a real turn/job lifecycle deterministically needs a fake LLM, a fake clock, and a sandboxed exec env. The seed already exists: `agent/internal/agenttest.FakeAdapter` (scripted `llm.Response`s). The work: (1) a **fuzz-driven programmable provider** so a `rapid` state machine can drive a session through fuzzed sequences of (LLM responses, tool results, steers, interrupts, job events); (2) a first-class **fake clock** seam (extend the existing `freezeClock` test helper); (3) a **sandboxed `execenv`** (replace/deny over `agent/execenv/local.go`, which runs real commands) — this is also what would unblock fuzzing tool *handler execution*, not just validation. Then one stateful target over the session/turn/job machinery: invariants = no wedge, status monotonicity, no lost turns, transcript↔state consistency. This is the high-effort, high-value piece; everything else in B depends on it. ~800–1500 LoC.

### C. Tooling that multiplies coverage
- **8.4 Corpus harvesting from recorded traffic** → `docs/design/plans/04-corpus-harvesting.md`. serf already records `RawRequestBody`/`RawResponseBody` and full transcripts. A tool that sanitizes those into `fuzz/corpus/` seeds beats hand-written `f.Add` seeds by orders of magnitude (real provider quirks for free). Highest-leverage tooling item. ~200–400 LoC.
- **8.5 Unified wire-type → generator registry** → `docs/design/plans/05-unified-schema-registry.md`. Tool args have `schemagen`; appwire params/responses are reflected ad-hoc. One registry mapping every wire type (all 46 `appwire.Methods` params + response types) to a generator lets a single harness cover the whole protocol uniformly instead of one target per surface. Builds on `schemagen`. ~300–500 LoC.

### D. Automation — the difference between a one-shot and a standing capability
- **8.6 Coverage measurement** → `docs/design/plans/06-coverage-measurement.md`. The honesty tool: without it, "fuzzed the whole codebase" is unverifiable. Run each target's corpus under `-coverprofile`, report per-surface line coverage ("fuzzed nominally" vs "actually exercised"), and gate on it. ~150–300 LoC.
- **8.7 Nightly CI campaign + auto-triage** → `docs/design/plans/07-nightly-ci-automation.md`. `make fuzz` runs seed corpus in the gate; `fuzz-nightly` is manual. Need a scheduled job running a real per-target budget, with the **promoter auto-filing crashers as a reviewable PR** (closing the failure→test loop unattended) and a found/fixed/quarantined triage record. ~250–500 LoC.

## 9. Roadmap build order

All Phase-6+ decisions were resolved interactively with Jesse on 2026-06-28; each plan under `docs/design/plans/` carries its own resolved-decisions section, and the dependency graph below reflects those choices. (LoC/ordering are advisory — see each plan; the goal is *perfect coverage*, so the more-thorough option was taken throughout.)

**Dependency graph (what gates what):**
- **8.6 coverage (focus-set)** — stand up first; every target that lands afterward is ratcheted toward 100% from the start.
- **8.4 corpus harvesting + the new WS-frame / hub-HTTP recorders** — precedes **8.1** (persistence consumes 8.4's harvested seeds, including jobs.jsonl). The recorder is a prerequisite sub-deliverable of 8.4.
- **8.1 persistence** — depends on 8.4.
- **8.2 codex-compat + config** — self-contained; includes the `launchconfig.ModelFallbacks` → `*[]string` production refactor as a prerequisite step before its launchconfig target.
- **8.5 typegen registry** — depends on a foundational `fuzz/schemagen` **`Source` refactor** (generators abstracted over a byte-stream **or** rapid entropy source) so the registry can drive coverage-guided `testing.F`; the existing rapid targets migrate to a rapid-backed Source.
- **8.3 offline agent harness** — the big rock: a first-class injectable **Clock** (replaces ~27 `time.Now` + jobs sleeps/timers), jobs + compaction + goals in scope, hybrid `Responder` adapter. Independent; can run any time.
- **8.7 local triage tool** — last: needs targets to exist + the promoter temp-dir persistence fix. Local on-demand only (no scheduled CI); the sole CI change is adding `make fuzz` to the existing `ci.yml` PR gate.

**Suggested order:** 8.6 → 8.4 (+recorders) → 8.1 → 8.2 → 8.5 (Source refactor → registry) → 8.3 (clock → jobs/compaction/goals harness) → 8.7. Foundational refactors (schemagen `Source`, the `Clock`) land at the head of their dependent item.
