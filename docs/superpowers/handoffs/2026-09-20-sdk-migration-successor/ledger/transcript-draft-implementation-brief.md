# Transcript draft implementation brief (A8/A9)

Status: checkpointed implementation breakdown only. No transcript code is implemented or declared complete by this brief.

## Evidence and boundaries

The retained P10 requirements keep the checkpointed draft port, restore/stale/invalid-checkpoint handling, draft/storage/error/conflict fields, generation stamping, save/discard/rebase, `settledWrite`/`whyFenced`, and direct-write gating open as A8/A9. The tiny-stacks ledger measures A8 at about 100 package lines and A9 at about 40 web lines, and explicitly says that the current web store has no offline transcript-draft mechanism. Therefore A8 needs failing-first behavior tests; moving existing tests or claiming package tests alone is insufficient.

The requested P9 SHA `b3694ccc8d989bfec7ffeb954f4d39bbc745490d` was absent before refreshing the coordinator checkout, but is now verified from GitHub as the merged commit (`D6 piece 9: checkpointed draft editor primitives`). Its relevant files match the local restacked implementation at `98cb71188f6bb30656b01e1d055e64d8db063e2c`; the local history splits that work across `1df8dcb6f`, `8cfebea58`, and `24da26047`. Use `b3694ccc…` as the publication/base receipt after fetching, while using the qualified local files for inspection.

The local `pr-1549` ref is `dfde921602c17e70d393adfcbdeece2dcb4682d9`; it is the native unreadable-keybindings projection fix and is not the transcript draft oracle. The original transcript implementation oracle is retained in `claude/sdk-d6-transcript-display`, including the merged reference `2d479be39` and its `appwire-client/typescript/transcriptDisplayStore.ts` and `transcriptDisplayStore.test.ts`. Use that source as a behavior oracle, not as a branch to merge wholesale: it also contains later generation and native fixes outside this small web adoption sequence.

Scope stays package transcript draft behavior plus its web adapter. Do not add a framework, a new SDK API family, native projection (A10), cross-tab or transition work, or unrelated cleanup. Native remains deferred.

## A8 — package checkpointed transcript editor

Base A8 on the qualified P9 package primitives and the already-landed transcript hub-default/direct-write store (A1/P10b/P10c). Keep the existing framework-free `createTranscriptDisplayStore` and `TranscriptDisplayStoreDeps` shape. Add only the transcript-specific checkpoint contract and actions needed by the existing store:

* Add `TranscriptDraftCheckpoint` (`id`, `layout`, `baseRevision`, normalized config, `writeUncertain`) and `TranscriptDraftStorage` with `createId`, `load`, `save`, `removeIf`, and `replaceIf`. Build the store repository with `createDraftRepository`; do not duplicate raw identity/CAS handling.
* Add state fields `draft`, `saving`, `writeUncertain`, `storageUnavailable`, `draftUnreadable`, `draftConflict`, and `draftError`. Preserve the existing direct-write `drafts` preview map; these are distinct paths.
* Add `editDraft(layout, config)`, `saveDraft(layout?, config?)`, `discardDraft()`, and `rebaseDraft(reviewedRevision)`. `editDraft` must persist before any hub PATCH. `saveDraft` must use the checkpoint's base revision, refuse while stale/busy/uncertain/unloaded, and classify the canonical response as known success or conflict. `discardDraft` must use `discardCheckpointedDraft` so readable and unreadable records use the classified identity and a replacement is adopted. `rebaseDraft` must move only onto the reviewed confirmed revision and refuse if the hub advanced again.
* Reuse `assertDraftDiscardable`, `persistCheckpointedDraft`, and `discardCheckpointedDraft` from `checkpointedDraftEditor.ts`, and `createDraftRepository`/`UnreadableDraftError` from `draftCheckpointPort.ts`. The transcript store owns its decoder, messages, layout identity, and restore function; do not force a generic payload abstraction into the store.
* Restore once through the repository at store creation/refresh. A port throw sets `storageUnavailable` and `draftError`; a decode failure sets `draftUnreadable` while leaving the hub readable and allowing discard. A replaced checkpoint is reloaded/adopted, never overwritten. No port means the documented in-memory fallback (`removeIf`/`replaceIf` succeed for that local repository); it must not manufacture a concurrent-writer conflict.
* Stamp a draft with the current ready generation when composed. A restored pre-ready draft remains generation `null` until the first authoritative payload stamps it. Staleness is generation-aware even when a replacement hub reuses the same numeric revision. A notification before first confirmation must not stamp a fake generation or lose the subsequent read.
* Preserve the two settlement classes: a known revision conflict lands the canonical hub value and leaves the proposal for review; a fenced/lost reply leaves `writeUncertain` and the checkpoint durable until an authoritative read settles it. Unreadable restoration clears stale uncertainty/conflict so discard is possible. `draftError` describes storage/cleanup failures; uncertainty itself is state, not a synthetic storage error.

The A8 package PR is complete only when these behaviors have new, meaningful tests and the package type/build surface passes. The existing hub/default and direct-write tests are regression coverage, not evidence that checkpointed drafts exist.

### A8 tests

Add focused cases to `appwire-client/typescript/transcriptDisplayStore.test.ts` using the existing draft-port fake (or the smallest equivalent) and real store/client fakes. At minimum cover:

1. edit persists, save checkpoints before PATCH, and confirmed success clears the checkpoint;
2. save refuses stale, unloaded, saving, uncertain, unsupported, and replaced-checkpoint state;
3. lost reply preserves the checkpoint and sets `writeUncertain`; an authoritative read settles it;
4. known revision conflict keeps the proposal and sets review/conflict state;
5. generation change marks a draft stale even when the next hub reports the same revision;
6. pre-ready restore stamps on first authoritative payload and does not stamp from a pre-generation relay;
7. malformed/unreadable storage leaves hub loading usable, exposes `draftUnreadable`, and discard clears it;
8. readable and unreadable discard both honor compare-and-remove; replacement during discard/settle is adopted;
9. rebase requires reviewing the current revision and then allows save;
10. port save/load/remove/replace failures set the right `storageUnavailable`/`draftError` fields without dropping an uncertain checkpoint.

Retain the package's existing direct-write oracle tests, especially direct-write refusal while checkpointed saving/uncertain, superseded replies, preview contradiction reconciliation, and retired-generation fencing. They must remain unchanged unless a failing-first assertion demonstrates the package adoption difference.

## A9 — web adapter and direct-write gate

A9 depends on A8's exported store contract and the A5/A6 web hub adapter. There is currently no browser transcript-draft persistence helper: `cmd/evener-hub/frontend/src/stores/transcriptDisplay.ts` has only the per-layout local override keys (`LOCAL_KEYS`) and its `writeLocal`/`removeLocal` helpers, while the `drafts` field there is only an in-flight PATCH preview. A2's extracted `stores/transcriptDisplay/localStore.ts` remains the local override/legacy-write boundary and must not be misused as a checkpoint repository. Add the smallest dedicated web `TranscriptDraftStorage` adapter (a new narrow module beside that boundary, or the equivalent explicitly reviewed host seam) backed by one namespaced `localStorage` record, JSON serialization, and compare-and-remove/replace by the checkpoint identity. Reuse the package's `createDraftRepository` and editor primitives for classification and state; do not invent another draft state machine or claim an existing web helper.

Pass that new port when creating the package store and mirror the package draft fields/actions through the real Zustand `StoreApi` bridge. Its storage failure behavior must feed the package's `storageUnavailable`/`draftError` fields; the existing local override `storageWarning` remains a separate concern.

Make the smallest hub wiring changes:

* The package A8 store owns the direct-write gate (`saving || writeUncertain`) and the shared post-await `settledWrite`/`whyFenced` settlement. A9 must not reimplement either rule in the web adapter. It should only expose the package action/state through Zustand, so `patchHubDefault` reaches the A8 gate and authoritative reads reach the A8 settlement path. An unreadable record remains discardable per A8's store rule.
* Keep generation and layout routing in the package; the web adapter only forwards state and supplies host persistence. The existing `drafts` preview map remains distinct from the persisted `draft` checkpoint.
* Wire existing callers to the package actions and fields without changing component behavior or adding a second draft editor. Existing local/cross-tab/transition modules remain host concerns and are not touched by A9.

### A9 tests and completion gate

Add only adapter-level tests needed to prove the real Zustand `StoreApi` forwards `editDraft`, `saveDraft`, `discardDraft`, and `rebaseDraft`, and that package `saving`/`writeUncertain` state is mirrored without a second web gate. The A8 package tests own the direct-PATCH refusal contract; A9 may exercise it once end-to-end as a forwarding regression, but must not duplicate its settlement logic. Exercise the new browser port with a local-storage fake for reload/blocked-storage semantics; do not assert rendered strings or snapshots. Keep the current 29 web transcript oracles unchanged and run the existing direct-write, reconnect, storage, and cross-tab suites as regression coverage.

A9 is behavior-complete only after A8 tests pass through the web adapter, the local draft survives reinitialization, migration/restore ordering is proven, direct writes are gated during uncertain settlement, and `make test-web` plus scoped Biome/type checks pass. Passing tests before this wiring is not a completion claim.

## Dependency order and review boundary

1. Use the verified merged P9 head `b3694ccc8d989bfec7ffeb954f4d39bbc745490d` and package exports/build files as the prerequisite receipt.
2. Land/verify A1 and A5/A6 package adoption prerequisites as planned.
3. Implement A8 as one package-only PR with failing-first draft tests and no web/native edits.
4. Implement A9 on A8's exact head as one web adapter PR; do not restack it onto an unqualified P9 or bypass the package contract.
5. Keep A10 native transcript projection deferred until A8/A9 are merged and independently verified.

Required final evidence for each PR is exact base/head, changed-file scope, meaningful test output, Biome/type/build gates, and review against the exact base. This brief records the plan and oracle; it does not report any of those future gates as passed.
