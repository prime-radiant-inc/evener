# Agent Session actor-core — design (for sign-off before implementation)

**Status:** PROPOSAL — awaiting Jesse's review. No code changes until the design
is signed off AND the golden behavior tests below are written and green against
the *current* implementation.

**Goal:** make the agent core's concurrency *correct by construction* instead of
correct-by-documented-discipline — the single biggest "Pike-grade" lever in the
codebase — without changing any externally observable behavior.

## 1. The problem (today)

`Session` is a ~60-field shared-mutable object driven by **one** turn-loop
goroutine (`ProcessInput` → `processOneInput`) while **~16 external methods**
(`SetModel`, `SetReasoningEffort`, `SetTimeout`, `Steer`, `Enqueue`,
`DrainAsSteer`, `Compact`, `Close`, plus the `QueueDepth`/`ContextPressure`/
`DetailedStatus`/`Tasks`/`State`/… readers) hit it concurrently from RPC handler
goroutines (`cmd/serf/serve.go`). Correctness rests on hand-maintained discipline:

- `mu` guards **~25 enumerated fields**; lock order `responseSideEffectsMu > mu`,
  `queueEventsMu > mu` is documented prose.
- `SetModel` does a **lock → unlock-for-network-I/O → re-lock → re-check-closing**
  dance (it can't hold `mu` across the model-resolve HTTP call).
- The per-round "snapshot profile/cache under `mu`" pattern (the A4 race fix) is
  manual and easy to forget when adding a new loop reader.
- `Close` is a 9-step ordering ballet using **two WaitGroups + an RWMutex + a
  bool flag + a `sync.Once`** purely to close one channel without a send race.

It works and is race-clean today. It is not what you demo as exemplary Go.

## 2. The design

### 2.1 Ownership split

Partition `Session` state into three tiers by *who may touch it*:

1. **Loop-owned (no lock).** Mutated only by the turn-loop goroutine:
   `history`, `state`, `turns`, `modelResponses`, `totalRounds`,
   `sessionEndEmitted`, `closing`, `profile` (+ derived `cachedSystemPrompt`,
   `cachedToolDefs`), the mutable `cfg` knobs (`ReasoningEffort`, command
   timeouts, `MaxToolRoundsPerInput`), the `communicate*` transient fields, the
   `name*` fields, the task-reminder counters, `loopDetectionCount`,
   `readOnlyStreak`, `depth`. → **`mu` and `responseSideEffectsMu` are deleted.**
2. **Concurrent queues (small, local lock — kept honestly).** `inputQueue` and
   `steeringQueue` are genuine producer/consumer structures: external callers
   push (and `Enqueue` must return "closed?" *immediately*, even while the loop
   is blocked in a 30 s stream), the loop pops at safe points. These stay behind
   a single small `queueMu` (replacing `queueEventsMu`), or become buffered
   channels — TBD in §2.5. This is idiomatic, local, and *not* the fragile part.
3. **Self-synchronizing collaborators (unchanged).** `reg` (ToolRegistry),
   `taskStore` (may be shared with child sessions), `transcript`, `contextMgr`,
   `subagents`, `mcpMgr` already own their locks. The actor core does **not**
   touch them.

> Honest note: this is **not** "zero locks." It deletes the two fragile Session
> locks (`mu`, `responseSideEffectsMu`) and the lock-dance + per-round-snapshot
> patterns, and replaces them with one small queue lock. The *fragile core*
> becomes structural; the *local, easy* concurrency stays a lock.

### 2.2 Commands (external mutators → the loop)

`SetModel`, `SetReasoningEffort`, `SetTimeout` mutate loop-owned state. They
become **commands** delivered to the loop and applied by the owning goroutine at
a **safe point**:

- A buffered `commands chan func()` (or a typed command sum) on the Session.
- The external method validates cheaply, then sends the command and returns
  (fire-and-forget for the void setters).
- `SetModel`'s HTTP model-resolve runs **off-loop** (in the caller), and only the
  pure `profile` swap + cache rebuild is the command the loop applies — the
  lock-dance disappears entirely.
- The loop drains `commands` at the existing safe points: top of each round and
  after tool execution (exactly where `drainSteering` is called today).

### 2.3 Queries (external readers → published snapshot)

The crux: the UI reads `QueueDepth`/`QueuePreview`/`ContextPressure`/
`ContextMetrics`/`DetailedStatus`/`Tasks`/`State`/name **live, during a 30 s
streaming LLM call.** A mailbox-only actor would block these until the loop
returns — unacceptable. So:

- The loop publishes an **immutable `*sessionView`** via `atomic.Pointer` after
  every change to loop-owned readable state (state transition, profile swap,
  counter bump, name update).
- Readers do `view := s.view.Load()` — **lock-free, instant, even mid-stream.**
- Queue depth/preview come from the snapshot too (the loop republishes after
  draining/pushing); collaborator-derived reads (`ContextMetrics`, `Tasks`)
  continue to read the self-synchronized collaborator directly.

This is the part that makes it a *real* design rather than "slap a channel on it":
mailbox for writes **+** atomic published snapshot for reads.

### 2.4 Close

`Close` keeps its `sync.Once`, but: it sets a closed atomic flag, **cancels the
session context** (aborting any in-flight LLM call so the loop returns promptly),
and signals the loop to stop. The loop, on exit, performs teardown and closes the
events channel as its *last* act — so the `eventsMu`/`eventsClosed`/`sendersWG`/
`toolEventsWG` ballet collapses to "the only emitter is the loop; it closes the
channel when it stops." (Detached emitters — subagent runs, the namer — are
already cancelled by `cancelFunc` + child `Close`; they join before the loop
exits.) Exact teardown ordering (hooks, ATIF export, SESSION_END dedup) is
preserved.

### 2.5 Open design questions (resolve during sign-off)

- **Queues: small lock vs. channels.** A `queueMu` + slices is the lowest-risk,
  exact-semantics-preserving choice (the `QueueChanged` emit + snapshot update
  stay synchronous with the mutation, as today). Buffered channels are "purer"
  but introduce a publish lag between `Enqueue` returning and the snapshot
  reflecting it — a behavior change. **Recommendation: keep a small `queueMu`.**
- **`Compact`** is caller-invoked and mutates history mid-life; it likely becomes
  a command (applied at a safe point) so it can't race the loop.
- **Command-with-result ops** that must return promptly (`Enqueue`,
  `DrainAsSteer` return errors) are served by the queue path (§2.1 tier 2), NOT
  the loop mailbox, precisely to avoid blocking on a busy loop.

## 3. What does NOT change (the de-risk)

**Every public method keeps its exact signature and externally observable
behavior.** `SetModel(string)`, `Steer(string)`, `Enqueue(ctx,string) error`,
`Close()`, `ProcessInput(...)`, all readers — unchanged. The rewrite is **internal
to the `agent` package**; `cmd/serf/serve.go`, the server, the TUI, and all
existing tests compile and behave identically. That bounds the blast radius and
is what makes an XL rewrite of the most delicate code defensible.

## 4. Subtle semantics that MUST be preserved (and golden-tested first)

These live today only in code + kata comments. Golden tests pin each against the
*current* implementation **before** the rewrite; they must stay green through it.

1. **Interrupt (kata 0ax1):** a cancelled turn flips back to `Idle` (unless
   closed), appends the `<SYSTEM-REMINDER>` interrupt turn, emits one
   `SESSION_END{interrupted}`, and may drain the queue head.
2. **`queuedInputDrainContext`:** bare `context.Canceled` drains; an `*AbortError`
   drains only when *this* turn's ctx was canceled; `DeadlineExceeded` never
   drains; `rootCtx` error blocks the drain.
3. **Steer-mid-turn:** `Steer`/`SteerWithImages` injects after the current tool
   round; `drainSteering` between rounds appends `TurnSteering` + emits
   `SteeringInjected`.
4. **Queued input as fresh turns (kata 111a):** each `popQueueHead` becomes a
   distinct user turn after the active turn completes; `QueueChanged` fires on
   every mutation with correct depth/preview.
5. **Drain-as-steer (kata 0bq1):** requires `state == Processing`; appends
   optional input, joins the whole queue with `\n\n`, emits one combined `Steer`,
   one `QueueChanged`; errors on empty queue / no active turn / closed.
6. **SESSION_END dedup:** emitted exactly once across `ProcessInput`, the
   interrupt path, and `Close` (`sessionEndEmitted`).
7. **Closed-session refusal:** every mutator no-ops/errors once closing.

## 5. Plan & gating

1. **Write the golden tests (§4) against current `main`; verify green.** ← first.
   If these are gnarly/underspecified to write, that's the signal to pause and
   reconsider scope.
2. Sign-off on this design (esp. §2.5) — Jesse.
3. Implement behind the unchanged public API (§3). `-race` + the golden tests +
   the full suite prove equivalence.
4. 3× opus review panel on the diff (concurrency correctness, semantics
   preservation), plus my independent verification.
5. The Session-slimming (extracting turn-private / naming / reminder sub-state)
   falls out naturally and can follow as a separate, lower-risk pass.

## 6. Honest risk assessment

Highest-risk change in the codebase: it rewrites the heart of the turn loop, and
a regression is user-visible (dropped steering, lost queued input, wrong
interrupt behavior). Mitigations: behavior-preserving by construction (public API
unchanged), golden tests written first, `-race` gate, panel review. The current
model is *not broken* — this is a deliberate reach for the world-class bar, justified
only because the agent core is where that bar is won.
