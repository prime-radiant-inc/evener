# AppServer Serial Worker Dispatch Design

Date: 2026-08-30
Status: design only; implementation not started
Depends on: PR #667 (`review/appserver-dispatch-narrowing`) — this design's "current
state" is that branch's end state
Lineage: 2026-08-30 audit findings C1 (lost panic barrier) and I3 (per-connection
ordering retired without a handler audit), both introduced by `0291be074`

## Summary

PR #667 restored the per-connection ordering contract that `0291be074` had silently
retired: every AppWire request except the three known-slow transcript-walking reads
(`thread/read`, `thread/turns/list`, `evener/subagentPreview`) now runs inline in the
WebSocket receive loop, serial per connection, behind one shared panic barrier
(`handleAndEnqueue`). The residual exposure is structural: an inline handler occupies
the receive loop for its whole duration, so a slow serial handler delays every later
frame on that connection — including the browser's app-level `ping` heartbeat and the
transport's dead-peer detection.

This design removes that exposure without giving up ordering. Each connection gains a
single **serial worker** goroutine. The receive loop shrinks to transport work only:
read frames, answer `ping`, observe connection cancellation, and enqueue everything
else onto a bounded per-connection request queue. The worker drains the queue strictly
in arrival order and executes each handler through the existing `handleAndEnqueue`
barrier. Ordering is preserved for all handlers with no handler-side audit; `ping` and
dead-peer detection are never starved by any handler; panics are contained in the one
place they are contained today.

The honest limit, stated up front: requests still queue behind a slow operation on the
same connection. That is what per-connection ordering *means* — a later `turn/steer`
must not run before the `turn/start` pipelined ahead of it — and it is a property, not
a flaw. What this design removes is the *transport* being hostage to handler latency,
not handlers waiting on each other.

## Goals

- No handler can starve the transport. `ping`, WebSocket keepalive/dead-peer
  detection, close handling, and connection cancellation all stay live while any
  handler — mutation or read, fast or slow — is executing.
- Per-connection request ordering is fully preserved for every handler: a later
  request does not begin executing until the earlier request's handler has returned
  and its response has been enqueued. No audit of the ~104 hub and daemon handler
  registrations is required.
- One panic barrier in one place (`handleAndEnqueue`), covering every execution path
  identically, exactly as PR #667 left it.
- Every transport property PR #667's tests pin — response/id pairing, hydration
  capture commit/abort ordering, buffered-replay vs live-stream record accounting,
  initialize-first, the exact concurrent method set, serial mutation ordering —
  carries over unchanged.

## Non-goals

- Concurrent execution of order-sensitive methods. A slow mutation still delays the
  requests queued behind it on the same connection; clients that want parallelism
  across independent operations already get it across connections.
- A per-request cancel frame. AppWire has no such frame today (verified against
  `appwire/types.go`: `turn/cancelQueued` removes queued composer entries and
  `turn/interrupt` is an ordinary request; neither cancels an in-flight RPC), and this
  design does not add one.
- Reintroducing an in-flight limiter with a wire-visible overflow error. The 128-slot
  limiter and its `Unavailable` response died in PR #667 and stay dead; the queue
  bound here applies flow control, never a wire error.
- Bounding the *count* of in-flight concurrent slow reads. PR #667 dispatches each
  slow read on its own goroutine with no cap; this design keeps that property
  unchanged (see Open questions).

## Current state (post-#667) and the residual exposure

At the tip of `review/appserver-dispatch-narrowing`:

- `runWebSocketReceiveLoop` (`internal/appserver/websocket.go`) reads one frame at a
  time and calls `Connection.dispatchMessage` inline.
- `dispatchMessage` (`internal/appserver/server.go`) sends the three methods named by
  `concurrentDispatchMethod` — `thread/read`, `thread/turns/list`,
  `evener/subagentPreview` — to their own goroutine once the connection is
  initialized. Everything else, `ping` and `initialize` included, runs inline in the
  receive loop via `handleAndEnqueue`, which is the shared panic barrier: a panicking
  handler is logged with its stack, answered with `InternalError`, and the connection
  survives.
- The keepalive read gate (`webSocketReadGate`) marks the reader unavailable while the
  receive loop is *not* parked in `Recv` — which includes the entire runtime of every
  inline handler. Keepalive pings are skipped or deferred for that window
  (`TestWebSocketKeepaliveSkipsPingsWhileReaderIsUnavailable` pins this), so a slow
  inline handler does not falsely kill the connection — but dead-peer detection is
  paused for its duration.

The exposure, verified against the handlers on that branch:

- `evener/auth/device/poll` (`cmd/evener-hub/app_auth.go`, `DevicePoll`) performs
  outbound OAuth HTTP — `pollDeviceOnce` against the provider's device-authorization
  endpoint and, on success, `exchangeDevice` for the token exchange. Both ride the
  connection context but block the receive loop for a full network round trip (or
  until the HTTP client's timeout) on every poll tick the web UI issues.
- `evener/git/head` (`cmd/evener-hub/app_git_head.go`) forks `git rev-parse` up to
  twice via `exec.CommandContext`. Normally milliseconds; on a cold NFS mount or a
  repository with a wedged `index.lock`-holding process, arbitrarily long.

While either occupies the receive loop, the browser's `ping` — the app-level
heartbeat, because browsers cannot send WebSocket ping frames from JS — sits unread in
the socket, and every other frame queues behind it in the kernel. The inline set is
"bounded work" by policy (`concurrentDispatchMethod`'s doc comment says every inline
method "must stay bounded"), but nothing enforces it, and these two members already
break it. Adding them to the concurrent set instead would be whack-a-mole: each
addition reopens the ordering question PR #667 existed to close.

## Design

### The receive-loop / worker split

Each connection runs three goroutines instead of two: the existing send loop, the
existing receive loop, and a new **serial worker**.

The receive loop keeps only transport concerns:

1. Park in `transport.Recv` (reader available; keepalive can ping).
2. On a `ping` request, answer it immediately from the loop itself — build the
   response and enqueue it through `enqueueDispatched`, bypassing the request queue.
3. On any other frame — request or notification, pre- or post-initialize — push it
   onto the connection's bounded request queue, giving up if the connection context is
   done.
4. On `Recv` error, return; the existing close handling is unchanged.

The worker is one goroutine started by `ServeWebSocket` beside the send loop:

```go
for {
    select {
    case <-ctx.Done():
        return
    case msg := <-c.requests:
        c.executeOrdered(ctx, msg) // classification + handleAndEnqueue, below
    }
}
```

It drains the queue strictly in arrival order. Because the receive loop is the only
producer and the worker the only consumer, FIFO channel semantics *are* the ordering
contract: request N's handler returns and its response is enqueued before request N+1
is dequeued. That is the same observable ordering PR #667's inline path provides —
the only difference is that frame N+1 may already be decoded and sitting in the queue
while N executes, which no client can observe.

`ping` overtaking the queue is deliberate and safe: the ping handler is stateless
(`HandleMessage` answers it before the initialize gate with `struct{}{}`), so no
ordering relationship with any other request exists to violate. This also upgrades the
pinned ping property from "ping survives a slow *read*" (PR #667) to "ping survives
any handler."

A second transport would inherit the whole policy by driving the same enqueue path,
exactly as `dispatchMessage` is positioned today.

### Where the panic barrier lives

Unchanged: `handleAndEnqueue` remains the single panic barrier, and every handler
execution — worker-serial or concurrent slow read — passes through it. The worker
calls it directly, so a panicking mutation is logged via `panicLogf`, answered with
`InternalError`, and the worker loop continues with the next queued request; the
connection and process survive. The receive loop's ping path never enters handler
code, so it needs no barrier of its own (and `enqueueDispatched` is not handler code).
`TestServeWebSocketPanickingHandlerAnswersInternalErrorAndConnectionSurvives` carries
over verbatim; a new case must additionally pin that requests queued *behind* the
panicking one still execute (the worker survived, not just the connection).

### How the concurrent slow-read set composes with the worker

The three-method concurrent set is kept, and slow reads are **dispatched from the
worker, not the receive loop**. When the worker dequeues a request whose method is in
`concurrentDispatchMethod` and the connection is initialized, it spawns
`go c.handleAndEnqueue(ctx, msg)` and immediately dequeues the next request; otherwise
it executes inline in the worker.

Why the worker and not the receive loop:

- **It reproduces PR #667's ordering exactly.** On that branch, by the time the
  receive loop reads a slow-read frame, every earlier serial request has already
  completed (they ran inline, ahead of it, in the same loop). So the pinned contract
  is: *a slow read begins only after every earlier request on the connection has
  completed; later requests may overtake it; responses pair by id; only the
  sequence-cut discipline governs how notifications interleave with a hydration
  response.* Dispatching from the worker preserves all four clauses. Dispatching from
  the receive loop would create a reordering PR #667 never had: a `thread/read` could
  begin — and take its hydration capture cut — while an earlier queued mutation
  (say, the `session/setThread`-shaped call that repoints the connection's
  subscription) had not yet run. Nothing pins what that interleaving means, because
  it has never been possible.
- **One classification site.** The initialize gate, the method-set check, and the
  spawn all live in the worker's `executeOrdered`; the receive loop stays free of
  dispatch policy. The `isInitialized` check is also exact there rather than racy:
  the worker is the goroutine that *sets* initialized (it executes `initialize`), so
  when it classifies a dequeued frame the answer cannot be mid-flip.

The cost is one queue hop of latency for a slow read behind a slow mutation — but
waiting for earlier requests to finish is precisely the ordering PR #667 pinned for
the moment a slow read *starts*, so this is the contract, not a regression.

### Queue semantics: bound, backpressure, memory

The queue is a buffered channel on `Connection`:

```go
requests chan appwire.Message // cap appserver.requestQueueCap = 64
```

**Bound.** 64 slots. The number needs to hold any legitimate pipelined burst from one
client tab — the observed bursts are a handful of requests on pane switch — while
staying small enough that the memory multiplier below stays boring. It is deliberately
not `appwire.NotificationBufferCap` (4096): that constant sizes the *outbound*
notification firehose, where overflow means eviction; this queue sizes inbound
pipelining, where overflow means flow control. Coupling them would let the wrong
contract resize this one.

**Backpressure.** A full queue blocks the receive loop:

```go
select {
case c.requests <- msg:
case <-ctx.Done():
    return
}
```

This is the natural pressure valve, and it is worth analyzing rather than hiding:

- The blocked loop stops calling `Recv`, so inbound frames accumulate in the kernel
  socket buffer and TCP flow control eventually reaches the client. A client that
  pipelines more than 64 requests behind a slow one experiences exactly the ordering
  it asked for, applied at the transport instead of in server memory.
- No wire error, no eviction. Unlike the outbound buffer (where full means the
  consumer stopped draining and the connection is torn down) and unlike the deleted
  128-slot limiter (where full was answered with `Unavailable`), a full request queue
  is a healthy server applying flow control. It is invisible to a well-behaved
  client and non-fatal to a greedy one.
- While blocked, the loop is not in `Recv`, so the keepalive read gate reports the
  reader unavailable and transport pings are deferred — the same no-false-teardown
  behavior inline handlers get today, now confined to the >64-deep-pipeline case
  instead of every slow handler. App-level `ping` is also delayed in that window
  (the loop cannot read it); this is the one residue of the old exposure, reachable
  only by a client that has already buried the connection 64 requests deep.

**Memory.** Per connection: 64 queued frames plus the channel array. A frame's decoded
size is bounded by the transport read limit (`appWireWebSocketReadLimit`, 128 MiB in
`appwire/ws_transport.go`), so the worst case is capacity × read limit from a client
deliberately stuffing maximal frames behind a parked handler. That is a ×64 multiplier
over PR #667's inline path, which buffers at most one decoded frame while a handler
runs. It is not a new trust boundary — the transport already extends 128 MiB of trust
per frame to an authenticated client, and the same client can open more connections —
but it is a real multiplier and is recorded here deliberately. A byte-budgeted queue
was considered and rejected as YAGNI: the clients are our own web UI and TUI, and the
budget's complexity would defend against a peer the per-frame limit already trusts.

**Initialize handshake.** No special casing. Pre-initialize frames ride the queue like
everything else; the worker executes them in order, and `HandleMessage`'s existing
gate answers non-`initialize` requests with "initialize required" and completes the
handshake before any later frame is dequeued. PR #667's guarantee — later dispatch
cannot observe a half-initialized connection — holds by the same FIFO argument as the
general ordering contract, with one improvement: the guarantee no longer depends on
`dispatchMessage` checking `isInitialized` from the receive loop before the flag's
writer has necessarily finished. `ping` is answered from the receive loop even before
initialize, which matches today's behavior (ping bypasses the gate in
`HandleMessage`).

### Cancellation

AppWire has no per-request cancel frame, so "cancellation" on this transport means
exactly one thing: the connection context ending — client disconnect, keepalive
failure, response-enqueue failure (`enqueueDispatched` → `cancelContext`), or server
shutdown tearing down the HTTP request context. The design keeps that shape and
threads it through both queue states:

- **Queued but not started.** The worker's `select` observes `ctx.Done()` and returns
  without executing anything further. Queued requests are abandoned unanswered —
  correct, because connection cancellation is connection death: there is no peer to
  answer, and `enqueueResponse` on the closed send channel would refuse the response
  anyway. No handler side effects occur for a request that never started, so
  abandonment is clean by construction.
- **Executing.** The handler already holds the connection context (the worker passes
  the same `ctx` the inline path passes today), so handlers that respect their
  context — `DevicePoll`'s HTTP calls, `resolveGitHead`'s `exec.CommandContext` —
  unwind promptly. A handler that ignores its context parks the worker until it
  returns, exactly as it parks the receive loop today; teardown does not wait for it
  (see Shutdown).
- **`turn/interrupt` rides the queue.** Interrupt is an ordinary request and executes
  in arrival order, behind any slow mutation queued ahead of it — identical to PR
  #667, where interrupt runs inline behind the same handler. This is deliberate:
  interrupt mutates turn state, and a client that pipelines `turn/start` then
  `turn/interrupt` relies on that order the same way `turn/start` → `turn/steer`
  does. Promoting interrupt to the out-of-order set would reopen precisely the audit
  this design exists to avoid.

### Shutdown, drain, leak-freedom

Worker lifecycle is owned by `ServeWebSocket`, symmetric with the send loop:

- The worker starts before the receive loop runs and exits when the connection
  context is done. The receive loop is the only producer, the worker the only
  consumer, and neither closes the channel — producer teardown is "stop sending"
  (the loop returned), consumer teardown is `ctx.Done()`, and the channel is
  garbage-collected with the `Connection`.
- **Close with a non-empty queue: no drain.** When the receive loop returns,
  `ServeWebSocket`'s deferred `cancel()` fires and the worker exits at its next
  `select` without executing the remaining entries. Executing them would be work for
  a peer that is gone, and any hydration capture they might open would be aborted at
  response time regardless. Queued-but-unstarted requests hold no resources — no
  hydration finalizers exist until a handler runs `CaptureSubscription` — so
  abandonment leaks nothing.
- **Teardown does not join the worker.** `unregisterConnection` proceeds while a
  parked handler may still be executing, exactly as it does for PR #667's concurrent
  slow reads: `closeSend` flips `sendClosed`, the parked handler's eventual
  `enqueueResponse` fails, its hydration finalizer (if any) aborts, and
  `takeAllHydrations` in `unregisterConnectionLocked` has already swept whatever was
  registered. A wedged handler must not be able to hold connection teardown hostage;
  teardown-by-cancellation is the existing contract and the worker inherits it.
- **Leak-freedom.** The worker can only be blocked in its `select` (exits on cancel),
  in a handler (bounded by the handler's own respect for `ctx`, same as every
  execution path today), or in `enqueueResponse` (which selects on `ctx.Done`). The
  implementation should expose the worker's exit to tests the same way the keepalive
  loop exposes decisions (`keepaliveDecision`): a package-private done channel or
  callback seam, so the drain test asserts exit instead of sleeping.

An incidental improvement worth naming: because the receive loop returns to `Recv`
immediately after enqueuing, the keepalive read gate now reports the reader available
during handler execution. Dead-peer detection keeps running while a slow mutation
executes — today it is suspended for the duration of every inline handler.

## Invariants to pin, and the test plan

### Carried over unchanged

All of these drive the public WebSocket surface, not dispatch internals, and must pass
without modification:

- `TestServeWebSocketSlowHandlerDoesNotDelayPing` (ping vs a parked slow read)
- `TestServeWebSocketFastRequestCompletesWhileSlowHandlerRuns` (overtaking a slow read)
- `TestServeWebSocketRejectsRequestsBeforeInitialize` (initialize-first)
- `TestServeWebSocketResponsesPairToIDsWhenHandlersCompleteOutOfOrder` and
  `TestServeWebSocketBurstOfConcurrentRequestsAllPairCorrectly` (response/id pairing)
- `TestConcurrentDispatchMethodsAreExactlyTheSlowReads` (the method set, swept against
  the full appwire catalog)
- `TestServeWebSocketMutationsDispatchSeriallyPerConnection` (serial ordering — the
  worker must satisfy the same observable contract the inline path does)
- `TestServeWebSocketHydrationThenMutationDeliversEveryNotificationExactlyOnce`
  (buffered-replay vs live-stream record accounting under read/mutation interleaving)
- `TestServeWebSocketPanickingHandlerAnswersInternalErrorAndConnectionSurvives` and
  `TestPanicLogfFallsBackToStandardLoggerWhenNoSinkIsConfigured` (panic barrier)
- The keepalive suite (`websocket_keepalive_test.go`) — the read gate still governs
  the blocked-enqueue window — and the send-loop tests (`websocket_send_test.go`)
- The seqfuzz and `HandleMessage` fuzz suites

### New tests the implementation must add

1. **Ping under a slow serial mutation** — the capability this design adds. Park an
   inline-set handler (e.g. `thread/list`); assert `ping` completes on the same
   connection while it is parked. This is the slow-*mutation* twin of
   `TestServeWebSocketSlowHandlerDoesNotDelayPing` and fails against PR #667.
2. **Ordering across the worker under interleaving** — pipeline a mix of parked and
   fast serial requests with slow reads sprinkled in; assert serial responses arrive
   in request order, each serial handler observes the previous one's completion, and
   slow reads start only after all earlier requests completed.
3. **Panic in the worker** — extend the carried panic test: requests queued *behind*
   the panicking one still execute and answer; the worker goroutine survived.
4. **Queue-full backpressure** — park a serial handler, pipeline `requestQueueCap`+k
   further requests; assert no wire error and no eviction, and that after release
   every response arrives, in order. Then repeat and close the client while the
   receive loop is blocked enqueuing; assert clean teardown (worker exit seam).
5. **Drain on close** — park a serial handler, queue several requests, close the
   client; assert the worker exits, abandoned requests produce no responses and no
   handler side effects (a counting handler), and — via the existing accounting
   tests' machinery — no hydration finalizer leaks.
6. **Keepalive live during a slow mutation** — using the `keepaliveDecision` seam:
   with a serial handler parked, keepalive ping decisions report the reader
   available. Fails against PR #667; pins the incidental improvement so it cannot
   silently regress.

## Rejected alternative: full-async dispatch for all handlers

Recorded here as the paper trail for not doing it, because "make everything
concurrent so nothing blocks anything" is the obvious question and was asked
explicitly.

`0291be074` was that design: every request on its own goroutine. The audit's C1 and I3
findings are what it cost, and the deeper problem is the contract change:

- **Per-connection ordering is load-bearing.** Every handler among the ~104 hub and
  daemon registrations (81 in `cmd/evener-hub`, 23 in `server/appwire_runtime.go`,
  counted on the #667 branch) was written against serial-per-connection dispatch. A
  client that pipelines `turn/start` then `turn/steer` relies on the start being
  applied first; under full-async the steer can win the race and target the previous
  turn — or no turn — with no error that names the reordering. The same shape exists
  for `thread/model/set` racing a `turn/start` from the same tab.
- **Only one family carries fences.** The queue-mutation methods carry explicit
  preconditions — `ExpectedQueueRevision`, `ExpectedEntryID`, `ExpectedInstanceID` —
  because they were designed for retries across reconnects. Nothing similar guards
  the rest of the surface, so full-async means either auditing every handler for
  order-independence and adding fences where it isn't, or eating latent reordering
  bugs that the race detector cannot see (they are cross-request logic races, not
  data races).
- **The failure mode is silent.** A reordered mutation pair doesn't crash; it
  produces a wrong state that surfaces as a flaky UI bug far from the cause. That is
  the worst kind of contract to retire implicitly.

The serial worker gets full-async's actual benefit — the transport never blocks on a
handler — while keeping the contract all ~104 handlers were written against, at the
cost every ordering contract carries: queued requests wait their turn.

## Migration and implementation shape

Assumes PR #667 is merged; this change is a delta on its end state, touching only
`internal/appserver/server.go` and `internal/appserver/websocket.go`.

- `Connection` gains the `requests` channel (created in `NewConnection`) and a worker
  entry point; `ServeWebSocket` starts the worker beside the send loop.
- `runWebSocketReceiveLoop` loses the `dispatchMessage` call in favor of the
  ping-answer-or-enqueue split.
- `dispatchMessage`'s classification logic moves into the worker's dequeue path
  (`executeOrdered` or similar domain name); `concurrentDispatchMethod`,
  `handleAndEnqueue`, `enqueueDispatched`, and everything downstream are unchanged.
- The large dispatch-policy doc comment on `dispatchMessage` is rewritten to describe
  the queue/worker contract — it is the authoritative statement of the ordering
  contract and must move with the policy.

Estimated size: roughly 80–120 lines of production delta (most of it comments and the
worker loop; the mechanism is one channel and one goroutine) and 300–400 lines of new
tests per the plan above. No wire change, no handler change, no client change.

## Open questions

- **Unbounded concurrent slow reads.** PR #667 spawns a goroutine per slow read with
  no cap (the old limiter died with the overflow error). A client can pipeline many
  `thread/read`s and hold that many transcript walks in flight. This design neither
  worsens nor fixes it; if it needs a bound, that is a separate small change (a
  per-connection semaphore around the spawn) and a separate decision.
- **Queue capacity.** 64 is a judgment call sized against observed UI bursts; the
  constant is one line to change and the backpressure behavior is capacity-
  independent. If real clients ever pipeline deeper legitimately, raise it.
- **Should `evener/auth/device/poll` still do network I/O on the RPC path at all?**
  The worker makes it harmless to the transport, but a poll loop that holds the
  serial queue for a network round trip still delays same-connection requests behind
  it. Restructuring device-poll (server-side polling with a notification) is out of
  scope here and may be worth its own note.
