# x-client-duplication: logic implemented more than once across the TS clients

Slice: `cmd/evener-hub/frontend/src` (web), `mobile-native/src` (phone), `mobile/src` (phone store core), `appwire-client/typescript` (shared package, `@evener/appwire-client`).
Audit date: 2026-10-25. Read-only; nothing run beyond grep/wc/node one-liners.

## Overview and inventory

| Tree | Files (ts/tsx) | Non-test LOC |
|---|---|---|
| `cmd/evener-hub/frontend/src` | 1280 | ~130,900 |
| `mobile-native/src` | 838 | ~74,600 |
| `mobile/src` | 12 | ~5,900 (conversation.ts alone 3,721) |
| `appwire-client/typescript` | 324 | ~47,100 (types.gen.ts 5,338, reducer.ts 5,168) |

Test code across the four trees is ~515,000 lines. Largest single test file: `mobile/src/state/conversation.test.ts`, 20,457 lines.

How the package is used today: the store-factory pattern works well. `createDisclosureStore`, `createArchivedListStore`, `createHubOverviewStore`, `createConnectionStore`, `createKeybindingsStore` and `createAskDockStore` are consumed by both clients through thin bindings, and the send/queue gate (`sendQueueGate.ts`) is a good example of a decision both clients import. The problem is the layer around those seams. Decisions that need a *rule* (state to attention class, liveness, durations, which methods are composer verbs, which statuses are shut down) are usually exported as raw pieces, and each client then writes its own composition, often with a comment saying "mirrors the web's X" or "the web does the same". I counted comment-declared mirrors in at least 20 places (`mobile-native/src/markdownSegments.ts:10`, `session/trayLine.ts:70`, `board/projectTree.ts:6`, `session/sessionNotes.ts:57`, `hosts/hostsController.ts:3`, `questionAnswers.ts:245`, `mutationOutboxStorage.ts:6`, and so on). A mirror held by a comment is the thing the package exists to remove.

Method: enumerated top-level function and const names per tree, intersected them (web/native/package/mobile), then read each hit. Also grepped for status/threshold literals, backoff formulas, pass-through wrappers (perl multi-line match on `function f(a){return g(a)}`) and the package `index.ts` export list against client usage.

Things I checked and found fine (not findings): `disclosure` (native store is the intended thin binding), `displayFormat` token formatting, `sendQueueGate`, `activityDelegateState`/`shellJobState` reuse on web, `reasoningEffort.effortLabel` vs native `effortLabel` (same name, different job), `fileReferences` vs native `markdownFileReferences.ts` (shared parser, different render substrates), `lastLine` in `toolEvidence.ts` vs `board/attention.ts` (name collision only), `parseCounts` (native one is dev-only demo code).

---

## Findings

### x-client-duplication-1 (high): session state to attention class is re-derived in about eight places

The wire states (`active`, `awaiting`, `warning`, `restartRequired`, `errored`, `ended`, `closed`, `notLoaded`, `systemError`) are mapped to "needs you / working / failed / idle / shut down" by independent switch statements. The package owns `humanizeState`, `needsResponseRest` and `approvalWaiting` (`appwire-client/typescript/railSessionState.ts:21-60`) but none of the clients build their classification on them.

Locations and evidence:
- `cmd/evener-hub/frontend/src/shell/sessionStatus.ts:4-19` `cadenceStateFor`: awaiting/warning/restartRequired -> needs-you, errored -> failed.
- `cmd/evener-hub/frontend/src/panes/session/liveness.ts:30-45` `cadenceStateForStatus`: the same table over raw `ThreadStatus.type` (`systemError`, `closed`), with a comment that it "mirrors that normalization".
- `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts:373-376` `stateNeedsYou`: a third copy of the needs-you family.
- `cmd/evener-hub/frontend/src/notifications/attention.ts:6-18` `levelFromState`: a fourth.
- `mobile-native/src/board/attention.ts:68-105` `EXPLAINED`, `decisiveState`, `boardState`, plus `WORDS` at :59.
- `mobile-native/src/session/sessionState.ts:44-58` `sessionStateLine`: a sixth, whose comment says it "follows the Board's own attention order ... exactly, so the two surfaces never disagree".
- `mobile-native/src/board/boardStops.ts:121`: `resumeRequired || restartRequired || notLoaded` inline.
- Go side (`hubapi.StateWord`, `NeedsYouBand`, `promotedAttentionLevel`) is a seventh and is cited in comments in four of these files as the thing being matched.

Two real inconsistencies already exist: web's `cadenceStateFor` treats `warning` as needs-you unconditionally while native `boardState` lets a non-blocking warning yield to running subagents (`attention.ts:92-95`); and `humanizeState` returns "failed" for `errored` only, while sessionState also handles `systemError`.

Impact: any new state or a change to the precedence (approval vs question vs warning) needs 7 coordinated edits, each pinned by its own test file, and the comments show people already rely on memory to keep them aligned.

Fix plan:
1. Add `attentionOf(state, flags): AttentionClass` to `railSessionState.ts` (or a new `sessionAttention.ts`). Input: normalized wire state or `ThreadStatus.type` (one `normalizeWireState` mapping `closed->ended`, `systemError->errored`), `askPending`, `approvalPending`, `runningSubagents`. Output: one enum (`failed | question | needsYou | approval | warning | restartNeeded | working | idle | shutDown`) which is what native `BoardState` already is.
2. Port the native table (it is the most complete and has the tests that encode the hub's order) into the package with its existing tests, then delete `decisiveState/boardState` bodies in native and make them thin callers.
3. Replace `cadenceStateFor`, `cadenceStateForStatus`, `stateNeedsYou`, `levelFromState`, `sessionStateLine`'s branching with a one-line map from the enum to Cadence/notification levels. Ask Jesse before unifying the warning-vs-subagents difference (it is a product decision).
4. Update `docs/product/subsystems.md` S05 row and `docs/product/session-activity.md` to name the single owner.

Size: ~120 lines added to package, ~250 lines removed across clients. Risk: medium (behavior is pinned by many tests; do the web mapping swap and native swap in separate PRs, each behavior-preserving, with the warning decision called out).

### x-client-duplication-2 (high): liveness / "quiet" / "stuck" policy has two implementations with different thresholds

Evidence:
- Web: `panes/session/transcript/flow/liveness.ts:28-29` `QUIET_THRESHOLD_MS = 20_000`, `STALL_THRESHOLD_MS = 180_000`; `describeLiveness` (:166-192), `formatRetryWait`, `formatSubagentCount`, `formatQuietBucket`, `formatExactGap`.
- Native: `session/trayLine.ts:27` declares `export const STUCK_AFTER_MS = 10 * 60_000`, the same name and value the package already exports (`appwire-client/typescript/sessionActivity.ts:11`, re-exported at `index.ts:577`). `trayLine.ts:66-98` re-implements describeLiveness: retry wins over silence, `attempt >= 2` bypasses the quiet gate (comment at :72-78 cites "the web's retryKnownEarly"), waiting-on-subagents text, stuck text. `retryText` (:111) rewrites `formatRetryWait` ("attempt 3 of 11" vs web's "attempt 3/11").
- Package: `sessionActivity.ts:8-11` `QUIET_AFTER_MS = 3 min`, `STUCK_AFTER_MS = 10 min`; `delegateDetails.ts:52` `DELEGATE_QUIET_AFTER_MS = 20_000`.
- Native `board/attention.ts:339` aliases `AGENT_QUIET_AFTER_MS = DELEGATE_QUIET_AFTER_MS`.

So the same concept has four constants meaning "20 seconds" or "3 minutes" or "10 minutes": the web session pane flips to "May be stalled" at 180s while the phone tray flips to "May be stuck" at 600s for the same session (`describeLiveness` line 191-192 vs `trayLine.ts:93`). The Board (package `quietState`) uses 3/10 minutes. Whether the web/phone difference is intended is not recorded anywhere I could find.

Impact: the same stalled session reads differently on the two clients and on the Board; retry wording drifts.

Fix plan:
1. Move the pure policy into the package as `describeLiveness(gapMs, active, runningSubagents, retry)` returning `{ level, kind, parts }` (structured, no copy), with the thresholds as the package's single constants. Web keeps its phrasing in `liveness.ts`, native keeps its in `trayLine.ts`, both switching on `level`.
2. Delete native `STUCK_AFTER_MS` and import the package's. Delete `AGENT_QUIET_AFTER_MS` alias or point it at one named constant. Reconcile web 180s vs package 3 min by importing `QUIET_AFTER_MS`.
3. Decide with Jesse which stall threshold wins (3 vs 10 minutes) before the behavior change; add a red-first test for whichever side changes.
4. Docs: session-activity.md names the thresholds.

Size: ~90 added, ~150 removed. Risk: medium (user-visible thresholds).

### x-client-duplication-3 (medium): at least a dozen duration / relative-time / clock formatters, with visible drift

Evidence (all bucket seconds into s/m/h/d with slightly different rules):
- Package: `selectors.ts:30` `relativeAge` (floors; "now"/m/h/d), `taskListTime.ts:8` `relativeTime` (rounds; "5m ago"), `displayFormat.ts:28` `formatDurationMs`, `:76` `formatElapsed` (`1m05s`/`2h03m`), `:55,:65` `formatClockTime`/`formatClockTimeSeconds` (the latter has no caller anywhere), `toolCallText.ts:69` `formatToolDuration` (alias of `formatDurationMs`), `watchRows.ts:41,63` `humanizeSeconds` and `humanizeInterval` (two 15-line functions identical except the "in "/"every " prefix), `taskListTime.ts:21` absolute time via `Intl`.
- Web: `chrome/activityFormat.ts:50` `formatQuietAge` (same algorithm as native `durationIn`), `chrome/statusFormat.ts:42` `formatWorkDuration`, `messages/reasoningFormat.ts:97` `formatThoughtDuration` (copy of `formatDurationMs`; differs for 0 -> "0ms" and for ~9.96s it prints "10.0s" where `formatDurationMs` prints "10s"), `widgets/loader/index.tsx:43` a private `formatElapsed` (mm:ss) that shadows the package name, `widgets/timestamp/index.tsx:57` `relativeLabel` (rounds, adds weeks), `tools/jobWatch.tsx:414` `formatCreatedDate`, `chrome/detailsAccounting.ts:15` `toLocaleString`, `EntityRef.tsx:68` `elapsedBetween`, `flow/liveness.ts:41,53` quiet bucket and exact gap.
- Native: `session/format.ts:16-31` `durationIn`/`compactDuration`/`spokenDuration`, `timeAgo`, `hub/PluginsPage.tsx:769` `agoText`, `board/attention.ts:323` `durationLabel` which calls the package `relativeAge(new Date(0).toISOString(), forMs)` and the comment admits it "anchors both ends to the epoch" to reuse the buckets.

Concrete drift: a 90-minute-old item reads "1h" in the rail (`relativeAge` floors), "2h ago" in the tasks panel (`relativeTime` rounds) and "2h ago" in the transcript timestamp (`relativeLabel` rounds); the phone uses `durationIn` (floor) so shows "1h".

Fix plan:
1. In `displayFormat.ts`, add one `durationParts(ms)` returning `{count, unit}` (the native `durationIn`), and build `compactDuration`, `relativeAge`, `formatQuietAge`, `spokenDuration` and `agoText` on it. Pick floor vs round once (floor, matching the rail and phone) and let `relativeTime`/`relativeLabel` use it.
2. `formatThoughtDuration` -> `formatDurationMs` (confirm the "0ms" and "10.0s" differences with a red test first, then drop them).
3. Merge `humanizeSeconds`/`humanizeInterval` into one `humanizeSeconds(total, prefix)`.
4. Delete `formatClockTimeSeconds` (no caller) and the `durationLabel` epoch hack.
5. Tests: one table-driven test in the package that fixes the bucket boundaries; client tests keep only the copy that is theirs.

Size: ~-200 / +80 lines. Risk: low to medium (visible text changes at rounding boundaries; list them in the PR).

### x-client-duplication-4 (high): the phone's conversation store re-derives reducer internals

`mobile/src/state/conversation.ts` (3,721 lines, test 20,457 lines) wraps the package reducer, but carries its own merge rules next to it:
- `:124-139` `SNAPSHOT_AUTHORITY_FIELDS` is the same 14-field list as `appwire-client/typescript/reducer.ts:3484-3499` `ITEM_OMISSION_TOLERANT_FIELDS`; the reducer's own comment (`:3479`) says "the same list the mobile store's rehydrate reconciliation already carries". Two lists that must stay equal.
- `:149-167` `THREAD_SCALAR_ONLY_METHODS` is a hand-copied denylist "read straight off applyNotificationToThread's switch" (comment :142-146). A new scalar-only notification added to the reducer without updating this set makes the phone treat it as a transcript gap and force a reread.
- `:772-880` `withdrawOmittedFullTurns`, `reconcileCrossTurnReissues`, `applyCrossTurnFallback` handle "an item reissued under another turn". The reducer has a per-turn `mergeHistory`; web handles reissues separately in `TranscriptBody.tsx`/`transcriptRepository.ts`. Phone-only merge semantics mean the two clients can show different transcripts for the same frames.
- `createDrainScheduler` (:272-376) is a hand-rolled keyed coalescing queue; web has its own retry/serial machinery in `threads.ts`.

Impact: transcript correctness rules live in a 3,700-line closure that only the phone runs; any reducer change must be mirrored by reading this file.

Fix plan:
1. Export `ITEM_OMISSION_TOLERANT_FIELDS` from the reducer and delete the phone copy (3-line change, safe, do first).
2. Export the scalar-only set from the reducer (or a `notificationTouchesTranscript(method)` predicate next to `applyNotificationToThread`) and delete the phone copy; add a reducer test that fails when a switch case is added without classification.
3. Move the cross-turn reissue and omitted-full-turn reconciliation into the reducer (`reconcileHistoryReissues`) with the phone's tests moved alongside; then check whether web's equivalent in `transcriptRepository.ts` can call it.
4. Split `conversation.test.ts` by concern as the logic leaves.

Size: step 1-2 ~40 lines; step 3 ~300 moved. Risk: low for 1-2, medium for 3 (needs the phone's existing behavior tests to follow the code).

### x-client-duplication-5 (medium): `mobile/` is native-only code in a pseudo-package with cyclic relative imports

Evidence: `mobile/` has no `package.json` and 12 files (`mobile/src/{services,state}`). 140 non-test and test files in `mobile-native/src` import it by relative path (`from "../../mobile/src/services/conversation"`), and `mobile/src/state/conversation.ts:71-85` imports back from `../../../mobile-native/src/projectedRows` (`projectedRows.ts:1-14` calls itself the "canonical row module" for the store). The docs call `mobile/src` "shared mobile services" (`docs/product/subsystems.md` S03 row) but nothing else consumes it. AGENTS.md requires package imports by name; the equivalent rule is not applied to this directory, and `projectedRows.reachability.test.ts` exists to police a deleted sibling (`mobile/src/conversation/`).

Impact: no ownership boundary; the store/projector cycle means neither can be tested or moved alone; "shared" in the docs is false.

Fix plan: do the cleanup in finding 4 first, then move `mobile/src/{state,services}` into `mobile-native/src/conversation/` (git mv, rewrite 140 imports mechanically, remove `mobile/`), break the cycle by moving `projectedRows` types the store needs into the same folder, update the S03 row and `mobile-native/README.md`. Only keep `mobile/` if a second consumer is planned (none found).

Size: ~0 net, ~170 touched import lines. Risk: low (mechanical), big diff.

### x-client-duplication-6 (medium): phone `ActivityStore` appears to be write-only

`mobile/src/state/activity.ts` (450 lines) and `mobile/src/services/activity.ts` (422 lines), with 2,814 lines of tests, build an `ActivityView` (`tasks`, `work: WorkEntry[]`, `usage`) from every thread read (`services/conversation.ts:678,870` call `projectActivity`) and patch it from live notifications. In `mobile-native/src`, `createActivityStore()` is called once (`session/nativeConversationSessions.ts:159`) and the instance is only used as the `activitySink` and `reset()` (:161, :297). I found no non-test reader of `.view`, `WorkEntry`, `WorkTone` or `TaskGroup` in `mobile-native/src`. The visible Activity screens read the package's `SessionActivityStore` (`subagents/subagentTree.ts:8,74`) and `subagentModel.ts`. This is a third activity projection next to `ActivityTree`/`buildActivityRows` and the native `flattenActivity`.

Caveat: `applyLiveNotification` returns `"rehydrate"` outcomes that the conversation store acts on (`conversation.ts:1514`), so removing it needs those triggers moved, not just the view.

Fix plan: confirm no reader with a type-check after deleting `view`/`projectActivity`; replace the sink with a small `shouldRehydrate(notification)` predicate (the only live behavior), delete `ActivityView`, `WorkEntry` and `projectJobEntry/projectDelegateEntry`, drop ~1,500 test lines that assert the view. Needs Jesse's go-ahead before deleting (it is a rewrite of existing work).

Size: ~-850 non-test, ~-2,800 test, +30. Risk: medium (rehydrate triggers).

### x-client-duplication-7 (medium): the two mutation-outbox adapters duplicate the transition rules and have already drifted

`cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts` (1,406 lines) and `mobile-native/src/mutationOutboxStorage.ts` (1,173 lines) both implement the package's `MutationOutboxStorage` port. A shared conformance suite exists (`*.conformance.test.ts`), and the package already extracted leaf decisions (`acceptedRecord`, `carriesOptimisticInput`, `canEditQueuedMessage`). The multi-step transition decisions are still written twice, and native's header says it "mirrors" the web's (`mutationOutboxStorage.ts:6-18`).

Observed drift in `settleReceipt`:
- Web `mutationOutboxIndexedDB.ts:675-676`: keeps an accepted copy when `projectionState === "pending" && (carriesOptimisticInput(source) || source.method === "notes/human/set")`. Native `mutationOutboxStorage.ts:553` only uses `carriesOptimisticInput(source)`.
- Web `:685` keeps a recovery row whose `compositionRevision > 0`; native `:572` deletes the recovery row unconditionally and has no `compositionRevision` at all (`grep` finds none), so the preserve-composition path exists only on web.

Fix plan: extract `planSettleReceipt(source, rows, projectionState): {putOptimistic?, deleteOutbox, deleteRecovery, deleteOptimistic}` and the same for `settleApplied`, `markUnknown`, `restoreProvenAbsent`, `nextDispatchable` blocking, as pure functions in `state/mutation/`; each adapter only applies the plan in its own transaction. Add the conformance cases for the two drifts above (red first) and decide with Jesse whether native should gain note retention and composition revisions.

Size: ~+250 package, ~-400 across adapters. Risk: medium-high (durable-state code; conformance suite is the safety net).

### x-client-duplication-8 (medium): the composer verb to wire method mapping exists in seven places

Evidence:
- `threads.ts:4144-4149` `COMPOSER_ROUTE_METHODS` (web, send/queue/steer/drain).
- `mobile-native/src/nativeMutationRuntime.ts:93-99` `methodFor` and `:104-109` `DIRECT_FALLBACK_METHODS` (same four literals; the web comment at `threads.ts:4611` says its set is "DERIVED ... so the builder and this set cannot drift", native hard-codes it).
- Package: `state/mutation/queuedMessages.ts:36-42` `REMOVABLE_MESSAGE_METHODS`, `pendingEntries.ts:46-52` `pendingMethod` and `:60-66` `PENDING_TURN_METHODS`, `dispatcher.ts:305-315` `RETRY_SAFE_MUTATION_METHODS`.
- `MutationRecoveryPanel.tsx:108` also names two of them.

Fix plan: add `state/mutation/composerMethods.ts` with `COMPOSER_METHODS = { send:"turn/start", queue:"turn/queue", steer:"turn/steer", drain:"turn/drainAsSteer", promote:"turn/promoteQueuedAsSteer" }` plus the derived sets (`PENDING_TURN_METHODS`, `DIRECT_FALLBACK_METHODS`, `REMOVABLE_MESSAGE_METHODS`) and `pendingMethod`; web and native import it. Keep `RETRY_SAFE` as an explicit list that extends them. A test asserts every `COMPOSER_METHODS` value is in `types.gen.ts` method names.

Size: ~+40, ~-50. Risk: low.

### x-client-duplication-9 (medium): the rail/project tree layout logic is duplicated between web and native

`mobile-native/src/board/projectTree.ts:6` says it "mirrors the web rail (railNodes.ts hostProjectNodes and projectNodesWithHostBranches)". Leaf helpers are shared (`orderedHosts`, `canonicalHostId`, `projectHostIds`, `CONTROLLER_SOURCE_ID` in `state/navigation/hostGrouping.ts`), but the layout decisions are written twice:
- grouping mode ("flat until a host besides this hub exists"): native `projectTree.ts:79-81` `grouping`; web `Rail.tsx` `displaySources.some(id !== "local")`.
- the host that carries the project-level overflow: web `railNodes.ts:690-700` `overflowHost` via `canonicalHostId`; native `projectTree.ts:263-272` `hostCopies`/`canonical`/`archivedCarrier`.
- branches only when rows span more than one host: web `railNodes.ts:820-826` `hostBranchNodes`; native `projectTree.ts:200-206` `hosts.length > 1`.
- Today/Recent de-dupe by ref, pinned-first ordering, fold keys (`projectFold`, `hostFold`) are native-only copies of web `hostGroupId`/`hostBranchId`/`hostProjectCopyId` ids.

Fix plan: add `state/navigation/projectLayout.ts` with a pure `layoutProject({project, rows, sources, mode})` returning `{hostCopies:[{hostId, rows, carriesOverflow, carriesArchived}], branches}`; both trees map it to their node types (web keeps its identity caches around it). Move the web/native shared tests to the package.

Size: ~+150 package, ~-200 across clients. Risk: medium (web caches depend on identity; keep the layout function returning stable data and wrap, do not change cache keys).

### x-client-duplication-10 (medium): native re-derives the Activity row model that the package already builds

The package builds activity rows with `walkActivitySessions` / `buildActivityRows` and `activityDelegateState` (`activityRows.ts:303-356, 440-515`), consumed by web in `ActivityTree.tsx:693` and `activitybar/activityRows.tsx:59`. Native does not call any of them (grep: zero native references). It has its own tree walk `flattenActivity` (`subagents/subagentModel.ts:175-227`, with its own `seen` set and parent-title threading) and its own state rule `subagentState` (:74-86) that returns `running|failed|done` while the package returns `{active, failed, status}`.

The rules differ on purpose in one place: native deliberately ignores child failures ("the subagent's own outcome, never its children's (ruling 4)", :68-73) while `activityDelegateState` sets `failed: ownFailure || childFailed` (`activityRows.ts:313`). That is a product decision hidden in a duplicate, which is how divergence accumulates.

Fix plan: expose `delegateOwnState(delegate)` in the package (the native rule, which is what `subagentState` is) and keep `activityDelegateState` for rollup, with both documented in `docs/product/session-activity.md`. Rebuild `flattenActivity` over `walkActivitySessions` (export it) so ordering and de-duplication come from one walker. Ask Jesse whether web's rollup-failure should match native's ruling 4.

Size: ~+60 package, ~-120 native. Risk: medium (ordering of rows; native tests pin it).

### x-client-duplication-11 (medium): small behavior copies between web and native marked "keep in sync"

- Markdown mermaid splitting: `widgets/markdown/segments.ts:41-95` and `mobile-native/src/markdownSegments.ts:20-66` both define `MERMAID_FENCE`, `isMermaidCodeToken`, `mermaidText`, `FENCE_OPEN_LINE`, `FENCE_CLOSE_LINE`, `fenceTokenTerminated`, with headers saying "keep the two in sync (their bodies deliberately differ where noted)". The only difference I can see is a `\r\n` replace.
- Mermaid sanitize config: `widgets/mermaid/security.ts:15-21` and `mobile-native/src/mermaidPageContent.ts:29-35` are identical object literals; only `MERMAID_FORBID_TAGS` was extracted (to `forbidTags.ts`, a "zero-import leaf" imported by the native build script across trees).
- `canWriteHumanNote`: `stores/humanNoteDrafts.ts:370-376` and `mobile-native/src/session/sessionNotes.ts:59-65` are the same four-status test, while the package already owns `canReadSharedNotes` (`sharedNotesAvailability.ts:6`).
- Ask dock: `AskQuestionCard.tsx:71` and `mobile-native/src/session/askDockCopy.ts:23` both implement `orderedOptions` (recommended first); native `questionAnswers.ts:245-258` `seedQuestionAnswers` ("Match the web dock") is the same loop as `askDock.ts:327-333`, and `nextUnansweredQuestion` (:271-282) is `askDock.ts:162-173 nextUnansweredKey` on indices. The comment on `questionBatches.ts:5` says the store's rules "are the package's, not copied here", but these are copied. Web `AskDock.tsx:151 answerFor` repeats `askDock.ts:152`.

Fix plan: move each to the package as pure functions: `markdownFence.ts` (`isTerminatedMermaidToken(token)`), `mermaidSanitizeConfig` data module (or have the build script import the whole config from `forbidTags.ts` renamed `mermaidSecurity.ts`), `canWriteSharedNotes(model)` beside `canReadSharedNotes`, `orderedOptions` and `seedRecommended(questions, saved)` in `askShared.ts`/`askDock.ts` with `askDock.ts` using them. Delete the "keep in sync" comments.

Size: ~+90, ~-130. Risk: low.

### x-client-duplication-12 (low): host/constant parity held by comment

- `LOCAL_HOST = "local"` in `appwire-client/typescript/hostRouting.ts:20`, again in `mobile-native/src/hosts/liveCounts.ts:10`, and `CONTROLLER_SOURCE_ID = "local"` in `state/navigation/hostGrouping.ts:13`; 18 bare `"local"` comparisons remain in non-demo code (`board/rowActions.ts:48`, `session/sessionFacts.ts:50`, `organizationNavigation.ts:54,55,121`, `ProjectsScreen.tsx:626`, web `Rail.tsx:1007`, `SessionMenu.tsx:145`, `Spawn.tsx:479,1043`, `startThread.ts:62`, `rail/actions.ts:109`).
- Archive identity: web `Rail.tsx:1007 archiveSessionIdentity` (bare id for local, ref otherwise) and native `board/rowActions.ts:44-52 archiveTarget`, which cites the web function by name.
- `MODEL_LIST_CAP = 50` and the same filter-then-slice lines in `InstanceSheet.tsx:83,720-723` and `hub/ProvidersPage.tsx:69,878-882`, each with a comment pointing at the other.
- `HOST_POLL_MS = 2000` in `panes/settings/sections/hosts.tsx:60` and `mobile-native/src/hosts/hostsController.ts:19`, plus two separate poll loops ("as the web's hosts section does").
- `EMPTY_APPLIED_REMOVALS` in `marketplacesPlugins/index.tsx:33` and `hub/PluginsPage.tsx:83`.

Fix plan: one `LOCAL_HOST` (alias `CONTROLLER_SOURCE_ID` to it) and `isLocalHost` use at the call sites; `sessionArchiveIdentity(row)` in `state/navigation`; `MODEL_LIST_CAP` + `filterModels(models, query)` in `credentialLabels.ts`; `HOST_POLL_MS` in `hostMutations.ts` beside `HOST_GATE_TIMEOUT_MS`. Delete the cross-reference comments.

Size: ~+30, ~-40. Risk: low.

### x-client-duplication-13 (low): the same exponential backoff written six times

`1000 * 2 ** n` capped at 30s: `mobile-native/src/hubConnection.ts:26`, `appwire-client/typescript/sessionActivityStore.ts:1061`, `historyPaging.ts:144` (three slightly different cap shapes: `Math.min(5, ...)`, `Math.min(failures-1, 5)`, unbounded exponent). Web `threads.ts:1982-1996` and `:2012-2018` define two identical schedulers (500ms base, 15s cap) with identical install-for-tests seams, differing only in names.

Fix plan: `backoffDelayMs({failures, baseMs, maxMs})` in the package (e.g. `retryDelay.ts`); the two web schedulers become one `createBackoffScheduler()` instance each or share one. Keep reconnect jitter in `client.ts` out of scope.

Size: ~+20, ~-45. Risk: low.

### x-client-duplication-14 (low): pass-through wrappers that only rename another function

Perl multi-line scan for `function f(args){ return g(args); }` found 8 non-test hits that add no behavior:
- `appwire-client/typescript/reducer.ts:2531 prependOlderTurns -> mergeOlderItemPage`
- `activityRows.ts:95 watchName -> watchTitle` (9 uses)
- `spawnHarnessModels.ts:14 harnessSupportsPluginSelection -> harnessUsesEvenerModels`
- `toolCallText.ts formatToolDuration = formatDurationMs`
- web `stores/hostOps.ts:310 teardownRecoverRefusalAction -> teardownRetryRefusalAction`
- web `SkillEditor.tsx:143,148 sharedPrefixLength/sharedSuffixLength` wrapping the imported aliases `documentSharedPrefixLength/...`
- web `LaunchConfigForm.tsx:119 sameLayer -> sameJsonValue`, `keybindings.tsx:98 errorMessage -> storeErrorText`, `QueueStrip.tsx:178 pendingPreview -> pendingEntryPreview`, `EntityRef.tsx usageText -> formatUsagePair`
- `mobile-native/src/connection.ts:71 connectionTarget -> hubRPCURL`
- `dev/pairingEnvelope.ts:3,101` re-exports `MAX_TRANSFER_BYTES` and `crc32` from `@evener/appwire-client/pairing`, which itself defines `MAX_TRANSFER_BYTES = 1152`/`crc32` at `pairing.ts:214-215`.

Fix plan: delete each wrapper and call the target (keep the more descriptive name if the other has fewer callers); `prependOlderTurns` and `watchName` have the most callers, so rename in place. Pure mechanical changes covered by the existing tests.

Size: ~-70. Risk: low.

### x-client-duplication-15 (low): scattered micro-utilities

- `isRecord`-style guards: `reducer.ts:1373`, `memoryContext.ts:37`, `state/navigation/codec.ts:61` (three bodies differ only in style) next to the package's `isPlainObject` (`plainObject.ts:10`, whose header says it is "used across this package and by the apps"); web `roundTimingsView.ts:61` (accepts arrays), `AdvancedOptions.tsx:536`; plus ~12 inline `typeof x === "object" && x !== null && !Array.isArray(x)` (`systemEventCopy.ts:29`, `taskListStep.ts:87`, `watchRows.ts:7` and web `jobTools.tsx:32` `asJsonObject`, `delegateStatus.tsx:102`, `readerPosition.ts:382,410`).
- Ellipsis truncation: `displayFormat.ts firstLine`, `toolCallText.ts:17 clip`, `pendingEntries.ts:179 truncateForDisplay`, `launchSchema.ts:147`, web `ToolCallItem.tsx:56`, `reasoningFormat.ts:208`, `NotificationCard.tsx:158,269`, `AdvancedOptions.tsx:326`, native `ForkEditor.tsx:28`, `CommentSheet.tsx:83`, `reader/reviewMessage.ts:22`: ten variants of "cut at N and add an ellipsis", some by UTF-16 unit, some by code point.
- `escapeRegExp` (`styles/cssBlock.ts:36`, `steeringNotifications.ts:1006`), `utf8Length` (`projectedRows.ts:1396`, `state/navigation/codec.ts:79`), `createSecureUUID` wrapper binding (`stores/secureUUID.ts`) vs `state/mutation/secureUUID.ts`.

Fix plan: add `truncate(text, max, {by: "codePoint"})` and use `isPlainObject` everywhere the plain-object semantics are fine (JSON-derived data); keep the two documented exceptions (arrays accepted, cross-realm). Do it opportunistically as files are touched, not as a sweep.

Size: ~-100. Risk: low.

### x-client-duplication-16 (low): shared-session status sets stated five times despite a comment saying "one set for every client"

`submitRouting.ts:148` exports `SHUT_DOWN_STATUSES` with the comment "One set for every client, so none keeps its own copy". Copies: `mobile-native/src/session/sessionState.ts:29 SHUT_DOWN`, `humanNoteDrafts.ts:374` and `sessionNotes.ts:63` (the same set plus `restartRequired`), `ConversationBody.tsx:485 HELD_SURFACE_OFF_STATUSES` (identical to the notes set), `coldStart.tsx:7-8` thread/turn terminal sets, `DetailsPanel.tsx:76 isEndedStatus`, `exchangeClosers.ts:43`, `transcriptProjector.ts:159` and `useTranscriptScroll.ts:953` for turn failure.

Fix plan: export `isShutDownStatus`, `isRestartOrShutDown` and `isTurnFailed` from the package (these belong with finding 1's `attentionOf`), replace the literals, delete native `SHUT_DOWN`. Fold into finding 1's PR series.

Size: ~-40. Risk: low.

### x-client-duplication-17 (low): dead and test-only public exports from the package

`index.ts` re-exports 908 names. Cross-checking against all non-test client sources (`cmd/evener-hub/frontend/src`, `mobile-native/src`, `mobile/src`, `mobile-native/scripts`), 276 names are not used by any client; most are types, but these values have no consumer anywhere except their own tests or definition module: `formatClockTimeSeconds`, `isToolCallItemId`, `isToolResultItemId` (only `reducer.ts`), `mergeOlderItemPageWithFolds`, `canSteer`, `DAEMONLESS_BOOT_GENERATION`, `reconcileActivityState`, `configSummary`, `delegatePacket`, `cachedSessionRecord`, `isTranscriptIndexLockedError`, `jobOwnerSessionId`, `mergeTurnHistoryWithFolds`. `publicExports.test.ts` pins part of the surface, so removal has to go through it.

Fix plan: drop the unused names from `index.ts`, delete the ones with no use at all (`formatClockTimeSeconds`, `canSteer`), un-export the rest. Document the rule that the root barrel only holds names a client imports.

Size: ~-60. Risk: low.

---

## Recommended fix sequence for this slice

1. **Safe de-duplication first (findings 8, 11, 12, 14, 16, 17).** Each is a small, behavior-preserving move into the package with the existing tests; they also reduce the noise for the larger work. One PR per finding, in this order: 8 (composer methods), 14 (wrappers), 12 (constants), 11 (markdown/notes/ask), 16 + 17.
2. **Finding 4 steps 1-2** (export the reducer field list and scalar-only set). Three-line wins against silent drift.
3. **Finding 3** (duration/time formatters), then **finding 2** (liveness). Both need a product decision on rounding and stall thresholds; raise them with Jesse one at a time (stall threshold first).
4. **Finding 1** (attention class) as a series: package function and tests, native swap, web swap. Ask about the warning-vs-subagents rule before the web swap.
5. **Findings 9 and 10** (project layout, activity rows) after 1, since both rely on the settled attention vocabulary.
6. **Finding 7** (outbox transition plans) with the two red conformance cases first.
7. **Findings 6, 5 and 4 step 3** (delete the write-only activity store, then fold `mobile/` into `mobile-native`, then move reissue reconciliation into the reducer). These delete or move existing work, so get Jesse's explicit approval before each.
8. **Finding 13 and 15** opportunistically.

Docs to touch alongside: `docs/product/subsystems.md` S03 and S05 rows (single owners for attention, liveness, durations, composer methods, `mobile/` removal), `docs/product/session-activity.md` (delegate own-state vs rollup, thresholds), `appwire-client/typescript/README.md` (export list), `mobile-native/README.md`. Keep the friction punchlist untouched; none of these are product proposals except the three flagged decisions (stall threshold, warning vs subagents, ruling 4 child-failure rollup).
