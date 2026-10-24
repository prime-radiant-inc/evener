# Client state cleanup design

Status: draft for review. Written after the one-owner Retry work in #4871 and the
outbox consolidation it depended on.

Related open work: #4816 (queued-message projection reads) and #4901 (native
lost-acceptance duplicate queue requests). #4788 (durable lost-reply settlement)
closed with the Retry work.

## Problem

Both clients now have one Retry owner, but the state around a submission still
has several overlapping owners:

- the composer owns the draft and its submission attempt;
- the pending-turns view owns a second copy of what is in flight;
- the session store owns canonical turns and a readiness flag;
- the mutation outbox owns the durable pending row and its recovery record;
- each screen's service/registry owns the live client used to send.

Each layer re-derives "is this submission allowed" and "is this session ready".
That is why the Retry work needed activation-time rechecks, a live-service
lookup, a pending-row guard, and a store-readiness gate. Those are correct
guards, but they exist because no single layer owns the answer.

The same shape produces the remaining symptoms: readiness flickers during
reconnect, a stale tab can settle another tab's row, and tests need to know
which of several projections is authoritative.

## Principles

1. **One durable record per submission.** The outbox row is the only authority
   for pending; the recovery row is the only authority for not-sent. Views
   derive from them and never keep a parallel queue.
2. **One projection per hub/session binding.** Canonical turns, pending rows,
   readiness, and the live client come from one registry entry. A screen
   borrows that entry; it does not create a second service.
3. **One submission gate.** Eligibility is a pure predicate over the projection
   and the clicked turn, returning allowed plus a reason. Components render the
   answer and do not repeat the checks at activation.
4. **One continuation intent.** Latest-failure selection and the `continue`
   input live in the shared client package, used by browser and native.
5. **Reads are demand-driven and coalesced.** One read actor per binding serves
   transcript, activity, and pending demand. A new demand joins the in-flight
   read or schedules one follow-up; it does not start a second reader.
6. **Drafts do not own submission state.** A draft survives acceptance,
   refusal, remount, and reconnect without becoming a second record of what is
   in flight.

## Target model

### Shared package

- Keep `turnContinuation` predicates (latest failed turn, one pending
  continuation, continuation input) in `appwire-client/typescript`.
- Export the projection shape the gate consumes: canonical latest turn, pending
  rows, readiness, and the live target identity.
- Keep the mutation record and recovery shapes here so both clients read the
  same fields.

### Browser

- `threads.ts` exposes one `SessionProjection` per hub/session. It combines the
  canonical store, the outbox projection, and the read state.
- `pendingTurnsStore` becomes a derived view over the outbox projection. Its
  separate in-memory queue and submission refs are removed once the projection
  covers every caller.
- `Composer` and the Retry action call the shared gate. The activation path
  still re-reads the live projection, but it no longer has a second local list
  of conditions.

### Native

- `nativeConversationSessions` remains the one registry entry per hub/session:
  store, mutation host, service, activity sink, read actor, refcounted attach.
- `NativeMutationRuntime.read` is the only projection source for pending and
  recovery; screens do not query storage directly.
- The screen renders `gate(projection, clickedTurn)` and submits through the
  entry's current service. A retired entry refuses without losing the draft.

## Migration

### Phase 1: inventory and delete dead paths

- Remove `useReadRetry` if no caller remains after #4816.
- List every place that constructs a pending queue or service, and mark its
  owner. Exit when the list has one owner per concern.

### Phase 2: derived pending views

- Replace the browser's separate pending list with a selector over the outbox
  projection.
- Replace native screen-local pending reads with the registry projection.
- Exit when both clients' pending counts come from the same record shape and
  the existing recovery tests pass unchanged.

### Phase 3: one gate and one read actor

- Move the remaining eligibility checks into the shared gate.
- Make transcript, activity, and pending demand share one read actor per
  binding, with explicit cancellation and a single follow-up.
- Exit when a reconnect or remount produces one read and one pending row, and
  the scoped-cancellation tests show no orphaned read.

### Phase 4: remove compatibility paths

- Delete dual-write and fallback branches that exist only for the old owners.
- Keep the persisted-row migration until both clients have shipped one release
  with the new shape.

## Non-goals

- Cross-device locking or leader election. The clients continue to rely on the
  durable outbox and the hub's idempotency.
- A new global state library. The cleanup uses the existing stores and the
  shared mutation package.
- Changing the Retry product rule: latest failed turn only, one pending
  continuation, input is `continue`.

## Test plan

- Real-hub journeys: lost reply, remount before settlement, two views, scoped
  cancellation, peer consumes a stale recovery, refcounted attach/release.
- Browser and native projection tests: pending count, readiness transitions,
  recovery ownership, draft preservation.
- Race tests: storage notification during a read, reconnect during a send,
  settlement during a remount.
- Exit criterion for every phase: the full affected journeys pass twice in a
  row, and no test needs to hold a transport frame to observe durable state.

## Open questions

- Does the browser outbox projection need a separate activity feed, or can the
  session projection serve both without increasing read demand?
- Which client should migrate first? Browser has the simpler recovery path;
  native has the stricter lifecycle. The phases above assume browser first.
- Can the persisted-row migration run in one release, or does it need a
  read-both/write-new window?
