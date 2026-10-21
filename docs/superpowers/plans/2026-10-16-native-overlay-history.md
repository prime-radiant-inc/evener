# Native Recorded-History Ownership Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking. Jesse requires a dedicated Sol 6.1 coordinator in its own worktree for this fix.

**Goal:** identical refreshes and reconnects never multiply live notices or promote display overlays into recorded history.

**Architecture:** expose a narrow shared-reducer operation for replacing recorded turns and deriving display. Native reconciliation and retention use recorded history at every write boundary, while overlays remain independently owned live state.

**Tech Stack:** shared TypeScript AppWire reducer, Zustand native conversation store, Vitest, existing React Native renderer.

**Spec:** [Plugin session preservation design](../specs/2026-10-16-plugin-session-preservation-design.md), shared constraints and Fix 4.

## Global Constraints

- Tests assert typed state, identities, counts and side effects, never prose.
- Default tests use local fixtures and scripted external boundaries, never live providers or ambient home configuration.
- Run targeted red/green checks locally. Full tests and lint gates belong in CI.
- Use existing client widgets. This work introduces no new widget or widget prop.
- Preserve authoritative tool bodies carrying live overlays; never strip all `overlayKey` items.
- Preserve loaded older pages, drafts and reading position across refresh/reconnect.

## Review Focus

1. Stable overlay on a completed turn or overlay-only turn: repeat refresh stays one, reset becomes zero. Task 1 and Task 2.
2. Recorded tool plus live output overlay: recorded body survives, deltas and completion handoff still work. Task 1 and Task 2.
3. Multiple older pages with a visible later-page row: refresh/reconnect retains that row and its reading anchor. Task 2.
4. Cross-turn reissues, full-turn withdrawals and omitted fields: reconcile authoritative data without reviving removed fields. Task 2.
5. Stale pages, epoch/boot changes, cap trimming and a racing newer draft: existing fencing/retention rules stay intact. Task 2.

---

## Ownership and execution

Use coordinator `codex-jesse-at-pr/gpt-6.1-sol` with `isolation="worktree"`.
This fix is independent of plugin behavior and can run beside preservation.
Own only the paths below and focused test fixtures. Do not change warning codes,
plugin loaders, transport protocols or the existing UI design.

### Task 1: expose recorded-turn replacement through the shared display owner

**Files:**
- Modify: `appwire-client/typescript/reducer.ts`, `appwire-client/typescript/index.ts`.
- Test: new `appwire-client/typescript/reducer.recorded-history.test.ts`.
- Update: `appwire-client/typescript/README.md` if documenting the new root export.

**Interfaces, new root export:**

```ts
export function replaceRecordedHistoryTurns<M extends ThreadModel>(
  model: M,
  turns: readonly TurnModel[],
): M & { history: HistoryState };
```

This function accepts **recorded** turns only. Preserve the existing history's
boot/epoch/length/generation/cursor/deferred metadata; replace only `history.turns`
and derive display through the existing `withDisplay`. Use `EMPTY_HISTORY` for a
model that genuinely has no established history, without inventing a real boot
identity. Preserve model extras and live overlay state subject to the reducer's
existing covered-overlay pruning. Do not acquire new history authority or accept
a stale read here; callers must first pass the existing reducer gates.

- [ ] Add tests using real hydrated models: overlay-only notice, notice anchored to a recorded turn, recorded tool with an overlay and final recorded handoff. Use distinct fixture IDs and assert record/display counts independently:

```ts
const updated = replaceRecordedHistoryTurns(model, model.history!.turns);
expect(updated.history.turns.flatMap(t => t.items).filter(i => i.id === noticeId)).toHaveLength(0);
expect(updated.turns.flatMap(t => t.items).filter(i => i.overlayKey === noticeKey)).toHaveLength(1);
expect(updated.history.bootGeneration).toBe(model.history!.bootGeneration);
```

  Use explicit guards instead of non-null assertions in committed tests. Pin history metadata, generic model extras and covered-overlay pruning independently. Include legacy/unversioned input without creating a compatibility shim.
- [ ] Run the targeted file with the command below. First demonstrate the failing display-to-history assignment using the current native helper behavior; a missing-export compiler error alone is not the regression proof.
- [ ] Implement the wrapper over `withDisplay` and export it by package name. Keep the private display algorithm in the shared reducer; do not copy it into native code.
- [ ] Rerun the new file and relevant existing reducer history/overlay tests. Read the scripts before installing/running dependencies. Stage only the named files, inspect the staged diff, commit `fix(client): expose recorded-history replacement through display owner`, then run both task reviews.

Targeted runner, from `mobile-native`:

```bash
node node_modules/vitest/vitest.mjs run --root .. --config mobile-native/vitest.config.mts --configLoader runner --no-cache --maxWorkers 2 --exclude '**/.worktrees/**' appwire-client/typescript/reducer.recorded-history.test.ts
```

### Task 2: repair every native history write boundary

**Files:**
- Modify: `mobile/src/state/conversation.ts`.
- Test: `mobile/src/state/conversation.test.ts`, `mobile-native/src/projectedRows.test.ts`, and `mobile-native/src/ConversationScreen.recovery.test.tsx` for the later-page reading-anchor scenario.
- Update: `docs/product/subsystems.md` S03/S10 and `mobile-native/README.md` recovery/qualification guidance.

**Interfaces:**
- Import `replaceRecordedHistoryTurns` from `@evener/appwire-client`.
- Retire the local `withSyncedHistoryTurns` helper by converting all its callers.
- Reconciliation inputs for versioned models are `model.history.turns`, never `model.turns`. Legacy models without history use their recorded legacy turns only until the shared operation establishes history.
- Keep `applyReadModel`, `mergeOlderItemPage` and `applyNotification` as the identity/generation authorities. Read their accepted recorded result before applying native cross-turn reconciliation, full-turn omission, snapshot field clearing or retention.
- Rebuild display after each recorded transformation. Snapshot authority maps come from fresh recorded history, not overlaid display rows. Retention windows may be chosen from display, but the bounded payloads committed to history must come from the recorded turns. Keep existing transient warning seating separate.

- [ ] Add `rehydrate keeps one overlay notice and no recorded ghost` using `createConversationService` + `createConversationStore` and a scripted wire client at the external boundary. Open one completed recorded turn with one overlay notice, then perform three identical refreshes. Assert display counts `[1, 1, 1, 1]`, recorded notice counts `[0, 0, 0, 0]`, and unchanged overlay identity. Current behavior should fail with display `[1, 1, 2, 3]`. Send overlay reset and assert zero notice in history and display.
- [ ] Add companion cases for `history/updated`, `loadOlder`, background/foreground rehydrate and full-versus-ordinary detail. Cover a real recorded tool with live overlay, two separately identified notices with identical prose, cross-turn reissues, full-turn withdrawal, snapshot omitted-field clearing, stale reads/pages and boot replacement. No prose-based deduplication.
- [ ] Add a multi-page scenario: load at least two older pages, select a visible row on the older page, refresh and reconnect, and assert its identity, retained body, paging cursor and reading anchor remain valid. Retain a newer draft written while the refresh is pending. Include cap pressure with a seated transient notice and a hidden-by-detail tool body.
- [ ] Add recorded-payload cap assertions after each of `setDisplayConfig`, `setFollowingLiveEnd` and live notification publication, then send an overlay frame and assert discarded bodies do not reappear. Preserve the active-turn exemption and detail-hidden payload rules. These three boundaries currently trim display only and never call `withSyncedHistoryTurns`.
- [ ] Preserve main's reconnect cases: a new service object resuming the same ref keeps loaded history; live frames during the pending read reach both conversation and activity; a resync during opening schedules the deferred canonical reread after resume; applied mutation identities settle pending sends without retiring merely queued sends. Extend the existing store cases with one overlay notice and assert no lost event, duplicate notice or extra subscription.
- [ ] Run the targeted store test file before changes and record the original duplication failure:

```bash
# From mobile-native, after inspecting its dependency and runner setup.
node node_modules/vitest/vitest.mjs run --root .. --config mobile-native/vitest.config.mts --configLoader runner --no-cache --maxWorkers 2 --exclude '**/.worktrees/**' mobile/src/state/conversation.test.ts
```

- [ ] Convert every `withSyncedHistoryTurns` call: notification reconciliation, post-read reconciliation, post-read cap, page merge and post-page cap. Also convert the three direct `turns: boundRetainedTurns(...)` writes in `setDisplayConfig`, `setFollowingLiveEnd` and live notification publication to bound recorded payloads before deriving display. Preserve accepted/rejected-model reference checks, cursor advancement and activity sink generation behavior. Do not solve the symptom with notice text deduplication, overlay-key stripping, or reset-on-refresh.
- [ ] Rerun the new shared tests, the whole touched conversation-store file, projected rows and affected screen tests. Run `(cd mobile-native && npm run check)` and `(cd cmd/evener-hub/frontend && npm run typecheck)` after safe dependency preflight. Use pinned Biome from each owning package on exact touched paths. Audit both assignments into `history.turns` and display-only reconciliation/retention writes; the renamed helper's absence proves neither contract. Preserve `routeLiveFrame`, opening-time deferred rereads and mutation settlement from main.
- [ ] Update docs, stage only the named touched paths, inspect the staged diff, commit `fix(native): reconcile recorded history before applying overlays`, then run task specification/quality reviews and a whole-fix review.

## Completion and qualification

Report actual red/green counts, paging/cursor/draft invariants and the shared
helper's consumers. Full native/web gates run in CI. Request an installed-iOS
foreground/background and later-page refresh check on an available iOS host;
headless store/renderer tests alone do not establish device scroll geometry.
Keep that qualification explicitly pending if this Linux environment cannot run
it. Do not label the four-fix product result device-verified without that run.
