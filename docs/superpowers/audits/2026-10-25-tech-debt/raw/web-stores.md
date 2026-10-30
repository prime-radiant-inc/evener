# Tech-debt audit: web-stores

Slice: `cmd/evener-hub/frontend/src/stores/` (non-test). All paths below are relative to
`cmd/evener-hub/frontend/src/` unless they start with another root. Worktree `.worktrees/debt-audit` on origin/main
(`a6c0b9c561`). No tests were run; evidence is grep, `rg`, `wc` and reading.

## Inventory

- 140 entries in `stores/`. Non-test TypeScript: 25,924 lines (25,633 excluding `testing/`). Test code in the same dir: 73,098 lines,
  about 2.8x production.
- Comment lines (`//`, `*`, `/*`) in non-test files: 7,259 of 25,633 (28%). `threads.ts`: 2,982 of 8,990 (33%).
- Largest files: `threads.ts` 8,990; `transcriptRepository.ts` 3,108; `sessionEntryCache.ts` 1,805; `hostOps.ts` 1,729;
  `mutationOutboxIndexedDB.ts` 1,406; `hosts.ts` 744; `prefs.ts` 733; `transcriptDisplay.ts` 704; `credentials.ts` 513; `extensions.ts` 439.
  Everything else is under 410.
- `threads.ts`: 71 `export`s, 28 module-level `let`, 55 module-level `new Map/Set/WeakMap`, 52 mentions of `ForTests`.
  Largest functions (read, not heuristic): `handleNotification` 6172-6428 (256 lines), `enqueueMutationIntent` 4727-4945 (~218),
  `ensureThread` 7230-7414 (184), `publishAndReconcileThreadHydration` 5693-5874 (181), `loadOlderTurns` 7669-7845 (176),
  `getMutationRuntime` 2645-2800 (~156), `putThreadModels` 1817-2015 (~198 by heuristic).
- `transcriptRepository.ts`: one `TranscriptRepository` class 367-3100; methods `attachReader` 1323-1632 (301 lines),
  `#queueWrite` 2328-2743 (~154 by heuristic, 415 to the next method), `loadOlderPage` (190), `#issueCommit` (186), `acceptRead` (174).
- `hostOps.ts`: `restart` action 1248-1408 (160 lines), whole store body 1040-1726.
- 41 `export ... ForTests/ForTesting` symbols in non-test store files. 1,680 `reset*ForTests` calls in test files; 117 test files
  import `resetThreadsStoreForTests`.
- Biggest test files: `threads.sessionActions.test.ts` 10,661 lines (220 tests, larger than `threads.ts`), `transcriptRepository.resources.test.ts`
  4,321, `threads.entryProduction.test.ts` 4,025, `sessionEntryCache.test.ts` 3,541, `transcriptRepository.test.ts` 3,170,
  `credentials.test.ts` 3,141.
- Ownership model (from `docs/product/README.md`, `AGENTS.md`): shared derivations belong in `@evener/appwire-client`; `stores/` is the web's
  binding of package cores to `connectionStore`, plus the web-only IndexedDB adapters. The package already owns the mutation outbox and dispatcher
  (`appwire-client/typescript/state/mutation/`), credentials, extensions, navigation, connection core, archived list, hub update, keybindings,
  transcript display, tasks panel and session activity cores. Most small stores here are thin and healthy. The debt is concentrated in
  `threads.ts`, `transcriptRepository.ts`, `hostOps.ts`, and in a handful of cross-store copy-pastes.

Method: inventoried by `wc`/`rg`, read `threads.ts` (headers, state interface, fixture/route code, hydration pair, notification fold, action
methods, reset), `connection.ts`, `hostOps.ts` (refusal classifier, sequences, store shell), `agentsDoc.ts`, `credentials.ts` head,
`keybindings.ts`, `commandCatalog.ts`, `transcriptDisplay.ts` (rewire code), `prefs.ts`, `transcriptDisplay/localStore.ts`, `draftStorage.ts`,
`versionedChannel.ts`, `transcriptRepository.ts` (helpers, `queueWrite` head), `sessionEntryCache.ts` function index, all the tiny shim files, and
cross-checked `appwire-client/typescript`, `mobile-native/src`. Dead-export detection: ran an `rg -w` script over all 433 non-test exports in
`stores/`; 37 had no non-store, non-test reference (listed under web-stores-14).

---

## Findings

### web-stores-1 (high) `threads.ts` is an 8,990-line module that owns ten subsystems

Locations: `stores/threads.ts` (whole file); state interface 212-506; entry routes 660-1580; model indexes 1695-1960; hydration ownership 1960-2165;
mutation runtime 2167-2850 and 3675-5150; stop/resume 2849-3340; recovery mutations 3341-3675; hydration begin/publish 5184-5930; notification fold
6008-6460; refresh/ready/rewire 6463-7184; store actions 7184-8325; cache epoch, channel, clear 8324-8700; background lease 8733-8845; reset 8845-8982.

Evidence:
- One file holds: refcounted ThreadModel tracking; watched (lean) threads; the entry-transcript route table (cache, repository, reader per ref);
  the mutation runtime (outbox, dispatcher, pins, transient-refusal pacing, retarget, undelivered-id tracking); the Stop/resume driver; recovery
  mutation actions; hydration scheduling and retry; notification routing and folding; the ready/reconnect state machine; the models list cache;
  the cross-tab cache epoch channel and Clear; background leases; and ~40 store actions.
- 28 `let` and 55 module `Map/Set/WeakMap` singletons share one namespace (for example `inflightHydrates`, `inflightHydrateClients`,
  `inflightHydrateEpochs` at 660-662; `pendingThreadHydrations`, `pendingWatchedHydrations`, `pendingMutationReconciliations` at 1641-1659;
  `stoppingRefs`, `activeStops`, `userIntentStopGenerations`, `shutdownDispatchGates`, `inFlightResumes`, `resumeSwapTargets`).
- `resetThreadsStoreForTests` (8845-8980) is a 135-line hand-maintained list of `.clear()` calls over every one of those singletons. Adding a map
  without extending it leaks state between tests silently.
- Modules cannot import each other cleanly: `liveControls.ts:5-8` ("threads.ts cannot import this module without an import cycle"),
  `tasksPanel.ts:5-9` and `threads.ts:73` import cycle workarounds ("never need each other at evaluation time").
- Git churn: 68 commits touched `threads.ts` since 2026-09-01.
- Doc rows: `docs/product/subsystems.md` S02 treats "the thread store" as one owner and links only `threads.ts`; the recovery contracts live
  in `docs/product/message-recovery.md`.

Impact: the largest maintenance drag in the slice. Reviewers (and LLM edits) cannot hold the module in context, so fixes are local and
duplicate neighbours (see web-stores-4, -5). Test setup cost is O(all singletons), and the 10,661-line action test exists because everything is reachable only through the
one store.

Fix plan (behavior-preserving, mechanical, one extraction per PR, each PR keeps the exported surface of `threads.ts` by re-exporting only until
its importers are repointed, then deleting the re-export):
1. Extract in dependency order, each into its own module owning its own state and a `reset()` the module registers with a single
   `registerStoreReset` list (see web-stores-3): (a) `threadModelIndex` (1695-1960, pure); (b) `entryRoutes` (660-1580); (c) `mutationRuntime`
   (2167-2850, 3675-3700, 4981-5030 storage classification, 5044-5150); (d) `stopResume` (2849-3340); (e) `hydration` (1960-2165, 5184-5930,
   6463-6620); (f) `notificationFold` (6008-6460); (g) `cacheEpoch` (8324-8700). `threads.ts` keeps `threadsStore`, the actions and the
   subscribe/rewire glue, target under 2,000 lines.
2. Break the cycles by having extracted modules take the store as a parameter or read it through a `getThreadsState()` accessor passed at init,
   instead of importing `threadsStore` (removes the `liveControls`/`tasksPanel` workarounds).
3. Test strategy: no behavior change, so the existing 25 `threads.*.test.ts` files are the guard; run them in CI per extraction. Split
   `threads.sessionActions.test.ts` along the same seams (web-stores-17).
4. Docs: update `docs/product/subsystems.md` S02 links to name the new modules; refresh the stores section in `docs/architecture.md` if it lists
   `threads.ts`.

Size: about 9,000 lines moved, ~0 net; about 300 lines of re-export plumbing added then removed. Risk: medium-high (shared mutable singletons,
ordering of module-load wiring such as `rewireClient` at load). Mitigate by moving one cluster per PR and keeping all wiring calls in `threads.ts`.

---

### web-stores-2 (high) Dead test-fixture entry route threaded through production control flow

Locations: `stores/threads.ts:716-805` (`EntryTranscriptFixtureOptionsForTests`, `entryTranscriptFixtureForTests`,
`installEntryTranscriptFixtureForTests`), branches at 808-811, 829, 5703, 6644, 8454, 8522-8579.

Evidence:
- `rg installEntryTranscriptFixtureForTests` across the whole repo (excluding node_modules) returns only its definition at `threads.ts:762`.
  No test, dev harness or production caller exists. `EntryTranscriptFixtureOptionsForTests` is referenced only by the same file.
- The never-set variable still gates live code: `entryTranscriptRoute` (808: `if (entryTranscriptFixtureForTests?.ref === ref) return ...;
  811: if (entryTranscriptFixtureForTests) return undefined;`), `entryTranscriptRoutes` (829), `publishAndReconcileThreadHydration`
  (5703: `const productionEntry = entryTranscriptFixtureForTests?.ref !== ref;`), `refreshTrackedThread` (6644: `|| entryTranscriptFixtureForTests?.ref === ref`),
  a cache-epoch branch (8454) and a 60-line branch in `clearCachedSessions` (8522-8579: `const entryFixture = ...; if (entryFixture) {...}`).
- `EntryTranscriptRoute` itself is named after the fixture (`extends EntryTranscriptFixtureOptionsForTests`, 723) even though every real route is a
  "production" route (`productionEntryRoutes`, 757).
- `lastLocalOpenStatus` (route field, ~744) is documented "Read-only test evidence ... never consulted by product logic" but is exported through
  `productionEntryTranscriptForTests` (679-695).

Impact: roughly 120 lines of dead code, eight branches a reader must prove irrelevant to understand the production path, and a misleading route
type name.

Fix plan: delete `installEntryTranscriptFixtureForTests`, `entryTranscriptFixtureForTests`, the options type, and every branch listed; collapse
`productionEntryRoutes` + `entryTranscriptRoutes()` to one map; rename `EntryTranscriptRoute` fields accordingly and inline `cache/clientId/hostId`
defaults. In `clearCachedSessions` keep only the non-fixture path. Guard: `threads.entryProduction.test.ts`, `threads.sessionCache.test.ts`,
`threads.entryReadCut.test.ts`, `threads.entryPostCut.test.ts`. Drop `lastLocalOpenStatus` unless a test reads it (grep first; if only tests read it,
keep it behind `productionEntryTranscriptForTests`). No doc change expected (the fixture is not documented in `docs/`; confirm with
`rg installEntryTranscriptFixture docs`).

Size: ~-150 lines. Risk: low (dead path). Do this before web-stores-1 step 1(b).

---

### web-stores-3 (medium) Test seams live in production modules: 41 `ForTests` exports, hand-written resets, `ForTesting` methods, test-mode checks

Locations: `stores/threads.ts:674-695, 853, 1993, 2020, 3675, 5210, 8382, 8691-8730, 8845-8990`; `stores/transcriptRepository.ts:341, 570-670, 1367`;
`stores/entryWriteAdmission.ts:293, 323`; `stores/mutationClientIdentity.ts:30`; `stores/backgroundTranscriptOptions.ts:21`;
`stores/projectionWork.ts:38-46`; `stores/threads.ts:8373` (`import.meta.env.MODE !== "test"`); every store with `reset*ForTests` (listed by
`rg "^export .*ForTests"`: 28 definitions across 24 files).

Evidence:
- `TranscriptRepository` (production class) exposes `getRetentionSnapshotForTesting`, `getRecoveryStateForTesting`, `getAdmissionSnapshotForTesting`,
  `retainedInventoryForTesting` (570-670, ~100 lines); `TranscriptReader` interface (341) exposes one too; `EntryWriteAdmission.snapshotForTesting` (293).
- `defaultCacheChannelFactory` (8372-8373) is `typeof BroadcastChannel !== "undefined" && import.meta.env.MODE !== "test" ? new BroadcastChannel(name) : null`:
  production behavior differs under test mode.
- Unused seams: `threadRoutingIndexesForTests` (8982, 0 callers anywhere though its comment says a differential test uses it) and
  `resetEntryWriteAdmissionForTests` (323, 0 callers).
- Each store hand-rolls its reset (`resetXStoreForTests`), and `resetThreadsStoreForTests` calls siblings by name (`resetActivityPanelStoreForTests`,
  `resetTasksPanelStoreForTests`, `resetHumanNoteDrafts`). A store added without a call from the right reset leaks into 117 test files.
- `docs/developing-evener/testing.md` already says tests should assert behavior.

Impact: production bundle carries test accessors; reset coverage is by convention; tests couple to private state through "snapshot" accessors, which
makes refactors (web-stores-1, -16) expensive.

Fix plan: (1) delete the two unused seams; (2) add `stores/testReset.ts` exporting `registerStoreReset(fn)` and `resetAllStoresForTests()`;
every store registers its own reset at module load, `testSetup.ts` calls `resetAllStoresForTests()` in `afterEach`, and the per-store exports
remain only where a test needs a single store (delete the rest after migrating call sites: 1,680 calls, mostly `beforeEach` boilerplate that becomes
redundant, deletion is mechanical); (3) move `*ForTesting` snapshot methods behind a `testing/` helper that reads through the public repository
`getSnapshot()` plus a narrow `inspect()` object created only by `createTranscriptRepositoryForTest`; replace the `MODE !== "test"` check with a
factory injected by `AppShell` bootstrap, defaulting to `null` in `threads.ts`. Guard: full store tests in CI; add one meta-test that fails when
a module under `stores/` calls `createStore(` without registering a reset.

Size: ~-600 lines of reset boilerplate/seams, +80 for registry. Risk: low-medium (test ordering). Needs a quick Jesse check on the injected-factory
choice since it changes how the cache channel is wired.

---

### web-stores-4 (high) Tracked and watched thread hydration are parallel implementations with parallel state

Locations: `stores/threads.ts:3838-3958` `hydrateAndSubscribe` vs `4032-4088` `hydrateAndSubscribeWatch`; `5448-5527` `beginThreadHydration` vs
`beginWatchedHydration`; `5609-5875` `publishThreadHydration`/`publishAndReconcileThreadHydration` vs `5875-5925` `publishWatchedHydration`;
`1797-1960` `putThreadModel/putWatchedThreadModel/removeThreadModel/removeWatchedThreadModel`; `6589-6616` `retryTrackedHydration` vs
`retryWatchedHydration`; `6630-6840` `refreshTrackedThread/Now` vs `refreshWatchedThread`; `7230-7414` `ensureThread` vs `7476-7592` `watchThread`;
`7420-7475` `releaseThread` vs `7593-7625` `releaseWatchedThread`; state `660-662` vs `3723-3741`.

Evidence:
- `hydrateAndSubscribe` and `hydrateAndSubscribeWatch` share the same skeleton: acquire subscription lease, `heldSnapshotFor`, read with held snapshot,
  `shouldRetryWithoutHeldSnapshot` second read without it setting `discardHeldHistory`, catch -> `markThreadDeletedIfFenced` ->
  `publishThreadRepairing(...)` -> history-failed vs `scheduleOwnedHydrationRetry`, success -> `publishThreadRepairing(..., false)` ->
  `!discardHeldHistory && baseModel.history !== undefined ? apply... : hydrateThread(...)` -> `applyHydrationResponseCut`. The watch copy even
  says "See hydrateAndSubscribe's identical comment" (4081) and "same per-ref recording as hydrateAndSubscribe's own catch" (4055).
- The watch loop (7476-7592) re-implements the `ensureThread` retry loop line for line with the `inflightWatch*` maps: three parallel maps for
  tracked (`inflightHydrates`, `...Clients`, `...Epochs`) and four for watched (`inflightWatchHydrates`, `...Clients`, `...Epochs`, `...IncludeTurns`),
  all cleared in the same `.finally`. The `ownedHydrations` abstraction (6463-6540, keyed by `kind: "thread" | "watched"`) shows the intended unification,
  which stopped halfway.
- Drift already visible: the tracked path has a background `signal`/AbortController and entry-repository branch (3855-3870, 3911-3925); the watch path
  does not. The watch catch gates repair publishing on `includeTurns || isRebuilding` (4059) while tracked publishes unconditionally. These look intentional but are
  documented only by comments pointing at each other.

Impact: every hydration fix must be made twice and the two have already diverged in details nobody can verify at a glance; it is the core of
the 8,990-line size.

Fix plan: introduce one `HydrationOwner<K extends "thread"|"watched">` record `{ pendingMap, inflight: {promise, client, epoch, includeTurns},
put, remove, schedule retry }` and one `runHydrationLoop(owner, ref, needs)` containing the retry loop; `ensureThread` and `watchThread` become
~30-line wrappers supplying the owner and the "satisfied" predicate (`hydrated && (!needTurns || hydratedIncludeTurns)`). Merge the two `hydrateAndSubscribe*`
functions into one with an `options: { background?: AbortSignal; includeTurns: boolean; fallbackToRepository: boolean }`. Do this after web-stores-1
step (e) so it happens inside one module. Guard first: pin both behaviors with the existing `threads.backgroundRecovery/Settle/Owners`, `threads.entryMembership`,
`threads.connection` tests, and add two characterization tests for the differences listed above before merging code (TDD for the pinned behavior).

Size: -400 to -600 lines net. Risk: high (ordering and race fences; the comments in 3838-4088 document many). Do not combine with other changes.

---

### web-stores-5 (medium) Mega-functions with positional-flag parameters and pass-through locals

Locations: `stores/threads.ts:6172-6428` `handleNotification`; `6630-6840` `refreshTrackedThread`/`refreshTrackedThreadNow`;
`4727-4945` `enqueueMutationIntent`; `7669-7845` `loadOlderTurns`; `2645-2800` `getMutationRuntime`; `stores/transcriptRepository.ts:1323-1632`
`attachReader`, `2328-2743` `#queueWrite`, `1080-1270` `loadOlderPage`; `stores/hostOps.ts:1248-1408` `restart`.

Evidence:
- `refreshTrackedThread(client, epoch, ref, targetedResync, reportFailure = false, beforePublish?, reopenOnlyClientMutationId?, intent = "foreground")`
  (6630) and `refreshTrackedThreadNow` (+ `signal`) take 8-9 positional parameters; callers write `refreshTrackedThread(wiredClient, readyEpoch, ref,
  true, false, undefined, undefined, hydrationIntentFor(ref))` (6252-6261, 6417-6423). 15 call sites in the file. `refreshTrackedThread`'s body forwards
  all parameters twice (6646-6655, 6666-6676).
- `handleNotification` computes `refs = "ref" in route ? [route.ref] : [...(currentRefsByThreadId.get(route.threadId) ?? [])]` three times (6217, 6259, 6377);
  creates pass-through aliases `const publishableThreads = nextThreads; const publishableChangedThreads = changedThreads; const publishableEndedResumeOnly =
  endedThreadsResumeOnly;` (6338-6340) that carry no meaning; and interleaves routing, repository acceptance, clear-replay capture, fallback invalidation,
  frame-time accounting, state publish and re-read issuing in one 256-line body.
- `PAGE_LIFETIME_NONCE`, `comparePosition` and similar helpers in `transcriptRepository.ts` sit above a 3,000-line class with 300-line methods.

Impact: hard to review, and the positional `undefined, undefined` calls are a bug vector.

Fix plan: (1) replace the trailing positional parameters of `refreshTrackedThread*` with one options object (`{ targetedResync, reportFailure, beforePublish,
reopenOnlyClientMutationId, intent, signal }`), mechanical across 15 sites; (2) split `handleNotification` into `routeNotification` (compute refs once),
`foldNotification` (reducer + prune), `settleAfterFold` (fallback invalidation, resume-only obligation clear, invalidation re-reads), deleting the alias
locals; (3) same pattern for the other functions as part of web-stores-1/-16. Guard: existing notification routing tests
(`threads.notificationRouting.test.ts`, `threads.entryAuthority.test.ts`).

Size: -150 lines, +100 for named helpers. Risk: low-medium for (1)-(2).

---

### web-stores-6 (medium) Session-action boilerplate in `threads.ts`: five identical RPC wrappers and five composer variants

Locations: `stores/threads.ts:7973-8003` (`setModel`, `setReasoningEffort`, `setVisionModel`), `8064-8081` (`rename`, `compact`), `7846-7960` (`send`, `steer`,
`queue`, `drainAsSteer`), interface declarations 356-440 (the `ThreadsStoreState` action signatures, `send` 363, `steer` 376, `queue` 384, `drainAsSteer` 399
each ~200 lines of comment+signature per heuristic count; read them to judge).

Evidence:
- Five methods are the same body: `const client = requireClient(); try { await client.request(<method>, {ref, ...}); } catch (err) { throw mapConflict(err); }`
  (7973-7980, 7982-7989, 7991-7998, 8064-8071, 8073-8080). `setGoal` and `removeURL` repeat the try/mapConflict shell with extra logic.
- `send/steer/queue/drainAsSteer` take six positional parameters `(ref, text, attachments, skillNames, commandNames, mentions)` and forward the same
  six to `composerMutationIntent(ref, "<route>", ...)` (7846-7850, 7894-7898, 7900-7912, 7939-7943); `ComposerMutationRoute = "send"|"queue"|"steer"|"drain"` (142)
  already is the discriminator.
- The state interface repeats the same long doc comment per sibling.

Impact: small but pervasive, and any new action copies a wrapper.

Fix plan: add `async function requestMapped(method, params)` (requireClient + mapConflict) and make the five methods one-liners; introduce
`ComposerInput = {text, attachments, skillNames, commandNames, mentions}` as a single parameter for the four composer routes (call sites in
`panes/session/composer/*` and `shell/palette` change mechanically; check `rg "\.(send|steer|queue|drainAsSteer)\(" src`). Guard: `threads.sessionActions.test.ts`
(spy on `client.request`, assert method and params, not messages).

Size: -90 lines, ~40 call-site edits. Risk: low. Needs Jesse's nod only if he wants to keep positional arguments stable for mobile parity (native has its own path).

---

### web-stores-7 (medium) Three reimplementations of "guarded random id with Math.random fallback"

Locations: `appwire-client/typescript/state/mutation/secureUUID.ts:1-54` (canonical `createSecureUUID(source)`), `stores/secureUUID.ts:1-14` and
`stores/browserRandomSource.ts:1-20` (web wrappers), `stores/versionedChannel.ts:25-32` `makeSourceId`, `stores/transcriptRepository.ts:110-124`
`PAGE_LIFETIME_NONCE`; note comments at `shell/routing.ts:281`, `shell/attentionHistory.ts:21`.

Evidence:
- `makeSourceId` (try `crypto.randomUUID()`, else `${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`) and the `PAGE_LIFETIME_NONCE` IIFE
  (try `randomUUID`, else `getRandomValues` -> hex, else `Date.now().toString(36)-Math.random()...`) are both hand-written versions of what
  `createSecureUUID(browserRandomSource())` already does, including the `getRandomValues` and last-resort fallbacks.
- `stores/secureUUID.ts` + `browserRandomSource.ts` split one 3-line idea (`createSecureUUID(browserRandomSource())`) across two files; `browserRandomSource.ts`
  is consumed by only `secureUUID.ts` and `mutationClientIdentity.ts`.
- `makeSourceId` is used for cross-tab channel source ids (`threads.ts:8354`, `transcriptDisplay/crossTabSync.ts:99`) and draft checkpoint ids
  (`draftStorage.ts:73`).

Impact: three subtly different fallback formats for ids that are all compared for equality; any future hardening gets applied to one.

Fix plan: keep `createSecureUUID` as canonical; make `stores/secureUUID.ts` the only web entry (inline `browserRandomSource` into it and export both
`createSecureUUID` and the source for `mutationClientIdentity`); delete `makeSourceId` and `PAGE_LIFETIME_NONCE` bodies in favor of `createSecureUUID()`
(the "insecure-" fallback id is still unique per load). Update `versionedChannel.ts` doc comment, `crossTabSync.ts:9` re-export, `draftStorage.ts`.
Guard: `browserRandomSource.test.ts`, `versionedChannel.test.ts`, `crossTabSync` tests; add a test that each former call site yields distinct ids when
`crypto` is absent.

Size: -45 lines. Risk: low.

---

### web-stores-8 (low) Position comparator copied into three modules

Locations: `appwire-client/typescript/reducer.ts:3564` (`comparePositions`, exported), `stores/transcriptRepository.ts:134-139`, `stores/sessionEntryCache.ts:210-214`
(null-aware) plus `compareNullablePosition` 230-235 and `positionKey` 226; used 34 times in `transcriptRepository.ts`, 26 in `sessionEntryCache.ts`, 4 `comparePositions` in
`threads.ts` (import from the package).

Evidence: all three bodies are `left.entry - right.entry || left.item - right.item || (left.sub ?? 0) - (right.sub ?? 0)`, differing only in null handling. The package
version already handles `undefined`.

Impact: tiny, but position ordering is the key invariant of the transcript cache; a fix to one copy (for example sub-position semantics) will skip the others.

Fix plan: export one `comparePositions` from the package (already exported) and a null-tolerant variant (`null` sorts first) from the same place; replace both local
functions and `compareNullablePosition`. Guard: `sessionEntryCache.test.ts`, `transcriptRepository.test.ts`.

Size: -25 lines. Risk: low.

---

### web-stores-9 (medium) Pass-through re-export shims and web tests of package classes

Locations: `stores/hostRouting.ts:1-9` ("Compatibility entry point for browser consumers; the SDK owns host routing."), `stores/mutationDispatcher.ts:1-5`,
`stores/mutationOutbox.ts:1-37`, `stores/secureUUID.ts`, `stores/archivedList.ts:28-33` (aliases `refreshArchivedList = archivedListStore.refresh` ...),
`stores/navigation/selectors.ts:12-34`; tests `stores/mutationDispatcher.test.ts` (1,408 lines), `stores/mutationOutbox.test.ts` (1,330 lines).

Evidence:
- `hostRouting.ts` is 9 lines of re-export from `@evener/appwire-client`; `rg "stores/hostRouting"` finds 35 importers in `panes/`, `shell/`, `widgets/`, `stores/`.
  The package exports the same names (`appwire-client/typescript/hostRouting.ts`).
- `mutationDispatcher.ts` re-exports `MutationDispatcher` and `validConsumedClientMutationIds`; `mutationOutbox.ts` redefines six record types as
  `PackageX<MutationAttachment>` and re-exports the class. Its own header says "so its importers do not move" (mutationDispatcher.ts:3-4), which is the
  backward-compat-shim pattern Jesse's rules require approval for.
- The package has no `dispatcher.test.ts` or `outbox.test.ts` (`ls appwire-client/typescript/state/mutation` shows tests for records, queue, pending entries but not
  dispatcher/outbox); the 2,738 lines of web tests exercise package classes through the shim over fake IndexedDB. Native tests the same classes in
  `mobile-native/src/mutationOutboxStorage*.test.ts` and `nativeMutationRuntime.test.ts`.
- `docs/product/README.md` says the shared client owns shared derivations; tests of shared logic belong with it.

Impact: indirection that hides the owner, and package behavior is only verifiable by running the web suite.

Fix plan: (1) repoint the 35 `hostRouting` importers to `@evener/appwire-client` (per AGENTS.md package-import rule) and delete `hostRouting.ts`; (2) repoint
`threads.ts`, `humanNoteDrafts.ts`, `mutationOutboxIndexedDB.ts` to `@evener/appwire-client/state/mutation` and keep only the `MutationAttachment`-typed aliases in
one file (rename to `mutationTypes.ts`); delete `mutationDispatcher.ts` and the class re-export from `mutationOutbox.ts`; (3) move the dispatcher/outbox behavior tests
whose subject is package logic into `appwire-client/typescript/state/mutation/` against an in-memory `MutationOutboxStorage` (the native tests already show a port-level
conformance shape; reuse `mutationOutboxIndexedDB.conformance.test.ts` as the shared conformance), keeping in the web only tests of `MutationOutboxIndexedDB`. Needs
Jesse's decision on (3) since it moves 2.7k test lines and touches the package qualification script `appwire-client/typescript/scripts/qualify-package.mjs`.

Size: -80 lines shims, 35 import edits, ~2,700 test lines relocated. Risk: low (1-2), medium (3).

---

### web-stores-10 (medium) `connection.ts` monkey-patches the package core store in place

Locations: `stores/connection.ts:20-95`.

Evidence:
- It builds `core = createConnectionStore()`, then overwrites `core.getState`, `getInitialState`, `setState`, `subscribe` on the same object via a cast
  (`const mutableCore = core as unknown as Pick<...>; mutableCore.getState = () => withConnect(originalGetState()); ...`, 76-87) so that every state read
  carries a `connect` field. The header (1-18) admits the reason: "Every one of this store's ~36 importers calls `connectionStore.getState().connect(client)`".
- It needs a `WeakMap` memo (47-55) to keep snapshots stable for `useStore`, plus unwrap logic in `setState` that strips `connect` (79-85), plus a comment explaining
  an earlier failed attempt ("which is exactly how this broke threads.test.ts's vi.spyOn(connectionStore, 'setState') idempotency check the first time").
- `withConnect` returns a fresh `{...state, connect}` for every distinct core state; `subscribe` wraps both `state` and `previous`.

Impact: fragile (identity semantics, test spies), and ~80 lines to avoid editing 36 call sites.

Fix plan: expose `connect` as a sibling export (`connectClient(client)` or `connectionStore.connect`, as the package core already has `core.connect`), edit the ~36
call sites (`rg "getState\(\)\.connect\("`), delete `withConnect`, the WeakMap, the four overrides and the `ConnectionStoreState` extension. Keep `connectionStore = core`. Guard:
`connection.test.ts`, `threads.connection.test.ts`. Doc: none.

Size: -70 lines, ~36 one-line edits. Risk: low (type-checked). Jesse's approval needed only for the call-site migration.

---

### web-stores-11 (medium) Hand-rolled "follow the connection" lifecycles and request-fencing in six stores

Locations: `stores/transcriptDisplay.ts:420-495` (`detachPackageStore`, `rewireClient`, `onConnectionChange`); `stores/keybindings.ts:45-100` (same three functions);
`stores/readyGenerationCallback.ts:1-49`; `stores/commandCatalog.ts:13-55`; `stores/agentsDoc.ts:70-185`; `stores/credentials.ts:73-82`; `stores/extensions.ts` (own subscription);
`stores/hostOps.ts:686-830` (four per-host sequence maps); `stores/hosts.ts:108-175` (`latestGeneration`, `latestPublishedGeneration`); `stores/threads.ts:6964-7035` `rewireClient`.

Evidence:
- `keybindings.ts` and `transcriptDisplay.ts` contain the same state machine: `let wiredClient, unwireReady`; `rewireClient(client)` = end generation, un-wire ready callback, set wired,
  detach hub, `client.onReady(readyGenerationCallback(client, () => wiredClient, begin))`, begin if `client.state === "ready"`; `onConnectionChange` = rewire on new client, `endReadyGeneration` on
  ready loss for the same client, detach on null client. `readyGenerationCallback.ts` exists only to make the two instances not contend (its 27-line comment).
- `agentsDoc.ts`, `commandCatalog.ts` (`connectionStore.getState().client !== client` at 31 and 41), `credentials` and `hostOps` (`connectionStore.getState().client === client` inside `planIsCurrent`,
  `operationPollIsCurrent`, `restartIsCurrent`) each re-implement "a response may only land if it is the newest request and the client is unchanged". `rg "client !== client|client === client"`
  across `stores/` non-test finds 20 such fences.
- `hostOps.ts` keeps `planSequences`, `restartSequences`, `operationSequences`, `repairSequences` plus four trivial `xSequence(name)` accessors (710-721), `operationRequestSequences`
  and `operationPublishedRequestSeqs`.

Impact: a connection-semantics change (for example a new transition) must be mirrored by hand in each store; the shared predicates (`readyConnectionTransition`,
`onConnectionReplacedOrRecovered`) already exist in `connection.ts` but only half the stores use them.

Fix plan: add to `stores/connection.ts` (or the package `state/connection`, if native wants it) two helpers: (1) `bindReadyGeneration({ begin, end, detach })` implementing the
rewire/onConnectionChange machine once, replacing the copies in `keybindings.ts` and `transcriptDisplay.ts` and deleting `readyGenerationCallback.ts`; (2) `createRequestFence()`
returning `{ begin(): Token; isCurrent(token, client): boolean; supersede(): void }` with the client check built in, used by `agentsDoc`, `commandCatalog`, and keyed per name by
`hostOps` (one `Map<string, RequestFence>` replaces four maps and four accessors). Guard: `keybindings.initialWiring.test.ts`, `keybindings.test.ts`, `transcriptDisplay.test.ts`,
`agentsDoc.test.ts`, `hostOps.test.ts`. Check first whether the package's `readyGenerationFence.ts` already provides (2); if so, bind it instead of adding code.

Size: -250 lines. Risk: medium (ready-generation races are subtle; each store's tests pin them).

---

### web-stores-12 (low) The `useXStore` overload with a `biome-ignore` is copy-pasted 13 times

Locations: `stores/connection.ts:188-201`, `credentials.ts:52-59`, `agentsDoc.ts:219`, `daemonResidents.ts:161`, `extensions.ts:369`, `hubUpdate.ts:126-132`, `keybindings.ts:121-128`,
`launchConfig.ts:177-183`, `navigation/store.ts:15-23`, `prefs.ts:639-646`, `settingsOverview.ts:30-38`, `tasksPanel.ts:21-28`, `threads.ts:8827-8835`, `transcriptDisplay.ts:695-703`
(plus `shell/workspace.ts:938`, `panes/session/composer/askDock/askDockStore.ts:27`, `panes/settings/.../sessionCacheRow.tsx:80`, `marketplacesPlugins/hostStore.tsx:38`).

Evidence: each is `export function useX(): S; export function useX<T>(selector): T; export function useX<T>(selector?) { // biome-ignore lint/correctness/useHookAtTopLevel ... return selector ? useStore(s, selector) : useStore(s); }`,
each with a paragraph explaining why the ternary is not a conditional hook (`connection.ts:189-200`, `tasksPanel.ts:25`, `keybindings.ts:122-124`). Six other stores (`hosts.ts:742`, `hostOps.ts:1727`, `activityPanel.ts`, `settingsHost.ts:108`) export the selector-required form only.

Impact: 20 `biome-ignore` suppressions for one pattern.

Fix plan: add `stores/storeHook.ts` with `export function bindStoreHook<S>(store: StoreApi<S>)` returning the overloaded hook (one suppression there). Each store becomes
`export const useX = bindStoreHook(xStore)`. Guard: `npm run typecheck` plus existing component tests.

Size: -150 lines. Risk: low.

---

### web-stores-13 (low) Transcript-display persistence: duplicate key tables, verify-after-write twice, dead alias block

Locations: `stores/prefs.ts:155-197, 201-264`, `stores/transcriptDisplay/localStore.ts:1-67`, `stores/transcriptDisplay/draftStorage.ts:45-95`, `stores/transcriptDisplay.ts:22-31, 532-548, 648, 671`.

Evidence:
- Same keys spelled in two places: `prefs.ts:155` `TRANSCRIPT_DISPLAY_LOCAL_KEYS = {desktop: "transcriptDisplay.desktop", ...}` (prefixed with `evener.prefs.` in `readRaw`) and
  `localStore.ts:4` `LOCAL_KEYS = {desktop: "evener.prefs.transcriptDisplay.desktop", ...}` written by `localStorage.setItem` directly. The six legacy keys are listed twice:
  `prefs.ts:160` `LEGACY_TRANSCRIPT_KEYS` and `localStore.ts:9` `LEGACY_KEYS`.
- `prefs.ts` has `readTranscriptDisplayLocal/hasTranscriptDisplayLocal/writeTranscriptDisplayLocal/removeTranscriptDisplayLocal` and `localStore.ts` has
  `writeLocal/removeLocal` doing the same job with verify-after-write and a boolean result; `transcriptDisplay.ts` mixes both (`writeLocal` at 532 but `readTranscriptDisplayLocal` at 671).
  `draftStorage.ts` repeats "set then read back, throw if different" (62-65, 92-93).
- Six aliases are exported and never used anywhere: `readLegacyPref`, `hasLegacyPref`, `writeLegacyBool`, `removeLegacyPref`, `migrateLegacyPrefs`, `dualWriteLegacyPrefs` (`prefs.ts:237-240, 258, 265`;
  `rg -w` over the repo returns only the definitions). `hasLegacyPreference`/`removeLegacyPreference`/`removeTranscriptDisplayLocal` are used only by those aliases or tests.
- Raw `localStorage` is reached from ~12 other files outside `stores/` (`shell/palette/recentCommands.ts`, `shell/rail/railExpansion.ts`, `panes/spawn/spawnDefaults.ts`, `shell/DockHost.tsx` ...), each with its own try/catch.

Impact: two sources of truth for storage keys that must match byte for byte; dead surface suggesting a rename that was abandoned.

Fix plan: make `prefs.ts` the only key owner; add `localStore`-style helpers there (`writeVerified(name, value): boolean`, `removeVerified`) used by `localStore.ts` (which shrinks to
`verifyLegacyWrite`/`reportStorageResult` or folds into `transcriptDisplay.ts`) and by `draftStorage.ts`; delete the six aliases and the unused helpers. Do NOT touch the legacy dual-write
behavior (it is a live compatibility promise; discuss with Jesse before dropping it). Guard: `prefs.test.ts`, `transcriptDisplay.test.ts`, `transcriptDisplay.adapter.test.ts`, `draftStorage` tests.

Size: -80 lines. Risk: low.

---

### web-stores-14 (low) Dead and test-only exports

Locations: `stores/archivedList.ts:35` `useArchivedList` (0 uses repo-wide), `stores/sessionCacheSchema.ts:117` `EntryClearEpochRow` (0 uses), `stores/threads.ts:8982`
`threadRoutingIndexesForTests`, `stores/entryWriteAdmission.ts:323` `resetEntryWriteAdmissionForTests`, the six `prefs.ts` aliases (web-stores-13), `stores/threads.ts:762`
(web-stores-2). Production exports used only by tests: `resumeStopFence` (2999), `removeTranscriptDisplayLocal`, plus the test helpers listed below.

Evidence: script result of `rg -w` over 433 exports yielded 37 with no non-store, non-test reference; after removing helper exports in `testing/` and ones with test users, the confirmed
zero-reference set is the first six lines above. Also `stores/pendingUrlRemovals.ts` has no reset seam although it holds module state (checked by reading, not by usage).

Fix plan: delete the zero-use exports in one commit; run `npm run typecheck` and `make lint-biome` (knip is not used here; typecheck catches importers). Size: -40 lines. Risk: low.

---

### web-stores-15 (medium) Tests assert on copy; hostOps classifier mixes copy and logic

Locations: `stores/hostOps.test.ts:102-158, 255-337, 459-506, 810-846` (28 `refusal.message`/`notice` assertions), `stores/humanNoteDrafts.test.ts:327, 450, 655, 807`,
`stores/mutationOutboxIndexedDB.stall.test.ts:134`, `stores/credentials.test.ts:222, 2992, 3012`, `stores/agentsDoc.test.ts:239, 407`, `stores/commandCatalog.test.ts:120-126`,
`stores/settingsOverview.test.ts:117-171`; source `stores/hostOps.ts:97-200`.

Evidence:
- `AGENTS.md`: "Never assert on phrasing: no test may check that copy, error messages, or other human-readable strings match a hardcoded phrase or a constant imported from the code."
  Examples: `expect(refusal.message).toContain("An open teardown remnant is blocking this host (remnant-7).")` (hostOps.test.ts:117), `toContain("Another operation is running on this host (op-42).")` (129),
  `expect(result.current?.error).toBe("Note save was canceled by Stop")` (humanNoteDrafts.test.ts:655), `expect((error as Error).message).toBe("It didn't go through. Try again.")` (stall.test.ts:134).
  Several tests use `friendlyErrorMessage(...)` as the oracle (`daemonResidents.test.ts:150, 204`, `settingsOverview.test.ts:117-171`, `credentials.test.ts:340`), which is a code-imported
  string the rule also forbids.
- `hostOpRefusal` (hostOps.ts:121-200) is a 80-line switch that builds user-facing sentences inside the store layer (`withDetail("This confirmation's plan token is no longer on the server.", detail)`); the kind
  and the sentence are computed together, and `timeoutMessage(context)` (99-110) duplicates the same pattern. The behavior that matters (the `kind`, the `recovery` action table
  `deployRefusalAction`, `planRefusalAction`, ... 208-320) is separately testable.

Impact: copy edits break tests; reviewers skim past failures as "just text". Violates a stated repo rule.

Fix plan: (1) in `hostOps.test.ts` assert `refusal.kind`, `remnantId`/`operationId` fields and the `recovery` arm, not `message`; where the message must carry the hub's own detail, assert that `detail` text the test injected
appears (that is data, not copy); (2) same pattern in the other files (assert `error !== null`, kind, or the injected detail); (3) optionally move the sentence table out of `hostOpRefusal` into
`Record<HostOpRefusalKind, string>` so the function is `{kind, message: withDetail(HEADLINES[kind], detail)}` (removes ~60 lines of repeated `return { kind: info, message: withDetail(...) }`).
Guard: the edited tests plus `npm run typecheck`. Doc: none.

Size: ~-120 lines of test and source. Risk: low.

---

### web-stores-16 (medium) `transcriptRepository.ts` and `sessionEntryCache.ts` hold very large single classes

Locations: `stores/transcriptRepository.ts:367-3100` (`TranscriptRepository`), `attachReader` 1323-1632, `#queueWrite` 2328-2743, `#issueCommit` ~2556-2742, `acceptRead` 702-876, `acceptReadFailure` 690-884, `accept`
914-1272; `stores/sessionEntryCache.ts:731-1805` (`SessionEntryCache`), `commitBatch` 887-1024, `#applyCoverage` 1340-1505, `getLatestWindow` 1505-1607.

Evidence: one class with ~70 members owns reader attachment, local-open restoration, read/page/notification ingestion, write queueing with preflight/retry/abandon bookkeeping, handoff overlay, background
draining and end-of-round resolution (`#startEndDisposition`, `#resolveEnd`, `#drainBackground`). The helper section at the top (44-232) already shows the separable pieces: coverage reach arithmetic
(`AbandonedCoverage`, `widerReach`, `reachWithin`, `commitCarriesCoverage` 148-225), row mapping (`#rowsFromRead`, `#pageItemRows`, `#row` 2022-2265), write queue (`#queueWrite` onwards). The class is the
only consumer of `EntryWriteAdmission` (`entryWriteAdmission.ts`, 325 lines) which is already extracted, showing the pattern works.

Impact: highest cognitive load after `threads.ts`; any change risks the persistence-ordering invariants described in comments.

Fix plan: only after web-stores-1 and with its tests green: extract (a) `coverageReach.ts` (pure, 148-225); (b) `repositoryRows.ts` (pure mapping, 2022-2265, takes `binding`); (c) `writeQueue.ts` class
owning `#queueWrite/#issueCommit/#declinePersistence/#dropForOverflow/#scheduleRetry/#abandon*`; (d) `readerSession.ts` for `attachReader`. Keep `TranscriptRepository` as the facade with the same public
methods. Because the invariants are specified in docs (`docs/product/subsystems.md` S02 transcript-repository paragraph), re-read that paragraph before each extraction and keep the existing 8 repository test
files as the guard. Not a candidate for opportunistic cleanup; a dedicated PR series with Jesse's review (architectural, per CLAUDE.md "discuss before significant restructuring").

Size: ~3,100 lines moved; net ~0. Risk: high.

---

### web-stores-17 (medium) Test suite size and duplicated test plumbing

Locations: `stores/threads.sessionActions.test.ts` (10,661 lines, 220 tests, `describe` blocks at 304, 1362, 1544, 1715, 1845, 2537, 2724, 6462), `transcriptRepository.resources.test.ts` (4,321),
`threads.entryProduction.test.ts` (4,025), `sessionEntryCache.test.ts` (3,541); local `deferred()` in 18 test files (7 in `stores/`: `credentials.test.ts`, `transcriptRepository.resources.test.ts`,
`mutationDispatcher.test.ts`, `threads.readOnlyRequests.test.ts`, `threads.test.ts`, `transcriptDisplay.test.ts`, `threads.sessionActions.test.ts`) although `appwire-client/typescript/testing/deferred.ts` exists;
local `transactionDone` in `sessionEntryCache.test.ts:64` and `sessionEntryCache.lifecycle.test.ts:53`; three independent `deleteDatabase` wrappers (`testing/entryCacheFixture.ts:14`, `testing/stalledIndexedDB.ts:48`,
`testing/legacyOutboxRows.ts`).

Evidence: file sizes above (counts from `wc -l`); 73k test lines vs 26k source. The file named "session actions" also covers send, steer, queue, drain, promote, Stop-cancellation durability and "pending shared
membership acquisition" (6462), which are different subsystems. Mock counts are small (`vi.spyOn/vi.fn` 7 in `threads.test.ts`, 23 in `credentials.test.ts`); tests use `FakeClient` and fake-indexeddb, which matches the
boundary rules in AGENTS.md.

Impact: slow to navigate and run; duplicated helpers drift.

Fix plan: split `threads.sessionActions.test.ts` along the same seams as web-stores-1 (`threads.send.test.ts`, `threads.stopCancellation.test.ts`, `threads.resume.test.ts`, `threads.membership.test.ts` ...) with a
shared `stores/testing/threadsHarness.ts` for the repeated setup; replace local `deferred` with the package helper (`@evener/appwire-client/testing/deferred`) in all 18 files; hoist `transactionDone` and `deleteDatabase` into
`stores/testing/idb.ts`. Mechanical; run each split file in CI.

Size: -400 lines of duplicated helpers; the split is net 0. Risk: low. Order: after web-stores-1 so file boundaries match module boundaries.

---

### web-stores-18 (low) Narrative and history comments and 28% comment density

Locations: pervasive; examples `stores/prefs.ts:6-68, 354-368` ("a persona panel voted 3-1", "2026-08-14 reversal", "wave-7 pre-adjudication", "legacy JS"), `stores/threads.ts:5133-5137` ("Jesse's ruling documents in issue #3313"),
`6228-6234` ("task-15-report.md's store contract"), `7164`, `4001-4004` ("yd16 §4.2", "stop-cancellation-outbox §4"), `8845-8880` ("(kata ycet)"), `stores/projectionWork.ts:5-12` ("kata 3c7t"),
`stores/hostOps.ts:100-197` ("08b §8", "S16"), `stores/sessionCacheSchema.ts:4-5, 20-22` ("6d", "Ruling 37"), `stores/versionedChannel.ts:1-4` ("#3619"), `stores/pendingUrlRemovals.ts:6-8` ("roborev's seventh round asked for exactly that").

Evidence: `rg -n "wave-[0-9]|persona|kata [a-z0-9]{4}|Ruling [0-9]+|task-[0-9]+|spec 0|§[0-9]"` yields 98 hits in non-test store files; `rg "#[0-9]{3,5}"` adds more (28 in `threads.ts`). `threads.ts` has 2,982 comment lines. The
`friction`/`CLAUDE.md` rules forbid comments about how code used to work or what changed.

Impact: comments are the main reason files are huge, many describe review history (not current behavior), and issue/spec ids are unresolvable after the docs move. They also mislead (`prefs.ts` says "legacy" for code that is live).

Fix plan: a comment-only pass per file, done together with the extraction of that file (web-stores-1, -16) rather than as one huge diff: keep comments that state an invariant or non-obvious "why", delete rounds/kata/ruling/persona/PR
numbers and "used to" narration, move product-decision rationale ("title notifications default on") to `docs/web-ui/decisions.md` (it already exists and `prefs.ts` cites it). Do not manually reflow; let Biome format.

Size: -2,000 to -3,000 comment lines over time. Risk: none functionally; review noise.

---

### web-stores-19 (low) Layering: stores import shell/panes, and a registration hook breaks a cycle

Locations: `stores/transcriptDisplay.ts:20` (`../shell/useIsMobile`), `stores/settingsHost.ts:20, 98` (`../shell/routing`, `setSettingsHostRouteSync`), `stores/panelStoreEviction.ts:2-3` (`../panes/zoom/intent`, `../shell/workspace`),
`stores/hubUpdate.ts:26` (`../shell/pageReload`), `stores/threads.ts:72` (`../panes/session/transcript/tools/subagentModuleStore`), `stores/navigation/selectors.ts:9` and `navigation/persistence.ts:7`
(`../../shell/rail/*`), `stores/transcriptDisplay/transitions.ts:8` (`../../panes/session/transcript/flow/transcriptViewRegistry`). Duplicate host-selection split: `stores/settingsHost.ts` (114 lines)
and `panes/settings/settingsHost.ts` (110 lines).

Evidence: 10 upward imports. `settingsHost.ts:1-19` explains that `shell/routing.ts` "cannot import this store itself (the dependency runs one way only)", so `setSettingsHostRouteSync(syncSettingsHostToRoute)` injects it at module
load (line 98). `threads.ts` calls into a pane module's `releaseSubagentRows` from `markThreadDeletedIfFenced` (3961).

Impact: stores cannot be loaded without UI modules; import-order sensitivity.

Fix plan: invert, do not add code: have `subagentModuleStore` subscribe to `threadsStore.deletedRefs` (or to a `registerThreadDeletedListener` exported by threads) instead of threads importing it; move `settingsHost` store state into `shell/routing.ts`'s
module or into `stores/` with `routing.ts` calling it directly (merge the 114-line and 110-line files' split along the real dependency: route-parsing in `shell`, state in `stores`, no injection hook); pass `isMobile` and `reloadPage` into
`transcriptDisplay.ts` and `hubUpdate.ts` through the existing `Deps` pattern (`hubUpdate.ts:42-46` already has it).

Size: -40 lines. Risk: low-medium (module init order); needs Jesse's call on moving `settingsHost` state.

---

### web-stores-20 (low) Test utility files live in the production tree

Locations: `stores/sessionActivityTestUtils.ts` (178 lines), `stores/throwingGetterTestUtils.ts` (19), `stores/testing/*` (5 files), versus `appwire-client/typescript/sessionActivityTestUtils.ts` (142).

Evidence: `sessionActivityTestUtils.ts` imports `FakeClient` from `@evener/appwire-client/testing/fakeClient` and is imported by 20+ test files and two `dev/*-entry.tsx` harnesses, but sits beside production stores and shares a name with the
package's `sessionActivityTestUtils.ts` (`activityRef` is `"remote:owner"` in one and `"remote:session"` in the other; the contexts are built differently, `activityContext(ref)` vs `activityContext(epoch)`).
`throwingGetterTestUtils.ts` is 19 lines used by 2 tests.

Impact: confusion about which helper to use; the AGENTS.md rule that `@evener/appwire-client/testing/<module>` is in-repo test-only already restricts imports to `*TestUtils.*`, so this works, but it grows the production dir.

Fix plan: move both into `stores/testing/` (already the home), rename the web one `sessionActivityHarness.ts` or reuse the package's builders if the contexts can be unified (check the 20 consumers); update imports.

Size: ~0 net, 25 import edits. Risk: low.

---

### web-stores-21 (low) Per-ref panel stores repeat the same Map-plus-evictor skeleton

Locations: `stores/activityPanel.ts:27-110`, `stores/tasksPanel.ts:15-32`, `stores/topNotes.ts`, `stores/humanNoteDrafts.ts`, `stores/panelStoreEviction.ts:1-135`.

Evidence: each store holds `entries: Map<string, Entry>`, replaces the Map on every setter (`const entries = new Map(state.entries); entries.set(ref, {...entry, ...}); return { entries };` repeated 5 times in `activityPanel.ts` alone: 38-44, 51-55, 59-67, 87-90, 97-103), registers a
`registerPanelStoreEvictor({ refs, evict })`, and exports a `resetXStoreForTests`.

Impact: low per store, but each new panel store copies the skeleton and its eviction wiring, and a missed `registerPanelStoreEvictor` call silently leaks state.

Fix plan: add `createRefKeyedStore<Entry>({ empty, evict? })` returning `{ store, update(ref, fn), evictRef }` that performs the copy-on-write and registers eviction; migrate `activityPanel` first (most boilerplate), then `topNotes`. Do not migrate `tasksPanel` (package-owned core). Only
worthwhile if a fourth such store is coming; otherwise leave. (YAGNI call for Jesse.)

Size: -60 lines. Risk: low.

---

## Recommended fix sequence

1. web-stores-2 (delete dead fixture route) and web-stores-14 (dead exports): trivial, low risk, shrink `threads.ts` before anything else. About -190 lines.
2. web-stores-9 steps 1-2 (drop `hostRouting.ts` and the dispatcher/outbox shims), web-stores-10 (remove the `connection.ts` monkey patch), web-stores-12 (single `bindStoreHook`), web-stores-7 and -8 (single id and comparator source). All mechanical, type-checked, independent; can land as five small PRs in any order.
3. web-stores-13 (one owner for prefs keys and verified writes) and web-stores-15 (copy-free tests, hostOps classifier table). Do 15 early since it also unblocks refactors that would otherwise churn copy assertions.
4. web-stores-3 (reset registry, remove test seams from production classes) before the big extraction, so extracted modules register their own reset.
5. web-stores-11 (shared ready-generation binder and request fence), then web-stores-6 and web-stores-5(1) (action boilerplate, options object for refresh).
6. web-stores-1 (split `threads.ts`, one cluster per PR in the dependency order given), followed by web-stores-4 (merge tracked/watched hydration, characterization tests first) and web-stores-5(2) (split `handleNotification`).
7. web-stores-17 (split `threads.sessionActions.test.ts` along the new module seams, shared harness, package `deferred`) alongside step 6; web-stores-20 and -19 as cleanup.
8. web-stores-16 (decompose `TranscriptRepository`/`SessionEntryCache`) last and only with Jesse's explicit go-ahead; web-stores-18 (comment pass) rides along with every extraction; web-stores-21 only if another panel store is added; web-stores-9 step 3 (move package-class tests to the package) once Jesse decides.

Each PR must update `docs/product/subsystems.md` S02 links when file names change, keep tests behavior-based per `docs/developing-evener/testing.md`, run `make test-web` once per PR (CI is the full-suite source of truth), and avoid running Biome from the repo root.
