# Tech-debt audit: web-panes-a

Slice: `cmd/evener-hub/frontend/src/panes/` first half by name: `doc/`, `session/`,
`sessionPanels/`, `settings/` (the second half is `spawn/`, `transcript/`,
`welcome/`, `zoom/`). Non-test code. All paths below are relative to
`cmd/evener-hub/frontend/src/panes/` unless they start with another root.

## Inventory

| Dir | TS/TSX non-test lines |
| --- | --- |
| `doc/` | 604 |
| `session/` | 31,560 |
| `sessionPanels/` | 92 |
| `settings/` | 17,225 |
| total | 49,481 across 244 files (+91 CSS modules, 7,181 lines) |

- Test code in the same dirs: 127,375 lines across 250 test files, about 2.6x production.
- Comment lines (`//`, `*`, `/*`) in non-test TS/TSX: 12,358 of 48,914 (25%). Per file:
  `useTranscriptScroll.ts` 37%, `ToolRow.tsx` 39%, `Composer.tsx` 29%, `pendingTurnsStore.ts` 24%.
- Largest files: `session/transcript/flow/useTranscriptScroll.ts` 2,449; `settings/sections/credentials/ProviderConnection.tsx` 2,131;
  `session/composer/Composer.tsx` 1,824; `settings/sections/hosts.tsx` 1,421; `session/transcript/ToolRow.tsx` 1,070;
  `credentials/InstanceSheet.tsx` 1,022; `composer/sourceState.ts` 892; `session/chrome/ActivityTree.tsx` 873; `composer/queue/QueueStrip.tsx` 868.
- Largest single functions (top-level `function` to next top-level `function`): `Composer` 1,633 lines, `useTranscriptScroll` 1,369,
  `SelectedConnection` (ProviderConnection) 1,658, `InstanceSheet` 804, `ToolRow` 552, `CredentialsSection` 613, `HostsSection` 360.
- 658 `requireClass(styles.x, "file.module.css", "x")` lines in the slice (1,297 repo-wide in tsx).
- 25 `biome-ignore lint/correctness/useExhaustiveDependencies` suppressions and 12 `noArrayIndexKey` in the slice.
- Method: inventory, read the central large files, grep for same-named and same-shaped helpers in the slice and across
  `src/`, `appwire-client/typescript` and `mobile-native/src`, checked export consumers by grep. No tests were run.
  Note: the main checkout and this worktree differ (composer files); every citation here was read in the worktree.

## Findings

### web-panes-a-1: Composer submit and QueueStrip drain duplicate one submit protocol (HIGH)

Locations:
- `session/composer/Composer.tsx:1027-1230` (`submitAction`, about 200 lines)
- `session/composer/queue/QueueStrip.tsx:405-505` (`handleDrain`)
- `session/composer/Composer.tsx:954-990` (`activateRecovery`), `QueueStrip.tsx:287-289,449,552`, `composer/sourceState.ts:490`

Evidence:
- Both paths implement the same sequence: acquire recovery operation, fence check, skill capability gate, command capability gate, capture
  Stop barrier, `submitWithPendingTracking`, either `resendRecoveryPendingTurn` or the store call, then map `refused`/`consumed` outcomes to toasts.
- The capability-gate strings are copied verbatim: `"Skill selections aren't supported on this session yet; your draft is kept"` at `Composer.tsx:1063` and `QueueStrip.tsx:429`;
  `"Command selections are not supported on this session; your draft is kept"` at `Composer.tsx:1068` and `QueueStrip.tsx:434`.
- `"This message changed in another tab. Refresh and try again."` appears 7 times in 4 files (`Composer.tsx:962,1190`, `QueueStrip.tsx:287,289,449,499,552`, `sourceState.ts:490`),
  plus `"already sent in another tab"` at `Composer.tsx:1192`, `QueueStrip.tsx:500`.
- Drift already exists: Composer gates on a hoisted `skillInputSupported` (`Composer.tsx:746`), QueueStrip re-reads `model?.capabilities.skillInput !== true` inline (`QueueStrip.tsx:427`).
  Both pass only `skillNames, commandNames` to `submitWithPendingTracking` while the store call carries `mentions` too, so the tracked record and the sent payload differ in the same way in both copies (a shared function would fix it once).
- `Composer.tsx:1013` comment says it "wraps ... the same way QueueStrip's own drain handler" does.
- The store's `send/queue/steer/drainAsSteer` take six positional args (`stores/threads.ts:7846,7894,7900,7939`); Composer fans them out with four copies of the same six-argument call (`Composer.tsx:1135-1149`).
- Test side: 50 test hits mention "another tab" (e.g. `Composer.test.tsx:3828`) and assert on the literal phrase, against `docs/developing-evener/testing.md` "Copy Is Not a Test Oracle".

Impact: recovery and obligation semantics (the most delicate code in the composer) are maintained in two hand-synchronized copies. A fix to one (for example a new refusal outcome) silently misses the other. Toast copy cannot change without editing seven sites and about 50 test lines.

Fix plan:
1. New `composer/submitComposerDraft.ts` exporting one function `submitDraft({kind, ref, snapshot, source, model, toasts})` that owns: capability gates, recovery barrier, resend-vs-store dispatch (a single `store[kind](ref, {text, attachments, skillNames, commandNames, mentions})` call, replacing the four copies), outcome-to-toast mapping.
2. Replace the string literals with named outcomes returned by the function (`"skill-unsupported" | "command-unsupported" | "changed-elsewhere" | "sent-elsewhere"`) and one `submitOutcomeToast(outcome)` table; the 7 sites call it.
3. `Composer.submitAction` and `QueueStrip.handleDrain` shrink to "snapshot inputs, call `submitDraft`, apply the clear-draft policy".
4. Rewrite the copy-asserting tests to assert the toast severity/outcome and draft retention (behavior), per the testing doc. Do this red-green: first add a test that drain and send reject identically for an unsupported skill, watch it pass for both, then refactor.
5. Docs: update the composer row in `docs/product/subsystems.md` only if the recovery owner changes (it should not).

Size: about 350 lines moved, about 120 net removed. Risk: medium-high (recovery paths); needs the existing recovery tests intact before moving code.

### web-panes-a-2: Composer is a 1,633-line component (HIGH)

Locations: `session/composer/Composer.tsx:192-1824`; helpers listed at `:808-1560` (`handleTextChange`, `commitSlashCompletion`, `restoreTextToComposer`, `activateRecovery`, `submitAction`, `handleBuiltinSubmit`, `handleFormSubmit`, `handleSteerClick`, `handleInterruptClick`, `handleKeyDown`, `handlePaste`, composition and blur handlers).

Evidence: one function component, 27 hook calls, 97 inner declarations, 546 comment lines (29%), many comments cite review rounds and dated decisions ("2026-08-14 decision", "kata ledger"). Eight files already exist beside it (`sourceState.ts`, `draft.ts`, `useAttachments.ts`, `askDock/`, `queue/`) so the split pattern is established; the render-time logic was not moved with them.

Impact: no unit can be read or tested without mounting the whole composer; every composer change is a merge-conflict magnet; `useExhaustiveDependencies` suppressions (2 in this file) hide stale-closure risk.

Fix plan: extract by seam, behavior-preserving, one extraction per commit, existing `Composer*.test.tsx` as the guard:
1. `useComposerSubmit` (after finding 1): submit/steer/interrupt/builtin handlers.
2. `useComposerRecovery`: `activateRecovery`, `restoreTextToComposer`, drain revision helpers.
3. `useComposerKeys`: `handleKeyDown`, paste, composition, blur.
4. Leave `Composer` as layout plus wiring (target under 500 lines).
Delete history narration while moving (see finding 9).

Size: about 1,300 lines moved, no new behavior. Risk: medium; refs captured by closures (`textRef`, `skillNamesRef`) must travel together, so pass one `composerRefs` object.

### web-panes-a-3: useTranscriptScroll.ts mixes three modules; the hook is 1,369 lines (HIGH)

Locations: `session/transcript/flow/useTranscriptScroll.ts` lines 125-490 (anchor capture/restore, focus metadata), 490-875 (`useTranscriptViewRegistration`, 378 lines), 925-1080 (turn counters and input-direction predicates), 1081-2449 (`useTranscriptScroll`); `flow/transcriptViewRegistry.ts:134`.

Evidence:
- 60 hook calls, 6 `useExhaustiveDependencies` suppressions, 908 comment lines (37%).
- `captureTranscriptView` is exported by both files with different signatures: `useTranscriptScroll.ts:316` (`(el, measure, measureAnchors, positioningRevision)`) and `transcriptViewRegistry.ts:134` (`(id)`).
- A registry module (`transcriptViewRegistry.ts`) already exists, yet `useTranscriptViewRegistration` lives in the scroll file.
- Comments cite removed material: `:945-951` "this rewrite's own TurnModel", "the w4 fix-round report's item-9 writeup", "contracts-transcript-scroll-liveness.md §5 line 114" (no such file in the repo).
- Pure predicates (`totalItemCount`, `isAttentionWorthy`, `isFailedTurn`, `failedTurnCount`, `:925-980`) sit among hooks and are only reachable through the hook's tests.

Impact: the most timing-sensitive UI code in the app cannot be read in one sitting; the name collision invites importing the wrong `captureTranscriptView`.

Fix plan:
1. Move lines 125-490 to `flow/viewCapture.ts` (pure DOM capture/restore), rename the element-level function `captureElementView`.
2. Move `useTranscriptViewRegistration` into `flow/useTranscriptViewRegistration.ts` next to `transcriptViewRegistry.ts`.
3. Move the predicates (925-1080) to `flow/scrollInput.ts` and `flow/threadCounts.ts`; test them directly.
4. Split the 1,369-line hook by concern already visible in its dependency arrays (follow-bottom, load-older, error-anchor, key handling) into sub-hooks that share one state ref object.
5. Replace the narrating comments with one sentence each of "why".
Keep all existing `useTranscriptScroll*.test.*` files as the guard; do steps 1-3 first (pure moves).

Size: about 2,400 lines relocated; deletions mostly comments (roughly 400). Risk: medium for step 4, low for 1-3.

### web-panes-a-4: ProviderConnection is one 1,658-line component driven by about 20 refs (HIGH)

Locations: `settings/sections/credentials/ProviderConnection.tsx:474-2131` (`SelectedConnection`), state at `:485-560`.

Evidence: 37 `use*` hook calls and about 20 `useRef` coordination cells (`phaseRef`, `operation`, `retryTimer`, `retryAttempt`, `retryRunRef`, `pendingRecovery`, `acquisitionSource`, `onConnectedRef`, `checkAnchor`, `draftClaim`, `draftValue`, `confirmedStore`, `adoptBaselineRef`, ...) plus 8 `useState` flags (`saved`, `storedAccess`, `configured`, `host`, `missingCredential`, `confirmationOwed`, `endpointClaim`, `settingsClaim`). The phases are an implicit state machine: `type Phase = "idle" | "saving" | "refreshing" | "checking" | "review" | "result"` (`:429`) is mirrored in `phaseRef` and `setPhaseState`. 12 error constants at `:58-70`.
Related files make the credentials stack 6,300 lines of production code: `CredentialsSection` (747), `InstanceSheet` (1,022), `instanceDialogs` (695), `CredentialsHostScope` (597), `oauthDialogs` (358), `instanceLanding` (295).

Impact: any change risks a stale-ref bug; `ProviderConnection.test.tsx` has to drive the whole flow. The guided connector and the full editor (finding 5) carry overlapping rules.

Fix plan:
1. Extract the phase machine into a pure reducer `connectionFlow.ts` (`state, event -> state, effects`) with the retry schedule (`RETRY_DELAYS_MS`) as data; unit test transitions directly (saving to checking to result, reconnect, destination moved).
2. Extract `useConnectionRetry` (timer, attempt count, `retryScheduled`) and `useDestinationClaim` (claim refs, `claimHolds`, `claimEndpointMoved`).
3. Leave the component as view plus wiring.
4. Move `DestinationClaim` helpers (`:220-245`) to `credentials/destinationClaim.ts`; reuse from finding 5.
Guard with the existing 2,131-line behavior test file; do not alter copy.

Size: about 1,200 lines moved, 200 to 300 removed. Risk: high (live credential flow); sequence after finding 5's tests exist.

### web-panes-a-5: Endpoint-changed guards are reimplemented four times across the credentials files (MEDIUM)

Locations: `ProviderConnection.tsx:58-70` (`ENDPOINT_MOVED_ERROR`, `CHANGED_ERROR`), `InstanceSheet.tsx:88-111` (`CHANGED_INSTANCE_ERROR`, `ENDPOINT_CHANGED_SAVE_ERROR`, `RENAME_PENDING_*`), `instanceDialogs.tsx:64-75` (`ENDPOINT_CHANGED_ERROR`, `ENDPOINT_VANISHED_ERROR`), `CredentialsSection.tsx:96` (`ENDPOINT_CHANGED_CONFIRM_ERROR`); identity helpers `ProviderConnection.tsx:177-262` (`destination`, `accessIdentity`, `claimOf`), `InstanceSheet.tsx:140-172` (`draftIdentity`, `draftIdentityWithoutEndpoint`).

Evidence: four near-identical sentences ("This connection/instance changed to a different endpoint ... enter/review ... again") each with its own `FormError` reason union (`ConnectionErrorReason`, `AddInstanceErrorReason`, `CredentialValueErrorReason`, `FormErrorReason`). Each surface independently computes "has the destination I was authored against moved" from `endpointFingerprint`.

Impact: the policy "a write is asserted against the endpoint the user reviewed" is four implementations; the shared client already owns `instances.ts` (credential state) and `credentialLabels.ts`, and mobile has `mobile-native/src/providerForm.ts` and `ProviderEditor.tsx` with its own copies of the same assertion.

Fix plan: define `endpointAssertion` in `appwire-client/typescript/state/credentials/` (claim capture, `claimHolds`, `moved(listing)`), returning typed outcomes; web surfaces map outcomes to their own copy via one `credentials/endpointCopy.ts` table. Delete the four local identity helpers. Tests assert the outcome (`moved`, `vanished`, `holds`), not wording. Update the credentials row in `docs/product/subsystems.md`.

Size: about 250 lines added to shared, about 400 removed across web (more once mobile adopts it). Risk: medium; mobile adoption can follow separately.

### web-panes-a-6: renderContext is delivered both by prop and by React context; ToolCallItem has Legacy and Provider variants (MEDIUM)

Locations: `session/transcript/ToolCallItem.tsx:455-468` (`ProviderToolCallItem`, `LegacyToolCallItem`, outer `ToolCallItem` switch), `transcript/types.ts:30-33,100`, `TurnBlock.tsx:64,139,151,187,234`, `ToolRunGroup.tsx:45,49,84`, `TranscriptBody.tsx:447,464`; 7 other components read it only through `useTranscriptRenderContext()` (`ThinkBlock`, `AgentMarkdown`, `SystemNoticeItem`, `NotificationCard`, `TurnSeparator`, `subagentModule`, `delegateStatus`).

Evidence: `types.ts` documents "Context is threaded for memoized/custom renderers that prefer props; the built-in renderers consume the same value through TranscriptRenderContext". `TurnBlock` and `ToolRunGroup` both do `renderContext ?? useTranscriptRenderContext()`. `ToolCallItem` is the only renderer that branches on the prop, creating two wrapper components whose only difference is where the value comes from, and it forces `prev.renderContext === next.renderContext` into `ignoringTurn`'s comparator (`types.ts:100`). 21 test lines reference the prop.

Impact: two ways to supply the same value means callers can disagree (prop shadows context) and the memo comparator has to know about both.

Fix plan: delete the prop, keep the context (consumers already use it). `ToolCallItem` becomes one component calling `useTranscriptRenderContext()`; memo comparison already re-renders on context change. Remove `renderContext` from `ItemRenderProps`, `TurnBlockProps`, `ToolRunGroupProps`, and the `renderContext ?? ...` lines. Add a test that a context change re-renders a memoized tool row (the one behavior the prop existed for), then delete the prop.

Size: about 70 lines removed. Risk: low-medium (memo behavior; guard test first).

### web-panes-a-7: Duration and timestamp helpers are re-invented per file (MEDIUM)

Locations:
- Formatters: `appwire-client/typescript/displayFormat.ts:28` `formatDurationMs`, `:76` `formatElapsed`; `session/chrome/activityFormat.ts:50` `formatQuietAge`; `session/chrome/statusFormat.ts:42` `formatWorkDuration`; `session/transcript/flow/liveness.ts:41,53` `formatQuietBucket`, `formatExactGap`; `session/transcript/messages/reasoningFormat.ts:97` `formatThoughtDuration`.
- Parsers: `session/chrome/ActivityTree.tsx:146` and `ActivityRowDetail.tsx:199` (`parseMillis`, byte-identical); `reasoningFormat.ts:46` `parseTimestamp`; `EntityRef.tsx:68` `elapsedBetween`; `reasoningFormat.ts:37` `elapsedMilliseconds`; job duration (`end - start` of parsed `startedAt`/`endedAt`) computed in `ActivityTree.tsx:197-201`, `ActivityRowDetail.tsx:230-232`, `EntityRef.tsx:68-73`.

Evidence: `formatThoughtDuration` is `formatDurationMs` with a `0 -> "0ms"` branch and different rounding order; `formatQuietAge` and `formatElapsed` differ only in whether minutes carry seconds; `statusFormat.ts:30-41` comment itself says it is "a SEPARATE convention from formatDurationMs". Six formatters for "how long".

Impact: the same quantity renders differently on adjacent surfaces (`1m05s` from `formatElapsed` in EntityRef, `1m` from `formatQuietAge` in the rail), and drift is only visible by eye. The shared client is meant to own shared derivations.

Fix plan: add `parseTimestampMs(value)` and `durationBetween(start, end)` to `displayFormat.ts`; delete both `parseMillis`, `elapsedBetween`, `elapsedMilliseconds` and use the shared helper (keep `reasoningFormat`'s strict ISO validation as an option only if a test needs it). Decide with Jesse which of the 6 formats are intentionally different (rail compact vs elapsed clock vs thought timer), record that in `docs/web-ui/design-system.md`, and fold the rest into `formatDurationMs` options. Unit-test the one shared parser; existing format tests keep behavior.

Size: about 80 lines removed, 30 added. Risk: low; the format decision needs a product call, so ask first.

### web-panes-a-8: The "wait until connected, then ensureThread" effect is copied five times (MEDIUM)

Locations: `session/ConversationBody.tsx:131-143`, `doc/DocPane.tsx:162-176`, `stores/useThreadModel.ts:10-26` (outside slice), `session/chrome/ActivityRowDetail.tsx:289-318` (`JobOutputPreview`), `settings/sections/useConnectedEffect.ts:36-60`.

Evidence: all five contain `let started=false; const tryStart = () => { if (started || connectionStore.getState().state !== "ready") return; started = true; ... }; tryStart(); const unsubscribe = connectionStore.subscribe(tryStart);`. The first three also share the `ensureThread(ref).catch(()=>{})` plus cleanup `releaseThread(ref)` body byte for byte. `useConnectedEffect.ts:12-17` states it exists so "every section here doesn't reimplement the tryStart/subscribe/unsubscribe dance by hand" and even says Session.tsx "hit this exact race first". It is scoped to settings and imports a launch-config hook.

Impact: a fix to handshake races has to be found in five places; `useThreadModel` already is the hook `ConversationBody` and `DocPane` need.

Fix plan: (a) `ConversationBody` and `DocPane` call `useThreadModel(ref)` (it returns the model they then read anyway), deleting 28 lines; (b) promote the generic part of `useConnectedEffect` to `src/hooks/useWhenConnected.ts`, leaving the host-attach/launch-config wiring in the settings file; (c) `JobOutputPreview` uses it. Test: one hook test for "starts immediately when ready", "starts on transition", "cancels on unmount".

Size: about 90 lines removed. Risk: low.

### web-panes-a-9: Comments cite documents and plans that no longer exist; 25% of lines are comments (MEDIUM)

Locations (examples): `session/transcript/toolRenderers.ts:128,237,273` ("legacy renderer-tools.js"), `flow/liveness.ts:35,49,134` ("parity doc §16"), `composer/draft.ts:1` ("parity-m5-composer.md §F"), `settings/sections/skillsDirs.tsx:2-4` ("parity-m7-settings.md §14"), `Settings.tsx:55-66` ("T1b ... wave-7 report", "Controller-merged, single-line-append discipline"), `sections/PlaceholderSection.tsx:9-16`, `flow/useTranscriptScrollKeys.ts:2` ("webui-keybindings-p3 Task 1"), `settings/sections/*:` "(component 07b)" 43 times.

Evidence: grep for `renderer-tools.js`, `renderer-format.js`, `parity-m*.md`, `contracts-*.md` returns no tracked file; 117 references to `parity`, `wave-N`, `kata`, `contracts-*.md` in non-test code; 95 comment lines mention "legacy"; 74 cite issue/PR/plan numbers; 58 say "previously/used to/no longer". CLAUDE rules: history belongs in the commit message.

Impact: readers chase nonexistent docs; real constraints (why) are buried in review-round narrative, which is what makes finding 2 to 4 files hard to refactor. Several comments are now false (finding 11).

Fix plan: mechanical pass per file while touching it (do not do a mass reformat commit): keep a one-to-three-line "why" per non-obvious block, drop provenance. Do it as part of the extractions in findings 1 to 4 rather than as a separate PR so the diff stays reviewable. If a comment encodes a real product rule, move it into `docs/product/` instead.

Size: about 3,000 comment lines removable across the slice. Risk: low.

### web-panes-a-10: Failure/terminal status vocabulary is redefined in the web instead of using the shared client (MEDIUM)

Locations: `session/transcript/tools/subagentModuleStore.ts:133-141` (`classifyJobStatus`), `session/chrome/activityFormat.ts:70-96` (`isFailedStatus`, `jobStatusDotState`), `session/coldStart.tsx:7` (`TURN_TERMINAL_STATUSES`), `session/transcript/tools/delegateStatus.tsx:384`; shared: `appwire-client/typescript/activityData.ts:31-43` (`isActivityFailure`).

Evidence: `classifyJobStatus` failed set is `failed, errored, error, exhausted, command_exited_nonzero, command_killed`; shared `isActivityFailure` has `failed, exhausted, error, command_exited_nonzero, command_killed` (no `errored`); `jobStatusDotState` has a third list of "working" statuses (`running, working, queued, starting, resuming`) while `classifyJobStatus` treats only `running` as running. The same job can be "failed" in one component and "unknown" in another.

Impact: drift between the transcript card, activity tree and phone for the same status; AGENTS.md says the shared client owns shared derivations.

Fix plan: add `classifyActivityStatus(status): "running"|"failed"|"stopped"|"done"|"unknown"` to `activityData.ts` implemented once, have `isActivityFailure` and `jobStatusDotState` derive from it, delete `classifyJobStatus`'s lists and `isFailedStatus`. Test table of status to class in the package (behavior), plus one web test per consumer that a `command_killed` row reads failed. Check mobile's `demoSubagents.ts`/status code adopts the same function.

Size: about 60 lines removed, 30 added. Risk: low-medium; `errored` and "queued" semantics need a quick check against Go job statuses before unifying.

### web-panes-a-11: hosts.tsx: 1,421 lines, duplicated replay toast and duplicated poll effect (MEDIUM)

Locations: `settings/sections/hosts.tsx:868-1040` (`DeployDialog`), `:1042-1165` (`RestartDialog`), `:1166-1388` (`RemnantRepairControls`), `:213-238` (two `setInterval` pollers); `settings/HostPicker.tsx:36-39`.

Evidence: `DeployDialog.submit` (`:874-888`) and `RestartDialog.finishRestart` (`:1056-1066`) contain the same block: read `operations[name]`, if `state !== "pending"` push "repeated its existing operation" info else push "started" success, then `onClose()`. Two component-level `setInterval(... hostsStore.refresh(), HOST_POLL_MS)` effects exist (`hosts.tsx:217` and `:228` region, plus `HostPicker.tsx:37`) with the same 8-line explanatory comment. `RestartDialog` has three handlers (`handleRestart`, `handleConnectAndRestart`, `handleReSeedRestart`) that only differ in the store method called. Six components in one file (`HostSheet`, `HostEntryDialog` 199 lines, `DeployDialog` 174, `RestartDialog` 124, `RemnantRepairControls` 222, `RemnantRepairDialog`).

Impact: two nearly identical dialogs to keep in step; poll cadence defined in a component, not in the store that needs fresh data.

Fix plan: (1) `hostOpsStore` gets a `startedToast(name, kind)` selector or the dialogs share `reportStartedOperation(name, label, toasts)`; (2) `useHostsPolling()` hook (or move polling into `hostsStore.startPolling()` with ref-counting) used by `HostsSection` and `HostPicker`; (3) wrap the busy/try/finally in `useBusyAction()`; (4) move the dialogs to `sections/hosts/` as separate files. Existing `hosts.test.tsx` guards.

Size: about 150 lines removed, file split into 5. Risk: low.

### web-panes-a-12: Dead production code kept alive by tests (LOW)

Locations: `session/chrome/sessionActions.ts` (26 lines, `lastUserMessageText`), `session/composer/skillDocument.ts:139-147` (`materializeSkillReferences`), `session/composer/queue/pendingTurnsStore.ts:439-442` (`useRetryPendingState`), `session/chrome/activityTime.tsx:66` (`taskActivityTime` exported, used only in-file).

Evidence: `grep -rw lastUserMessageText src/` returns only `sessionActions.ts` and `sessionActions.test.ts`; the module comment refers to a "SessionActionsMenu" that does not exist in `src/`. `materializeSkillReferences` is referenced only in `skillDocument.test.ts` (4 assertions); its sibling `materializeSelectionReferences` (`:150`) is the production version. `useRetryPendingState` is only in `pendingTurnsStore.test.ts` (tests at `:1416,1451`); its comment says it exists "for callers that only ask" but there are none. Many other exports exist only for tests (`resetAskDockStoreForTests`, `draftStorageKey`, `composerDraftStorageKey`) and add surface area without callers.

Impact: dead code plus the tests that pin it; the tests count as coverage but protect nothing.

Fix plan: delete the three symbols and their tests (`sessionActions.ts` plus `sessionActions.test.ts`; the two functions and their test blocks). Unexport helpers with no external consumers. Keep `useRetryPendingProjection`.

Size: about 140 lines removed including tests. Risk: very low (grep-verified). Deleting requires Jesse's OK per the "never throw away implementations" rule; these are unused so flag it in the PR.

### web-panes-a-13: Settings dispatch scaffolding: unused `sectionId` prop and an unreachable placeholder fallback (LOW)

Locations: `settings/Settings.tsx:55-66,150,244`, `settings/sections/PlaceholderSection.tsx`, props declared "Unused - kept so this component's signature matches" at `sections/agents.tsx:20`, `agentsDoc.tsx:34`, `project.tsx:45`, `hosts.tsx:86`, `launchServer.tsx:32`, `credentials/CredentialsSection.tsx:100`, forwarded through 5 `*HostScope` wrappers (`agentsDoc.tsx:237`, `project.tsx:233`, `launchServer.tsx:209`, `CredentialsHostScope.tsx:61-72`).

Evidence: `SECTION_COMPONENTS` has an entry for all 16 ids in `SETTINGS_SECTIONS` plus `project`; `resolveSettingsSection` already maps unknown ids to the default, so `?? PlaceholderSection` is unreachable for valid input. Its doc comment says "Settings.tsx currently renders this unconditionally for every section id" (false). Only `PlaceholderSection` reads `sectionId`. The map type `ComponentType<{ sectionId: string }>` forces seven components to declare a prop they ignore.

Impact: noise, and misleading comments that say sections are unbuilt.

Fix plan: type the map as `ComponentType`, drop `sectionId` from the seven components and their wrappers, delete `PlaceholderSection` (and `settings/sections/PlaceholderSection.test.tsx`) and the `??` fallback; rewrite the `SECTION_COMPONENTS` comment to one line. Update tests that pass `sectionId`.

Size: about 90 lines removed. Risk: low.

### web-panes-a-14: Overloaded `useStore` selector wrapper boilerplate (LOW, cross-cutting)

Locations in slice: `session/composer/askDock/askDockStore.ts:21-30`, `settings/sections/sessionCacheRow.tsx:74-80`, `settings/sections/marketplacesPlugins/hostStore.tsx:32-40`; repo-wide 18 occurrences of `selector ? useStore(...) : useStore(...)` with `biome-ignore lint/correctness/useHookAtTopLevel` (e.g. `stores/connection.ts:200`, `stores/threads.ts`, `shell/workspace.ts`).

Evidence: each copy is three overload signatures, a four-line comment pointing at `stores/connection.ts` for the explanation, a lint suppression, and a ternary calling the same hook in both arms.

Fix plan: one helper in `stores/useBoundStore.ts`: `export function bindStore<S>(store: StoreApi<S>) { function useBound(): S; function useBound<T>(sel: (s: S) => T): T; function useBound(sel?) { return useStore(store, sel ?? identity) } return useBound }` (zustand's `useStore` accepts an identity default, so no conditional hook call and no suppression). Replace the 18 sites. Verify with `make test-web` typecheck. Note `hostStore.tsx` takes the store from React context, so it keeps a per-render form but can use the same `sel ?? identity` trick without the ternary.

Size: about 18 x 12 lines removed, 15 added. Risk: low (type-only plus mechanical).

### web-panes-a-15: Cross-client model-list filter and cap, copy-pasted with a cross-reference comment (LOW)

Locations: `settings/sections/credentials/InstanceSheet.tsx:81-83,719-723,899`; `mobile-native/src/hub/ProvidersPage.tsx:69,874-882,921`.

Evidence: both define `MODEL_LIST_CAP = 50` and the same `models.filter(m => m.id.toLowerCase().includes(query)).slice(0, CAP)`. Each comment points at the other ("the phone makes the same decision (ProvidersPage's MODEL_LIST_CAP)" / "the web sheet makes the same decision (InstanceSheet's MODEL_LIST_CAP)"), which is a manual sync contract.

Fix plan: `visibleModels(models, query, cap?)` returning `{shown, total, needsSearch}` in `appwire-client/typescript/credentialLabels.ts` (or the credentials state module); both clients call it; package unit test pins cap and case-insensitive matching.

Size: about 20 lines added, 20 removed. Risk: low.

### web-panes-a-16: Pass-through wrappers and re-export hops (LOW)

Locations:
- `settings/sections/keybindings.tsx:98-100` `errorMessage(error)` returns `storeErrorText(error)`; it is the only name used in that file for the call.
- `session/transcript/EntityRef.tsx:76-78` `usageText(usage)` returns `formatUsagePair(usage)`.
- `session/chrome/activityFormat.ts:100-101` re-exports `jobStatusDisplay` from `@evener/appwire-client` "to keep the web importers on their existing path" (3 importers: `EntityRef.tsx:22`, `jobTools.tsx:20`, `ActivityRowDetail.tsx:40`).
- `useAskDockPending` is defined in `composer/askDock/askDockStore.ts:37` and re-exported by `askDock/AskDock.tsx:141`, `askDock/index.ts:11`, and `composer/askDockPending.ts:10`; `AskDock.tsx:45` and `index.ts:3` document it as three separate "seams".
- `Host*Scope` wrappers (`agentsDoc.tsx:237`, `project.tsx:233`, `launchServer.tsx:209`, `skillsDirs.tsx:29`, `mcp.tsx:309`) are each a one-line `HostScopedSurface` call that could be a `hostScoped(Component)` factory used in `SECTION_COMPONENTS`.

Fix plan: inline the wrappers, import `jobStatusDisplay` from the package at the three sites and delete the re-export, keep one `useAskDockPending` import path (the store file; the lean-chunk reason survives because the store is already separate from `AskDock.tsx`), add `withHostScope()` in `hostScopedSurface.tsx`. Delete the matching explanatory comments.

Size: about 60 lines removed. Risk: low; check the chunk guard (`connectDialogChunkGuard.test.mjs`-style scripts) still passes for the askDock import graph.

### web-panes-a-17: Compat and transition code kept without an end date (LOW)

Locations: `session/composer/draft.ts:25-26,54-56,125,173-174,185,245-246` (v1 plain-text draft key read, double-delete), `session/transcript/ToolRow.tsx:170,548-551,960-967` ("legacy single-level mode" when `onToggleSummary` is absent), `session/transcript/tools/delegateStatus.tsx:91,296` ("legacy transcript edge case" defensive reads).

Evidence: `draft.ts` header says the v1 key "held plain text only; see readComposerDraft for the approved transition", and every write path removes both keys. `ToolRow` is used from a single file (`ToolCallItem.tsx:368,408`); the bodyless call site passes `expandable={false}` and no `onToggleSummary`, so the "legacy" mode is really "row without a body", mislabelled. 72 test render sites exercise both modes.

Impact: low per item, but each is a dormant branch with its own tests.

Fix plan: do not delete without Jesse's approval (compat rule). Propose: (a) set a removal condition for the v1 draft key and drop the read once one release has shipped; (b) rename ToolRow's modes to `bodyless`/`expandable` and delete the word "legacy"; (c) none for delegateStatus beyond comment cleanup.

Size: about 40 lines. Risk: low once approved.

### web-panes-a-18: `requireClass` table boilerplate (LOW, cross-cutting)

Locations: 658 lines in the slice (`const CLASS = { x: requireClass(styles.x, "File.module.css", "x"), ... }`), 1,297 repo-wide; definition `widgets/internal/requireClass.ts`.

Evidence: each entry repeats the key twice and the module filename as a string. A scan found no mismatched label or key today (1,235 checked), so this is maintenance cost, not live drift. The documented reason is `noUncheckedIndexedAccess` making `styles.x` possibly undefined.

Fix plan: raise with Jesse before changing a design-system pattern. A `classMap(styles, "File.module.css", ["root","note"] as const)` helper returning `Record<K,string>` would turn 658 lines into about 150 and remove the filename-typo class of bug; alternatively generate `.module.css.d.ts` typings so `styles.x` is `string`. Not worth doing unless approved.

Size: about 500 lines removed. Risk: low, but a pattern decision.

## Recommended fix sequence

1. Findings 12 and 13 first: pure deletions of unreachable code (about 230 lines), no behavior change, shrinks what later steps move.
2. Finding 14 and 16: mechanical, type-checked cleanups (`make test-web` typecheck is the guard).
3. Finding 8 (shared connected-start hook) then finding 6 (drop the `renderContext` prop): small, test-first, remove two classes of duplicate wiring.
4. Finding 10 and 7 (shared status vocabulary, shared duration/timestamp parsers) in `appwire-client`: ask Jesse which duration formats are intentional, then land with package unit tests; follow with finding 15 and the web side of finding 5.
5. Finding 1 (unified composer submit protocol) before finding 2 (decompose `Composer`), so the split moves one implementation instead of two.
6. Finding 3 pure moves (view capture, registration hook, predicates) then the hook split.
7. Finding 5 (shared endpoint assertion) before finding 4 (decompose `ProviderConnection`), so the flow reducer extraction uses the shared claim helper.
8. Finding 11 (hosts dialogs) whenever convenient; independent.
9. Finding 9 rides along with every extraction above (delete provenance comments in code you move). Findings 17 and 18 need Jesse's decision first.

Doc updates to ship with the code: `docs/product/subsystems.md` rows for composer recovery ownership (1, 2), credentials (4, 5), and `docs/web-ui/design-system.md` for the chosen duration formats (7). Tests that currently assert literal toast and error copy (finding 1 has 50 hits; roughly 150 phrase-like `getByText`/`toContain` matches across the slice's tests) should be rewritten to assert outcomes as each area is touched, per `docs/developing-evener/testing.md`.
