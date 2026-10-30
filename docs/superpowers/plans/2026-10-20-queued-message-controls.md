# Queued-message controls implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make locally queued messages editable and removable in browser and native without losing drafts, replaying removed submissions, or duplicating edited resends.

**Architecture:** Keep the existing outbox, recovery records and composer owners. Share eligibility and composition helpers; transfer ownership atomically in each storage adapter. Native's separate draft and mutation databases require a durable resend receipt until the draft acknowledges admission.

**Tech Stack:** TypeScript, React, React Native/Expo, IndexedDB, SQLite, Vitest.

**Spec:** `docs/superpowers/specs/2026-10-20-queued-message-controls-design.md`, approved by Jesse on 2026-10-20, including the older-native-record fallback.

## Global constraints

- Cover browser and native, including offline messages without first pressing Stop. Edit never sends automatically. Remove targets one submission identity, not equal text.
- Preserve full text, image bytes/order/references, selections, mention locations and current composer work. Older native records retain saved prose and images without guessed reference locations.
- Preserve uncertain-delivery reconciliation, accepted queue editing's text-only boundary, Retry identity and each client's existing ordering. Edited Send creates one fresh identity.
- Promotion and input-empty drain previews are not compositions. A content-bearing drain restores only its new input. Notes remain with the note editor.
- No new widgets or widget props, wire protocols, databases or tables without discussion. No deployment or hub restart.
- Follow `docs/developing-evener/testing.md`, the frontend design-system guide and `mobile-native/AGENTS.md`. Assert state, bytes, ownership and requests, never UI/error phrasing. Exercise real application logic and adapters; script external transport/provider boundaries only.
- Use package-name imports for the shared client. Run focused red/green tests, typechecks and touched-file formatting locally; CI owns full gates. Preserve unrelated files.

## Review focus

1. Two Edit actions into one occupied composer preserve both sources and consume them once on Send. Tasks 2–5 test multiple owners.
2. Legacy literal `[image 1]` must never bind to a current or later tile numbered 1. Tasks 1, 4 and 5 test merges, removal, later insertion and encoding.
3. A native resend may settle before the separate draft write completes. Tasks 2 and 4 test crash recovery after settlement and during acknowledgment.
4. Attachment encoding and newer typing can overlap an Edit. Tasks 3–5 test actual composer owners across deferred boundaries.
5. Another tab, Retry, dispatch, a newer Stop or a changed session identity may win first. Tasks 2–5 test losing actions without copies or duplicate sends.

## Preparation

Read the spec and current repository instructions. Source baseline: `5b1020a7ae6d225828a1127680a6f7467cb6870c`. Recheck the worktree before implementation. The active pre-commit hook is already verified; do not change shared hook configuration.

After Jesse reviews this plan and chooses execution, inspect `scripts/web-preflight.sh` and `scripts/native-preflight.sh` before preparing dependencies. Never run `npm ci` through a symlink. Native needs an independent install. No dependencies were installed during planning.

Tasks 1–2 establish shared contracts; Task 3 delivers browser behavior; Tasks 4–5 deliver native persistence and UI. Each task includes a red/green cycle, focused regression checks and a commit of its named paths after staged-diff review. Keep progress in the task list.

## Task 1: Shared eligibility and composition restoration

**Files**
- Create: `appwire-client/typescript/state/mutation/queuedMessages.ts`, `appwire-client/typescript/state/mutation/queuedMessages.test.ts`
- Create: `appwire-client/typescript/composerRecovery.ts`, `appwire-client/typescript/composerRecovery.test.ts`
- Modify: `appwire-client/typescript/state/mutation/records.ts`, `appwire-client/typescript/state/mutation/index.ts`, `appwire-client/typescript/index.ts`
- Modify/test: `cmd/evener-hub/frontend/src/panes/session/composer/recovery/recoveryDraft.ts` and its existing `.test.ts`

**Interfaces:** Introduce the following, using existing shared record, attachment and mention types:

```ts
interface ComposerComposition<A extends { marker: number }> {
  text: string;
  attachments: A[];
  skillNames: string[];
  commandNames: string[];
  mentions?: ComposerMention[];
}
interface RecoveredComposition<A extends { marker: number }> extends ComposerComposition<A> {
  imageReferences: "recorded" | "unavailable";
}
function recoverMutationComposition(record: MutationRecord): RecoveredComposition<InputAttachment>;
function mergeComposerCompositions<A extends { marker: number }>(
  current: ComposerComposition<A>, recovered: RecoveredComposition<A>
): ComposerComposition<A>;
function nextComposerImageMarker(text: string, attachments: readonly { marker: number }[], highWaterMark?: number): number;
function canEditQueuedMessage(record: MutationOutboxRecord): boolean;
function canRemoveQueuedMessage(record: MutationOutboxRecord): boolean;
```

Add `"editing"` to `MutationRecoveryKind`. Export policy through `state/mutation` and composition helpers through the root barrel. Browser wrappers keep their signatures, skill-document parsing and pending-tile conversion.

- [ ] **Write failing tests.** `queuedMessageActionsRespectOwnership` covers canceled/unattempted, attempted/unknown, note/Stop, promotion and drain records. `compositionRoundTripPreservesAssociations` covers image-only/selection-only input, gaps and UTF-16 offsets. `legacyLiteralNeverCapturesATile` asserts:

```ts
expect(canEditQueuedMessage(unattemptedQueue)).toBe(true);
expect(canEditQueuedMessage(attemptedQueue)).toBe(false);
expect(canEditQueuedMessage(emptyDrain)).toBe(false);
expect(canRemoveQueuedMessage(unattemptedPromotion)).toBe(true);
expect(merged.text).toContain(legacyText); // fixture input, not application copy
expect(merged.attachments.every(image => image.marker !== 1)).toBe(true);
expect(encodedImageData).toEqual([currentImageData, legacyImageData]);
```

Also remove each tile, add another, merge a later recovery and encode with real `buildComposerInput`: the legacy literal stays verbatim and original image associations survive.
- [ ] **Run red.** From `cmd/evener-hub/frontend`: `./node_modules/.bin/vitest run ../../../appwire-client/typescript/state/mutation/queuedMessages.test.ts ../../../appwire-client/typescript/composerRecovery.test.ts src/panes/session/composer/recovery/recoveryDraft.test.ts`. Failure must identify missing behavior, not a runner/install error.
- [ ] **Implement.** Eligibility requires canceled state or explicit unattempted waiting state, rejecting contradictory attempt evidence. Only owned start/queue/steer/content-bearing-drain input is editable. Read image bytes from payload input and original associations from composer metadata. For legacy text, reserve all literal marker numbers, renumber colliding CURRENT tiles and their references only in current text, update mention offsets, and allocate recovered tiles outside those numbers. Later allocation and merges must also skip literal numbers already in current text. Preserve unchanged attachment identities.
- [ ] **Verify green.** Repeat the command, then the existing shared composer-input/attachment-marker tests. Every selected test must execute and pass.
- [ ] **Commit** only these source/test files: `feat(client): share queued-message policy and composition recovery`.

## Task 2: Atomic storage actions and resend receipts

**Files**
- Modify: Task 1's `appwire-client/typescript/state/mutation/queuedMessages.ts`
- Create: `appwire-client/typescript/testing/queuedMessageStorageConformance.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts`, `mobile-native/src/mutationOutboxStorage.ts`
- Create: `cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.queuedMessages.test.ts`, `mobile-native/src/mutationOutboxStorage.queuedMessages.test.ts`
- Regression: both adapters' existing `.conformance.test.ts`, native `.test.ts` and browser `.cancel.test.ts`

**Interfaces:** Consume Task 1's policy. Add an extension port, leaving the dispatcher's mandatory interface unchanged:

```ts
interface QueuedMessageStorage<A extends MutationAttachmentRef = MutationAttachmentRef> {
  getOutboxWithStopEpoch(id: string): Promise<{ record: MutationOutboxRecord<A> | undefined; stopEpoch: number }>;
  editQueuedMessage(expected: MutationOutboxRecord<A>, barrier: MutationStopBarrier): Promise<MutationRecoveryRecord<A> | undefined>;
  removeQueuedMessage(expected: MutationOutboxRecord<A>, barrier: MutationStopBarrier): Promise<boolean>;
  resendRecovery(sourceIds: string | readonly string[], intent: MutationIntent<A>, barrier?: MutationStopBarrier): Promise<MutationOutboxRecord<A> | undefined>;
}
```

IndexedDB already has the snapshot method and single-ID resend. `undefined`/`false` means a lost/stale claim; storage failure rejects. In its existing `updateRecoveryInput(id, input, attachments?, composerText?, composerMentions?)`, add final `consumeRecoveryIds?: readonly string[]`: saving a combined recovery and consuming other sources must share its transaction.

Native-only exports from `mutationOutboxStorage.ts`:

```ts
interface RecoveryResendCommit {
  targetRef: string;
  sourceIds: readonly string[];
  clientMutationId: string;
}
// Methods on MutationOutboxSQLite:
readRecoveryResend(targetRef: string, sourceIds: readonly string[]): Promise<RecoveryResendCommit | undefined>;
acknowledgeRecoveryResend(targetRef: string, clientMutationId: string): Promise<void>;
```

- [ ] **Write failing adapter tests.** `editClaimsOneDurableOwner`, `removeOnlySelectedIdentity`, `dispatchAndEditHaveOneWinner`, `retryStopAndEditRejectStaleSnapshots`, `resendConsumesEverySourceOrNone` and `resendReceiptSurvivesSettlement` assert:

```ts
expect(await storage.getOutbox(original.clientMutationId)).toBeUndefined();
expect(await storage.getRecovery(original.clientMutationId)).toMatchObject({ recoveryKind: "editing" });
expect(await storage.editQueuedMessage(original, capture)).toBeUndefined();
expect(successfulResends).toHaveLength(1);
expect(successfulResends[0]?.clientMutationId).not.toBe(original.clientMutationId);
```

Include identical-text neighbors, two clients, stale target/instance, missing attempt evidence, newer Stop, old-schema reopen, write failure/rollback at each boundary, and successful retry after storage recovers.
- [ ] **Run red.** Frontend: `./node_modules/.bin/vitest run src/stores/mutationOutboxIndexedDB.queuedMessages.test.ts`. Native: `npm test -- src/mutationOutboxStorage.queuedMessages.test.ts`. Run each from its package directory. Native also runs its existing scene-lifecycle script.
- [ ] **Implement transactional guards.** Compare durable ID/target/instance/thread/method/state/sequence and Stop epoch against the expected snapshot inside the write transaction. Edit writes `recoveryKind: "editing"` then deletes the outbox owner atomically; Remove deletes only that row. Never rewrite a dispatchable payload under its old identity. Resend/consolidation require every source to exist under the exact target; create one fresh resend ID and consume all sources or none. A newer Stop holds the fresh row canceled. Preserve Retry's existing ordering/identity.
- [ ] **Implement native persistence.** Add composer-mention JSON to existing record-table serialization and migrations. Store resend receipts in `mutation_sequence.recovery_resends`, written in the resend savepoint and retained after outbox/optimistic settlement. Acknowledgment removes only the matching receipt and is idempotent. Use synchronous savepoint bodies, without awaits.
- [ ] **Verify green.** Repeat both commands plus existing adapter suites. Reopen after settlement to prove the receipt survives; test failed acknowledgment followed by successful acknowledgment. Verify browser recovery consolidation rolls back both update and deletion on failure.
- [ ] **Commit** only this task's paths: `feat(client): atomically edit and remove unsent queued messages`.

## Task 3: Browser controls and recovery composer

**Files**
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts`
- Modify: under `cmd/evener-hub/frontend/src/panes/session/composer/`: `queue/pendingTurnsStore.ts`, `queue/QueueStrip.tsx`, `Composer.tsx`, `sourceState.ts`, `attachments/useAttachments.ts`
- Create: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.queuedMessages.test.tsx`
- Regression: existing `Composer.integration.test.tsx`, `queue/QueueStrip.test.tsx`, `attachments/useAttachments.test.ts`, recovery and store tests
- Create: `docs/product/message-recovery.md`
- Modify: `docs/product/subsystems.md`, `docs/web-ui/README.md`

**Interfaces:** In `threads.ts`, add:

```ts
function editQueuedMutation(clientMutationId: string, targetRef: string): Promise<MutationRecoveryRecord | undefined>;
function removeQueuedMutation(clientMutationId: string, targetRef: string): Promise<boolean>;
function useEditableQueuedMutationEntries(ref: string): MutationOutboxRecord[];
```

Mirror the first two as `editQueuedPendingTurn`/`removeQueuedPendingTurn` in `pendingTurnsStore.ts`, using its mutate-then-refresh boundary. Consume Task 2's storage methods and atomic recovery consolidation. Keep `activeRecoveryId` singular; reuse `QueueStrip.onEditRecovery`, `Composer.activateRecovery`, `sourceState.queueRecoveryPersistence` and `resendRecoveryPendingTurn`.

- [ ] **Write the failing journey.** `queuedEditsPreserveDraftAndSendOnce` drives the real composer/store and IndexedDB: enqueue identical messages, Stop, reload, edit into an occupied image/selection-bearing draft, revise, send once and remove the other. Repeat offline without Stop and with two recovery edits. Assert real persisted identities and captured transport input:

```ts
expect(deliveredInputs).toEqual([expectedMergedRevision]);
expect(await reopened.getOutbox(removedId)).toBeUndefined();
expect(await reopened.getRecovery(editedId)).toBeUndefined();
expect(savedImageBytes).toEqual(expectedOriginalAndCurrentBytes);
```

Add two-tab races, failed writes then recovery, newer Stop, stale session identity, encoding-in-progress and newer typing while awaits resolve. Preserve accepted queue, uncertain delivery and control-operation behavior.
- [ ] **Run red.** Frontend: `./node_modules/.bin/vitest run src/panes/session/composer/Composer.queuedMessages.test.tsx`.
- [ ] **Implement.** Use existing widget actions for eligible waiting and held rows without duplicate rendering. Offline actions require local storage and matching identity, not daemon connectivity. Capture the snapshot/epoch at press, refresh losing actions, and activate only winning edits. Check attachment encoding before claiming. Make edited recovery discoverable after reload, without calling it rejected. Consolidate a second recovery atomically; preserve newer composer state and source-pane lifetime. Capture resend's Stop barrier before asynchronous prerequisites. Use Task 1's marker allocator for later images.
- [ ] **Verify green.** Repeat the journey and the named regression suites, then `npm run typecheck` from the frontend. Confirm image-only/selection-only, canceled promotion and empty/content-bearing drain coverage.
- [ ] **Document and commit.** Make `docs/product/message-recovery.md` the current ownership/recovery guide. Link it from S11 and the web guide; keep historical designs unchanged. Commit the named paths after staged-diff review.

## Task 4: Native composition persistence and interrupted resends

**Files**
- Modify: `mobile/src/state/conversationMutation.ts`, `mobile/src/state/conversation.ts` and their `.test.ts` files
- Modify: under `mobile-native/src/`: `nativeMutationRuntime.ts`, `draftRepository.ts`, `draftDocument.ts`, `draftRecovery.ts`, `outbox/offlineSend.ts`, `composerCommand.ts`, `imageSelection.ts`
- Create: `mobile-native/src/queuedMessageControls.ts`, `mobile-native/src/queuedMessageControls.test.ts`
- Regression: native `nativeMutationDispatch.test.ts`, `nativeMutationRuntime.test.ts`, `draftDocument.test.ts`, `draftDocumentRecovery.test.ts`, `draftRecovery.test.ts`, `imageSelection.test.ts`, `outbox/offlineSend.test.ts`, `composerCommand.test.ts`

**Interfaces:** Add to `mobile/src/state/conversationMutation.ts`:

```ts
interface ConversationComposerSnapshot {
  readonly text: string;
  readonly attachments: readonly InputAttachment[];
  readonly skillNames?: readonly string[];
  readonly commandNames?: readonly string[];
  readonly mentions?: readonly ComposerMention[];
}
interface ConversationMutationContext {
  readonly composer?: ConversationComposerSnapshot;
  readonly recoverySourceIds?: readonly string[];
}
```

Extend `ConversationMutationTarget` with this context. Add final `context?: ConversationMutationContext` to existing store `send(service, input)`/`queue(service, input)`, `offlineRequest(target, action, input)`, and the `composerCommand` submit callback. Forward through the store's internal dispatch/submit methods. Service wire signatures stay unchanged.

Add `DraftComposerMetadata` in `draftRepository.ts`: optional `skillNames: string[]`, `commandNames: string[]`, `mentions: ComposerMention[]`, `recoveryIds: string[]`. Extend `DraftRecord` with optional `composer` and `unconfirmedComposer` of that type, plus `recoveryResendAcks?: string[]`. Persist these in existing draft-table columns and its savepoint. Selection-only, recovery-owned and acknowledgment-only rows must survive empty-row cleanup.

Add/extend methods on `DraftDocument`:

```ts
appendEditedRecovery(record: MutationRecoveryRecord): "restored" | "already-restored" | "blocked";
submit(operation: (text: string, images: InputAttachment[], context: ConversationMutationContext) => Promise<boolean>): Promise<void>;
settleEditedResend(commit: RecoveryResendCommit): boolean;
finishRecoveryResendAck(clientMutationId: string): boolean;
```

Add runtime facades `editQueuedMessage(id: string, targetKey: string): Promise<MutationRecoveryRecord | undefined>` and `removeQueuedMessage(id: string, targetKey: string): Promise<boolean>`, plus Task 2's receipt read/ack methods. The existing `submit(request)` detects `recoverySourceIds` and uses guarded resend; it must never fall back to direct wire send for this path.

New `queuedMessageControls.ts` consumes those methods:

```ts
function editQueuedMessageIntoDraft(runtime: NativeMutationRuntime, targetKey: string, id: string, document: DraftDocument): Promise<"restored" | "already-restored" | "blocked" | "lost">;
function reconcileEditedResends(runtime: NativeMutationRuntime, targetKey: string, document: DraftDocument): Promise<void>;
```

- [ ] **Write failing persistence tests.** `editedResendSurvivesEveryDatabaseCutPoint` uses separate real draft/mutation SQLite databases. Test interruption after edit claim, draft append, resend admission, settlement, draft confirmation, witness acknowledgment and acknowledgment-marker deletion. Preserve newer typing and image bytes:

```ts
expect(reopenedDocument.getSnapshot().record.draft).toBe(newerDraft);
expect(reopenedDocument.getSnapshot().record.unconfirmed).toBeNull();
expect(replayedSubmissionIds).toEqual([]);
expect(await reopenedStorage.readRecoveryResend(targetKey, sourceIds)).toBeUndefined();
```

Also test two edited sources, failed writes then recovery, duplicate restore, selection/image-only drafts, legacy gaps/collisions, later recovery merge, and a literal typed during delayed image encoding.
- [ ] **Run red.** Native: `npm test -- src/queuedMessageControls.test.ts src/draftDocumentRecovery.test.ts src/imageSelection.test.ts`.
- [ ] **Implement metadata and append.** Capture originals before `buildComposerInput`; persist composer text, attachment marker metadata and mentions in `intentFor`, leaving bytes/selections in payload input. Append against the latest draft using Task 1, save source IDs with text/images/metadata, and deduplicate by recovery ID. Keep failed append sources recoverable. Preserve existing text-only rejected recovery. Audit every draft record constructor so replace/restore/dismiss/submit cannot silently drop ownership.
- [ ] **Implement interrupted-resend handling.** Submission saves captured composition/IDs as unconfirmed before Task 2 consumes sources. New typing remains separate. Reconciliation finds the exact receipt, clears only matching unconfirmed ownership and saves its fresh ID in `recoveryResendAcks` in one draft write, acknowledges the receipt, then removes the saved acknowledgment ID. Repeat safely after any crash; never infer non-delivery from missing outbox rows. Ordinary submit success must not clear recovery-owned content before this confirmation. Retained source rows prove an all-or-none resend did not consume them.
- [ ] **Implement marker reservation.** `imageSelection.ts` skips text literals, current/unconfirmed tiles and pending allocations. Recheck inside `DraftDocument.addImage` after encoding; if a literal now collides, choose a fresh marker and preserve image identity/bytes. Remap mention offsets when marker lengths change.
- [ ] **Verify green.** Repeat the new tests and listed regression files with `npm test -- <files>`. For touched shared-store tests, run from native: `PATH="$PWD/node_modules/.bin:$PATH" sh ../scripts/native/run-tests.sh --root .. --config mobile-native/vitest.config.mts mobile/src/state/conversation.test.ts mobile/src/state/conversationMutation.test.ts`. Run `npm run check`.
- [ ] **Commit** only this task's source/test paths after staged-diff review.

## Task 5: Native controls in transcript and QueueSheet

**Files**
- Modify: under `mobile-native/src/`: `screens.tsx`, `useNativeMutationRecovery.ts`, `MutationRecoveryPanel.tsx`, `session/ghosts.ts`, `session/GhostBubble.tsx`, `session/GhostRows.tsx`, `QueueSheet.tsx`
- Create: `mobile-native/src/queuedMessageControls.render.test.tsx`
- Regression: native `nativeMutationPendingRecovery.test.ts`, `session/ghosts.test.ts`, `session/GhostRows.test.tsx`, `QueueSheet.test.tsx`, `MutationRecoveryPanel.test.tsx`
- Modify: `mobile-native/README.md`, `docs/product/message-recovery.md`, `docs/product/subsystems.md`

**Interfaces:** Consume Tasks 1–4; retain current ghost action routing and QueueSheet's close-before-Edit path. Derive local Edit/Remove eligibility from durable records, not pending display rows or connection state. Share local merge readiness between both surfaces, distinct from the empty-composer predicate for rejected text recovery.

- [ ] **Write failing rendered journeys.** `nativeQueuedControlsPreserveAndResendOnce` drives actual GhostRows and QueueSheet through Task 4's helper/runtime/adapters with scripted external transport. Run the spec's held/offline/legacy journeys, recreate before resend and after removal, and race Retry/dispatch/newer Stop. Assert:

```ts
expect(wireInputs).toEqual([expectedRevisedComposition]);
expect(await reopenedStorage.getOutbox(removedId)).toBeUndefined();
expect(reopenedDraft.getSnapshot().record.draft).toBe(expectedPreservedDraft);
```

Verify both surfaces expose the same eligible rows, image/selection-only submission, storage failure recovery, stale identities, uncertain delivery, accepted queues and promotion/drain boundaries.
- [ ] **Run red.** Native: `npm test -- src/queuedMessageControls.render.test.tsx`.
- [ ] **Implement.** Pass original composition context through online `deliver` and offline submit. Wire guarded Edit/Remove and losing-claim refresh. Acquire edited recovery offline after recreation. Include local waiting/held rows in QueueSheet and its matching More count/limit; it currently filters to daemon queue origins. Preserve pending-encoding protection, current draft work, existing uncertain controls and rejected-image limits. Run resend reconciliation on reopen and after admission before clearing captured ownership.
- [ ] **Verify green.** Repeat the new rendered test, named regressions and `npm run check`. Use the real native testkit only for platform/navigation boundaries, not mocked ownership logic.
- [ ] **Document and commit.** Complete the current guide's native handoff and legacy-image limit; link it from native README/S03. Do not close broader C05 convergence or C06 rejected-image work. Commit only the named paths.

## Whole-change acceptance and delivery

Verify every spec behavior against red/green evidence, including removal/reopen and failed-write recovery. The screenshot's duplicate cause remains unconfirmed unless a reproducing test establishes it. Format touched files using each owning package's pinned Biome; rerun affected checks and typechecks.

Use two independent PAR implementation reviewers on the immutable base/head diff, spec and test evidence, including new files. Resolve findings, simplify, and rerun affected checks. Shepherd the PR through matching-head CI and review; follow repository merge policy. Verify the intended squash merge before removing only this task's worktree and branches. Report the PR, proved behavior, limits and cleanup. No deployment.

## Plan self-review

Spec behaviors 1–6 and 8–10 map to Tasks 1–5; Retry identity/ordering and Stop behavior map to Tasks 2, 3 and 5. Both journeys, offline operation, legacy input, restart cuts and recovery after failure have explicit tests. Shared types are defined before consumers. Native's acknowledgment fields cover the separate-database gap without another queue or store. The browser retains its existing single recovery owner. No server protocol change is planned.
