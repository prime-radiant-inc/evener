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

Share only what is genuinely common; keep storage and publication local.

- Reuse the shared record, pending-entry, projection and commit-feed selectors
  in browser and native host code instead of host-side scans.
- Share the stale-admission refusal classification between browser
  `MutationAdmissionLostError` and native `requirePrecondition`, preserving the
  rule that a refusal never falls back to a storage write.
- Share pure settlement identity/evidence selection only where the browser and
  native decisions are identical. Do not move publication fences, storage
  commits, SQLite transactions, IndexedDB epochs, attachment bytes, or
  notifications.
- Keep `mutationOutboxIndexedDB.ts` and the native SQLite adapter separate.
- Tests: refusal classification and shared selector cases; no storage rewrite.

Acceptance: no duplicate record/entry shape remains in the host layers; all
existing outbox and recovery tests pass unchanged; the refusal distinction is
covered in both clients.

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

- Replace the per-route `createConversationStore` instances with one store per
  `(hubId, ref)`, shared by every mounted view of that session. Keep the
  existing store lifecycle and subscription teardown; release on the last view.
- The durable mutation runtime and its SQLite adapter remain the submission
  owner. The shared store holds the canonical conversation and the projected
  pending state.
- Red-first: two mounted views share a canonical turn advance; one view's
  accepted continuation is visible to the other; no second mutation can be
  admitted from the stale view after the shared store advances.
- Do not add a new admission coordinator, progress revision, or refresh claim.

Acceptance: the two-view tests fail on the per-route stores and pass on the
shared store; existing native store tests pass.

## Task 6: Native one-owner Retry

- Keep the clicked failure's turn ID through `runErrorAction` and
  `retryFailedTurn`; do not lose it at the row boundary.
- Use `isLatestRetryTurn` on the shared canonical store and
  `ownPendingTurnInput` over the shared runtime pending projection. Show the
  action only for the latest own failure; render it disabled while own input is
  unresolved; recheck both at activation before the existing
  `document.submitText(TURN_CONTINUATION_TEXT, ...)` path.
- Preserve `errorAction` sign-in precedence and the existing refusal/recovery
  paths.
- Tests: extend `mobile-native/src/session/errorAction.test.ts` and
  `mobile-native/src/ConversationScreen.retryJourney.test.tsx` with the
  transition table and activation races; add a two-view shared-store lost-reply
  case.

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
