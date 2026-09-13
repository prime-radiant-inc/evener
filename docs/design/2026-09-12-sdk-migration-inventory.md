# SDK migration inventory: web and native state modules

Written 2026-09-12 against `92561dbe3`. Companion to
`docs/superpowers/plans/2026-09-12-sdk-migration.md`, which sequences the PRs.

Goal, from Jesse: the TypeScript client library built for the native app
(`@evener/appwire-client`, `cmd/evener-hub/frontend/src/protocol`) becomes the
underpinning of the web interface too. Issue #1116 "Scope and standing
decisions" records that this extraction starts only after the native checkpoint
lands, and that the package is today "a suitable shared transport/protocol
boundary, not yet a replacement for every web state store". This document is
the map of what is actually there.

Method: every classification below came from reading the module and its
importers, not from its name. **Verification rule, re-applied at `f2599d1ed`
(origin/main) after the first phase-B PR stopped on a row that did not hold:**
a row is DUPLICATED only if a grep names the exact symbol and `file:line` on
*both* sides and the two take the same inputs, use the same vocabulary, and
apply the same fallback. A pair that merely occupies the same conceptual slot —
"both compute usage", "both fold runs" — is two different rules, and is recorded
as PLATFORM-ONLY or single-consumer PACKAGE CANDIDATE instead. A row is
PACKAGE CANDIDATE (2 consumers) only if `git grep "frontend/src/<module>\""
origin/main -- mobile-native` returns at least one hit; all 34 such rows were
re-checked this way and all 34 hold. Line counts are non-test lines at the stated
commit. Where I am unsure I say so in the row.

## 0. What the package actually is today

Five facts that change the shape of the whole migration. Facts 1, 2 and 5
describe the package **as it stood at `92561dbe3`, before A1**; each carries its
post-A1 value inline, measured at `3bf357337`. Facts 3 and 4 hold as written.

1. **The shipped package was six files, not sixteen** — until A1.
   `protocol/tsconfig.build.json:13` lists
   `files: ["index.ts", "client.ts", "errors.ts", "transport.ts", "types.gen.ts", "askAnswers.ts"]`.
   The other ten runtime modules in that directory (`model.ts`, `reducer.ts`,
   `activityData.ts`, `activityList.ts`, `activityMerge.ts`, `jobOutput.ts`,
   `docContent.ts`, `sendQueueAvailability.ts`, `sessionErrors.ts`,
   `stableDelegate.ts` — 3,502 lines, per §5's own table) were compiled by the
   apps' own build, never packed, never qualified. **A1 (#1184) closed this,
   merged to main as `f2599d1ed`:** the build `files` list now carries all
   sixteen, `docContent.ts` included, with only `readDocFile` held off
   `index.ts` until C24. §5's "In tarball?" column reflects the post-A1 state.
2. **`index.ts` carried nine export statements and eleven runtime exports**
   before A1 (`protocol/index.ts:1-9` at `92561dbe3`; all sixteen modules export
   through it now, so size new work off the current file, not this list): `AppwireClient`, `APPWIRE_PROTOCOL_VERSION`,
   `ConnectionClosedError`, `RequestTimeoutError`, `WireError`,
   `rpcURLFromLocation`, `composeAskAnswers`, `METHOD_NAMES`,
   `NOTIFICATION_NAMES`, `STEERING_KINDS`, `THREAD_ITEM_EVENT_KINDS`, plus the
   type-only re-exports. Count symbols, not statements: an executor scoping work
   off "nine" under-scopes. And
   `package.json:12-18` declares one export path (`"."`). Everything both apps
   actually import is a deep relative path into the source tree: 229 web sites on
   `protocol/types.gen`, 129 on `protocol/model`, 88 on `protocol/errors`, 15 on
   `protocol/activityData`, 11 on `protocol/reducer`. `protocol/errors` ships in
   the tarball but only three of its nineteen exported declarations are
   re-exported by `index.ts`; the rest are reachable in-repo only.
3. **The interface every web store is written against is a test double's.**
   `AppwireClientLike` is declared at `protocol/testing/fakeClient.ts:40`;
   136 web files import from `protocol/testing/fakeClient`, 20 of them
   production modules (`stores/threads.ts:24`, `shell/clientContext.tsx:10`, …).
   `protocol/testing/` is not in the build `files` list. Of those 136 files,
   only **25** import `AppwireClientLike` itself (27 files name the type; the
   other two are its declaration and `FakeClient`). The rest import `FakeClient`
   and keep that import wherever the type moves.
4. **The package has zero runtime dependencies** (`protocol/package.json` has no
   `dependencies`). Both consumers' stores are zustand
   (`cmd/evener-hub/frontend/package.json` and `mobile-native/package.json`
   both depend on `zustand ^5`). A shared *state* core cannot keep the
   zero-dependency property and use zustand.
5. **The qualification runner hard-coded the export list four times** — and no
   longer does. Before A1 it wrote four consumer files each naming all eleven
   runtime exports literally. **A1 fixed this the way this document asked:** at
   `3bf357337` one shared `runtimeExports` array (`qualify-package.mjs:69`)
   feeds all four writes — `esm.mts` (:163), `commonjs.cts` (:177),
   `esm-runtime.mjs` (:201), `commonjs-runtime.cjs` (:207). A new export is
   added in one place; a new *subpath* still needs all four write sites touched.

The unexpected finding: **the native app already imports 30+ web frontend
modules by relative path.** `mobile-native` reaches into
`cmd/evener-hub/frontend/src/` at **204 import statements**, of which **88** are
not `protocol/` at all, spread over **39 distinct web modules** —
`transcriptDisplay/config` (10), `stores/navigation/testing` (7),
`stores/composerInput` (6), `panes/session/composer/slashCompletion` (5),
`panes/session/composer/attachments/limits` (5), `widgets/codeblock/ansi` (4),
`stores/navigation/codec` (4), `shell/reasoningEffort` (4), and more.
Counting method, so the figures are reproducible: lines under `mobile-native`
matching `from "<any prefix>cmd/evener-hub/frontend/src/`, tests included,
`node_modules` excluded; "non-`protocol`" drops the lines whose path contains
`src/protocol/`. An earlier draft of this document said "202 / 124"; 124 was an
arithmetic error and the per-module breakdown above always summed to 88. The web
imports nothing from `mobile/src`. So the dependency is already one-directional
and already deep; the migration is mostly about *relocating* modules two
consumers share into a place both can name, not about discovering new sharing.

## 1. Classification key

| Class | Meaning |
| --- | --- |
| SHARED ALREADY | In the built package (`tsconfig.build.json` `files`) and consumed by both frontends. |
| DUPLICATED | The same rule is implemented twice — web store vs. native module, or one side vs. an unused package module. |
| PLATFORM-ONLY | Browser- or device-specific. Never moves. |
| PACKAGE CANDIDATE | Pure logic that belongs in the package. Includes modules already imported by both frontends through a deep relative path, marked "(2 consumers)" — the taxonomy has no bucket for "shared but not packaged", and calling those SHARED ALREADY would hide that they are not in the tarball. |

"Wire surface" lists the methods/notifications the module names literally. A
`none` means the module is pure and takes its data from a caller.

## 2. Web `stores/` (32 modules, 12,336 lines)

| Module (lines) | Owns | Wire surface | Counterpart | Class | Seam needed to move |
| --- | --- | --- | --- | --- | --- |
| `stores/threads.ts` (2997) | `ThreadModel` per open ref, refcounted across panes; routes notifications into the reducer; owns send/steer/queue/drain and mutation lifecycle | `evener/thread/{name/set,resync}`, `turn/*`, `evener/{tasks,jobs}/list`, `evener/jobs/output`, `evener/goal/updated`, `evener/steering/injected`, `evener/sandbox/escalation/resolve`, `evener/auth/updated` | `mobile/src/state/conversation.ts` (3369) + `services/conversation.ts` (1263) | DUPLICATED | store core without zustand; durable-outbox interface; panel-eviction callback instead of `panelStoreEviction` import |
| `stores/activityPanel.ts` (371) | Per-ref activity-tree load state + disclosure; grafts continuations | none (fetch injected; tree comes from `evener/jobs/list` via threads.ts) | `protocol/activityList.ts` (188, native-only) and `mobile/src/state/activity.ts` (495) | DUPLICATED | a fetch/subscribe port; `ActivityList` already is the shared shape |
| `stores/activitySummary.ts` (327) | Per-ref counts, root-fetch sequencing, failure sentences | none | `mobile/src/services/activity.ts` (481) | DUPLICATED | error-sentence helpers already in `protocol/errors.ts`; needs a scheduler port |
| `stores/agentsDoc.ts` (178) | Personal AGENTS.md document, refreshed by broadcast | `evener/settings/agentsDoc/{get,set,changed}` | none (native has no AGENTS editor) | PACKAGE CANDIDATE | client port only |
| `stores/attachmentMarkers.ts` (48) | Translating `[image N]` editing anchors to prose at send | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | none — already pure |
| `stores/commandCatalog.ts` (26) | Plugin/user slash-command catalog cache | `evener/command/list` | `mobile-native/src/commandCatalog.ts` (105) | DUPLICATED | client port |
| `stores/composerInput.ts` (27) | `InputItem[]` assembly from text + staged attachments | none | native imports this file (6 sites) | PACKAGE CANDIDATE (2 consumers) | none — already pure |
| `stores/connection.ts` (107) | The single `AppwireClientLike` + its mirrored `ConnectionState` | none | `mobile-native/src/ConnectionProvider.tsx` | DUPLICATED | needs `AppwireClientLike` to exist outside `protocol/testing/` |
| `stores/credentials.ts` (322) | Provider instances + auth flows; never echoes a secret | `evener/instance/{list,create,edit,remove,setDefault}`, `evener/auth/{status,test,logout,apiKey/*,credentialJson/set,login/*,device/*,updated}` | `mobile-native/src/providerInstances.ts` (176) + `providerSignIn.ts` (386) | DUPLICATED | client port; native already shares `credentialLabels.ts` |
| `stores/extensions.ts` (531) | Marketplaces, plugins, plugin/skill dirs, MCP servers | `evener/marketplace/*`, `evener/plugin/*`, `evener/launch/{getLayer,setLayer,updated}`, `evener/path/validate`, `evener/paths/complete`, `evener/dirs/create` | `mobile-native/src/installedPlugins.ts` (102) + `marketplaces.ts` (144) | DUPLICATED | client port; two failure conventions (fetch-never-throws vs mutation-rejects) must survive |
| `stores/hubUpdate.ts` (244) | Release channel, update check and apply | `evener/update/{check,apply}` | `mobile-native/src/hubUpgrade.ts` (263) | DUPLICATED | the post-apply half polls `/api/health` and calls `location.reload()` — that tail stays platform-side |
| `stores/keybindings.ts` (852) | Hub keybinding overrides, reconciled into the registry as a delta | `evener/settings/keybindings/{get,patch,changed}` | `mobile-native/src/nativePreferences.ts` (839) | DUPLICATED | registry (`src/keybindings/`) must move first or be injected |
| `stores/launchConfig.ts` (114) | Launch schema/layers/resolve/trust | `evener/launch/{schema,getLayer,setLayer,resolve,trustRepo}`, `evener/path/validate` | `mobile-native/src/launchSettings.ts` (311) | DUPLICATED | client port; the schema cache is the only stateful part |
| `stores/mutationDispatcher.ts` (251) | Serialized per-ref dispatch of outbox records, blocked/unknown handling | none (dispatches by `MethodName`) | `mobile/src/state/conversation.ts:299-315` (`ConversationMutationState`, in-memory only) | DUPLICATED | needs the storage interface below; native's twin has no durability at all |
| `stores/mutationOutbox.ts` (241) | Outbox record shapes, discovery reasons, recovery kinds | none | none | PACKAGE CANDIDATE | pure types + helpers; parametrize over a storage port |
| `stores/mutationOutboxIndexedDB.ts` (562) | IndexedDB persistence of the outbox | none | `mobile-native/src/draftRepository.ts` (303, expo-sqlite) | PLATFORM-ONLY | never moves; it is the implementation behind the storage port |
| `stores/navigation/store.ts` (867) | Navigation resource graph, attention, pagination | `evener/navigation/{read,invalidated}`, `evener/attention/changed` | `mobile-native/src/navigationPages.ts` (394) + `navigationActions.ts` (302) + `navigationReveal.ts` (131) + `pinNavigation.ts` (171) | DUPLICATED | store core without zustand (`store.ts:1-2`), plus a host persistence port: `store.ts:203` calls `loadExpansion()` in the initial state, and `shell/rail/railExpansion.ts:40,68` is `localStorage` |
| `stores/navigation/codec.ts` (786) | Snapshot/delta decode, normalization, deep freeze | none | native imports this file (4 sites) | PACKAGE CANDIDATE (2 consumers) | none — already pure |
| `stores/navigation/merge.ts` (164) | Delta application onto a normalized graph | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | none |
| `stores/navigation/revalidator.ts` (467) | Invalidation → refetch scheduling per resource key | none | native re-implements inside `navigationPages.ts` | PACKAGE CANDIDATE | needs an injected scheduler (it currently takes request callbacks, so this is small) |
| `stores/navigation/selectors.ts` (431) | Derived views over the navigation graph | none | `mobile-native/src/navigationTree.ts` (21) + `rosterSearch.ts` (144) | DUPLICATED | reads `navigationStore` directly; must take state as an argument |
| `stores/navigation/types.ts` (176) | Resource keys, scopes, offsets, `NavigationBaseInvalidError` | none | native imports this file (2 sites) | PACKAGE CANDIDATE (2 consumers) | none |
| `stores/navigation/immutable.ts` (25) | `cloneAndDeepFreezeJSON`, `equalJSON` | none | none (single consumer) | PACKAGE CANDIDATE | none technically, but it is imported by `codec.ts:8` and `merge.ts:10`, which both move in C1 — so it crosses the boundary with them as a transitive dependency, not on its own merit |
| `stores/navigation/shutdownConvergence.ts` (56) | Convergence rule for a shutting-down session's rows | none | unsure — I did not find a native twin | PACKAGE CANDIDATE | none |
| `stores/navigation/testing.ts` (294) | Navigation fixtures/harness | none | native imports this file (7 sites) | PACKAGE CANDIDATE (2 consumers) | a test-only subpath export, or it stays a source-only file |
| `stores/panelStoreEviction.ts` (80) | Evicting per-ref panel stores when a pane closes | none | none | PLATFORM-ONLY | reads `shell/workspace` pane types; the eviction *trigger* is host-specific |
| `stores/prefs.ts` (682) | localStorage preferences under `evener.prefs.*` | none | `mobile-native/src/nativePreferenceDrafts.ts` | PLATFORM-ONLY | key names are a pinned contract; the storage is the browser's |
| `stores/secureUUID.ts` (20) | UUIDv4 from an injectable `SecureRandomSource` | none | `expo-crypto` on native | PACKAGE CANDIDATE | already takes the source as a parameter; defaults to `globalThis.crypto` |
| `stores/settingsOverview.ts` (122) | Fetch-once mirror of the settings overview bag | `evener/settings/overview` | `mobile-native/src/hubOverview.ts` (71) | DUPLICATED | client port; shape is pinned across three streams |
| `stores/tasksPanel.ts` (228) | Per-ref task rows + unsupported/daemon-gone states | none (rows from `evener/tasks/list` via threads.ts) | `mobile-native/src/taskList.ts` (123) | DUPLICATED | client port |
| `stores/testing/stalledIndexedDB.ts` (33) | A deliberately stalled IDB double | none | none | PLATFORM-ONLY | — |
| `stores/transcriptDisplay.ts` (707) | Hub + local transcript-display config, viewport-aware | `evener/settings/transcriptDisplay/{get,patch,changed}` | `mobile-native/src/nativePreferences.ts` (839) | DUPLICATED | depends on `shell/useIsMobile` and localStorage; the hub half is portable, the local half is not |

## 3. Web state/projection/logic outside `stores/` (49 rows)

The web has 497 non-test TS/TSX files and 87,278 lines outside `protocol/`.
Most are React components and CSS modules and are out of scope. These are the
modules that hold state, project wire data, or encode a rule — the ones a
migration has to place.

| Module (lines) | Owns | Wire surface | Counterpart | Class | Seam |
| --- | --- | --- | --- | --- | --- |
| `transcriptDisplay/config.ts` (603) | Transcript display config: wire↔local encode/decode, presets, fingerprint | none | native imports this file (10 sites) | PACKAGE CANDIDATE (2 consumers) | none — pure |
| `transcriptDisplay/projector.ts` (415) | `ThreadModel` → display entries at a content level; failure/interaction classification | none | `mobile/src/conversation/project.ts` (787), whose comment at line ~108 says it "mirrors the web frontend's hasItemFailure/hasFailureStatus/isNonZeroExit predicate (projector.ts:118-130) exactly" | DUPLICATED | this is the clearest duplication in the repo — one side says so in a comment |
| `transcriptDisplay/renderContext.tsx` (246) | React context for the projector | none | none | PLATFORM-ONLY | React |
| `keybindings/{actions,chord,defaults,display,overrides,registry,validation}.ts` (1445) | Chord grammar, default table, override delta application, semantic validation | none | native imports `defaults`, `display`, `registry`, `validation` (1 site each) | PACKAGE CANDIDATE (2 consumers) | `registry.ts:6` imports `zustand/vanilla` and `chord.ts:12` imports `tinykeys` — a zero-dependency package cannot take them as-is; `registry.ts` is also a module-level singleton needing an instance factory |
| `keybindings/dispatcher.ts` (197) | Keydown routing through tinykeys | none | native has its own | PLATFORM-ONLY | DOM events |
| `panes/session/chrome/taskData.ts` (102) | Narrows `TaskListResponse.data` (typed `unknown`) to `TaskRow[]` | none | native imports this file (2 sites). Not a twin of `services/activity.ts:409` `projectTasks`, which emits three *counts* off `TaskAggregate` with `active` pinned to literal `0`, against this module's parsed row list | PACKAGE CANDIDATE (2 consumers) | a relocation, not a dedup — the two task rules answer different questions and both are load-bearing |
| `panes/session/chrome/taskGroups.ts` (24) | Status partition for the tasks panel | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | none |
| `panes/session/chrome/taskTime.ts` (28) | Task recency/completion formatting | none | native imports this file (2 sites) | PACKAGE CANDIDATE (2 consumers) | none |
| `panes/session/chrome/activityRows.ts` (208) | `ActivityTree` → flat display rows | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | none |
| `panes/session/chrome/{activityFormat,statusFormat,detailsAccounting}.ts` (243) | Activity/status labels; token and cost accounting | none | **none.** `detailsAccounting.ts:69-89` `sessionTokens` takes a `ThreadModel` and returns `{inputTokens, outputTokens, scope} \| null`, *deriving* a session figure by summing loaded turns when no cumulative exists — pinned by `detailsAccounting.test.ts:26-29,41-44`. `services/activity.ts:437-451` `projectUsage` takes raw `Thread["evener"]` and copies ten optional fields with no arithmetic. Native does not merely lack the derivation, it **forbids** it: `state/activity.ts:23-27,337-344` returns `"rehydrate"` on `turn/completed` rather than fold per-turn usage into the aggregate. `projectUsage`'s real analog is `protocol/reducer.ts:797-805`, already in the package | PACKAGE CANDIDATE | single consumer, and the reducer analog makes this a D-phase reducer question, not a B-phase dedup — see decision 4 in the plan |
| `panes/session/composer/slashCompletion.ts` (334) | Inline `/`-completion parser | none | native imports this file (5 sites) | PACKAGE CANDIDATE (2 consumers) | imports `slashCommandInvocation` from `shell/palette/catalogCommands.ts:13`; that helper must cross the boundary first or with it |
| `panes/session/composer/submitRouting.ts` (50) | send/queue/steer/drain routing decision | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | reads `protocol/sendQueueAvailability` (also unpacked) |
| `panes/session/composer/attachments/limits.ts` (29) | 8 files / 8 MiB attachment caps | none | native imports this file (5 sites) | PACKAGE CANDIDATE (2 consumers) | none |
| `panes/session/composer/attachments/textareaMarkers.ts` (60) | `[image N]` marker splicing | none | native imports this file (2 sites) | PACKAGE CANDIDATE (2 consumers) | none |
| `panes/session/composer/askDock/deriveAskQuestions.ts` (95) | Positional "which asks are live" snapshot | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | reads `ThreadModel`, and pulls two modules with it: `:20-21` imports `panes/session/askShared.ts` (151), which at `:19` imports `parseAskUserQuestions`'s dependency `panes/session/transcript/tools/helpers.ts` (138, no imports of its own — pure). All three cross the boundary together |
| `panes/session/composer/askDock/reconcileBatches.ts` (76) | Stateful in-flight ask-batch reconciliation | none | native imports this file (2 sites) via `questionBatches.ts` (63) | PACKAGE CANDIDATE (2 consumers) | none |
| `panes/session/composer/askDock/askDockStore.ts` (440) | Per-ref answering dock bookkeeping | none | `mobile-native/src/questionBatches.ts` + approval screens | DUPLICATED | subscribes to `threadsStore` at module load |
| `panes/session/composer/queue/pendingTurnsStore.ts` (474) | Optimistic pending turns across outbox + model | none | `mobile/src/state/conversation.ts` queue half | DUPLICATED | imports `react`'s `act` at module scope (line 1) — that has to go before it can move |
| `panes/session/composer/queue/pendingReconcile.ts` (137) | Reconciling optimistic turns against `ThreadModel` | none | **none** — `:111` `reconcilePendingEntries` matches outbox records against a `ThreadModel` and emits `PendingTurnEntry[]` across four states (`submitting`/`blockedUnknown`/`accepted`/`claimed`); `project.ts:759` `projectQueue` is a field copy of `Thread["evener"]["queue"]` into `MobileQueue` — no optimistic records, no states, no reconciliation | PACKAGE CANDIDATE | single consumer; the native queue state that would be its twin lives in `state/conversation.ts` and is D25's problem |
| `panes/session/composer/draft.ts` (76) | Per-ref sticky composer drafts | none | `mobile-native/src/{nativeDrafts,draftRepository}.ts` (318) | PLATFORM-ONLY | localStorage vs expo-sqlite; the *rule* is one line |
| `panes/session/composer/recovery/recoveryDraft.ts` (76) | Failed-mutation record → restorable composer draft | none | unsure — native recovery path not verified | PACKAGE CANDIDATE | depends on `useAttachments`' `PendingAttachment` type |
| `panes/session/transcript/toolRenderers.ts:189` `toolCallFailed` (6) | A third settled-item failure predicate, registry-driven | none | the shared `protocol/itemFailure.ts` introduced by PR #1189 | DUPLICATED | does not trim `error`, does not treat `interrupted` as failure, ignores `exitCode`, and adds a per-tool `descriptor.failed` hook the other two have no equivalent for. Filed as issue #1190; 43 files reach the renderer registry |
| `panes/session/transcript/{toolRuns,toolSupersession,turnFailure}.ts` (247) | Tool-run folding, supersession, turn-failure classification | none | **none** — `toolRuns.ts:56` `foldToolRuns` folds settled, error-free `commandExecution` entries at `MIN_RUN = 3` (`:43`) over `ProjectedEntry[]`, through the renderer registry's `descriptorFor`; `project.ts:528` `clusterActivities` folds *consecutive same-family* activity items at run length ≥ 2 over `PreActivity[]` | PACKAGE CANDIDATE | single consumer; different input unit, threshold and output shape |
| `panes/session/transcript/messages/{format,systemGrouping,turnMeta}.ts` (212) | Message formatting, system-notice run grouping, turn metadata | none | `format.ts` is imported by native (1 site). `systemGrouping.ts` is **not** `project.ts:67` `systemFamily`'s twin: it groups runs of consecutive system messages at `MIN_GROUP_SIZE = 3` (`:23,54,79`), while `systemFamily` classifies one `eventKind` into a `NoticeFamily`. `systemFamily`'s real web counterpart is the `eventKind` set matching inside `transcriptDisplay/projector.ts:112-130`, which D24 adopts wholesale | PACKAGE CANDIDATE | `format.ts` relocates with C26-class work; `systemGrouping.ts` and `turnMeta.ts` are single-consumer and stay |
| `panes/session/askShared.ts` (151) + `panes/session/transcript/tools/helpers.ts` (138) | `ask_user` question parsing; tool-argument JSON helpers | none | `mobile/src/conversation/model.ts` `MobileAskQuestion`/`MobileAskOption` | DUPLICATED | `helpers.ts` is pure; `askShared.ts` reads `ItemModel`/`ThreadModel`. Reached transitively by `deriveAskQuestions.ts`, so C11 carries them |
| `panes/session/transcript/tools/subagentModuleStore.ts` (138) | Per-delegate presentation state | none | `mobile-native/src/delegateDetails.ts` (116) | DUPLICATED | uses `protocol/stableDelegate.ts` (unpacked) |
| `panes/session/transcript/useTranscript.ts` (66) | Selector + older-turn paging over `threadsStore` | none | `mobile/src/state/conversation.ts` `loadOlder` | DUPLICATED | React hook; the paging rule underneath is shared |
| `panes/session/transcript/flow/seenWatermark.ts` (34) | Reader position watermark | none | **none** — the earlier "unsure" is resolved: `activityRetention.ts` `retainedActivityTree` keeps an `ActivityTree` across a ref/threadId change, an unrelated rule | PLATFORM-ONLY | `:19,27` read and write `localStorage` |
| `panes/session/liveness.ts` (64), `threadTitle.ts` (23) | Liveness line and title derivation | none | **none** — `project.ts:715` passes `thread.status.type` straight through (pinned by `project.test.ts:1483,1493`), and `roster.ts:48-53` `classifyAttention` is a different rule with a different vocabulary (`needsYou`/`running`/`recent`) | PLATFORM-ONLY | `liveness.ts` exports React hooks and `threadTitle.ts:21-23` reads the web navigation zustand singleton. Both stay |
| `shell/rail/sessionState.ts` (41) | Humanized wire state for a session row | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | none |
| `shell/rail/railNodes.ts` (775), `railController.ts` (69) | Rail tree construction and expansion | none | `mobile-native/src/navigationTree.ts` | DUPLICATED | reads navigation selectors |
| `shell/palette/catalogCommands.ts` (37) | `/plugin:name` qualification rule | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | none |
| `shell/palette/{commands,search,commandScore,recentCommands}.ts` (919) | Command palette catalog, scoring, recency | none | none (native has no palette) | PACKAGE CANDIDATE | `commands.ts` closes over pane actions; scoring is the portable half |
| `shell/reasoningEffort.ts` (30) | Reasoning-effort vocabulary | none | native imports this file (4 sites) | PACKAGE CANDIDATE (2 consumers) | none |
| `shell/{workspace,routing,paneRegistry,chromeStore,deletedSessionPanes,sessionCycle}.ts` (800) | Open panes, focus, URL↔pane mapping, chrome | none | none | PLATFORM-ONLY | dockview, `window.location`, `history` |
| `notifications/{attention,channels,favicon,leader,title}.ts` (295) | OS notification policy, tab title, favicon, Web Locks leader election | none | native uses its own notification stack | PLATFORM-ONLY | `attention.ts` (60) alone is a portable policy function |
| `panes/spawn/schema.ts` (97), `pluginSelectionState.ts` (79), `harnessModels.ts` (16) | New-session form schema and plugin selection | none | native imports all three (5 sites) | PACKAGE CANDIDATE (2 consumers) | none |
| `panes/spawn/spawnDrafts.ts` (144), `preflight.ts` | Spawn drafts; path/dir preflight | `evener/path/validate`, `evener/dirs/create` | `mobile-native/src/creationDraftRepository.ts` | DUPLICATED | drafts are platform storage; preflight is a client port |
| `panes/settings/sections/launchShared/schema.ts` (337), `inherited.ts` (44), `pathListAdd.ts` (54) — 435 total | Launch-option schema interpretation and inheritance | none | native imports `schema` (3 sites), `pathListAdd` (1) | PACKAGE CANDIDATE (2 consumers) | `schema.ts:8` imports `LaunchConfigLayerName` from the web-only `stores/launchConfig.ts`; that type has to move too |
| `panes/settings/sections/credentials/credentialLabels.ts` (157) | Provider/credential display vocabulary | none | native imports this file (2 sites) | PACKAGE CANDIDATE (2 consumers) | none |
| `panes/settings/sections/credentials/oauthFlow.ts` (33) | OAuth step sequencing | none | `mobile-native/src/providerSignIn.ts` | DUPLICATED | native opens a system browser; web a popup |
| `panes/settings/sections/marketplacesPlugins/sourceLabel.ts` (20) | Marketplace source labels | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | none |
| `widgets/modelCatalog/{catalogClient,catalogView,pickerRows,scopedCatalog}.ts` (339) | Model catalog fetch, scoping, picker rows | none (catalog via threads.ts `listModels`) | native imports `pickerRows` and `types` (2 sites) | PACKAGE CANDIDATE (2 consumers) | `pickerRows.ts:18` is built on `catalogView.ts` helpers; the two move together |
| `widgets/codeblock/ansi.ts` (439) | ANSI → styled spans | none | native imports this file (4 sites) | PACKAGE CANDIDATE (2 consumers) | depends on `anser`, a runtime dependency the package does not have |
| `widgets/pathfield/pathRows.ts` (156) | Path completion rows | none | native imports this file (2 sites) | PACKAGE CANDIDATE (2 consumers) | none |
| `widgets/disclosure/disclosureStore.ts` (142) | Disclosure open/closed state | none | native imports this file (1 site) | PACKAGE CANDIDATE (2 consumers) | zustand |
| `widgets/toast/store.ts` (56) | Toast queue | none | native has its own | PLATFORM-ONLY | — |
| `auth.ts` (56) | Browser auth token handling for the socket URL | none | `mobile-native` uses expo-secure-store | PLATFORM-ONLY | — |
| `panes/doc/{docFile,openDoc}.ts` (60) | Doc pane file loading | none (uses `protocol/docContent.ts`) | none | PACKAGE CANDIDATE | `docContent.ts` is browser-shaped in three ways, not one: `docFileRawURL:58` and `docImageURL:97` build **same-origin-relative** `/doc/file?...` and `/doc/image?...` paths (no origin at all, so a native app cannot use them), and `readDocFile:74` calls **global `fetch`** with `credentials: "same-origin"` to carry the hub auth cookie. A device has no cookie jar and no same origin; all three need injection |

## 4. `mobile/src` (9 modules, 7,615 lines; 10 rows — one covers a native-internal duplicate pair)

The checkpoint doc (`docs/design/mobile/2026-09-09-iphone-main-checkpoint.md:7`)
names eight runtime modules; the ninth below is dead.

| Module (lines) | Owns | Wire surface | Counterpart | Class | Seam |
| --- | --- | --- | --- | --- | --- |
| `state/conversation.ts` (3369) | The active session's projection, draft, cursor, mutation lifecycle, generation fencing | `evener/{goal/updated,task/updated,thread/name/changed,thread/resync}`, `evener/sandbox/escalation/{requested,resolved}` | `stores/threads.ts` (2997) + `protocol/reducer.ts` (1630) | DUPLICATED | this is the big one: two independent notification appliers over the same wire |
| `state/activity.ts` (495) | `ActivityView` + identity/generation fencing, relocation→rehydrate rules | (validates notification payloads) | `stores/activityPanel.ts` (371) + `activitySummary.ts` (327) | DUPLICATED | the rehydrate discipline here is stricter than the web's; keep it |
| `services/conversation.ts` (1263) | Typed capability-gated RPC layer over `AppwireClient`; open epochs | `evener/thread/name/set` plus generated `MethodName`s | the client-facing half of `stores/threads.ts` | DUPLICATED | closest thing to "the shared service layer" that exists; a good base |
| `conversation/project.ts:774-787` and `services/activity.ts:437-451` (two `projectUsage`) | The same ten-field copy off `Thread["evener"]`, written twice inside the native tree | none | each other; `protocol/reducer.ts:797-805` does the same copy with `?? 0`/`?? null` where these pass `undefined` through | DUPLICATED | native-internal, no boundary to cross — the smallest real duplication in this document |
| `services/activity.ts` (481) | Thread diagnostics/tasks/jobs/usage → `ActivityView`; redaction allowlist | none (pure over `Thread`) | `protocol/activityData.ts` (702) + `panes/session/chrome/taskData.ts` | DUPLICATED | different source (embedded `Thread.evener` vs `evener/jobs/list` tree) for overlapping output |
| `services/roster.ts` (114) | `thread/list` → `RosterEntry[]` with attention classification | `thread/list` | none — the web reaches the same UX through `evener/navigation/read` | PACKAGE CANDIDATE | pure projection; note the two surfaces answer the same question differently |
| `services/newSession.ts` (105) | New-session parameter assembly | none | `panes/spawn/startThread.ts` | PACKAGE CANDIDATE | pure |
| `conversation/model.ts` (227) | `MobileConversation`/`MobileTimelineItem` view types | none | `protocol/model.ts` (369) `ThreadModel`/`ItemModel` | DUPLICATED | two view models over one wire type |
| `conversation/project.ts` (787) | `Thread` → `MobileConversation`: classification, clustering, approvals, queue | none | `protocol/reducer.ts` hydrate + `transcriptDisplay/projector.ts` | DUPLICATED | the web splits this into two layers (transport model, then display projection); native does it in one |
| `dev/conversationFixtures.ts` (774) | Geometry-test fixtures | none | — | dead code | imports `../services/nativeProfiles` and `../state/connection`, **neither of which exists**; no importer, so `tsc --project mobile-native/tsconfig.check.json` never reaches it (that tsconfig has no `include`, so `mobile/src` enters only through the import graph). Delete or restore in its own PR. |

## 5. The `protocol/` directory itself (16 top-level modules, 7,233 lines;
`testing/` adds 799, for the 8,032 the directory holds in all)

| Module (lines) | In tarball? | Consumers | Class |
| --- | --- | --- | --- |
| `index.ts` (9) | yes | — | SHARED ALREADY |
| `client.ts` (837) | yes | web (12 sites), native (6) | SHARED ALREADY |
| `errors.ts` (260) | yes (only 3 of its 19 exported declarations via `index.ts`) | web (88), native (13) | SHARED ALREADY |
| `transport.ts` (20) | yes | web (3), native (5) | SHARED ALREADY |
| `types.gen.ts` (2505) | yes | web (229), native (78) | SHARED ALREADY |
| `askAnswers.ts` (100) | yes | web (3), native (2) | SHARED ALREADY |
| `model.ts` (369) | yes, since #1184 | web (129), native 0 | PACKAGE CANDIDATE |
| `reducer.ts` (1630) | yes, since #1184 | web (11), native 0 | PACKAGE CANDIDATE |
| `activityData.ts` (702) | yes, since #1184 | web (15), native (6) | PACKAGE CANDIDATE (2 consumers) |
| `activityList.ts` (188) | yes, since #1184 | web 0, native (3) | PACKAGE CANDIDATE — the only protocol module the web does not use |
| `activityMerge.ts` (278) | yes, since #1184 | web (2), native 0 | PACKAGE CANDIDATE |
| `sendQueueAvailability.ts` (147) | yes, since #1184 | web (2), native via `submitRouting` | PACKAGE CANDIDATE (2 consumers) |
| `sessionErrors.ts` (42) | yes, since #1184 | web (2), native (2) | PACKAGE CANDIDATE (2 consumers) |
| `stableDelegate.ts` (12) | yes, since #1184 | web (3), native (1) | PACKAGE CANDIDATE (2 consumers) |
| `jobOutput.ts` (35) | yes, since #1184 | web (2), native (1) | PACKAGE CANDIDATE (2 consumers) |
| `docContent.ts` (99) | **yes, since #1184** | web (5), native 0 | PACKAGE CANDIDATE — the module ships and `docFileRawURL`/`docImageURL`/`DOC_FILE_MAX_BYTES`/`DocFileError` are exported and qualified, but `readDocFile` is deliberately kept off `index.ts` until C24 gives it a base-URL and fetch port |
| `testing/` (6 files, 799 lines) | **no**, deliberately | 136 web files import `testing/fakeClient`; 25 name `AppwireClientLike` | PACKAGE CANDIDATE — the type must leave `testing/` |

## 6. The package boundary

### How the package is consumed today

Three different ways, only one of which is qualified.

1. **Tarball, qualified.** `make test-api-package` →
   `protocol/scripts/qualify-package.mjs`: `npm pack`, install outside the
   checkout, ESM + CJS type-check and runtime import, declaration check, then the
   shipped examples against a scripted `ws` server. This exercises exactly the
   eleven `index.ts` runtime exports, across four generated consumer programs.
2. **Deep relative import from the web** (`../protocol/model`,
   `../protocol/reducer`, `../protocol/testing/fakeClient`). Compiled by Vite and
   `tsc --noEmit` in `make test-web`. Nothing checks these files stay packable.
3. **Deep relative import from `mobile/src` and `mobile-native`**
   (`../../../cmd/evener-hub/frontend/src/protocol/...`). Compiled by Metro and
   `tsc --project mobile-native/tsconfig.check.json` in `make test-native`.

Only (1) would survive the package being consumed as a real dependency. (2) and
(3) work because everything is one checkout.

### What a shared state core would require

Of the qualification runner:

- A second entry in `package.json` `exports` for the state subpath, plus its
  source files in `tsconfig.build.json` `files` (or a switch to `include`).
- All four generated consumer programs in `qualify-package.mjs` extended
  to import from the new subpath under both ESM and CJS `NodeNext` resolution.
  Today they name every export literally; a subpath doubles that list.
- A decision about dependencies. The runner installs with `--offline
  --ignore-scripts` into a temp dir; a `zustand` dependency would have to
  resolve there. The simplest answer is that the shared core ships a
  framework-free store (a `getState`/`subscribe`/`setState` triple, which is what
  `zustand/vanilla`'s `createStore` already gives the web) and each app wraps it
  in its own `useStore`. That keeps the zero-dependency property.
- The runner also asserts the tarball contains no source files. A state core
  that is only useful with test fixtures (`protocol/testing/`,
  `stores/navigation/testing.ts`) needs either a separate `testing` subpath that
  ships built, or those fixtures stay app-side.

Of the generated types: nothing changes. `types.gen.ts` is generated from Go by
`make generate` and is already both the largest shipped file and the shared
vocabulary. A state core consumes it; it does not extend it. The one caution is
`TaskListResponse.data` and `JobsListResponse.data`, typed `unknown` because the
Go side is `any` (`panes/session/chrome/taskData.ts:1-4` documents this) — the
parsers for those (`taskData.ts`, `parseActivityTree`) are hand-written and must
move with the state core, or the core hands `unknown` back to both apps and the
duplication survives.

### Which is smaller: `mobile/src` into the package, or a `state/` subpath?

**A `state/` subpath inside the package is smaller**, and `mobile/src` should
not move as-is. Reasons, in order of weight:

- `mobile/src` is 7,615 lines of which 3,864 (`state/conversation.ts` 3,369,
  `state/activity.ts` 495) are zustand stores. Moving them wholesale imports zustand
  into a zero-dependency package and moves *one* app's state model into a
  package the other app does not use — the duplication would be enshrined, not
  removed.
- `mobile/src` depends on the package by relative path today; moving it inside
  turns those into intra-package imports, which is a strict improvement, but it
  does nothing for the web, which imports none of it.
- The shared logic is not in `mobile/src` alone. The 88 non-`protocol` import
  sites from `mobile-native` into the web tree say the opposite: the largest
  already-shared body of code lives in `cmd/evener-hub/frontend/src/{stores,
  panes,keybindings,transcriptDisplay,widgets}`. A `state/` (or better, several:
  `state/`, `projection/`, `settings/`) subpath gives both sides a name to
  import, and the first PRs move the modules that already have two consumers.
- `mobile/src` can then dissolve module by module into that subpath, which is
  the same motion as the web stores' and can share the sequence.

Cheapest first step either way: put the unpacked `protocol/*.ts` modules into
the build and move `AppwireClientLike` out of `protocol/testing/`. That alone
converts 3,502 lines from "in the folder" to "in the package" and gives every
later PR a place to land. Both are now written: see "Since this was written".

## 7. Counts

| Class | Rows |
| --- | --- |
| SHARED ALREADY | 6 |
| DUPLICATED | 33 |
| PACKAGE CANDIDATE | 55 (of which 34 already have two consumers via deep relative import) |
| PLATFORM-ONLY | 13 |
| dead code | 1 |
| **Total rows** | **108** |

Rows cover 32 web `stores/` modules, 49 rows for web modules outside `stores/`
(several rows group a directory), 9 `mobile/src` modules, and 17 rows for
`protocol/` (its 16 top-level modules plus the `testing/` directory).

## Since this was written

Recorded 2026-09-12, refreshed after round 6; statuses observed at
`3bf357337`. Re-query before acting.

| Plan PR | GitHub | State | What it changed here |
| --- | --- | --- | --- |
| A1 | #1184 | **merged** as `f2599d1ed` | Shipped **all ten** unpacked modules, `docContent.ts` included (`files` goes 6 → 16) — the earlier plan held the whole module back; the PR shipped it and split it at the entry point instead. `docFileRawURL`, `docImageURL`, `DOC_FILE_MAX_BYTES` and `DocFileError` are exported and qualified (`qualify-package.mjs:110-111,155`); `readDocFile` is deliberately absent from `index.ts`, with the reason written there, because it hardcodes a relative URL and the browser `fetch` global. So C24 owns the port and the re-export, not the move. The runner also smoke-calls every other shipped module |
| A2 | #1188 | **merged** as `3bf357337` | `protocol/clientLike.ts` declares `AppwireClientLike`, exported from `index.ts` and in the build `files`. 25 importers rewritten, `FakeClient` imports left alone — §0 fact 3 above is corrected to match |
| A5 | #1186 | **merged** (`2245f9715`) | Deleted the dead `mobile/src/dev/conversationFixtures.ts`, which takes `mobile/src` from the 7,615 lines tabulated in §4 down to 6,841 and made `mobile-native` typecheck every file under `mobile/src`, so the gap that hid it is closed too |
| B3b | #1203 | **merged** as `4da382482` | Native's two `projectUsage` copies collapsed to one; that row's duplication is closed |
| B1 | #1189 | open | `protocol/itemFailure.ts`; the web and native predicates were byte-identical in behavior. Surfaced the third predicate now recorded above |
| — | #1190 | open issue | The `toolRenderers.ts:189` divergence |
