# SDK migration completion-scope audit

Snapshot: 2026-09-19T18:54:33Z. This is a read-only scope audit. The original
plan and the handoff table are historical planning documents; merge state below
uses targeted GitHub REST receipts plus the coordinator's current `progress.md`.
Source seams were rechecked at the locally available post-#1907 merge commit
`5d62d3a070ca3e912e9de00db7e2f285bf9b4a0c`. Root additionally verified the named native paging, pending-mutation, queue, and projector seams at current `origin/main` `323f28c30536c3a07f21538128cdad1e3afbb607`, the direct child of that merge. The accepted two-layer decision was also reread at this current main head. The repository-wide
inventory and `project-pr-accounting-current.json` snapshot predate the #1907
merge, so I do not derive a new repository-wide open count from them.

## Current baseline

The phase-D rows before the native transcript/queue tails are already landed.
Targeted REST verification confirms D1/D2/D3/D5/D7/D8a/D9/D10/D11/D12/D13,
D14-1/2, D15, D20, D21, D22, D23a, D23b, D23c-1, D23c-2a, D24-web, D25a,
D25b, D25c, D26 and D27 are merged. D28's web lift (#1623/#1667) and native
D1 (#1888) are merged. The old D6 p6b #1902 was closed without merge and its
work was superseded by the completed offline chain; #1904 is merged.

#1907 is also now merged: REST reports head
`00849a796e8ec03b76426fca443e132dbbd62dd2`, merged at 18:43:40Z as
`5d62d3a070ca3e912e9de00db7e2f285bf9b4a0c`, with all 15 current checks
successful. The 18:19 accounting snapshot still lists it as carried. The
follow-up interrupt-persistence Medium is explicitly accepted under #1181 and
is now open as #2005 at e3e99b5153a40e7268ea9f593259dbe2e61f93b6. Independent local review and RoboRev2633 passed; CI and the remote panel remain pending. The new panel has a confirmed Luna quota failure, so this is not merge-qualified.

## Dependency graph and remaining migration work

### D6 settings/transcript-display tail and Stack A

The immediate chain is:

`#1792 (p7 draft generation) → #1841 (p8 settings-hub generation) →
#1844 (p9 checkpointed draft editor) → #1845 (p10 transcript-hub defaults) →
Stack A.`

Targeted REST still reports #1792, #1841, #1844 and #1845 open. They are the
remaining serial D6 implementation chain; each successor needs an exact-head
refresh, current CI and a fresh raw review before the next one is rebased.
#1792 also carries the real generation-identity finding tracked as #1985; it
is corrected in isolated successor 264ae08142c0aed8841a089c24cc1917122c7ec5, with independent review and local RoboRev2631 passing. The successor is unpublished and must land before p8 can be treated as a clean foundation. A byte-identical P7 rebase onto main289f was attempted and aborted at the restoreDraft conflict: current main owns draftFieldsFrom(checkpoint) and writeUncertain preservation. Original41fc and child264ae remain clean and preserved. Since the parent is capped at five rounds, a smaller decomposition is being prepared; no semantic conflict resolution or sixth product round has been silently published. The local P10a wire-contract slice is consequently blocked by this chain and is not an additional open PR.

After #1845, the handoff's unstarted Stack A is five small, independently
reviewable settings/transcript-display pieces: A1 publish surface (~25
production lines), A2 localStore (~110), A3 crossTabSync (~150), A4 transitions
(~45), and A5 hubHalf (~150). They must start from the merged p10 API and the
saved Stack A oracles. The completion criterion is that each piece has its
production consumer wired, deterministic tests for publication/lifecycle and
cross-tab or hub behavior where applicable, and a current-head CI/raw receipt.
Do not count Stack A as started merely because the historical design note
exists.

### D18 activity and usage

The package activity primitives are present, and native already consumes
`ActivityList` in `mobile-native/src/ActivitySheet.tsx:120`. The native
identity/generation contract remains in `mobile/src/state/activity.ts:384`
(`createActivityStore`) and the projection remains in
`mobile/src/services/activity.ts:425` (`createActivityService`). The remaining
D18 stack is concrete:

- #1919 is open and must extract/reuse `mergeTurnHistory(older, newer)` on
  both older-page loading and rehydrate. The historical panel identified a
  rehydrate path that dropped an accumulated fragment instead of applying
  fresh-wins-with-fallback; that finding still needs a current-head read, so
  this audit does not count the old panel as a fresh verdict.
- #1920 is open and is file-disjoint but review-ordered after #1919. It wires
  session usage totals from the package store. The settled ruling is to sum
  session usage and never overwrite the cumulative aggregate with a per-turn
  usage notification.

Completion means #1919's shared merge contract is used on both paths, the
cursor and fragment retention matrix passes, #1920 is rebased and merged after
it, and the existing identity/generation tests still reject stale relocation,
wrong-thread/ref and ambiguous live patches. There is no unresolved D18
product ruling in the current evidence.

### D23c-2b and D23d

D23a, D23b, D23c-1 and D23c-2a are merged. The remaining c-2b stack is the
three open, stacked pieces #1737 (C), #1738 (E) and #1740 (D), with #1580 only
the reference/oracle PR. They must be completed in that order with the saved
C/E/D restack note. Historical panel items are not all live blockers: the
projection-stack audit separated current contract work from refuted claims (for
example, #1930 concerns attachment filenames, not the earlier array-identity
explanation). Requalify each current head rather than carrying every old
Medium into the completion count. #1580 may be closed after the pieces land
and should not be mistaken for implementation.

D23d has no current PR. It is the model-owned paging cutover that follows the
c-2b stack. The live source still exposes the seam it must remove:

- `mobile/src/services/conversation.ts:1214`, `projectOlderTurns`, creates a
  temporary hydrated model and projects an older page;
- `mobile/src/state/conversation.ts:899`, `pageOwnedIds`, tracks page-owned
  rows and the rehydrate prepend/retention rules.

A D23d implementation is complete only when older-page data and cursor/order
ownership live in the `ThreadModel`/package path, the store no longer needs
`projectOlderTurns` or `pageOwnedIds`, and tests cover older-page plus live
notification interleavings, cap/truncation, cursor supersession and rehydrate
without resurrecting settled questions or moving live rows to the old prefix.
D23d is a hard prerequisite for native D24. This is a later cutover than
#1740: #1740's c-2b piece D makes rows read from the model and keeps older pages
attached within that slice; D23d owns the remaining store-level page ownership,
prepend and cursor machinery. Do not duplicate #1740's row projection work in
D23d.

### D24 native projector

D24-web #1663 is merged and `appwire-client/typescript/transcriptProjector.ts`
exports `projectThread` (line 345). The native half is not implemented. The
current native path is still split across:

- `mobile/src/conversation/project.ts:193` (`systemFamily`) and `:593`
  (`clusterActivities`);
- `mobile-native/src/timeline.ts:68` (`groupTimeline`);
- `mobile-native/src/transcriptPresentation.ts:181`
  (`projectNativeTranscript`), consumed from `mobile-native/src/screens.tsx`.

The native D24 slice should replace those native classification/clustering and
presentation seams with the package projector and its `ProjectedEntry`/
`ProjectedTurn`/metadata visibility contract. It depends on D23d's model-owned
paging, then needs tests for the native screen's activity grouping, warning and
failure visibility, attachments, usage/cost visibility, anchors and config
presets. The stale plan calls this a pending product ruling, but decision 2 is
already settled in the inspected main plan (`docs/superpowers/plans/2026-09-12-
sdk-migration.md:311-322`): native adopts the two-layer projector and therefore
gains the web's config-driven content levels. The remaining work is
implementation and validation, not a new product decision.

### D25 native host, rows and recovery

The shared package foundation is landed: D25a/b/c and D26/D27 are merged, and
D25d-1a/#1916 plus D25d-1b/#1917 are merged. The four-part native adoption
approved by Jesse is still incomplete:

1. **Runtime/dispatcher host wiring (D25d-2).** The open #1981 runtime slice
   covers native lifecycle/readiness and the approved composite durable key
   `JSON([hubId, ref])`; it does not prove the real host callers. Wire the
   native send/steer/queue/interrupt paths to the package dispatcher and
   durable storage, with one process handle and cleanup. The actual client and
   host lifetime must be used; no fake receipt or production fallback.
2. **Durable pending rows (D25d-3).** The current UI still gets the wire queue
   and one in-memory mutation marker: `mobile/src/state/conversation.ts:633`
   exposes `pendingSend`/`pendingMutation`, while
   `mobile-native/src/QueueSheet.tsx:48` reads `conversation.queue`. Adopt the
   package `pendingEntries`/`pendingTurns` projection so submitted-but-not-yet-
   echoed rows survive a remount and are identity-based rather than a single
   pending flag.
3. **Recovery and authoritative replay (D25d-4).** Persisted records must
   recover after restart, reconnect and loaded-thread remount. A known
   applied/replayed receipt settles exactly once; an attempted unknown outcome
   becomes blocked/needs confirmation and is never blindly replayed; a valid
   never-attempted record may dispatch after readiness. Exercise storage
   failure, hub/ref isolation, offline startup, successful settlement and
   recovery cleanup.

The post-#1907 main commit already contains the native SQLite storage adapter at
`mobile-native/src/mutationOutboxStorage.ts:157` and its storage tests. It does
not yet contain the host's `MutationDispatcher`/`MutationOutbox` wiring: the
native screens still call the conversation service and expose only the legacy
in-memory pending marker. D25d is complete when all three host slices are
merged with the four-part behavior matrix and the native screens no longer use
only that marker for pending work.

### D28 and current correctness successors

D28 D1 and the web lift are merged, but native D2a #1922 remains open. Its
completion criterion is retained screen/model state across a connection flap,
correct ready gating for initial and deferred reads, and no stale callback
publication after a client swap, with its current head requalified.

The current queue also carries correctness successors outside the old D-row
numbering. They are still part of practical migration closure because they
change the SDK contracts' consumers:

- marketplace outcome/reconciliation/publication: #1897, #1940, #1954,
  #1960, #1966, #1972, #1973, #1976, #1978 and #1983;
- paginated history precedence and public coverage: #1961, #1968 and #1982;
- reconnect/readiness adapters: #1952 and #1955.

These must be treated as dependency-ordered implementation lanes with fresh
review evidence, rather than inferred clean from an older panel. In particular,
#1897 still has the saved S3 marketplace Mediums, and #1982 is parked behind
three history Mediums in the current progress record. The 18:19 accounting
snapshot lists these rows as carried or dependent; it is not a fresh post-#1907
inventory.

## Intentional withdrawals and parked work

The following are not missing migration obligations:

- B4 is folded into D24; D17' (#1498) is the settled activity-panel cycle
  seam, not an activity-list adoption. D16, D19 and D14-3 native halves were
  withdrawn after finding no real twin. The old D1–D15 and D20–D27 plan rows
  that REST shows merged are stale table status, not open implementation.
- #1480 (A2 transcript Seq fold) is explicitly parked by Jesse. #1580 is an
  oracle/reference PR, not a required implementation. #1934 is the handoff
  document reference.
- TestFlight build 5 and physical-device acceptance for #1116 are parked by
  Jesse's focus ruling. They remain release/landing obligations if “entire
  native iPhone landing” includes device acceptance, but they are intentionally
  outside the SDK code queue.
- The #1907 interrupt-persistence correction under #1181 and accepted Low
  follow-ups such as #1946 are post-merge correctness work. They should be
  tracked to closure, but they do not reopen the completed #1907 carrier.

## Practical completion definition

The SDK migration can be called code-complete when the following receipts exist:

1. D6 p7→p10 and Stack A are merged and current-head qualified.
2. D18 #1919→#1920, D23 c-2b→D23d, native D24 and D25d-2/3/4 are merged,
   with the source seams and legacy native paths named above removed or reduced
   to host adapters.
3. D28 #1922 and the carried marketplace/history/reconnect consumer contracts
   have current-head CI and raw review qualification.
4. The package and native tests cover generation/identity fences, model-owned
   paging, config-driven projection, durable rows, known/unknown outcomes,
   restart/reconnect recovery and hub/ref isolation.
5. The explicit parked boundary is recorded: code queue closure does not imply
   TestFlight/device acceptance until Jesse reactivates it; if the requested
   goal is the full native landing, that is the final release gate.

## Addendum: retained web-first Stack A scope (2026-09-19)

The older inventory above is preserved as historical accounting. Against the
current `origin/main` tip `7b23fd083416bb7944b652ed73116b4949da8a80`, the
following web obligations remain uncovered and must stay in the completion
scope. They are retained migration work, not new product asks:

- **A7 — remove the dead web ready callback.** Delete the unused
  `useEffectiveTranscriptDisplay`/ready callback seam after A5 has adopted the
  package fence. The retained plan identifies the zero-consumer callback and
  its web typecheck/full-suite oracle (`docs/superpowers/handoffs/2026-09-18-mobile-landing-sdk-migration/ledger/tiny-stacks-plan.md:12`).
- **A8 — add transcript checkpointed drafts to the shared package store.**
  Extend `transcriptDisplayStore.ts` with `draft`, `draftConflict`,
  `draftError`, `draftUnreadable`, and `storageUnavailable`, plus the draft
  port and edit/save/discard/rebase actions, using the checkpointed editor
  primitives from P9. The current web store has only per-layout PATCH
  `drafts`, not an offline transcript draft repository; the retained plan
  explicitly calls this shared behavior net-new and requires its own tests
  (`tiny-stacks-plan.md:13`). This follows P9 and the package transcript
  store.
- **A9 — restore the web draft/gate adapter.** After A8, restore the
  `saving || writeUncertain` condition in the web `patchHubDefault` gate and
  carry `settledWrite` through the adapter. The retained plan assigns this
  work to the web adapter (`tiny-stacks-plan.md:13`); the current web gate
  does not contain those draft-write fields. This is web-only and follows A8.

A10's native `transcriptMobile` projection remains a separate native lane and
does not satisfy or reduce A7-A9. The A7-A9 order and package/web boundary are
also recorded by the retained Stack A plan (`tiny-stacks-plan.md:5-13`).
