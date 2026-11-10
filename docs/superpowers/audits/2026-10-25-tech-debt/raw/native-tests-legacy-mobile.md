# Audit: native-tests-legacy-mobile

Scope: mobile-native test files, plus the separate `mobile/` directory.
Worktree: `.worktrees/debt-audit` (origin/main). Read-only audit; no test suites run.

## Overview and inventory

| Tree | Files | Lines | Notes |
|---|---|---|---|
| `mobile/src` | 12 TS (no package.json, no tsconfig) | 32,219 (26,364 test = 82%, 5,855 prod) | `state/conversation.ts` 3,721; `state/conversation.test.ts` 20,457 (406 `it`) |
| `mobile-native/src` tests | 393 `*.test.ts(x)` | 130,678 | prod (excluding `src/dev`) is 71,463 lines, so tests are 1.8x prod |
| `mobile-native/src/dev` + `scripts/demo-hub.mts` | 7 prod + 7 test files | ~5.5k prod, ~4.6k test | scripted fake hub |

Answer to "is `mobile/` dead or legacy?": No. It is the live shared headless core of the phone app (conversation service,
conversation store, new-session service, activity service/store, mutation port). It is mis-named and mis-placed, not dead:

- `mobile-native` imports it by relative path in ~122 files (`../../mobile/src/services/conversation` alone has 122
  importers; `state/conversation` 14, `state/activity` 9, `services/newSession` 12, `state/conversationMutation` 11).
- `mobile-native/metro.config.js:25` special-cases `root/mobile/src` as "shared" source; `tsconfig.check.json:30` includes
  `../mobile/src`; `package.json` has `lint: biome ci src ../mobile/src` and `test:shared`; `scripts/sdk/source-files.env:10`
  lists it as a consumer tree; `docs/product/subsystems.md` S03 links it as "shared mobile services".
- The cmd/evener-hub web frontend does NOT import it (zero hits under cmd/ and appwire-client/ besides test fixtures
  that spell the path as a string).
- The dependency is cyclic: `mobile/src/services/conversation.ts:40-41` and `mobile/src/state/conversation.ts:65,80,90`
  import `../../../mobile-native/src/projectedRows`, and tests import `mobile-native/src/transcriptPresentation`.

Within the live core, one sub-part (activity view/store, ~3.7k lines with tests) has no production reader (finding 2).

Findings below are ordered by severity then leverage.

---

## native-tests-legacy-mobile-1 (medium): `mobile/` is a headless core living outside the app that is its only consumer, with a cyclic dependency

Locations: `mobile/src/services/conversation.ts:40-41`, `mobile/src/state/conversation.ts:65,80,90`,
`mobile/src/state/conversation.test.ts:40-43`, `mobile-native/metro.config.js:25`, `mobile-native/tsconfig.check.json:7,30`,
`mobile-native/biome.jsonc:4,18`, `mobile-native/package.json` (`lint`, `test:shared`), `scripts/sdk/source-files.env:10`,
`packageimportpaths_audit_test.go:193,269`, `hooks_test.go:86,121`, `docs/developing-evener/{README,testing,linting}.md`,
`make/{linting,testing}.mk`, `scripts/hooks/pre-commit:8`.

Evidence: there is a top-level directory with only `src/` (no manifest). Every consumer reaches into it with
`../../mobile/src/...` (122 importers of one file). `mobile/` imports `projectedRows` (1,655 lines) and
`transcriptPresentation` back out of `mobile-native/src`, so the "shared" core cannot be built or tested without the app tree.
The repo's own rule for the other shared tree (`@evener/appwire-client`, "never by a relative path") is not applied to it; the
Metro resolver, vitest config and tsconfig each carry special cases for it. Seven repo files/docs name `mobile/src` as a
separate sweep root purely because of the split.

Impact: every gate (lint, biome, tsc, test, metro, copyonly audit, package-import audit, pre-commit) maintains a second root;
the cycle means the layering the directory name implies does not exist; new contributors will assume `mobile/` is the legacy
twin of `mobile-native/`.

Fix plan (restructuring, needs Jesse's approval before doing): `git mv mobile/src mobile-native/src/core` (or
`session-core`), rewrite the relative imports mechanically (`../../mobile/src/` to `./core/`, depth-adjusted), delete the
`mobile/src` entries from metro.config.js (`shared` predicate), tsconfig.check.json include, biome `lint` script, `test:shared`
script, `source-files.env`, make targets, hooks, the Go audit tests (`packageimportpaths_audit_test.go`, `hooks_test.go`
fixture paths), and update `docs/product/subsystems.md` S03, `session-activity.md:967`, `friction.md:148`, and the three
developing-evener docs. Verification: `tsc` check, metro bundle gate, and the existing suites (run in CI) are the guard; no
behavior change. Do this before findings 2-6 so later PRs touch the final paths.
Size: ~250 lines of import/config/doc edits, 0 logic. Risk: low-medium (big mechanical diff; conflicts with in-flight native PRs; Metro
`shared` origin rewriting must be re-verified by `make test-native-bundle`).

---

## native-tests-legacy-mobile-2 (high): the activity view and store are produced on every read but never read by the app, and a projection throw blocks the whole conversation

Locations: `mobile/src/services/activity.ts` (422), `mobile/src/state/activity.ts` (450), `mobile/src/services/conversation.ts:117,678,870`,
`mobile/src/state/conversation.ts:230-232,2073,2376,2466,3376`, `mobile-native/src/session/nativeConversationSessions.ts:14,34,159-161,297`,
tests `mobile/src/services/activity.test.ts` (1,224), `mobile/src/state/activity.test.ts` (1,590),
`mobile/src/services/conversation.test.ts:1952-1975`.

Evidence:
- Header of services/activity.ts: "projects ... into ActivityView view models the ActivitySheet consumes". `ActivitySheet` exists
  nowhere in the repo except those two files (grep over mobile-native, appwire-client, docs/product).
- Non-test grep of mobile-native/src for `ActivityView`, `WorkEntry`, `.work`, `activity.getState().view`: no reader. The only
  production uses of the store are `createActivityStore()` (nativeConversationSessions.ts:159), `activity.getState()` passed as the
  sink (:161), and `.reset()` (:297).
- `docs/product/session-activity.md` and AGENTS.md say activity belongs to the shared domain reads (sessionActivity store in
  appwire-client); the phone's Activity screen is built from `useSubagentTree`/`sessionActivityBinding`, not from this view.
- The store's only observable output is `NotificationOutcome` ("applied" | "rehydrate" | "ignored"), which feeds the drain
  scheduler (conversation.ts:1514). The decision is tiny (history/updated with a settled turn, or `evener/jobs/treeUpdated`, return
  "rehydrate"; activity.ts:300-330); the rest of the 450 lines patches a view nobody displays.
- `projectActivity` runs `validateHierarchy` which throws `ActivityProjectionError` on duplicate/missing-parent/cycle
  (activity.ts:196-266), and `readProjection` calls it (conversation.ts:678, 870). The test at conversation.test.ts:1951-1975
  ("activity projection throw leaves pair null - all operations fail before wire") documents that a thrown projection makes send,
  queue and every control fail. So an unused view can lock the conversation, against the repo's recovery-over-lockdown rule
  (AGENTS.md). Whether real hubs can emit a missing parent (paged delegate sets) is not proven here; the code path exists.
- `LiveActivityState.generationForTest()` (activity.ts:76) is a test-only method on the production surface, used by 9 test lines.

Impact: ~3.7k lines (880 prod, 2.8k test) of maintained code, a second identity/generation state machine in every session
binding, extra work on every notification, and a fail-closed path with no user value.

Fix plan: (1) Characterize the one live behavior: which notifications make the conversation store request a reread. Write
store-level tests through the public `applyNotification` (settled history/updated and treeUpdated cause one coalesced reread;
others do not; mismatched thread/ref frames are dropped by the store's existing checks). (2) Replace `LiveActivitySink` with a
pure `rereadOutcome(notification)` function in the conversation store (or `core/rereadSignals.ts`). (3) Delete
`services/activity.ts`, `state/activity.ts`, `ActivityView` from `ReadProjection`, the `activitySink` parameters of
openProjected/resumeProjected/rehydrate and `NativeConversationActivity`; drop the activity tests and `generationForTest`.
Update the ~30 test files that construct `createActivityStore()` (goalCommand, approvals, liveImages, memoryContextRows, demo-hub,
nativeMutationDispatch, useOlderHistory...). Docs: `session-activity.md:967` and subsystems S03 only if they mention this view.
Size: -3,300 net lines (-880 prod, -2,800 test, +150 new tests/signal fn). Risk: medium; the reread-trigger semantics must be pinned by
step 1 first. Needs Jesse's approval (deleting an existing implementation).

---

## native-tests-legacy-mobile-3 (high): `createConversationStore` is a 3,065-line closure in a 3,721-line file

Locations: `mobile/src/state/conversation.ts:596-3660`; `rehydrate` 2206-2612 (406 lines), `loadOlder` 2612-2916 (304), `applyNotification`
3399-3590 (191), `bindPendingMutations` 2921-3053 (132), `seatTransientWarnings` 1160 (93), `drainTrailingReread` 1334 (81),
`boundRetainedTurns` 1578 (74), `acceptedMutation` 661 (71), `reconcileCrossTurnReissues` 821 (62).

Evidence: 40 inner `function`s plus the action bag share ~25 `let` variables (`conversationGen`, `bindingEpoch`, `loadOlderToken`,
`errorOwnerRev`, `draftRevision`, `mutationIdCounter`, `boundService`, `boundSink`, `activitySink`...). Row-derivation helpers
(`seatTransientWarnings`, `reanchorTransientWarnings`, `splitClusterAtAnchors`, `activityRunRow`, `idleWarningRow`,
`reconcileCrossTurnReissues`, `applyCrossTurnFallback`, `capAndTruncate`) are pure-ish transforms of `MobileConversation` buried
inside the store closure, while the file's own top comment says rows are "projected through the one projector". Several are only
reachable through 20k lines of store-level tests.

Impact: the largest single maintenance drag in the slice; every fix risks interacting with the shared lets; the 20k-line test file
(finding 5) exists because nothing smaller can be tested.

Fix plan: extract in order, each behavior-preserving, guarded by the existing store tests: (a) pure row transforms to
`core/conversationRows.ts` (or into `mobile-native/src/projectedRows.ts` where the projector already lives, deleting the
mobile-to-mobile-native back-import once moved) with direct unit tests; (b) the drain scheduler (`createDrainScheduler`, line 272,
already a module-level function) to its own file; (c) generation/binding guards (`captureOperationBinding`, `isBindingCurrent`,
`bindingEpoch`) into one small `OperationBinding` helper; (d) split `rehydrate` into read / commit / reread-follow-up. Do after finding 2 so
the sink plumbing is gone first. Consider (a) as a candidate for appwire-client if web needs the same transforms; I did not find a web
duplicate (no hits in `threads.ts` for those names), so keep them native.
Size: ~3,000 lines moved, ~400 net removed. Risk: medium; mitigated by moving only whole functions with no logic edits.

---

## native-tests-legacy-mobile-4 (medium): `send`, `queue` and `interrupt` repeat the same 90-line mutation envelope

Locations: `mobile/src/state/conversation.ts:3092-3197` (send), `3197-3285` (queue), `3285-3360` (interrupt), helper `dispatchMutation` 633.

Evidence: `send` and `queue` differ in 32 diff lines out of ~100 after normalizing the words "send"/"queue": same
`requireControl`, `captureOperationBinding`, draft snapshot, `mutationId`, `set({draft:"", pendingMutation, lastAcceptedMutation:null, error:null})`,
`errorOwnerRev` capture, `dispatchMutation`, `isBindingCurrent` recheck, and the same accept/restore/error branches. `dispatchMutation` was extracted
for the call but not for the surrounding lifecycle. The test side mirrors it: `describe("mutation state - parameterized send/queue/interrupt")`
(conversation.test.ts:1048) had to parameterize.

Impact: a lifecycle fix (as in "Fix round 1 I3", "I1 error-owner rev" comments) must be applied three times; drift is already visible in
the comment variants.

Fix plan: one `runMutation(kind, {requireKind, buildDraftClear})` inside the store (or in the extracted mutation module from finding 3),
with send/queue/interrupt as 5-line callers. Existing parameterized tests are the guard.
Size: -150 lines. Risk: low-medium.

---

## native-tests-legacy-mobile-5 (high): `state/conversation.test.ts` is 20,457 lines, with describe blocks named after review rounds

Locations: `mobile/src/state/conversation.test.ts` (406 `it`, ~90 describes). Examples: line 10463 `"D18 B3 round 5: ..."`, 10720 `"D18 B3 round 6"`,
10933 `"D18 B3 round 8"`, 12498 `"Residual 1: same-epoch-like rebind..."`, 17068 `"Residual 1: deferred trailing reread..."`,
13030/11865/14334/18910 `"I1: ..."` (four different I1s), 12309..., `"C1: ..."` x3, `"F5: ..."` x2, `"R1"`, `"R2"`, `"M1"`, `"Task3"`.

Evidence: 30 describes start with a review-tracker label (F/I/C/R/M/D/Residual/Task N); 122 comment lines in the file reference
"round N", "Fix round", "Residual", "D18/D23". The same label is reused for unrelated topics, so the label cannot locate anything.
Setup helpers are re-declared mid-file inside describes (`makeThread` 12816, `makeTurn` 12838, `rows` 13004, `outbox` 19841,
`createStorage` 19863, `withWatchdog` 12595) so the first 660 lines of fixtures are not the only ones. The file imports
`mobile-native/src/projectedRows` and `transcriptPresentation` (lines 40-43), so even its fixtures cross the cycle in finding 1.

Impact: no one can find where a behavior is tested; the file is too large to edit safely in an editor or review; naming narrates PR
history, which the repo's comment rules forbid.

Fix plan: split by behavior into `core/conversation/*.test.ts` sharing one `conversationTestkit.ts` (FakeConversationService,
FakeLiveActivitySink-or-successor, makeConversation, wire fixtures): open/close/reset, paging (loadOlder), notifications/frames, rehydrate,
mutations (send/queue/interrupt), pending rows/outbox, scheduler/rereads, display bound/truncation, questions/warnings. Rename describes
by behavior; delete the per-round comments (history belongs in git). Do not delete tests during the split; afterwards look for duplicates
(e.g. the three "C1: wrong service" tests, two "Residual 1" tests assert the same binding rule). Do this split before or with finding 3 so
extracted modules get their own test files.
Size: ~20,000 lines moved, expect -1,500 to -3,000 from deduped setup. Risk: low (move-only), reviewer-heavy.

---

## native-tests-legacy-mobile-6 (low): history narration and stale anchors in the core

Locations: `mobile/src/state/conversation.ts:989,1085,1541,1605,2816` (`RoboRev review round N`), 24 lines matching
`round N|Fix round|Residual|D18|D23` in that file; `docs/product/friction.md:148`; unused exports.

Evidence: comments such as `// RoboRev review round 4: the transient warnings seat into it` and `// Fix round 1 I2:` (the same text at two
sites). `friction.md:148` links `mobile/src/state/conversation.ts#L2017` as "native rehydrate"; line 2017 is inside `openProjected`
(rehydrate is at 2206), so the anchor is already wrong. Exports with no importer anywhere: `trimBoundary` (579; used only inside the
file), `CANONICAL_MUTATION_DISPOSITIONS` (services/conversation.ts:279), and ~20 exported-only-for-types names (`LatestReadState`, `ConversationStatus`,
`ActivityProjectionErrorCode`, ...).

Impact: comments narrate history; line-anchored doc links rot with every edit.

Fix plan: delete round-tracker comments (keep the invariant they state, rewritten in domain terms); change friction.md and session-activity.md
links to symbol-level links (file path only, or heading) rather than `#Lnnn`; un-export the unused names. Fold into the PRs of findings 3/5.
Size: -150 lines of comments, ~10 doc link edits. Risk: none.

---

## native-tests-legacy-mobile-7 (low): tests that assert an API is absent, and tests that spy on their own collaborators

Locations: `mobile/src/state/conversation.test.ts:12565-12585` ("Residual 4: no test-only mutable API on the production store": asserts
`flushScheduler` is undefined, twice), `mobile/src/services/conversation.test.ts:1888,1926,1954,2124` (`vi.spyOn(projectModule,
"projectConversation")` / `vi.spyOn(activityModule, "createActivityService").mockImplementation`), `mobile/src/state/conversation.test.ts:18765,18807,18839`
(`vi.spyOn(project, "truncateText")`).

Evidence: the first guards against re-adding a method that was removed; the second group replaces the module's own projector with a thrower to
force the "pair null" path and then asserts calls did not reach the wire. These test the harness wiring more than a product contract, and the
`createActivityService` spy disappears with finding 2.

Fix plan: delete the "absent API" describe. For the throw cases, drive the failure with a real malformed wire thread through the real projector
(the file already has "R3: real malformed/incomplete Thread fixtures through projectConversation" at 14180), and keep one spy-free test per
failure path. Truncation tests should assert byte-bounded output rather than call counts (`cutsOf(truncate...)`).
Size: -150 lines. Risk: low.

---

## native-tests-legacy-mobile-8 (medium): 92 test files re-mock modules the global setup already mocks

Locations: `mobile-native/src/vitestSetup.ts:32-79` (setup mocks expo-crypto, expo-haptics, expo-symbols, react-native-webview,
keyboard-controller, reanimated, expo-glass-effect, expo-sqlite/kv-store), and per-suite mocks.

Evidence (grep over `*.test.ts(x)`): `vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }))` identical to setup line 56 appears in
92 files (96 occurrences; `useFocusAfterModal.test.tsx`, `ConversationScreen.recovery.test.tsx`, `useNativeMutationRecovery.render.test.tsx` declare it twice in
one file). `expo-crypto` is mocked in 51 occurrences despite setup line 32. `expo-sqlite/kv-store` in 27 files. `react-native` is mocked in 159
occurrences, 155 of them with the same shape `vi.mock("react-native", async () => ({ ...(await import("./renderNative.testkit")).nativeModuleMock(), <viewport override> }))`.
The setup file's own header says "A suite's own vi.mock of the same module replaces these", so the copies are redundant, not needed.

Impact: ~250 lines of boilerplate that must be edited together when a native dependency changes; the first thing a new test author copies is the redundant block.

Fix plan: (1) delete the byte-identical expo-symbols mocks (92 files, mechanical; diff each to confirm identity, keep any that differ). (2) Make
the `react-native` default `nativeModuleMock()` part of vitestSetup (hoisted `vi.mock("react-native", ...)`), leaving explicit
per-suite mocks only where they override (useWindowDimensions, ActionSheetIOS). (3) Same for expo-crypto where the value is not asserted
(the setup's counter variant is deterministic; tests that assert on a specific uuid, e.g. `"recovery-entry-uuid"`, keep their own).
Guard: the suites themselves in CI.
Size: -400 to -600 lines, 150 files touched. Risk: low (mock order/hoisting differences; verify with CI).

---

## native-tests-legacy-mobile-9 (medium): ~60 suites hand-roll `{ request } as never` clients and bypass FakeClient's wire-method check

Locations: `appwire-client/typescript/testing/fakeClient.ts:1-40` (the shared fake; rejects method names not in `METHOD_NAMES`) versus, for
example, `mobile-native/src/hubModels.test.ts:5-7`, `launchMcp.test.ts:8,28,33`, `sessionDeletion.test.ts:29`, `pinNavigation.test.ts:13,95`,
`hub/hubUpdates.test.ts` (`hub()`), `hosts/hostsController.test.ts` (`hub()`), `hub/useInstalledPluginCount.test.ts` (`hub()`),
`organizationRecovery.test.ts:31`.

Evidence: 68 native files construct `new FakeClient(`; a further 62 files define `request: async (...) =>` or `request: vi.fn` without importing
FakeClient (computed by grep; a few are false positives where `request:` is not a client). `fakeClient.ts` explains why this matters: a hand-built
double let the `evener/dirs/complete -> evener/paths/complete` rename "ship a broken picker past a green suite". Native tests contain 1,177 occurrences of
`as never` / `as unknown as` (ConversationScreen.send.test.tsx 131, BoardScreen.test.tsx 77, PluginsPage.test.tsx 60, ProvidersPage.test.tsx 56),
many of them forcing these doubles into `ConversationClientLike`. Three suites each hand-write a "pending promise queue" client
(hostsController, useInstalledPluginCount, ConversationScreen.recovery) and two suites (`sessionActivityLeases.test.ts`, `sessionActivityBinding.test.ts`) each
register near-identical `thread/read`, `activity/read`, `delegates/list`, `jobs/list` handlers on FakeClient.

Impact: those suites can pass against method names or payload shapes the hub does not have; the casts remove tsc as a second guard.

Fix plan: add to the shared fake (or `renderNative.testkit.scriptedClient`, line 613, which already exists) a `deferredRequests()` mode (the
take/count pending queue) and a `sessionActivityHandlers(client, overrides)` helper in `subagents/sessionActivityTestUtils.ts`. Migrate suites that script wire
methods (launchMcp, sessionDeletion, pinNavigation, organizationRecovery, hubUpdates, hosts, plugin count, hubModels) first; leave pure
timing suites that never name a method. Needs no product change. Note appwire-client/testing is the shared home, so the pending-queue helper belongs there if web
suites need it too (check `cmd/evener-hub/frontend` for the same hand-rolled pattern before adding).
Size: net -600 lines across 40 files. Risk: low.

---

## native-tests-legacy-mobile-10 (medium): copy-as-oracle assertions remain in native tests

Locations (examples verified by opening): `mobile-native/src/SessionMenu.test.tsx:127-128` (`labels` contains "Find in session"),
`board/BoardScreen.test.tsx:1033-1034` (`confirm.message` toBe "Its sessions stay; they're only unpinned."), `board/boardData.test.ts:550`
(`snapshot.error` toBe "The hub went away." where the test supplied that string, which is fine, but the pair at 545-546 shows the pattern),
`session/Composer.test.tsx:85` (`placeholder` toBe "Tell the agent something..."), `TimelineItem.test.tsx:1029` (accessibilityLabel toBe the full
phrase), `sheet/useSheet.test.ts:45`, `tasksRead.test.ts:28` (`sentence` toContain "Reconnect failed to load tasks"), `session/sessionState.test.ts:61,90`.

Evidence: `docs/developing-evener/testing.md` "Copy Is Not a Test Oracle" forbids asserting rendered text equals a hardcoded phrase. In
`mobile-native/src` tests I count 107 `accessibilityLabel).toBe/toContain(` assertions and 37 `.title/.message/.placeholder` string assertions
(top files: NewSessionForm.test.tsx 18, BoardScreen.test.tsx 18, trayLine.test.ts 17, BoardRow.test.tsx 12, PluginsPage.test.tsx 10). The
`copyonlyAudit` in the frontend only fails tests whose whole claim is copy, so these per-assertion cases pass the gate. Some of the 107 are
fixture names echoed through a label (legitimate to locate a control); the product-copy ones need judgement, so I did not count a precise violation number.

Impact: wording edits break unrelated tests; the tests cannot fail for behavioral reasons.

Fix plan: triage the top five files. For labels used to find a control, locate by role/testID and assert the state (disabled, selected, alert
shown, callback payload). For confirmations/alerts assert `buttons` styles and that destructive action only runs on the destructive button
(already partly done at BoardScreen.test.tsx:1035). Where a label carries structured facts (count, state word), assert via the function that builds the
facts (`whyReason`, `subagentLine`) rather than the sentence. No allowlist additions.
Size: ~300 lines touched. Risk: low.

---

## native-tests-legacy-mobile-11 (medium): shared test helpers are redefined per file

Locations: `mobile-native/src/singleFlight.test.ts:4`, `navigationPages.test.ts:117`, `board/navigationHubTestUtils.ts:71` (`tick`, three definitions);
`mount` defined as a local function in 75 test files; `texts` in 11; `press` in 15; 12 `flush/settle/flushPromises` variants; testkit homes:
`renderNative.testkit.tsx` (800+ lines, exports `settle`, `settleMicrotasks`, `until`, `render`), `conversationPlatform.testkit.tsx`, plus `*TestUtils.ts`
files (`board/navigationHubTestUtils.ts`, `board/organizationTestUtils.ts`, `hosts/hostsTestUtils.ts`, `providerSignInTestUtils.ts`,
`subagents/sessionActivityTestUtils.ts`).

Evidence: `renderNative.testkit.tsx` already exports `settle()` (729) and `until()` (740), yet `tick` (setTimeout 0) is re-declared in three places and 741
`await act(async () => {` blocks remain inline. Two naming conventions for test-only modules coexist (`.testkit` and `*TestUtils`); AGENTS.md names `*TestUtils.*`
as the pattern for test-only files elsewhere.

Impact: ordinary duplication; makes the global timing guarantees (fake timers, microtask drain) inconsistent between suites.

Fix plan: replace local `tick`/`flush` with testkit `settle`; hoist the clone-heavy `mount` patterns into `renderNative.testkit` only where >=5 suites share an identical body
(check by diff first; `mount` varies in what it wraps, so do not force a single helper); rename `*.testkit.tsx` to `*TestUtils.tsx` or document the split once in
`docs/developing-evener/testing.md`. No new widgets or production changes.
Size: -300 lines. Risk: low.

---

## native-tests-legacy-mobile-12 (medium): giant multi-topic native test files

Locations: `mobile-native/src/ConversationScreen.send.test.tsx` (7,195 lines, 253 `it`, 37 `vi.mock` lines), `board/BoardScreen.test.tsx` (5,871; 185 `it`),
`hub/PluginsPage.test.tsx` (3,743; 89 `it`), `hub/ProvidersPage.test.tsx` (2,392), `ConversationScreen.sheets.test.tsx` (2,010), `projectedRows.test.ts` (2,489),
`nativeMutationRuntime.test.ts` (2,275), `demo-hub.test.ts` (2,035).

Evidence: `ConversationScreen.send.test.tsx` is named for sending but its local helpers show reader position (`seedReaderPosition` 1881, `layOutRow` 1899, `scrollTo` 1907,
`transcriptList`), dock sizing (`expectOnlyTheDockSlotShrinks` 624), failed turns (`failedTurn` 3048), menus (`chooseMenu` 5304) and toasts. The sibling files `.memoization`,
`.recovery`, `.retryJourney` show the intended split pattern, but it was not continued. Each of these files holds its own `hubClient`/`mount` copy (finding 9, 11).

Impact: slow to navigate and review; hits CI worker isolation unevenly (one 7k-line file per fork worker).

Fix plan: split `send` by topic into `ConversationScreen.{send,queue,scroll,dock,failure,menus}.test.tsx` sharing one `ConversationScreen.testkit.tsx` for `hubClient`, `mount`, `press`,
`type`, `thread`; do the same for BoardScreen (search / rows / organization / hubs) and PluginsPage. Pure move first, dedupe second.
Size: ~20,000 lines moved, -1,000 deduped. Risk: low.

---

## native-tests-legacy-mobile-13 (low): a second hub implementation for demos, with 4.6k lines of tests for the fixtures

Locations: `mobile-native/scripts/demo-hub.mts` (1,020), `src/dev/demoFleet.ts` (1,705), `demoSessions.ts` (1,420), `demoSetup.ts` (582), `demoSubagents.ts` (342),
`demoSessionActivity.ts` (228), `demoOlderHistory.ts` (113); tests `src/demo-hub.test.ts` (2,035), `dev/demoFleet.test.ts` (1,261), `demoSessions.test.ts` (566),
`demoSetup.test.ts` (440), `demoSubagents.test.ts` (163), `demoSessionActivity.test.ts` (150).

Evidence: demoFleet.ts:1-9 says it is "transcribed" from a browser prototype `docs/design/mobile/redesign/prototype/data.js`; README.md:256-274 describes it as "a scripted
WebSocket boundary" with no Evener or LLM. It reimplements hub read semantics (navigation, activity, search, archived, delegates/jobs/watches) in TS, ~5.5k prod
lines plus ~4.6k test lines. It is used only by the explicit demo script and its own tests (nothing outside `src/dev` imports it besides `demo-hub.test.ts`).
CLAUDE.md/AGENTS.md say e2e work uses a real hub. Because the fixtures mirror hub responses they can drift from the Go hub exactly like hand-rolled test clients.

Impact: ~10k lines to keep in sync with the wire; the tests mostly check the fixture against itself.

Fix plan: this is a product decision, not a refactor; ask Jesse whether the demo hub is still the supported way to screenshot the phone app. If yes, keep but trim: derive
fixtures from `appwire-client/testing/*WireFixtures` rather than re-declaring shapes, and cut tests that only re-read fixture constants. If no, retire it (and
README section) in a dedicated PR. Do not delete without approval.
Size: up to -9,000 lines if retired; -2,000 if trimmed. Risk: low (dev-only).

---

## native-tests-legacy-mobile-14 (low): `ConversationClientLike` restates `AppwireClientLike` and is re-exported through an unrelated module

Locations: `mobile/src/services/conversation.ts:72-80`, `mobile/src/services/newSession.ts:26,113` (`export type { ConversationClientLike }`),
`appwire-client/typescript/clientLike.ts:26`, 5 test files importing the type from `services/newSession`.

Evidence: `ConversationClientLike extends Pick<AppwireClientLike, "state" | "onReady" | "onStateChange">` and adds `request`, `onNotification`, `forceStop?`,
`resumeThread?` ... i.e. a narrower local copy of the shared client seam, imported by 52 native test files and ~20 production files from `mobile/src/services/conversation`. The
pass-through at newSession.ts:113 exists only so tests can import both from one module.

Impact: two names for the connection seam; each new client method means two edits; tests must cast (`as never`) to satisfy the narrower type.

Fix plan: replace with `AppwireClientLike` (or a `Pick<>` alias exported from appwire-client where the narrow shape is wanted) and delete the re-export. Mechanical import rewrite over ~70 files, tsc is the guard.
Size: -40 lines, ~70 import edits. Risk: low.

---

## native-tests-legacy-mobile-15 (low): the AppWire path map is hand-listed in four configs

Locations: `mobile-native/tsconfig.json:14-23`, `tsconfig.check.json:9-33` (repeats the whole block plus 7 more), `vitest.config.mts:19-60`, `metro.config.js:27-60`,
guarded by `src/vitestResolver.test.ts` (56) and `src/metroResolver.test.ts` (80).

Evidence: nine `@evener/appwire-client/*` subpaths appear in tsconfig.json, again in tsconfig.check.json (since `paths` replaces rather than merges), again as
vitest aliases, with Metro computing the same set from `scripts/sdk/source-files.env`. Two small test files exist only to keep these in sync.

Impact: adding a subpath is a four-file edit; low risk because tests catch drift.

Fix plan: have `tsconfig.check.json` not redeclare the appwire entries (move the extra `anser`/`react`/... entries into tsconfig.json), and generate the vitest alias map from the
`exports` map in `appwire-client/typescript/package.json` (a ~15-line loop) so the resolver tests assert behavior, not a list. Defer unless a subpath is added soon.
Size: -80 lines. Risk: low.

---

## Recommended fix sequence

1. Ask Jesse about the structural moves (findings 1, 2, 13); these need explicit approval.
2. Finding 1 (move `mobile/src` into the app tree). Mechanical, one PR, makes all later diffs smaller.
3. Finding 9 and 8 (shared fakes, mock cleanup). Independent, low risk, reduces the casts and boilerplate every later test move carries.
4. Finding 2 (delete the unread activity view and store) with characterization tests first.
5. Findings 3 and 4 (extract pure row transforms, drain scheduler, binding guards; unify send/queue/interrupt).
6. Finding 5 and 7 (split `conversation.test.ts` by behavior, rename the round-labelled describes, drop absent-API and self-spy tests) alongside 3.
7. Findings 10, 11, 12 (copy-oracle triage, helper consolidation, split giant native test files), best done together per area (conversation, board, plugins).
8. Finding 6 and 14, 15 as cleanup riders; 13 only after Jesse decides on the demo hub.
