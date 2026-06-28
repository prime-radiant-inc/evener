# 8.3 — Deterministic offline agent harness + first stateful agent-core target

**Status:** plan (decisions folded in — turnkey for execution). **Charter:** fuzzing-toolkit-design.md §8.3 (roadmap item B). **Date:** 2026-06-28.
**Branch:** `wip/fuzzing-toolkit` (worktree `.worktrees/fuzzing-toolkit`).
**Builds on:** the Phase-2 stateful pattern in `internal/appserver/router_seqfuzz_test.go`, the `fuzz/promoter` core (design §3), and the existing agent test scaffolding in `agent/internal/agenttest/` and `agent/testkit_test.go`.

This is the largest item in the roadmap. The `agent` core (~47K LoC) is unfuzzed because driving a real turn lifecycle deterministically needs three infrastructure pieces that don't exist yet as first-class, fuzz-safe seams: a fuzz-driven LLM provider, an **injectable clock**, and a sandboxed exec env. Jesse's decision is to build the **full harness to a high design bar** — the clock is a first-class deliverable (a clean, well-designed seam, not a patch), and jobs + compaction + goals are **in scope for v1**. The injectable clock is what makes the jobs/timers/watchdogs deterministic: the test advances a fake clock instead of waiting on wall time. A flaky target is still a defect, so the determinism analysis (§3) remains the spine of the plan.

---

## 0. Goal & scope

**Goal.** Stand up the offline harness (fuzz adapter + first-class clock seam + deny env) and ship a stateful `rapid` target that drives a real `agent.Session` — *including* its job/subagent/delegate lifecycle, compaction, and goal engine — through fuzzed turn-lifecycle sequences entirely offline, with failures routed through `fuzz/promoter`. Invariants weakest-first: never panic → never wedge → status monotonicity → no lost turns → transcript↔state consistency.

**In scope (v1).**
- The single-session turn lifecycle: `ProcessInput` / `Steer` / `Enqueue` / `FollowUp` / interrupt (ctx cancel) / `Close`, with the model's behavior each round drawn by the fuzzer (text, tool calls to safe tools, communicate/end-turn, empty/degenerate responses, pause).
- **Jobs / subagents / delegates** — driven by fuzzed model tool calls into the job tools, with the fake clock (§1.2) firing the finalize sleeps, the per-job timers, and the delegate **quiet watchdog** deterministically (advance-the-clock, never wall-time sleep).
- **Compaction** — the model can call the `compact` tool; the round-tail `applyPendingForceCompact` shrinks history. The transcript oracle (§2.4) models this shrink as its one sanctioned exception.
- **Goals** — `SetGoal`/`ClearGoal` ops; the goal engine's continuation timers and timestamps run on the injectable clock.

**Out of scope (v1).** Real LLM calls, real filesystem/exec, wall-clock dependence in the core, auto-commit of emitted tests (emit-to-tempdir only, per design §3.2 / Phase 2 precedent). The deny env (§1.3) and the fake clock (§1.2) replace every real IO and time source the lifecycle would otherwise touch.

---

## 1. The three infrastructure seams (grounded)

### 1.1 Programmable / fuzz-driven provider — the **hybrid** design

**Interface it must satisfy.** `llm.ProviderAdapter` (`llm/client.go:13`):

```go
type ProviderAdapter interface {
    Name() string
    Complete(ctx context.Context, req Request) (Response, error)
    Stream(ctx context.Context, req Request) (Stream, error)
}
```

Three methods. The agent core's non-streaming path calls `Complete`; `Stream` returns `llm.ErrStreamUnsupported` in every existing fake. There is also an optional capability the responses-continuation path probes for — `PlanResponsesContinuation(req) (ResponsesContinuationPlan, error)` (`llm/client.go:242`, implemented by `agenttest.FakeAdapter` at `agent/internal/agenttest/agenttest.go:68`). v1's fuzzed sessions use a profile that does not trigger continuation planning, so the fuzz adapter only needs `Complete` (returning `ErrStreamUnsupported` from `Stream`).

**What exists.** Two scripted adapters already implement the interface:
- `agenttest.FakeAdapter` (`agent/internal/agenttest/agenttest.go:27`) — `Steps []func(req llm.Request) llm.Response`, advanced one per `Complete` call, with response builders `ToolCallResponse`, `CommunicateResponse`/`FinalResponse`, `EmptyResponse`, `CommunicateCall`/`CommunicateCallArgs` (same file). This package is deliberately **agent-free** (its doc comment, lines 1–11, makes that a hard constraint to avoid a test import cycle), so it depends only on `llm`/`execenv`.
- The agent package's own `fakeAdapter` (`agent/session_test.go:41`) — the one `newSession` registers by default (`agent/testkit_test.go:73`). Identical shape (`steps []func(req) llm.Response`), package-internal.

**The decision: the hybrid seam.** Split the adapter into two layers along the same seam philosophy as the clock (interface/reusable mechanism in a shared place, the fuzzer-aware part in package `agent`):

1. **A reusable scriptable provider in `agenttest`, driven by a `Responder`.** Add to `agenttest` a `ScriptedAdapter` that holds a `Responder func(llm.Request) llm.Response` and calls it on every `Complete`. It depends **only on `llm`** (no `rapid`, no `agent`), so it stays inside the package's agent-free / dependency-light constraint and is reusable by any test. It exposes the existing response builders unchanged.
2. **The `rapid` driver + the oracles live in the package-`agent` white-box test** (`agent/lifecycle_seqfuzz_test.go`, §5). The driver draws a per-input *response script* from `*rapid.T` and supplies the `Responder` closure to the `agenttest.ScriptedAdapter`. Because the closure is constructed in the test, it can draw from `rapid` and react to the request (e.g. only emit `communicate` once tools have been offered) while the adapter mechanism itself stays `rapid`-free.

This keeps the fuzz-aware logic where it must live (package `agent`, next to the Session-state-reading oracles) while making the provider mechanism a clean, reusable `agenttest` building block. Drawing inside the `Responder` is sound: `rapid` draws are deterministic for a fixed seed, and `Complete` is called synchronously from the turn loop. The one hazard is a *second concurrent caller* of `Complete` racing the draw sequence; in v1 the namer is the only other caller, and it is disabled by config (§3 pillar 2). When jobs/subagents are in play, a subagent run gets its **own** Session with its **own** `ScriptedAdapter` + `Responder` (the rapid machine draws a distinct sub-script per spawned run), so there is never shared draw state across goroutines.

**Response vocabulary the fuzzer draws from** (each maps to a builder or a hand-rolled `llm.Response`):
1. *bare text + continue* — `llm.Response{Message: llm.Assistant(text)}` with tool calls absent → exercises the bare-text retry path (`handleNoToolCalls`, `session_lifecycle.go:688`).
2. *tool call(s) to a safe tool* — `agenttest.ToolCallResponse(ToolCall{Name:"read_file"/"glob"/..., Arguments: fuzzedJSON})` → drives `execToolBatch` against the deny env (§1.3). This is what unblocks fuzzing **handler execution**, not just validation (design §8.3).
3. *tool call(s) to a job/delegate/subagent tool* — drives `createDelegate` / `launchSubagentRun` / the watch + notification rails. The rapid machine pairs these with clock-advance ops (§2.3) so the finalize/watchdog goroutines fire deterministically.
4. *compact tool call* — `requestForceCompact` (`session_self_compact.go:120`) → round-tail `applyPendingForceCompact` (`session_self_compact.go:136`, invoked from `session_lifecycle.go:749`) shrinks history.
5. *communicate / end-turn* — `agenttest.FinalResponse(text)` or `CommunicateResponse(false, text)` (awaits reply) → the normal turn-completion path (`deliverIfCommunicated`, `session_lifecycle.go:757`).
6. *empty response* — `agenttest.EmptyResponse()` → the empty-retry budget (`maxEmptyRetries`/`maxTotalEmptyResponses`, `session_lifecycle.go:50`).
7. *pause_turn* — `Response{Finish: {Reason: llm.FinishReasonPauseTurn}}` → the pause path (`session_lifecycle.go:653`).

Size: ~80–120 LoC for the `agenttest.ScriptedAdapter` + ~120–200 LoC for the in-package rapid driver and the response-kind enum the machine samples.

### 1.2 First-class injectable clock — the foundational piece

**Decision (Jesse): front-load a first-class, cleanly-designed `Clock`, "like Rob Pike was doing it."** This is THE foundational deliverable: it is what makes the jobs / timers / watchdogs deterministic. It is built in v1, before the target depends on it — not deferred.

**The seam today is partial and ad-hoc.** The only injectable clock is on the job manager: `jobManager.now func() time.Time` (`agent/jobs.go:76`, defaulted to `time.Now` at construction, `agent/jobs.go:337`, reachable only via the two `newJobManager` call sites `session_init.go:137,373`). Test helpers reassign it from inside the package (`freezeClock`/`freezeClockAt`, `testkit_test.go:110/116`; `frozenTestTime`, `testkit_test.go:104`). But this is a single `now` func — it does **not** govern sleeps, timers, tickers, watchdogs, or the ~26 direct `time.Now()` reads scattered across the Session. A real injectable clock must own all of time.

**Design — a `Clock` interface in a small core leaf package.** Place the interface + the production wrapper in a dependency-light leaf package (e.g. `agent/internal/clock`) that **both** package `agent` and `agent/internal/agenttest` can import without a cycle (agenttest must stay agent-free, so the interface cannot live in package `agent` itself — a structurally-satisfied fake still has to *name* the `Timer`/`Ticker` return types, which would force the import). The fake, deterministically-advanceable clock lives in `agenttest`, mirroring the clock's seam philosophy: interface in core, fake in agenttest, the test advances it.

```go
// agent/internal/clock — the agent core's sole source of time.
type Clock interface {
    Now() time.Time
    Sleep(d time.Duration)
    After(d time.Duration) <-chan time.Time
    AfterFunc(d time.Duration, f func()) Timer
    NewTimer(d time.Duration) Timer
    NewTicker(d time.Duration) Ticker
}
type Timer  interface { C() <-chan time.Time; Stop() bool; Reset(d time.Duration) bool }
type Ticker interface { C() <-chan time.Time; Stop(); Reset(d time.Duration) }

func Real() Clock { /* thin wrappers over the stdlib */ }
```

`agenttest` provides a `Fake` implementing `Clock`: it holds a virtual `now`, a min-heap of pending waiters (sleeps, timers, tickers, `AfterFunc` callbacks), and two test-facing controls — `Advance(d)` (moves virtual time forward, firing every due waiter in order) and `BlockUntil(n int)` (blocks the test goroutine until `n` goroutines are parked on the clock). `BlockUntil` is the quiescence handshake: the rapid machine arms a job, waits for its watchdog/finalize goroutine to park on the clock, then `Advance`s past the deadline so it fires — deterministically, with no wall-time and no race (the standard `jonboulle/clockwork`-style pattern; we build our own minimal version, no new dependency). This is prior art well worth matching for the high design bar.

**What gets threaded onto the clock (all verified sites).** A `Session` gains a single `clock.Clock` field, set once at construction (defaulting to `clock.Real()`, injectable via `SessionConfig`), and **passed through** to `newJobManager` so `jm.now` and every job timer share the one clock. Replace:

- **The ~26 `time.Now()` reads in package `agent`** with `s.clock.Now()`:
  - state / lifecycle: `session_state.go:36`, `session_lifecycle.go:558,580,711,723,732`, `session_model_call.go:159,180,222`.
  - session/meta stamps: `session.go:679`, `session_init.go:196`, `fork.go:127`, `history_repair.go:101`, `env_info.go:25`, `session_tools.go:358`, `tool_web_fetch.go:37`.
  - goals: `session_goal.go:45,180,246`, `session_tools_goal.go:58`.
  - namer: `session_namer.go:336`.
  - jobs/subagents path: `job_delegate.go:740,1098`, `subagents.go:579,731,947`, `session_tools_jobs.go:2237`.
  - (`events/eventdata.go:28`, `schema/turn.go:55`, and the `transcript`/`internal/hooks`/`execenv` subpackages keep their own `time.Now()` — they are off the v1 oracle's read path; the no-IO config keeps `transcript`/`execenv`/`hooks` cold. Note this boundary explicitly rather than chasing every subpackage.)
- **The real sleeps** with `s.clock.Sleep` / the job manager's clock:
  - `job_delegate.go:1678` (`delegateFinalizeRetryDelay`), `job_shell.go:548,560,571` (shell finalize backoff).
  - The existing `LLMSleep llm.SleepFunc` retry seam (`session_config.go:127`) defaults to `s.clock.Sleep` so retries advance on the fake clock too.
- **The timers / tickers / watchdogs** with `clock.NewTimer`/`NewTicker`/`After`/`AfterFunc`:
  - delegate timers `job_delegate.go:220,1171,1191`; close-grace `jobs.go:412`; shell timers `job_shell.go:120,152`.
  - the delegate **quiet watchdog** ticker `job_watch.go:2655` (driven by `jm.quietWindow`/`jm.quietCheckInterval`, `jobs.go:81-86`) and the progress-timer ticker `job_watch.go:2553`.
  - subagent cancel timeout `subagents.go:883` (`time.After(5s)`); job-wait polling timers/tickers `session_tools_jobs.go:1966,1984,1986,2022,2024`; the job-notification retry `time.AfterFunc` `session.go:364`.

This is the invasive, high-blast-radius change (many files), so it is **its own reviewed commit landed first**, separate from the fuzz target — but it is firmly in v1. ~300–500 LoC (advisory). The payoff: the jobs follow-up is no longer a follow-up; jobs are deterministic in v1.

### 1.3 Sandboxed execenv — fuzzed-but-bounded deny env

**Interface.** `execenv.ExecutionEnvironment` (`agent/execenv/execenv.go`) — 13 methods: `Initialize`, `Cleanup`, `WorkingDirectory`, `Platform`, `OSVersion`, `ReadFile`, `WriteFile`, `EditFile`, `FileExists`, `Glob`, `Grep`, `ListDirectory`, `ExecCommand`. Plus the optional `StreamingExecutor` (`StreamCommand`) the job runtime type-asserts for.

**The danger.** `LocalExecutionEnvironment` (`agent/execenv/local.go:59`) runs **real commands** (`ExecCommand` forks a shell, `local.go:705`; `StreamCommand`, `:798`) and touches the real FS. Fuzzing tool-handler execution through it would run model-generated (fuzzed) shell/file ops against the host — unacceptable.

**What exists.** `agenttest.FakeEnv` (`agent/internal/agenttest/env.go:15`) is already an inert implementation: `ExecCommand` answers only `git rev-parse --show-toplevel` (when `GitRoot` set) and fails everything else; all FS ops are no-ops returning empty. It satisfies the full interface.

**Decision (Jesse): the deny env returns *fuzzed-but-bounded* deterministic outputs.** Add a **`denyEnv`** (extend `FakeEnv`, or a sibling in package agent) tuned for fuzzing:
- Every method returns a *deterministic, structured* result/error and **never** touches FS or forks a process. `ExecCommand` returns `ExecResult{ExitCode: <fuzz-drawn>, Stdout: <bounded canned>, ...}, nil` (or a deterministic error) so the tool layer's result-handling runs end-to-end. Reads/globs/greps return bounded canned content (or empty) so `read_file`/`glob`/`grep` handlers execute their full decode/format paths.
- A few outcomes (exit code, "file exists" y/n, content length, stdout body) are drawn from the fuzzer so **more handler branches are explored** — but every output is **capped in size and pure** (a function of the draw, no host state), so replay is byte-identical. The fuzz draws for the env come from the same recorded artifact as the response scripts, so replay reconstructs them exactly.
- Deliberately decide whether to implement `StreamingExecutor`: implement it (also fuzzed-but-bounded, on the clock) so the streaming shell job path is in scope, OR leave it off to keep that path cold. With jobs in v1, implement it bounded — the streaming finalize backoff now runs on the fake clock.

This is the seam that "unblocks fuzzing handler *execution*, not just validation" (design §8.3): with a deny env, the fuzzed tool-arg path runs through `execToolBatch` → the real handler → the env → result formatting, all offline and replay-stable. ~100–180 LoC.

---

## 2. The first stateful target

### 2.1 Where it lives (the import-cycle decision — load-bearing)

**Verdict: package `agent`, white-box test file `agent/lifecycle_seqfuzz_test.go`.**

Why not where Phase 2 lives: `router_seqfuzz_test.go` is in `internal/appserver`, and its own header (lines 41–47) documents that it **cannot reach the agent core** — importing `server`/`cmd` would be an import cycle, and those handlers need a live agent/LLM. The agent-core target has the inverse requirement: it must *build a real `agent.Session`*. That is only possible from:
- **package `agent`** (white-box `_test.go`) — can call `NewSession` (`agent/session_init.go:79`), reuse `newSession`/`fakeAdapter`/`freezeClock` (all package-internal), inject a `clock.Fake`, and reach unexported seams (`jobManager`, `s.state`, `s.history`, `s.turns`, `s.modelResponses`) the oracles need.
- **not `agenttest`** — it is constitutionally agent-free (cannot import `agent`). The reusable `ScriptedAdapter`, `FakeEnv`/`denyEnv`, and the `clock.Fake` live here; the rapid driver does not.
- **not a black-box `agent_test` package** — it could call the exported API but could not read the unexported state the transcript↔state oracle needs, nor inject the clock onto the job manager.

Build wiring is already in place: **`agent/go.mod` already requires `primeradiant.com/serf/fuzz`** (`agent/go.mod:14`), so the target imports `fuzz/promoter` with no new dependency — exactly as `internal/appserver` does. `fuzz` is in `go.work` and (per design §1) in the Makefile `GO_MODULES`, so the seed corpus runs under the gate.

### 2.2 The control surface the model drives

Public + package-internal methods the op table calls (all verified):
- `ProcessInput(ctx, text, images)` (`session_lifecycle.go:254`) — runs one input to completion (synchronous; loops rounds internally).
- `Steer(msg)` (`session_queue.go:62`) — inject steering (delivered at the next turn boundary or mid-turn).
- `FollowUp(msg)` (`session_queue.go:144`) — per-turn follow-up queue.
- `Enqueue(ctx, text)` (`session_queue.go:169`) — queue a user message for the drain loop.
- `SetGoal(ctx, objective)` (`session_goal.go:32`) / `ClearGoal()` (`session_goal.go:63`) / `GoalStatus()` (`session_goal.go:74`) — the goal engine; continuation timers (`armGoalContinuation`, `session_goal.go:158`) run on the injected clock.
- Job/subagent/delegate ops are driven **through fuzzed model tool calls** (response vocabulary #3/#4), not direct Session methods, so the surface is the tool registry; the rapid machine pairs each spawn with a `opAdvanceClock` op (§2.3) to fire watchdogs/finalize.
- Observers: `State()` (`session_state.go:24`), `QueueDepth()`/`QueuePreview()` (`session_queue.go:271/284`).
- `Close()` (`session_lifecycle.go:73`).
- **Interrupt** is modeled by passing a *cancellable* ctx to `ProcessInput` and cancelling it mid-turn; the loop's `isTurnCancellation` path (`session_lifecycle.go:333`) applies interrupt semantics (flip to idle, append the interrupt system-reminder, optionally drain the queue head).

### 2.3 The model (declarative op table, mirroring Phase 2)

Follow the Phase-2 shape exactly: a declarative `opTable` (the reusable artifact, design §7 #5) + a thin `rapid` machine that draws ops. Each op declares how to apply it and what the monotonic model predicts about `State()`/counters.

Op set (v1):
| op | action | model prediction |
|---|---|---|
| `opProcessInput` | `ProcessInput(boundedCtx, drawnText, nil)` with the fuzzer scripting the round responses (may include job/compact/communicate kinds) | ends `SessionIdle` (or `SessionClosed`); `turns`↑ if accepted; `modelResponses`↑ |
| `opProcessInterrupted` | `ProcessInput` with a ctx cancelled after a drawn round | ends `SessionIdle` unless closed; transcript gains an interrupt marker; no panic |
| `opSteer` | `Steer(drawnText)` | enqueues steering; no state regression |
| `opEnqueue` | `Enqueue(ctx, drawnText)` | `QueueDepth`↑ |
| `opFollowUp` | `FollowUp(drawnText)` | per-turn followup recorded |
| `opSetGoal` / `opClearGoal` | `SetGoal`/`ClearGoal` | goal state set/cleared; no state regression |
| `opAdvanceClock` | `fakeClock.BlockUntil(n)` then `Advance(drawnDuration)` | fires due timers/watchdogs/finalize-sleeps; jobs reach terminal state deterministically |
| `opClose` | `Close()` | terminal: `State()==SessionClosed`, idempotent |
| `opObserve` | read `State()`/`QueueDepth()`/`GoalStatus()` | pure; must not panic or mutate |

The fuzzer also draws, *per `ProcessInput` op*, a short script of round-response kinds (§1.1 vocabulary) the `Responder` will play, plus the deny-env draws for that run. So one drawn artifact = (op sequence) × (per-input response scripts) × (interrupt timing) × (clock-advance durations) × (env draws). All of it is recorded for replay.

### 2.4 Oracles (weakest-first)

1. **Never panic (floor).** The turn loop does not blanket-`recover` (it `recover()`s only to flush meta then re-panics, `session_lifecycle.go:500`). Run each op under a recovering goroutine (like `safeHandle`, `router_seqfuzz_test.go:329`) so a panic is captured, classified, and turned into a `promoter.Failure{Oracle: Panic}` with a project-relative stack (reuse `captureStack`, `router_seqfuzz_test.go:474`). With jobs in scope, a panic in a spawned job/watchdog goroutine must also be caught — wrap the job-goroutine entry points the harness can reach, or treat an unrecovered job-goroutine panic (crashing the test binary) as the strongest possible signal.
2. **Never wedge.** Every op returns under a bounded context + a wall-clock watchdog goroutine (the harness's own real-time timeout, distinct from the in-session fake clock). A `ProcessInput` or `opAdvanceClock` that never returns is a `Wedge` failure. The harness watchdog is deliberately real-time so it survives even if an in-session timer misbehaves.
3. **Status monotonicity.** `State()` follows the legal lattice: `idle ⇄ active → closed` (closed is terminal; `active` only inside a `ProcessInput`; observed state at op boundaries is `idle` or `closed`). Reuse the Phase-2 "kind divergence from a monotonic model" technique: a regression to a pre-`idle`/post-`closed` state is an `Invariant` failure.
4. **No lost turns.** Every *accepted* user input (one not rejected by `MaxTurns`, `session_lifecycle.go:792`) appears as a `schema.TurnUserInput` in the transcript, and `turns`/`modelResponses` are monotonic non-decreasing across the sequence. A model that counts accepted inputs and compares to transcript user-turns catches a dropped turn.
5. **Transcript↔state consistency (strongest).** `len(s.history)` never *decreases* **except across a compaction** — the one sanctioned shrink. The oracle tracks compaction events (the `compact` tool was called → `applyPendingForceCompact` ran, `session_self_compact.go:136`, replacing `s.history` with the compacted copy at `session_self_compact.go:152`) and excepts exactly that boundary: after a compaction, history may be shorter, but it must still be well-formed (no dangling tool call — `repairOrphanedToolResults` is the production guard, `history_repair.go:107/109`; assert its post-condition). Outside compaction, `len(s.history)` is non-decreasing, `modelResponses` ≥ number of completed `ProcessInput`s, and every tool call has its result. This is the strongest oracle and the likeliest to surface a real bug.

Failures route through `fuzz/promoter` exactly as Phase 2 does (§2.5).

### 2.5 Promoter wiring (reuse the Phase-2 Adapter shape)

Implement `promoter.Adapter` (`fuzz/promoter`, four hooks) for this surface, copying the `seqAdapter` structure (`router_seqfuzz_test.go:368`):
- **Artifact:** `{opCodes []int, responseScripts [][]int, envDraws [][]int, clockAdvances []int64, interruptStep int, seed?}` — JSON, minimized by `rapid` already (Minimize is passthrough).
- **Signature:** `(Oracle, topN stack frames)` for panics; `(Oracle, Detail)` otherwise (`ShortHash` fallback) — same as `seqAdapter.Signature`.
- **Replay:** a single `seqOracleRun(artifact)` that rebuilds a *fresh* `Session` (deny env, injected `clock.Fake`, `ScriptedAdapter` replaying the recorded scripts, recorded env draws) and re-runs — the single source of truth so the live property and Replay classify identically (the Phase-2 discipline). The fake clock makes this fully deterministic even with jobs.
- **Emit:** `promoter.WriteGoTest` to a tempdir with a `ReplayBody` calling a package-local `replayLifecycleArtifact(t, json)`. Promotion into the tree stays the human/opt-in step.

Add the twin determinism tests Phase 2 has: a deterministic *injected* failure promotes once + dedups (`TestSeqAdapter_PromotesDeterministicFailure` analogue — e.g. register a tool whose handler panics, or feed a response the loop mishandles), and a *flaky* failure is quarantined (never emitted). These are the most important tests in the item (design §6).

---

## 3. Determinism risks — the crux (now incl. jobs)

A flaky target is a defect. The injectable clock (§1.2) is precisely what makes the jobs/timers/watchdogs deterministic — instead of wall-time sleeps and races, the test advances the fake clock at chosen points (`opAdvanceClock`) after a `BlockUntil` handshake. Spell out exactly what is made deterministic and what could still wobble.

**Made deterministic (the pillars):**
1. **Seeded `rapid`.** `rapid.Check` is seed-driven; the `Responder` and the env both draw from the same `*rapid.T`, so the entire (ops × response-scripts × env-draws × clock-advances × interrupt-timing) artifact is reproducible from the seed. Replay reconstructs from the *recorded* artifact, not the seed, so it is independent of `rapid` internals.
2. **No real IO.** Deny env (§1.3): no FS, no subprocess. `stateDir == ""` for the root session: no jobstore-file IO, no transcript-file IO, no ATIF export, no namer (gated off by `stateDir == ""` *and* an unconfigured cheap model — `launchInitialPromptNamer`, `session_namer.go:186` returns early on both). A default `NewOpenAIProfile` with no cheap model double-guarantees the namer goroutine never launches — removing the one concurrent `Complete` caller that would otherwise race the draw sequence. (Jobs still run in-memory with `stateDir == ""`; the durable jobstore is what's off, not the job lifecycle.)
3. **The fake clock owns all time → jobs are deterministic.** Every sleep, timer, ticker, watchdog, and `AfterFunc` in the jobs/delegate/subagent paths (§1.2 site list) runs on `clock.Fake`. No goroutine waits on wall time. The rapid machine uses `BlockUntil(n)` to wait until the job goroutines are parked on the clock, then `Advance`s past their deadlines so finalize/quiet-watchdog/retry fire in a single, ordered, reproducible step. This is the mechanism that turns "jobs spawn real `time.Sleep` and racy watchdogs" into "advance the fake clock."
4. **Bounded contexts.** Every op runs under a `context.WithTimeout`; interrupts are explicit ctx cancels at a drawn step. No op blocks forever on the model (the `Responder` returns synchronously).
5. **Goroutine quiescence detection.** Known background goroutines now include: (a) the per-`ProcessInput` ctx-link goroutine (`session_lifecycle.go:510`, exits via `defer cancel()`); (b) the **events drain** the harness must run; (c) **job/subagent/watchdog goroutines**. Quiescence is the conjunction of three signals: the events drain is caught up, `BlockUntil` reports all timed goroutines parked on the fake clock, and the job manager reports its runs terminal. The harness drains `s.Events()` (`session_events.go:14`, buffered 256, `session_init.go:129`) in a dedicated goroutine for the whole `rapid.Check` body and joins it after `Close()` (Close closes the channel after `sendersWG.Wait()`/`toolEventsWG.Wait()`, `session_lifecycle.go:170-181`). **If the harness does not drain events, a turn emitting >256 events blocks forever** — designed in from the start; with jobs, event volume is higher, so draining is non-negotiable.

**What could still make it flaky (and the mitigation):**
- **The harness watchdog is real wall-time.** The wedge oracle uses a generous real timeout (e.g. 5–10s) so a slow CI box doesn't false-positive. Mitigation: keep ops cheap (fake adapter is instant, all time is virtual), set the bound well above worst-case, run the gate corpus under `-race` to surface data races without timing pressure.
- **Job-goroutine scheduling order feeding observable state.** With jobs, completion *ordering* of concurrent runs can vary. Mitigation: oracles read only *state, transcript, and terminal job status* — never event ordering or wall-time timestamps. Where the `opAdvanceClock` fires several due waiters, the fake clock fires them in deterministic heap order (deadline, then insertion order). The flake-guard (K=5 replays, same signature, design §3.2) is the backstop — anything that doesn't reproduce K times is quarantined, never promoted.
- **A timed goroutine the harness forgot to wait for before `Advance`.** If `Advance` runs before a job goroutine parks on the clock, the timer is missed and the run wedges. Mitigation: always `BlockUntil(expectedParked)` before `Advance`; if the parked count is unknown, advance in small steps and re-check. This handshake is the single subtlest part of the jobs integration — build and test it in isolation first (step 2b, §4).
- **Compaction timing.** `applyPendingForceCompact` runs synchronously at the round tail (`session_lifecycle.go:749`), not on a timer, so it is already deterministic; the only oracle subtlety is modeling the history-shrink exception (§2.4 #5), not timing.
- **Latent concurrency outside the modeled set** (hooks, plugin runners). Mitigation: v1 config disables hooks (no `hookRunner`); if any path proves to spawn an unmodeled goroutine or a wall-time wait the clock doesn't cover, scope it out and document it (same honesty contract as Phase 2's "out of reach by design").

**Acceptance for determinism:** the target survives **thousands of `rapid` checks under `-race`** with zero quarantines on a clean tree, and the injected-bug test reproduces K=5/5.

---

## 4. Build order, LoC, dependencies

Infra → target, so each piece is testable before the target depends on it. The clock lands first as its own reviewed commit.

| Step | Piece | File(s) | LoC (advisory) | Depends on |
|---|---|---|---|---|
| 1 | **First-class `Clock` seam** — interface + `Real()` in `agent/internal/clock`; thread onto `Session` + `jobManager`; replace the ~26 `time.Now()` + all sleeps/timers/tickers/watchdogs (§1.2) | `agent/internal/clock/*.go`, `session*.go`, `jobs.go`, `job_*.go`, `subagents.go`, `session_config.go`, `session_init.go` | 300–500 | execenv/llm (exist) |
| 1b | **Fake clock** (Advance + BlockUntil) | `agent/internal/agenttest/clock.go` | 120–200 | step 1 |
| 2 | **Deny exec env** (fuzzed-but-bounded) | extend `agent/internal/agenttest/env.go` | 100–180 | execenv interface (exists) |
| 2b | **Clock-advance handshake helper** (BlockUntil/Advance pairing for jobs) | `agent/lifecycle_seqfuzz_test.go` | 40–80 | steps 1b, 2 |
| 3 | **`ScriptedAdapter` (Responder)** in agenttest + in-package rapid driver | `agent/internal/agenttest/agenttest.go`, `agent/lifecycle_seqfuzz_test.go` | 200–320 | `llm.ProviderAdapter`, builders |
| 4 | **Determinism config** (no namer/hooks; in-memory jobs; events drain helper) | test file | 60–120 | steps 1–3 |
| 5 | **Op table + rapid machine + oracles** (incl. job/compact/goal ops + clock-advance op) | test file | 350–550 | steps 1–4 |
| 6 | **Promoter Adapter (4 hooks) + replay body + twin determinism tests** | test file | 180–280 | step 5, `fuzz/promoter` (exists) |
| 7 | **Make/CI** — confirm the `agent` module's `rapid.Check` runs in the gate; add deterministic small seed corpora | Makefile already loops `GO_MODULES` | 20–40 | step 6 |

**v1 total: ~1370–2270 LoC** (above the §8.3 original ~800–1500 band because the clock refactor + jobs are now in v1 by decision; LoC is advisory). The clock (step 1) is the single biggest and highest-blast-radius piece — land and review it independently before anything depends on it.

**Dependencies:** `pgregory.net/rapid` (already a dep — used by `router_seqfuzz_test.go`), `fuzz/promoter` (already required by `agent/go.mod:14`). No new third-party deps (the fake clock is hand-rolled, ~`clockwork`-style).

---

## 5. Acceptance

- **First-class clock landed:** a `Clock` seam owns all of time in the agent core — the ~26 `time.Now()` reads and every sleep/timer/ticker/watchdog in the jobs paths route through it; production defaults to `clock.Real()`; tests inject `clock.Fake`. Verified by `grep` showing no direct `time.Now()`/`time.Sleep`/`time.NewTimer` left on the lifecycle + jobs read paths enumerated in §1.2.
- **Drives a real session offline, incl. jobs/compaction/goals:** the target builds a real `agent.Session` via `NewSession` (deny env, injected fake clock, `ScriptedAdapter`, in-memory jobs) and runs fuzzed lifecycle sequences — turn loop, tool execution, job/subagent/delegate spawn + finalize + quiet watchdog (fired by clock-advance), compaction, and goals — with zero real IO and no wall-clock dependence in the oracles.
- **Catches an injected lifecycle bug:** a deliberately broken seam (e.g. a state transition that skips `idle`, a handler panic, or a compaction that drops a tool result) is caught by the corresponding oracle, promoted once, and deduped on the second sighting; the emitted regression replays red before the fix and green after (design §6, Phases 1/2 contract).
- **Deterministic at scale:** thousands of `rapid` checks under `-race -short` with no quarantines on a clean tree; flake-guard K=5 enforced; the clock-advance handshake demonstrably makes a job's quiet-watchdog fire reproducibly.
- **Gate-wired:** `make fuzz` (seed/deterministic mode) runs the target in the `agent` module under CI; the unbounded search stays in `fuzz-nightly`.

---

## 6. What is *not* deterministically drivable yet (honesty section, revised)

Jesse's decision pulls the job/subagent/delegate machinery, compaction, and goals **into v1**, enabled by the first-class clock. The original "no-jobs" framing is therefore retired. What remains genuinely out of reach, honestly:

1. **Real-IO subpackages, by design.** The `transcript`, `internal/hooks`, and `execenv/local` subpackages keep their own `time.Now()`/sleeps and do real IO. v1 keeps them cold (no `stateDir`, deny env, no hooks) rather than threading the clock into them — they are not on the oracle's read path. If a future target wants durable-jobstore or hook fuzzing, the clock thread extends there as a scoped follow-up.
2. **Streaming exec fidelity.** The deny env's `StreamingExecutor` returns bounded canned streams on the fake clock; it does **not** reproduce real subprocess streaming semantics (partial reads, backpressure, signal handling in `execenv/local.go`). That is a fidelity gap, not a determinism gap — flagged so no one mistakes a green streaming-job fuzz for coverage of real `StreamCommand`.
3. **The clock-advance handshake is the load-bearing subtlety.** Determinism of jobs hinges entirely on the harness pairing `BlockUntil` with `Advance` correctly (§3). If a job path parks on something *other* than the clock (a raw channel with no timeout, an unmodeled goroutine), the handshake can't see it and the run can wedge. Step 2b builds and tests this in isolation precisely because it is the one place a subtle flake could hide. Any job path discovered to wait off-clock gets the clock threaded in (extending §1.2) or is scoped out with a written note.
4. **Plugin / coordinator-workflow goroutines** (`coordinator_workflow_plugin.go`) are not modeled in v1; if the fuzzer can reach them via a tool call, either disable them in the v1 config or extend the quiescence set in a follow-up.

The defensible v1 is therefore: **the full single-session lifecycle + jobs + compaction + goals, offline, on a fully injectable clock** — a large, previously-unfuzzed surface. The named follow-ups (durable jobstore, hooks, real-streaming fidelity, plugins) are scoped extensions of the same three seams, not blockers.

---

## 7. Decisions (resolved by Jesse — recorded for execution)

These were the original open questions; Jesse has decided them. Recorded verbatim so the plan is turnkey.

1. **How much of the jobs machinery in v1?** **All of it** — the job/subagent/delegate lifecycle is in v1, enabled by the first-class clock.
2. **Clock now or later?** **Now — front-loaded as a first-class deliverable**, a cleanly designed `Clock` interface (not a patched `freezeClock`), threaded through the whole core and the jobs paths. It is step 1, its own reviewed commit.
3. **Fuzz adapter location?** **The hybrid design:** a reusable `ScriptedAdapter` taking a `Responder` in `agenttest` (depends only on `llm`, no `rapid`); the rapid driver + the Session-state-reading oracles in the package-`agent` white-box test. Same seam philosophy as the clock (interface/mechanism in core, fake in agenttest, the test drives it).
4. **Deny-env fidelity?** **Fuzzed-but-bounded deterministic outputs** (capped sizes, drawn from the fuzzer) so more handler branches are explored while replay stays byte-identical.
5. **Compaction & goals in scope?** **Yes, both in v1.** The transcript oracle models compaction's history-shrink as its one sanctioned exception; the goal engine's timers run on the injectable clock.
</content>
</invoke>
