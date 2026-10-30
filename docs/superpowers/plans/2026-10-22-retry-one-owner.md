# Retry one-owner implementation plan

Plan for [the one-owner Retry spec](../specs/2026-10-22-retry-one-owner-design.md).
Branch: `retry-one-owner` from `origin/main` `0cf2e55b90`.

## Global constraints

- Two facts, two owners: `isLatestRetryTurn(canonicalSession, failedTurnId)` and
  `NOT ownPendingTurnInput(outboxEntriesFor(ref))`. No third derivation.
- No transcript-reflection settlement, accepted-copy layer, per-view admission,
  progress watermark, refresh claim, or parallel persistent queue.
- Keep ordinary Send/Queue, drafts, structured selections, images, Resume/Sign
  in precedence, explicit refusal recovery, and local-storage-failure recovery.
- Keep IndexedDB and SQLite storage adapters, the dispatcher, and the protocol
  separate. Consolidate only genuinely shared outbox code.
- Tests first. Real Evener code below the provider/network/storage boundary. No
  copy-as-oracle, fabricated durable identity, fixed microtask drains, or
  wall-clock sleeps. Assert canonical turn IDs, outbox identities/counts,
  submitted payloads, disabled/accessibility state, and durable rows.
- Run targeted suites, both client typechecks, and touched-file formatting
  before each push. CI owns full gates.
- Update `docs/daily-work.md` and the owning `docs/product/subsystems.md` rows
  only with verified behavior.

## Task 1: Shared eligibility predicates

Add the two shared predicates and their transition tests.

- `appwire-client/typescript/turnContinuation.ts`: port `latestTurnId` and
  `isLatestRetryTurn` from the reviewed implementation. It uses
  `Pick<ThreadModel, "turns" | "runningTurnId" | "history" | "status">`,
  `isTurnActive`, rejects invalidated/failed history, vetoes a different
  running identity, rejects active status without a running identity, and
  compares the canonical last turn by identity.
- `appwire-client/typescript/state/mutation/pendingEntries.ts`: add
  `ownPendingTurnInput(entries)` returning true for an own (`fromThisClient`)
  non-canceled `send` or `queue` entry. Keep `ownPendingSend` as the narrower
  routing predicate.
- Export both through the existing barrels.
- Tests: extend `turnContinuation.test.ts` (create if absent) and
  `pendingEntries.test.ts` with a transition table: latest own failure,
  historical failure, foreign running turn, active status without ID,
  invalidated/failed history, no pending, own send, own queue, foreign send,
  canceled own send, mixed entries. Red before implementation.

Acceptance: table tests fail before the predicates and pass after; package
typecheck and targeted Vitest pass.

## Task 2: Adjacent outbox consolidation

Clean `main` already uses the shared mutation package from both clients:
`mutationOutbox.ts` re-exports and extends the package's `MutationOutbox`,
records and selectors, and `nativeMutationRuntime.ts` imports the shared
mutation contracts. The named `MutationAdmissionLostError` /
`requirePrecondition` classes belong to the abandoned feature branch and do
not exist here. No record-shape or discovery consolidation is required.

Verify the imports and record the result. Any remaining native adapter
alignment (flush result/target ports) is cleanup, not Retry correctness, and
belongs in the broader client-state cleanup spec. Do not manufacture a merge
of IndexedDB and SQLite adapters, the dispatcher, or the protocol.

Acceptance: a short verification note in the task report names the shared
imports and confirms no duplicate record/entry shape remains in the host
layers; no code change unless a real duplicate is found.


## Task 3: Browser one-owner Retry

- `TurnBlock.tsx` passes the failed turn's ID to `TurnFailureEndCap`.
- `TurnFailureEndCap.tsx` reads the canonical `threadsStore` model and the
  projected pending entries. Show Retry only when
  `isLatestRetryTurn(model, turnId)`; disable it when
  `ownPendingTurnInput(entries)`.
- `threads.ts:continueTurn` accepts the expected failed turn ID, rechecks both
  owners synchronously before submission, and submits
  `TURN_CONTINUATION_TEXT` through the existing preserve-composer path. A
  stale callback submits nothing.
- Do not import the pending store back into `threads.ts`; extend the existing
  projection seam or keep the checked action beside the projection owner.
- Tests: extend `TurnFailureEndCap.test.tsx` and
  `pendingTurnsStore.test.ts` with render/activation races: latest changes after
  render, pending appears after render, repeated press, historical failure
  hidden, own unresolved send/queue disables, foreign pending does not.

Acceptance: the browser unit tests fail before the wiring and pass after; no
reflection or accepted-copy code is introduced.

## Task 4: Browser real journey and docs

- Extend the real-hub Retry journey in `scripts/retryguard` for: lost acceptance
  reply through reconnect/remount, exactly one continuation, draft preserved,
  repeated press, newer turn removes the historical action, reload with an
  unresolved durable row.
- Update `docs/daily-work.md` and the browser Retry row in
  `docs/product/subsystems.md` with verified behavior.

Acceptance: the journey runs against the real hub with deterministic provider
and network boundaries; evidence names the exact commands and outcomes.

## Task 5: Native canonical session store

The binding-sharing design is
`native-store-sharing-design.md`. Split implementation into four bounded
steps, each committed and reviewed before the next.

### Task 5a: Conversation store canonical/pending readiness

- In `mobile/src/state/conversation.ts`, add the store-owned `latestReadState`
  (`unknown`, `reading`, `ready`, `error`), a read-only `canonicalSession`
  selector that returns the model only while valid, `ensureLatest()` through the
  existing drain scheduler, and pending readiness (`pendingMutationsReady`) with
  an awaitable refresh.
- Invalidated/failed history, initial open, reconnect, and a target storage
  notification lose canonical readiness until an accepted current latest-window
  publication restores it. Paging, display reprojection, pending emptiness, a
  returned promise, or a discarded/superseded read cannot restore it.
- A pending storage notification marks readiness false before its read starts;
  retain prior rows for display but never present them as verified empty. A
  pre-notification read cannot certify a later write. A failed refresh leaves
  readiness false and the durable row intact.
- Tests first in `mobile/src/state/conversation.test.ts`: unknown before first
  read; error stays unknown; accepted latest ready; discarded read not ready;
  paging/display no restore; notification invalidates before read; late
  pre-notification read rejected; awaitable refresh resolves/cancels on
  retirement; ordinary Send/Queue routing unchanged.

### Task 5b: Binding registry and provider

- Add `session/nativeConversationSessions.ts` and
  `NativeConversationSessionsProvider.tsx` per the design: one entry per
  `(hubId, ref)` owning store, host/submitter, service, activity sink and read
  actor; attach/release handles with refcount; final release closes the entry,
  not the durable runtime; committed-layout attachment; Strict Mode safe;
  client replacement/hub isolation/reopen semantics.
- Tests: two attachments share store/service/sink and one target registration;
  release one during a held read, survivor publishes; final release unregisters,
  cancels, closes; late replies cannot resurrect; reopen starts fresh; same ref
  on different hubs isolated.

### Task 5c: Screen migration

- `App.tsx` installs the provider under preferences and above navigation.
- `screens.tsx` replaces screen-owned host/service/sink/store/read-retry/pending
  rebind/focus suspend with the shared attachment. Keep per-view controls,
  drafts, scroll/reader state, paging demand, image picker, Find and disclosure
  behavior; aggregate following-live-end and move disclosure clear to final
  release.
- Tests: existing screen suites plus two-view canonical advance, one unresolved
  own submission blocking both, settlement/read gap, display-level sharing and
  reader isolation.

### Task 5d: Entry read actor

- Move the existing `useReadRetry` backoff into one entry-owned actor; cancel
  on entry retirement; make rehydrate failures reach the actor even when the
  retained store still reads open. No per-view timer, no settlement demand
  loop.
- Tests: one timer per entry, rehydrate error recovery, teardown.

Acceptance: the split tests fail before each step and pass after; existing
native store/screen tests pass; no `conversationAdmission`, progress watermark,
refresh claim or admission coordinator is introduced.


## Task 6: Native one-owner Retry

Split into two bounded steps.

### Task 6a: Action identity, predicates, activation

- `errorAction` returns an internal action descriptor carrying the clicked
  failed turn ID and disabled state; Sign in precedence remains independent.
  `TimelineItem`/`ErrorRow` carry the source turn ID and set the existing
  Pressable's `disabled` plus accessibility state together. No design-system
  widget/props change.
- Eligibility uses the shared store's `canonicalSession` (only while ready),
  `pendingMutationsReady`, `isLatestRetryTurn`, and `ownPendingTurnInput`, plus
  the existing own-failure, connection, focus, capabilities and document-busy
  checks. Retry is hidden unless the canonical latest own failure is the
  clicked row; disabled while pending input or canonical/pending readiness is
  unknown.
- `retryFailedTurn`/`runErrorAction` retain the clicked turn ID; activation
  rechecks the current shared store, `handle.live`, attachment focus/binding and
  document state before the existing `document.submitText(TURN_CONTINUATION_TEXT,
  ...)` path. A stale callback submits nothing.
- Tests: `session/errorAction.test.ts` transition table; `TimelineItem.test.tsx`
  and row tests for source identity + actual disabled/accessibility; screen
  tests for latest, pending, unknown/failed readiness, two views, stale
  activation and Sign in precedence.

### Task 6b: Non-wire precondition and `not-sent`

- Add an optional internal `precondition: () => boolean` to
  `ConversationMutationRequest`, passed from the Retry action through the entry
  submitter. Recheck the originating attachment, binding, expected source turn
  and pending readiness/entries immediately before runtime enqueue, and again
  before direct fallback. No protocol/persistent field.
- `DraftDocument.submitText` distinguishes a proven local `not-sent` refusal
  from ordinary false/throw outcomes: clear only that operation's uncertainty
  marker, keep the live draft text/images, no durable row, no false unconfirmed
  ghost. Ordinary refusal/recovery behavior unchanged.
- Tests: retirement while startup is held refuses before commit with no durable
  row; retirement after commit leaves the row owned by the runtime; `not-sent`
  preserves an edited draft and images; ordinary outcomes unchanged.

Acceptance: the native unit and journey tests fail before the wiring and pass
after; no `conversationAdmission` availability policy is introduced.


## Task 7: Native real journey and docs

- Extend the native journey for lost reply, remount, repeated press, newer
  running/successful/failed turn, reload with unresolved and settled rows, and
  draft/image preservation.
- Update the native Retry row in `docs/product/subsystems.md` and any
  `docs/daily-work.md` behavior note with verified outcomes.

Acceptance: the journey runs on the native surface with the real hub and
provider boundary; evidence names the exact commands and outcomes.

## Task 8: Broader client-state cleanup spec

After Tasks 1-7 land, write a separate spec covering transcript publication and
read ownership, overlay/history reconciliation, store lifecycle and
subscription teardown, general composer/draft state, and the cross-client
projection architecture. No implementation in this plan.

## Delivery

One PR for the two Retry rules and the adjacent consolidation. Independent
implementation review, simplify, shepherd, admin squash under repository
policy, verify merged content, then remove only owned worktrees and scratch.
The old browser PR and native candidate remain preserved and unmerged.
