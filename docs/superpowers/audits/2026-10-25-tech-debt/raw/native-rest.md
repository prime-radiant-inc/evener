# Tech-debt audit: native-rest

Slice: `mobile-native/src/*` except `session/` and `board/` (reader, hub, subagents, newSession, sheet, alerts, share, hubs, design, hosts, outbox, display, providers, dev, loose root files) plus non-test `mobile-native/scripts`.
Worktree: `.worktrees/debt-audit` (origin/main at a6c0b9c561). Read-only audit; no tests run.

## Inventory

- 629 files in scope. Non-test source in `src/` (whole tree, incl. session/board): about 54.8k lines. Test, testkit and test-util files in scope: about 102k lines (roughly 2:1 test to source for the slice).
- `src/` root has 314 files, 145 of them non-test: screens, editors, repositories and helpers share one flat directory.
- Largest non-test files: `screens.tsx` 4031, `reader/markdownFileReferences.ts` 2642, `dev/demoFleet.ts` 1705, `projectedRows.ts` 1655, `dev/demoSessions.ts` 1420, `mutationOutboxStorage.ts` 1173, `scripts/demo-hub.mts` 1020, `draftDocument.ts` 984, `sheet/Grouped.tsx` 975, `nativeMutationRuntime.ts` 971, `hub/ProvidersPage.tsx` 966.
- Largest tests: `ConversationScreen.send.test.tsx` 7195 lines (253 cases), `dev/demoFleet.test.ts` 1261, `markdownFileReferences.test.ts` 1106.
- `src/dev` (fake-hub fixtures used only by `scripts/demo-hub.mts`): 5,000 lines of source plus 2,300 of tests.
- Scripts: `demo-hub.mts` 1020, `check-markdown-file-links.mts` 868, `check-script-imports.mts` 114, `auth-harness-smoke.mts` 163, `build-mermaid-page.mts` 96, `launch-settings-smoke.mts` 72, `plugin-fixture.mts` 64, `check-hub.mts` 29, two Ruby iOS distribution scripts, one C++ probe.
- Cross-package coupling: 83 imports across 74 non-test files reach `../../mobile/src/...`; 8 files import `cmd/evener-hub/frontend/src/...` by relative path.
- Provenance comments: 328 `spec N` / `ruling N` references in 136 files, plus 85 `D24-x` / `Review Focus N` / dated-attribution references in 43 files (slice excluding session and board).

## Findings

### native-rest-1 (high) ConversationScreenView is a 3,600-line single function

Locations: `mobile-native/src/screens.tsx:413-4031` (function body to the `return (` at 3551, JSX 3551-4031), imports 1-240.

Evidence: `ConversationScreenView` starts at line 413 and the file's only other top-level closing brace is line 4031. The file imports from about 90 modules. 155 `useState/useRef/useEffect/useCallback/useMemo` calls, 36 `useEffect`s, about 66 inline `const x = useCallback/async` handlers. It contains an inline class component (`ReaderCell`, line 3410), composer command submission, steer/queue handling, find-in-session, question/approval docks, reader-position restore, offline send, fork, notes, subagent panel and toast wiring. `docs/product/subsystems.md` S03 links this file as "Native screens". Five test files (10.6k lines, `ConversationScreen.send.test.tsx` alone 7195) exist mostly because every behavior can only be reached through the whole screen.

Impact: any change to the conversation screen risks unrelated behavior; reviewers cannot hold the function in their head; tests must mount the whole screen and grow without bound. High maintenance drag and the most likely place for accreted drift.

Fix plan: behavior-preserving extraction, guarded by the existing five ConversationScreen test files (run on CI, per repo rule). Split into custom hooks and child components inside `src/screens/` (or `src/conversation/`): `useComposerSubmit` (send/steer/queue/commands, STEER_* constants at 150-160), `useReaderAnchor` (readerPosition restore, `ReaderCell`), `useFindInSession`, `useQuestionAnswers`, `useSessionMenuActions`, `useNotesBar`, then the JSX list header/footer pieces. One extraction per PR, deleting the moved code from `screens.tsx`; no new abstractions beyond hooks. Once a hook owns a behavior, move its tests from the 7k-line file to a hook-level test file. Update the S03 subsystem row links in `docs/product/subsystems.md` and mobile-native README to the new files.

Size: about 3,600 lines moved, roughly net zero plus 150 for hook signatures. Risk: medium (React effect ordering and memoization; `ConversationScreen.memoization.test.tsx` guards).

### native-rest-2 (high) Native app imports web frontend source by relative path

Locations: `mobile-native/src/ansiOutputStyles.ts:6`, `AnsiOutputLine.tsx:2`, `AnsiOutputList.tsx:6`, `session/StepEvidence.tsx:8` (all `.../cmd/evener-hub/frontend/src/widgets/codeblock/ansi`), `newSession/sheetPlugins.ts:19` and `newSession/newSessionContext.tsx:11` (`.../frontend/src/panes/spawn/usePluginPreview`), `scripts/build-mermaid-page.mts:18` (`widgets/mermaid/forbidTags`), `metro.config.js:27` whitelisting the frontend src dir. Sources: `cmd/evener-hub/frontend/src/widgets/codeblock/ansi.ts` (579 lines), `panes/spawn/usePluginPreview.ts` (110), `widgets/mermaid/forbidTags.ts` (29).

Evidence: AGENTS.md says shared derivations live in `@evener/appwire-client` and every consumer imports it by name; `make lint-package-imports` guards only that package. Here the ANSI parser, a React hook with a client call, and the mermaid forbid list are shared with web by reaching into the web app tree. `ansiOutputStyles.ts` also holds its own ANSI palette (`ANSI_COLORS`, lines 15-50) beside the web's CSS variables and its own `colorValue` (line 56) next to the web's `ansiLine.tsx:15`.

Impact: a web refactor (moving a widget, adding a CSS-module import to `ansi.ts`) silently breaks the phone build; Metro has to be configured to compile a second app's tree; layering is inverted (app depends on app).

Fix plan: move `ansi.ts` (+ its test), `usePluginPreview` core (pure state part; keep the React binding thin or move the hook to the package if it has no DOM dependency) and `forbidTags` into `appwire-client/typescript`, export via `package.json` exports, repoint both apps by package name, delete the relative imports and the Metro whitelist entry. Extend `lint-package-imports` (or add a grep rule) to fail on `cmd/evener-hub` paths from `mobile-native/src` and `scripts`. Update `appwire-client/typescript/README.md` export list.

Size: about 720 lines moved, 15 import edits, 20 lines of lint rule. Risk: low-medium (module move, tests move with files).

### native-rest-3 (medium) Layering inversion: shared `mobile/src` imports `mobile-native/src` and vice versa

Locations: `mobile/src/services/conversation.ts:40-41` (imports `MobileConversation`, `projectConversation` from `mobile-native/src/projectedRows`), `mobile/src/services/conversation.test.ts:36`; 83 imports in 74 native files from `../../mobile/src/...` (e.g. `screens.tsx:55-56`, `hubModels.ts:2`, `newSession.ts:14`, `launchPaths.ts:2`).

Evidence: `mobile/src` contains `services/` (activity, conversation 1016 lines, newSession) and `state/` (conversation 3721 lines, activity 450, conversationMutation 133) and is consumed only by `mobile-native` (it is "shared" with no other client; `rg` finds no other consumer outside docs and the package-import audit). The state module depends on the row projection that lives in the app's `projectedRows.ts`, which is itself documented as the "canonical row module" (header lines 1-30).

Impact: two directories that must move together but are not one package; cyclic dependency between them; the name `mobile` implies a second client that does not exist. Contributes to the "where does this live" confusion across the 145 flat root files.

Fix plan: needs Jesse's decision on direction. Simplest: fold `mobile/src` into `mobile-native/src` (a `git mv` plus about 83 import rewrites, and update `plugins/shared-sources.gradle`, `biome.jsonc`, `package.json`, `scripts/hooks/pre-commit`, `packageimportpaths_audit_test.go`, `docs/product/subsystems.md` S03 row). Alternative: keep `mobile/src` as the portable layer and move `projectedRows.ts` into it so the dependency runs one way.

Size: about 6,000 lines moved mechanically, 100 edits. Risk: low logically, wide diff; do as its own PR with no behavior change.

### native-rest-4 (medium) Reader markdown file-link module is 80% a vendored entity table

Locations: `mobile-native/src/reader/markdownFileReferences.ts:516-2642` (`namedEntities`), logic at 1-515 and `entityText` at 131-138.

Evidence: file is 2,642 lines; the object literal `namedEntities` starts at 516 and closes at 2642 (2,127 lines, one entry per line, WHATWG named character references). The comment says it is kept local "to avoid a browser DOM, a new dependency or renderer changes". The actual link-extraction logic (about 480 lines) is buried above it. `marked` is already a dependency (`package.json` `"marked": "18.0.6"`).

Impact: the real module is hard to read and review; one file hides a data table the diff tools collapse badly; any change to the lexer logic scrolls past 2k lines of data.

Fix plan: move the table verbatim to `reader/namedEntities.ts` (data only, no logic) and import it; add a test that asserts a sample of well-known entries and that every value is a string (no phrase assertions). Optional follow-up, requires approval because it adds a dependency: replace by the `entities` package already in many lockfiles. Do the zero-risk split first.

Size: 2,130 lines moved, 3 lines changed. Risk: very low.

### native-rest-5 (medium) Durable "checkpoint" repositories each re-implement the same validators and compare-and-swap shell

Locations: `forkCheckpointRepository.ts:19-35` (`valid`, `record`, `text`) and `:62-113`; `navigationActionRepository.ts:36-62` (`NavigationActionBackend`, `valid`, `record`, `keys`, `text`), `:146-192`; `pinAssignmentDrafts.ts:3-16` (`PinAssignmentDraftBackend`, `valid`, `clone`); `pinSectionDrafts.ts:7-12`; `sessionDeletionResult.ts:8-11` (`record`); `newSession/launchMemory.ts:11` (`record`); `nativePreferenceDrafts.ts:112-140` (`NativePreferenceDraftBackend`); `nativeOrganization.ts` (the implementer).

Evidence: `createId(): string; get; set; deleteIf` is declared three times (`navigationActionRepository.ts:39-42`, `pinAssignmentDrafts.ts:7-10`, `nativePreferenceDrafts.ts:112-122`). `valid(condition): asserts condition` with a per-file error string is written five times; `record(value)` four times (two wrap `isPlainObject`, `forkCheckpointRepository.ts:22` re-checks `typeof/Array.isArray` by hand); `text(value)` ("non-blank string") twice; `clone` via `JSON.parse(JSON.stringify())` three times (`navigationActionRepository.ts:147`, `pinAssignmentDrafts.ts:15`, `newSession.ts:264`); `equal` via `JSON.stringify` (key-order sensitive) in `forkCheckpointRepository.ts:60` and `navigationActionRepository.ts:150`, while the shared package already exports `sameJsonValue`/`canonicalJson` (`appwire-client/typescript/plainObject.ts`) and `nativePreferenceDrafts.ts` uses `canonicalJson`. `pinAssignmentDrafts.ts:23-35` and `pinSectionDrafts.ts:12-19` hand-check `Object.keys(record).length === 2`. Each repository repeats the begin / acknowledge / finish (removeIf) load-validate-compare shape.

Impact: three equality semantics (JSON.stringify order, canonicalJson, `===` on identity) for the same durable recovery records; a validator fix in one file does not reach the others; the next checkpoint feature copies a sixth variant.

Fix plan: one small `durableRecord.ts` module in `src/` exporting `DurableRecordBackend` (the single interface), `invalid(message)`/`valid`, `plainRecord(value, required, optional)`, `nonBlank(value)` and `sameRecord` (built on `sameJsonValue`). Replace the local copies file by file, behavior preserved by each file's existing `*.test.ts` and `draftBackend.testkit.ts`. Add one test for the shared module. Do not touch stored shapes.

Size: about 120 lines added, 200 removed. Risk: low-medium (equality change from JSON.stringify to canonical may accept key-reordered records; add a test pinning that).

### native-rest-6 (medium) SQLite outbox adapter: hand-rolled ALTER TABLE ladder and a second copy of the outbox state machine

Locations: `mutationOutboxStorage.ts:195-290` (schema + five migration blocks), `:497` (`#seedLegacyAttemptCount`), whole file 1,173 lines; web twin `cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts` (1,406 lines); port in `appwire-client/typescript/state/mutation/outbox.ts` (428) and `records.ts` (269).

Evidence: the constructor repeats `PRAGMA table_info(...)` then `ALTER TABLE ... ADD COLUMN` five times (`stop_epoch`, `recovery_resends`, `instance_id`/`composer_mentions`/`draft_capture_token`, `unresolved_attempts`, `stop_epoch_at_enqueue`, `accepted_at` with backfill), each with a 6-10 line "a database created before X" comment. The native app's first commit is 2026-09-11, 44 days ago. File header (lines 1-20) states it is a mirror of the web adapter's contracts ("Oracle: ...mutationOutbox.test.ts"). Settlement semantics (`settleReceipt` 545, `settleApplied` 579, `discardSupersededNoteRecovery`, `restoreProvenAbsent` 983, `nextDispatchable` 961) exist in both adapters; only a conformance suite keeps them aligned.

Impact: ~2,600 lines of storage adapters implement one state machine twice; behavior fixes (the file cites #4342 and a web "fused identity fix") must be made twice; schema history is encoded as migration code for installs that are weeks old.

Fix plan: (1) Decision for Jesse: if no shipped install predates the current schema, delete the migration ladder and keep one `CREATE TABLE` with all columns (no backward-compat without approval; this removes about 90 lines and `#seedLegacyAttemptCount`). If installs exist, collapse the five blocks into one `ensureColumns(table, [{name, ddl}])` helper (about 20 lines). (2) Longer: lift the pure transition decisions (which table a record belongs to after receipt/applied/unknown, note-supersede rule, prune cutoff) into `appwire-client/state/mutation` as functions over plain records, leaving each adapter only row IO; conformance suite (`testing/mutationOutboxStorageConformance.ts`) stays as the guard.

Size: (1) -90 to -70 lines; (2) about 600 lines moved, large. Risk: (1) low once the install question is answered; (2) high, do only after (1).

### native-rest-7 (medium) Parallel model-picker stacks and three picker components with the same name

Locations: `ModelPicker.tsx` (297 lines, `ModelPicker`, `ChoiceRow`), `modelPickerEntries.ts` (62; own `dollars()` and `modelSections`), `newSession/ModelPicker.tsx` (52, imports the root one as `ModelList`), `LaunchModelPicker.tsx` (137), `hubModels.ts` (85), `session/ModelSheet.tsx`; shared `appwire-client/typescript/modelCatalogPickerRows.ts` and `modelCatalogView.ts` (`filterCatalog`, `formatCost`, `contextWindowLabel`, `toCatalogOptions`).

Evidence: `LaunchModelPicker.tsx:3,36` builds rows with the shared `buildPickerRows(state.catalog, query)`; `ModelPicker.tsx:11` builds sections with the native-only `modelSections(catalog, query, visionOnly)`. `modelPickerEntries.ts:15-17` reimplements price formatting (`dollars`) that `formatCost`/`trimCost` in `modelCatalogView.ts:74-80` already does, and reimplements the search filter and Recent grouping the shared `filterCatalog`/`PickerRow` provide. The shared header (modelCatalogPickerRows.ts:1-4) says the shared rows exist so "the web panel and native's LaunchModelPicker only map rows to markup". `HubModels` (a 75-line fetch/backoff store) exists only for LaunchModelPicker, while the new-session store separately holds `models`/`recentModels` (`newSession/ModelPicker.tsx:28-30`). Two components are literally named `ModelPicker`.

Impact: price and capability text can differ between the Session model sheet and Launch settings; a catalog change (diagnostics, vision filter) needs two code paths.

Fix plan: extend the shared `buildPickerRows` with the two native needs (vision-only filter, capability meta) if missing, switch `ModelPicker.tsx` to it, delete `modelPickerEntries.ts` and its test (keep behavior assertions on rows), rename `newSession/ModelPicker.tsx` to `NewSessionModelPicker.tsx`. Leave `HubModels` until the picker unification lands, then fold its refresh into the shared model-list read if one exists.

Size: about -150 / +40 lines. Risk: low-medium (`ModelPicker.tsx` render tests cover rows).

### native-rest-8 (medium) Compatibility and migration code for old on-device data

Locations: `nativePreferenceDrafts.ts:248-330` (`migrateLegacyTranscriptCheckpoint`, `legacyBytes` WeakMap, best-effort adoption), `questionAnswers.ts:51-130` (digest "eras", legacy question hashing, "twin" signatures), `draftDocument.ts:59-100` (`releasedRecoveryIds` legacy merge, `migrated`), `draftRepository.ts:205` (`parked-legacy-${index}`), `readerPosition.ts:14`, `reader/documentMemory.ts:85,158`, `creationDraftRepository.ts:6-7`, `newSession.ts:714-726` ("only an older build could set"), `screens.tsx:3153` (`kind: "queue:legacy-images"`), `hub/HubHome.tsx:36-63` (`legacyUpdate`), `mutationOutboxStorage.ts` (finding 6).

Evidence: `nativePreferenceDrafts.ts:248` "A transcript checkpoint the previous native implementation wrote ... stranding a saved draft on upgrade"; `questionAnswers.ts:73` "a draft saved by an older build holds the question object...". The app's first commit is 2026-09-11, so every "older build" is at most ten weeks old, and the TestFlight pipeline is recent.

Impact: each branch is dead for new installs and cannot be tested without hand-built legacy fixtures (`legacyBytes` exists only to survive a failed migration write). AGENTS and Jesse's rules require approval for backward-compat work; this is a standing amount of it.

Fix plan: ask Jesse one question per area whether any build in the wild still holds the old shape. Where none does, delete the legacy branch and its fixture tests (`nativePreferenceDrafts` migration first: about 80 lines plus the WeakMap plumbing in `nativeTranscriptDrafts`). Where some do, record the removal condition in the code comment so it can be deleted on a known release.

Size: about -250 lines across files. Risk: data-loss for stale installs if answered wrongly; hence the question first.

### native-rest-9 (medium) Three near-identical "gated action" handlers in the hub settings pages

Locations: `MarketplaceBrowser.tsx:374-384` (`act`), `hub/PluginsPage.tsx:545-559` (`act` with `success`), `hub/ProvidersPage.tsx:399-460+` (`act` with `secret`/`endpointAsserted`), plus `HubEditor.tsx:35-55` (`pending` ref submit guard). Pages are 829, 807 and 966 lines.

Evidence: MarketplaceBrowser and PluginsPage both do `const version = revision.current; const outcome = await runGatedMutation(canUseConnection, action); if (revision.current !== version) return; if (outcome === "not-ready") return; setError(null); refused -> "write-busy"; failed -> "write-failed"`. The same `ACTION_PROBLEMS` map (`write-busy`/`write-failed` copy) is declared at `PluginsPage.tsx:781-785` and needs the same copy in MarketplaceBrowser. Each carries a multi-line comment explaining the same "a not-ready press runs nothing" rule.

Impact: the outcome-to-error mapping and stale-version guard are maintained three times; the pages carry 800-1000 lines because every action path is inline.

Fix plan: add `useGatedAction(canUseConnection, versionRef)` returning `{ run, error, clear }` in `hub/` next to `runGatedMutation`, owning the version check, not-ready no-op and busy/failed mapping; migrate MarketplaceBrowser and PluginsPage first (identical shape), then ProvidersPage's simple branches. Move the shared error copy to one module. Tests: existing page tests plus one hook test for stale-version and not-ready.

Size: +50 / -90. Risk: low.

### native-rest-10 (low-medium) Hand-rolled retry timers and observable boilerplate repeated per store

Locations: retry: `outbox/outboxFlush.ts:340-349`, `navigationPages.ts:125-135`, `hubModels.ts:62-72`, `usePinNavigation.ts:125`, plus `board/useBoardReadRetry.ts`, `board/boardSearch.ts:89`, `session/nativeConversationSessions.ts:455` (all `setTimeout(..., reconnectDelay(n))` with their own failure counter). Observable: 38 `new Set<() => void>()` listener sets in non-test code, each with its own `subscribe`/`publish` (e.g. `hubModels.ts:20-33`, `hubPaths.ts:21-30`, `forkActions.ts:21-35`, `approvalControls.ts:18-27`, `providerSignIn.ts:66-79`, `sessionControls.ts:42-68`, `imageSelection.ts:128-136`, `hosts/hostsController.ts:38-68`).

Evidence: the shared package already ships `createFrameworkFreeStore` (`appwire-client/typescript/frameworkFreeStore.ts`), but native imports only zustand `createStore` once (`newSession.ts:1`). `singleFlight.ts` (67 lines) is native-only and the seven timers each re-derive "grow with failures, reset on success, stop when disposed".

Impact: each class has its own dispose/idempotency edge cases (e.g. `HubModels.publish` ignores disposed; others may not); more code to test per store.

Fix plan: (a) add `createBackoffRetry(read, {delay: reconnectDelay})` beside `singleFlight.ts` and migrate hubModels, usePinNavigation, navigationPages first; (b) add a 12-line `Emitter` (`subscribe`, `emit`) to `src/` and replace the 38 sets opportunistically when each file is touched. Do not rewrite all in one PR.

Size: +60 / -200 over several PRs. Risk: low.

### native-rest-11 (medium) Planning and review-cycle identifiers in source comments

Locations (samples): `projectedRows.ts:1-30,376-380,612` ("D24 end state", "D24-6", "operator's summary-only ruling (Sep-25)", "Jesse, 2026-09-27"), `newSession.ts:46,150` ("Review Focus 1/2"), `reader/documentSource.ts:2` ("Review Focus 5"), `nativeMutationRuntime.ts:188` ("Review Focus 4"), `display/displayPreferences.ts:16`, `TimelineItem.tsx:306`, `transcriptPresentation.ts:103,189,245`, `nativeOrganization.ts:9` ("D6 hardened"), `dev/demoFleet.ts:1-9` ("Appendix B"), `scripts/demo-hub.mts:75` ("The phase 6 screenshots' staging"), `haptics.ts:2` ("phase 3's ruling 5"), `mutationOutboxStorage.ts` ("#4342", commit hashes).

Evidence: counts above: 328 spec/ruling references in 136 files, 85 D-number / Review Focus / dated-attribution references in 43 files. The specs live under `docs/superpowers/specs`/`plans` (dated, per AGENTS "keep dated narratives out of these references"); `projectedRows.ts:9` narrates a deleted directory (`mobile/src/conversation/`). Code comments tied to "phase 3's" numbering cannot be resolved by a reader, and a comment such as "(spec 8.5)" points to a number in a plan that was not kept evergreen.

Impact: comments explain history rather than present constraints; they decay and mislead (`readerPosition.ts:14` "Absent on an anchor saved before this field existed").

Fix plan: mechanical pass per directory: keep the one-line "why" and drop the identifier; where the number was the only content, delete the comment. Never paste in new plan numbers. Add a grep check (`D[0-9]+-[0-9]`, `Review Focus`, `Jesse, 20`) to `lint` for `mobile-native/src` to stop regrowth.

Size: about 400 comment lines touched, 0 code. Risk: none; keep as a separate PR so behavior diffs stay empty.

### native-rest-12 (medium) Tests assert on product copy against the repo's testing rule

Locations: `QueueSheet.test.tsx` ("Couldn't take this message out of the queue."), `alerts/AlertBanner.test.tsx` ("Couldn't start the new session"), `newSession/ProjectPicker.test.tsx` ("Couldn't load this host's recent projects."), `newSession/ModelPicker.test.tsx` ("Couldn't load this host's models. The hub's default model still works."), `newSession/PluginChecklist.test.tsx` ("1 of 3 on. Plugins can't be changed after the session starts."), `subagents/StopSubagentSheet.test.tsx` (whole sentences), `TimelineItem.test.tsx` (`"Output clamped to fit the context window\nFree some context."`), `ConversationScreen.send.test.tsx` ("Couldn't send this · thread instance is stale"), `calmCopy.test.ts` (scans all production string literals for words).

Evidence: AGENTS.md "no test may check that copy, error messages, or other human-readable strings match a hardcoded phrase". Counts for the slice: 290 `renderedText(tree).toContain/toBe/toMatch(...)` assertions in 52 files; 111 occurrences of "Couldn't"/"Could not" in 46 test files; many of them assert the exact sentence rather than that an error state exists. `calmCopy.test.ts` is a lint rule dressed as a test and reads copy from `mobile/src` too.

Impact: copy edits fail tests; tests do not state the behavior (an error row is shown, retry is offered). Also `hub/PluginsPage.tsx:781-785`-style copy constants make it tempting to import them in tests.

Fix plan: replace phrase assertions with structural ones (testID, accessibilityRole, a `kind`/`reason` field already present on most models such as `setActionError("write-busy")`, button presence). Do it as each file is touched plus one sweep on the list above; convert `calmCopy.test.ts` to a Biome/lint rule or keep it explicitly under the "copy policy" label with a documented exemption in `docs/developing-evener/testing.md`.

Size: about 300 assertions to rewrite. Risk: low; coverage must not drop, so keep the case, change the oracle.

### native-rest-13 (medium) 7,195-line ConversationScreen send test file

Locations: `ConversationScreen.send.test.tsx` (7195 lines, 253 cases), `ConversationScreen.sheets.test.tsx` (2010), `renderNative.testkit.tsx` (874), `conversationPlatform.testkit.tsx`.

Evidence: one file holds a quarter of the slice's test volume; it exists because `screens.tsx` (finding 1) is only testable whole. Nearby behavior tests (`composerCommand.test.ts`, `queuedMessageControls.test.ts`) show the unit-level pattern works when logic is extracted.

Impact: slow to run in CI shards, hard to bisect, and test intent is lost in the volume.

Fix plan: split by behavior area during the finding-1 extractions (send, steer/queue, recovery, commands), moving each test with the hook it exercises; keep the whole-screen file for integration cases only. Do not delete cases (coverage rule).

Size: about 7k lines moved. Risk: low.

### native-rest-14 (medium) src/dev holds a 5,000-line fake hub that mirrors Go hub behavior

Locations: `src/dev/demoFleet.ts` (1705), `demoSessions.ts` (1420), `demoSetup.ts` (582), `demoSubagents.ts` (342), `demoSessionActivity.ts` (228), `demoOlderHistory.ts` (113), `scripts/demo-hub.mts` (1020), 2,300 lines of tests.

Evidence: comments cite Go files being mirrored: `demoFleet.ts:991-1639` ("as cmd/evener-hub/navigation_archived_list.go", "Mirrors cmd/evener-hub/app_navigation.go's navigationReadPage", "openAIInstanceStatusKeyed", "hubcore/tree.go"); `demoSetup.ts:223` pins a method list to `cmd/evener-hub/host_request_methods.txt` via test. Nothing in app code imports `src/dev`; only `scripts/demo-hub.mts` and its tests. The header (`demoFleet.ts:1-9`) says content is "transcribed" from a browser prototype fixture that is a separate `docs/design/mobile/redesign/prototype/data.js`.

Impact: a second implementation of pagination, invalidation, archived lists and auth status that must follow the Go hub by hand; any wire change breaks the demo silently unless a test catches it.

Fix plan: decide whether the demo hub is still used (screenshots, e2e). If yes, move `dev/` plus `demo-hub.mts` to `mobile-native/demo/` (outside `src/`, so `src` lint, `calmCopy` and Metro never see it) and add a conformance test that replays recorded Go fixtures against the demo handlers for the three paginated reads. If no, delete (about -7,000 lines). Ask Jesse which.

Size: -7k or move only. Risk: low.

### native-rest-15 (low) Test-only exports and dead helpers

Locations (defined, referenced only by tests): `subagents/subagentModel.ts` `subtreeStopped` (124), `subagentSections` (273), `tallyActivity` (283, "for dev fixtures"); `providers/providerStatus.ts:44` `authByProvider`; `nativePreferenceDrafts.ts:107` `sameDraftIdentity` (used by `draftBackend.testkit.ts` only); `newSession/shareSeed.ts:48` `retrySharedSeed`; `sheet/useScreenInFront.ts:25` `leaveScreen`; `reader/documentSource.ts:30` `refHost`; `nativeDocPort.ts:23` `nativeDocImageSource`; `timeline.ts` `isInterruptedNotice`; `hub/hubScreenSlot.tsx:141` `useProviderDetailSlotReader`; `ui.tsx:160` `WarningMessage`.

Evidence: for each, `rg -nw` over `src`, `../mobile`, `scripts` returns only the definition and tests (checked with a script excluding test files; `subagentSections` and `tallyActivity` have `own=1` references, i.e. none in their own file either). Roughly 40 other exports are used only inside their own file or tests (constants and types) and only need `export` removed.

Impact: tested-but-unused code reads as product behavior; the tests keep it alive.

Fix plan: delete each function and the tests that exist only for it; for `sameDraftIdentity` keep the testkit use by importing the internal `draftIdentityKey`. Remove `export` from file-local constants.

Size: about -150 lines. Risk: very low.

### native-rest-16 (low) Orphan one-off scripts with inconsistent formatting and wrong usage text

Locations: `scripts/auth-harness-smoke.mts` (163), `launch-settings-smoke.mts` (72), `plugin-fixture.mts` (64), `check-hub.mts` (29), `check-markdown-file-links.mts` (868) + `markdown-file-links-probe.cpp` (37).

Evidence: `rg` for these names finds no references in README, package.json, Makefile or CI except `check-hub.mts` (README:97) and `check-markdown-file-links.mts` (only in dated plans `2026-10-06-file-links-mvp-release.md:32`). `check-hub.mts:9` usage says `scripts/check-hub.ts` (file is `.mts`); its body mixes tab and two-space indentation (`createConversationService` block). `auth-harness-smoke.mts` uses two-space indentation while `demo-hub.mts` uses tabs. `check-markdown-file-links.mts` hard-pins `react-native-enriched-markdown` 1.0.2 and `marked` 18.0.6 and compiles C++ with `cc` on the dev machine.

Impact: scripts that nobody runs rot; the markdown one gates nothing yet asserts version equality that will break on the next dependency bump.

Fix plan: for each script ask "who runs this"; delete those with no owner. For the markdown probe, either wire it into the native test lane (CI) or delete it with the plan reference. Format the keepers with Biome and add a header comment with purpose and usage (per Jesse's script rule).

Size: -1,000 lines if deleted. Risk: low.

### native-rest-17 (low) Mirrored markdown segment splitter with "keep in sync" comments

Locations: `markdownSegments.ts` (105 lines; `FENCE_OPEN_LINE`, `FENCE_CLOSE_LINE`, `isMermaidCodeToken:23`, `mermaidText:55`, `fenceTokenTerminated:27`) vs `cmd/evener-hub/frontend/src/widgets/markdown/segments.ts:41,55,71` (530 lines).

Evidence: `markdownSegments.ts:12-14` "mirror the web sibling ... keep the two in sync"; the web file carries the matching note. Function names and logic are the same for the fence helpers; only the output shape differs (string sources vs token slices).

Impact: fence-closing rules fixed in one place drift in the other; the correct fix is the shared package, which would also resolve finding 2's pattern.

Fix plan: move `isMermaidCodeToken`, `mermaidText`, `fenceTokenTerminated` and the two fence regexes into `appwire-client` (e.g. `markdownFences.ts`), import from both, delete the copies and sync comments, keep each app's own segment builder.

Size: +60 shared, -50 each side. Risk: low.

### native-rest-18 (low) Duration and age formatting implemented four ways

Locations: `session/format.ts:16-45` (`durationIn`, `compactDuration`, `spokenDuration`, `timeAgo`, "just now" under one minute); `hub/PluginsPage.tsx:769-772` (`agoText`, "just now" under one second); `board/BoardRow.tsx:98-104` (`spokenAge` parses the string `relativeAge` produced); shared `appwire-client/typescript/state/navigation/selectors.ts:30-39` (`relativeAge`, "now" under a minute).

Evidence: `session/format.ts:1-3` itself says `relativeAge` "doesn't read the way the spec's copy does". A plugin updated 30 seconds ago reads "30s ago" (agoText) while a document updated 30 seconds ago reads "just now" (`timeAgo`). `BoardRow.spokenAge` regex-decodes `2m` back to `2 minutes`.

Impact: inconsistent wording across screens; four sources of unit thresholds.

Fix plan: make `session/format.ts` the single native formatter (rename out of `session/` since reader and hub import it), have `PluginsPage` use `timeAgo`, and have `relativeAge` callers on native go through `compactDuration`/`spokenDuration` on the millisecond age so `spokenAge` disappears. Keep shared `relativeAge` for web.

Size: -30 / +10. Risk: low; PluginsPage "just now" threshold changes (a behavior change: write a test first).

### native-rest-19 (low) Inconsistent device-storage access

Locations: `deviceStorage.ts:8-37` (`readJson`, `writeJson`, `removeKeys`, guarded) used by `board/boardMemory.ts`, `board/boardHold.ts`, `newSession/launchMemory.ts`, `subagents/stopRequests.ts`, `reader/documentMemory.ts`; hand-rolled `JSON.parse` of kv storage in `connection.ts:100,117`, `display/displayPreferences.ts:46`, `share/sharePayload.ts:41`, `alerts/alertPreferences.ts:12`, `session/detailLevels.ts:98`, `session/sessionNotes.ts:88`, `location.ts:92`, `readerPosition.ts:356,381,409`, `nativeOrganization.ts:30`.

Evidence: the helper's header says "Every read and write is guarded, because the store can throw"; the hand-rolled sites each re-write `try { JSON.parse } catch { return null }`, and `readerPosition.ts:356-365` also has its own in-memory fallback map.

Impact: some sites throw on a storage failure, others swallow; each decides separately.

Fix plan: migrate the plain read-then-validate sites (`displayPreferences`, `alertPreferences`, `detailLevels`, `sessionNotes`, `location`, `sharePayload`) to `readJson`/`writeJson`; leave `readerPosition` (memory fallback) as is, documented.

Size: -60 lines. Risk: low.

### native-rest-20 (low) Flat `src/` root and misfiled screens

Locations: `src/` root: `HubEditor.tsx`, `HubSettingsScreen.tsx`, `HubPathField.tsx`, `ProviderEditor.tsx`, `ProviderSignInSheet.tsx`, `MarketplaceBrowser.tsx`, `ForkEditor.tsx`/`ForkScreen.tsx`, `Pin*` (5 files), `SessionDeletion*` (2), `Launch*` (8) versus `hub/` which holds the hub pages that use them (`hub/HubDetailsPage.tsx:9` imports `../HubEditor`, `hub/HubSheet.tsx:11` imports `../HubSettingsScreen`).

Evidence: 145 non-test files in the root (314 with tests); `hubs/` and `hub/` are two directories with near-identical names (`hubs/AddHub.tsx` 372 lines wrapped by `hub/AddHubPage.tsx` 19 lines).

Impact: hard to find the owner of a feature; the directory layout no longer states the S03 map.

Fix plan: `git mv` by feature into `hub/`, `providers/`, `launch/`, `pins/`, `fork/`, `connection/` (each with its tests), rewrite imports with the repo's TS tooling, update `docs/product/subsystems.md` S03 links. Merge `hubs/` into `hub/`. Pure moves; do after findings 1 and 3 to avoid conflicts.

Size: about 250 files moved, 600 import edits. Risk: low but noisy.

### native-rest-21 (low) SavedLocation is an optional-field bag with hand-written exclusivity rules

Locations: `location.ts:8-24` (`SavedLocation`), `:85-160` (`LocationRepository.read`).

Evidence: seven optional fields (`conversation`, `reader`, `pinAssignment`, `deleteSession`, `fork`, `pinned`, `projects`), with each pair's mutual exclusion encoded in five multi-line `if` blocks (`value.deleteSession !== undefined && (... value.fork !== undefined || value.pinAssignment !== undefined || value.pinned !== undefined)`).

Impact: adding a destination means editing every other destination's exclusion list.

Fix plan: represent as a tagged union `{ hubId, at: { kind: "conversation" | "reader" | ... } }` and validate with a table of required/forbidden fields per kind. Changes the stored shape, so it needs Jesse's approval (compat decision, see finding 8) or a single read adapter; otherwise leave.

Size: about -60 lines. Risk: medium (persisted data).

### native-rest-22 (low) Provider sign-in status derived separately on web and native

Locations: `providers/providerStatus.ts:15-55` (`providerStatus`, `statusOf`, `signInKind`), web `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ProviderConnection.tsx:775`; shared `appwire-client/typescript/state/credentials/authStatuses.ts`.

Evidence: native words ("Sign-in expired", "Error", "Signed in", "Key set", "Not signed in", "No key", "No sign-in needed") and tones are computed from `activeSource`/`authModes`/`credentialRequired`/`needsLogin` in native only; the web computes its own label set with different words ("Not signed in", row "Sign in to connect"). Only `needsLogin` is interpreted by a shared module.

Impact: the same instance can read differently in each client; AGENTS says the shared client owns shared derivations.

Fix plan: move the state decision (an enum like `expired | error | signedIn | keySet | notSignedIn | noKey | noSignInNeeded`) into `appwire-client/state/credentials`, leaving word and tone mapping per client. Web and native tests both assert the enum.

Size: +40 shared, -30 each. Risk: low.

## Recommended fix sequence

1. native-rest-15 and native-rest-16 (delete dead exports and orphan scripts): no-risk cleanup that shrinks the surface.
2. native-rest-4 (split entity table), native-rest-11 (comment identifier pass), native-rest-19 (storage helper): mechanical, behavior-neutral.
3. Ask Jesse the compat questions (native-rest-8, -6 part 1, -21) and delete what he approves.
4. native-rest-2 and native-rest-17 (shared package homes for ansi, plugin preview, forbidTags, mermaid fences), then add the lint rule.
5. native-rest-5 (durable record helpers), native-rest-7 (model picker unification), native-rest-9 (gated action hook), native-rest-10 (backoff and emitter helpers), native-rest-18 (duration formatting), native-rest-22 (provider status enum).
6. native-rest-1 with native-rest-13: extract the screen into hooks one PR at a time, moving tests alongside; this is the biggest win and needs the earlier cleanups out of the way.
7. native-rest-12: rewrite copy assertions as files are touched, finishing with a sweep.
8. native-rest-14: decide demo hub fate, then move or delete.
9. native-rest-3 and native-rest-20: structural moves last (`mobile/src` fold-in decision, then directory regrouping), each as a pure rename PR with the `git diff --name-only` check against main.
10. native-rest-6 part 2 (shared outbox transitions) only after part 1 and only if drift between adapters shows up in practice.
