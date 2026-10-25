# Tech-debt audit: appwire-ts-state-testing

Slice: `appwire-client/typescript/state/**`, `appwire-client/typescript/testing/**`, the package's root test files, `package.json` exports map, README subpath rules.
Worktree head: a6c0b9c561 (origin/main). Read-only audit; no test suites were run.

## Inventory

| Area | Files | Lines | Notes |
|---|---|---|---|
| `state/**` source | 36 | 9,135 | 2,752 comment lines (30%) |
| `state/**` tests | 31 | 13,845 | 792 comment lines |
| `testing/**` | 27 (25 modules + 2 tests) | 3,759 | 786 comment lines |
| root `*.test.ts` | 110 | 50,568 | not read in depth; `publicExports.test.ts` and a few others sampled |
| `index.ts` | 1 | 908 | 183 export statements |
| `scripts/qualify-package.mjs` | 1 | 1,305 | hand-written per-subpath manifest |

Largest files: `state/navigation/store.test.ts` 2,862; `codec.test.ts` 1,705; `instances.ts` 1,298 (616 comment lines, 47%); `instances.test.ts` 1,274; `codec.ts` 1,139; `store.ts` 1,008; `storeLifecycle.ts` 338 (222 comment lines, 66%).

Exports map: `.`, `./docContent`, `./state/{connection,navigation,credentials,extensions,mutation}`, `./pairing`, `./hubOrigin`. Every entry has `types/import/require` pointing at `dist`. `tsconfig.build.json` `files` is a hand-maintained list of 153 entries; the only non-test `.ts` files not in it are `tokenFlood.bench.ts`, `sessionActivityTestUtils.ts` and `state/mutation/testing.ts` (all test support, correctly excluded). No dangling entries. The exports map itself is clean; the debt is in what the barrels publish (finding 5) and in docs that drifted (finding 14).

Testing modules by consumer count (files importing `.../testing/<name>`): fakeClient 287, deferred 47, navigation 43, notifications 43, currentTranscript 22, toolWireFixtures 14, subagentWireFixtures 12, fakeSocket 10, macrotask 10, six modules with 2-6, and `modelAccessors` with 1 (plus `reducerHooks`, `hubWriteGate`, `goConstants`, `hubWireFixtures`, `queuedMessageStorageConformance`, `mutationOutboxStorageConformance` at 2 each).

---

## Findings

### 1. `equalJSON` and `sameJsonValue` are the same function (medium, duplication)

- `state/navigation/immutable.ts:14-27` `equalJSON` and `plainObject.ts:21-38` `sameJsonValue` both implement structural JSON equality (arrays by index, objects by key set + `Object.hasOwn`).
- `sameJsonValue` is the better one: it checks array holes and compares plain objects across realms (`isJsonObject`, `plainObject.ts:44`). `equalJSON` treats any non-null object (Date, class instance) structurally.
- Consumers: `merge.ts:57,60,99` and `launchConfig.ts:25,218-301` use `equalJSON`; `projection.ts:246`, `sessionActivityStore.ts:1452`, `LaunchConfigForm.tsx:120` use `sameJsonValue`. `launchConfig.ts` (a root module) imports from `./state/navigation/immutable`, a layering inversion for one helper. `equalJSON` is also published from the navigation barrel (`state/navigation/index.ts:48`).
- Impact: two equality semantics for the same domain; `launchConfig.ts` dirty-checks (`equalJSON`) and `LaunchConfigForm.tsx:120` (`sameJsonValue`) can disagree on edge inputs.
- Fix: delete `equalJSON`; `merge.ts` and `launchConfig.ts` import `sameJsonValue` from `plainObject`; drop `equalJSON` from the nav barrel; keep `cloneAndDeepFreezeJSON`. Move `equalJSON`'s test cases into `plainObject.test.ts` (keep key-order and array-order cases). Update README nav paragraph.
- Size: about -20 / +8 lines. Risk: low; merge.ts runs on every navigation delta, so run `merge.test.ts`/`store.test.ts` in CI.

### 2. `createCredentialInstancesStore` is a 946-line single closure (high, complexity)

- `state/credentials/instances.ts:351-1297` is one function holding: listing fetch with `requestVersion` ordering, connection bookkeeping + generation (`bookkeepingGeneration`), `refetchTimer` (`:403,659-688`), own-echo markers (`noteLocalMutation`/`consumeOwnEcho`, `:878-975`), per-instance `refreshModels` (`:707-870`), `applyMutation` with superseded-reconcile (`:574-636`), `authMutation` (`:1026`), and the public action table.
- 616 of 1,298 lines are comments; several are multi-paragraph rationales mid-function (e.g. `:1020-1110`). The code carries correctness arguments that belong in tests or a short design doc.
- Repeated tail: `if (!applied) scheduleRefetch(); return applied;` in `create/edit/remove/setDefault` (`~:1100-1150`).
- `resetForTests()` is on the production store interface (`:213-215`, `:1286`), used only by `instances.test.ts:402,407,611,681`. The README itself says tests build their own instance.
- Impact: highest-risk module in the slice; every change must be reasoned against 900 lines of closure state. Reviewers and LLMs both mis-edit it.
- Fix (behavior-preserving, no logic change): split along its existing seams into sibling modules under `state/credentials/`: `echoMarkers.ts` (the marker table + `consumeOwnEcho`), `modelRefresh.ts` (in-flight refresh bookkeeping), `listing.ts` (pure `listState/listingOf/listingChanged/foreignListingChange`, already top-level pure functions at `:234-290`). Each takes the shared mutable `deps` object explicitly. Collapse the `applied`/`scheduleRefetch` tail into `applyListingMutation(...)`. Delete `resetForTests` and rewrite its 3 test uses to construct a fresh store. Trim comments to the invariant, move long arguments into `docs/product` or the tests that pin them.
- TDD: existing 1,274-line `instances.test.ts` is the guard; run it unchanged before and after each extraction.
- Size: about 1,300 lines touched, net -250 to -400. Risk: medium (shared closure state). Do one extraction per commit.

### 3. The reconnect/lifecycle logic exists twice, pinned only by a parity test (medium, duplication/architecture)

- `state/credentials/instances.ts` (own subscription `:977-994`, own debounce timer `:659-688`, `connectionChanged` `:1251`) and `state/extensions/storeLifecycle.ts` (`createStoreLifecycle`) both implement: subscribe to a hub `*/updated` notification, debounce a refetch, fence in-flight work on a replaced client, re-read a wanted list on reconnect.
- `state/reconnectPredicate.test.ts:1-20` documents this: "Neither can adopt the other: #1558 measured the move at +21 lines and a generalization of the shared helper that exists for one user." Meanwhile `authStatuses.ts` in the same directory does adopt the lifecycle (`:55-60`), so there are now four lifecycle users plus one fork.
- Impact: a fix to reconnect recovery must be made twice and the parity test is the only guard. The #1558 measurement predates `authStatuses` joining the lifecycle and the lifecycle growing `resetState`/`onFence` options.
- Fix: do finding 2 first. Then re-measure whether the credentials listing can sit on `createStoreLifecycle` (its `wantsList`/`revision`/`onFence` options already model "read again when ready if wanted"). Discuss with Jesse before attempting: this is a consequential design call and #1558 recorded a deliberate decision. If adopted, `reconnectPredicate.test.ts` collapses to a single-implementation test and is deleted.
- Size: unknown until finding 2 lands; likely -150 net. Risk: high (own-echo correlation depends on the timer).

### 4. Extensions/credentials stores restate the same scaffolding four times (medium, duplication)

- `PluginsStore` (`plugins.ts:67-81`), `LaunchLayerStore` (`launchLayer.ts:44-59`), `MarketplacesStore` (`marketplaces.ts`, same three members) and `AuthStatusesStore` (`authStatuses.ts:36-43`) each re-declare `start()/reset()/dispose()` with the doc comment copied; all three already come from `HostLifecycle<S>` (`storeLifecycle.ts`, `Omit<StoreLifecycle,"guard"|"epoch">`). The redeclarations add nothing but a place for the docs to drift.
- `PLUGIN_REFETCH_DEBOUNCE_MS`, `LAUNCH_LAYER_REFETCH_DEBOUNCE_MS`, `MARKETPLACE_REFETCH_DEBOUNCE_MS`, `AUTH_STATUSES_REFETCH_DEBOUNCE_MS` are all `250`.
- The `*Client` types (`PluginsClient`, `LaunchLayerClient`, `MarketplacesClient`) are the identical `RequestPort<HostRequestMethod> & Pick<AppwireClientLike,"onNotification">`.
- The fetch body is the same shape in all four: set loading, `readRevisioned(..., {onAnswer, onFailure: friendlyErrorMessage})`; the comment explaining why an outrun read publishes none of the three fields is copied into plugins.ts, launchLayer.ts and marketplaces.ts.
- Fix: store interfaces become `export interface PluginsStore extends FrameworkFreeStore<PluginsState>, HostLifecycle<PluginsState> {}`; one `HUB_LIST_REFETCH_DEBOUNCE_MS` (keep the four exported names only if apps/tests import them: mobile `PluginsPage.test.tsx:25` imports `MARKETPLACE_REFETCH_DEBOUNCE_MS`, so re-export aliases or update that import); one `HubListClient` type. Keep the explanatory comment once, on `readRevisioned`.
- Do NOT build a generic "list store factory": the per-store state names (`pluginsLoading`, `launchLayerError`) are public API and the stores differ in their mutations. Only remove the copy-pasted declarations and comments.
- Size: about -90 lines. Risk: low (types and comments).

### 5. `export *` barrels publish internals no consumer uses; barrel style is inconsistent (medium, architecture/compat)

- `state/extensions/index.ts:7-12` and `state/credentials/index.ts` use `export *`. `state/navigation/index.ts:20-26` explicitly says it must never use `export *`, because a `.mts` script run by tsx loads the barrel as CommonJS and Node's named-export analysis cannot see through star re-exports. The same hazard applies to the other two barrels; `state/mutation/index.ts` and `connection/index.ts` use explicit lists.
- Zero consumers outside the package for `createKeyedRevision`, `createListRevision`, `readRevisioned`, `writeRevisioned`, `attachLifecycle` (grep across `cmd/`, `mobile-native/src`; `createStoreLifecycle` only appears in a comment at `frontend/src/stores/connection.ts:140`). They are locked into the public surface by `scripts/qualify-package.mjs:700-728` and documented in the README as public.
- `tryOrUndefined` is exported from `state/mutation/secureUUID.ts:13` only to be shared with `records.ts` and is not in the barrel; fine, but it is a generic helper living in a UUID module.
- Impact: every internal refactor of finding 3/4 is an API change that the qualification manifest and README must track. Internals read as API to the next LLM author.
- Fix: switch both barrels to explicit export lists; list only what apps import (`createHubWriteGate`, `HubWriteBusyError`, `HUB_WRITE_BUSY`, `isHubWriteBusy`, the three store factories and their `*Client`/state types, `marketplaceRemovalOutcome`, `MarketplaceCatalogEntry`, `*_DEBOUNCE_MS`). Remove the internals from `qualify-package.mjs` and the README sentence listing them. This removes public API, so it needs Jesse's approval (package is versioned 0.2.0 and ships a tarball, though all consumers are in-repo).
- Size: about -60 lines across barrel, manifest, README. Risk: low; `make test-api-package` / `lint-package-imports` verify.

### 6. `marketplaces.ts` repeats its publish step three times (low, duplication)

- `{...publishMarketplaceSnapshot(x), browseCatalogs: retireCatalogsAbsentFrom(s.browseCatalogs, x)}` appears in `fetchMarketplaces` onAnswer, `mutate` onAnswer, and `mutate` onFailure (about `:230`, `:240`, `:250` of the 400-line file). `mutate` nests four closures and checks `issuedIn !== lifecycle.epoch()` three times.
- `isMarketplaceEntry` (about 14 lines) hand-validates a generated `MarketplaceEntry` for the removal-litter payload.
- Fix: extract `applySnapshot(marketplaces, retire)` once and use it in all three; hoist the epoch check into the write wrapper. Add one test for the onFailure applied path if missing (existing `marketplaces.test.ts` 845 lines covers it).
- Size: about -35 lines. Risk: low.

### 7. Navigation store re-validates what the codec already validated, with weaker copies of the guards (medium, duplication)

- `state/navigation/store.ts:174-177` defines `record`, `string`, `bool`, `count`; `codec.ts:61,84,85` defines `isRecord`, `count`, `bool` identically.
- `store.ts:178-203` `isNavigationManifest` is a weaker re-implementation of `codec.ts:558-586` `manifestMetadata` (no key whitelist, no bounds, no `identity`/`boundedString`). It is called at `store.ts:483,567`. `isNavigationProjectResource` (`:205`) likewise duplicates codec project validation.
- Elsewhere in the package: `isRecord` in `memoryContext.ts:37`, `reducer.ts:1373`, `systemEventCopy.ts:28` (as `record`), and canonical `isPlainObject` in `plainObject.ts:10` (stricter on prototype).
- Impact: two definitions of "a valid manifest"; the weaker one runs on data the codec already accepted, so it is either dead defence or evidence the state type is `unknown` where it should be typed.
- Fix: type `ResourceState.data` for manifest/project at the point the codec materializes it so the store narrows by key kind instead of re-checking; delete `isNavigationManifest`/`isNavigationProjectResource` and the local guards; export `isRecord` from `codec.ts` (or use `isPlainObject`) for the remaining `assertNavigationPageProgress` use. If a runtime check must stay (persisted/foreign state), reuse codec's `manifestMetadata`.
- Size: about -60 lines. Risk: medium (store has 2,862 lines of tests; some may feed malformed manifests deliberately; check `store.test.ts` for that before deleting).

### 8. The turn-method vocabulary is a hand-copied list in five-plus places (medium, duplication)

- `state/mutation/pendingEntries.ts:45-52` (`pendingMethod` if-chain) and `:58-64` (`PENDING_TURN_METHODS` array) encode one mapping twice in the same file.
- `queuedMessages.ts:40-46` `REMOVABLE_MESSAGE_METHODS` repeats the same five strings; `canEditQueuedMessage` (`:68-73`) inlines three of them again.
- `dispatcher.ts:313-323` `RETRY_SAFE_MUTATION_METHODS`, plus `:208-216` `turn/drainAsSteer` special case.
- Outside the slice: `mobile-native/src/nativeMutationRuntime.ts:92-112` (`methodFor`, `DIRECT_FALLBACK_METHODS`), `cmd/evener-hub/frontend/src/stores/threads.ts:4148` (`COMPOSER_ROUTE_METHODS`, whose comment already describes the problem: "so a route added here cannot leave the fallback set silently behind").
- Impact: adding a verb means editing 5+ literals with no compiler help.
- Fix: one `const PENDING_METHOD_WIRE = { send: "turn/start", steer: "turn/steer", queue: "turn/queue", drain: "turn/drainAsSteer", promote: "turn/promoteQueuedAsSteer" } as const satisfies Record<PendingMethod,string>` in `pendingEntries.ts` (or a small `turnMethods.ts`); derive `PENDING_TURN_METHODS`, `pendingMethod`, `REMOVABLE_MESSAGE_METHODS`, and the edit predicate from it; `RETRY_SAFE_MUTATION_METHODS` becomes `[...values, "turn/interrupt","turn/cancelQueued","thread/clear","notes/human/set"]`. Web and native import the record. Behavior-preserving; `queuedMessages.test.ts` (93) and `pendingEntries.test.ts` (459) guard it.
- Size: about -40 in slice, -30 in apps. Risk: low.

### 9. `deferred`/`flush` test helpers re-invented despite `testing/deferred.ts` and `testing/macrotask.ts` (medium, duplication)

- Canonical: `testing/deferred.ts` (47 importing files) and `testing/macrotask.ts` (10 files; its header comment is a careful spec).
- Inside the package, local copies: `documentReadDemand.test.ts:15` `deferred`, `state/navigation/store.test.ts:68-77` `flush` + `deferred` (a 64-microtask loop, semantically different from `nextMacrotask`), `hubUpdate.test.ts:77` `settle`, `taskPanelState.test.ts:300`, `state/extensions/marketplaces.test.ts:435`, `state/navigation/backgroundDemand.test.ts:151` inline `new Promise(r => setTimeout(r,0))`.
- Outside: 14+ local `deferred` definitions in `cmd/evener-hub/frontend/src/**` (`Rail.test.tsx:97`, `Spawn.test.tsx:490`, `CredentialsSection.test.tsx:85`, `transcript.test.tsx:98`, ...) and `mobile-native/src/**` (`newSession.test.ts:10`, `sessionResume.test.ts:67`, `nativeConversationSessions.test.tsx:104`, ...), plus several `tick`/`flush`/`settle` timeouts.
- Fix: replace the 6 in-package copies with imports (mechanical); then a sweep PR for apps. For `flush` in `store.test.ts`, decide per call whether `nextMacrotask` is enough; if the microtask loop is needed, add it to `testing/macrotask.ts` as `drainMicrotasks`.
- Size: about -60 in package, -150 in apps. Risk: low. Timing-sensitive tests can change behavior (microtask loop vs macrotask), so verify on CI.

### 10. Full `ThreadModel` literals hand-copied into tests; `state/mutation/testing.ts` sits outside `testing/` (medium, duplication/inconsistency)

- Complete `ThreadModel` object literals: `state/mutation/testing.ts:12-48`, `deriveAskQuestions.test.ts:55-90`, `transcriptProjector.test.ts:31`, also `threadUsage.test.ts:7`, `askDock.test.ts:27`, `askShared.test.ts:115`, and the production `reducer.ts:2335-2350` hydrate literal. `mutation/testing.ts` papers over a field with `capabilities: {} as ThreadModel["capabilities"]` (the cast its header says it exists to avoid).
- `state/mutation/testing.ts` (threadModel, outboxRecord, recoveryRecord, testPendingTurnsStore) is test support in the shipped `state/` tree, excluded from `tsconfig.build.json` only by omission. AGENTS.md says test support is `@evener/appwire-client/testing/<module>`.
- Fix: add `testing/threadModel.ts` exporting `threadModel(overrides)` built from the reducer's own empty-model constructor (extract one from `reducer.ts:~2320` so there is a single literal), and move `outboxRecord/recoveryRecord/testPendingTurnsStore` to `testing/mutationRecords.ts`. Migrate the six test-local builders. Keep per-file wrappers only where defaults differ (`askPending: true` in `deriveAskQuestions.test.ts`).
- Size: about -150 lines. Risk: low; type checker finds drift.

### 11. Wire-fixture loaders are seven copies of the same pattern (medium, duplication)

- `testing/{hub,notification,memoryContext,systemEvent,toolWire,subagentWire,summaryFailureWire}Fixtures.ts` each: `?raw` import, `JSON.parse` on every call (e.g. `notificationWireFixtures.ts:50-58` re-parses the whole steering file per lookup), `find(rec => rec.case === name)`, `throw new Error("no X case in PATH")`. The "no ${callId} step in ..." lookup is duplicated between `toolWireFixtures.ts:107-113` and `subagentWireFixtures.ts:49-52`, each also rebuilding a hydrated thread with its own cached `model` variable.
- Hand-typed case unions duplicate the JSON: `ToolWireCall` has 58 members (`toolWireFixtures.ts:26-85`), `SystemEventWireCase` 21, `NotificationWireCase` 18, `MemoryContextWireCase` 12. They give typo safety but drift silently when Go adds a case (no test checks the union against the fixture).
- The regenerate instruction is inconsistent: five files say `make fuzz-goldens`; `memoryContextWireFixtures.ts:5-7` gives a raw `go test ... -update-wire` command.
- Fix: `testing/recordedFixture.ts` with `recordedCases(raw, path)` (parse once, memoize) and `recordedStep(model, callId, path)`; each fixture file shrinks to its typed accessors. Add one test per union asserting it equals the fixture's case list (turns the hand-typed unions from drift risk to checked). Make the regenerate hint uniform.
- Size: about -120 lines, +40 helper/tests. Risk: low.

### 12. `testing/navigation.ts` hand-re-implements the hub's snapshot normalizer (medium, architecture/test-debt)

- `testing/navigation.ts:78-243` `wireSnapshot` + `completeBody` + `completeSession` convert a "v1-shaped body" into a v3 snapshot "mirroring the server's normalization" (flattening session trees to entities + owned containers, per-kind metadata). 325 lines; used by 43 files including `mobile-native/src/dev/demoFleet.ts`, `mobile-native/src/board/navigationHubTestUtils.ts`, 13+ mobile suites and the package's own store tests.
- Every other wire fixture in `testing/` is recorded from a Go test that drives the real producer (`make fuzz-goldens`). This one is a TypeScript guess at Go's behaviour, so tests can pass against snapshots the hub would never produce (and the reconnect fixtures at `:245-325` add three more hand-built variants).
- Impact: the codec validates strictly, so drift usually fails loudly, but the failure shows up in the fixture and not as "the hub changed".
- Fix: staged. (a) Add a Go test that emits `testdata/navigationwire/*.json` for manifest/section/location/project/catalog from the real normalizer, with TS `testing/navigationWireFixtures.ts` loading it like its siblings. (b) New tests use recorded snapshots; migrate `wireSnapshot` callers by suite, deleting `completeBody/completeSession` last. Raise with Jesse first: this is a multi-PR migration and Go work.
- Size: about -250 TS, +80 Go/TS. Risk: medium-high (43 consumer files).

### 13. `fakeClient.test.ts` is 604 lines testing a test double against itself (medium, test-debt)

- `testing/fakeClient.test.ts` (30+ tests) asserts FakeClient's own behavior: listenerCount, `close()` idempotence, "a request result whose then getter throws rejects instead of hanging" (`:461`), throwing listeners. Only the catalog-validation block (`:138-237`, METHOD_NAMES/NOTIFICATION_NAMES guards) pins something external. The fidelity claims ("mirrors AppwireClient.close", "same rejection shape as the real client") are asserted against the fake, not the real `AppwireClient`, so the two can drift while both suites pass. This is the "tests that test mocked behavior" class Jesse asked to be warned about.
- Several assertions pin copy: `fakeClient.test.ts:280` `toThrow("Stop canceled this pending action")`, `:321` `toMatch(/Connect to the hub before resuming this session/)`, `:551` `/socket closed/`.
- Fix: keep the catalog tests (about 100 lines); replace the lifecycle tests with a shared contract suite (`describeClientContract(factory)`) run against both `new AppwireClient({socketFactory: FakeSocket})` and `FakeClient`, so fidelity is checked, not claimed. Drop the pure fake-internal cases (then-getter, listener-throw) or fold them into the contract. Replace message assertions with error-type/outcome assertions.
- Size: -350 / +120. Risk: medium (finding which behaviors the contract can honestly share).

### 14. Test assertions on copy and thrown phrases (low-medium, test-debt)

AGENTS.md: "no test may check that copy, error messages, or other human-readable strings match a hardcoded phrase or a constant imported from the code." In-slice violations (code-owned strings, not test-authored ones like `"mutation failed"`):
- `state/mutation/submission.test.ts:33` `toThrow("A message submission is already pending for this task")`.
- `state/navigation/backgroundDemand.test.ts:145,612,722,723,799` `toThrow("background navigation lease released")`, and `:379,680` `"target convergence failed"` (check whether test-authored).
- `state/navigation/codec.test.ts:1572,1581` `"navigation protocol: invalid archived list"`; `merge.test.ts:341,370` `"navigation protocol"`; `store.test.ts:1350` `protocolError?.message).toContain("representation v3")`.
- `state/credentials/instances.test.ts:301` `refusal?.message).toBe(CONNECTION_REPLACED_ERROR)` (constant imported from code); `state/extensions/hubWriteGate.test.ts:114` `message).toBe(HUB_WRITE_BUSY)`.
- `testing/goConstants.test.ts:36`, `testing/fakeClient.test.ts:280,321,551`.
- Fix: introduce typed error classes (several already exist: `StaleListingRefusal`, `HubWriteBusyError`, `NavigationProtocolError` is a private class in `store.ts:170`) and assert `rejects.toBeInstanceOf(...)`/a `code` field. Export `NavigationProtocolError` and a `BackgroundLeaseReleasedError`; codec's `schemaError` (`codec.ts:87`) returns bare `Error` and should return the same class.
- Size: about 30 assertions, +20 lines of classes. Risk: low.

### 15. `publicExports.test.ts` pins deleted code and tautologies (low, test-debt)

- `publicExports.test.ts:36-75` `@ts-expect-error` locks on two retired aliases (#1431) and the `retiredValueAliases` list (about 21 names) asserting they are NOT exported; the `typeof value === "function"` check over 23 names (about `:100-130`) is already guaranteed by the named imports compiling.
- Impact: history is encoded as a test; each future removal adds to the list; the compile-time import already enforces presence.
- Fix: delete the negative alias list and the typeof-function loop; keep the type-position imports (compile locks) and the two behavior tests (`mergeTurnHistory`, `activityNodeID`, `buildWatchRows`). Per Jesse's rule against reducing coverage, confirm with him before removing the retired-alias assertions.
- Size: about -90 lines. Risk: low.

### 16. Comment bloat and history narration in non-test source (medium, slop)

- 30% of `state/**` source lines are comments (2,752 / 9,135); `testing/` 21%. Worst: `storeLifecycle.ts` 222/338, `instances.ts` 616/1298, `dispatcher.ts` 134/388, `outbox.ts` 166/428, `records.ts` 141/269, `connection/core.ts` 116/212.
- 33 references to issue/kata numbers in non-test state/testing source (`mutationOutboxStorageConformance.ts` 10, `dispatcher.ts` 6, `codec.ts` 4, ...), e.g. `testing/notifications.ts` "evener/attention/changed carried an undeclared payload until kata 4j2t moved...", `mutationOutboxStorageConformance.ts:8` "Before this, each adapter hand-transcribed the other's storage contracts", `fakeClient.ts` "as this fake used to", "(all of them, before this field existed)", `testing/tokenFlood.ts:1-9` "wave-4 ... binding gate". CLAUDE.md rule: history belongs in commit messages.
- Stale pointers: `testing/fakeClient.ts:1-7` names `../../stores/threads.test.ts` and `src/stores/*` (no such path under the package; the web's is `cmd/evener-hub/frontend/src/stores/threads.test.ts`) and `ConnectionBanner.test.tsx`; `tokenFlood.ts:6` says it is imported by `tokenFlood.test.ts` (the test is `frontend/src/panes/session/tokenFlood.test.tsx`); `goConstants.ts:5-9` says mobile "can adopt this when next touched".
- Fix: per-file sweep deleting narration/history/issue-number paragraphs and fixing stale paths; keep invariants and the "why". Do it alongside findings 2-4 so no file is touched twice. No code change.
- Size: about -1,000 comment lines across the slice. Risk: none beyond review noise; Biome format only.

### 17. Two different canned `InitializeResponse` fixtures (low, duplication)

- `testing/fakeClient.ts:53-71` `DEFAULT_INITIALIZE_RESPONSE` (features all false, `sourceId: "fake"`, version `0.0.0`) and `testing/fakeSocket.ts:15-33` `FAKE_INITIALIZE_RESULT` (all true, `fake-source`, `0.0.0-test`). Same 12 feature keys, hand-listed twice; `fakeClient.test.ts:13` imports the socket's to compare.
- Fix: one `fakeInitializeResponse(features?)` in `testing/` with the key list once; the two call it with false/true. Size -20. Risk: low.

### 18. Test-only production surface (low, dead-code)

- `testing/reducerHooks.ts` publishes `appendChunk` whose own comment says "no production path calls it any more"; the function stays in `chunkview.ts:200` and is exercised only by `chunkview.test.ts` and `currentTranscript.test.ts:1214`. Dead production code kept alive by tests.
- `testing/modelAccessors.ts` (1 consumer file by name, sub-20 lines) and `testing/keybindingRegistry.ts`, `testing/hubWriteGate.ts` (2 consumers) are fine but tiny.
- `instances.ts` `resetForTests` (see finding 2) and `taskPanelState.ts:65,241` have the same pattern in production types.
- Fix: delete `appendChunk` and its chunkview tests if the read-model no longer uses the chunk view for writes (confirm `chunkview.ts` readers still need only `pendingTextJoined`); remove `reducerHooks.appendChunk`. Size: about -80. Risk: low-medium; verify no production import.

### 19. `tokenFlood` bench harness never runs in a gate (low, dead-code)

- `tokenFlood.bench.ts` (317 lines) + `testing/tokenFlood.ts` (336 lines): vitest excludes `*.bench.ts` (`vite.config.ts:269-272`); the doc says run manually to reproduce numbers in `docs/superpowers/plans/wave4-report.md`. The correctness half (`frontend/src/panes/session/tokenFlood.test.tsx`) is the only gated user of the 336-line harness, along with `reasoningFormat.test.ts`.
- Fix: decide with Jesse whether the benchmark is still wanted. If not, delete the bench and trim `tokenFlood.ts` to what the two tests use (stream builder + PRNG); move the harness next to its test. Size: -400. Risk: low.

### 20. Conformance suites carry legacy-shape contracts (low, compat-legacy)

- `testing/mutationOutboxStorageConformance.ts:35-49,402` `stripAttemptCount` and `stripCaptureAtEnqueue` factory hooks and the "legacy pre-count evidence" contract pin that adapters read pre-`unresolvedAttempts` / pre-`stopEpochAtEnqueue` rows. `queuedMessages.ts:36-41` `hasContradictoryAttemptEvidence` reads both `attempted` and `unresolvedAttempts`. `testing/navigation.ts` says "v1-shaped" bodies and "v3".
- No evidence in this audit of whether any persisted data older than these fields still exists in the field. Per Jesse's rule, no new back-compat is added; this is a candidate to retire once a storage schema floor is declared.
- Fix: ask Jesse whether a schema floor exists; if so delete the two legacy hooks, their contracts, and the dual-field reads in `queuedMessages.ts` and both adapters. Size -80. Risk: medium (user data on disk).

### 21. README drift in the subpath section (low, doc-drift)

- README says the nav subpath "resolves to `state/navigation/index.ts`, a barrel that re-exports the nine modules whole"; the barrel is an explicit selective list and says it must never `export *` (`state/navigation/index.ts:20-26`). It also does not re-export all of `hostGrouping`/`selectors`/`types` whole.
- The README states `MutationOutboxStorage` has "the 14 calls this layer and the dispatcher make"; count in `outbox.ts` interface is 13 method signatures by a simple grep (verify before correcting).
- The root intro paragraph is a single 150-line sentence chain listing every module; most of it duplicates `index.ts` and the per-module docs.
- The rule for subpaths (README "A module gets its own subpath when...") is followed: all five state layers have a barrel, an exports entry and a qualification entry; `docContent`, `pairing`, `hubOrigin` are the other three. No violation found, but `state/mutation` mixes record shapes, outbox, dispatcher, reconciliation and a store, which stretches "a layer adopted as a whole".
- Fix: correct the two statements; collapse the intro into a short module index pointing at `index.ts`. Size: about -80 README lines. Risk: none.

### 22. Duplicate random-id fallbacks in the web next to the package's canonical one (low, duplication; outside slice)

- Canonical: `state/mutation/secureUUID.ts:22-54` `createSecureUUID(source)` with documented fallback. Web re-implements the same try/randomUUID/getRandomValues/Math.random chain in `frontend/src/stores/versionedChannel.ts:24-33` `makeSourceId` and `stores/transcriptRepository.ts:105-125` `PAGE_LIFETIME_NONCE`, while `stores/browserRandomSource.ts` already adapts `crypto` to the package port.
- Fix: both call `createSecureUUID(browserRandomSource())`. Size -25. Risk: low (the id format changes from hex to UUID in the nonce).

---

## Recommended fix sequence (this slice)

1. Mechanical, zero-risk: finding 9 (adopt `deferred`/`nextMacrotask` in the 6 package test files), 17, 1 (`equalJSON` removal), 4 (interface/constant dedupe).
2. Finding 10 and 11 (shared builders in `testing/`, fixture loader helper). These unlock deleting local copies in later work.
3. Finding 8 (turn-method record), then 7 (store drops duplicate validators).
4. Finding 14 (typed errors replace phrase assertions), then 13 (client contract suite) and 15.
5. Finding 2 (split `instances.ts`, delete `resetForTests`), one extraction per commit; then discuss 3 with Jesse and decide whether to adopt `createStoreLifecycle`.
6. Finding 5 and 20 after Jesse approves API removal and the schema floor.
7. Finding 16 comment sweep, folded into each file as it is touched in steps 1-5; finish with a standalone sweep of the rest. Finding 21 README fix in the same PRs that change the facts it describes.
8. Finding 12 (recorded navigation fixtures) as its own multi-PR effort after discussion; 18, 19, 22 as small opportunistic cleanups.

Docs to update with each: `appwire-client/typescript/README.md` (subpath section), `docs/developing-evener/testing.md` if helper homes change, and `scripts/qualify-package.mjs` manifest for any barrel change.
