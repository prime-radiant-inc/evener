# AppWire Retry-Safe Mutations and Atomic Rejoin Design

**Date:** 2026-07-28
**Status:** Approved direction; written specification awaiting Jesse's review

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
- Preserving mutation support against an older daemon that does not advertise
  this contract. Compatibility is discussed explicitly below.

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

The in-scope methods gain required `clientMutationId` when the target thread
advertises `retrySafeMutations`:

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
    "queueEntryIds": ["q_…"]
  }
}
```

`disposition` is:

- `applied` when this request durably committed the mutation; or
- `replayed` when the server returned the result of an earlier identical
  request.

`threadId` is always present. `turnId` and `queueEntryIds` are present only when
the method produces them.

Method-specific responses retain their existing fields:

- `turn/start` retains its reserved `turn`;
- `turn/cancelQueued` retains `removedText` and `removedImages`; and
- the new typed responses for steer, queue, drain, promote, and interrupt carry
  the common receipt.

The original method result is stored with the receipt so replay returns the
same turn ID, removed text, image count, and affected queue entry IDs.

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
- `turn/interrupt` requires `expectedTurnId`.
- `turn/promoteQueuedAsSteer` requires `expectedTurnId` and
  `expectedEntryId`.
- `turn/cancelQueued` requires `expectedEntryId`.
- `turn/drainAsSteer` requires `expectedTurnId` and
  `expectedQueueRevision`.

`QueueState` gains a monotonically increasing `revision`. Every enqueue, pop,
cancel, promote, drain, or restore changes it. A delayed drain must not consume
entries that arrived after the snapshot from which the user chose “drain.”

Precondition failure does not create a successful receipt because no mutation
was accepted. The response is a typed conflict carrying the current active
turn ID and/or queue revision needed to rehydrate the control.

## Durable Acceptance

### Per-session receipt state

The daemon owns a per-session client-mutation index. Each entry stores:

- client mutation ID;
- normalized method;
- semantic payload hash;
- method-specific result;
- the associated turn and queue entry IDs; and
- committed disposition.

Version one retains receipts for the lifetime of the session. It does not add a
time-based pruning policy: an expired idempotency window would reintroduce an
unsafe retry boundary. The receipt record is small relative to the transcript
entry generated by most mutations. Session deletion removes its receipts.

Receipts are co-located with the durable domain transition whenever possible:

- a started user turn stores its own client mutation ID and result identity;
- a queued input or pending steer stores its client mutation ID in the
  persisted queue state;
- an incorporated queue or steer carries that identity into its transcript
  turn; and
- queue transforms, cancellations, and interrupts retain compact receipt
  tombstones in the per-session mutation index.

The in-memory index is reconstructed from these records on resume. The
persisted queue state is expanded to retain the receipt tombstones it owns; an
empty queue no longer causes that state file to disappear while such receipts
remain. The design does not duplicate every successful turn receipt into the
queue file.

### Atomic mutation rule

The mutation effect and its receipt must become durable as one serialized
operation before the RPC returns success.

For queue and steering mutations, the daemon:

1. acquires the existing queue mutation serialization boundary;
2. validates the active turn, queue revision, and entry preconditions;
3. checks the receipt index;
4. computes the resulting queue/steering state and receipt;
5. atomically writes the combined state;
6. publishes the corresponding in-memory state; and
7. emits queue/steering notifications.

If persistence fails, the method returns an internal error and does not publish
the computed mutation or a success receipt.

Once the durable effect and receipt commit, the method must return success.
Notification enqueue or connection-write failure after that boundary cannot
turn the result into a JSON-RPC rejection; clients recover the missing
reflection through rejoin. This makes every explicit mutation rejection proof
that the mutation was not accepted.

For `turn/start`, the client mutation ID is persisted on the reserved user turn
before the response is sent or model work begins. Resume reconstructs the
receipt index from the persisted turn and returns the same reserved turn for a
duplicate start.

For `turn/interrupt`, the receipt is committed with the target turn's terminal
transition. A retry returns the original terminal result instead of reporting
that no turn is active.

Crash tests must cover every boundary around durable write, in-memory
publication, event emission, and response delivery. No boundary may produce
both a success receipt and a missing mutation after resume, or apply one client
mutation ID twice.

## Atomic Thread Rejoin

`thread/read(subscribe: true)` is the single recovery primitive for initial
mount, WebSocket reconnect, relay resync, long-silence self-heal, and page
reload.

The server-side ordering is:

1. resolve the authoritative source and thread;
2. register the connection's subscription;
3. begin buffering matching notifications for that connection;
4. capture the thread snapshot;
5. send the `thread/read` response; and
6. release buffered notifications in producer order, followed by live events.

Events may arrive before the response on the physical socket. The frontend
already has a hydration buffer and must keep buffering by hydration generation
until the matching response is installed.

The snapshot and buffered events may overlap. Reducers must therefore be
idempotent by stable turn, item, queue-entry, escalation, job, and mutation
identity. The contract is no loss, not exactly-once notification delivery.

`replaceSubscription: true` performs the swap in the same serialized operation.
There is no interval in which neither the old nor the new subscription owns
events. A late response or notification from a superseded connection or
hydration generation cannot publish over the current model.

The existing `serf/thread/resync` hint continues to request this operation
after a Hub-to-daemon relay recovers. A failed rejoin retries with reconnect
backoff while leaving the last model visible. It does not warn the user that
the view failed to update.

## Browser Recovery Outbox

An in-memory optimistic registry cannot survive the full reload that currently
repairs stale sessions. Before issuing an in-scope mutation, the WebUI writes a
recovery record to IndexedDB containing:

- client mutation ID;
- target ref and resolved thread ID when known;
- method and complete request payload, including attachment blobs;
- creation time for presentation only; and
- the optimistic display data.

This outbox is a recovery journal for user actions initiated while connected,
not an offline-send feature. The composer does not accept a new send while the
connection is unavailable.

If the outbox write fails, the request is not sent and the composer retains its
text and attachments. That is an explicit local persistence failure, not an
ambiguous server outcome.

An outbox record moves through:

1. `submitting`: locally recorded, RPC response not yet observed;
2. `accepted`: an applied or replayed receipt was observed;
3. `reflected`: for a message-producing operation, an authoritative snapshot
   or notification contains the same client mutation ID; and
4. removed after reflection, or immediately after an authoritative receipt for
   a receipt-only operation such as cancel or interrupt.

There is no elapsed-time transition to failed.

On reconnect or reload, the WebUI:

1. initializes the AppWire connection;
2. performs atomic `thread/read` rejoin for tracked outbox refs;
3. removes records already reflected in the snapshot;
4. resubmits every remaining record with its original client mutation ID; and
5. reconciles the replayed receipt and eventual state by identity.

Multiple tabs may replay the same record concurrently. Server idempotency makes
that safe; no browser lease or leader election is required for correctness.

Outbox records pin their target refs for recovery until removal. A ref need not
be visibly open in the restored workspace layout: startup scans the outbox and
hydrates those refs in the thread store before replay.

Explicit server rejection removes the outbox record only when the error
guarantees that no mutation was accepted. The original composer payload is
restored for user editing when the rejected method created user input.

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

## Compatibility

This is an end-to-end contract. A Hub cannot honestly advertise retry-safe
mutations for a thread whose daemon does not implement durable receipts.

Add `retrySafeMutations` to per-thread capabilities. The new WebUI uses the
outbox/retry path only when it is true.

Backward-compatible mutation forwarding to old daemons is intentionally not
part of this design. During a mixed-version deployment, legacy threads remain
readable and continue streaming, but the new WebUI does not expose mutation
controls until that session is resumed under a compatible daemon. It presents
one capability explanation in the disabled control:

> Restart this session to send with the current control protocol.

The implementation must not silently fall back to one-shot mutation semantics
or content-based reconciliation. Approving this specification approves this
explicit mixed-version behavior; any broader backward-compatibility shim
requires a separate design and Jesse's approval.

Codex-bridged and other read-only sources may leave the capability false. Their
existing source-specific controls remain outside this Serf daemon contract.

## Scope of the First Implementation

The first implementation includes:

- receipt types and per-thread capability;
- daemon receipt persistence and idempotency for the seven turn/queue methods;
- client mutation identity on queue, steering, and transcript projections;
- queue revisions and required preconditions;
- Hub forwarding without stripping receipt fields;
- atomic `thread/read` snapshot/subscription ordering;
- frontend IndexedDB outbox and identity-based reconciliation;
- reconnect, reload, resync, and silence-triggered automatic recovery;
- removal of confirmation timeout warnings; and
- generated TypeScript/AppWire documentation updates.

The following mutation families require the same ambiguity audit but are
separate specifications and implementation plans:

- `thread/start` at the Hub, `thread/fork`, `thread/clear`,
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
- concurrent identical retries serialize to one effect;
- same ID with a different method or payload is rejected;
- restart after acceptance still replays the original receipt;
- persistence failure publishes neither mutation nor success receipt;
- `turn/start` returns the same reserved turn after retry;
- steering may remain pending across arbitrary model/tool delay without
  changing receipt state;
- drain rejects stale active-turn or queue-revision preconditions without
  consuming new entries;
- promote and cancel never target a shifted queue entry;
- interrupt retry returns the original terminal result; and
- identical-text messages reconcile independently by identity.

Crash-boundary tests exercise failure immediately before and after durable
state write, in-memory publication, notification emission, and RPC response.

### Hub and AppWire

Script the real Hub relay/request paths to prove:

- receipt fields survive browser-to-Hub-to-daemon forwarding;
- `thread/read` subscribes before snapshot capture;
- an event produced before, during, and after snapshot capture is represented
  by the final client model;
- replacement subscription has no ownership gap;
- relay recovery emits resync and the subsequent rejoin converges;
- old daemon capabilities do not advertise retry-safe mutations; and
- a compatible resumed daemon restores mutation controls.

### Frontend

Use the real AppWire facade, threads store, pending/outbox store, reducer, and
Composer/QueueStrip components with fake transports and IndexedDB:

- disconnect before a mutation response, reconnect, retry the same ID, and
  reconcile without a warning;
- full reload restores text and attachment blobs from the outbox;
- hydration removes already-reflected records before replay;
- accepted-but-not-yet-reflected steering remains pending beyond the old
  timeout without failure;
- A-to-B client replacement rejects late A hydration and settlement;
- two same-text messages reconcile only their matching IDs;
- an explicit rejection restores editable input and clears its outbox record;
- local outbox failure leaves composer content untouched and sends no RPC; and
- mixed-version threads expose the documented disabled capability instead of
  legacy mutation behavior.

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
- A full page reload recovers unresolved text and attachments automatically.
- A steer can take arbitrarily long to reach a model boundary without any
  timeout-derived warning or failure.
- A rejoining client receives an authoritative snapshot plus every later
  notification with no gap.
- Content equality is never used to reconcile user mutations.
- Mixed-version behavior is explicit and cannot silently weaken the contract.
- The WebUI contains no “didn't confirm,” “view did not update,” or
  “reload before retrying” mutation warning.
