# Phase 10 — Deeper fuzzing: failure paths, real concurrency, and detection sufficiency

> **Status (2026-06-30): DONE (W1, W3, W5, W4, W2; W6 skipped).** Point-in-time
> design + plan. Successor to `10-raising-detection-power.md` (Phase 9, complete).
> W1 fault-injection (model-call-failure recovery, FaultResponder seam + opLLMError;
> 8000 -race clean). W3 coverage-guided stateful (FuzzLifecycleSeq). W5
> mutation-audit extended to rapid targets AND mutation-score-at-scale via
> gremlins (`make fuzz-mutation-score`; providercfg 91.89%, 3 survivors; others
> 100%). W4 conformance golden-replay for the provider decoders. W2
> true-concurrency stress under -race WITH the go.uber.org/goleak goroutine-leak
> oracle (8x -race clean). No new product bug — the failure/concurrency paths
> newly fuzzed are sound. The "offline-blocked" goleak/gremlins deferrals were
> resolved (network works; the friction was the go.work local-module setup, worked
> around). Remaining follow-up: the disk-fault op (quiesceJobs loop-advance) and
> the anthropic assembled-reasoning section-break the W4 conformance golden pins.

## Why this phase exists

Phases 0–9 fuzzed the **happy path's structure**: decoders over well-formed-ish
input, stateful models with *cooperative* infrastructure, and deterministic
single-goroutine interleavings. But every deep serf bug we have actually shipped
a fix for lives somewhere we have deliberately *not* fuzzed:

- mid-stream retry gaps and the `ASSISTANT_TEXT_RESET` continuation bug — a
  **failure path** (the provider stream broke mid-turn);
- the "Working, quiet for N minutes" WebSocket keepalive desync — **real
  concurrency** (a silent TCP drop no goroutine was watching);
- the delegate-responder `callSeq` race — **real concurrency** the deterministic
  models specifically engineer away.

So this phase goes after three things Phase 9's diagnosis (detection, not search,
is the ceiling) points at but we have not yet attacked:

1. **The failure half of the state space** (W1 fault injection, W2 true
   concurrency) — the least-tested, most-bug-prone code in any runtime.
2. **Reaching deeper states / more realistic inputs** (W3 coverage-guided
   stateful, W4 real-traffic corpus) — the explicit "improve coverage" ask.
3. **Whether our oracles are even sufficient** (W5 mutation score) — measure
   detection across all code, not just where we curated checks.
4. The semantic ceiling (W6) — named honestly, scoped as speculative.

Non-goals are at the end; they are the things prior phases proved don't pay.

---

## W1 — Fault-injection / chaos stateful fuzzing (do first)

**Goal.** Fuzz the *recovery* paths. Our stateful models assume the LLM client
succeeds, disk writes land, and contexts don't cancel. Inject those faults at
fuzzer-chosen points and assert the session always recovers to a consistent
state. This is the highest-yield workstream: failure handling is where serf's
real bugs have been, and it is almost entirely unfuzzed.

**Mechanism.** Extend the lifecycle model (`agent/lifecycle_seqfuzz_test.go`)
with fault ops, reusing the seams that already exist plus one adapter extension:

- **LLM faults.** `agenttest.ScriptedAdapter` (`agent/internal/agenttest/scripted_adapter.go`)
  today has `Responder func(llm.Request) llm.Response` and always returns a nil
  error. Extend it with an optional fault hook so a scripted step can instead
  return a `Complete`/`Stream` error, or a **truncated stream** (emit N events
  then an error / early close) — the exact shape of the mid-stream-gap bug.
- **Disk faults.** `failAppendN` (`agent/testkit_test.go:127`) already makes the
  next N jobstore appends of a given event kind fail. Lift it into the model as a
  fault op so a job's terminal/notification append fails under interleaving.
- **Cancellation faults.** The model already cancels mid-op for interrupts (the
  `cancelAt`/`cancelFn` machinery in `lifecycleOracleRunInjected`). Generalize it
  to cancel at a fuzzer-chosen step of *any* op (delegate, compaction, shell).
- **Env faults.** `agenttest.DenyEnv` returns deny errors; add a mode that fails
  a chosen FS/exec call rather than denying uniformly.

**Oracle.** Recovery invariants that must hold *after* a fault — fault-tolerant
versions of the existing oracles (assert "consistent," not "identical to the
no-fault run"): the session reaches idle or closed; no job is stuck non-terminal
after quiesce; history stays well-formed (no orphaned tool call, no half-written
turn); terminal-stickiness (Oracle 7) still holds; and **no goroutine leak**
after Close (see W2's goleak oracle, shared).

**Build steps.** (1) Extend `ScriptedAdapter` with the fault hook + a truncated
`Stream`. (2) Add `opLLMError`, `opStreamTruncate`, `opAppendFail`,
`opCancelAt`, `opEnvFail` to the model, each fuzzer-parameterized. (3) Add the
recovery oracles. (4) Validate over 8000 `-race` checks. (5) Each fault that the
model already survives becomes a standing guard; each it *doesn't* is a bug fixed
TDD, with a W5 mutation proving the new recovery oracle reddens.

**Effort.** ~300–500 LoC. **Risk.** A fault can legitimately produce a
*different but valid* terminal state — the oracles must assert consistency, not
equivalence to the clean run, or they false-trip.

---

## W2 — True-concurrency stress fuzzing under -race (do after W1)

**Goal.** Find the data races and deadlocks the deterministic models engineer
away. Every Phase 8/9 stateful model is single-goroutine by design; the desync
and delegate-race bugs needed *genuine* parallelism.

**Mechanism.** A non-`rapid` stress harness that launches N goroutines driving
**parallel** session operations — concurrent ProcessInput + delegate + watch +
interrupt + Close + (hub) multiple WebSocket clients — under `-race`, with the op
mix and counts chosen from a logged seed for partial reproducibility.

**Oracle.** The relaxed, concurrency-appropriate set: `-race` clean (the headline
detector); **no goroutine leak** via `go.uber.org/goleak` (a NEW dependency)
asserted after every session/hub teardown; no panic; **no deadlock** via a
wall-time watchdog (the lifecycle model's wedge-watchdog pattern); and final
consistency once quiesced (jobs terminal, history well-formed).

**Build steps.** (1) Add the `goleak` dep + a shared `assertNoLeak(t)` helper.
(2) A `TestSessionConcurrencyStress` driving parallel ops under `-race`. (3) A
`TestHubProtocolStress` driving multiple appwire/WS clients with interleaved,
out-of-order, and malformed RPCs (the desync's actual shape). (4) Run them in the
`-race` nightly, not the fast gate.

**Effort.** ~250–400 LoC + the goleak dep. **Risk.** Nondeterminism — a race may
not reproduce. Mitigate with `-race` (which flags the race regardless of
outcome), a logged schedule seed, and run-many; keep these *out* of the
deterministic gate so a flake never blocks a PR (they run in the nightly/local
campaign).

---

## W3 — Coverage-guided stateful fuzzing (parallel with W4)

**Goal.** Reach deep states random generation misses. `rapid` generates op
sequences *randomly*; it does not follow coverage. Drive the same state machine
from a coverage-guided byte stream so Go's fuzzer explores by what code it
reaches.

**Mechanism.** A native `FuzzLifecycleSeq(f)` whose `[]byte` input is decoded by
a small, **stable** byte→op decoder into a lifecycle op sequence, then run
through the existing `lifecycleOracleRun` + oracles 3–8. `go test -fuzz`
coverage-guides the byte stream into deep interleavings (delegate-during-
compaction-during-background-shell) that uniform random sampling rarely hits.

**Oracle.** Reuse the lifecycle oracles unchanged — this workstream is about
*reach*, not new properties.

**Build steps.** (1) A deterministic `decodeOps([]byte) []opRecord` (stable
mapping — coverage guidance needs it). (2) `FuzzLifecycleSeq` wiring it to the
existing oracle run. (3) Register native; seed with the corpus W4 produces.
(4) Compare state-space coverage vs the rapid target (expect deeper).

**Effort.** ~200 LoC (decoder + wiring; oracles reused). **Risk.** The byte→op
mapping must stay stable across edits or the persisted corpus decays — document
it as append-only.

---

## W4 — Real-traffic conformance + whole-session corpus (parallel with W3)

**Goal.** Fuzz with inputs the synthetic generators can't produce, and detect
provider wire-format drift the moment it ships. The decoders' deepest bugs come
from *real* provider quirks (the anthropic finish-reason and gemini usage bugs
were real shapes).

**Mechanism.** Build on the existing harvester (`cmd/serf-fuzz-harvest/` —
`http.go`, `raw.go`, `transcript.go`, `sanitize.go`) and the recorders:

- **Conformance corpus.** Capture real `(provider request, raw response)` pairs,
  scrub them (reuse the existing gitleaks + `sanitize.go` path), commit them, and
  replay through each decoder as a **golden differential** (`llm/providers/difftest/`):
  a decode that diverges from the committed golden = provider drift or a decoder
  regression, surfaced the moment it happens.
- **Whole-session corpus.** Harvest real scrubbed sessions (huge histories, odd
  tool sequences, every content kind) and seed the reload / compaction / lifecycle
  targets — shapes the structured generators don't reach.

**Oracle.** Golden match (drift detection) + the existing decode/reload/compaction
oracles exercised over realistic shapes.

**Build steps.** (1) Extend the recorder to persist request+response pairs.
(2) A `make fuzz-conformance` that replays the corpus through the decoders vs
goldens. (3) Session-corpus harvest mode + seed wiring. (4) Scrub-gate every
committed artifact (the secret-scan barrier already exists).

**Effort.** ~300 LoC + corpus. **Risk.** Secrets in real traffic — the scrub +
gitleaks gate is mandatory and already built; never commit un-scrubbed capture.

---

## W5 — Mutation score at scale (after W1–W4 have oracles to measure)

**Goal.** Answer "are our oracles *sufficient*?" quantitatively. Phase 9's W1
proves a *curated* set of oracles fire; this measures detection across the whole
SUT and surfaces code that has fuzz coverage but no teeth.

**Mechanism.** Generalize the W1 audit harness (`scripts/fuzz-oracle-audit.sh`):

- Extend it to **rapid targets** (the known follow-up — it is native-only today),
  so the stateful oracles (lifecycle, jobstore, compaction) participate.
- Run a broad mutation pass (`gremlins` or `go-mutesting`) over each package and
  score it by the fraction of mutants **any** registered fuzz target kills — the
  **mutation score** — reporting low-score packages as the weak-oracle worklist.

**Oracle.** The mutation score itself; a ratchet/floor per package like the
coverage ratchet, so detection sufficiency can only improve.

**Build steps.** (1) Audit-harness rapid support. (2) Mutation-tool integration
(scoped to core + changed packages — full-tree mutation is slow/noisy with
equivalent mutants). (3) A score report + floor file.

**Effort.** ~200 LoC + tool integration. **Risk.** Slow + noisy (equivalent
mutants read as "uncaught"); scope tightly and treat the score as a trend, not an
absolute gate.

---

## W6 — Semantic-correctness oracle (speculative, last)

**Goal.** The deepest class: the agent does the *wrong thing* without crashing —
which no code oracle reaches.

**Mechanism.** An LLM-as-judge oracle for **narrow, checkable** properties only:
"compaction preserved the key facts," "the tool summary matches the call it
describes." Offline, gated, run as a periodic eval (not a gate), with multiple
judges + thresholds to blunt noise. Builds on the compaction-eval work.

**Oracle.** Judge verdict, majority-voted, thresholded.

**Effort.** Large + ongoing. **Risk.** Noisy, flaky, expensive — explicitly *not*
a gate. Lead with nothing here; pursue only if W1–W5 plateau and a specific
semantic property proves worth the cost.

---

## Sequencing & dependencies

1. **W1 (fault injection) first** — highest bug-yield, extends the existing
   lifecycle model, deterministic-friendly (faults injected at chosen points,
   replayable).
2. **W3 and W4 in parallel** — disjoint (W3 = agent model; W4 = llm + harvester);
   W4's corpus feeds W3's seeds.
3. **W2 (true concurrency) after W1** — harder and non-deterministic; needs the
   goleak dep and a nightly-only home so flakes never gate a PR.
4. **W5 (mutation score)** once W1–W4 have added their oracles — it measures them.
5. **W6** only if the rest plateaus.

Each workstream is its own gate-green `--no-ff` merge. New oracles (W1, W2) land
with a W5/Phase-9-W1 mutation proving they redden. `-race`/leak-heavy targets
(W1, W2) live in the nightly/local campaign, never the fast PR gate.

## Non-goals (prior phases settled these)

- More single-input decoder targets — the surface is covered (gap gate green).
- More raw fuzztime on covered code — the ceiling is detection, not search.
- More structure-aware generators for their own sake — they found zero bugs.
- Driving coverage % on unreachable-by-construction code (reflect-decode floors).
