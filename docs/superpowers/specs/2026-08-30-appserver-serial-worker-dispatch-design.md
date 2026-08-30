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

- No *handler* can starve the transport. `ping`, close handling, and connection
  cancellation stay live while any handler — mutation or read, fast or slow — is
  executing, regardless of how long it runs or whether it respects its context.
  Scoped deliberately: a *client* can still delay the transport by pipelining more
  requests than the queue holds — that is flow control, analyzed in the queue
  section, not handler starvation — and dead-peer detection pauses in exactly the
  windows it pauses today (see Queue semantics).
- The per-connection ordering contract PR #667 restored is preserved unchanged for
  every queued frame — requests and notifications alike — with no ordering audit of
  the 105 hub and daemon handler registrations. The contract is a partial order,
  stated precisely in "The ordering contract" below — not total FIFO, because the
  three concurrent slow reads participate on both sides — and the single
  deliberate exception is ping, whose scheduling this design changes on purpose
  (it bypasses the queue; see the table's ping cells).
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

### The ordering contract, stated as a partial order

This is the contract PR #667's end state actually provides, derived from its
mechanics (serial requests run inline in the receive loop; slow reads spawn from that
loop the moment their frame is read), and it is the contract the worker must
reproduce exactly. For two requests A before B on one connection:

| A ↓ / B → | serial request | concurrent slow read | ping |
|---|---|---|---|
| **serial request** | B starts only after A's handler returned and its response was enqueued | B starts only after A completed | B may overtake A |
| **concurrent slow read** | B does not wait for A | B does not wait for A | B may overtake A |
| **ping** | B does not wait for A | B does not wait for A | — |

In words: *earlier serial requests block all later work; nothing waits for a slow
read; ping waits for no queued work.* Slow reads are unordered among themselves — two
pipelined `thread/read`s run concurrently — and a serial request issued after a slow
read runs without waiting for it (that overtaking is the point of the concurrent
set). Responses always pair by request id, and only the sequence-cut discipline
governs how notifications interleave with a hydration response.

Inbound notification frames (today only the `initialized` no-op reaches
`handleNotification`) ride the queue like serial requests and keep their arrival
order relative to them; nothing about this design is request-specific, so a future
order-sensitive client notification inherits the same contract automatically.

The table describes frames the receive loop has read. Its ping cells carry the one
qualification the queue section derives: ping bypass applies from the moment the
loop reads the ping frame, and the loop can only read it when it is not blocked
enqueuing into a full queue. Under saturation — a client more than
`requestQueueCap` requests deep — the ping frame itself waits for a queue slot,
per the flow-control analysis in "Queue semantics".

The one cell this design changes relative to PR #667 is ping: today ping is answered
inline in the receive loop, so it waits behind an executing serial handler; here it
bypasses the queue (see the split below). Every other cell is preserved unchanged.

Wire compatibility of the ping change: AppWire clients correlate responses by
request id (`appwire/client.go`'s pending-request map), and `ping` carries no state —
`HandleMessage` answers it before the initialize gate with an empty struct. A ping
response overtaking an earlier serial or pre-initialize response is therefore
observable on the wire but meaningless to any conforming client; no ordering
semantics attach to ping in the protocol.

### The receive-loop / worker split

Each connection runs three goroutines instead of two: the existing send loop, the
existing receive loop, and a new **serial worker**.

The receive loop keeps only transport concerns:

1. Park in `transport.Recv` (reader available; keepalive can ping).
2. On a `ping` request, handle it inline via the existing `handleAndEnqueue`,
   bypassing the request queue. `HandleMessage` already answers ping before the
   initialize gate and before the router, so this is bounded stateless work, and it
   keeps exactly one ping implementation and one panic barrier — no hand-built
   response beside the real path that could drift from it.
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
        // select chooses randomly when both cases are ready, so a canceled
        // connection can still win a dequeue; re-check before executing so
        // no request *starts* after cancellation is observable here.
        if ctx.Err() != nil {
            return
        }
        c.executeOrdered(ctx, msg) // classification + handleAndEnqueue, below
    }
}
```

It drains the queue strictly in arrival order. Because the receive loop is the only
producer and the worker the only consumer, FIFO dequeue plus `executeOrdered`'s
classification implements the partial order above: a serial request executes to
completion — handler returned, response enqueued — before the worker dequeues
anything later, and a slow read is spawned and left behind, exactly as the receive
loop leaves it behind today. The only difference from PR #667's inline path is that
later frames may already be decoded and sitting in the queue while a serial handler
executes, which no client can observe.

`ping` overtaking the queue is deliberate and safe: the ping handler is stateless
(`HandleMessage` answers it before the initialize gate with `struct{}{}`), so no
ordering relationship with any other request exists to violate. This also upgrades the
pinned ping property from "ping survives a slow *read*" (PR #667) to "ping survives
any handler."

A second transport would inherit the whole policy by driving the same enqueue path,
exactly as `dispatchMessage` is positioned today.

### Where the panic barrier lives

Unchanged: `handleAndEnqueue` remains the single panic barrier, and every execution
path — worker-serial, concurrent slow read, and the receive loop's inline ping —
passes through it. The worker calls it directly, so a panicking mutation is logged
via `panicLogf`, answered with `InternalError`, and the worker loop continues with
the next queued request; the connection and process survive.
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
  receive loop reads a slow-read frame, every earlier *serial* request has already
  completed (they ran inline, ahead of it, in the same loop), while earlier slow
  reads may still be in flight (they were spawned and left behind). That is
  precisely the "nothing waits for a slow read / earlier serial requests block all
  later work" partial order stated above, and worker-side dispatch preserves every
  cell of it. Dispatching from the receive loop instead would create a reordering PR
  #667 never had: a `thread/read` could begin — and take its hydration capture cut —
  while an earlier *queued serial mutation* (say, the subscription-repointing call)
  had not yet run. Nothing pins what that interleaving means, because it has never
  been possible.
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

**Bound.** 64 slots, and the number is explicitly **provisional**: slice 0 of the
landing order characterizes real client burst depth (named UI scenarios measured at
the existing client edges — see the landing order for the method) and the final
constant is the worst measured legitimate burst with at least 4× headroom, capped
at 64 — the ceiling this design's memory analysis covers. The decision branch is
explicit: measured burst ≤ 16 freezes `min(burst × 4, 64)`; a measured burst
above 16 means a real client legitimately pipelines deeper than this design
assumed, and the capacity **and** the memory analysis come back to this spec for
revision together rather than the ceiling being quietly raised. The number needs to hold
any legitimate pipelined burst from one client tab while staying small enough that
the memory multiplier below stays boring. It is deliberately
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
  (the loop cannot read it). This is the deliberate scope limit on the liveness
  goal: transport liveness is guaranteed against handler behavior, not against a
  client that buries the connection more than `requestQueueCap` requests deep. Such
  a client has explicitly asked for 64+ operations to happen in order behind a slow
  one; delaying its heartbeat until the queue admits the next frame is flow control
  doing its job, and the alternative — an overload policy that times out the
  enqueue and closes the connection — would turn a self-inflicted wait into a
  server-inflicted disconnect. Rejected. One consequence to accept knowingly: if
  the saturated client's own heartbeat timeout is shorter than the parked
  handler's remaining runtime, that client will conclude the connection is dead
  and reconnect — the correct outcome for a connection it buried, and no worse
  than PR #667, where the same heartbeat stalls behind *any* slow inline handler
  with no saturation required.
- Disconnect detection while blocked: the loop cannot observe a peer close frame or
  a dead TCP peer until it next parks in `Recv`, and the read gate suspends
  keepalive for the same window. This is not new — PR #667 has the identical window
  during *every* inline handler's execution — and it is bounded the same way: a
  queue slot frees when the executing handler finishes, the loop re-enters `Recv`,
  and normal close/keepalive handling resumes. Independent of that path, the send
  loop still cancels the connection if a write fails or exceeds
  `webSocketWriteTimeout`, and server shutdown still cancels outright. The window
  shrinks under this design overall: it used to cover every serial handler's
  runtime; now it exists only while the queue is full.
- Saturation is observable: the receive loop reports the first blocked enqueue per
  connection through `Server.logf`, the same advisory channel `evictSlowConsumer`
  uses. No metrics fabric exists in `internal/appserver` and this design does not
  introduce one; one log line is the proportionate version.

**Memory.** Per connection while live: the 64 queued frames plus the channel array,
plus two frames the queue does not hold — the one the worker is executing and the
one the receive loop may hold while blocked enqueuing (capacity + 2 total). A
frame's decoded size is bounded by the transport read limit
(`appWireWebSocketReadLimit`, 128 MiB in `appwire/ws_transport.go`), so the
arithmetic worst case is (capacity + 2) × read limit — ~8 GiB — from an
authenticated client deliberately stuffing maximal frames behind a parked handler.
On teardown the buffered frames are explicitly discarded rather than left to
`Connection` garbage collection, because an orphaned handler can retain the
`Connection` (see the teardown purge in Shutdown). Recorded deliberately, with its context: this is not the cheapest
route to that outcome on the current tree. PR #667 already lets the same client hold
*unbounded* decoded maximal frames — each pipelined `thread/read` spawns a goroutine
that retains its params message, with no cap since the 128-slot limiter died — and
the same client can open additional connections besides. The queue's ×64 multiplier
on the serial path is therefore strictly smaller than an exposure the trusted-client
model already accepts. The governing knob for the whole class is the per-frame read
limit, which is a transport decision outside this design's scope. A byte-budgeted
queue was considered and rejected: it adds real complexity to close one route into a
class that stays open through the concurrent-read route and the extra-connection
route, against a peer that is our own authenticated web UI and TUI. If the class is
ever closed for real, it needs an aggregate per-client admission budget spanning all
of these, not a budget on this one channel (see Open questions).

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

- **Queued but not started.** The worker returns without executing anything further.
  The guarantee is stated exactly, because Go's `select` is not a priority
  statement: when `ctx.Done()` and a non-empty queue are simultaneously ready, the
  dequeue can win, so the worker re-checks `ctx.Err()` after every dequeue (see the
  loop above) and the contract is *no request begins executing after the worker has
  observed cancellation, and it observes at every dequeue*. The admission cutoff is
  therefore racy by one edge, unavoidably: a cancellation that lands between the
  `ctx.Err()` check and the handler's first instruction does not stop that one
  request from starting — the same edge every cancellation mechanism has — and the
  handler then runs with an already-canceled context. Requests still in the queue
  behind the observation point never execute. Abandonment is clean by construction:
  a request that never started has no side effects, no peer remains to answer, and
  `enqueueResponse` on the closed send channel would refuse the response anyway.
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

### Admitted-request semantics on disconnect

The worker changes one execution-time property that deserves its own contract
statement: under PR #667 the receive loop cannot *observe* a peer close while an
inline serial handler runs (it is not in `Recv`), so the common disconnect —
client closes the tab — could not cancel an executing serial mutation's context.
Under the worker, the loop observes the close immediately and cancels while the
mutation executes.

The contract this design adopts: **an admitted request may observe connection
cancellation at any await point, and its side effects are whatever it completed
before observing it.** This is a promptness change, not a new category, and the
system is already built for it on all three sides:

- *It already happens.* Mid-handler cancellation on disconnect exists under PR
  #667 through the send path — a send-loop write to the dead peer fails or times
  out (`webSocketWriteTimeout`) and cancels the shared context while the inline
  handler runs — and through server shutdown; and every concurrent slow read
  experiences prompt disconnect cancellation today. No handler may assume its
  context outlives its await points now.
- *The core mutations cannot be canceled mid-flight at all, because they never
  observe the context.* Measured on the #667 branch: 18 of the daemon's 24
  handlers ignore their context entirely (16 bind it as `_`, two leave the
  parameter unnamed). `turn/start` and
  `turn/steer` are the archetype (`server/appwire_runtime.go`): under
  `lockRetrySafeMutation` — the durable per-`ClientMutationID` dedup — they
  accept a durable mutation intent and return a receipt; the turn itself runs
  under session-lifetime context in the serve loop, not under the RPC. Prompt
  disconnect cancellation is a no-op for this entire class. The six daemon
  handlers that do bind ctx are the slow read (`thread/read`), two benign reads
  (`thread/unsubscribe`, `model/list`), and three mutations that carry their own
  failure discipline: `turn/interrupt` sits under the same `lockRetrySafeMutation`
  dedup as start/steer, `thread/clear`'s failure path releases its gate and
  admits a retry with a fresh mutation id, and `thread/compact/start` forwards
  ctx into a single callback — each is a named subject for the slice-0 audit
  below, not an open-ended population.
- *The wire already answers the client-side ambiguity, and the retry contract is
  explicit.* A client disconnected mid-mutation has never been able to know
  whether the mutation applied — under PR #667 the handler usually ran to
  completion but its response was undeliverable. That is why the retry-safe
  machinery exists (`ClientMutationID` on 13 mutation param types, validated via
  `ValidateMutationParams`; the queue-mutation fences `ExpectedQueueRevision`,
  `ExpectedEntryID`, `ExpectedInstanceID`; the reconnect flow in
  `2026-07-28-appwire-retry-safe-mutations-and-atomic-rejoin-design.md` and
  `2026-07-30-single-composer-mutation-recovery-design.md`): after reconnect the
  client retries with the *same* `ClientMutationID`, and the durable dedup
  answers "already applied" with the original outcome rather than applying
  twice. An abandoned (never-started) mutation retries as a fresh application.
  This design adds no new state to that contract; it only makes the
  abandoned-vs-canceled cases more common relative to ran-to-completion.
- *The remaining class is small and gets a bounded pre-implementation audit,
  gating the worker.* The only handlers prompt cancellation can touch are those
  that bind their context *and* await between durable side effects. Slice 0 of
  the landing order inventories every registration mechanically — all 105, hub
  and daemon, classified serial/concurrent, read/mutation, ctx-ignoring/
  ctx-binding (18 of 24 daemon handlers already classified above; the hub's 81
  get the same sweep) — then reads each ctx-binding *serial mutation* body for
  awaits between durable writes. The acceptance artifact is a disposition table
  appended to this spec, one row per registration, and it must be complete
  before slice 1 lands: any member that does not already tolerate mid-flight
  cancellation (the gate-release/retry shape `thread/clear` has) gets its fix —
  or its detachment from connection cancel — landed *before or with* the worker,
  with a test, so the prompt-cancellation behavior never deploys ahead of a
  handler that cannot take it. This is a bounded audit of a grep-able class with
  a hard gate, not the 105-handler ordering audit this design exists to avoid —
  the ordering audit would re-derive every handler's cross-request assumptions;
  this one reads six named daemon bodies and whatever the hub sweep names.
- *Post-cancellation access to connection-owned state is fenced at the seam, not
  per handler.* Handler code can reach connection-owned state only through five
  entry points, each already fenced for exactly this race because PR #667's
  concurrent slow reads need it: `appserver.Subscribe` and
  `appserver.ReplaceSubscriptions` verify `server.conns[conn.id] == conn` under
  the projection gate (verified: they are the only subscription paths handlers
  use — `cmd/evener-hub/app_relay.go` — the `Connection` methods have no
  production callers); `CaptureSubscription` carries the same registration check
  under the same gate; `Notify` and response enqueue land on the
  `sendClosed`-fenced channel. Everything else a handler touches is server-global
  state (stores, projectors) that must already tolerate concurrent mutation from
  *other* connections. A per-handler audit would re-verify 105 handlers against
  a race the seams already close; this design instead pins the seam behavior in
  the test plan (test 8).

### Shutdown, abandonment, teardown

Worker lifecycle is owned by `ServeWebSocket`, symmetric with the send loop:

- The worker starts before the receive loop runs and exits when the connection
  context is done. The receive loop is the only producer, the worker the only
  consumer, and neither closes the channel — producer teardown is "stop sending"
  (the loop returned), consumer teardown is `ctx.Done()`, and the channel is
  garbage-collected with the `Connection`. For any future second transport, this is
  the embedding contract: the transport owns a cancelable connection context, starts
  the worker beside its send loop, feeds the enqueue path from its receive loop, and
  cancels the context when either loop exits — the same obligations `ServeWebSocket`
  discharges today for the send loop.
- **Close with a non-empty queue: abandonment, not drain.** When the receive loop
  returns, `ServeWebSocket`'s deferred `cancel()` fires and the worker exits at its
  next `select` without executing the remaining entries. Executing them would be
  work for a peer that is gone, and any hydration capture they might open would be
  aborted at response time regardless. Queued-but-unstarted requests hold no
  *handler* resources — no hydration finalizers exist until a handler runs
  `CaptureSubscription` — but they do hold their decoded frames, which is why the
  purge below exists.
- **Teardown purges the queue's buffered frames; ownership rule.** "The channel is
  garbage-collected with the `Connection`" is not sufficient on its own: an
  orphaned handler that ignores its canceled context retains the worker goroutine,
  through it the `Connection`, and through *that* the queue's buffered frames — up
  to capacity × frame size held behind one wedged handler. So teardown discards
  them explicitly: after the receive loop has returned (the queue's only producer
  has stopped — this is the ownership rule that makes the purge safe) and `cancel()`
  has fired, `ServeWebSocket`'s teardown drains the channel in a non-blocking loop,
  dropping every buffered message without executing anything. The worker may race
  it by winning a dequeue, but its post-dequeue `ctx.Err()` check discards the
  message just the same; both sides only ever discard. After the purge, an orphaned
  handler retains exactly one frame — the one it is executing — not sixty-four (a
  frame the receive loop held while blocked enqueuing is released when that loop
  returns). When the purge discards a non-empty queue it reports the count and the
  abandoned method names — never params, which can carry user content — through
  `Server.logf`. This is an aggregate teardown advisory, not request-level
  attribution: repeated methods are indistinguishable, and a frame discarded by
  the worker's post-dequeue check or released with a dying receive loop bypasses
  it. Request-level "did my mutation run?" diagnosis belongs to the retry
  contract above (`ClientMutationID` dedup answers it authoritatively on
  reconnect), not to this log line.
- **Teardown does not join the worker.** `unregisterConnection` proceeds while a
  parked handler may still be executing, exactly as it does for PR #667's concurrent
  slow reads: `closeSend` flips `sendClosed`, the parked handler's eventual
  `enqueueResponse` fails, its hydration finalizer (if any) aborts, and
  `takeAllHydrations` in `unregisterConnectionLocked` has already swept whatever was
  registered. A wedged handler must not be able to hold connection teardown hostage;
  teardown-by-cancellation is the existing contract and the worker inherits it.
  The obvious worry — a still-executing handler registering hydration state *after*
  the sweep, leaving teardown incomplete — cannot happen, and the argument is
  already load-bearing for PR #667's concurrent slow reads: `captureSubscription`
  verifies `server.conns[conn.id] == conn` while holding `projectionMu`, the same
  gate `unregisterConnection` holds for the sweep, so a capture either completes
  before the sweep (and is swept) or observes the unregistration (and aborts
  without registering). Response enqueue is likewise fenced by `sendClosed` under
  `sendMu`. The worker adds no new registration path, so it inherits the fence.
- **Non-blocking teardown, not absolute leak-freedom.** The worker can only be
  blocked in its `select` (exits on cancel), in `enqueueResponse` (selects on
  `ctx.Done`), or in a handler. The last is the honest limitation: a handler that
  ignores its canceled context retains the worker goroutine — and through it the
  `Connection` — until it returns. That is cooperative cancellation, the same
  bound every execution path has today (an inline handler retains the receive-loop
  goroutine identically), not a hard guarantee, and no handler
  cancellation-deadline contract exists to make it one. What the design does
  guarantee is that teardown never *waits* on such a handler and that a worker
  whose handlers return eventually exits. The implementation should expose the
  worker's exit to tests the same way the keepalive loop exposes decisions
  (`keepaliveDecision`): a package-private done channel or callback seam, so the
  abandonment test asserts exit instead of sleeping.

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
2. **Ordering across the worker, pairwise** — one case per cell of the partial-order
   table: serial→serial (the later handler observes the earlier one's completion;
   responses in request order), serial→slow-read (a slow read does not start until
   the parked serial handler ahead of it completes), slow-read→serial (the serial
   request completes while the earlier slow read is still parked), and
   slow-read→slow-read (two parked slow reads are in flight simultaneously). The
   carried serial-ordering and overtaking tests cover the first and third cells
   already; the second and fourth are new.
3. **Panic in the worker** — extend the carried panic test: requests queued *behind*
   the panicking one still execute and answer; the worker goroutine survived.
4. **Queue-full backpressure** — with an injectable small capacity (a
   package-private field beside the existing `keepaliveTickerFactory` seam, so the
   test does not need 65 real frames) and a blocked-enqueue seam that signals when
   the receive loop is actually parked on the full queue: park a serial handler,
   pipeline past capacity, wait for the blocked-enqueue signal; assert no wire error
   and no eviction, then release the handler and assert every response arrives, in
   order. For teardown-under-saturation: reach the blocked-enqueue signal, close
   the client, then release the parked handler; assert the connection tears down
   cleanly and the worker exits (exit seam). The release step is part of the
   contract being tested, not a test convenience — the design states that a close
   arriving during saturation is *observed* only when a queue slot frees and the
   loop re-enters `Recv`, exactly like PR #667's inline-handler window.
5. **Abandonment on close** — park a serial handler, queue several requests behind
   it (counting handlers), close the client; assert — *while the parked handler is
   still parked* — that the teardown purge emptied the queue (a package-private
   length check or purge-complete seam), then release the handler and assert the
   worker exits and the counters stayed untouched. The sequence is deterministic,
   not racy: the parked handler pins the worker inside `executeOrdered` for the
   whole close-and-purge window, so the worker cannot dequeue a queued request
   before the purge empties the queue, and after release it observes the canceled
   context before it could execute anything. The side-effect counters are the
   abandonment contract; a "no responses arrived" assertion is deliberately absent
   because a closed client cannot tell "never ran" from "ran and the enqueue was
   refused". Hydration-finalizer cleanup is likewise *not* asserted here — a queued
   request that never started cannot have opened a capture, so the assertion would
   be vacuous; the executing-capture abort path is already pinned by the carried
   accounting and unregister tests.
6. **Cancellation observed at dequeue, deterministically** — the post-dequeue
   `ctx.Err()` re-check cannot be pinned by racing a close against a release
   (`select` may pick either case and the test would flake or pass vacuously). Use
   a package-private after-dequeue hook in the style of the existing seams
   (`afterUnregisterDelete`, `keepaliveDecision`): park the worker in the hook with
   a counting handler's request dequeued, cancel the connection, release the hook;
   assert the handler never starts.
7. **One saturation advisory per connection** — drive the queue to the blocked
   state repeatedly on one connection; assert exactly one advisory line reached
   `Logf` (matching on the connection id, not exact wording), so an implementation
   can neither flood the log under sustained backpressure nor skip the advisory.
8. **Executing serial mutation across disconnect** — the admitted-request contract
   above, pinned from both sides. A context-aware serial mutation parks at an await
   point; the client disconnects; assert the handler's context cancels promptly
   (the new promptness this design introduces) and the handler unwinds. Then a
   context-*ignoring* serial handler parks, the client disconnects and teardown
   completes, and the handler resumes to call `appserver.Subscribe`,
   `CaptureSubscription`, and `Notify`: assert each is refused by its fence
   (subscription/capture rejected on the unregistered connection, notification
   dropped on the closed send channel) and nothing leaks into `Subscriptions`.
9. **Keepalive live during a slow mutation** — using the `keepaliveDecision` seam:
   with a serial handler parked, keepalive ping decisions report the reader
   available. Fails against PR #667; pins the incidental improvement so it cannot
   silently regress.

## Rejected alternative: full-async dispatch for all handlers

Recorded here as the paper trail for not doing it, because "make everything
concurrent so nothing blocks anything" is the obvious question and was asked
explicitly.

`0291be074` was that design: every request on its own goroutine. The audit's C1 and I3
findings are what it cost, and the deeper problem is the contract change:

- **Per-connection ordering is load-bearing.** Every handler among the 105 hub and
  daemon registrations (81 in `cmd/evener-hub`, 24 in `server/appwire_runtime.go`,
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
handler — while keeping the contract all 105 handlers were written against, at the
cost every ordering contract carries: queued requests wait their turn.

## Migration and implementation shape

Assumes PR #667 is merged; this change is a delta on its end state. The worker
mechanism itself touches only `internal/appserver/server.go` and
`internal/appserver/websocket.go`; slice 0's audit may additionally name handler
remediation, which lands with its own per-handler estimate before the worker (see
the landing order) and is deliberately outside the mechanism estimate below.

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

Landing order — a gating pre-implementation slice and two code slices, each code
slice landing its behavior *with* the tests that pin it (test-driven, each
independently green and safe to deploy alone — no slice ships the worker without
the semantics the final design requires):

0. **Audit and measurement — a hard gate, remediation included.** (a) The
   ctx-binding mutation audit from "Admitted-request semantics on disconnect",
   producing the complete 105-row disposition table appended to this spec; any
   handler the audit finds intolerant of mid-flight cancellation gets its fix (or
   its detachment from connection cancel) and its test landed *here*, before the
   worker exists, so slice 1 can never deploy prompt cancellation ahead of a
   handler that cannot take it. (b) Burst-depth measurement for
   `requestQueueCap`. It cannot come from the not-yet-built queue, so measure at
   the existing edges: count requests per named UI action in the web client's
   request layer and the TUI's client (static reading plus a browser-devtools
   frame count on the real UI), and apply the min(burst × 4, 64) rule from
   "Bound". No production code beyond audit remediation.
1. **Worker, queue, ordering, ping — with its lifecycle floor and its tests.**
   The receive-loop split, the worker with its classification, the inline ping
   bypass, the post-dequeue cancellation re-check, the teardown purge, and the
   rewritten policy comment — the re-check and purge belong to the worker's
   minimum correct form, and so do the tests that pin them: a slice that could
   execute queued work after cancellation or strand sixty-four frames must not
   exist even briefly, and a slice whose lifecycle floor is untested has not
   landed its behavior with its tests. Carried tests green unmodified, plus
   tests 1–3, 5, 6, 8, and 9 with the seams they need (worker-exit,
   after-dequeue, purge-complete).
2. **Saturation** — the blocked-enqueue path's one-shot advisory, the injectable
   capacity and blocked-enqueue seams, the measured `requestQueueCap`, plus
   tests 4 and 7.

Estimated size for the worker mechanism: roughly 120–160 lines of production delta
(most of it comments and the worker loop) and 450–550 lines of new tests per the
plan above; handler remediation, if slice 0 names any, is additional and estimated
per finding. No wire-format or API change and no client change; handler changes
are exactly the slice-0 remediation set (expected small, possibly empty); the one
wire-observable behavior change is scheduling — a ping response may now overtake
earlier responses, which the ordering-contract section shows is meaningless to
id-correlating clients.

## Open questions

- **Unbounded concurrent slow reads, and aggregate per-client admission.** PR #667
  spawns a goroutine per slow read with no cap (the old limiter died with the
  overflow error). A client can pipeline many `thread/read`s and hold that many
  transcript walks — and their decoded params frames — in flight, and can multiply
  any per-connection bound by opening more connections. This design neither worsens
  nor fixes that class; the queue-memory analysis above concludes that closing it
  for real means an aggregate per-client admission budget (bytes and goroutines
  across all of a client's connections), which is its own design. If only the
  slow-read count needs a bound sooner, a per-connection semaphore around the spawn
  is a separate small change.
- **Queue capacity.** Resolved to a process rather than a number: 64 is the
  provisional ceiling, slice 0 measures real burst depth, and the frozen constant
  is worst-measured-burst × ≥4 headroom (see "Bound" and the landing order). The
  backpressure behavior is capacity-independent, so nothing else in the design
  moves with the number.
- **Should `evener/auth/device/poll` still do network I/O on the RPC path at all?**
  The worker makes it harmless to the transport, but a poll loop that holds the
  serial queue for a network round trip still delays same-connection requests behind
  it. Restructuring device-poll (server-side polling with a notification) is out of
  scope here and may be worth its own note.
