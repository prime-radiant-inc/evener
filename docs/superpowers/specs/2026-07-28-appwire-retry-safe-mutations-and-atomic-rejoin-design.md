# AppWire Retry-Safe Mutations and Atomic Rejoin Design

**Date:** 2026-07-28
**Status:** Approved for implementation after two adversarial review and
revision rounds

## Problem

The WebUI currently treats several independent observations as though they were
one event:

1. the browser wrote a JSON-RPC request to its WebSocket;
2. the Hub or daemon accepted the requested mutation;
3. the mutation reached durable session state;
4. the agent incorporated the input at a model boundary; and
5. a notification reflected the mutation into this browser's model.

Those events can be separated by an arbitrarily long time. A model response,
tool call, approval, queue drain, daemon resume, or cellular network disruption
can intervene. In particular, steering is accepted before the agent can
incorporate it. A timer therefore cannot determine whether a message failed.

The current pending-confirmation implementation arms a ten-second timer after a
successful mutation response and reports:

> The server didn't confirm this message in time.

or:

> Steer was accepted, but this view didn't update.

Those messages expose a protocol ambiguity to the user. They also invite an
unsafe retry: the original request may already have mutated the session even
though its response or later notification was lost.

A WebSocket reconnect alone does not solve this. It restores a transport, but
it does not answer whether an unacknowledged mutation was applied. Conversely,
a targeted `thread/read` can repair the visible model but cannot safely infer
the identity of an accepted same-text message.

Cellular connections make these independent failures ordinary:

- the TCP connection can survive while an inner Hub-to-daemon relay is stale;
- a request can reach the server while its response is lost;
- notifications can be missed during a reconnect or page lifecycle change;
- a full reload destroys the browser's in-memory pending registry; and
- delivery to the model may legitimately take much longer than any UI timeout.

The protocol needs explicit acceptance identity and a no-gap snapshot-to-live
handoff. Timeouts remain useful for transport liveness, but never as evidence
that a domain mutation failed.

## Predecessor Designs

This design supersedes
`2026-07-27-pending-confirmation-after-rpc-design.md`. That design correctly
separated unresolved RPC time from post-response notification time, but its
post-response deadline still assigns semantic meaning to elapsed wall-clock
time. The warning and its `PendingConfirmationTimeoutError` are removed.

This design extends, rather than replaces,
`2026-07-27-relay-recovery-thread-resync-design.md`. A relay recovery hint
remains useful because the Hub knows that notification continuity broke.
Recovery now converges through the atomic rejoin contract defined here; it
never ends in an accepted-but-stale warning.

The design borrows four useful ideas from Codex App Server:

- the browser is not the source of truth for long-running work;
- `expectedTurnId` prevents a delayed steer from targeting a different turn;
- mutation acceptance is distinct from later agent incorporation; and
- a client-provided message identity is echoed on the eventual user item.

Serf adds the guarantee Codex's current open-source implementation does not
provide: the client identity is an idempotency key backed by durable acceptance,
not correlation metadata alone.

## Goals

- Make every WebUI turn/queue mutation safe to retry after response loss,
  reconnect, or full page reload.
- Return a typed receipt only after the daemon has durably accepted the
  mutation.
- Distinguish accepted input from input later incorporated by the agent.
- Correlate optimistic messages, queue entries, steering events, transcript
  items, and replayed snapshots by identity rather than text and timing.
- Make `thread/read(subscribe: true)` an ordered snapshot-to-live handoff with
  no notification gap.
- Make relay recovery, WebSocket reconnect, long-silence self-heal, and page
  reload converge through the same thread hydration path.
- Remove all user-facing accepted-but-stale, confirmation-timeout, and
  reload-before-retrying warnings.
- Keep default tests deterministic and below the provider boundary.

## Non-goals

- Offline composition or accepting new sends while disconnected.
- A general event journal, notification cursor, or exactly-once notification
  delivery.
- A protocol-wide generic transaction manager.
- Changing when the agent is able to consume steering.
- Making daemon-authored steering idempotent. This design covers explicit
  client mutations only.
- Adding retry-safe receipts to thread creation, forking, clearing, compaction,
  goals, auth, provider configuration, marketplaces, or plugins in this
  implementation.
- Preserving interoperability with older browsers, Hubs, or daemons. This is a
  flag-day protocol change.

## Protocol Laws

### Client-to-server mutations

A successful client mutation response means:

> The authoritative daemon durably owns this mutation and will not apply the
> same client mutation ID more than once.

It does not mean:

> The model has already seen this input.

If a response is lost, the client reconnects and resubmits the identical method
and normalized payload with the same client mutation ID. The server returns the
original result without applying the mutation again.

The same rule applies to state-dependent rejections. Once the daemon durably
rejects a well-formed mutation ID for a failed precondition, a retry returns
that rejection even if session state later changes. A delayed retry must never
turn a previously rejected intent into an applied one.

### Server-to-client streams

Every notification family must be one of:

1. **Reconstructible:** current state is present in `thread/read`.
2. **Replayable:** the client can request events after a stable cursor.
3. **Ephemeral:** loss cannot change the user's understanding of authoritative
   state.

Turn state, transcript items, user steering, the input queue, tasks, goals,
pending escalations, jobs, model, reasoning effort, title, and attention are
not ephemeral. The current value of each must be reconstructible from the
thread snapshot even if its live notification was missed.

Streaming deltas may remain ephemeral only when `thread/read` reconstructs the
complete accumulated in-progress item they update. A reconnect must never
require every historical delta to render the current text.

### Timeouts

Timeouts may:

- close a transport that failed its heartbeat;
- stop waiting for one JSON-RPC response;
- schedule reconnect and retry with the same mutation ID; or
- trigger a silent authoritative rehydrate after suspicious inactivity.

Timeouts may not:

- mark a mutation failed;
- remove an accepted optimistic item;
- advise the user to reload;
- generate a warning that the view did not update; or
- cause a retry with a newly generated mutation ID.

Only an explicit server rejection that guarantees no mutation was accepted may
produce a mutation-failed UI state.

## Wire Shape

### Client mutation identity

The in-scope methods gain required `clientMutationId`:

- `turn/start`
- `turn/steer`
- `turn/queue`
- `turn/drainAsSteer`
- `turn/promoteQueuedAsSteer`
- `turn/cancelQueued`
- `turn/interrupt`

The WebUI generates one opaque UUID with `crypto.randomUUID()` for each user
intent. JSON-RPC request IDs remain connection-local response correlation and
are never reused as mutation IDs.

The server normalizes the target to the authoritative session ID and hashes
the mutation method plus its normalized semantic parameters. `ref` versus
`threadId` aliases and fields irrelevant to behavior do not produce different
hashes.

For a previously seen `clientMutationId`:

- the same method and payload hash return the original receipt with
  `disposition: "replayed"`;
- a different method or payload returns `invalid request`; and
- the mutation is never executed a second time.

### Receipt

Each in-scope response carries:

```json
{
  "receipt": {
    "clientMutationId": "9e03…",
    "disposition": "applied",
    "threadId": "01…",
    "turnId": "turn_42",
    "queueEntryIds": ["q_…"],
    "projectionState": "reflected"
  }
}
```

`disposition` is:

- `applied` when this request durably committed the mutation; or
- `replayed` when the server returned the result of an earlier identical
  request.

`threadId` is always present. `turnId` and `queueEntryIds` are present only when
the method produces them.

Every receipt also carries the current authoritative projection state:

- `pending` when the accepted intent is durable but has not yet appeared in a
  queue, transcript item, or terminal operation state;
- `reflected` when authoritative state contains the mutation identity, even if
  that state is outside the current transcript window; or
- `removed` when a receipt-only mutation deliberately removed or terminated
  its target.

Projection state is current metadata computed when the receipt is returned; it
is not part of the immutable original method result.

### Mutation error outcome

Every error response to an in-scope mutation carries:

```json
{
  "serfErrorInfo": "conflict",
  "clientMutationId": "9e03…",
  "mutationOutcome": "notAccepted",
  "retryDisposition": "none"
}
```

`mutationOutcome` is:

- `notAccepted` only when the responder guarantees the mutation did not apply;
- `unknown` when a transport or persistence boundary cannot prove whether an
  authoritative daemon committed it; or
- `targetDeleted` when authoritative deletion irrevocably fenced the target,
  so replay must stop without claiming the mutation never applied.

`unknown` uses `serfErrorInfo: "mutationOutcomeUnknown"` and is never rendered
as a mutation failure. The outbox retains the original record and retries it
with the same ID. Terminal rejection replay returns the original error with
`mutationOutcome: "notAccepted"`.

`targetDeleted` is emitted only from an authoritative `deleting` or `deleted`
host record, never from a transient missing relay or stale index. The browser
moves the input to an orphaned recovery record associated with the deleted
target and never resubmits it automatically. The record offers copy/export or
explicit reuse in a new session without asserting whether the deleted session
incorporated it.

`retryDisposition` is `automatic`, `blocked`, or `none`. Transport loss uses
`automatic`. A daemon journal write failure uses `blocked` with
`serfErrorInfo: "mutationOutcomeUnknown"` and
`cause: "persistenceUnavailable"`; elapsed time never changes one disposition
into another. Terminal rejection and target deletion use `none`.

Method-specific responses retain their existing fields:

- `turn/start` retains its reserved `turn`;
- `turn/cancelQueued` retains `removedText` and `removedImages`; and
- the new typed responses for steer, queue, drain, promote, and interrupt carry
  the common receipt.

The original method result is stored with the receipt so replay returns the
same turn ID, removed text, image count, and affected queue entry IDs.

### Pending mutation projection

`SerfThread` gains `pendingMutations`. It contains every accepted or claimed
message-producing mutation that is not yet represented by a durable transcript
item, including its client mutation ID, method, complete input, execution
state, turn ID when assigned, and queue entry IDs. Queue entries also remain in
`QueueState`; clients deduplicate the two projections by mutation ID.

This projection makes accepted work reconstructible after the browser has
received a receipt and removed its transport outbox record. On reload, a
pending start or steer remains visible without depending on component memory.
Once the transcript contains the identity, the pending projection disappears
and the transcript item replaces it in place.

`projectionState: "reflected"` means the identity is present in
`pendingMutations`, `QueueState`, a transcript item, or the terminal operation
state. Receipt delivery alone does not claim reflection.

### Identity on authoritative state

`clientMutationId` also rides the domain object created by message-producing
mutations:

- user-message `ThreadItem`;
- human steering `ThreadItem`;
- `serf/steering/injected`;
- persisted user and steering turns; and
- each input-queue entry and its `QueueState` projection.

Two identical messages sent concurrently therefore remain two distinct
messages. Text matching is removed from optimistic reconciliation.

Queue entry IDs remain server-minted target identities. They answer “which
queued entry?” while `clientMutationId` answers “which client intent created or
changed it?” They are deliberately not conflated.

For a drain, the resulting combined steering item carries the drain operation's
client mutation ID and its receipt lists every drained queue entry ID. For a
promotion, the resulting steering item carries the promote operation's client
mutation ID and its receipt retains the source queue entry ID. This preserves
both the action that produced the visible steer and the queued input it moved.

### Preconditions

Idempotency prevents duplicate execution; preconditions prevent delayed
execution against the wrong state.

- `turn/steer` requires `expectedTurnId`.
- `turn/queue` requires `expectedTurnId`.
- `turn/interrupt` requires `expectedTurnId`.
- `turn/promoteQueuedAsSteer` requires `expectedTurnId` and
  `expectedEntryId`.
- `turn/cancelQueued` requires `expectedEntryId`.
- `turn/drainAsSteer` requires `expectedTurnId` and
  `expectedQueueRevision`.

`QueueState` gains a monotonically increasing `revision`. Every enqueue, pop,
cancel, promote, drain, or restore changes it. A delayed drain must not consume
entries that arrived after the snapshot from which the user chose “drain.”

Start and queue acceptance reserve one future user-turn budget slot in the
mutation journal under the lifecycle serializer. The acceptance check uses
durable completed/active turns plus existing reservations, so work cannot be
accepted and later rejected by `MaxTurns` before transcript incorporation.
Claiming the record converts its reservation into the session turn count.
Canceling a queued input or transforming it into steering releases the
reservation in the same journal commit.

Every other deterministic gate that can reject before the user item is appended
is evaluated before the mutation becomes applied. If a later execution failure
still prevents incorporation, the daemon writes a failed user item carrying the
same mutation and turn identities and marks the execution terminal; it never
leaves a successfully accepted record permanently runnable with no visible
outcome.

Precondition failure does not create a successful receipt because no mutation
was accepted. For a well-formed, authenticated mutation ID, the daemon
durably stores the typed conflict as that ID's terminal rejection before
responding. The conflict carries the current active turn ID and/or queue
revision needed to rehydrate the control. A retry with the same method and
payload replays the same rejection; a different payload remains invalid.

## Durable Acceptance

### Per-session receipt state

The daemon owns a per-session mutation journal. Each record stores:

- client mutation ID;
- normalized method;
- semantic payload hash;
- the complete canonical request payload and preconditions while the operation
  is nonterminal;
- method-specific result;
- stable turn and queue entry IDs allocated before the first reservation;
- reserved resources, including user-turn budget capacity;
- outcome: `applied` or a terminal typed rejection;
- operation state: `inFlight`, `applied`, `rejected`, or `terminal`;
- a monotonically increasing recovery-attempt generation;
- execution state for message-producing effects: `accepted`, `claimed`, or
  `terminal`; and
- projection state: `pending`, `reflected`, or `removed`.

Version two retains these records for the lifetime of the session. It does not
add a time-based pruning policy: an expired idempotency window would
reintroduce an unsafe retry boundary. Session deletion removes its journal.

Applied records are co-located with the durable domain transition:

- a started user turn stores its full input, client mutation ID, reserved turn
  ID, and runnable execution state;
- a queued input or pending steer remains in the journal until its transcript
  incorporation is durable;
- an incorporated queue or steer carries that identity into its transcript
  turn; and
- queue transforms, cancellations, and interrupts retain compact receipt
  tombstones.

The in-memory index and queue projection are reconstructed from the journal and
transcript on resume. An empty visible queue does not delete the journal.

Terminal precondition rejections are journaled too. Schema, authentication,
unknown-source, and unsupported-method failures that happen before a mutation
can be normalized to an authoritative session remain deterministic stateless
errors.

### Lookup order

Every in-scope daemon handler uses this order:

1. authenticate and resolve the authoritative session;
2. normalize the method and semantic payload, including every precondition;
3. look up `clientMutationId`;
4. for an existing ID, replay its applied result or terminal rejection when
   the payload hash matches, and reject a hash mismatch;
5. only for an unseen ID, validate current turn, queue revision, entry, turn
   budget, and dynamic availability;
6. durably reserve the ID, method, payload hash, complete canonical payload and
   preconditions, stable generated identities, reserved resources, and first
   recovery-attempt generation as `inFlight` before any side effect;
7. durably commit the applied effect or terminal precondition rejection; and
8. publish in-memory state and notifications for an applied effect.

Receipt lookup therefore precedes every check whose answer can change after
the first attempt. In particular, a completed turn cannot make a retry of its
successful start or interrupt fail.

Each daemon process has an in-memory owner token for an active recovery-attempt
generation. A concurrent same-ID retry joins only while that exact owner is
still active. Every handler exit that has not committed a terminal journal
state releases its owner in a deferred cleanup. A later explicit retry
reacquires the record under the lifecycle serializer, increments the attempt
generation, and deterministically recovers it in the same process. After
restart, all surviving `inFlight` records are unowned and eligible for the same
takeover. Recovery either completes the recorded side effect or derives its
terminal result from durable turn/journal state. It never needs request bytes
held only by the failed handler. A different method or payload cannot reuse an
in-flight ID.

The Hub performs syntax, authentication, source resolution, and static
source-method support checks. For a Serf daemon source it does not reject an
ID-bearing retry from a fresh thread snapshot's dynamic capabilities; it
forwards the request so the daemon can replay its journal before evaluating
current state.

### Atomic mutation rule

The mutation effect and its receipt must become durable as one serialized
operation before the RPC returns success.

For every in-scope mutation, the daemon:

1. acquires the per-session mutation/lifecycle serializer;
2. normalizes the payload and checks the mutation journal;
3. validates dynamic preconditions only for an unseen ID;
4. writes the complete `inFlight` reservation and claims its attempt
   generation;
5. computes and atomically writes the resulting domain state and receipt;
6. publishes the corresponding in-memory state; and
7. emits notifications.

The same serializer owns active-turn start and terminal transitions, queue
claims, queue revision, turn-budget reservations, and every
`expectedTurnId` compare-and-commit. AppWire handlers call this authoritative
session state machine; server-side processing/capability shadows are projections
and never decide a mutation. There is no unlock gap between validating turn A
and committing an effect against turn A.

If persistence fails, the method returns an internal error and does not publish
the computed mutation or a success receipt. Because no terminal record was
committed, that error carries `mutationOutcome: "unknown"` with
`retryDisposition: "blocked"`; it does not restore the composer payload as a
rejection or enter an automatic retry storm. If the initial reservation
committed, its complete recovery input remains durable and the failed handler
releases its live owner. An explicit retry after storage recovers can therefore
take over without restarting the daemon.

Once the durable effect and receipt commit, the daemon's semantic result is
success. Notification enqueue or connection-write failure after that boundary
cannot change it to a rejection. A lost daemon-to-Hub response is represented
upstream as `mutationOutcomeUnknown`, never as a rejection that claims the
mutation was not accepted. The browser retains the outbox record, reconnects,
and retries the same ID. A pre-forwarding transport failure may use the same
outcome because a harmless extra retry is safer than a false rejection.

Only a normalized validation error, a replayed terminal rejection, or a new
terminal rejection durably written before response may produce a
mutation-failed UI state.

`turn/interrupt` uses two short serialized phases rather than retaining the
serializer while waiting for the runner:

1. under the serializer, persist the complete `inFlight` interrupt record and
   an `interruptRequested` fence on the target execution;
2. release the serializer, signal cancellation, and wait for completion outside
   the lock; and
3. the runner, or recovery after a crash, reacquires the serializer and
   atomically commits the target's interrupted terminal state and finalizes the
   interrupt receipt.

No new incompatible lifecycle mutation can validate against an execution with
an `interruptRequested` fence. Same-ID retries join the active owner or recover
the fenced operation; they do not create another cancellation. A retry returns
the original terminal result instead of reporting that no turn is active.

### Durable incorporation

Acceptance and later incorporation are separate durable transitions. Removing
an accepted input from its visible queue is never the incorporation commit.

For start and queued user input:

1. start acceptance persists the full input and reserved turn identity; queue
   acceptance persists the full input and stable queue-entry identity;
2. the runner durably changes `accepted` to `claimed`, assigning a queued input
   its stable turn identity in the same write;
3. transcript append writes a user item carrying `clientMutationId` and the
   same turn identity;
4. the record remains runnable until that turn reaches a durable terminal
   state; and
5. terminal completion removes the stored input payload but retains the compact
   lifetime receipt.

On resume, an `accepted` record is runnable. A `claimed` record with no durable
terminal turn resumes under the same turn identity. Provider or tool work may
be re-entered after a process crash; exactly-once external side effects are not
promised, but the accepted user intent is never silently discarded and a
second logical turn is never created.

For pending steering:

1. acceptance persists the full steering input;
2. a boundary consumer marks it `claimed` without deleting it;
3. transcript append writes a steering item with the mutation identity; and
4. only after that append succeeds does the record become incorporated and
   release its stored payload.

Drain and promotion atomically mark their source queue records removed and
create the operation record that owns the resulting steering input. Cancel
atomically marks its target queue record removed and commits the cancel
operation receipt. A crash cannot expose both the original queue projection and
the transformed steering projection, or neither.

Recovery scans transcript mutation identities. A claimed record already present
in the transcript is finalized without a second append; a claimed record absent
from the transcript returns to runnable state. Queue projections show accepted
but unclaimed entries and omit claimed entries, while the durable journal owns
both.

Crash tests cover every boundary around journal write, claim, transcript
append, terminal turn write, in-memory publication, event emission, and RPC
response. No boundary may leave a success receipt with neither runnable nor
incorporated intent after resume, apply one ID twice, or create two logical turn
identities.

## Atomic Thread Rejoin

`thread/read(subscribe: true)` is the single recovery primitive for initial
mount, WebSocket reconnect, relay resync, long-silence self-heal, and page
reload.

At the daemon AppWire boundary, each authoritative source has one
projection-commit boundary. Applying a durable state change to the in-memory
projector, allocating its internal source sequence, and inserting its
notification into every subscriber buffer happen under that one boundary.
Notifications are emitted only after their durable state is projectable.
Snapshot capture clones the same projector and records its cut while holding
that boundary. Internal sequence numbers are not exposed as a public replay
cursor.

The daemon-side ordering is:

1. resolve the authoritative source and thread;
2. under the projection-commit boundary, register the connection's
   subscription and begin buffering notifications with their internal source
   sequence;
3. under that same boundary, clone the authoritative thread projector and
   record its source-sequence cut;
4. discard buffered notifications at or before the cut because the snapshot
   already contains them;
5. queue the `thread/read` response before any post-cut notification on that
   AppWire connection; and
6. release buffered notifications after the cut in producer order, followed by
   live events.

This response-before-notification ordering is part of the appserver atomic
capture contract. A client still associates hydration with its generation so a
late response or notification from a superseded generation cannot publish over
the current model.

The snapshot and released buffer do not semantically overlap. This is
load-bearing for append-only agent-message, reasoning, and tool-output deltas:
stable item identity cannot make the same text delta idempotent. Full-state and
terminal reducers remain idempotent by stable thread-scoped turn, item,
queue-entry, escalation, job, and mutation identity.

The Hub does not create a second upstream subscription to reproduce this cut.
For each source/thread pair, the source layer owns exactly one canonical
`RelaySession` actor and exactly one canonical upstream AppWire notification
stream. The actor is deliberately narrow rather than a generic actor
framework. It owns:

- the canonical AppWire connection and its ordered notification feed;
- connection recovery and monotonically increasing connection epochs;
- serialized `thread/read(subscribe: true)` snapshot commands;
- buffering while a downstream hydration capture is installed; and
- idle shutdown and removal when no downstream relay or command still owns it.

The Hub relay is downstream fanout only. It never opens, merges, or
deduplicates an additional upstream stream. On a healthy rejoin, the
`RelaySession` issues `thread/read(subscribe: true)` on its existing canonical
connection. The actor buffers notifications from that same canonical feed
while the snapshot command is in flight and returns the materialized snapshot
with a one-shot, epoch-scoped, two-phase downstream hydration handoff token.
The token is bound to the source/thread, connection epoch, and serialized
snapshot command generation. While it is pending, the actor continues to hold
that canonical feed using only its own per-thread state; it does not retain a
global, Hub, relay-map, deletion, appserver projection, or appserver delivery
lock.

The Hub completes all upstream network snapshot work before beginning the
downstream appserver capture. Immediately before that capture, the Hub calls
`Prepare` on the handoff token. `Prepare` atomically validates the token's
connection epoch and snapshot-command generation and logically pins that actor
state until the downstream response outcome. It retains no mutex or other lock
across capture installation or response enqueue. If `Prepare` cannot establish
that pin, the Hub aborts the token and fails the read before installing a
capture or returning a successful response.

The Hub then installs the Task 6 `CaptureSubscription` using only the
already-materialized actor snapshot; the capture's snapshot callback performs
no source or other network I/O while the appserver projection and delivery
locks are held. The downstream response enqueue is the handoff decision:

- after the matching response successfully enters the downstream connection's
  send queue, appserver commits its hydration capture and invokes the actor
  token's `Commit` exactly once; the actor releases only post-cut
  notifications in producer order and resumes direct downstream fanout;
- if capture installation fails, response enqueue fails, the downstream
  request or connection is canceled, or a newer hydration supersedes it,
  appserver first withdraws or invalidates that failed hydration capture and
  then invokes the token's `Abort` exactly once; the actor resumes the canonical
  feed for remaining downstream owners without publishing any held
  notification into the failed hydration.

`Commit` and `Abort` are idempotent and race-safe terminal operations on the
same token: one pending-to-terminal transition wins, repeated or losing calls
are no-ops, and the canonical feed is resumed or released exactly once. A token
from a stale connection epoch or superseded snapshot command cannot publish.
If the canonical transport disconnects after `Prepare`, the actor records the
disconnect and closes the transport but defers token invalidation and the epoch
transition until `Commit` or `Abort` resolves the pinned handoff. Resolution
then applies the deferred transition and starts canonical recovery when
downstream listeners remain. This pin is per actor and logical only; it cannot
block another source/thread actor.
The one-shot appserver response finalizer is the outcome linearization point:
a completed response enqueue selects commit, while failure, cancellation, or
supersession that prevents enqueue selects abort; a later competing signal
cannot reverse that decision. Epoch recovery owns continuation after a stale
token is invalidated. This two-phase handoff preserves the Task 6 appserver cut
rather than attempting to infer one from two independently ordered streams.

Task 7 may add only the narrow internal appserver response-finalization seam
needed for this handoff: a `CaptureSubscription` companion or extension can
register one success-after-response-enqueue callback and one
failure/cancellation/supersession callback, while the existing
`CaptureSubscription` behavior remains available to its current callers.
Response enqueue, connection unregister, and capture supersession resolve that
one-shot finalizer after committing or withdrawing the matching hydration
generation. This is not a reusable transaction API or generic actor framework.

If the actor is disconnected or recovering, it establishes a new atomic
AppWire connection, initializes it, and performs
`thread/read(subscribe: true)` there. That connection becomes canonical only as
one epoch transition with a successful snapshot and a live continuation. A
snapshot without a continuing subscribed connection is not a successful
rejoin. The previous epoch is revoked before the new upstream stream may
publish; any late read, response, or notification from a stale epoch is
dropped at the actor boundary.

There is never an established normal stream plus a temporary atomic stream,
never overlapping upstream subscriptions for one source/thread, and no
sequence-, identity-, or content-based merge/deduplication between streams.
Concurrent same-thread reads are serialized by the actor. Cancellation at any
point must leave the canonical feed publishable and must either complete or
withdraw the downstream hydration capture without stranding buffered
notifications. Actor shutdown closes its canonical connection only after the
last downstream owner and in-flight command are gone.

The actor may use its own per-thread state while performing network I/O. It
must never hold the global Hub lock, relay-map lock, Task 8
deletion/ownership lock, or another unrelated/global lock across connection,
initialize, snapshot, recovery, or shutdown I/O. Independent thread actors
must continue to accept notifications and complete snapshots while one actor's
network operation is blocked. Target acquisition and publication still obey
the irrevocable Task 8 deletion fence.

Every v2 thread notification carries authoritative `threadId` and `ref`,
including `turn/completed`. Turn and item identities are scoped to that thread;
a multiplexed client never routes by matching an unqualified turn ID.

The contract is no loss and no duplicate append-only delta across the snapshot
cut. It is not a general exactly-once notification or public event-journal
contract.

`replaceSubscription: true` performs the swap in the same serialized operation.
There is no interval in which neither the old nor the new subscription owns
events. At the Hub, this replaces downstream relay ownership only; it does not
replace the actor's canonical upstream stream. A late response or notification
from a superseded connection epoch or hydration generation cannot publish over
the current model.

The existing `serf/thread/resync` hint continues to request this operation
after a Hub-to-daemon relay recovers. A failed rejoin retries with reconnect
backoff while leaving the last model visible. It does not warn the user that
the view failed to update.

An adapter that cannot establish an atomic snapshot cut must synthesize
full-state replacement notifications or disable live streaming; read-only
mutation capability has no bearing on delta correctness. It may not release
overlapping append-only deltas.

## Browser Recovery Outbox

An in-memory optimistic registry cannot survive the full reload that currently
repairs stale sessions. Before issuing an in-scope mutation, the WebUI writes a
recovery record to IndexedDB containing:

- client mutation ID;
- target ref and resolved thread ID when known;
- method and complete request payload, including attachment blobs;
- a monotonically increasing per-target intent sequence allocated by the shared
  IndexedDB transaction;
- creation time for presentation only; and
- the optimistic display data.

This outbox is a recovery journal for user actions initiated while connected,
not an offline-send feature. The composer does not accept a new send while the
connection is unavailable.

If the outbox write fails, the request is not sent and the composer retains its
text and attachments. That is an explicit local persistence failure, not an
ambiguous server outcome.

Composer submission and network settlement are separate APIs. `enqueueIntent`
resolves as soon as the IndexedDB transaction commits. At that point the
composer clears only the text and attachments that still match the submitted
payload; edits made while the transaction was pending remain. An asynchronous
dispatcher owns RPC attempts and settlement. A lost or failed response never
restores the submitted payload into the main composer because the durable
outbox or recovery tray already owns it. Clicking Send again therefore cannot
create a second mutation merely because the first response was lost.

An outbox record moves through:

1. `submitting`: locally recorded, no authoritative outcome observed;
2. removed after an applied or replayed receipt;
3. retained as `submitting` after a transient `mutationOutcomeUnknown`;
4. `blockedUnknown` after a classified persistent storage failure;
5. moved to orphaned recovery after `targetDeleted`; or
6. atomically transferred to the durable recovery draft after a terminal
   rejection.

There is no elapsed-time transition to failed.

Every dispatch outcome is applied in a conditional IndexedDB read-modify-write
transaction. An `unknown` result may update only the matching extant outbox
record; it never recreates a missing, transferred, or settled record. An
applied or replayed receipt deletes the matching outbox record and any
same-mutation recovery record, regardless of which tab created that recovery
record. Authoritative applied/reflected settlement dominates a late unknown or
local recovery classification. These monotonic rules make reversed responses
from concurrent tabs converge rather than resurrecting work.

The outbox is strictly a transport ambiguity journal. It is not the
post-acceptance optimistic-rendering registry. A durable applied/replayed
receipt is sufficient to remove it because the daemon now owns both the intent
and its recovery state. The receipt's current projection state lets the client
settle an old mutation even when its transcript item is outside the latest
turn window.

All new and recovery submissions pass through the same per-target outbox
dispatcher. A newly created sequence does not bypass unresolved lower
sequences.

After committing an outbox record, a tab broadcasts its target through
`BroadcastChannel`. Every ready tab rescans that target. Startup, connection
ready, online, focus, and visibility transitions rescan all targets, and a
two-second ready-state liveness scan covers an origin tab that crashes between
its IndexedDB commit and broadcast. This timer only discovers durable work; it
never changes a mutation outcome or declares failure.

On reconnect or reload, the WebUI:

1. initializes the AppWire connection;
2. performs atomic `thread/read` rejoin for tracked outbox refs;
3. removes records whose mutation identity is reflected in the snapshot;
4. for each target, resubmits remaining records serially by intent sequence,
   awaiting an authoritative receipt or terminal rejection before advancing;
   and
5. reconciles optimistic state and eventual notifications by identity.

Multiple tabs may replay the same record concurrently. Every tab observes the
same IndexedDB sequence order and waits at the same record boundaries. Server
idempotency makes duplicate submissions safe, so no browser lease or leader
election is required for correctness. Before sending each loaded record, a tab
rechecks that it still exists; another tab may already have settled it.

Outbox records pin their target refs for recovery until removal. A ref need not
be visibly open in the restored workspace layout: startup scans the outbox and
hydrates those refs in the thread store before replay.

Explicit server rejection settles the outbox only when the error carries
`mutationOutcome: "notAccepted"`. In one IndexedDB transaction, the browser
moves the original text and attachment blobs into a durable recovery-draft
record keyed by ref, intent sequence, and client mutation ID, then deletes the
outbox record. Exactly one tab can consume that outbox record. A composer loads
and acknowledges recovery drafts in intent order; later rejected inputs remain
durable rather than overwriting the first. Component-local state is never the
sole restored copy. Crashing or closing a tab at any boundary leaves either
the outbox record or its recovery-draft record durable.

Authoritative session or project deletion is a host-level state machine stored
outside the target's deletable directory and guarded by the same ownership
boundary as target resolution and resume. Its first durable transition to
`deleting` is the irrevocable semantic deletion commit. From that point every
resolve, resume, launch, relay, and mutation path rejects the target as
`targetDeleted`; no path may reopen the old session or journal.

Cleanup of the session, journal, rendezvous state, and project files is
idempotent and retried after a failure or Hub restart. Only successful governed
cleanup advances the host record from `deleting` to `deleted`; cleanup failure
never rolls the target back to live after clients have observed deletion. The
record contains only stable ref/thread identity and deletion generation and is
retained for the lifetime of Hub state. In one IndexedDB transaction the
browser moves the outbox record to orphaned recovery and deletes the
dispatchable record. This interaction is in scope even though making deletion
itself a retry-safe client mutation is a separate design.

Recovery drafts render in a separate recovery tray rather than merging
automatically into the current composer. Editing a recovered entry is backed
by that durable record and leaves the main composer untouched. Sending it
uses one conditional IndexedDB transaction: load and claim the recovery record,
abort if another tab already claimed or consumed it, allocate the next target
sequence and new mutation ID, create exactly one outbox record, then delete the
recovery record. The winning transaction commits the new identity; losing tabs
do not send. Attachment presentation IDs are re-minted while the original blobs
remain durable through the transfer.

`blockedUnknown` is entered from an explicit persistence-unavailable outcome,
not elapsed time. It keeps the payload durable, blocks later sequences for that
target, stops automatic retry storms, and presents actions to retry, copy or
export the payload while explaining that the server could have accepted it.
There is no browser-local abandon operation: later sequences remain blocked
until the same mutation ID receives an authoritative outcome or target deletion
irrevocably fences the session. The user may copy or export the payload for use
in a different session, but that does not unblock or reorder the original
target.

## UI Behavior

- A new mutation renders optimistically immediately after its outbox record is
  durable.
- Receipt acceptance may change subtle pending styling but does not remove the
  optimistic item for a message-producing mutation.
- Authoritative reflection replaces the optimistic item in place by client
  mutation ID.
- Receipt-only controls settle from their receipt without inventing a
  transcript echo.
- A long-running steer remains accepted and pending for as long as necessary.
- Transport loss shows only connection state. It does not mark individual
  messages failed.
- Reconnect and rejoin happen automatically.
- No toast, transcript line, or queue warning says that an accepted mutation
  failed to update the view or asks the user to reload.
- Genuine validation, authorization, conflict, and persistence errors retain
  normal actionable error presentation.

The `PendingConfirmationTimeoutError`, `PENDING_TIMEOUT_MS` confirmation
behavior, and their Composer/QueueStrip warning branches are removed. Heartbeat
and request deadlines remain transport mechanisms.

## Flag-Day Cutover

This is an end-to-end protocol replacement, not a negotiated feature. The
browser, Hub, and Serf daemon ship as one release unit. There is no
`retrySafeMutations` capability, no legacy request shape, no mutation fallback,
and no mixed-version UI.

Set `appwire.ProtocolVersion` to `serf-appwire-v2` and add required
`protocolVersion` to the Serf `InitializeParams`. The Serf browser, Hub, and
Serf daemon send that exact version, each Serf server rejects a mismatch before
accepting any other request, and each Serf client verifies the response version
before sending `initialized`. The Hub likewise ignores or rejects rendezvous
entries from a Serf daemon with a different protocol version and never relays
to them.

A protocol mismatch is a terminal deployment error for that connection, not a
per-thread disabled control and not a reconnectable transport failure. The
client must not enter an automatic reconnect loop against a known-incompatible
peer.

Deployment replaces the Hub and WebUI assets together and restarts active Serf
daemons under the new binary. Persisted sessions remain resumable as data, but
an old daemon process cannot remain attached to a new Hub and an old browser
asset cannot operate a new Hub. Active pages running old assets must load the
new application as part of the cutover.

The generic JSON-RPC transport and codec may still be shared with source
adapters, but Serf v2 initialization is not sent to an upstream Codex App
Server or any other adapter-native protocol. Those adapters retain their native
handshake.

Existing source-specific method capabilities still describe whether a source
supports operations such as start, steer, or queue. They are not protocol
version negotiation. A source adapter that exposes an in-scope mutation must
provide the same durable receipt and retry contract. The current Codex bridge
cannot do that because its client message ID is correlation metadata rather
than durable idempotency. In this implementation its send, steer, interrupt,
queue, and queue-transform capabilities are all false.

Codex read/list behavior remains available. Its current separate snapshot and
live connections cannot establish a shared upstream cut, so the adapter does
not forward raw append-only deltas. Each upstream change marks the thread
dirty; a single-flight loop re-reads authoritative full state and emits a
qualified thread resync/full replacement through the source projector. Dirty
state is cleared only after a successful full read commits the replacement. If
another change arrives during the read, the loop reads again before becoming
clean. If a read fails, dirty remains set and the loop retries indefinitely
with capped reconnect backoff, forcing upstream source reconnect/resync when
the connection itself is suspect. A final upstream event followed by one
failed read therefore still converges without needing another event. Reconnect
performs an unconditional full read. The committed replacement is the cached
authoritative value returned by later `thread/read`; it is not an overlapping
raw-delta stream. This preserves live convergence without claiming delta-level
atomicity the upstream protocol does not provide.

`thread/clear` replaces the session identity underneath a stable workspace ref.
That replacement is unsafe while any old-identity browser outbox record or
daemon operation can still settle. The first v2 implementation therefore sets
the Serf clear capability false and rejects direct `thread/clear` calls as
unsupported. Restoring clear requires its separate design to durably fence the
old identity and return an authoritative disposition for every unresolved
mutation; a browser-local empty-outbox check is insufficient across tabs.

Restoring Codex mutations requires a separate upstream-supported idempotency
design; the Hub must not emulate certainty across an ambiguous upstream call.

## Scope of the First Implementation

The first implementation includes:

- the AppWire protocol-version bump and exact initialize/rendezvous validation;
- receipt types;
- daemon mutation-journal persistence, rejection replay, and idempotency for
  the seven turn/queue methods;
- durable in-flight operation reservation and lifecycle serialization;
- durable start/queue/steer claim and incorporation recovery;
- user-turn budget reservation and failed-incorporation projection;
- client mutation identity on queue, steering, and transcript projections;
- queue revisions and required preconditions;
- Hub forwarding without dynamic pre-rejection or stripping receipt fields;
- qualified v2 notifications and atomic `thread/read` snapshot-cut ordering;
- frontend ordered IndexedDB outbox, cross-tab wakeup, blocked-unknown and
  deleted-target recovery, durable recovery drafts, and identity-based
  reconciliation;
- reconnect, reload, resync, and silence-triggered automatic recovery;
- Codex mutation-capability removal and full-state streaming convergence;
- Serf `thread/clear` capability removal until retry-safe replacement is
  specified;
- removal of confirmation timeout warnings; and
- generated TypeScript/AppWire documentation updates.

The following mutation families require the same ambiguity audit but are
separate specifications and implementation plans:

- `thread/start` at the Hub, `thread/fork`, retry-safe `thread/clear`,
  `thread/compact/start`, and `thread/shutdown`;
- `goal/set`;
- sandbox escalation resolution;
- model, reasoning-effort, and name setters;
- auth completion and credential writes;
- provider-instance and launch-config mutations; and
- marketplace/plugin install, remove, refresh, and upgrade operations.

The audit classifies each as message identity, operation receipt, naturally
idempotent setter with an expected revision, or read/refetch-only. It must not
default every RPC into a generic ledger.

## Testing

Before production changes, add a real RED regression at the AppWire boundary:
the daemon applies a queued message, the transport drops its response, the
client reconnects and retries the same ID, and the session contains exactly one
queue/transcript message. A compile failure does not count as RED.

### Daemon

Use real session and persistence paths with deterministic transport/storage
seams to prove:

- response loss followed by same-ID retry produces one effect and a replayed
  receipt for each in-scope method;
- concurrent or restarted retries of an `inFlight` record join or recover one
  operation and reject payload-hash reuse;
- a crash immediately after `inFlight` reservation recovers complete text,
  attachments, preconditions, stable identities, and reserved resources;
- reservation success followed by effect-write failure releases the live owner,
  and an explicit same-process retry takes over successfully without restart;
- a same-ID retry replays its applied result or terminal rejection before
  current dynamic preconditions are evaluated;
- concurrent identical retries serialize to one effect;
- same ID with a different method or payload is rejected;
- restart after acceptance still replays the original receipt;
- `thread/read` projects accepted and claimed inputs until their transcript
  identity is durable;
- persistence failure publishes neither mutation nor success receipt;
- `turn/start` returns the same reserved turn after retry;
- accepted and claimed start records resume the same logical turn after crash;
- interrupt persists its in-flight claim before cancellation and remains
  terminal after restart;
- interrupt releases the serializer while awaiting runner cancellation, its
  fenced terminal transition completes, and incompatible mutations cannot pass
  through the gap;
- start and queue reserve `MaxTurns` capacity at acceptance, including the
  final available slot and queued cancellation/steering release paths;
- a deterministic pre-append failure produces one failed item with the original
  mutation and turn identities;
- steering may remain pending across arbitrary model/tool delay without
  changing receipt state;
- a crash between claim and transcript append loses no queue or steering input,
  and a crash after transcript append duplicates no item;
- recovery recognizes an already-incorporated transcript mutation identity;
- drain rejects stale active-turn or queue-revision preconditions without
  consuming new entries;
- queue rejects a stale active-turn precondition;
- deterministic barriers between expected-turn validation and lifecycle
  transition cannot attach queue, steer, promote, drain, or interrupt to a
  later turn;
- promote and cancel never target a shifted queue entry;
- interrupt retry returns the original terminal result; and
- identical-text messages reconcile independently by identity.

Crash-boundary tests exercise failure immediately before and after durable
state write, in-memory publication, notification emission, and RPC response.

### Hub and AppWire

Script the real Hub relay/request paths to prove:

- receipt fields survive browser-to-Hub-to-daemon forwarding;
- a current capability change does not block replay of a previously applied or
  rejected mutation;
- a dropped daemon response becomes `mutationOutcomeUnknown`, never a
  not-accepted rejection;
- the browser sends `serf-appwire-v2` and rejects a different initialize
  response version;
- the Hub rejects an initialize request with a missing or different protocol
  version;
- the Hub rejects a mismatched daemon rendezvous entry or initialize handshake
  before relay attachment;
- Codex uses its adapter-native initialize shape and exposes no send, steer,
  interrupt, queue, or queue-transform capability;
- Codex suppresses raw deltas and repeats full-state reads when an upstream
  change races its current read;
- a failed Codex full-state read after the final upstream event stays dirty and
  retries to convergence without another event;
- every resolve, resume, launch, relay, and mutation path rejects a
  host-authoritative `deleting` target, and interrupted deletion cleanup resumes
  idempotently after each fallible step;
- Serf exposes no clear capability and rejects direct `thread/clear` calls;
- `thread/read` subscribes before snapshot capture;
- append-only deltas at or before the snapshot cut are discarded from the
  buffer, while deltas after the cut are delivered once;
- Hub upstream snapshot I/O completes before downstream appserver capture, and
  the capture installs an already-materialized snapshot without source I/O
  under appserver projection or delivery locks;
- response enqueue success commits an epoch-scoped actor handoff once, while
  response enqueue failure or downstream cancellation after capture withdraws
  that hydration and aborts the handoff once without stranding the canonical
  feed;
- a deterministic commit-versus-abort race has exactly one terminal winner,
  repeated terminal calls do not duplicate release or resume, and a stale
  epoch token cannot publish;
- pausing before projector mutation, between projector mutation and sequence
  allocation, and before buffer insertion cannot create a snapshot/event gap or
  overlap;
- every notification, including `turn/completed`, routes by authoritative ref
  and thread ID;
- replacement subscription has no ownership gap;
- relay recovery emits resync and the subsequent rejoin converges.

### Frontend

Use the real AppWire facade, threads store, pending/outbox store, reducer, and
Composer/QueueStrip components with fake transports and IndexedDB:

- disconnect before a mutation response, reconnect, retry the same ID, and
  reconcile without a warning;
- a lost response leaves one durable mutation while the composer clears after
  local enqueue commit; a second click cannot duplicate the unchanged payload;
- full reload restores text and attachment blobs from the outbox;
- full reload reconstructs receipt-settled but unincorporated input from
  `pendingMutations`;
- hydration removes already-reflected records before replay;
- a replayed receipt settles an accepted record whose transcript item is older
  than the hydration window;
- per-target replay preserves IndexedDB intent sequence under reversed network
  scheduling and concurrent tabs;
- reversed applied/unknown outcomes from two tabs converge monotonically,
  never recreate a settled outbox record, and remove same-mutation recovery
  records;
- another ready tab discovers a committed record when the origin crashes before
  broadcast;
- accepted-but-not-yet-reflected steering remains pending beyond the old
  timeout without failure;
- A-to-B client replacement rejects late A hydration and settlement;
- two same-text messages reconcile only their matching IDs;
- an explicit rejection restores editable input and clears its outbox record;
- a crash or tab handoff during rejection restoration leaves the payload in
  either the outbox or the durable recovery draft;
- recovered attachment input remains independently editable without replacing
  a nonempty main composer;
- simultaneous recovery-tray resend in two tabs conditionally consumes one
  record and creates exactly one new mutation;
- `targetDeleted` moves unresolved input to orphaned recovery without automatic
  resend;
- a persistence-classified `blockedUnknown` stops retry storms and later
  dispatch while preserving retry/copy/export actions, and no local action
  advances the blocked target without authority;
- local outbox failure leaves composer content untouched and sends no RPC; and
- the frontend never branches between legacy and retry-safe mutation paths.

Tests use fake clocks only for heartbeat/backoff scheduling. No assertion uses
elapsed time to decide whether a mutation succeeded.

### Verification

Run focused Go and frontend tests first, then the repository-required build,
test, lint, typecheck, and generated-artifact drift gates. Default verification
must not use provider credentials, network access, quota, or live model
behavior.

## Acceptance Criteria

- A response can be dropped after any in-scope mutation without duplicating or
  losing the user intent on retry.
- Daemon restart after a success receipt preserves the mutation and receipt.
- Daemon restart after acceptance preserves a runnable or incorporated user
  intent under the same logical turn identity.
- A live handler that exits after reserving an operation cannot strand it;
  same-ID retry can take over from complete durable input without daemon
  restart.
- A full page reload recovers unresolved text and attachments automatically.
- A steer can take arbitrarily long to reach a model boundary without any
  timeout-derived warning or failure.
- A rejoining client receives an authoritative snapshot plus every later
  notification with no gap.
- Append-only deltas are neither lost nor duplicated across the snapshot cut.
- Concurrent threads cannot consume one another's notifications.
- Recovery replay preserves per-target user-intent order.
- A browser-local unknown outcome or recovery action can never advance later
  mutations ahead of a possibly accepted earlier mutation.
- A committed browser outbox record is eventually discovered even if its
  originating tab dies immediately.
- Permanent authoritative persistence failure preserves user input and blocks
  later target mutations without converting uncertainty into failure.
- Authoritative target deletion converges unresolved outbox records into
  orphaned recovery rather than retrying or claiming non-acceptance.
- Once deletion is committed, no resolve, resume, launch, or relay path can
  revive the target, and cleanup resumes until complete.
- Content equality is never used to reconcile user mutations.
- Browser, Hub, and daemon protocol mismatches fail before any mutation can be
  submitted.
- Session clear is unavailable until replacement has its own authoritative
  unresolved-mutation disposition.
- No compatibility path can silently weaken the receipt contract.
- The WebUI contains no “didn't confirm,” “view did not update,” or
  “reload before retrying” mutation warning.
