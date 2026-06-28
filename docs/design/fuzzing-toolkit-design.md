# Fuzzing + failure-to-regression toolkit — detailed design

**Status:** design, ready to implement. **Date:** 2026-06-28. **Branch:** `wip/fuzzing-toolkit` (worktree `.worktrees/fuzzing-toolkit`).
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

## 7. Open decisions (for the implementer to raise, not assume)
1. **Auto-commit of generated tests:** default to *emit-only* (write file, leave unstaged) and require `--commit`? (Recommended: yes, opt-in.)
2. **Where `fuzz/` sits in `go.work`/`GO_MODULES`** and the `envvars`-not-gated caveat (research §10).
3. **K and N** for flake-guard / stack-hash (start K=5, N=4; tune).
4. **Nightly budget** per target in `fuzz-nightly` (start 60s).
5. Whether Phase 2's state model lives as code or is derived from a declarative transition table (prefer the table — it's the reusable artifact).
