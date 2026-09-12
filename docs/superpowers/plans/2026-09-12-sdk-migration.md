# Plan: migrate both frontends onto the shared AppWire SDK package

Written 2026-09-12 against `92561dbe3`. Reads on
`docs/design/2026-09-12-sdk-migration-inventory.md`, which holds the evidence
for every claim about what a module owns and who imports it.

Someone else executes this one PR at a time. Nothing here is approved yet;
issue #1116 item 7 records that "no backward-compatibility layer or large
state-store extraction has been approved by this landing task."

## Rules the sequence obeys

1. Every PR leaves both frontends working and `make test-web`,
   `make test-native` and `make test-api-package` green. A PR that touches the
   package also re-runs `make test-web-browser`, because the five browser guards
   build their own entry bundles and resolve imports independently.
2. A PR migrates at most one store/module. The exception is a pure
   packaging/plumbing PR (phase A), which may touch many files as long as no
   behavior changes.
3. Duplication is removed in the same PR that introduces the shared version.
   A PR that adds a shared module and leaves both copies in place is not done.
4. The web store's existing public interface and its existing test file stay as
   the contract; the store's implementation becomes a thin adapter over the
   shared module. The test file is edited only where it reaches inside the
   implementation.
5. No backward-compatibility layers. Two temporary seams are listed explicitly
   below, each with the PR that removes it.
6. `types.gen.ts` changes only via `make generate`. No PR here edits it.
7. **A PACKAGE CANDIDATE with one consumer does not move.** Relocating a module
   into the package "for later" adds a boundary and buys nothing. The 18
   single-consumer candidates in the inventory stay where they are until a
   second consumer arrives in the same PR. This is why phase C schedules 24
   relocations and not 51.

## Rows that stay put, permanently

These never move, and a PR that proposes moving one is wrong:
`stores/mutationOutboxIndexedDB.ts`, `stores/prefs.ts`,
`stores/panelStoreEviction.ts`, `stores/testing/stalledIndexedDB.ts`,
`keybindings/dispatcher.ts`, `transcriptDisplay/renderContext.tsx`,
`shell/{workspace,routing,paneRegistry,chromeStore,deletedSessionPanes,sessionCycle}.ts`,
`notifications/{channels,favicon,leader,title}.ts`, `widgets/toast/store.ts`,
`panes/session/composer/draft.ts`, `auth.ts`, and on the native side
`draftRepository.ts`, `nativeLocation.ts`, `nativeImagePicker.ts`. They are
browser or device storage, DOM event routing, dockview, `window.location`, or
`expo-*`. `widgets/codeblock/ansi.ts` also stays put — see decision 1.

## Phase A — packaging and plumbing (5 PRs)

| # | Title | Scope | Oracle | Lines | Deps | Risk |
| --- | --- | --- | --- | --- | --- | --- |
| A1 | Ship the nine unpacked protocol modules | In: `protocol/tsconfig.build.json`, `package.json` exports, `index.ts`, `scripts/qualify-package.mjs`. Out: every app file. Deleted: nothing | `make test-api-package`, extended so the three generated consumer programs import each newly shipped module under ESM and CJS | ~200 | — | A module that compiles under `lib: ["ES2022","DOM"]` but needs a browser global at runtime ships silently; the runner only imports. `docContent.ts` is held back to C24 for exactly this reason |
| A2 | Move `AppwireClientLike` out of `protocol/testing/` | In: new `protocol/clientLike.ts`, all 136 importers rewritten. Deleted: the declaration in `testing/fakeClient.ts:40` | `tsc --noEmit` in both apps; `make test-web` + `make test-native` unchanged otherwise | ~150 | A1 | None found — tsc catches every miss. The type is structural, so a partial rewrite still compiles; grep for the old path in the same PR |
| A3 | Relocate the package to a top-level directory and alias it | In: `git mv` of `protocol/` to the agreed path, `tsconfig.paths`, `vite.config`, `vitest.config`, `mobile-native/tsconfig*.json`, Metro config, the five browser-guard runners. Deleted: nothing | all four gates | ~250 | A1, decision 3 | Metro and the guard bundlers resolve independently of `tsc`; a working `make test-web` does not prove `make test-web-browser` resolves the alias |
| A4 | Rewrite deep relative imports to the package name | In: ~700 import sites across both apps (506 web, 202 native). Deleted: nothing | all four gates; no test assertion changes, only test imports | ~800 (import lines only) | A3 | Large mechanical diff hides a semantic edit. Review with `--stat` plus a check that the non-import diff is empty |
| A5 | Delete the dead native conversation fixtures | Deleted: `mobile/src/dev/conversationFixtures.ts` (774 lines) | `make test-native` | ~780 (deletion) | — | None. It has no importer and two dangling type imports, so it is never typechecked today |

A5 is independent and can land first if it is convenient.

## Phase B — lowest-risk duplicated logic (7 PRs)

Each collapses one rule that is provably written twice. All pure functions; no
wire calls, no storage, no React.

| # | Title | Scope | Oracle | Lines | Deps | Risk |
| --- | --- | --- | --- | --- | --- | --- |
| B1 | One settled-item failure predicate | In: new `itemFailure.ts` in the package. Deleted: the predicate in `transcriptDisplay/projector.ts:118-130` and `toolCallFailed`/`isInProgressStatus` in `mobile/src/conversation/project.ts:114-133` | `transcriptDisplay/projector.test.ts` and `mobile/src/conversation/project.test.ts` both keep their assertions and both point at the shared module | ~120 | A1 | Low. `project.ts`'s own comment says it mirrors the web predicate "exactly"; if it does not, the two test files disagree and the PR surfaces it |
| B2 | One task row parser and grouping | In: package `taskData.ts` absorbing `panes/session/chrome/{taskData,taskGroups,taskTime}.ts`. Deleted: `deriveOpenTaskCount` and `TaskGroup` in `mobile/src/services/activity.ts:415-433` | `taskData.test.ts`, `taskGroups.test.ts`, `mobile/src/services/activity.test.ts` | ~280 | A1 | Medium. The two sides read different wire fields — web parses `TaskListResponse.data` (`unknown`), native reads `TaskAggregate` off the Thread. The shared module must accept both inputs or the PR must pick one and prove the counts match |
| B3 | One usage and cost accounting | In: package `usage.ts`. Deleted: `panes/session/chrome/detailsAccounting.ts` body, `projectUsage` in `mobile/src/services/activity.ts` | `detailsAccounting.test.ts`, `mobile/src/services/activity.test.ts` | ~180 | A1 | Low. Rounding and context-pressure thresholds differ; a snapshot difference is a real behavior change, not a test to relax |
| B4 | One system-notice family classification | In: package `systemNotice.ts`. Deleted: `messages/systemGrouping.ts` classifier, `systemFamily` in `project.ts:70-77` | `systemGrouping.test.ts`, `project.test.ts` | ~160 | A1 | Low. The two `eventKind` sets are not identical today — reconciling them changes what the web hides |
| B5 | One tool-run folding rule | In: package `toolRuns.ts`. Deleted: `transcript/toolRuns.ts`, `transcript/toolSupersession.ts`, `clusterActivities` in `project.ts:528` | `toolRuns.test.ts`, `toolSupersession.test.ts`, `project.test.ts` | ~340 | A1, B1 | Medium-high. This decides what a reader sees in the transcript. Fold behavior differs for live turns; `make test-web-browser`'s transcript guard is the only check that the rendered result still scrolls correctly |
| B6 | One queue reconciliation | In: package `queue.ts`. Deleted: `composer/queue/pendingReconcile.ts`, `projectQueue` in `project.ts:759` | `pendingReconcile.test.ts`, `project.test.ts`, `protocol/activityList.queue.test.ts` | ~260 | A1 | Medium. Optimistic-turn identity rules differ; a mismatch shows as a duplicated or missing queued turn, which unit tests can miss if both sides' fixtures are the same shape |
| B7 | One liveness line and thread title | In: package `liveness.ts`. Deleted: `panes/session/liveness.ts`, `panes/session/threadTitle.ts`, native equivalents in `project.ts` | the two web test files plus `project.test.ts` | ~120 | A1 | Low |

## Phase C — relocate modules both apps already import (24 PRs)

Every module here is already imported by the web and by `mobile-native` through
a deep relative path (inventory §0). These PRs move the file into the package
and rewrite both sides' imports. No logic changes; the existing test file moves
with the module and stays the oracle. Sizes are move + import-rewrite lines.

| # | Module moved | Lines | Deps | Risk |
| --- | --- | --- | --- | --- |
| C1 | `stores/navigation/{codec,merge,types,immutable}.ts` (1151) | ~1250 | A4 | `codec.ts` deep-freezes; a bundler that strips `Object.freeze` in production changes behavior no test sees |
| C2 | `stores/navigation/testing.ts` (294) | ~340 | C1 | Needs a `testing` subpath that ships built, or it stays a source-only file — decide in C1 |
| C3 | `keybindings/{actions,chord,defaults,display,overrides,registry,validation}.ts` (1445) | ~1550 | A4 | `registry.ts` is a module-level singleton; the move must turn it into a factory or two apps share one registry in a test process |
| C4 | `transcriptDisplay/config.ts` (603) | ~700 | A4 | Encoding is a pinned localStorage contract (`prefs.ts` comment on commit 932eeddca); do not touch `encodeLocalConfig` |
| C5 | `stores/composerInput.ts` (27) | ~90 | A4 | None |
| C6 | `stores/attachmentMarkers.ts` (48) | ~110 | C5 | None |
| C7 | `composer/attachments/textareaMarkers.ts` (60) | ~120 | C6 | None |
| C8 | `composer/attachments/limits.ts` (29) | ~90 | A4 | None |
| C9 | `composer/slashCompletion.ts` (334) | ~400 | A4 | Carries a third-party port under `LICENSES/beautiful-ui.txt`; the attribution comment moves with it |
| C10 | `composer/submitRouting.ts` (50) | ~110 | A1 (needs `sendQueueAvailability` shipped) | None |
| C11 | `composer/askDock/deriveAskQuestions.ts` (95) | ~160 | A1 | Reads `ThreadModel`; blocked until A1 ships `model.ts` |
| C12 | `composer/askDock/reconcileBatches.ts` (76) | ~140 | C11 | None |
| C13 | `panes/session/chrome/activityRows.ts` (208) | ~270 | A1 | None |
| C14 | `shell/rail/sessionState.ts` (41) | ~100 | A4 | None |
| C15 | `shell/palette/catalogCommands.ts` (37) | ~100 | A4 | None |
| C16 | `shell/reasoningEffort.ts` (30) | ~100 | A4 | None |
| C17 | `panes/spawn/{schema,pluginSelectionState,harnessModels}.ts` (192) | ~270 | A4 | None |
| C18 | `settings/launchShared/{schema,inherited,pathListAdd}.ts` (~400) | ~480 | A4 | None |
| C19 | `settings/credentials/credentialLabels.ts` (157) | ~220 | A4 | None |
| C20 | `settings/marketplacesPlugins/sourceLabel.ts` (20) | ~80 | A4 | None |
| C21 | `widgets/modelCatalog/{pickerRows,types}.ts` (~180) | ~250 | A4 | None |
| C22 | `widgets/pathfield/pathRows.ts` (156) | ~220 | A4 | None |
| C23 | `widgets/disclosure/disclosureStore.ts` (142) | ~210 | decision 1 | zustand; blocked on the store-shape ruling |
| C24 | `docContent.ts` gains a base-URL/fetch port and ships | ~180 | A1 | It calls global `fetch` against absolute hub URLs; the port is the behavior change, not the move |

Three modules that look like relocations are deliberately not here.
`stores/secureUUID.ts` has one consumer today and moves inside D26, where the
outbox core becomes its second. `panes/session/chrome/{activityFormat,statusFormat}.ts`
and `transcript/messages/{format,turnMeta}.ts` are classified DUPLICATED, not
relocations, and are handled by B3/B4, D18 and D24.

## Phase D — the stores (28 PRs)

Smallest and least coupled first. Each replaces a web store's body with an
adapter over a shared module and deletes the native twin in the same PR (rule 3).
The web store's exported shape and its `.test.ts` are the contract (rule 4).

| # | Store migrated | Native twin deleted | Lines | Deps | Risk |
| --- | --- | --- | --- | --- | --- |
| D1 | `stores/commandCatalog.ts` (26) | `commandCatalog.ts` (105) | ~250 | A4 | Low. Native also listens to `evener/plugin/updated`; the shared version must keep that or native catalogs go stale |
| D2 | `stores/settingsOverview.ts` (122) | `hubOverview.ts` (71) | ~280 | A4 | Low. The store shape is pinned across three streams; the adapter keeps it |
| D3 | `stores/tasksPanel.ts` (228) | `taskList.ts` (123) | ~400 | B2, D1 | Low-medium. Unsupported vs daemon-gone vs empty are three distinct states; collapsing any two is invisible to a shape-only test |
| D4 | `stores/hubUpdate.ts` (244) | `hubUpgrade.ts` (263) | ~480 | A4 | Medium. The `/api/health` poll and `location.reload()` stay web-side, `expo` restart stays native-side; only check/apply move. Runtime risk: the socket drops mid-apply, which no unit test reproduces |
| D5 | `keybindings` overrides store (852) | `nativePreferences.ts` keybinding half | ~900 | C3 | Medium-high. Reconciliation is a delta against a live registry; a full rebind tears down in-flight dispatcher state |
| D6 | `stores/transcriptDisplay.ts` hub half (707) | `nativePreferences.ts` display half | ~800 | C4, D5 | Medium. The local-storage half and `shell/useIsMobile` stay web-side; the split point is the PR's whole design |
| D7 | `stores/launchConfig.ts` (114) | `launchSettings.ts` (311) | ~450 | C18 | Low-medium. The schema cache is read-your-writes sensitive for everything except `schema()` |
| D8 | `stores/credentials.ts` instance half (322) | `providerInstances.ts` (176) | ~500 | C19 | Medium. The never-echo-a-secret invariant is a review item, not a test |
| D9 | `stores/credentials.ts` auth half | `providerSignIn.ts` (386) | ~550 | D8 | Medium-high. Device and OAuth flows differ by platform (popup vs system browser); only the RPC sequencing is shared |
| D10 | `stores/extensions.ts` marketplaces | `marketplaces.ts` (144) | ~400 | C20 | Medium. Fetches never throw, mutations reject — the two conventions must survive the move |
| D11 | `stores/extensions.ts` plugins | `installedPlugins.ts` (102) | ~350 | D10 | Medium |
| D12 | `stores/extensions.ts` dirs and MCP | — | ~300 | D11 | Low |
| D13 | `stores/navigation/revalidator.ts` (467) | invalidation logic in `navigationPages.ts` | ~600 | C1 | Medium. Needs an injected scheduler; a wrong one produces refetch storms under load that no unit test shows |
| D14 | `stores/navigation/store.ts` (867) | `navigationPages.ts` (394), `navigationReveal.ts` (131) | ~1300 | D13, decision 1 | High. Pagination, attention and base invalidation interact; `NavigationBaseInvalidError` recovery is the sharp edge |
| D15 | `stores/navigation/selectors.ts` (431) | `navigationTree.ts`, `rosterSearch.ts` (165) | ~600 | D14 | Medium. Selectors read the store directly today and must take state as an argument |
| D16 | `mobile/src/services/roster.ts` (114) folded onto navigation reads | `navigationActions.ts` pin/archive half (302) | ~450 | D15 | Medium. `thread/list` and `evener/navigation/read` answer the same question differently; this PR picks one |
| D17 | Activity tree state: web adopts `activityList.ts` | `stores/activityPanel.ts` body (371) | ~600 | A1, C13 | High. Continuation grafting (`activityMerge.ts`) and retained-tree behavior across a thread replacement is the bug #1096 already fixed once |
| D18 | Activity counts and summary | `mobile/src/state/activity.ts` (495), `services/activity.ts` remainder | ~800 | D17, B2, B3 | High. Native's generation/identity fencing and relocation-to-rehydrate rules are stricter than the web's; keep native's, do not average them |
| D19 | `subagentModuleStore.ts` (138) | `delegateDetails.ts` (116) | ~300 | A1 | Low |
| D20 | `askDockStore.ts` (440) | `questionBatches.ts` (63) | ~550 | C11, C12 | Medium. Subscribes to `threadsStore` at module load; the shared version needs an explicit wiring call |
| D21 | Native adopts `ItemModel`/`TurnModel` | `mobile/src/conversation/model.ts` (227) | ~900 | A1, B1, B4 | High. Two view models collapse into one; every native screen reads the new shape |
| D22 | Native adopts `reducer.hydrateThread` | the hydrate half of `project.ts` (787) | ~1100 | D21 | High. Cold attach, older-page merge and truncation caps (`MAX_ITEM_BYTES`, `RETAINED_ITEM_CAP`) must agree |
| D23 | Native adopts `reducer.applyNotification` | the notification appliers in `state/conversation.ts` (3369) | ~1800 | D22 | Highest in the plan. Two independent appliers over the same notification set; generation fencing lives on the native side and must be lifted into the shared reducer or kept as a wrapper |
| D24 | Native adopts `transcriptDisplay/projector.ts` | native timeline clustering | ~900 | D23, B5, C4 | High. Native gains config-driven content levels it does not have today; that is a visible product change, flag it |
| D25 | `pendingTurnsStore.ts` (474) | the queue half of `state/conversation.ts` | ~700 | B6, D23 | Medium-high. Imports React's `act` at module scope today — remove that first |
| D26 | Outbox record core + a storage port, `secureUUID.ts` moved in | `ConversationMutationState` in `state/conversation.ts` | ~700 | D23 | Medium. Defines the port; no storage moves. `secureUUID` defaults to `globalThis.crypto`, so native must keep passing `expo-crypto` |
| D27 | `mutationDispatcher.ts` (251) over the port | native's in-memory retry path | ~500 | D26 | High. Serialized per-ref dispatch, blocked/unknown receipts, and "never blindly replay a write after reconnect" (#1116). Connection loss during a mutation is exactly what tests do not cover |
| D28 | `stores/connection.ts` (107) lifecycle | `ConnectionProvider.tsx` | ~400 | D27, A2 | High. Reconnect rewiring, heartbeat and terminal-protocol handling; a wrong handler set leaves a live app silently stale |

## Temporary seams

Exactly two, each with its removal PR named.

1. **A3 keeps the old `protocol/` path as a directory of re-export stubs** so
   A3 and A4 can be reviewed separately. A4 deletes the stubs. If A3 and A4 land
   together, this seam does not exist.
2. **D21–D24 leave `mobile/src/conversation/project.ts` in place as a shrinking
   shim** while its four halves migrate. D24 deletes the file and the
   `mobile/src/conversation/` directory. No PR after D24 may import it.

Nothing else gets a compatibility layer. In particular, no PR adds a
`legacy`/`v1` export alongside a new one.

## Decisions needed before the first code PR

**1. Does the package take runtime dependencies, or stay at zero?**
It has none today (`protocol/package.json`), and the qualification runner
installs the tarball with `--offline --ignore-scripts`, so every dependency has
to resolve in a temp directory. Both apps' stores are zustand; two candidate
modules (`disclosureStore.ts`, `widgets/codeblock/ansi.ts` via `anser`) carry
dependencies today.
*Recommendation: stay at zero.* Ship framework-free stores (a
`getState`/`subscribe`/`setState` triple — which is what `zustand/vanilla`'s
`createStore` already gives the web, so the web adapters are near-trivial) and
let each app wrap them. Leave `ansi.ts` where it is; native already imports it
by relative path and moving it buys one import site for one dependency. This
decision gates C23 and every phase-D store.

**2. One view model or two layers?**
The web goes wire → `ThreadModel` (`reducer.ts` + `model.ts`) → display entries
(`transcriptDisplay/projector.ts`, filtered by a hub-backed config). Native goes
wire → `MobileConversation` in one step (`conversation/project.ts`), with
classification and clustering baked in.
*Recommendation: adopt the web's two layers.* The display config is already
shared — `transcriptDisplay/config.ts` has 10 native import sites — so native is
already half-committed, and a hub setting that only one client honors is a bug
waiting to be filed. The cost is real and should be stated to users: D24 gives
native content levels it does not have today. This decision gates D21–D24, which
is roughly 4,700 of the plan's lines.

**3. Where does the package live on disk, and what is the directory called?**
It is `cmd/evener-hub/frontend/src/protocol` today — a subdirectory of the web
app that both mobile trees reach with `../../../`. Every phase-A build-config
edit lands in whatever directory wins, so this has to be settled before A1.
*Recommendation: `git mv` it to a top-level `appwire-client/`* in A3, so neither
app owns it and the `../../../` chains disappear. The cost is a large mechanical
diff plus Metro, Vite, vitest and five browser-guard resolvers to update, all in
one PR. The alternative — leave it in place — is free and keeps a package the
web app nominally owns; I do not recommend it, but it is defensible if the move
churn is judged worse than the asymmetry.

A fourth question is settled by the inventory rather than needing a ruling:
`mobile/src` should not move into the package as a unit. Its 4,383 lines of
zustand stores are one app's state model, and the largest already-shared body of
code is in the web tree, not in `mobile/src`. It dissolves module by module
through D21–D24 and the directory is deleted at the end of D24.

## Honest total

**64 PRs. Roughly 29,000 lines changed.**

Phase A 5 PRs / ~2,200 lines. Phase B 7 / ~1,500. Phase C 24 / ~7,900.
Phase D 28 / ~18,100.

Net, the tree should shrink by roughly 6,000–8,000 lines: about 3,900 lines of
native twins deleted outright (`providerInstances`, `providerSignIn`,
`installedPlugins`, `marketplaces`, `launchSettings`, the four navigation
modules, `nativePreferences`, `hubOverview`, `hubUpgrade`, `taskList`,
`commandCatalog`, `jobOutput`, `delegateDetails`), 774 from A5, and maybe half
of `mobile/src`'s 6,600 lines of projection and conversation state — the other
half is behavior the shared version has to absorb, not delete.

Treat these numbers as ±40%. Phase D's four transcript PRs (D21–D24, ~4,700
lines) and the three mutation PRs (D26–D28, ~1,500) are where the estimate is
weakest, because both are cases where two implementations disagree today and
nobody has yet written down which one is right.
