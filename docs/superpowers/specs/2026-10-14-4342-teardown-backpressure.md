# Spec: Explicit backpressure for inbound queue saturation (#4342)

Issue: [prime-radiant-inc/evener#4342](https://github.com/prime-radiant-inc/evener/issues/4342)
Branch: `fix-4342-teardown-backpressure`
Approved mechanism (Jesse, 2026-10-11): explicit hub backpressure signal — refusal at
teardown, saturation signal — with client pacing and a surfaced busy state; the client
outbox remains the owner of unsent intent.
Amended (Jesse, 2026-10-14): the round-2 review proved an in-band teardown refusal frame
physically undeliverable — the pinned websocket library closes the server's socket
inside `Recv` the moment it reads the client's close frame, and every server-side exit
closes the socket before teardown code runs (verified in coder/websocket v1.8.14
`read.go`/`close.go`/`write.go`; found independently by both reviewers). A dead socket
cannot carry a refusal, and the incident's mutations entered the queue with room to
spare, so refusing only when the queue is full would miss them too. Jesse approved the
amendment: the refusal fires at a high-water queue depth while the connection still
carries writes, and the outbox owns teardown recovery.

Revision 4 — folds the round-3 review (both reviewers: implementable, no blocker;
findings were fine-grained): the high-water refusal gets its own one-shot advisory so
it cannot consume the full-queue block advisory; the false-positive cost is stated
honestly per host (the queue can sit deep in healthy bursts at `slowReadDispatchCap`
16; outbox hosts pay a paced retry, the TUI and C13 fallback a manual re-press); the
ride-window and teardown-redelivery claims are corrected (below-high-water riders
exist and follow the pre-existing `blockedUnknown` reopen path); the refusal's
precedence over params validation and the no-extractable-id degradation path are
documented.

## Why (incident summary)

During the 2026-10-11 evening wedge of session 034ZofFCdFFliT15Mqx1bm, the user sent at
least 10 messages to a session whose view would not load. Every one was lost from the
user's point of view:

- The hub logged `connection conn-45 discarded 63 undispatched queued frames at teardown
  (… turn/start=10)` — `purgeRequestQueue` (internal/appserver/server.go) discarded ten
  `turn/start` frames that had been accepted on the wire but never dispatched.
- The session's mutation journal shows nothing after 18:37 UTC: no `turn/start` was ever
  admitted, so reconnect-time ClientMutationID dedup has nothing to replay against.
- The browser kept the messages in its outbox retrying ("queue forever"), because the
  hub never told it they were dropped.

The discard chain: a connection's serial worker parks (slow-read slot acquire, or any
inline request awaiting a daemon) → reads pipeline into the 64-frame inbound queue until
it fills → the receive loop blocks in `enqueueRequest` on the next frame — the incident's
queue was dominated by reads, so that frame is a read — so keepalive pings go unanswered
and the client disconnects within its 15s ping + 10s pong budget → teardown discards
every queued frame. The ten `turn/start` frames entered the queue while it still had
room; by the time it was full, the connection was already doomed.

`purgeRequestQueue` treats queued frames as discardable on the assumption that "did my
mutation run?" belongs to reconnect dedup — but a frame discarded before dispatch was
never run, so nothing survives to dedup against.

The contract that failed, in the issue's words: **an admitted-on-the-wire `turn/start`
either reaches admission or the client learns it did not.** S05 already names the states
this needs: "wire compatibility, a stale cache, a lost response and an authoritative
refusal are distinct states." Today's saturation and teardown produce none of them — the
mutation rides a doomed queue silently and dies silently.

## Scope

### 1. Wire vocabulary (appwire)

One new `ErrorInfo` discriminant for connection-level backpressure refusals, with one
`Cause` string:

- `ErrorConnectionBackpressure ErrorInfo = "connectionBackpressure"`
- Cause `"queueSaturated"` — the inbound request queue is at or beyond its high-water
  depth; the hub refused the mutation without queueing it.

A refusal is a `WireError` with `CodeUnavailable`, carrying in `Data`:

- `EvenerErrorInfo: "connectionBackpressure"`,
- `ClientMutationID`: extracted from the request's own params (see below),
- `MutationOutcome: "notAccepted"` — the truth: this mutation never started running,
- `RetryDisposition: "automatic"` — an identical retry is safe (the ClientMutationID
  dedup on admission already guards a double-apply) and expected,
- `Cause: "queueSaturated"`.

The hub's message states what happened on the hub, nothing about what the client will
do: e.g. "the hub's inbound queue is busy and did not accept this message". Retry
wording ("Retrying automatically…", a Retry press's guidance) belongs to the client
surface that owns the retry, because the TUI and the C13 fallback path do not retry
automatically and must not inherit a promise the hub cannot keep. Tests assert these
surfaces structurally (per the copy-oracle convention), never by wording comparison.

A Go constructor `appwire.BackpressureRefusal(clientMutationID, message string)` builds
the refusal; the hub's refusal site shares it.

A Go-side retry-safe mutation method set, exported from `appwire` (e.g.
`RetrySafeMutationMethods`), mirroring the shared TypeScript dispatcher's set. The shared
package exports `RETRY_SAFE_MUTATION_METHODS` (today a module-private `const` in
`appwire-client/typescript/state/mutation/dispatcher.ts`) so a parity test can pin the
Go and TS sets equal. The set is the nine methods both sides already agree on:
`turn/start`, `turn/steer`, `turn/interrupt`, `turn/queue`, `turn/drainAsSteer`,
`turn/promoteQueuedAsSteer`, `turn/cancelQueued`, `thread/clear`, `notes/human/set`. The
hub refuses only frames in this set.

`clientMutationId` extraction: the refusal site decodes the request's raw JSON params
into a minimal `{clientMutationId string}` struct for cataloged mutation methods only.
The extracted id is never logged.

### 2. Hub: high-water saturation refusal (internal/appserver)

In `enqueueRequest`, before the enqueue attempt, when the frame is a request in the
retry-safe mutation set and the queue's depth is at or beyond a high-water mark — half
the queue's capacity, 32 of the 64 `requestQueueCap` frames — the hub answers the
request inline: mint a `queueSaturated` `BackpressureRefusal` and enqueue it through
`recoverPanic` + `enqueueDispatched`, mirroring the existing force-stop busy refusal in
`receiveInbound`. Return `true` so the receive loop continues.

The receive loop is the queue's only producer, so `len(c.requests)` is exact at the
check — no race, no new state. A refusal never enqueues, so it never consumes queue
depth; only reads and other non-mutation frames do.

Why high water, and why this is the trigger that answers the incident: the ten
`turn/start` frames entered a queue with room to spare — a refuse-only-when-full rule
would have queued them onto the doomed connection and lost them at teardown. Refusing
at half depth keeps every later mutation out of a connection that is demonstrably
backlogged, and the refusal is delivered while the connection provably still carries
writes.

The cost of a refusal that turns out unnecessary (the worker was merely busy, not
wedged) is honest and host-shaped: the queue can legitimately sit deep during healthy
operation — `slowReadDispatchCap` is 16 concurrent slow reads, and flows like a
reconnect resync pipeline many reads onto one connection at once — so a mutation sent
during a deep-but-healthy burst can be refused. An outbox host (browser, native) pays
one paced retry and a briefly surfaced busy state. The TUI and the C13 direct fallback
pay a manual re-press: the refusal surfaces as a send failure with the hub's message
and the draft preserved. The alternative in the same window is a send sitting
unacknowledged behind the queue's FIFO until the worker drains it — the refusal tells
the user now instead of leaving the send hanging, and it never loses the message: the
outbox (or the preserved draft) owns it either way.

The refusal also answers before the worker's own params validation (validation happens
at dispatch, and the refusal happens before enqueue): a malformed mutation payload
refused under load surfaces its real invalid-params error on the retried attempt. The
classification is safe — `CodeUnavailable` is not `refusedBeforeRunning`, so no client
reads the refusal as terminal, and the record stays retryable until the genuine
answer arrives. If the hub cannot extract a `clientMutationId` from the refused
request's params, it still refuses (the frame is never queued either way); the client's
dispatcher then takes its names-none path — the record stays `submitting`, no busy
surface, and the next flush event retries it. A degraded surface, never a loss.

What this buys, stated honestly:

- **The mutation never joins a backlogged queue.** It is answered at once with a typed
  refusal, the client's outbox row stays queued, and the row is paced for retry.
- **The receive loop stays available while it is refusing mutations.** It still parks
  when a non-mutation frame meets a full queue — the incident's queue was dominated by
  reads, so the frame that parks the loop is usually a read, and pings pipelined behind
  it are never read. The refusal branch does not rescue connection liveness for the
  read-dominated wedge; that is #4344's territory (non-goal).
- **The inline refusal can itself park** when the outbound buffer is full
  (`enqueueDispatched` → `enqueueResponse` selects on `c.send` until the send loop
  drains it or the connection dies) — the same acknowledged trade the inline ping path
  makes: liveness is guaranteed against handler behavior, not against a peer that
  stopped draining its own socket.

Non-mutation frames keep the existing blocking pressure valve unchanged at every depth:
a full queue still parks the receive loop, inbound frames still accumulate in the kernel
socket buffer, and TCP flow control still reaches the client.

The once-per-connection saturation advisories are now two, each with its own one-shot
flag, following the codebase's existing pattern (the slow-read cap advisory keeps its
own `capSaturationAdvised`):

- the existing full-queue advisory, unchanged, fires when a non-mutation frame first
  blocks on the full queue — the operator-critical wedge signature;
- a new high-water advisory fires when a mutation is first refused at high water, with
  wording accurate for the refusal branch (a refusal does not block the receive loop).

One flag must not consume the other: a refusal at high water must not suppress the
later full-queue block advisory, and vice versa. Today's line asserts blocking, which
would be false on the refusal branch.

Ordering safety: the browser and native dispatch durable mutations one record at a time
per target ref, so a refused mutation cannot be overtaken by a later mutation for the
same ref — the outbox's per-ref FIFO does not advance past an unsettled row. Across refs
there is no ordering contract to violate.

### 3. Teardown: unchanged, and owned by the outbox (internal/appserver + websocket.go)

Teardown keeps today's behavior exactly — `cancel()`, then `purgeRequestQueue()`, then
`transportLoops.Wait()`, with the advisory tally line. No in-band refusal is attempted,
because none is deliverable:

- **The physics (round-2 finding, verified in the pinned library source):** the receive
  loop returns only when the peer/socket is gone or the connection context was
  canceled. On a client-initiated close — the incident's own shape — coder/websocket
  v1.8.14 closes the underlying TCP connection inside `Recv` when it reads the peer's
  close frame (`read.go`: `if !c.casClosing() { c.readMu.unlock(); _ = c.close() }`),
  and later writes return `net.ErrClosed` (`write.go`). On every server-side exit the
  loop's own defers (`ws.CloseNow` when the context is canceled, `ws.Close` on read
  errors) close the socket before `ServeWebSocket` regains control. There is no
  post-exit write window in any path.
- **The teardown answer is the client outbox, by design:** a mutation still riding the
  queue at teardown was never admitted (the worker's post-dequeue cancellation
  re-check and the purge both discard without executing), so the client's outbox row
  survives. Its actual recovery path is the pre-existing delivery-uncertain machinery,
  unchanged: the row keeps its `attempted` marker (no refusal ever arrived for it), so
  a first reconnect read that is not mutation-authoritative parks it `blockedUnknown`
  on both hosts, and an authoritative read that proves it absent reopens it
  (`restoreProvenAbsent`) for redelivery — the same path every uncertain send already
  takes. The high-water refusal (§2) keeps this set small: only mutations enqueued
  while the queue was below high water can be riding at teardown, and the incident's
  pile-up shape — dozens of mutations queued onto a backlogged connection — no longer
  exists.
- **One bounded window remains, documented:** between `cancel()` and the worker's next
  dequeue, the worker can win one racing dequeue and discard that frame under its
  post-dequeue re-check — neither executed nor purge-collected. The window is one
  frame, pre-existing, and its row is outbox-protected exactly like every other
  undelivered mutation. No new machinery is added for it.

A shutdown-only flush (drain-and-refuse through the live send loop before the hub cancels
draining peers during hub-initiated shutdown) was considered and deferred: it needs a
cooperative receive-loop exit redesign and serves only the hub-restart case the outbox
already redelivers. Filed as a follow-up issue.

### 4. Shared client dispatcher: a transient refusal keeps the record queued (appwire-client)

The shared `MutationDispatcher` (#attempt's catch) gains one classification before the
existing `notAccepted` branch: when `mutationErrorData(error)` reports
`mutationOutcome: "notAccepted"` with `retryDisposition: "automatic"`, the refusal is
transient. The dispatcher:

- keeps the record `submitting` in the outbox FIFO (the attempt settles as `"stop"`; a
  later ready, discovery, or paced event starts the next attempt — the loop never spins
  on a refusal),
- clears the record's durable `attempted` marker through a new storage-port method
  (`unmarkAttempted(clientMutationId)`), returning the row to its never-attempted shape,
  and
- fires a new optional `onTransientRefusal(record, error)` callback so hosts surface the
  busy state and arm a paced retry.

Without this rule the existing `notAccepted` branch would move the row to recovery as
`rejected` and stop retrying — the wrong outcome for backpressure, and the reason the
disposition, not just the outcome, drives the classification.

The `attempted`-marker clear is load-bearing on **both** hosts, not only native: each
host's reconnect reconcile parks every `submitting` + `attempted` row as
`blockedUnknown` when the first read is not mutation-authoritative (native
`mobile-native/src/nativeMutationRuntime.ts` in its non-authoritative branch; browser
`cmd/evener-hub/frontend/src/stores/threads.ts` in the reconcile path, where the
`!mutationStateAuthoritative || restartRequired || notLoaded` arm scans the outbox and
`markUnknown`s `submitting` rows with `onlyAttempted: true`). A row the hub just
authoritatively refused would be parked as unknown. Clearing the marker returns the row
to what it was before the attempted dispatch, so both scans skip it and the flush
rediscovers it as a fresh dispatchable row. The clear is safe against cross-tab
double-dispatch precisely because the refusal is authoritative for the attempt: the
hub's journal holds no record for a `queueSaturated` mutation, and ClientMutationID
dedup makes a raced re-dispatch a replay no-op.

`unmarkAttempted` is added to the shared `MutationOutboxStorage` port
(appwire-client/typescript/state/mutation/outbox.ts), implemented by both adapters (web
IndexedDB `mutationOutboxIndexedDB.ts`, native `mutationOutboxStorage.ts` /
`MutationOutboxSQLite`), and pinned by the shared storage conformance suite both
adapters already run (`@evener/appwire-client/testing/mutationOutboxStorageConformance`).

The existing branches are untouched: `notAccepted` without `automatic` still recovers
`rejected` with the daemon's reason; `targetDeleted` still orphans; `unknown` +
blocked/persistence still parks `blockedUnknown`; transport failures still retain
`submitting`. `mutationErrorData` and `refusedBeforeRunning` live in
appwire-client/typescript/errors.ts and already accept this error's shape (round-1
review verified no existing consumer misclassifies it: `refusedBeforeRunning` is false
for `CodeUnavailable`, and the history/activity/native readers match other
discriminants).

### 5. Browser (cmd/evener-hub/frontend)

The threads store consumes `onTransientRefusal`:

- **Surface:** the affected pending row shows a busy state — the hub explicitly did not
  accept this message yet and it will retry — rather than sitting in the queue with no
  explanation. The marker clears when the record settles (receipt or reclassification),
  including a settlement another tab performed: every dispatch tail re-reads the shared
  storage, and the settle prune rides it, so this tab's marker leaves even when its own
  dispatcher never saw the settle. A row that is not `submitting` — accepted work, in
  particular — never reads as refused, whatever the marker names.
- **Pace:** the store arms a retry through the existing exponential-backoff idiom
  (500ms base doubling to a 15s cap, with an injectable scheduler for tests, mirroring
  `hydrationRetryScheduler`), so a saturated hub is not hammered and a freed hub is
  re-driven promptly.
- **Reconcile survival:** the refused row must survive the browser's own reconnect
  reconcile scan (the `markUnknown` park named in §4) and redeliver — pinned by test.

The C13 direct-send fallback (`dispatchMutationDirectly`, used when the durable write
is unavailable) has no outbox row to keep, so it does not pace: a backpressure refusal
there surfaces through the existing send-failure path with the hub's truthful message
and the draft preserved, and the user re-presses Send. The spec claims no paced retry
for that path; a test pins the surfaced shape.

### 6. Native

Native shares the dispatcher and outbox, and the native-specific behavior that would
otherwise defeat the mechanism is covered: the dispatcher's `attempted`-marker clear
(§4) keeps the refused row out of the reconnect reconcile's `blockedUnknown` park, so
the row is redelivered by the ordinary flush on reconnect. Native's queued/unconfirmed
rows already surface as pending (C05), and its automatic reconciliation on reconnect is
unchanged. No native-specific UI beyond what the shared dispatcher gives it; a native
runtime test pins that a refused row survives the reconnect scan and redelivers.

### 7. TUI

The TUI's send path (`hub_commands.go`) surfaces the typed refusal through its existing
"Send failed:" error line with the draft preserved, exactly as transport failures do
today, and its own retry guidance ("Check the hub connection and retry the action")
stays truthful because the hub's message no longer promises client-side retries.
Automatic TUI retry remains out of scope; the typed refusal makes it possible later.

### 8. Product documentation

Evergreen true-up in the same change: the S05 row gains the backpressure refusal as a
named authoritative-refusal state (distinct from a lost response) and names the
outbox-owned teardown recovery; the S11 row's "client flush owner … must arrange
re-entry after a temporary failure" gains the paced re-entry an explicit backpressure
refusal triggers; `appwire-protocol.md` documents the new discriminant and its cause.

## Behavior

1. A retry-safe mutation request arriving on a connection whose inbound queue holds at
   least half its capacity (32 of 64 frames) is answered on that connection with a
   `connectionBackpressure` refusal (`notAccepted` + `automatic`, cause
   `queueSaturated`) carrying the request's own `clientMutationId`. The receive loop
   continues. A mutation arriving below high water rides the queue exactly as today —
   the trigger's guarantee is that a backlogged connection stops accepting new
   mutations, not that no mutation can ever be queued.
2. Non-mutation frames are unchanged at every depth: FIFO ordering, blocking pressure
   valve at full, kernel-buffer accumulation, TCP flow control.
3. Teardown is unchanged: silent purge of queued frames with the advisory tally, no
   in-band signal (physically undeliverable — §3). A mutation still queued at teardown
   — one enqueued below high water — is covered by the outbox: its row parks
   `blockedUnknown` on a non-authoritative reconnect read and reopens for redelivery
   when an authoritative read proves it absent, exactly like every uncertain send
   today.
4. A client that receives a backpressure refusal keeps its outbox record queued
   (`submitting`), rolls back the refused attempt's evidence, surfaces a busy state
   on the affected row, and paces its next attempt with exponential backoff
   (500ms → 15s). The rollback resolves only the refused attempt: the row's
   `attempted` marker clears only when that attempt was the row's one unresolved
   attempt, so evidence of an earlier or concurrent unresolved attempt (a lost
   response, another tab's in-flight dispatch) survives and the row keeps its
   uncertain treatment — Stop cannot cancel it as never-sent, and the reconnect
   reconcile's onlyAttempted-guarded park keeps guarding it. A row parked
   `blockedUnknown` while its only attempt was in flight reopens, because the
   refusal is authoritative proof that park's uncertainty resolves. The busy state
   and the pacing arm only when the rollback returns the row to its
   never-attempted shape. The record
   settles by the ordinary receipt path when a retry lands, and survives both hosts'
   reconnect reconcile scans.
5. A refusal that cannot be delivered changes nothing: the record stays `submitting`
   under existing unknown-outcome semantics, and reconnect-time flushes reconcile it.
6. No mutation is refused after dispatch: refusals happen strictly before enqueue, so
   the mutation journal, ClientMutationID dedup, and all daemon semantics are
   unchanged. The pre-existing one-frame worker racing-dequeue window at teardown is
   unchanged and outbox-protected.

## Non-goals

- Fixing the starvation wedge itself (#4344 owns the poller starvation; #4343 owned the
  rebuild wedge). High-water refusals keep mutations out of a backlogged queue and make
  the jam visible; they do not keep a read-dominated saturated connection alive.
- In-band teardown refusal frames: physically undeliverable with the pinned transport
  (§3); the outbox owns teardown recovery by design.
- A shutdown-only flush before hub-initiated connection cancel — deferred to a
  follow-up issue (needs a cooperative receive-loop exit redesign).
- A durable server-side pending-input record (Jesse chose the client outbox as the owner
  of unsent intent).
- Dispatch-class exemption for mutations (Jesse chose refusal; the blocking pressure
  valve for reads is preserved deliberately).
- A connection-level "backlogged" notification method: the per-mutation refusal is the
  saturation signal; read paths already own their retry pacing and error surfaces.
- Per-method request-pool saturation (the second, narrower advisory surface) — the
  incident's mechanism is the inbound queue.
- Paced retry on the C13 direct-send fallback path (no durable row exists there; the
  refusal surfaces as an ordinary send failure with the truthful message).
- Native-specific busy UI beyond what the shared dispatcher gives it, and TUI automatic
  retry.

## Recovery ownership

The client outbox remains the single owner of unsent intent (S11), for teardown and for
every undeliverable refusal alike. The hub's refusal is authoritative and final for the
attempt it names — "not accepted, retry automatically" — and the existing reconnect
machinery (flush on ready, discovery re-scan, reconcile by ClientMutationID) remains the
recovery path. No server state is created by a refusal.

## Test plan (whole journey)

All default tests deterministic, no live providers; appserver seams used:
`requestQueueCapacity`, `blockedEnqueue`, `afterWorkerDequeue`, the hub RPC test
fixtures, and an injectable transport/observer where needed.

1. **High-water refusal (appserver):** with the worker parked and the queue filled to
   depth 31, a `turn/start` enqueues normally; at depth 32 a pipelined `turn/start` is
   answered inline with the typed refusal carrying the request's `clientMutationId`,
   and the queue depth is unchanged by the refusal; further pipelined mutations are
   each refused; at every depth reads and other non-mutation frames enqueue normally
   and block only when the queue is full; the two advisories are independent one-shots
   (a high-water refusal's advisory does not suppress the later full-queue block
   advisory, and each wording is accurate for its branch); the inline refusal parks
   bounded on a full outbound buffer rather than wedging the loop; a mutation whose
   params carry no extractable `clientMutationId` is still refused at high water.
2. **Dispatcher classification (shared TS):** `notAccepted` + `automatic` keeps the
   record `submitting`, clears the `attempted` marker, fires `onTransientRefusal`, and
   does not advance the FIFO to recovery; `notAccepted` without `automatic` still
   recovers `rejected`; `unknown` + blocked still parks `blockedUnknown`; transport
   failures unchanged.
3. **Storage port:** `unmarkAttempted` returns an attempted submitting row to its
   never-attempted shape, refuses non-submitting rows, and is pinned in the shared
   conformance suite for both adapters.
4. **Set parity:** the exported Go `RetrySafeMutationMethods` equals the exported TS
   `RETRY_SAFE_MUTATION_METHODS`.
5. **Browser surface and pacing:** the pending row shows the busy state while refused,
   clears on settlement, and the paced retry drives a redispatch through the injectable
   scheduler (red first: without the classification, the row parks in recovery). The
   refused row survives the browser's non-authoritative reconcile scan (no
   `blockedUnknown` park) and redelivers.
6. **Native reconnect:** a refused row survives the reconnect reconcile scan (not
   parked `blockedUnknown`) and redelivers on flush.
7. **C13 fallback:** a refusal on the direct-send path surfaces as the existing send
   failure with the hub's message; no row, no pace, draft preserved.
8. **TUI non-regression:** a typed refusal surfaces in the existing send-failure line
   with the draft preserved (existing e2e seam).
9. **Teardown unchanged (appserver):** the purge still discards silently with the
   advisory tally; no teardown code path attempts a write; the worker's
   racing-dequeue window behaves exactly as today.
10. **Whole journey (real hub + store):** drive a real connection through the hub test
    server — park the worker, pipeline reads to high water, then pipeline `turn/start`
    frames: each is refused in-band, the client store surfaces busy and paces; unpark
    the worker; the paced retries land and the mutation journal admits each row exactly
    once; end the connection with a read-only queue and confirm teardown needs no new
    machinery; reconnect and confirm any row still `submitting` redelivers.
11. **Docs:** the S05/S11/appwire-protocol rows name the new refusal state, the
    outbox-owned teardown recovery, and paced re-entry.

## Implementation notes (2026-10-14)

- Test plan item 10's journey is covered by composition rather than one
  cross-language test: the hub-side journey runs against the real appserver
  transport and queue (park the worker, pipeline reads to high water, pipeline
  `turn/start` frames, each refused in band with its own clientMutationId, the
  refusal consumes no depth, a retried send into the freed hub validates and
  executes exactly once), and the client-side journey runs the real store
  against the same wire envelope (busy marker, doubled pacing, marker clear on
  the accepted retry, reconcile survival on both hosts, C13 fallback
  message-only). The repo has no harness that runs the TypeScript store
  against a real Go hub, and building one for this change would be a new
  fixture class, not a test. The wire envelope itself is pinned from both
  sides (Go asserts the JSON the hub sends; the TS dispatcher test parses the
  same fields).
- The Go retry-safe set follows the `KnownWireName` pattern: an unexported map
  plus an exported membership function `RetrySafeMutationMethod(name)`. The
  spec's `RetrySafeMutationMethods` spelling was illustrative ("e.g."); the
  parity test pins the Go and TS sets equal by reading the dispatcher source,
  and the TS set is exported (`RETRY_SAFE_MUTATION_METHODS`, re-exported from
  the package entry alongside the dispatcher).
- §5's busy surface reaches every pending-row surface, not only the send
  chips: queue rows in the composer strip and the steer family's ghosts carry
  the same marker (data-busy plus a visible Retrying/Waiting label), and the
  refusal's paced-retry timer replaces the pending one a prior refusal armed
  (re-arm cancels; no timer stacking under the discovery cadence). The busy
  marker itself clears on settlement — a receipt, a recovery transfer, a
  blocked reclassification, each of which the dispatcher announces as a
  storage change — not when the paced attempt fires: an in-flight attempt is
  still the refusal being worked. Methods with no pending row of their own
  (thread/clear, turn/interrupt, turn/cancelQueued) keep the retry machinery
  without a dedicated row to mark, and notes/human/set's busy state is the
  note editor's own pending state.
- The refusal's rollback is scoped to the refused attempt (implementation
  review, high finding, product owner approved fixing it in-PR): each
  `markAttempted` increments a per-row unresolved-attempt count (the new
  `unresolvedAttempts` record field; on native, a default-zero SQLite
  column with the instance_id-style ALTER migration), and `unmarkAttempted`
  decrements its own attempt's evidence. `attempted` clears only when no
  unresolved attempt remains; a row parked `blockedUnknown` while its only
  attempt was in flight reopens to submitting, while one parked with
  earlier evidence stays parked; and the dispatcher arms the busy surface
  and the pacing only when the rollback comes out clean. Records written
  before the count field existed carry their evidence as the bare
  `attempted` boolean, which both adapters read as one unresolved attempt
  (the browser normalizes at read time; native seeds the zero count from
  the flag before each count-touching write), so a refused retry preserves
  that legacy evidence too. One class older still (round-7 review,
  medium): a record with neither the count nor the `attempted` boolean -
  a pre-#936 row, persisted untouched through every later schema version
  because the browser's upgrade handler never rewrites records - has an
  unknown delivery state from that era, exactly the Stop guard's own rule
  for these rows ("never canceled by Stop"): the rollback's baseline
  reads it as one unresolved attempt, only an explicit `attempted: false`
  proving a clean row, so the refused attempt's rollback preserves the
  uncertain treatment instead of declaring the row never-attempted.
  Native is structurally exempt - its first schema already carried
  NOT NULL attempt columns, so no native row can hold the absent shape.
  Cross-tab
  settlements clear this tab's marker through the dispatch-tail prune, and
  every surface renders the refusal state only for `submitting` rows.
  The same review's mixed-version finding fences the browser database at
  version 4 (the stop-cancellation §8 precedent): a deployed version-3 tab
  dispatches without counting, so sharing rows with it would let this
  build's refusal rollback erase evidence the older tab's attempt still
  needs; its version-3 open now fails closed instead. The prune's own
  shared-storage read can fail, and a failed read proves nothing: the
  marker and its armed retry survive for a later recovery pass, and the
  fire-and-forget prune contains the failure rather than leaking an
  unhandled rejection.
  The refusal's rollback is also stop-fenced (round-4 review, high). The
  capture rides the enqueue barrier's own epoch read, stored on the row
  (`stopEpochAtEnqueue`; on native an additive default-zero
  `stop_epoch_at_enqueue` column) - an extra request inside the mark's
  write transaction reordered the note editor's tightly-sequenced drain,
  so the mark itself stays byte-identical to the pre-fence shape and the
  fence reads only at rollback time. A Stop whose cancel scan skipped the
  attempted row bumped the epoch after the enqueue-time capture, and the
  refusal's rollback compares the two inside its own transaction - that row
  holds `canceled` (released only by an explicit Retry, the born-canceled
  §4 direction) instead of returning dispatchable ahead of the interrupt
  the Stop already queued. A Stop before the first dispatch needs no
  capture: the cancel scan cancels the unattempted row outright, so the
  enqueue-time capture fences exactly the reachable interleavings. The
  fence covers both rollback branches (the submitting rollback and the
  blockedUnknown reopen); rows with earlier unresolved evidence keep
  their uncertain treatment, and the dispatcher arms no busy surface and
  no pacing for a fence-held row. Three refinements the same day's review
  forced: the Stop's own interrupt record captures the epoch its Stop just
  bumped (§4's "the interrupt passes no barrier" - a refusal of the
  interrupt itself redelivers instead of fence-canceling the Stop); an
  explicit release refreshes the captured epoch to the current one, so a
  Retry's own refused redeliveries are re-authorized past the Stop the
  fence held (§4's deliberate-post-Stop-send exemption, release barrier
  still fencing newer Stops); and the dispatcher's transient branch
  publishes its storage change like every other outcome, because the
  rollback can move the row (submitting, or canceled through the fence)
  and native's projections refresh through storage notifications. On
  native the capture also rides the row plumbing - COLUMNS, insertValues,
  fromRow - not just the returned record, or a fresh adapter over the
  same database reads every capture as zero. Three gaps the next review
  closed: a retargeted row rebases its capture to the destination ref's
  current counter inside the retarget's own transaction (the fence
  compares the destination's epochs, and the source ref's capture would
  authorize a same-number epoch past a destination Stop); a recovery
  resend is a deliberate send, so it captures the current epoch at its
  own enqueue like any enqueue does, and a historical Stop's fence does
  not cancel the resend the user just made (§4's exemption); and the
  native migration alters all three tables - the column rides the shared
  INSERT shape, so the instance_id loop precedent applies, not the
  outbox-only precedent of direct-SQL columns like the attempt count -
  or an upgraded optimistic or recovery write throws on the column the
  old schema never gained. The review after that closed the seam those
  rows still held: a record from before the capture field existed (on
  native, the migration column's DEFAULT 0) fence-canceled its first
  refusal against a Stop older than the row. The first mark now
  initializes the capture, inside the mark's own transaction, from the
  epoch the row's survival proves it was born at - any Stop since its
  birth would have scan-canceled it while unattempted - so the fence
  compares against the birth epoch; a modern row born at epoch zero is
  written back unchanged, the conditional read keeps every modern row's
  request sequence byte-identical (the note editor's drain), and a
  legacy pending interrupt redelivers on refusal instead of canceling
  its own Stop, the round-5 interrupt rule held for the legacy shape.
  The notification the round-6 fix added then closed its own loop: the
  refusal's storage change refreshes the ref's projection, the refresh
  re-marks the ref newly loaded, and the newly-loaded hand-off called
  the dispatch scheduler directly - an immediate redelivery per refusal
  (222 requests in ~350ms clean, 169 in ~370ms with residual evidence,
  both in the red tests), a far tighter hammer than the discovery
  cadence the two-clause construction rules. The first cut keyed the
  suppression on the busy marker, and the next review caught both of its
  blind spots: residual-evidence refusals arm no marker at all, and the
  marker outlives the timer whose fire it should track, stranding a
  parked resume-only send whose projection read completed while the
  marker stood. The final mechanism lives at the notification itself:
  the transient branch passes a cause naming whether the rollback
  changed anything a projection renders (a fence-held cancellation or a
  concurrent settlement did; a clean or residual rollback changed only
  attempt evidence, which no queue projection carries), and the
  browser's consumer skips its projection refresh for the unchanged
  shape - the marker-keyed filter is reverted whole. Native's consumer
  ignores the cause and re-reads on every notification, keeping the
  round-6 contract; the marker, its timer, and the paced ladder are
  untouched, and discovery and the timer remain the only redrivers.
  The review after that closed the classification's own gap and finished
  the gate: the rollback's `true` covers a blockedUnknown reopen exactly
  as it does the unchanged clean rollback (the reconcile parked the row
  while its request was pending, and the refusal's proof of non-delivery
  moves it back where every projection renders it), so the dispatcher
  reads the row's pre-rollback state to tell the shapes apart and
  reports the reopen as changed - a concurrent park in the
  read-to-rollback window misclassifies once and the next refresh
  self-heals, the same bounded cross-tab interleaving the attempt counts
  tolerate. And the reopen's refresh runs without its readiness hand-off
  redelivering the row immediately: the hand-off skips refs whose paced
  retry timer is live - that timer owns the re-drive, and its fire
  schedules the same dispatch the hand-off would - with the handle
  consumed at the timer's own fire so the gate never outlives what backs
  it (the round-10 medium's key, done right: the marker clears on
  settlement, the timer's lifetime is the map's). The fire carries one
  more duty the old hand-off silently doubled as: a dispatch request
  that joins a refusal's still-running dispatch dies with that run's
  stop, and the paced fire - not an immediate re-drain, which was the
  round-10 loop's shape - is what serves it.
- The busy surface's visible copy is pinned structurally, not verbatim
  (round-4 review, low): the queue row's reason span carries a
  data-busy-reason hook asserted non-empty, the ghost's caption stage and
  data-busy attribute are the assertions, and the refusal-message fixtures
  share one constant so no assertion matches a duplicated string.
- Item 8's TUI e2e uses a fixture-specific refusal message, so the pin proves
  the hub's message reaches the TUI's failure line verbatim rather than two
  copies of a known string agreeing, and asserts the refusal's ledger
  separately from accepted sends.
- Item 9 (teardown unchanged) is covered by the untouched teardown code plus
  the existing purge tests; the new refusal tests additionally pin that
  refusals answer strictly before the queue and never consume depth.
