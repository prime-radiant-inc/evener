# Retry Eligibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Offer Retry only for the latest failed turn and keep one pending continuation across repeated presses and recovery, in browser and native.

**Architecture:** Share the latest-turn identity predicate through the existing shared client, including running turns with no display items. Keep unresolved delivery in the existing mutation/outbox owners and use their pending-send projection, not message-text matching or a new Retry marker. Both clients validate the source turn and serialize admission before asynchronous work, retaining protection through accepted-but-unreflected delivery and reload.

**Tech Stack:** TypeScript, React, Zustand, Vitest, browser IndexedDB, native draft/outbox adapters, Go real-hub journey fixture.

**Spec:** `docs/superpowers/specs/2026-10-20-retry-eligibility-design.md`

## Delivery amendment

Jesse approved shipping browser first, then returning immediately to native on 2026-10-20. The first delivery contains Task 0, Task 1, and the browser portion of Task 3. Native changes stay in their existing isolated lane and are not part of that PR. Keep #4792 and #4793 open until native acceptance is also verified. Native requirements remain unchanged; review its ownership design before another repair attempt.

## Global Constraints

- Preserve `continue` input, transcript history, draft text, selections, and attachments.
- Preserve native Resume/Sign in precedence and ordinary composer Send semantics.
- Use existing widgets and mutation/recovery owners. No protocol changes or parallel persistent Retry queue.
- Approved scope is one client and session, including its mounted views and reloads, not global deduplication across devices.
- An unresolved own send/queue submission or not-yet-loaded pending projection makes Retry inactive; availability recovers automatically when authoritative state loads or settles.
- Read `docs/developing-evener/testing.md` and web `docs/developing-evener/design-system.md` before edits.
- Use DeepSeek 4.1 Flash implementers in isolated worktrees. Never install through a symlinked `node_modules`; compare lockfiles before sharing an install.
- Reproduce each regression before its fix. Use structured state and external-boundary fixtures, not mocked Evener logic or copy assertions.
- Targeted tests, typechecks, and touched-file formatting run locally; CI owns full gates.

## Review Focus

- A newer running turn may have zero items. Task 0 tests its identity, and Tasks 1 and 2 verify the old action disappears before any new item arrives.
- Two mounted views and an accepted-but-unreflected send must not create independent continuations. Tasks 1 and 2 hold enqueue, wire reply, and new-turn events independently.
- A remount can precede the pending-state read or replace a binding during enqueue. Tasks 1 and 2 test inactive initial state and automatic useful recovery after settlement.
- A stale callback can outlive its failed turn. Both client tasks retain a callback, advance the conversation, and assert no submission.
- Refusal or historical paging must not leave Retry permanently unavailable or make an old failure actionable. Both client tasks cover explicit recovery and refreshed live-tail identity.

## Task 0: Shared latest-turn predicate

**Owner:** DeepSeek shared-client implementer, before client lanes start.

**Files:**
- Modify `appwire-client/typescript/turnContinuation.ts` and `index.ts`.
- Create `appwire-client/typescript/turnContinuation.test.ts`.
- Extend `appwire-client/typescript/state/mutation/pendingEntries.ts`, its `pendingEntries.test.ts`, and `state/mutation/index.ts`.

**Interfaces:** Export `isLatestRetryTurn(thread: Pick<ThreadModel, "turns" | "runningTurnId" | "history" | "status">, failedTurnId: string): boolean`. This is the shared identity check, not send admission: callers still establish that the row is that turn's own failure and apply their capabilities, pending-read readiness, and submission guards. A different running identity prevents Retry even if absent from display turns; active status without a running identity is also insufficient to establish eligibility. Invalidated or failed history cannot establish the live tail; existing owners refresh it. Absent history carries no invalidation signal, so use the canonical owner's `turns` and live state, never the viewport's rows. Do not compare turn IDs lexically or infer ordering from an absent running turn.

Export `ownPendingTurnInput(entries: readonly PendingTurnEntry[] | null | undefined): boolean` from the shared mutation package for Retry: count this client's non-canceled `send` or `queue` entries. Keep existing `ownPendingSend` semantics unchanged for ordinary composer routing. Share provenance/state filtering where needed rather than duplicating it. Readiness remains a separate fact; a false predicate on null entries is not proof the read completed.

- [ ] Add tests named `latest failure identity`, `older failure identity`, `empty running turn`, and `invalidated history`. Use fixture `older` followed by `latest`: assert false for `older`, true for `latest`, false for `latest` with `runningTurnId: "new-running"` and no corresponding display turn, and false while history is invalidated. Assert true again after a valid latest-window refresh without a different running turn.
- [ ] Add tests for active status without a running ID (ineligible until coherent live state arrives), absent history with known canonical turns, and pending own send/queue entries in submitting/blockedUnknown/accepted/claimed states. Assert canceled, another client's input, and steer do not count. Pin unchanged `ownPendingSend` routing behavior.
- [ ] Run from `cmd/evener-hub/frontend`: `npx vitest run --maxWorkers=2 ../../../appwire-client/typescript/turnContinuation.test.ts ../../../appwire-client/typescript/state/mutation/pendingEntries.test.ts`; record failure before implementation.
- [ ] Implement and export the predicates without changing the shared `TURN_CONTINUATION_TEXT` value or mutation persistence.
- [ ] Rerun that test and `npm run typecheck`; require zero failures. Format the touched shared files with the frontend's pinned Biome.
- [ ] Commit only the six owned files and return the SHA and red/green evidence. Review and integrate before spawning both client lanes from that commit.

## Task 1: Browser admission and availability

**Owner:** DeepSeek browser implementer, isolated lane.

**Files:**
- Modify `cmd/evener-hub/frontend/src/stores/threads.ts`.
- Modify `cmd/evener-hub/frontend/src/panes/session/transcript/TurnFailureEndCap.tsx` and `TurnBlock.tsx`.
- Extend `cmd/evener-hub/frontend/src/panes/session/transcript/TurnFailureEndCap.test.tsx`, `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`, and affected `composer/Composer.test.tsx` cases.
- Reuse `cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts` and existing mutation persistence subscriptions if an availability projection needs extension.

**Interfaces:** Consume Task 0's `isLatestRetryTurn` and `ownPendingTurnInput`. Change internal `continueTurn(ref: string): Promise<void>` to `continueTurn(ref: string, failedTurnId: string): Promise<void>` and update every caller. Give `TurnFailureEndCap` the source `turnId: string` from `TurnBlock`. Expose the existing pending store's per-ref readiness alongside its entries. A stale/ineligible, unknown-state, or already-pending invocation must create no new mutation. Rendering and activation use the same current eligibility facts. Do not infer a Retry by matching message text or `preserveComposer`.

- [ ] Establish the targeted baseline after safe dependency setup.
- [ ] Add failing tests: latest failure is actionable; older failures remain visible without actions, including a newer running turn with zero items; stale handler cannot send; immediate repeated activation and two views leave one pending mutation; pending action stays inactive after rerender/remount.
- [ ] Delay the first durable pending read on remount. Assert disabled before read, disabled for a loaded unresolved send, and automatically enabled for loaded empty state when the same failure is still latest. Inspect real mutation persistence, not a mocked eligibility selector.
- [ ] Hold enqueue, external transport response, and new-turn events independently. Add lost-reply and refusal/recovery cases, retaining draft assertions. Assert one identity through accepted-but-unreflected state and restored useful operation after settlement.
- [ ] Invalidate history, allow the real read owner to refresh it, and assert Retry becomes available again without manual reconnect when the same failure is still latest. Repeat with a failed read followed by successful retry. Cover a successful newer turn and older-page refresh explicitly.
- [ ] Run the new tests and record behavioral failures before production edits.
- [ ] Implement the minimal admission and projection changes using existing persistence/recovery state. Cover the pre-persistence race with owner-scoped synchronization, not only component state. Revalidate source identity after asynchronous prerequisite reads.
- [ ] Run from `cmd/evener-hub/frontend`: `npx vitest run --maxWorkers=2 src/panes/session/transcript/TurnFailureEndCap.test.tsx src/panes/session/Session.test.tsx src/panes/session/composer/Composer.test.tsx`; require zero failures. Run `npm run typecheck` and touched-file `npx biome check --write`.
- [ ] Inspect and commit only owned paths. Return SHA, red/green commands and output, exact changed interfaces, and unverified cases.

## Task 2: Native eligibility and pending rendering

**Owner:** DeepSeek native implementer, isolated lane. Independent of Task 1's browser internals.

**Files:**
- Modify `mobile-native/src/screens.tsx`, `mobile-native/src/TimelineItem.tsx`, and `mobile-native/src/session/ErrorRow.tsx`.
- Modify `mobile-native/src/session/errorAction.ts` only to express the agreed availability; preserve its existing latest-own-failure and action precedence.
- Modify `mobile/src/state/conversation.ts` and its pending-mutation seam only where needed to distinguish unknown from loaded pending state and publish durable admission correctly.
- Extend `mobile-native/src/ConversationScreen.send.test.tsx`, `mobile-native/src/TimelineItem.test.tsx`, and `mobile-native/src/session/errorAction.test.ts`.
- Reuse `mobile-native/src/draftDocument.ts`, `draftDocument.test.ts`, and `draftDocumentRecovery.test.ts`; change the document only if a failing test exposes a gap in its existing guard.

**Interfaces:** Consume Task 0's `isLatestRetryTurn` and `ownPendingTurnInput`. Carry the source turn ID from the error row into activation. Extend the internal `onErrorAction` callback to `(action: ErrorAction, turnId: string | undefined) => void`, and make the Retry handler require `failedTurnId: string`. Re-read the current store and document snapshot before submitting. Treat `pendingMutations: null` as unknown until the owning seam publishes loaded state. Preserve the existing document's synchronous `submitting`/`unconfirmed` guard; once durable enqueue succeeds, the existing outbox owns uncertainty. The conversation owner must retain its per-ref admission guard until a pending read begun after that enqueue establishes the mutation's current state: merely re-reading an old loaded-empty projection is insufficient. Render a disabled Retry for an otherwise eligible latest failure until pending delivery is reflected or definitively settled.

- [ ] Establish the targeted baseline after safe dependency setup.
- [ ] Add failing rendered tests for inactive pending Retry and stale-source activation. Retain existing latest-only tests; extend them across empty-running/completed/failed newer turns and older-history refresh.
- [ ] Hold submission, press twice before rendering, remount before the pending read resolves, and assert one mutation identity plus unchanged draft/image records. Independently hold wire dispatch and new-turn events after durable enqueue, then after acceptance. Refuse and recover, proving the guard releases when appropriate.
- [ ] Delay the pending-seam refresh after durable enqueue succeeds, press again before it publishes, and assert one identity. Cover queued continuations in the pending projection, and active status without a running ID becoming coherent without permitting an intervening Retry. Invalidate/fail a history read, exercise its automatic retry, and assert useful operation resumes.
- [ ] Replace the binding before durable enqueue returns and verify the original document and outbox do not retain conflicting uncertainty owners after settlement. Reproduce before changing this path. Ordinary durable lost-reply recovery already clears the draft marker on enqueue, so do not add a new settlement workaround for that path. Preserve the explicit recovery path when local storage itself fails.
- [ ] Run the regressions and record their behavioral failures before production edits.
- [ ] Make the smallest native wiring and availability change, including conversation-owner synchronization across the post-enqueue projection lag. Do not replace the document/outbox state machines or conflate Sign in/Resume with Retry.
- [ ] Run from `mobile-native`: `npx vitest run --maxWorkers=2 src/ConversationScreen.send.test.tsx src/TimelineItem.test.tsx src/session/errorAction.test.ts src/draftDocument.test.ts src/draftDocumentRecovery.test.ts`; require zero failures. Run `npm run check` and touched-file Biome.
- [ ] Inspect and commit only owned paths. Return SHA, red/green commands and output, exact changed interfaces, and unverified cases.

## Task 3: Integrated recovery journeys and product contracts

**Owner:** DeepSeek journey implementer after the relevant client commits are integrated and reviewed. Under the delivery amendment, verify and document browser first, leaving native journey edits for the native delivery.

**Files:**
- `cmd/evener-hub/frontend/scripts/retryguard/run.mjs` and its `README.md`.
- `cmd/evener-hub/frontend/scripts/retryguard/wireFault.mjs` and `wireFault.d.mts` only if an external transport hold is needed.
- `mobile-native/src/ConversationScreen.retryJourney.test.tsx` and `conversationPlatform.testkit.tsx`.
- `cmd/evener-hub/retry_continuation_browser_test.go` only for required fixture orchestration.
- `docs/daily-work.md` and the S02/S03 entries in `docs/product/subsystems.md`.

**Interfaces:** Keep the existing real-hub fixture and driver contracts. Add scenarios through the current fixture rather than a second server. Use the real browser bundle and actual native screen renderer, scripted only at the external provider/network/platform boundaries.

- [ ] Extend the journey to hold a Retry acceptance and new-turn events independently, attempt repeated activation, reconnect/remount before pending reads resolve, and verify one input and mutation identity, durable settlement, draft retention, completed output, and useful operation after settlement.
- [ ] Verify an empty running turn removes the older Retry action before new display items arrive, and a new latest failure can recover. Verify refusal followed by the existing explicit recovery path.
- [ ] Verify successful newer turns and older-history paging across refresh/reconnect keep historical failures readable without Retry. Assert automatic recovery after delayed pending and live-history reads, not only the inactive intermediate state.
- [ ] Prove new journey assertions fail when the relevant guard is deliberately removed, then restore the guard and rerun.
- [ ] Update product docs with current eligibility and recovery ownership, preserving existing attachment-lifetime limitations.
- [ ] Read `make help`, the build-web target, and the guard before running from repository root: `make build-web`, then `go test -tags browserguard ./cmd/evener-hub -run '^TestRetryContinuation' -count=1 -v`. Require all selected scenarios to execute and pass. Report native renderer coverage separately from untested device behavior.
- [ ] Run both client typechecks, touched-file formatting, and affected targeted tests. Commit owned paths and report evidence.

## Integration and shipping

The coordinator reviews each task against the approved spec and verifies its tests. Integrate and review Task 0 before starting the clients. Browser and native writers may then run in parallel only in distinct worktrees with the ownership above. Stop before expanding a task into the other writer's files or changing the shared interface.

For each delivery, run independent implementation PAR, resolve legitimate findings red-first, run simplify-code, then push one PR. The browser PR references #4792 and #4793 without closing them; the native delivery closes them after its remaining acceptance checks pass. Use one CI/review wait per push. Merge the current base before final approval, settle every finding under repository policy, admin squash merge only after checks pass, and verify GitHub's merged content. Preserve the native lane and its evidence during browser cleanup, then return immediately to native work. Remove only owned disposable worktrees and scratch. Do not deploy.
