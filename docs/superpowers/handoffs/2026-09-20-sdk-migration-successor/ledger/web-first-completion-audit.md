# Web-first SDK completion gap audit

Read-only audit against `origin/main` at
`7b23fd083416bb7944b652ed73116b4949da8a80` (the #2024 merge tip), not the
older coordinator snapshot used by the original scope audit.
No tests were rerun and no product/ref/PR state was changed.

## Verdict

The web SDK is not complete. The mutation, usage, connection, projector, and
other previously landed web/package lifts are present on current `origin/main`.
The remaining web-first work is the D6 settings/transcript-display chain and its
Stack A web adapter. The p7 foundation, cross-generation fence, and
identity-preserving reload correction are merged as #2009, #2014, and #2024.
P8 and P9 remain open. The retained decomposition audit makes the
old p7 -> p8 -> p9 -> p10 description more precise: after p8, p10a can run in
parallel with p9; p10b/c follow p10a. Stack A still waits for the package
transcript-display API to be complete.

## Concrete remaining obligations

| Order | Obligation and scope | Current evidence / owner | Status and dependency |
| --- | --- | --- | --- |
| 1 | **D6 p7-C: preserve a draft generation across an identity-stable reload.** Shared `draftCheckpointPort`/`keybindingsStore` contract, consumed by web and native settings. | Existing local slice `codex/sdk-d6-p7-generation-reload-successor` at `d521522b9933933b1d1d1e7db99713936f7acffc`; retained queue called it #2024. The checked main store has the typed `KeybindingsDraft.generation` field (`appwire-client/typescript/keybindingsStore.ts:97-105`) and #2014's cross-generation fence. | **Merged in #2024 at `7b23fd083416bb7944b652ed73116b4949da8a80`.** P8 is the next live gate and must restack on this exact tip. |
| 2 | **D6 p8: extract the reusable settings-hub retirement/settlement and ready-generation wiring.** This is shared package code first consumed by web keybindings and later transcript display. | `claude/sdk-d6-p8-settings-hub-generation` at `c2afb38362d562370e81de5f559d162ae5ef6d4e`; PR #1841. `settingsHubGeneration.ts` is absent from current main, while the current keybindings store still owns the lifecycle (`appwire-client/typescript/keybindingsStore.ts`). | **Existing open PR #1841; serial after #2024.** Keep cohesive unless queue pressure requires the optional P8-A/P8-B split (`decomposition-audit-2026-09-19.md:286-308`). |
| 3a | **D6 P10a: stabilize transcript-display wire contracts.** Publish the shared rejection-payload helper and make default decoders forward-compatible, preserving malformed/foreign rejection behavior. Shared package, with current web transcript store and keybindings as consumers. | Existing local slice `codex/transcript-display-wire-contracts` at `68244fb400d17c7d4fd72696d4a311770022af7afa`, based on the immutable p8 tip. It has local qualification receipt 2615 and an independent PASS; it still needs current-head restacking/merge. | **Locally qualified, unmerged.** Can follow P8 and does not semantically require P9 (`decomposition-audit-2026-09-19.md:310-335`). |
| 3b | **D6 P10b: add the package transcript-display read-generation store.** Move support/loading/error/confirmed defaults, ready-generation fencing, notification/relayed-change handling, revision filtering, and refresh lifecycle into a real package store. | The intended module `appwire-client/typescript/transcriptDisplayStore.ts` is absent from current main. No P10b branch/PR exists in the local refs. | **Truly unstarted after P10a/P8.** It is the first real package consumer of the read APIs and can be prepared before P9 (`decomposition-audit-2026-09-19.md:337-363`). |
| 3c | **D6 P10c: add direct PATCH reconciliation to that same package store.** Preserve conflict, malformed response, superseded/fenced reply, preview contradiction, lower/equal revision, and post-apply durable-failure behavior. | The package module is absent on current main. The complete behavior remains in the web monolith at `cmd/evener-hub/frontend/src/stores/transcriptDisplay.ts:537-664`; its existing tests cover the write permutations, including the post-apply and stale-generation cases (`cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts:600-681`). | **Truly unstarted after P10b.** Do not create a temporary second store; grow the package store once (`decomposition-audit-2026-09-19.md:365-382`). |
| 4 | **D6 p9: extract checkpointed draft editor primitives and adopt them in keybindings.** Shared package contract; the small native projection that strips the web-only generation field remains a native consumer tail. | `claude/sdk-d6-p9-checkpointed-draft-editor` at `a50ac1a955481ff0f7d97011919f88b7f8cfafff`; PR #1844. `checkpointedDraftEditor.ts` is absent from current main. | **Existing open PR #1844; after P8.** Required before transcript draft adoption (A8), but not a semantic prerequisite for P10a/b/c. |
| 5 | **Stack A1: publish the p8/p9/p10 package surface.** Root exports, build file list, qualification smoke, and package tests for `settingsHubGeneration`, `checkpointedDraftEditor`, and `transcriptDisplayStore`. | Current `appwire-client/typescript/tsconfig.build.json:77-78,120-122` lists only transcript config/projector and the older draft port; it lacks all three modules. Current `appwire-client/typescript/index.ts:138-139,440-480` likewise has no exports for them. | **Truly unstarted.** Do after the three package APIs exist; the manifest is a shared merge hotspot (`tiny-stacks-plan.md:5-6`). |
| 6 | **Stack A2/A3/A4: split web-only local persistence, browser cross-tab sync, and view transitions.** Preserve storage warnings, legacy dual-write/migration, BroadcastChannel/storage fallback, and capture/restore transition behavior. | All code is still in the web monolith: local keys/storage at `transcriptDisplay.ts:39-50,164-211`; cross-tab protocol at `:80-86,213-332`; transition calculation/publication at `:121-153`. No `stores/transcriptDisplay/localStore.ts`, `crossTabSync.ts`, or `transitions.ts` exists on current main. | **Truly unstarted web-only work.** Retain the plan's serial A1 -> A2 -> A3 -> A4 -> A5 order; use the Stack A oracles (`tiny-stacks-plan.md:7-10`). |
| 7 | **Stack A5: replace the monolithic web hub half with the package store and retain the existing Zustand adapter surface.** It must remain a real `StoreApi`, keep current nine-field consumer shape, and preserve settings/session test seams. | The current web store still owns support/client lifecycle and the full hub state machine at `transcriptDisplay.ts:334-474,476-665`; `initTranscriptDisplay`/reset/hooks remain at `:695-751`. There are 14 current web importers of `transcriptDisplayStore`, `initTranscriptDisplay`, reset, or `useTranscriptDisplayStore`, including `Session`, `SessionChrome`, transcript panes, settings transcript, AppShell, and dev harnesses. | **Truly unstarted web adapter migration.** It follows P10c and A1-A4. Keep the import paths/API stable while moving implementation; do not make each pane import package internals. Plan and oracle: `tiny-stacks-plan.md:5,10`. |
| 8 | **Stack A6 behavior reconciliation.** Preserve the four measured differences while accepting the package/web adapter: fenced PATCH replies, support-flap retirement/reload, missed-notification refetch, and preview-base contradiction handling. | The retained plan records the four (`tiny-stacks-plan.md:11`). The HANDOFF has already settled the user-visible difference: a superseded settings write resolves with the hub's current value, matching the web (`HANDOFF-2026-09-18.md:224-233`). The package decomposition assigns the remaining read/write lifecycle and preview cases to P10b/P10c (`decomposition-audit-2026-09-19.md:337-374`). | **Not a new Jesse decision gate.** P10c must preserve the settled current-value result for fenced/superseded writes; P10b/P10c/A5 must retain tests for the other three implementation contracts. No new product question is needed. |
| 9 | **Stack A7/A8/A9: remove dead web ready callback; add transcript checkpointed drafts to the package; then restore the web draft/gate adapter.** A8 is shared package behavior; A9 is web-only. It must expose `draft`, `draftConflict`, `draftError`, `draftUnreadable`, and `storageUnavailable`, and retain the `saving`/`writeUncertain` gate. | Current web state has only `drafts` as a per-layout PATCH preview (`transcriptDisplay.ts:61-78,555-563`); it has no offline transcript draft repository/actions. The current package has the generic `draftCheckpointPort`, but no checkpointed transcript editor module. | **Truly unstarted.** A7 after A5; A8 after P9 and the package transcript store; A9 after A8. A10's native `transcriptMobile` projection is separate and does not count toward web completion (`tiny-stacks-plan.md:12-13`). |

### A6 disposition

The four differences have an existing disposition and do not create a new
product decision gate:

1. **Fenced/superseded PATCH reply:** settled by the HANDOFF ruling to resolve
   with the hub's current value, matching the web. P10c must retain that
   result rather than the package's earlier throw.
2. **Support flap:** the package retirement on `unsupported` and a fresh
   begin/reload on recovery remain the P10b read-lifecycle contract.
3. **Missed notification:** the package's loaded/first-payload tracking and
   one follow-up refresh remain the P10b notification/refresh contract.
4. **Preview contradiction:** `previewBases`/`contradictsPreview` remain the
   P10c direct-write reconciliation contract.

These are implementation and test obligations already captured by the
decomposition/oracles, not questions to reopen with Jesse.

## What is already complete for the web boundary

- D18 B1/B2 are in current main: `appwire-client/typescript/threadUsage.ts` is
  present and `detailsAccounting.ts` re-exports `sessionTokens` and
  `turnUsageTokens` (`cmd/evener-hub/frontend/src/panes/session/chrome/detailsAccounting.ts:5-9`).
  The remaining #1919 history merge is a shared/mobile consumer obligation and
  #1920/B3 is native usage adoption; neither is a missing web import/store lift.
- D25e C1-C5 and their follow-ups are in current main (`state/mutation` has
  `pendingEntries`, `pendingTurns`, `projection`, `commitFeed`,
  `projectionWork`, and `submission`). D25d durable native rows/host wiring are
  explicitly native. The retained plan marks the web remainder as D25e and
  D25d as native (`tiny-stacks-plan.md:3,21-26`).
- D28's web connection lift is already merged; #1922 is the native reconnect
  half. No current web `src/protocol` deep imports remain in the checked frontend
  tree, so a new broad import rewrite would be scope churn rather than a gap.

## Dependency order for closing web first

1. Merge/restack #1841 p8 on the exact #2024 tip.
2. Requalify/merge the already locally qualified P10a slice on the resulting
   p8 base; do not treat its immutable p8 base as current-main evidence until
   restacked.
3. Run P10b then P10c as one package-store lineage. P9 #1844 may proceed in
   parallel with P10a/b/c, but must land before A8 transcript draft adoption.
4. Publish the package surface (A1) once the APIs being published are present.
5. Run A2, A3, and A4 in the retained serial Stack A order, then wire A5's Zustand web adapter while preserving the 14
   consumer import contracts and the existing transcript/settings/session
   oracles. Apply the A6 behavior ruling during this step.
6. Finish A7, A8, and A9. Leave A10 and the D18/D23/D24/D25d/D28 native tails
   for the separate native completion lane.

This preserves the full migration goal. It only separates the package/web work
that can close first from native consumers that cannot be evidence for web
completion.
