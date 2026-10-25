# Client state cleanup design

Status: draft for review. Written after the one-owner Retry work in #4871 and the
outbox consolidation it depended on. Corrected after the phase-1 inventory.

Related open work: #4816 (queued-message projection reads) and #4901 (native
test counts raw queue attempts from a leaked discovery interval; the durable
record identity is unchanged). #4788 (durable lost-reply settlement) closed with
the Retry work.

## Problem

Both clients now have one Retry owner, and `pendingTurnsStore` is already a
derived projection over the durable outbox, not a second queue. What still has
several owners is the in-flight and read bookkeeping around a submission:

- the composer owns the draft and its submission attempt;
- the pending-turns projection owns the durable rows plus three facts that are
  not derivable from them: the click-to-commit marker, provenance that outlives
  settlement, and "unread" versus "read and empty";
- the session store owns canonical turns, a readiness flag, and several more
  in-flight markers (`retryActivationsInFlight`, `inflightDurableEnqueues`,
  `undeliveredMutationIds`);
- native keeps a second in-flight record in `ConversationMutationState` and in
  the draft's `unconfirmed` field;
- the mutation outbox owns the durable pending row and its recovery record;
- transcript, pending-projection, activity, and list reads each coalesce
  separately for the same binding.

Each layer re-derives "is this submission allowed" and "is this session ready".
That is why the Retry work needed activation-time rechecks, a live-service
lookup, a pending-row guard, and a store-readiness gate. Those are correct
guards, but they exist because no single layer owns the answer.

The same shape produces the remaining symptoms: readiness flickers during
reconnect, a stale tab can settle another tab's row, and tests need to know
which of several projections is authoritative.

## Principles

1. **One durable record per submission, one reconciled pending view.** The
   outbox row is the authority for unresolved delivery; the recovery row is the
   authority for not-sent; accepted optimistic records are the authority for
   accepted-but-not-yet-canonical work; canonical `pendingMutations` are the
   authority for daemon-owned pending turns. The pending view reconciles all
   four by `clientMutationId` and never double-counts. The outbox alone cannot
   be the sole authority: accepted submissions leave it before the transcript
   incorporates them.
2. **Every non-derived marker has one named owner.** The click-to-commit
   marker, provenance, and unread state are real facts; they belong to one
   projection contract, not to a second in-flight record.
3. **One projection per hub/session binding.** Canonical turns, pending rows,
   readiness, markers, and the live client come from one registry entry. A
   live conversation screen borrows that entry; it does not create a second
   live service. A one-shot modal read/action service is not a live binding and
   is not a second owner.
4. **One submission gate.** Eligibility is a pure predicate over the projection
   and the clicked turn, returning allowed plus a reason. Components render the
   answer and do not repeat the checks at activation.
5. **One continuation intent.** Latest-failure selection and the `continue`
   input live in the shared client package, used by browser and native. This is
   already true; keep it.
6. **Reads are demand-driven and coalesced.** One read actor per binding serves
   transcript, pending, and activity demand unless a measured case proves a
   separate subscription is cheaper. A new demand joins the in-flight read or
   schedules one follow-up; it does not start a second reader.
7. **Drafts do not own submission state.** A draft survives acceptance,
   refusal, remount, and reconnect without becoming a second record of what is
   in flight.

## Target model

### Shared package

- Keep `turnContinuation` predicates (latest failed turn, one pending
  continuation, continuation input) in `appwire-client/typescript`.
- Export the projection shape the gate consumes: canonical latest turn, the
  reconciled pending view (outbox, optimistic, and canonical
  `pendingMutations`), readiness, the in-flight markers, and the live target
  identity.
- Keep the mutation record and recovery shapes here so both clients read the
  same fields.

### Browser

- `pendingTurnsStore` stays the reconciled projection. It is not replaced. It
  already merges outbox, optimistic, and recovery records with the canonical
  thread model; keep that reconciliation and make its marker contract explicit:
  - `submittingRefs` owns the click-to-commit window;
  - `submittedHere` owns provenance after settlement;
  - `loadedRefs` owns "unread" versus "read and empty";
  - `retryActivationsInFlight` folds into `submittingRefs`;
  - `inflightDurableEnqueues` and `undeliveredMutationIds` become gate fields,
    not a second local condition list.
- `threads.ts` exposes one `SessionProjection` per hub/session combining the
  canonical store, the outbox projection, and the read state.
- `Composer` and the Retry action call the shared gate. The activation path
  still re-reads the live projection, but it no longer repeats the checks.

### Native

- `nativeConversationSessions` remains the one registry entry per hub/session:
  store, mutation host, service, activity sink, read actor, refcounted attach.
- `NativeMutationRuntime.read` is the only projection source for pending and
  recovery; screens do not query storage directly.
- `ConversationMutationState.pendingMutation` is removed after the durable
  projection and gate replace it. The draft's `unconfirmed` field shrinks to
  the acceptance-window fact it actually owns.
- `ForkScreen` keeps its one-shot `createConversationService`: it verifies the
  source instance and performs a fork action, with no notification consumer,
  store, activity sink, or mutation host. Its `readProjection` participates in
  the shared, refcounted thread subscription and releases it on close. It is a
  modal action helper, not a second live conversation binding.
- The screen renders `gate(projection, clickedTurn)` and submits through the
  entry's current service. A retired entry refuses without losing the draft.

## Migration

### Phase 1: inventory and delete dead paths

- The inventory is done. The only dead production path is
  `mobile-native/src/session/useReadRetry.ts`; its only importer is its own
  test. Delete both.
- Some existing comments claim native has no durable outbox; correct them when
  touching those files.
- Exit with no behavior change and a clean affected suite.

### Phase 2: consolidate in-flight markers

- Fold browser `retryActivationsInFlight` into `submittingRefs`.
- Remove native `ConversationMutationState.pendingMutation` after the durable
  projection and gate cover its readers; reduce draft `unconfirmed` to the
  acceptance window.
- Keep `ForkScreen`'s one-shot service. Do not add a registry borrow API for
  it; reusing the live entry's `readProjection` would clear the shared
  ref/capabilities and fence the conversation underneath.
- Keep `submittingRefs`, `submittedHere`, and `loadedRefs`; they are not
  derivable from the outbox. Give them one documented home in the projection.
- Exit when each concern has one writer and one reader contract, and the
  existing recovery and draft tests pass unchanged.

### Phase 3: one gate and one read actor

- Move the remaining eligibility checks into the shared gate: browser
  `availabilityFor` and `continueTurn`, native `sendAction`, `errorAction`, and
  the Retry precondition.
- Make transcript, pending, and activity demand share one read actor per
  binding, with explicit cancellation and a single follow-up. If activity
  measurement shows a separate subscription is cheaper, keep it and document
  the exception.
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
  recovery ownership, draft preservation. Include the accepted-work case: an
  accepted optimistic record plus a canonical pending turn with no outbox row
  counts once and keeps Retry disabled.
- Test isolation: stop and recreate the native mutation runtime between cases,
  or inject one without a live discovery interval. Add a lost-reply case that
  advances the discovery interval and asserts one `clientMutationId`, not one
  raw method call.
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
