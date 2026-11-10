# Retry ownership: one outbox, one session

## Decision record

Jesse approved replacing the accumulated Retry settlement layers with one owner
for each fact, on both clients, and consolidating the adjacent outbox code that
this feature exposed. A separate spec will cover the broader client-state
cleanup after this workstream.

The previous eligibility spec's product behavior still holds. Its implementation
layers do not: transcript reflection, accepted-copy settlement, per-view
admission guards, progress watermarks, refresh claims, and paced reflection
reads are removed rather than repaired.

## Scope and non-goals

Apply the two Retry rules to browser and native:

- only the canonical latest turn's own failure offers Retry;
- one unresolved own submission keeps Retry inactive for that client and session.

Keep sending the shared `continue` message through the ordinary mutation and
outbox paths. Preserve ordinary Send and Queue routing, draft text, structured
selections and staged images, Resume and Sign in precedence, explicit refusal
recovery, and existing local-storage-failure recovery.

Do not change queued-message or canceled-message recovery, provider retries,
server admission, wire protocols, transcript hydration or paging, the mutation
dispatcher, or draft recovery. Cross-tab and cross-device deduplication is out
of scope; that needs a server-admission design.

## Ownership model

Two facts, two owners.

**Unresolved own submission** belongs to the durable outbox. The shared
`ownPendingTurnInput` predicate over the outbox's pending entries is the only
derivation. It covers own non-canceled send and queue entries. `ownPendingSend`
remains the narrower routing predicate and is not an eligibility authority.

**Canonical latest turn** belongs to the single canonical session model. The
shared `isLatestRetryTurn` predicate is the only derivation. It reads the live
session model, not the last visible transcript row, not a paged window, and not
a captured render.

Retry is eligible exactly when the canonical latest turn is this turn's own
failure and no own submission is unresolved:

```
retryEligible =
  isLatestRetryTurn(canonicalSession, failedTurnId)
  AND NOT ownPendingTurnInput(outboxPendingEntriesFor(ref))
```

No other layer may reconstruct either fact. A renderer may project the result,
and an activation may re-evaluate it against the current owner state. A captured
button state is not authority.

## Client topology

### Browser

The existing `threadsStore` is the canonical session model per ref. One store
serves every mounted view of that ref. Retry reads that store and the shared
outbox projection.

Remove the feature's duplicate authority:

- the accepted-copy reconciliation and `settleObservedAcceptedCopy` path;
- the reflection marker, `ensureRetryReflectionResolved`, and paced follow-up
  reads;
- `publishIdenticalSnapshot` / `requireLiveBaseModel` and the special
  publication path that exists only to let reflection reads settle;
- the Retry-specific pending-admission reconstruction in
  `pendingTurnsStore`/`threads.ts`.

Keep the unified optimistic store as the durable outbox projection, the
composition and recovery widgets, and the existing pending-entry projection.
The renderer consumes one owner snapshot.

### Native

The canonical session model must be one store per (client, ref), shared by every
mounted view of that session. A per-view canonical model is the root of the
native stale-sibling, retired-refresh, and newer-history defects; adding another
coordinator would recreate them.

Consolidate the native projected stores into one ref-keyed canonical store,
following the browser's existing single-store-per-ref shape. The durable outbox
and its SQLite adapter remain the submission owner. `isLatestRetryTurn` and
`ownPendingTurnInput` are consumed directly by the shared store's projection.

Remove `conversationAdmission.ts`'s availability policy: the three aggregate
`pendingAdmission` derivations, `admissionBehind`, progress watermarks, and
`refreshRequested`/`claimAdmissionRefresh`. Keep the native runtime's durable
protocol, enqueue admission refusal, observation/settlement entry points and
subscriptions as lifecycle operations. Keep splitA's retired-source refusal
before enqueue.

A view that cannot establish the canonical latest turn keeps Retry inactive and
asks the session store for an authoritative latest-window read. It does not
mint a progress revision, claim a refresh, or infer settlement from the read.
Once the store publishes a canonical model, every view renders the same answer.

## Lost replies and settlement

The outbox already owns durable enqueue, wire dispatch, refusal and lost-reply
recovery. Retry does not infer acceptance from the transcript.

While an own submission is unresolved, Retry is disabled. When the outbox
settles, the session store reflects the accepted or refused result through its
existing durable projection and authoritative read. If the reply is lost, the
outbox recovers the submission; Retry stays disabled until it does. A definitive
refusal keeps the existing recovery path and re-enables an otherwise eligible
failure.

## Adjacent outbox consolidation

Consolidate only what is genuinely shared; keep storage-specific behavior
separate.

Use the shared mutation package as the single source for record shapes, pending
entries, projection selectors, commit feed and outbox discovery. Browser
`MutationRecord`/`Outbox`/`Optimistic`/`Recovery`/`Commit` aliases already point
at the shared records; do not duplicate them again. Keep the browser Blob
attachment extension.

Share the distinction between stale admission refusal
(`MutationAdmissionLostError`/`requirePrecondition`) and storage failure, so
callers cannot turn a refusal into a fallback write. Share pure settlement
identity/evidence selection where the browser and native decisions are
identical; keep publication fences, storage commits, SQLite transactions,
IndexedDB epochs, attachment bytes and notifications local.

Do not consolidate the IndexedDB and SQLite storage implementations, the
dispatcher, or the protocol.

## Proof

A small transition table covers the owner, not a bespoke race fixture per
finding:

- no pending entries, one unsettled send, one unsettled queue, refusal,
  cancellation, settled delivery, reload with an unresolved durable row;
- latest failed, older failed, newer active with zero items, newer successful,
  newer failed, invalidated or failed history, running identity without a turn;
- the conjunction and its activation-time recheck.

One real journey per client, using the real client, outbox, storage and provider
boundary:

- Retry on the latest failure submits exactly one `continue`; repeated presses
  and remounts create no second identity;
- lose the acceptance reply, remount or reconnect, and prove the outbox recovers
  one continuation while Retry stays inactive until settlement;
- advance to a newer running, successful and failed turn and prove the older
  action is gone and a stale callback submits nothing;
- reload with an unresolved durable submission and with an already-settled row;
- preserve draft text, selections and images through acceptance and refusal.

No test may use transcript reflection as the settlement oracle, a fabricated
durable identity, fixed microtask-drain counts, or a copy string as the turn
identity. Assert canonical turn IDs, outbox identities and counts, submitted
payloads, disabled/accessibility state, and durable rows.

## Sequencing

1. Shared outbox consolidation plus the browser one-owner Retry change.
2. Native single-store consolidation plus the native one-owner Retry change.
3. Separate spec for the broader client-state cleanup: transcript publication
   and read ownership, overlay/history reconciliation, store lifecycle and
   subscription teardown, general composer/draft state, and cross-client
   projection architecture.

The existing browser PR and native candidate are preserved as evidence, not
merged. Implementation starts from a clean branch from `main` in the existing
isolated worktree.

## Success criteria

- Both clients expose Retry from the conjunction above and recheck it at
  activation.
- No transcript-reflection, accepted-copy, per-view admission, watermark or
  refresh-claim code remains in the Retry path.
- One canonical session store per (client, ref); every mounted view renders the
  same eligibility answer.
- The real lost-reply journeys pass with one mutation identity and preserved
  drafts.
- `make lint`, `make vet` and the affected suites pass in CI; the local targeted
  checks pass before push.
- The product docs describe the verified owner and recovery behavior.
