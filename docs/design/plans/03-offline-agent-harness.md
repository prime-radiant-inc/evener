# 8.3 — Deterministic offline agent harness + first stateful agent-core target

**Status:** plan. **Charter:** fuzzing-toolkit-design.md §8.3 (roadmap item B). **Date:** 2026-06-28.
**Branch:** `wip/fuzzing-toolkit` (worktree `.worktrees/fuzzing-toolkit`).
**Builds on:** the Phase-2 stateful pattern in `internal/appserver/router_seqfuzz_test.go`, the `fuzz/promoter` core (design §3), and the existing agent test scaffolding in `agent/internal/agenttest/` and `agent/testkit_test.go`.

This is the largest item in the roadmap. The `agent` core (~47K LoC) is unfuzzed because driving a real turn lifecycle deterministically needs three infrastructure pieces that don't exist yet as first-class, fuzz-safe seams: a fuzz-driven LLM provider, an injectable clock, and a sandboxed exec env. This plan grounds all three against the real code, picks the target's package (the import-cycle decision is load-bearing — see §5), models the first stateful target, and — most importantly — is honest about **which parts of the lifecycle are not deterministically drivable yet and why** (§6). A flaky target here is a defect, so the determinism analysis is the spine of the plan, not an appendix.

---

## 0. Goal & scope

**Goal.** Stand up the offline harness (fuzz adapter + clock seam + deny env) and ship **one** stateful `rapid` target that drives a real `agent.Session` through fuzzed turn-lifecycle sequences entirely offline, with failures routed through `fuzz/promoter`. Invariants weakest-first: never panic → never wedge → status monotonicity → no lost turns → transcript↔state consistency.

**In scope (v1).** The single-session turn lifecycle: `ProcessInput` / `Steer` / `Enqueue` / `FollowUp` / interrupt (ctx cancel) / `Close`, with the model's behavior each round drawn by the fuzzer (text, tool calls to safe tools, communicate/end-turn, empty/degenerate responses).

**Out of scope (v1), with reasons in §6.** The job/subagent/delegate machinery (`createDelegate`, `sendDelegateMessage`, watches, notifications) — it spawns real goroutines, real `time.Sleep`, real finalize timers and watchdogs that are not deterministically drivable until the clock/sleep seams are fully threaded. v1 runs sessions with `stateDir == ""` (no durable jobstore, no namer), which keeps the concurrency surface bounded; jobs are item 8.3-follow-up, gated on §6's open questions.

**Non-goals.** Real LLM calls, real filesystem/exec, wall-clock dependence, auto-commit of emitted tests (emit-to-tempdir only, per design §3.2 / Phase 2 precedent).

---

## 1. The three infrastructure seams (grounded)

### 1.1 Programmable / fuzz-driven provider

**Interface it must satisfy.** `llm.ProviderAdapter` (`llm/client.go:13`):

```go
type ProviderAdapter interface {
    Name() string
    Complete(ctx context.Context, req Request) (Response, error)
    Stream(ctx context.Context, req Request) (Stream, error)
}
```

Three methods. The agent core's non-streaming path calls `Complete`; `Stream` returns `llm.ErrStreamUnsupported` in every existing fake. There is also an optional capability the responses-continuation path probes for — `PlanResponsesContinuation(req) (ResponsesContinuationPlan, error)` (`llm/client.go:242`, implemented by `agenttest.FakeAdapter` at `agent/internal/agenttest/agenttest.go:68`). v1's fuzzed sessions use a profile that does not trigger continuation planning, so the fuzz adapter only needs `Complete`.

**What exists.** Two scripted adapters already implement the interface:
- `agenttest.FakeAdapter` (`agent/internal/agenttest/agenttest.go:27`) — `Steps []func(req llm.Request) llm.Response`, advanced one per `Complete` call, with response builders `ToolCallResponse`, `CommunicateResponse`/`FinalResponse`, `EmptyResponse`, `CommunicateCall`/`CommunicateCallArgs` (same file). This package is deliberately **agent-free** (its doc comment, lines 1–11, makes that a hard constraint to avoid a test import cycle), so it depends only on `llm`/`execenv`.
- The agent package's own `fakeAdapter` (`agent/session_test.go:41`) — the one `newSession` registers by default (`agent/testkit_test.go:73`). Identical shape (`steps []func(req) llm.Response`), package-internal.

**The gap.** Both are *scripted* (a fixed slice indexed by call count). A `rapid` state machine needs to feed a *fuzzed sequence* of responses that can also *react* to the request (e.g. only call `communicate` once tools have been offered). Two design choices:

- **Plan A (recommended): a `fuzzAdapter` in package `agent`** (next to `fakeAdapter`, in the new test file). It holds a `*rapid.T` (or a pre-drawn script) and on each `Complete` draws/returns the next `llm.Response`. Because the target lives in package `agent` anyway (§5), this is the cheapest path and can reuse the `agenttest` response builders (they're importable, agent-free). Drawing inside `Complete` is sound: `rapid` draws are deterministic for a fixed seed, and `Complete` is called synchronously from the turn loop. The one hazard is a *second concurrent caller* of `Complete` (the namer) racing the draw sequence — eliminated in v1 by disabling the namer (§1.2 note, §6).
- **Plan B: extend `agenttest.FakeAdapter` with a `Respond func(req, callIndex) llm.Response` hook.** Keeps the fuzz logic out of package agent, but `agenttest` cannot import `rapid`-drawn closures cleanly and cannot reach `Session`, so the state machine still lives in package agent and would just call back. Plan A is simpler; prefer it.

**Response vocabulary the fuzzer draws from** (each maps to a builder or a hand-rolled `llm.Response`):
1. *bare text + continue* — `llm.Response{Message: llm.Assistant(text)}` with tool calls absent → exercises the bare-text retry path (`handleNoToolCalls`, `session_lifecycle.go:688`).
2. *tool call(s) to a safe tool* — `agenttest.ToolCallResponse(ToolCall{Name:"read_file"/"glob"/..., Arguments: fuzzedJSON})` → drives `execToolBatch` against the deny env (§1.3). This is what unblocks fuzzing **handler execution**, not just validation (design §8.3).
3. *communicate / end-turn* — `agenttest.FinalResponse(text)` or `CommunicateResponse(false, text)` (awaits reply) → the normal turn-completion path (`deliverIfCommunicated`, `session_lifecycle.go:757`).
4. *empty response* — `agenttest.EmptyResponse()` → the empty-retry budget (`maxEmptyRetries`/`maxTotalEmptyResponses`, `session_lifecycle.go:50`).
5. *pause_turn* — `Response{Finish: {Reason: llm.FinishReasonPauseTurn}}` → the pause path (`session_lifecycle.go:653`).

Size: ~120–200 LoC (adapter + a small response-kind enum the rapid machine samples).

### 1.2 Fake clock

**The real seam today is partial.** The only injectable clock in the agent core is on the job manager: `jobManager.now func() time.Time` (`agent/jobs.go:76`, defaulted to `time.Now` at construction, `agent/jobs.go:337`). The existing test helpers reassign it from inside the package:

```go
// agent/testkit_test.go:110
func freezeClock(jm *jobManager) time.Time { jm.now = func() time.Time { return frozenTestTime }; return frozenTestTime }
func freezeClockAt(jm *jobManager, at time.Time) { jm.now = func() time.Time { return at } }   // :116
```

**The honest gap (state this plainly).** The `Session` itself holds **no clock** — `grep` for a `now`/`clock` field on `Session` returns nothing; the lifecycle reads wall time directly via **27 non-test `time.Now()` calls** in the agent core. Representative load-bearing-ish sites:
- `session_state.go:36` — `now := time.Now().UTC()` in the state-transition path.
- `session.go:679`, `session_init.go:196` — `Timestamp`/`CreatedAt` on session/meta.
- `session_goal.go:45,180,246`, `session_tools_goal.go:58` — goal-engine timestamps and the no-progress breaker.
- `session_namer.go:336`, `session_lifecycle.go:558,580,711,723,732`, `session_model_call.go:159,180,222` — namer + **round-timing telemetry** (fed only into `events.RoundTimings`).

Plus real `time.Sleep` outside any injectable seam: `job_delegate.go:1678` (`delegateFinalizeRetryDelay`), `job_shell.go:548,560,571` (shell finalize backoff). And the retry sleep seam `LLMSleep llm.SleepFunc` (`session_config.go:127`), already test-injectable.

**What this means for the plan.** A "first-class injectable clock" that nothing depends on wall-time is a *bigger* change than the charter's one-liner ("extend the existing `freezeClock` helper") implies. Two tiers:

- **Tier 1 (v1, sufficient):** scope the target to the no-jobs lifecycle, where the wall-time reads that remain are **telemetry-only** (round timings → `events.RoundTimings`) and timestamp stamps that the oracles do not read. The oracles assert on `State()`, the transcript/history, and turn counters — none of which derive from `time.Now()` in the no-jobs path. So v1 needs *no* clock surgery: it relies on "no real IO + no jobs + seeded rapid" for determinism, and simply tolerates non-deterministic *event-payload timestamps* (which the oracle ignores). Document this assumption in the target.
- **Tier 2 (prerequisite for the jobs follow-up, sized here but not built in v1):** introduce a single `Clock` seam on `Session` (a `now func() time.Time` field defaulted to `time.Now`, set once at construction and *passed through* to the `jobManager` so `jm.now` is the same clock). Promote `freezeClock` to operate on a `Session` (or a `SessionConfig.Clock` injection point). This is the invasive part: replacing the 27 `time.Now()` call sites with `s.now()` and threading the clock into goal/namer/state code. ~150–300 LoC and it touches many files — it must be its own reviewed change, **not** smuggled into the fuzz target. v1 proves the harness works without it; Tier 2 is the gate for fuzzing the job machinery (§6, §7).

Recommendation: **build Tier 1 for v1; specify Tier 2 as the first follow-up.** Do not block the first target on the clock refactor.

### 1.3 Sandboxed execenv

**Interface.** `execenv.ExecutionEnvironment` (`agent/execenv/execenv.go`) — 13 methods: `Initialize`, `Cleanup`, `WorkingDirectory`, `Platform`, `OSVersion`, `ReadFile`, `WriteFile`, `EditFile`, `FileExists`, `Glob`, `Grep`, `ListDirectory`, `ExecCommand`. Plus the optional `StreamingExecutor` (`StreamCommand`) the job runtime type-asserts for.

**The danger.** `LocalExecutionEnvironment` (`agent/execenv/local.go:59`) runs **real commands** (`ExecCommand` forks a shell, `local.go:705`; `StreamCommand`, `:798`) and touches the real FS. Fuzzing tool-handler execution through it would run model-generated (fuzzed) shell/file ops against the host — unacceptable.

**What exists.** `agenttest.FakeEnv` (`agent/internal/agenttest/env.go:15`) is already an inert implementation: `ExecCommand` answers only `git rev-parse --show-toplevel` (when `GitRoot` set) and fails everything else; all FS ops are no-ops returning empty. It satisfies the full interface.

**Plan.** Add a **`denyEnv`** (extend `FakeEnv`, or a sibling in package agent) tuned for fuzzing:
- Every method returns a *deterministic, structured* result/error and **never** touches FS or forks a process. `ExecCommand` returns `ExecResult{ExitCode: <fuzzable>, Stdout: <bounded canned>, ...}, nil` (or a deterministic error) so the tool layer's result-handling runs end-to-end. Reads/globs/greps return bounded canned content (or empty) so `read_file`/`glob`/`grep` handlers execute their full decode/format paths.
- Deliberately **does not** implement `StreamingExecutor`, so the streaming shell job path can't engage (belt-and-suspenders for the no-jobs scope).
- Optionally parameterize a few deterministic outcomes (exit code, "file exists" y/n, content length) off the fuzzer so handler branches are explored — but keep outputs *bounded and pure* so replay is byte-identical.

This is the seam that "unblocks fuzzing handler *execution*, not just validation" (design §8.3): with a deny env, the fuzzed tool-arg path runs through `execToolBatch` → the real handler → the env → result formatting, all offline. ~80–150 LoC (mostly the canned-but-deterministic `ExecCommand`/`ReadFile`).

---

## 2. The first stateful target

### 2.1 Where it lives (the import-cycle decision — load-bearing)

**Verdict: package `agent`, white-box test file `agent/lifecycle_seqfuzz_test.go`.**

Why not where Phase 2 lives: `router_seqfuzz_test.go` is in `internal/appserver`, and its own header (lines 41–47) documents that it **cannot reach the agent core** — importing `server`/`cmd` would be an import cycle, and those handlers need a live agent/LLM. The agent-core target has the inverse requirement: it must *build a real `agent.Session`*. That is only possible from:
- **package `agent`** (white-box `_test.go`) — can call `NewSession` (`agent/session_init.go:79`), reuse `newSession`/`fakeAdapter`/`freezeClock` (all package-internal), and reach unexported seams (`jobManager.now`, `s.state`, `s.history`, `s.turns`, `s.modelResponses`) the oracles need.
- **not `agenttest`** — it is constitutionally agent-free (cannot import `agent`).
- **not a black-box `agent_test` package** — it could call the exported API but could not read the unexported state the transcript↔state oracle needs, and could not inject `jm.now`.

Build wiring is already in place: **`agent/go.mod` already requires `primeradiant.com/serf/fuzz`** (`agent/go.mod:14`), so the target imports `fuzz/promoter` with no new dependency — exactly as `internal/appserver` does. `fuzz` is in `go.work` and (per design §1) in the Makefile `GO_MODULES`, so the seed corpus runs under the gate.

### 2.2 The control surface the model drives

Public + package-internal methods the op table calls (all verified in `agent/session_queue.go`, `session_lifecycle.go`, `session_state.go`):
- `ProcessInput(ctx, text, images)` (`session_lifecycle.go:254`) — runs one input to completion (synchronous; loops rounds internally).
- `Steer(msg)` (`session_queue.go:62`) — inject steering (delivered at the next turn boundary or mid-turn).
- `FollowUp(msg)` (`session_queue.go:144`) — per-turn follow-up queue.
- `Enqueue(ctx, text)` (`session_queue.go:169`) — queue a user message for the drain loop.
- Observers: `State()` (`session_state.go:24`), `QueueDepth()`/`QueuePreview()` (`session_queue.go:271/284`).
- `Close()` (`session_lifecycle.go:73`).
- **Interrupt** is modeled by passing a *cancellable* ctx to `ProcessInput` and cancelling it mid-turn; the loop's `isTurnCancellation` path (`session_lifecycle.go:333`) applies interrupt semantics (flip to idle, append the interrupt system-reminder, optionally drain the queue head).

### 2.3 The model (declarative op table, mirroring Phase 2)

Follow the Phase-2 shape exactly: a declarative `opTable` (the reusable artifact, design §7 #5) + a thin `rapid` machine that draws ops. Each op declares how to apply it and what the monotonic model predicts about `State()`/counters.

Op set (v1):
| op | action | model prediction |
|---|---|---|
| `opProcessInput` | `ProcessInput(boundedCtx, drawnText, nil)` with the fuzzer scripting the round responses | ends `SessionIdle` (or `SessionClosed`); `turns`↑ if accepted; `modelResponses`↑ |
| `opProcessInterrupted` | `ProcessInput` with a ctx cancelled after the first round draw | ends `SessionIdle` unless closed; transcript gains an interrupt marker; no panic |
| `opSteer` | `Steer(drawnText)` | enqueues steering; no state regression |
| `opEnqueue` | `Enqueue(ctx, drawnText)` | `QueueDepth`↑ |
| `opFollowUp` | `FollowUp(drawnText)` | per-turn followup recorded |
| `opClose` | `Close()` | terminal: `State()==SessionClosed`, idempotent |
| `opObserve` | read `State()`/`QueueDepth()` | pure; must not panic or mutate |

The fuzzer also draws, *per `ProcessInput` op*, a short script of round-response kinds (§1.1 vocabulary) the `fuzzAdapter` will play. So one drawn artifact = (op sequence) × (per-input response scripts) × (interrupt timing).

### 2.4 Oracles (weakest-first)

1. **Never panic (floor).** The turn loop does not blanket-`recover` (it `recover()`s only to flush meta then re-panics, `session_lifecycle.go:500`). Run each op under a recovering goroutine (like `safeHandle`, `router_seqfuzz_test.go:329`) so a panic is captured, classified, and turned into a `promoter.Failure{Oracle: Panic}` with a project-relative stack (reuse `captureStack`, `router_seqfuzz_test.go:474`).
2. **Never wedge.** Every op returns under a bounded context + a wall-clock watchdog goroutine (the harness's own timeout, not the session's). A `ProcessInput` that never returns is a `Wedge` failure. (This watchdog is the harness's, deliberately real-time, so it survives even if an in-session timer misbehaves — see §3 risk on timers.)
3. **Status monotonicity.** `State()` follows the legal lattice: `idle ⇄ active → closed` (closed is terminal; `active` only inside a `ProcessInput`; observed state at op boundaries is `idle` or `closed`). Reuse the Phase-2 "kind divergence from a monotonic model" technique: a regression to a pre-`idle`/post-`closed` state is an `Invariant` failure.
4. **No lost turns.** Every *accepted* user input (one not rejected by `MaxTurns`, `session_lifecycle.go:792`) appears as a `schema.TurnUserInput` in the transcript, and `turns`/`modelResponses` are monotonic non-decreasing across the sequence. A model that counts accepted inputs and compares to transcript user-turns catches a dropped turn.
5. **Transcript↔state consistency.** `len(s.history)` never *decreases* except across a compaction (none triggered in v1 — keep context pressure low / compaction disabled), `modelResponses` ≥ number of completed `ProcessInput`s, and no orphaned tool-result repair leaves a tool call without its result (`repairOrphanedToolResults` is the production guard; assert its post-condition: history has no dangling tool call). This is the strongest oracle and the likeliest to surface a real bug.

Failures route through `fuzz/promoter` exactly as Phase 2 does (§2.5).

### 2.5 Promoter wiring (reuse the Phase-2 Adapter shape)

Implement `promoter.Adapter` (`fuzz/promoter`, four hooks) for this surface, copying the `seqAdapter` structure (`router_seqfuzz_test.go:368`):
- **Artifact:** `{opCodes []int, responseScripts [][]int, interruptStep int, seed?}` — JSON, minimized by `rapid` already (Minimize is passthrough).
- **Signature:** `(Oracle, topN stack frames)` for panics; `(Oracle, Detail)` otherwise (`ShortHash` fallback) — same as `seqAdapter.Signature`.
- **Replay:** a single `seqOracleRun(artifact)` that rebuilds a *fresh* `Session` (deny env, `stateDir:""`, fuzzAdapter replaying the recorded scripts) and re-runs — the single source of truth so the live property and Replay classify identically (the Phase-2 discipline).
- **Emit:** `promoter.WriteGoTest` to a tempdir with a `ReplayBody` calling a package-local `replayLifecycleArtifact(t, json)`. Promotion into the tree stays the human/opt-in step.

Add the twin determinism tests Phase 2 has: a deterministic *injected* failure promotes once + dedups (`TestSeqAdapter_PromotesDeterministicFailure` analogue — e.g. register a tool whose handler panics, or feed a response the loop mishandles), and a *flaky* failure is quarantined (never emitted). These are the most important tests in the item (design §6).

---

## 3. Determinism risks — the crux

A flaky target is a defect. Spell out exactly what is made deterministic and what could still wobble.

**Made deterministic (the four pillars):**
1. **Seeded `rapid`.** `rapid.Check` is seed-driven; the `fuzzAdapter` draws from the same `*rapid.T`, so the entire (ops × response-scripts × interrupt-timing) artifact is reproducible from the seed. Replay reconstructs from the *recorded* artifact, not the seed, so it is independent of `rapid` internals.
2. **No real IO.** Deny env (§1.3): no FS, no subprocess, no streaming. `stateDir == ""`: no jobstore writes, no transcript-file IO, no ATIF export, no namer (the namer is gated off by `stateDir == ""` *and* by an unconfigured cheap model — `launchInitialPromptNamer`, `session_namer.go:186` returns early on `s.stateDir == ""` and on `!sessionNamerEnabled(profile)`). Using a default `NewOpenAIProfile` with no configured cheap model double-guarantees the namer goroutine never launches — **this removes the one concurrent `Complete` caller** that would otherwise race the fuzzAdapter's draw sequence.
3. **Bounded contexts.** Every op runs under a `context.WithTimeout`; interrupts are explicit ctx cancels at a drawn step. No op can block forever waiting on the model (the fuzzAdapter returns synchronously).
4. **Goroutine quiescence detection.** Two known background goroutines in the no-jobs path: (a) the per-`ProcessInput` ctx-link goroutine (`session_lifecycle.go:510`) — exits via `defer cancel()` when the input returns; (b) the **events drain** the harness itself must run. The harness drains `s.Events()` (`session_events.go:14`, buffered 256, `session_init.go:129`) in a dedicated goroutine for the whole `rapid.Check` body and joins it after `Close()` (Close closes the channel after `sendersWG.Wait()`/`toolEventsWG.Wait()`, `session_lifecycle.go:170-181`). **If the harness does not drain events, a turn emitting >256 events blocks forever** — that would read as a wedge, not a flake, but it must be designed in from the start.

**What could still make it flaky (and the mitigation):**
- **The harness watchdog is real wall-time.** The wedge oracle uses a generous real timeout (e.g. 5–10s) so a slow CI box doesn't false-positive. Mitigation: keep ops cheap (no jobs, instant fake adapter), set the bound well above worst-case turn cost, and run the gate corpus (not the unbounded search) under `-race` to surface data races without timing pressure.
- **Map iteration / goroutine-scheduling nondeterminism in the core.** If any oracle reads a value that depends on goroutine ordering (e.g. event *ordering*, or a timestamp), it will flake. Mitigation: oracles read only *state and transcript*, never event ordering or timestamps. The flake-guard (K=5 replays, same signature required, design §3.2) is the backstop — anything that doesn't reproduce K times is quarantined, never promoted.
- **Round-timing `time.Now()` (Tier-1 clock gap, §1.2).** Non-deterministic *event payloads* but not state. Tolerated because no oracle reads them. If a future oracle needs timing, Tier-2 clock is required first.
- **The 47K-LoC core may have latent concurrency the no-jobs scope doesn't fully avoid.** E.g. `applyPendingForceCompact`, goal-engine timers, hook runners. Mitigation: v1 config disables goals (no `SetGoal`), hooks (no `hookRunner`), and compaction-triggering pressure; if any such path proves to spawn a goroutine or sleep, scope it out and document it (same honesty contract as Phase 2's "out of reach by design").
- **`time.Sleep` in the jobs path** (`job_delegate.go:1678`, `job_shell.go:548-571`) — *the* reason jobs are out of v1. With jobs excluded these never execute.

**Acceptance for determinism:** the target survives **thousands of `rapid` checks under `-race`** with zero quarantines on a clean tree, and the injected-bug test reproduces K=5/5.

---

## 4. Build order, LoC, dependencies

Infra → target, so each piece is testable before the target depends on it.

| Step | Piece | File(s) | LoC | Depends on |
|---|---|---|---|---|
| 1 | **Deny exec env** | extend `agent/internal/agenttest/env.go` (or `agent/*_test.go`) | 80–150 | execenv interface (exists) |
| 2 | **Fuzz adapter** | new in `agent/lifecycle_seqfuzz_test.go` (Plan A) | 120–200 | `llm.ProviderAdapter`, `agenttest` builders |
| 3 | **Tier-1 determinism config** (no jobs/namer/goals/hooks; events drain helper) | same test file | 60–120 | steps 1–2 |
| 4 | **Op table + rapid machine + oracles** | same test file | 250–400 | steps 1–3 |
| 5 | **Promoter Adapter (4 hooks) + replay body + twin determinism tests** | same test file | 150–250 | step 4, `fuzz/promoter` (exists) |
| 6 | **Make/CI** — confirm `agent` module's `go test -run '^Fuzz'`/`rapid.Check` runs in the gate; add seed `f.Add`-equivalents (deterministic small corpora) | Makefile already loops `GO_MODULES` | 20–40 | step 5 |
| — | **(follow-up, not v1) Tier-2 first-class `Session` clock** | `session.go`, `session_state.go`, `session_goal.go`, `session_namer.go`, jobs wiring | 150–300 | gates the jobs target |

**v1 total: ~680–1160 LoC** (within the §8.3 ~800–1500 band; the upper end if the jobs follow-up's Tier-2 clock is pulled in).

**Dependencies:** `pgregory.net/rapid` (already a dep — used by `router_seqfuzz_test.go`), `fuzz/promoter` (already required by `agent/go.mod:14`). No new third-party deps.

---

## 5. Acceptance

- **Drives a real session offline:** the target builds a real `agent.Session` via `NewSession` (deny env, `stateDir:""`, fuzzAdapter) and runs fuzzed lifecycle sequences with zero real IO and no wall-clock dependence in the oracles.
- **Catches an injected lifecycle bug:** a deliberately broken seam (e.g. a state transition that skips `idle`, or a handler panic) is caught by the corresponding oracle, promoted once, and deduped on the second sighting; the emitted regression replays red before the fix and green after (design §6, Phases 1/2 contract).
- **Deterministic at scale:** thousands of `rapid` checks under `-race -short` with no quarantines on a clean tree; flake-guard K=5 enforced.
- **Gate-wired:** `make fuzz` (seed/deterministic mode) runs the target in the `agent` module under CI; the unbounded search stays in `fuzz-nightly`.

---

## 6. What is *not* deterministically drivable yet (honesty section)

The charter frames the three seams as if wiring them unlocks the whole turn/job lifecycle. Reading the code, that is true for the **single-session turn lifecycle** but **not yet** for the **job/subagent/delegate machinery**, for concrete reasons:

1. **Real `time.Sleep` in finalize paths.** `delegateFinalizeRetryDelay` (`job_delegate.go:1678`) and shell finalize backoff (`job_shell.go:548-571`) sleep on the real clock with no seam. Until these route through an injectable sleep (the `LLMSleep` pattern, `session_config.go:127`, generalized), any jobs target either sleeps for real (slow, and a wedge-oracle hazard) or races.
2. **Real timers and watchdogs.** `delegateFinalizeWaitTimeout` (5s), per-job `blockTimeout`, and the delegate **quiet watchdog** (`jobManager.quietWindow`/`quietCheckInterval`, `jobs.go`) run goroutines on the real clock. Deterministic control needs the Tier-2 clock threaded into the job manager *and* a way to advance it without wall-time.
3. **Goroutine fan-out.** `bridgeDelegateFinalization` (`job_delegate.go:1330`), `launchSubagentRun`, and the watch/notification rails spawn goroutines whose completion ordering feeds observable state. Quiescence detection for these is materially harder than the two benign goroutines in the no-jobs path.
4. **The clock is not a single seam.** Only `jobManager.now` is injectable, and only from inside the package; the `Session` has 27 direct `time.Now()` reads (§1.2). "Nothing depends on wall-time" is a Tier-2 refactor, not a v1 deliverable.

The defensible v1 is therefore: **the turn lifecycle, offline, no jobs** — which already exercises `ProcessInput`'s full round loop, tool execution against the deny env, steering/queue/interrupt/close, and the empty/bare-text/pause retry budgets. That is a large, previously-unfuzzed surface. The jobs machinery is a *named follow-up* gated on the Tier-2 clock + a sleep seam, not a v1 promise.

---

## 7. Open questions for Jesse

1. **How much of the job/subagent machinery in v1?** Recommendation: **none** — ship the no-jobs turn lifecycle first (§6), then do the Tier-2 clock + sleep seam as a separate reviewed change, then a *second* target for the job lifecycle. Pulling jobs into v1 risks a flaky target, which the rules say is a defect. Agree, or do you want the Tier-2 clock done up-front so v1 includes jobs?
2. **Tier-2 clock now or later?** It is the highest-LoC, highest-blast-radius piece (27 call sites across goal/namer/state). It is *not* needed for v1's oracles. Build it as the immediate follow-up (my recommendation), or front-load it?
3. **Fuzz adapter location — Plan A (package agent) vs Plan B (extend `agenttest.FakeAdapter`)?** Plan A is simpler given the target must live in package agent anyway; Plan B keeps more reusable but can't reach `Session`. I lean A.
4. **Deny-env fidelity.** Should `ExecCommand`/`ReadFile` return *fuzzed-but-bounded* deterministic outputs (explores more handler branches) or fixed canned outputs (simpler, still pure)? I lean lightly-fuzzed-but-bounded, capped for replay stability.
5. **Scope of the "no lost turns" / transcript oracle.** Compaction and goals are disabled in v1 to keep the transcript oracle clean. Is that acceptable, or do you want compaction in scope (which reintroduces `applyPendingForceCompact` timing and a shrinking-history exception the oracle must model)?
