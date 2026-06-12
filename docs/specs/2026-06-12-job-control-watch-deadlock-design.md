# Job-Control Watch Deadlock Design Note

Date: 2026-06-12

## Purpose

This note documents a real stuck Serf session, the likely root cause, and several possible solution shapes. It is intended for second-opinion review before implementation.

The central question is not just how to fix one deadlock. The design should make this class of deadlock structurally impossible, or at least hard to reintroduce.

## Incident Summary

Session `01KTWN9KEHZ041D77B3GKK572M` got stuck while running a job-control smoke test.

The last persisted transcript entry is an assistant tool call:

- transcript seq: `67`
- timestamp: `2026-06-12T01:03:41.998187Z`
- tool: `job_read_output`
- target job: `job_01KTWNP67GH9PFD7M6WFZJHFAB`
- args included `block=true` and `block_timeout_ms=2000`

There is no following `TOOL_RESULTS` entry.

The target shell job itself was not stuck. The job journal shows:

- `job_started` at `2026-06-11T18:03:35.664736-07:00`
- `job_finished` at `2026-06-11T18:03:35.894626-07:00`
- status `completed`
- reason `exit_zero`
- output bytes `16`

The output file contains:

```text
event-watch-job
```

So `job_read_output(block=true)` should have returned quickly. The stuck session is best explained by the session loop wedging before tool execution or before tool-result persistence.

## Relevant Preceding Watch

Immediately before the stuck point, the smoke test installed a mixed event watch:

```json
{
  "target": "caller",
  "events": ["assistant.message", "job.notification"],
  "trigger": {
    "event": "assistant.message",
    "every": 2
  },
  "send": {
    "to": "caller",
    "message": "Mixed event watch fired.",
    "include_frame": true,
    "include_excerpt": true
  }
}
```

The watch was accepted and persisted as transcript seq `61`/`62`.

After the shell job completed, the job journal recorded two pending watch sends:

1. A `JOB_FINISHED` watch send.
2. An `ASSISTANT_TEXT_END` watch send at the same timestamp as the final assistant tool-call turn.

Both watch frames attempted to include an excerpt for `job_id: caller` and recorded:

```text
output_read_error: job "caller" not found
```

That excerpt issue is a real bug, but it is probably adjacent rather than the primary cause of the hang.

## Root Cause Hypothesis

The current design allows synchronous re-entry from session event emission into session mutation:

```text
Session emits assistant event
  -> jobManager observes event
    -> jobManager delivers watch send
      -> send.to="caller"
        -> Session attempts to append/steer caller message
```

That creates a cycle:

```text
Session -> jobManager -> Session
```

The specific deadlock path appears to be:

1. `emitAssistantResponse` runs under `withResponseSideEffects`.
2. `withResponseSideEffects` holds `responseSideEffectsMu`.
3. While still inside that critical section, `emitAssistantResponse` calls `s.emit(EventAssistantTextEnd, ...)`.
4. `Session.emit` calls `jobManager.onSessionEvent`.
5. `jobManager.onSessionEvent` computes matching watch sends and calls `deliverWatchSends` synchronously.
6. The watch send targets `caller`.
7. Caller delivery routes through `sendDelegateMessage`.
8. Watch-originated caller delivery calls `deliverWatchCallerMessageAtBoundary`.
9. `deliverWatchCallerMessageAtBoundary` attempts to lock `responseSideEffectsMu`.
10. The same goroutine already holds `responseSideEffectsMu`.

Because `sync.Mutex` is not re-entrant, this wedges the session before the pending `job_read_output` tool result can be written.

Key code paths:

- `agent/session_model_call.go`: `emitAssistantResponse`
- `agent/session_state.go`: `withResponseSideEffects`
- `agent/job_watch.go`: `jobManager.onSessionEvent`
- `agent/job_delegate.go`: `sendDelegateMessage` for runtime alias `caller`
- `agent/session_queue.go`: `deliverWatchCallerMessageAtBoundary`

## Design Smell

The bug is not that one mutex was acquired in the wrong place. The deeper issue is that event observers can synchronously perform arbitrary side effects against the event source.

That makes deadlocks a discipline problem:

- every new event observer must know which locks may be held by the emitter
- every delivery path must avoid calling back into the session at the wrong time
- every recursive parent/child session path multiplies those constraints

The safer design rule is:

```text
Events produce intent. Safe boundaries perform effects.
```

Or more concretely:

```text
Session.emit may publish an event and let job/watch code record durable intent.
Session.emit must not synchronously deliver messages, steer sessions, resume delegates, or call back into session mutation.
```

## Goals

- Make this deadlock class structurally impossible.
- Preserve durable watch-send recovery behavior.
- Keep watch delivery ordering understandable.
- Keep agent-facing API behavior simple.
- Avoid a broad runtime rewrite unless the added safety clearly justifies it.
- Leave room for recursive subagents.

## Non-Goals

- Do not replay arbitrary historical event watches.
- Do not add multiple synonyms or compatibility modes.
- Do not paper over the problem with sleeps or retry loops.
- Do not solve by making all locks global or coarse.

## Solution Shapes

### Option 1: Minimal Lock Reordering

Change the current call sites so this exact path does not reacquire `responseSideEffectsMu`. For example, move some `emit` calls outside `withResponseSideEffects`, or special-case caller watch delivery when already at a boundary.

Pros:

- Smallest code change.
- Could fix the observed deadlock quickly.

Cons:

- Leaves the cyclic architecture intact.
- Future events can reintroduce the same class of bug.
- Hard to reason about because correctness depends on informal lock-state knowledge.

Assessment: not recommended except as a temporary hotfix.

### Option 2: Asynchronous Watch Delivery Goroutine

Keep `onSessionEvent` mostly as-is but replace synchronous delivery with a goroutine:

```go
go jm.deliverWatchSends(context.Background(), deliveries)
```

Pros:

- Likely unblocks this deadlock.
- Small code change.

Cons:

- Creates ordering races.
- Creates shutdown and lifetime races.
- Delivery timing becomes nondeterministic.
- Can still race with transcript/tool-result boundaries.
- Harder to test and reason about.

Assessment: not recommended. This trades a deadlock for less deterministic behavior.

### Option 3: Strict Lock Hierarchy

Define a lock order, audit all session/job/watch/delegate paths, and enforce that no path acquires locks out of order.

Example hierarchy:

```text
responseSideEffectsMu > Session.mu > jobManager.mu
```

Pros:

- Addresses the deadlock class more directly than options 1 or 2.
- Does not require changing the major runtime shape.

Cons:

- Go will not enforce the hierarchy.
- Synchronous cross-component callbacks still exist.
- Parent/child session recursion makes global lock ordering hard.
- The hierarchy itself becomes tribal knowledge.

Assessment: better than a local patch, but still brittle.

### Option 4: Durable Watch Outbox With Boundary Drain

Split watch handling into two phases:

1. Event observation records durable intent.
2. Explicit session boundaries perform delivery.

The event path becomes:

```text
Session.emit
  -> jobManager.onSessionEvent
    -> persist/coalesce watch_send_pending
    -> return
```

Delivery happens later:

```text
Session safe boundary
  -> drain pending watch sends
    -> deliver to caller or delegate
    -> mark delivered, keep pending, or drop with reason
```

Safe drain boundaries include:

- after tool results are persisted
- before preparing the next model request
- when entering idle
- after restore/recovery
- possibly after notification turns, if that boundary is explicitly safe

Pros:

- Breaks the `Session -> jobManager -> Session` synchronous cycle.
- Makes event emission a leaf operation with respect to session side effects.
- Preserves durable pending-send recovery.
- Gives one place to implement retries, coalescing, hard-failure classification, and metrics.
- A good stepping stone toward an actor/mailbox model.

Cons:

- Moderate refactor.
- Must be careful with ordering and duplicate prevention.
- Requires tests around pending, delivered, dropped, restored, and busy states.

Assessment: recommended practical fix.

### Option 5: Actor/Mailbox Runtime

Make each mutable subsystem an actor with a command mailbox. Components communicate by messages, not direct calls.

Possible actors:

- `SessionActor`: owns transcript, history, session state, model loop, steering queue.
- `JobActor`: owns jobs, watches, job output state, watch pending/delivery state.
- Optional `DeliveryActor`: owns delivery retry scheduling and failure classification.

The flow becomes:

```text
SessionActor emits event
  -> JobActor receives SessionEventObserved
    -> JobActor records watch intent
      -> SessionActor drains or receives delivery command at safe boundary
```

No session may synchronously mutate another session. Parent/child communication is queued.

Pros:

- Cleanest long-term concurrency model.
- Naturally supports recursive subagents.
- Makes deadlocks from shared locks much less likely.
- Ownership boundaries become explicit.

Cons:

- Large migration.
- Broad test impact.
- Harder to review in one change.
- Risk of introducing behavior drift while moving existing code.

Assessment: attractive long-term architecture, especially if recursive subagents are a real roadmap item. Too large for a first fix unless the team is ready to invest in a runtime migration.

## Recommended Direction

Implement option 4 now, but shape it as the first step toward option 5.

The invariant should be made explicit in code:

```text
onSessionEvent records watch intent only.
It must not perform delivery or call back into Session.
```

This gives most of the safety benefit without forcing a full actor rewrite immediately.

## Option 4 Implementation Plan

### 1. Split Watch Event Observation From Delivery

Change `jobManager.onSessionEvent` so it:

- matches watch configs
- builds watch-send state
- persists or coalesces `watch_send_pending`
- enqueues plain watch notifications if needed
- returns without calling `deliverWatchSends`

It should not:

- call `sendDelegateMessage`
- call `deliverWatchCallerMessage`
- resume delegates
- append steering turns
- call back into `Session`

### 2. Add A Session-Owned Drain Method

Add a method with a name like:

```go
func (s *Session) drainPendingWatchSendsAtBoundary(ctx context.Context) error
```

This method can:

- ask the job manager for pending deliveries
- classify target state
- deliver to `caller` or delegate
- mark delivered, keep pending, or drop

The method belongs on `Session` because only `Session` knows when it is safe to mutate transcript/history/steering state.

### 3. Call The Drain At Explicit Boundaries

Initial boundary candidates:

- after `persistToolResults`
- after `injectPostToolSteering`, if it is still before the next model request and not holding side-effect locks
- before `prepareModelRequest`
- when transitioning from processing to idle
- restore/recovery path after session state is ready

The exact placement should avoid double-delivery and should preserve this behavior:

- a caller watch send created while the last turn is an assistant tool call must remain pending until `TOOL_RESULTS` is appended
- after `TOOL_RESULTS`, it may be delivered as a steering turn

### 4. Keep Pending State Durable And Idempotent

The outbox should remain durable:

- `watch_send_pending` records intent
- `watch_send_delivered` records success
- `watch_send_dropped` records hard failure

Retries should be idempotent by `delivery_id` or existing watch-send key/update sequence.

### 5. Fix Session-Target Excerpts

`include_excerpt` currently tries to read job output for `caller`, which produces:

```text
output_read_error: job "caller" not found
```

For session targets (`caller`, `*`, or any future session ref), either:

- omit `excerpt` entirely, or
- include a session-event excerpt with a clearly defined source.

Recommendation: omit output excerpts for session targets for now. It is simpler and avoids inventing semantics.

### 6. Tests For Option 4

Add or update tests for:

- `job_watch(target="caller", events=["assistant.message"], send.to="caller")` does not deadlock when the assistant response contains a tool call.
- The tool result is persisted after that assistant tool call.
- Watch delivery remains pending while the transcript ends in an assistant tool call.
- Watch delivery drains after a `TOOL_RESULTS` turn.
- Restored pending caller watch sends drain at a safe boundary.
- `include_excerpt` for session targets does not attempt `readOutput("caller")`.
- Delivery to a busy delegate remains pending.
- Delivery to a non-messageable target drops with a stable diagnostic.

## Option 5 Implementation Plan

Option 5 should not be a one-shot rewrite. A staged migration is safer.

### Stage 1: Define Ownership And Command Interfaces

Document actor ownership:

- `Session` owns session state, transcript, history, steering, model loop.
- `JobRuntime` or `JobActor` owns jobs, watches, output state, watch outbox.
- Cross-owner work happens via commands, not direct callbacks.

Introduce command-style APIs while leaving internals mostly unchanged.

### Stage 2: Convert Watch Sends To Messages

Move watch send delivery behind an explicit message or command:

```text
DrainWatchSends
DeliverWatchSend
WatchSendDelivered
WatchSendDropped
```

This stage should be very close to option 4.

### Stage 3: Convert Job Lifecycle Notifications

Move job lifecycle events through actor messages:

```text
StartJob
JobOutputAppended
JobFinished
JobStopRequested
```

The session should observe job notifications through a subscribed event stream or mailbox, not through direct method calls that mutate session state.

### Stage 4: Convert Parent/Child Session Control

Recursive subagents make this important.

Rules:

- parent-to-child messages are queued commands
- child-to-parent messages are queued commands
- no child may synchronously steer or mutate a parent
- no parent may synchronously enter a child while holding parent locks
- close/interrupt/restore is a tree of commands, not recursive lock acquisition

### Stage 5: Reduce Or Remove Shared Locks

After command boundaries exist, remove direct lock sharing where possible.

Target state:

- each actor serializes its own mutable state
- external callers wait on responses or receive events
- no lock hierarchy needs to span session trees

### Stage 6: Actor-Level Testing

Tests should cover:

- command ordering
- pending delivery recovery
- close while delivery is pending
- parent/child/grandchild watch sends
- recursive interrupt
- restore with pending sends
- race tests for job completion plus watch delivery

## Recursive Subagents Impact

If recursive subagents are allowed, the long-term case for option 5 becomes stronger.

Recursive subagents introduce more cycles:

```text
parent processing -> child event -> parent watch send
child processing -> parent close -> child cancellation
grandchild job finish -> child watch -> parent notification
restore parent -> reconstruct child -> drain pending child sends
```

Trying to make those safe with only lock ordering is fragile. A message-passing boundary is easier to reason about:

```text
No session synchronously mutates another session.
Cross-session work is queued and handled by the target owner.
```

That said, recursive subagents do not make option 4 wrong. They make a hacky option 4 unacceptable. Option 4 should establish the same discipline option 5 needs:

- durable outbox
- explicit boundaries
- no synchronous event-observer callback into session mutation
- routing keys that can represent session trees

## Secondary Issue: Terminal Output-Match Watch Race

Observed failure:

```text
job_watch(target="job_01KTWNAXP160YRXDNXVB7T40ZQ",
          output_match="watch-match-once",
          send.to="caller")

target_terminal: job "job_01KTWNAXP160YRXDNXVB7T40ZQ" is completed; watches can only attach to running jobs
```

The command was:

```sh
sleep 1; echo watch-match-once; sleep 1
```

Even this job completed before the model could attach the watch.

This is a usability problem because the natural workflow is:

```text
start background job -> attach output watch -> observe token
```

Model/tool-call round trips can be slower than short shell jobs, so requiring the job to still be running at watch creation makes smoke tests and real use brittle.

Recommended narrow fix after the deadlock work:

- For `output_match` on a terminal job, scan retained output once.
- If the pattern matched, return a one-shot fired result and queue/send the frame through the same outbox.
- If the pattern did not match, return a clear terminal/no-match result.
- Do not replay arbitrary historical event watches.

This keeps the API useful without inventing broad event replay semantics.

Potential response shape:

```json
{
  "target": "job_...",
  "watching": false,
  "fired": true,
  "terminal_catchup": true,
  "output_match": "watch-match-once"
}
```

Or if no match:

```json
{
  "target": "job_...",
  "watching": false,
  "fired": false,
  "terminal_catchup": true,
  "status": "completed"
}
```

Exact fields should follow existing result conventions; the key behavior is that a completed job is not an automatic hard error for `output_match`.

## Questions For Reviewers

1. Is "events produce intent; boundaries perform effects" the right invariant?
2. Should `Session.emit` be allowed to call any observer that may perform side effects, or should all observers be restricted to durable intent only?
3. Are the proposed drain boundaries sufficient, or should there be exactly one drain site to make ordering easier?
4. Should caller watch sends be modeled as steering turns, notification turns, or a distinct transcript turn type?
5. For session-target watches, should `include_excerpt` be omitted, or should it include a structured event excerpt?
6. If recursive subagents are planned, should option 4 be treated as the first stage of option 5?
7. Is terminal catch-up for `output_match` enough, or do we need a more general "recent events/output" model?

## Recommended Decision

Proceed with option 4 now:

```text
durable watch outbox + explicit boundary drain
```

But implement it with option 5's discipline:

```text
no synchronous cross-session or cross-owner side effects from event observation
```

This is the best balance of reliability, simplicity, reviewability, and future extensibility.
