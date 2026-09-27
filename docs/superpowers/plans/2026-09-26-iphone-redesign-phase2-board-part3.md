# iPhone redesign, Phase 2: the Board, Implementation Plan, part 3

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

This is part 3 of phase 2. Part 1 (`docs/superpowers/plans/2026-09-26-iphone-redesign-phase2-board.md`, on main) carries the Goal, Architecture, Tech Stack, Spec, Global Constraints, Rulings 1-10 and Review Focus that bind every task here, and Tasks 1-8, 16 and 17. Part 2 (`docs/superpowers/plans/2026-09-26-iphone-redesign-phase2-board-part2.md`, on main since #2491) carries Tasks 9, 11, 14 and 15. This file designs the three tasks part 2 held through five review rounds, each of which found real bugs in them: Task 10 (projects and hosts, test runs, archived), Task 12 (swipes and the long-press menu) and Task 13 (select mode and a list that holds still). Task numbers continue part 1's; each task here is split into steps of work (10.1, 10.2, ...) that each carry their own test cycle and commit.

**Goal:** The Board's Projects/Hosts, Test runs and Archived sections, swipe and long-press actions on every session row, select mode, and a list that never moves under your finger, each built on a mechanism the app already runs rather than a new one.

**Architecture:**
- **Task 10.** The web rail's host grouping (`cmd/evener-hub/frontend/src/shell/rail/railNodes.ts`) moves into the shared package, so the web and the phone place rows by one rule: every row sits under the host its own `host_id` names, because the hub has no host-scoped project read. A pure `projectTree.ts` turns the three project catalogs into a flat list of Board items, and `projectBrowser.ts` gains the catalogs and the archived tier.
- **Task 12.** Every row action reuses the path the Session or the Projects screen already takes for the same change: Stop through the durable mutation runtime, registered and fenced exactly as a Session screen registers itself; Archive through `NavigationActions` and the organization journal; Pin by opening `PinAssignment`; Shut down and Rename as the Session's direct requests.
- **Task 13.** An explicit state machine over the list's touch and scroll events decides when the list is held. `HeldOrder` keeps order and membership while held and applies everything at once when the list settles.

**Tech Stack:** Part 1's, plus `react-native-gesture-handler` (~2.32.0), `react-native-reanimated` (4.5.1) and `react-native-worklets` (0.10.1), the versions Expo SDK 57 pins (`expo/bundledNativeModules.json`), installed by part 2's Task 11. `@expo/ui` (~57.0.16) or `@react-native-menu/menu` arrive only if Task 12.6's spike keeps one.

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: principle 2 (Calm), 7.1 (Projects/Hosts, Test runs, Archived, the toolbar's Select), 7.3 (row interactions), 14 (outbox), 16.1 (color), 16.5 (iconography) and 16.6 (motion). Roadmap: `docs/superpowers/plans/2026-09-25-iphone-redesign-roadmap.md`.

## Global Constraints

Part 1's Global Constraints bind every task. In addition:

- **Copy, verbatim from the spec:**
  - The toast after an archive: "Archived", with the action "Undo", for 8 seconds (spec 7.3). Stop's toast: "Stopped" (spec 8.3). Shut down's: "Session shut down" (spec 5).
  - Organize by's two choices: "Project, then host" (the default) and "Host, then project". The section title flips between "PROJECTS" and "HOSTS" (spec 7.1).
  - Section headers: "PROJECTS", "Test runs · 3", "ARCHIVED · 271" (spec 7.1's layout). Inside a project: the captions "Today" and "Recent", and the fold "Archived · 12". A host that isn't connected shows an amber "Offline"; a connected host shows no state.
  - Long-press actions, in spec 7.3's order: "Pin to category…", "Mark as read" or "Mark as unread", "Stop", "Shut down", "Archive" or "Unarchive", "Rename". Select mode's bar: "Done", "Archive", "Pin", "Mark as read".
- **Routes:** no new route names or params. Pin opens `PinAssignment` with `{ hubId, ref, title }`, as the Session's menu does (`screens.tsx:1270-1277`).
- **Storage:** one new kv-store key, `evener.native.board-organize.${hubId}`, cleared by `forgetBoard` and so by `ConnectionProvider.removeHub` (part 1 Task 3).
- **Mutations** reach the hub only by the paths in "How the Board's actions reach the hub" below: never a raw `turn/interrupt` request, and never a second `NavigationActions` mounted on the Board (ruling 16).
- **Color** (spec 16.1): swipe actions are ink, never a hue. Archive and Pin fill `inkMid`, Stop fills `inkHi`, More fills `inkLow`, and their labels and glyphs are `page`. The Needs you wash is `attentionBg`. Shut down's label in the menu is `dangerInk` (destructive).
- **Motion** (spec 16.6): rows move with `LinearTransition.springify().duration(250).dampingRatio(1)`. A row that enters Needs you washes `attentionBg` from full to nothing over 1200ms. With Reduce Motion, rows change places without the spring and the wash stays (ruling 23).
- **Gestures** (spec 7.3): a swipe that begins within 24pt of the screen's left edge never acts on a row.
- **Tests** meet the hub at the request boundary: `wireV2` fixtures (`@evener/appwire-client/testing/navigation`), `FakeClient` (`@evener/appwire-client/testing/fakeClient`), and the real `NativeMutationRuntime` over `openSqliteSyncDouble()` (`src/sqliteSync.testkit.ts`). They mock `react-native-gesture-handler` and `react-native-reanimated` only through the testkit's `gestureHandlerModuleMock()` and `reanimatedModuleMock()` (added in Tasks 12.4 and 13.3).
- **Local gates** (roadmap landing rules): each task's own tests, `cd mobile-native && npm run check`, and `make test-native-bundle` when imports, `metro.config.js` or `app.json` change. Task 10.1 also runs the web and package gates its steps name. CI runs the full matrix: never run the full `make test-native`, `make test-web` or `make lint` locally.
- **Repo rules** (AGENTS.md): never run Biome in `mobile-native`; for the shared package and the web run it only from `cmd/evener-hub/frontend` (`npx biome check --write <paths>`). Never `npm ci` through a symlinked `node_modules`. Never `git add -A`. iPhone only: Android keeps type-checking, and every iOS-only call (`ActionSheetIOS`, `Alert.prompt`) keeps an `Alert.alert` fallback or does nothing off iOS.

## How the Board's actions reach the hub

Each Board action takes the path the app already uses for the same change. The durable mutation outbox (`evener-mutations.db`) and the organization journal (`evener.native.navigation-action.${hubId}`) are the two recovery stores; nothing here adds a third.

| Action | How the Session (or the Projects screen) does it today | How the Board does it | Registration and recovery |
|---|---|---|---|
| Stop | `mutate("interrupt")` (`screens.tsx:1841-1878`) → `store.interrupt` (`mobile/src/state/conversation.ts:4379`, `requireControl(…, "stop", …)` at `:4382`) → `submitMutation` (`:497-510`, `instanceId ?? threadId`) → the screen's `NativeMutationHost.submit` → `NativeMutationRuntime.submit` (`nativeMutationRuntime.ts:369-381`) → `enqueueInterruptAndCancel` (`mutationOutboxStorage.ts:247-284`) → the dispatcher sends `turn/interrupt` with the record's `clientMutationId` | `BoardStops.stop` (Task 12.2): `createNativeMutationHost(getNativeMutationRuntime(), hubId, ref, client)` and `start()`, as the Session's host effect does (`screens.tsx:1035-1058`); `beginRead`, then the Session's bounded projection read without its subscription (`mobile/src/services/conversation.ts:767-775`); `sessionControls(…).stop`; `host.submit({ kind: "interrupt", … })`; then `host.reconcileRead`, which opens the dispatch gate | The runtime's target `nativeMutationTargetKey(hubId, ref)`: `registerTarget` (`nativeMutationRuntime.ts:247`), `beginAuthoritativeRead` (`:282`), `reconcileAuthoritativeRead` (`:304`). The interrupt lives in the mutation outbox, and the registration is held until it leaves `submitting` (ruling 17) |
| Archive, Unarchive | No Session archive today. `ProjectsScreen`'s `PageList` (`ProjectsScreen.tsx:124-188` and `:483`) → `NavigationActions.archive` (`navigationActions.ts:89-94`) → `evener/archive/set` | `archiveSession(actions, archiveTarget(row), archived)` (Task 12.3) through the Board's one `NavigationActions` (`useBoardOrganization`, Task 10.5) | The organization journal, `organizationJournal(hubId)` (`nativeOrganization.ts:23`), checked with `readOrganizationNavigation` (`organizationNavigation.ts:18`) |
| Pin (a row) | ⋯ menu → `navigation.navigate("PinAssignment", { hubId, ref, title })` (`screens.tsx:1270-1277`), which phase 3 keeps (its Task 14) | The same navigate (Tasks 12.5 and 12.7) | `PinAssignment`'s own `usePinNavigation` and the organization journal |
| Pin (select mode) | none | `pinSession(actions, { sessionRef, sectionId })` or `{ sessionRef, sectionName }` (Task 12.3) → `NavigationActions.assignPin` (`navigationActions.ts:101-106`) | The organization journal, checked with `refreshPinNavigation` (`pinNavigation.ts:45`) |
| Project Pin to top, Archive project | `ProjectsScreen` (`ProjectsScreen.tsx:659-671`): `NavigationActions.favorite` and `.archive` with `{ kind: "project", id, workingDir }` | The same calls through `useBoardOrganization` (Task 10.6) | The organization journal, checked with `readOrganizationNavigation` |
| Shut down | Session sheet → `SessionControls.shutdown` (`sessionControls.ts:85`) → `service.shutdown` → `thread/shutdown { ref }` (`conversation.ts:1128-1132`) | `shutDownSession(client, ref)` (Task 12.3), after a confirmation | None: a direct request, as the Session's is |
| Rename | Session sheet → `SessionControls.rename` → `service.rename` → `evener/thread/name/set { ref, name }` (`conversation.ts:1197-1201`) | `renameSession(client, ref, name)` (Task 12.3), from an `Alert.prompt` | None: a direct request, as the Session's is |
| Mark as read, unread | none | `seenMarkers(hubId).markSeen(row)` and `.markUnread(ref)` (part 1 Task 3) | This phone's own seen markers |

## What part 2's review found, and where this plan answers it

Every Medium-or-higher finding RoboRev raised on Tasks 10, 12 and 13 across part 2's six reviews (#2491), with the Lows on the same tasks:

| Round | Finding | Answer here | Pinned by |
|---|---|---|---|
| 0 | Search's scroll-to-project had no target in "Host, then project" | `projectRevealTarget` names the folds to open and the item to scroll to in every grouping; for a project on several hosts, in host mode, it is the canonical copy | Task 10.4 |
| 0 | `projectBrowser.ts` couldn't produce the archived tier | `ProjectSessionTier` gains `"archived"`, and every expanded project reads three tiers | Task 10.2 |
| 0 | Task 12's prose sent organization changes to the raw client | The table above names each action's path | Tasks 12.2, 12.3 |
| 1 | `HeldOrder.release()` took no snapshot | `release(next)` returns `next` in one call | Task 13.1 |
| 1 | `HeldOrder` dropped departed rows while held | A departed row keeps its last content until release | Task 13.1 |
| 1 | Offline and connected host headers were ambiguous | An offline host shows an amber "Offline" and no count; a connected host shows its live count when every Live page is loaded, else nothing | Task 10.4 |
| 1, 2 | The edge-zone rule couldn't be met by a start-x check alone: the row's recognizer still claims the touch | Two layers: a touch in the band never reaches the row, because a negative `hitSlop` takes the band out of the row's hit frame (checked in the simulator, with a fallback that covers the band with a sibling view), and a pure start-x guard refuses anything a swipe from the band opened | Tasks 12.3, 12.4 |
| 1 | `@expo/ui` and `@react-native-menu/menu` weren't declared | The spike installs only the library it keeps, and regenerates the lock; the sheet needs neither | Task 12.6 |
| 1 | An omitted `sources` means "local" and wasn't handled | The shared `projectHostIds(sources, rows)` reads an omitted or empty list as this hub | Tasks 10.1, 10.4 |
| 2 | Stop bypassed the durable path (`clientMutationId`, `enqueueInterruptAndCancel`, the stop epoch) | `BoardStops` submits through the runtime, so the cancel and the epoch land in the interrupt's own write | Task 12.2 |
| 2 (Low) | No tests for the full swipe, Undo, dimming or failure | Each has a test | Tasks 12.3, 12.4, 12.5 |
| 3 | `stopSession` couldn't be written as declared (no `hubId`, no client) | `new BoardStops(getRuntime, hubId).stop(client, ref)` | Task 12.2 |
| 3 | Settling raced momentum, and release needed the next bands | The state machine's `lifted` state waits 100ms for a drag or momentum; `release(next)` | Task 13.1 |
| 3 | Organize by overloaded `FoldedSections` | Its own key, `evener.native.board-organize.${hubId}`, with a round-trip test | Task 10.3 |
| 3 (Low) | Test runs, Archived and a project's archived group had no fold keys | Named: `test-runs`, `archived`, and `<project fold>:archived` | Task 10.4 |
| 3 (Low) | The back-swipe spike had no fallback | The band outside the swipeable | Task 12.4 |
| 4 (High) | Stop never dispatched: the Board registered no target and opened no read gate | `BoardStops` registers through `createNativeMutationHost` and reconciles a fenced read; the test proves `turn/interrupt` reaches the client and the registration is released | Task 12.2 |
| 4 | Settling from `onScrollEndDrag` raced `onMomentumScrollBegin` | The `lifted` state again; a fake-timer test proves a momentum begin inside the 100ms keeps the list held | Tasks 13.1, 13.2 |
| 4 (Low) | `thread/read` lacked `includeTurns`, and `instanceId` its fallback | `thread/read { ref, includeTurns: true, itemsView: "fragment", itemLimit: READ_ITEM_LIMIT }` and `instanceId ?? thread.id` | Task 12.2 |
| 5 | Host grouping duplicated a multi-source project's sessions and misreported their ended state | Rows sit under their own host (the shared `sessionGroupHostId`); a project's pages are read once and shared by its copies | Task 10.4 |
| 5 (Low) | The Organize by storage shape wasn't stated | Its own key (ruling 13) | Task 10.3 |

The design also closes gaps the review didn't reach: two `NavigationActions` on one screen settle each other's journal entries mid-flight (ruling 16); the organization journal can confirm only a local session's archive (ruling 20); a Stop must write its cancel before the dispatch gate opens, or a never-sent message goes out first (ruling 17); a touch cancelled as a drag begins must not release the list in between (Task 13.1); and removing the Projects link rows would leave a project's Pin to top and Archive project unreachable (ruling 15).

## Rulings

Part 1's rulings 1-10 stand; these continue the numbering.

11. **A row sits under the host its own `host_id` names; a cluster sits under its newest member's host.** The hub has no host-scoped project read (`NavigationReadParams`, `appwire-client/typescript/types.gen.ts:2004-2016`, names no source), so a project's pages are read once and shared by every host copy of the project. A session therefore appears once, however many hosts own its project. This is the web rail's rule (`railNodes.ts` `hostProjectNodes` and `activeSessionNodes`), and Task 10.1 moves it into the shared package so both clients run one copy.
12. **A project's own counts and "more" rows read once.** In "Project, then host" they sit on the project row; in "Host, then project" on the canonical copy, the first host in display order with a loaded row (the web's overflow host). A project's live count is `rollup_live + rollup_attn`, the hub's working and waiting-on-you sessions (the hub counts idle live sessions in neither, `hubcore/tree.go:1314-1319`). A host's live count is its rows in Live, shown only when every Live and Needs you page is loaded; otherwise the host shows no count.
13. **Organize by is its own per-hub key,** `evener.native.board-organize.${hubId}`, holding `"project-host"` or `"host-project"`, cleared by `forgetBoard`. `FoldedSections` stores a flat map of fold flags (part 1 Task 3): a mode is not a fold, and a nested shape would need a migration on devices that already store the flat map. Per hub matches the web, whose `sidebarGrouping` lives in the hub origin's localStorage (`cmd/evener-hub/frontend/src/stores/prefs.ts:515`).
14. **Inside a project: Today (the hub's current tier, the last 24 hours, `hubcore/tree.go:334`), Recent, and a folded Archived group** (spec 7.1). In "Project, then host", when a project's loaded Today and Recent rows span more than one host, they split into per-host branches, folded by default as on the web (`hostBranchNodes`), and the Archived group stays at the project level. In "Host, then project", each project copy holds only its host's rows, in all three tiers. Archived sessions of active projects live in their project's Archived group. The Archived section lists archived projects (`archived_projects`), whose Archived group starts unfolded because it is the content. Test runs and Archived stay flat, as on the web (`Rail.tsx` `projectsTierFor`).
15. **Project rows get a long-press action sheet** (`ActionSheetIOS`, the app's existing native action surface, `TimelineItem.tsx:75-91`): "Pin to top" or "Unpin", and "Archive project" or "Unarchive project", for projects the phone can organize today (`controllerOwnedProject`, `ProjectsScreen.tsx:606-608`). Task 10.6 removes PR 2's link rows to the Projects screen, and these were that screen's only project actions; the spec shows pinned projects (7.1) but not how to pin one.
16. **One organization `NavigationActions` per screen: `useBoardOrganization`.** The journal holds one change per hub (`navigationActionRepository.ts` `begin` asserts the slot is empty). Two instances on a focused screen would each `reconcile()` and `finish()` the other's change while it is still in flight, and the one in flight then fails its `acknowledge`. The Board's instance checks archive and favorite changes with `readOrganizationNavigation` and pin changes with `refreshPinNavigation`; a session deletion is checked on its own screen. The Board never renders the journal's error text ("Refresh before trying again"). While a change is unresolved it hides organization actions, and it reconciles on focus and whenever the connection turns ready, as `usePinNavigation.ts:109-111` does.
17. **Stop from the Board is the Session's Stop:** a durable `turn/interrupt` through `NativeMutationRuntime.submit`, whose `enqueueInterruptAndCancel` cancels the session's never-sent rows and bumps its stop epoch in the interrupt's own write. The Board registers the target as a Session screen does, reads the thread as the Session's projection read does (bounded, and without the subscription the Board doesn't hold), and checks `sessionControls(…).stop` as `requireControl` does. It enqueues the interrupt before it opens the dispatch gate, so the cancel lands before anything on that session can send, as when you tap Stop in a session. It keeps the registration until the interrupt has left `submitting`, the connection drops, or the Board goes away. An interrupt still waiting then is delivered like any durable Stop: by the session when it is next opened, or by phase 6's flush. A read that shows nothing to stop sends nothing.
18. **Pin from a row opens `PinAssignment`** with `{ hubId, ref, title }`, exactly as the Session's menu does and phase 3 keeps. Select mode's Pin, which that screen can't do for several sessions, goes through the Board's organization journal after a category picker.
19. **Shut down and Rename are the Session's direct requests,** gated by the row's own facts as the web's rail gates them (`RailRow.tsx:512-513`): Shut down while `live && !offline && state !== "restartRequired"`, after a confirmation; Rename while the hub marks the row `rename: true`.
20. **Archive and Unarchive act on this hub's own top-level sessions** (`host_id === "local"`, a ref of the shape `localSessionId` accepts naming the row's `session_id`, and not a subagent, fork or cluster): the only ones the organization journal can confirm, since `readOrganizationNavigation` reads `local:<id>` alone (`organizationNavigation.ts:30-35`), and the rule `ProjectsScreen` applies (`ProjectsScreen.tsx:749-757`). Question 1 asks about other hosts.
21. **The Board's actions that write to the hub show only while connected** (principle 2; phase 6's Task 16 plans the same rule). Stop needs a fresh read to fence its interrupt, and an organization change taken offline fails at once and leaves the journal unresolved. Mark as read and unread are this phone's own and stay. Offline, the leading swipe reveals nothing and the trailing swipe shows only More. Question 2 (phase 6's Question 3) asks whether to queue them instead.
22. **The list holds still** while a finger is on it, while it scrolls or glides, while a scroll the app started animates, while a row's swipe actions are open, while the long-press menu is open, and while select mode is on (Question 3). Held, rows keep their places and membership and show fresh content; departures, arrivals and moves all apply together when it settles. A fold you tap applies on settle too, 100ms after your finger lifts.
23. **Reduce Motion** (spec 16.6): rows change places without the spring. The amber wash stays: it is a fade, not motion, spec 16.6 lists what Reduce Motion changes and the wash isn't on the list, and with rows jumping it is the only cue for where a row landed. Reduce Motion is read live (`AccessibilityInfo`, `reduceMotionChanged`), not only at launch.
24. **"Blue-gray" (spec 7.3's Archive action) is `inkMid`:** the palette has no blue-gray, and it allows four hues (spec 16.1). The swipe fills are in the Global Constraints.
25. **The toast is phase 3's `src/Toast.tsx`** (phase 3 Task 1, "A shared toast", in `docs/superpowers/plans/2026-09-26-iphone-redesign-phase3-session.md` on main). It lands here first, as that task writes it, since phase 2 lands before phase 3; phase 3's Task 1 then finds it on main, as its ruling 27 anticipated.
26. **Select mode ends when one of its actions completes,** which releases the list.
27. **Copy link waits for a session deep link,** which the app doesn't have (part 2; phase 3 ruling 22).

## Questions for Jesse

1. **Archive for sessions on other hosts.** The hub archives a remote session by its ref (the web does, `cmd/evener-hub/frontend/src/shell/rail/actions.ts:265-285`), but the phone's organization journal can only confirm a local session's archive (`organizationNavigation.ts:30-35`), so a lost reply for a remote one would leave the journal stuck. Should the phone archive remote-host sessions?
   *Recommendation:* yes, in a small follow-up: `readOrganizationNavigation` reads the remote row's `location` by its ref (about 20 lines and a test). Until then remote rows have no Archive swipe or menu item (ruling 20).
2. **Board actions taken offline.** This is phase 6's Question 3 (PR #2511); one answer covers both. Spec 7.5 says they "go to the outbox", but only the four turn kinds have a durable outbox. Organization changes use a one-slot journal that fails at once offline, and a Stop queued offline would stop whatever turn is running when it lands.
   *Recommendation:* those actions don't show while offline (ruling 21, which this plan builds); a durable queue for them is its own later piece of work.
3. **Should select mode hold the list?** Spec 7.3 holds it under a finger and while it scrolls. In select mode you choose rows by where they sit, and a session entering Needs you would move them between your taps.
   *Recommendation:* hold it until Done or an action (ruling 22). Rows still show fresh marks and text while held.

## Review Focus

1. **A project owned by two hosts, organized by host.** Each session must appear once, under the host its row names; an offline host shows only its own rows, as shut down; the online host's rows stay live. Pinned by Task 10.4 ("a project on two hosts shows each session once, under the host its row names" and "an offline host shows only its own rows, and they read as shut down").
2. **Stop from the Board.** It must reach the hub now, cancel the session's never-sent messages as a Session's Stop does, send nothing to a session that already finished, and leave no registration behind. Pinned by Task 12.2.
3. **A fling.** A drag that ends into momentum must never apply changes mid-glide, and a touch cancelled as a drag begins must not let one through in between. Pinned by Task 13.1's transition table and Task 13.2's timer tests.
4. **An organization change left unresolved** by another screen or a dropped connection. It must never leave the Board asking you to refresh, or stuck: the Board settles it on focus and on reconnect, and hides organization actions until then. Pinned by Task 10.5.
5. **A swipe from the left edge.** A swipe that begins in the 24pt band must never archive or reveal a row's actions, whichever way it moves. Pinned by Tasks 12.3 and 12.4.

---

## PRs and lanes

Part 1's table put Task 10 in PR 3 beside Task 9, and Tasks 11-13 in PR 4. Part 2, as merged, keeps PR 3 as Task 9 alone and PR 4 as Task 11 alone, and leaves these tasks' PRs to this plan. At about 1,100 and 1,600 lines of production code, Task 10 and Tasks 12-13 would each pass the roadmap's 800-line target as one PR, so Task 10 lands as two PRs beside and after PR 3, and Tasks 12-13 as three after PR 4, in lane B:

| PR | Tasks | Model | Starts when | Lane | Size |
|---|---|---|---|---|---|
| 3a | Tasks 10.1-10.4 | Sonnet (the plan carries the code) | part 1's PR 2 lands | B | about 650 lines |
| 3b | Tasks 10.5-10.6 | Sonnet for 10.5, Opus (medium) for 10.6 | PRs 3 and 3a land | B | about 470 lines |
| 4a | Tasks 12.1-12.5 | Sonnet for 12.1-12.3, Opus (medium) for 12.4-12.5 | PRs 3b and 4 land | B | about 700 lines |
| 4b | Tasks 12.6-12.7 | Opus (medium) | PR 4a lands | B | about 280 lines |
| 4c | Tasks 13.1-13.4 | Sonnet for 13.1-13.2, Opus (medium) for 13.3-13.4 | PR 4b lands | B | about 650 lines |

- Part 1 still says Projects and Archived open today's screens "until PR 3" (its PR 2 introduction and Task 7 requirement 5) and lists projects, hosts, test runs and archived under PR 3, and row actions, select mode and list stability under PR 4, in its closing "PRs 3, 5 and 4" section. Since part 2 moved those tasks here, read that PR 3 as PRs 3a and 3b (PR 3b draws the sections and removes the link rows, Task 10.6 requirement 9) and that PR 4 as PRs 4a to 4c.
- PR 3a touches none of Task 9's files (it adds pure modules and changes `projectBrowser.ts`, `boardMemory.ts` and the web rail), so it can run beside PR 3 when a lane is free.
- PR 3a and part 2's PR 5 (lane A) both edit `boardMemory.ts`'s `forgetBoard` and its forget tests: Task 10.3 adds the Organize by key, part 2's Task 15 the recent-searches key. The second to land merges `origin/main` and keeps every key.
- Part 2's Task 15 commit that wires search's project tap waits for PR 3b. Part 2 calls the thing it scrolls "Task 10's `ProjectsSection`"; in this design it is `revealProject(projectKey)` inside `BoardScreen` (Task 10.6), which unfolds and scrolls through `projectRevealTarget` (Task 10.4).
- Part 2's Task 14 counts a host notice over the rows the Board has loaded; whichever of PR 3b and part 2's PR 5 lands second adds the project pages' rows to that union (Task 10.6 requirement 12).
- Part 1's Task 17 screenshots include select mode (frame 7), so they ride on PR 4c, or on a later phase 2 PR if one lands after it.
- If part 2's Task 9 mounted `usePinNavigation` on the Board for a category's Rename and Delete, Task 10.6 moves those calls onto `useBoardOrganization` (ruling 16).
- Every PR lands under the roadmap's rules: CI green, RoboRev with nothing Medium or higher, /simplify, admin squash merge, Lows in a fast-follow, and decompose after five rounds.

---

## PR 3a: the projects' data

### Task 10.1: Host grouping moves into the shared package

The web rail already groups rows by host (`railNodes.ts:876-1185`). The Board needs the same rules; copying them would give the web and the phone two answers to "which host does this row sit under". This moves the rules, unchanged, into `@evener/appwire-client/state/navigation`, and the web imports them.

**Files:**
- Create: `appwire-client/typescript/state/navigation/hostGrouping.ts`
- Create: `appwire-client/typescript/state/navigation/hostGrouping.test.ts`
- Modify: `appwire-client/typescript/state/navigation/index.ts` (export the module)
- Modify: `appwire-client/typescript/README.md` (the `state/navigation` bullet, `:116-139`)
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts` (delete the private copies at `:905-955`, import the shared ones)

**Interfaces:**
- Produces (exported from `@evener/appwire-client/state/navigation`):
  - `const CONTROLLER_SOURCE_ID = "local"`
  - `interface HostFacts { id: string; label: string; online: boolean }`
  - `type HostPlacedRow = Pick<NavigationSessionSummary, "kind" | "host_id"> & { readonly children: readonly Pick<NavigationSessionSummary, "host_id">[] }`
  - `sessionGroupHostId(row: HostPlacedRow): string`
  - `orderedHosts(hostIds: Iterable<string>, sources: readonly Source[]): HostFacts[]`
  - `projectHostIds(sources: readonly string[] | undefined, rows: readonly HostPlacedRow[]): string[]`
  - `canonicalHostId(hostIds: readonly string[], rows: readonly HostPlacedRow[], sources: readonly Source[]): string`

- [ ] **Step 1: Write the failing test**

```ts
// appwire-client/typescript/state/navigation/hostGrouping.test.ts
import { expect, test } from "vitest";
import type { Source } from "../../types.gen";
import { canonicalHostId, orderedHosts, projectHostIds, sessionGroupHostId } from "./hostGrouping";

const source = (id: string, label: string, online = true): Source => ({
  id,
  label,
  kind: id === "local" ? "local" : "appwire",
  online,
});
const row = (host_id: string, kind = "session", children: { host_id: string }[] = []) => ({ kind, host_id, children });

test("orders this hub first, then online hosts by label, offline hosts last, and reads an unknown host as online", () => {
  const sources = [
    source("local", "this host"),
    source("zeta", "Zeta"),
    source("alpha", "Alpha"),
    source("down", "Aardvark", false),
  ];
  expect(orderedHosts(["down", "zeta", "ghost", "alpha", "local", "zeta"], sources)).toEqual([
    { id: "local", label: "this host", online: true },
    { id: "alpha", label: "Alpha", online: true },
    { id: "ghost", label: "ghost", online: true },
    { id: "zeta", label: "Zeta", online: true },
    { id: "down", label: "Aardvark", online: false },
  ]);
});

test("places a row under its own host, and a cluster under its newest member's host", () => {
  expect(sessionGroupHostId(row("paradise-park"))).toBe("paradise-park");
  expect(sessionGroupHostId(row("cluster", "cluster", [{ host_id: "devbox" }, { host_id: "local" }]))).toBe("devbox");
  expect(sessionGroupHostId(row("cluster", "cluster"))).toBe("local");
});

test("a project's hosts are its sources plus its rows' hosts, and this hub when it names neither", () => {
  expect(projectHostIds(undefined, [])).toEqual(["local"]);
  expect(projectHostIds([], [])).toEqual(["local"]);
  expect(projectHostIds(["local", "paradise-park"], [])).toEqual(["local", "paradise-park"]);
  expect(projectHostIds(undefined, [row("local"), row("devbox"), row("local")])).toEqual(["local", "devbox"]);
  expect(projectHostIds(["paradise-park"], [row("paradise-park")])).toEqual(["paradise-park"]);
});

test("the canonical host is the first in display order with a loaded row, else the first", () => {
  const sources = [source("local", "this host"), source("paradise-park", "paradise-park")];
  const both = ["local", "paradise-park"];
  expect(canonicalHostId(both, [], sources)).toBe("local");
  expect(canonicalHostId(both, [row("paradise-park")], sources)).toBe("paradise-park");
  expect(canonicalHostId(both, [row("paradise-park"), row("local")], sources)).toBe("local");
});
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/hostGrouping.test.ts`
Expected: FAIL: `Cannot find module './hostGrouping'`.

- [ ] **Step 3: Implement**

```ts
// appwire-client/typescript/state/navigation/hostGrouping.ts
// Host grouping: where a navigation row, and a project whose rows live on
// several hosts, sit when a client organizes its sessions by host (the web
// rail's "Organize by" and the phone Board's Projects/Hosts section). Pure
// projections of what the manifest and the rows already carry: a project's
// owning `sources`, a row's `host_id`, and the manifest's Source rows (label,
// online). The hub has no host-scoped project read (NavigationReadParams
// names no source), so a client reads each project's pages once and places
// every row under the host its own `host_id` names: a session shows once,
// however many hosts own its project.
import type { NavigationSessionSummary, Source } from "../../types.gen";

/** The source id the hub gives its own sessions; the wire spells it "local". */
export const CONTROLLER_SOURCE_ID = "local";

/** A host as a group heading: the manifest's label and online flag. */
export interface HostFacts {
  id: string;
  label: string;
  online: boolean;
}

/** What placing a row under a host reads from it. */
export type HostPlacedRow = Pick<NavigationSessionSummary, "kind" | "host_id"> & {
  readonly children: readonly Pick<NavigationSessionSummary, "host_id">[];
};

/** The host a row groups under. A cluster row's own host_id is synthetic:
 * "cluster", the scope prefix of its id, because the hub names no host for a
 * row it folded out of repeated titles (navigationNodeRef falls back to the
 * node ID, so the wire carries "cluster:<hex>"). It groups under its
 * most-recent member's host, the member the cluster itself carries recency
 * from; a memberless cluster (the hub never builds one) falls back to this
 * hub so the row still renders somewhere. */
export function sessionGroupHostId(row: HostPlacedRow): string {
  if (row.kind !== "cluster") return row.host_id;
  return row.children[0]?.host_id ?? CONTROLLER_SOURCE_ID;
}

/** The manifest's id to Source lookup, single-slot memoized on the sources
 * array's identity: a client hands back the manifest's own array, so every
 * call between manifest updates shares one Map instead of building one per
 * call. */
const sourceLookupCache: { sources: readonly Source[] | null; known: Map<string, Source> } = {
  sources: null,
  known: new Map(),
};

function sourceLookup(sources: readonly Source[]): Map<string, Source> {
  if (sourceLookupCache.sources !== sources) {
    sourceLookupCache.sources = sources;
    sourceLookupCache.known = new Map(sources.map((source): [string, Source] => [source.id, source]));
  }
  return sourceLookupCache.known;
}

/** Hosts in display order: this hub first, then online hosts by their labels
 * (the id breaking ties), offline hosts last (an offline host cannot reveal
 * rows until it reconnects, so it sorts behind the hosts that can). A host the
 * manifest does not name reads as online, the contract the web's session
 * chips follow, and falls back to its id as a label. */
export function orderedHosts(hostIds: Iterable<string>, sources: readonly Source[]): HostFacts[] {
  return [...new Set(hostIds)]
    .map((id) => {
      const source = sourceLookup(sources).get(id);
      return {
        id,
        label: source?.label ?? id,
        online: source ? source.online : true,
        tier: id === CONTROLLER_SOURCE_ID ? 0 : source ? (source.online ? 1 : 2) : 1,
      };
    })
    .sort((a, b) => a.tier - b.tier || a.label.localeCompare(b.label) || a.id.localeCompare(b.id))
    .map(({ id, label, online }) => ({ id, label, online }));
}

/** Every host a project's rows can sit under: its owning sources, plus the
 * host of every row loaded for it (a summary that predates a host still lands
 * where its rows are). A project naming neither (`sources` omitted or empty,
 * which the wire means as the controller's own) is this hub's. */
export function projectHostIds(sources: readonly string[] | undefined, rows: readonly HostPlacedRow[]): string[] {
  const hosts = new Set<string>(sources ?? []);
  for (const row of rows) hosts.add(sessionGroupHostId(row));
  if (hosts.size === 0) hosts.add(CONTROLLER_SOURCE_ID);
  return [...hosts];
}

/** The one host whose copy of a project carries the project's own counts and
 * paging when projects nest under hosts, so they read once instead of
 * claiming per-host numbers the wire does not carry: the first host in
 * display order with a loaded row, else the first host. A project no loaded
 * row names yet keeps the first host, so the choice cannot flip from copy to
 * copy while rows stream in. */
export function canonicalHostId(
  hostIds: readonly string[],
  rows: readonly HostPlacedRow[],
  sources: readonly Source[],
): string {
  const ordered = orderedHosts(hostIds, sources);
  const withRows = ordered.find(({ id }) => rows.some((row) => sessionGroupHostId(row) === id));
  return (withRows ?? ordered[0])?.id ?? CONTROLLER_SOURCE_ID;
}
```

In `appwire-client/typescript/state/navigation/index.ts`, add `export * from "./hostGrouping";` in alphabetical order (after `./codec`) and update the barrel's header comment to name host grouping.

In `appwire-client/typescript/README.md`'s `state/navigation` bullet, add after the selectors' sentence: "and host grouping (`hostGrouping`): where a row, and a project whose rows live on several hosts, sit when a client organizes sessions by host (the web rail's Organize by and the phone Board's Hosts section)". Change "re-exports the eight modules whole" to "re-exports the nine modules whole".

- [ ] **Step 4: Point the web at the shared rules**

In `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts`:

1. Extend the existing import from `@evener/appwire-client/state/navigation` (line 15) to:

```ts
import {
  canonicalHostId,
  orderedHosts,
  projectHostIds as ownerHostIds,
  projectNodeExpansionKey,
  sessionGroupHostId,
} from "@evener/appwire-client/state/navigation";
```

2. Delete lines 905-955: the `sourceLookupCache` comment, constant and `sourceLookup`, `type HostFacts`, `orderedHosts` and `sessionGroupHostId` with their comments. In their place, keep `RailRow.tsx`'s import working with one line:

```ts
export { sessionGroupHostId };
```

3. In the private `projectHostIds(p: RailProject)` (its cache stays), replace the body's four lines that build `hosts` and `result` with:

```ts
  const result = ownerHostIds(p.sources, p.sessions);
```

4. In `hostProjectNodes`, replace the three statements that compute `ordered`, `withRows` and `overflowHost.set(...)` (lines 1036-1040) with one, keeping the comment above them:

```ts
    overflowHost.set(p, canonicalHostId(projectHostIds(p), p.sessions.filter((n) => !isArchivedTier(n)), sources));
```

5. Delete the now-unused `import { LOCAL_HOST } from "../../stores/hostRouting";` (line 16). `stores/hostRouting.ts` keeps its `LOCAL_HOST` for its other readers; both spell the wire's `"local"`.

- [ ] **Step 5: Run the package and web tests, the typecheck and Biome**

```bash
cd cmd/evener-hub/frontend
npx vitest run ../../../appwire-client/typescript/state/navigation/hostGrouping.test.ts src/shell/rail/railNodes.test.ts src/shell/rail/Rail.test.tsx src/shell/rail/RailRow.test.tsx
npm run typecheck
npx biome check --write ../../../appwire-client/typescript/state/navigation/hostGrouping.ts ../../../appwire-client/typescript/state/navigation/hostGrouping.test.ts ../../../appwire-client/typescript/state/navigation/index.ts src/shell/rail/railNodes.ts
cd ../../.. && make test-api-package
```

Expected: PASS everywhere. `railNodes.test.ts`'s "host grouping (organize by)" tests pass unchanged: the rules moved, they didn't change.

- [ ] **Step 6: Commit**

```bash
git add appwire-client/typescript/state/navigation/hostGrouping.ts appwire-client/typescript/state/navigation/hostGrouping.test.ts appwire-client/typescript/state/navigation/index.ts appwire-client/typescript/README.md cmd/evener-hub/frontend/src/shell/rail/railNodes.ts
git commit -m "refactor(appwire-client): host grouping moves from the web rail into the shared package"
```

### Task 10.2: The project data: three catalogs, the archived tier, and expansion the caller decides

`createProjectBrowserController` (`mobile-native/src/projectBrowser.ts`) reads only the `projects` catalog, only the `current` and `recent` tiers, and expands the first project on its own, the old home's behavior. Its one caller, `ProjectSessionsList.tsx`, is deleted by part 1's Task 8, so these changes break nobody.

**Files:**
- Modify: `mobile-native/src/projectBrowser.ts` (replace the whole file with the code below)
- Modify: `mobile-native/src/projectBrowser.test.ts` (replace the whole file with the code below)

**Interfaces:**
- Produces:
  - `type ProjectCatalog = "projects" | "archived_projects" | "test_runs"`
  - `type ProjectSessionTier = "current" | "recent" | "archived"`
  - `createProjectBrowserController(client: ConversationClientLike, catalogName: ProjectCatalog = "projects"): ProjectBrowserController`
  - `ProjectBrowserGroup` gains `archived: PageSnapshot`; `sessions` stays the current and recent rows.
  - `ProjectBrowserController` gains `collapse(projectKey: string): void`. `initialLoad()` reads the catalog alone.

- [ ] **Step 1: Write the failing tests**

Replace `mobile-native/src/projectBrowser.test.ts` with:

```ts
import { describe, expect, it } from "vitest";
import type {
	AnyNotification,
	NavigationInvalidationTarget,
	NavigationReadParams,
	NavigationReadResponse,
} from "@evener/appwire-client";
import { wireV2 } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import {
	createProjectBrowserController,
	type ProjectSessionTier,
} from "./projectBrowser";

function boundary() {
	const requests: Array<{
		params: NavigationReadParams;
		resolve: (value: NavigationReadResponse) => void;
		reject: (error: Error) => void;
	}> = [];
	const listeners = new Set<(event: AnyNotification) => void>();
	const client: ConversationClientLike = {
		request: (_method, params) =>
			new Promise((resolve, reject) => {
				requests.push({
					params: params as NavigationReadParams,
					resolve,
					reject,
				});
			}),
		onNotification: (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	};
	return { client, requests, listeners };
}
type Pending = ReturnType<typeof boundary>["requests"][number];
function response(params: NavigationReadParams, data: unknown, revision = 1) {
	return wireV2(
		{
			...params,
			representationVersion: 2,
			offset: params.offset ?? 0,
			limit: params.limit ?? 50,
		},
		data,
		`etag-${params.offset ?? 0}-${revision}`,
		revision,
		"generation-test",
	);
}
const project = (key: string) => ({ key, name: key, session_count: 2 });
const session = (ref: string) => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "p",
	state: "idle",
	kind: "session",
	live: false,
	children: [],
});
type Row = ReturnType<typeof session>;
/** Answers one project's tier reads, each with its own rows and remaining. */
function answerTiers(
	pending: readonly (Pending | undefined)[],
	rows: Partial<Record<ProjectSessionTier, Row[]>> = {},
	remaining: Partial<Record<ProjectSessionTier, number>> = {},
	revision = 1,
) {
	for (const request of pending) {
		if (!request) continue;
		const tier = request.params.tier as ProjectSessionTier;
		request.resolve(
			response(
				request.params,
				{
					key: request.params.projectKey,
					tier,
					sessions: rows[tier] ?? [],
					remaining: remaining[tier] ?? 0,
					truncated: false,
				},
				revision,
			),
		);
	}
}
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
const label = (request: Pending | undefined) =>
	`${request?.params.projectKey ?? "catalog"}:${request?.params.tier ?? ""}`;

describe("project browser", () => {
	it("reads the catalog it is made for, the projects catalog by default", () => {
		for (const catalog of [undefined, "projects", "archived_projects", "test_runs"] as const) {
			const { client, requests } = boundary();
			const controller = createProjectBrowserController(client, catalog);
			void controller.initialLoad();
			expect(requests.map((request) => [request.params.resource, request.params.catalog])).toEqual([
				["catalog", catalog ?? "projects"],
			]);
			controller.dispose();
		}
	});

	it("loads the catalog alone, and a project's three tiers once it is expanded", async () => {
		const { client, requests, listeners } = boundary();
		const controller = createProjectBrowserController(client);
		expect(listeners.size).toBe(0);
		const loading = controller.initialLoad();
		expect(listeners.size).toBe(1);
		requests[0]?.resolve(
			response(requests[0].params, { projects: [project("a"), project("b")], remaining: 0 }),
		);
		await loading;
		expect(requests).toHaveLength(1);
		expect(controller.getSnapshot().groups).toEqual([]);
		const expanding = controller.expand("a");
		expect(requests.map(label)).toEqual(["catalog:", "a:current", "a:recent", "a:archived"]);
		answerTiers(requests.slice(1), {
			current: [session("a1")],
			recent: [session("a2")],
			archived: [session("a0")],
		});
		await expanding;
		const group = controller.getSnapshot().groups[0];
		expect(group?.expanded).toBe(true);
		expect(group?.sessions.map((row) => row.ref)).toEqual(["a1", "a2"]);
		expect(group?.archived.rows.map((row) => row.ref)).toEqual(["a0"]);
		expect(listeners.size).toBe(4);
		controller.dispose();
		expect(listeners.size).toBe(0);
	});

	it("collapse keeps a project's rows and stops it loading more", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		requests[0]?.resolve(response(requests[0].params, { projects: [project("a")], remaining: 0 }));
		await loading;
		const expanding = controller.expand("a");
		answerTiers(requests.slice(1), { current: [session("a1")] }, { current: 5 });
		await expanding;
		controller.collapse("a");
		await controller.loadMoreSessions("a", "current");
		expect(requests).toHaveLength(4);
		expect(controller.getSnapshot().groups[0]).toMatchObject({
			expanded: false,
			current: { rows: [{ ref: "a1" }] },
		});
		controller.dispose();
	});

	it("deduplicates equal session refs between current and recent tiers", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		requests[0]?.resolve(response(requests[0].params, { projects: [project("a")], remaining: 0 }));
		await loading;
		const expanding = controller.expand("a");
		answerTiers(requests.slice(1), { current: [session("same")], recent: [session("same")] });
		await expanding;
		expect(controller.getSnapshot().groups[0]?.sessions).toHaveLength(1);
		controller.dispose();
	});

	it("returns a stable snapshot and ignores a late catalog result after dispose", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		controller.dispose();
		requests[0]?.resolve(response(requests[0].params, { projects: [project("late")], remaining: 0 }));
		await loading;
		const disposedSnapshot = controller.getSnapshot();
		expect(controller.getSnapshot()).toBe(disposedSnapshot);
		expect(disposedSnapshot.projects.rows).toHaveLength(0);
		expect(disposedSnapshot.groups).toHaveLength(0);
	});

	it("appends a 20-row session page once and keeps rows when the next page fails", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		requests[0]?.resolve(response(requests[0].params, { projects: [project("a")], remaining: 0 }));
		await loading;
		const expanding = controller.expand("a");
		answerTiers(
			requests.slice(1),
			{ current: Array.from({ length: 20 }, (_, i) => session(`s${i}`)) },
			{ current: 5 },
		);
		await expanding;
		const more = controller.loadMoreSessions("a", "current");
		const duplicate = controller.loadMoreSessions("a", "current");
		await tick();
		expect(requests).toHaveLength(5);
		requests[4]?.reject(new Error("page unavailable"));
		await Promise.all([more, duplicate]);
		expect(controller.getSnapshot().groups[0]?.sessions).toHaveLength(20);
		await controller.loadMoreSessions("a", "current");
		expect(requests).toHaveLength(5);
		const retry = controller.retry("a", "current");
		await tick();
		expect(requests).toHaveLength(6);
		expect(requests[5]?.params.offset).toBe(20);
		expect(requests[5]?.params.limit).toBe(20);
		requests[5]?.resolve(
			response(requests[5].params, {
				key: "a",
				tier: "current",
				sessions: ["s20", "s21", "s22", "s23", "s24"].map(session),
				remaining: 0,
				truncated: false,
			}),
		);
		await retry;
		expect(controller.getSnapshot().groups[0]?.sessions).toHaveLength(25);
		controller.dispose();
	});

	async function loadedProject() {
		const { client, requests, listeners } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		requests[0]?.resolve(response(requests[0].params, { projects: [project("a")], remaining: 0 }));
		await loading;
		const expanding = controller.expand("a");
		answerTiers(requests.slice(1), { current: [session("a1")] });
		await expanding;
		const invalidate = (target: NavigationInvalidationTarget, sequence = 1) => {
			for (const listener of listeners)
				listener({
					method: "evener/navigation/invalidated",
					params: { generationId: "generation-test", sequence, targets: [target] },
				});
		};
		return { controller, requests, invalidate };
	}

	it("re-reads an invalidated project's three tiers without a refresh call", async () => {
		const { controller, requests, invalidate } = await loadedProject();
		invalidate({ kind: "project", projectKey: "a", revision: 2 });
		expect(requests).toHaveLength(7);
		expect(requests.slice(4).map((r) => [r.params.tier, r.params.offset])).toEqual([
			["current", 0],
			["recent", 0],
			["archived", 0],
		]);
		expect(controller.getSnapshot().groups[0]?.current).toMatchObject({
			stale: true,
			loading: true,
			rows: [{ ref: "a1" }],
		});
		answerTiers(requests.slice(4), { current: [session("a1"), session("a3")] }, {}, 2);
		await tick();
		expect(controller.getSnapshot().groups[0]?.current).toMatchObject({
			stale: false,
			loading: false,
			error: null,
		});
		expect(controller.getSnapshot().groups[0]?.sessions.map((row) => row.ref)).toEqual(["a1", "a3"]);
		controller.dispose();
	});

	it("retries a session page whose re-read failed by reading it again from the top", async () => {
		const { controller, requests, invalidate } = await loadedProject();
		invalidate({ kind: "project", projectKey: "a", revision: 2 });
		for (const request of requests.slice(4)) request.reject(new Error("offline"));
		await tick();
		expect(controller.getSnapshot().groups[0]?.current).toMatchObject({
			stale: true,
			error: "offline",
		});
		const retry = controller.retry("a", "current");
		await tick();
		expect(requests).toHaveLength(8);
		expect(requests[7]?.params).toMatchObject({ tier: "current", offset: 0 });
		answerTiers(requests.slice(7), { current: [session("a9")] }, {}, 2);
		await retry;
		expect(controller.getSnapshot().groups[0]?.current).toMatchObject({
			stale: false,
			error: null,
			rows: [{ ref: "a9" }],
		});
		controller.dispose();
	});

	it("retries a project catalog whose re-read failed by reading it again from the top", async () => {
		const { controller, requests, invalidate } = await loadedProject();
		invalidate({ kind: "catalog", catalog: "projects", revision: 2 });
		expect(requests).toHaveLength(5);
		requests[4]?.reject(new Error("offline"));
		await tick();
		expect(controller.getSnapshot().projects).toMatchObject({ stale: true, error: "offline" });
		const retry = controller.retry();
		await tick();
		expect(requests).toHaveLength(6);
		expect(requests[5]?.params).toMatchObject({ resource: "catalog", offset: 0 });
		requests[5]?.resolve(
			response(requests[5].params, { projects: [project("a"), project("b")], remaining: 0 }, 2),
		);
		await retry;
		expect(controller.getSnapshot().projects).toMatchObject({ stale: false, error: null });
		expect(controller.getSnapshot().projects.rows.map((row) => row.key)).toEqual(["a", "b"]);
		controller.dispose();
	});

	it("holds re-reads while paused and catches up on resume", async () => {
		const { controller, requests, invalidate } = await loadedProject();
		controller.pause();
		invalidate({ kind: "project", projectKey: "a", revision: 2 });
		expect(requests).toHaveLength(4);
		expect(controller.getSnapshot().groups[0]?.current.stale).toBe(true);
		controller.resume();
		expect(requests).toHaveLength(7);
		controller.dispose();
	});

	it("does not refetch on repeated initialLoad after the catalog is loaded", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		requests[0]?.resolve(response(requests[0].params, { projects: [], remaining: 0 }));
		await loading;
		await controller.initialLoad();
		expect(requests).toHaveLength(1);
		controller.dispose();
	});
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/projectBrowser.test.ts`
Expected: FAIL. The catalog parameter is ignored (`["catalog", "projects"]` for every catalog), `initialLoad` expands the first project on its own, no `archived` tier is read, and `collapse` is not a function.

- [ ] **Step 3: Implement**

Replace `mobile-native/src/projectBrowser.ts` with:

```ts
import type {
	NavigationProjectSummary,
	NavigationSessionSummary,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { NavigationPages } from "./navigationPages";

/** The hub's three project catalogs, one per Board section: Projects,
 * Archived and Test runs. */
export type ProjectCatalog = "projects" | "archived_projects" | "test_runs";
/** A project's session tiers: current (the last 24 hours, the Board's
 * "Today"), recent, and archived. */
export type ProjectSessionTier = "current" | "recent" | "archived";
const TIERS: readonly ProjectSessionTier[] = ["current", "recent", "archived"];
type PageSnapshot = ReturnType<
	NavigationPages<NavigationSessionSummary>["getSnapshot"]
>;
export interface ProjectBrowserGroup {
	project: NavigationProjectSummary;
	expanded: boolean;
	current: PageSnapshot;
	recent: PageSnapshot;
	archived: PageSnapshot;
	/** The current and recent rows, without duplicates. */
	sessions: NavigationSessionSummary[];
}
export interface ProjectBrowserSnapshot {
	projects: ReturnType<
		NavigationPages<NavigationProjectSummary>["getSnapshot"]
	>;
	groups: ProjectBrowserGroup[];
	loading: boolean;
	error: string | null;
}
export interface ProjectBrowserController {
	getSnapshot(): ProjectBrowserSnapshot;
	subscribe(listener: () => void): () => void;
	/** Reads the catalog. A project's sessions load once it is expanded. */
	initialLoad(): Promise<void>;
	expand(projectKey: string): Promise<void>;
	/** The project's rows are no longer shown; its loaded pages stay. */
	collapse(projectKey: string): void;
	toggle(projectKey: string): Promise<void>;
	loadMoreProjects(): Promise<void>;
	loadMoreSessions(projectKey: string, tier: ProjectSessionTier): Promise<void>;
	retry(projectKey?: string, tier?: ProjectSessionTier): Promise<void>;
	refresh(): Promise<void>;
	/** Stop re-reading on the hub's behalf while the list is not shown. */
	pause(): void;
	resume(): void;
	dispose(): void;
}

type Page = NavigationPages<NavigationSessionSummary>;
type Group = {
	project: NavigationProjectSummary;
	expanded: boolean;
} & Record<ProjectSessionTier, Page>;
const projectKey = (row: NavigationProjectSummary) => row.key;
const sessionKey = (row: NavigationSessionSummary) => row.ref;
const tierPages = (group: Group) => TIERS.map((tier) => group[tier]);

export function createProjectBrowserController(
	client: ConversationClientLike,
	catalogName: ProjectCatalog = "projects",
): ProjectBrowserController {
	const listeners = new Set<() => void>();
	const catalog = new NavigationPages<NavigationProjectSummary>(
		client,
		{ resource: "catalog", catalog: catalogName },
		"projects",
		projectKey,
		50,
	);
	const groups = new Map<string, Group>();
	let loading = false;
	let error: string | null = null;
	let disposed = false;
	let paused = false;
	let epoch = 0;
	const inFlightPages = new Set<string>();
	const unsubs = new Set<() => void>();
	let unwatchCatalog = () => {};
	let catalogWatchStarted = false;
	let cachedSnapshot: ProjectBrowserSnapshot = {
		projects: catalog.getSnapshot(),
		groups: [],
		loading: false,
		error: null,
	};
	const rebuildSnapshot = () => {
		const projectSnapshot = catalog.getSnapshot();
		cachedSnapshot = {
			projects: projectSnapshot,
			groups: projectSnapshot.rows.flatMap((project) => {
				const group = groups.get(project.key);
				if (!group) return [];
				const current = group.current.getSnapshot();
				const recent = group.recent.getSnapshot();
				const seen = new Set<string>();
				const sessions = [...current.rows, ...recent.rows].filter((row) => {
					if (seen.has(row.ref)) return false;
					seen.add(row.ref);
					return true;
				});
				return [
					{
						project,
						expanded: group.expanded,
						current,
						recent,
						archived: group.archived.getSnapshot(),
						sessions,
					},
				];
			}),
			loading,
			error,
		};
	};
	const publish = () => {
		if (disposed) return;
		rebuildSnapshot();
		for (const listener of listeners) listener();
	};
	const eachPage = (visit: (page: Page | typeof catalog) => void) => {
		visit(catalog);
		for (const group of groups.values())
			for (const page of tierPages(group)) visit(page);
	};
	const groupFor = (project: NavigationProjectSummary): Group => {
		const existing = groups.get(project.key);
		if (existing) return existing;
		const make = (tier: ProjectSessionTier) =>
			new NavigationPages<NavigationSessionSummary>(
				client,
				{ resource: "project_page", projectKey: project.key, tier },
				"sessions",
				sessionKey,
				20,
			);
		const group: Group = {
			project,
			expanded: false,
			current: make("current"),
			recent: make("recent"),
			archived: make("archived"),
		};
		groups.set(project.key, group);
		for (const page of tierPages(group)) {
			unsubs.add(page.subscribe(publish));
			unsubs.add(page.watch());
			if (paused) page.cancel();
		}
		return group;
	};
	const loadExpanded = async (key: string, force = false) => {
		const group = groups.get(key);
		if (!group || disposed || !group.expanded) return;
		const currentEpoch = epoch;
		await Promise.all(
			tierPages(group).map((page) =>
				force || !page.getSnapshot().loaded
					? page.refresh()
					: Promise.resolve(),
			),
		);
		if (disposed || currentEpoch !== epoch) return;
		if (tierPages(group).some((page) => page.getSnapshot().error))
			error = "Could not load sessions for this project. Retry to try again.";
		publish();
	};
	const controller: ProjectBrowserController = {
		getSnapshot: () => cachedSnapshot,
		subscribe(listener) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		async initialLoad() {
			if (
				disposed ||
				catalog.getSnapshot().loading ||
				catalog.getSnapshot().loaded
			)
				return;
			loading = true;
			error = null;
			if (!catalogWatchStarted) {
				unwatchCatalog = catalog.watch();
				catalogWatchStarted = true;
			}
			const currentEpoch = epoch;
			await catalog.refresh();
			if (disposed || currentEpoch !== epoch) return;
			error = catalog.getSnapshot().error;
			loading = false;
			publish();
		},
		async expand(key) {
			if (disposed) return;
			const project = catalog.getSnapshot().rows.find((row) => row.key === key);
			if (!project) return;
			const group = groupFor(project);
			group.expanded = true;
			error = null;
			publish();
			await loadExpanded(key);
		},
		collapse(key) {
			const group = groups.get(key);
			if (!group?.expanded) return;
			group.expanded = false;
			publish();
		},
		async toggle(key) {
			if (groups.get(key)?.expanded) controller.collapse(key);
			else await controller.expand(key);
		},
		async loadMoreProjects() {
			if (
				disposed ||
				catalog.getSnapshot().loading ||
				catalog.getSnapshot().error ||
				!catalog.getSnapshot().loaded ||
				!catalog.getSnapshot().remaining
			)
				return;
			if (inFlightPages.has("catalog")) return;
			inFlightPages.add("catalog");
			try {
				await catalog.more();
			} finally {
				inFlightPages.delete("catalog");
			}
			publish();
		},
		async loadMoreSessions(key, tier) {
			const group = groups.get(key);
			if (disposed || !group?.expanded) return;
			const page = group[tier];
			const pageKey = `${key}:${tier}`;
			if (
				page.getSnapshot().loading ||
				page.getSnapshot().error ||
				!page.getSnapshot().loaded ||
				!page.getSnapshot().remaining
			)
				return;
			if (inFlightPages.has(pageKey)) return;
			inFlightPages.add(pageKey);
			try {
				await page.more();
			} finally {
				inFlightPages.delete(pageKey);
			}
			publish();
		},
		async retry(projectKey, tier) {
			if (disposed) return;
			error = null;
			if (projectKey) {
				const group = groups.get(projectKey);
				if (!group?.expanded) return;
				const pages = tier ? [group[tier]] : tierPages(group);
				await Promise.all(
					pages.map((page) => {
						const state = page.getSnapshot();
						if (state.loading) return Promise.resolve();
						return state.loaded ? page.more() : page.refresh();
					}),
				);
				publish();
				return;
			}
			if (catalog.getSnapshot().error) {
				const state = catalog.getSnapshot();
				if (state.loading) return;
				if (state.loaded && state.rows.length > 0) await catalog.more();
				else await catalog.refresh();
				publish();
				return;
			}
			await Promise.all(
				[...groups.values()]
					.filter((group) => group.expanded)
					.map((group) => loadExpanded(group.project.key, true)),
			);
			publish();
		},
		async refresh() {
			if (disposed) return;
			error = null;
			loading = true;
			await catalog.refresh();
			if (disposed) return;
			await Promise.all(
				[...groups.values()]
					.filter((group) => group.expanded)
					.map((group) => loadExpanded(group.project.key, true)),
			);
			loading = false;
			publish();
		},
		pause() {
			paused = true;
			eachPage((page) => page.cancel());
		},
		resume() {
			paused = false;
			eachPage((page) => page.resume());
		},
		dispose() {
			if (disposed) return;
			disposed = true;
			epoch++;
			for (const unsubscribe of unsubs) unsubscribe();
			unwatchCatalog();
			unsubs.clear();
			eachPage((page) => page.cancel());
			listeners.clear();
		},
	};
	unsubs.add(catalog.subscribe(publish));
	return controller;
}
```

- [ ] **Step 4: Run them and watch them pass, then type-check**

Run: `cd mobile-native && npx vitest run src/projectBrowser.test.ts && npm run check`
Expected: PASS. If `npm run check` reports `ProjectSessionsList.tsx`, part 1's Task 8 hasn't landed: rebase onto `origin/main` once PR 2 is on main.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/projectBrowser.ts mobile-native/src/projectBrowser.test.ts
git commit -m "feat(native): project data reads any catalog and the archived tier, expanding only what the Board shows"
```

### Task 10.3: The Organize by choice gets its own key

**Files:**
- Modify: `mobile-native/src/board/boardMemory.ts` (part 1 Task 3, on main since #2518): the `OrganizeBy` type, the `OrganizeByPreference` class, and `forgetBoard` clearing the new key
- Modify: `mobile-native/src/board/nativeBoardMemory.ts` (the same): the per-hub `organizeByPreference(hubId)`
- Test: `mobile-native/src/board/boardMemory.test.ts`

**Interfaces:**
- Produces:
  - `type OrganizeBy = "project-host" | "host-project"`
  - `class OrganizeByPreference`: constructor `(storage: BoardStorage, hubId: string)`, `get(): OrganizeBy`, `set(value: OrganizeBy): void`
  - `organizeByPreference(hubId: string): OrganizeByPreference` (per-hub singleton, from `nativeBoardMemory.ts`)
  - `forgetBoard(storage, hubId)` also removes `evener.native.board-organize.${hubId}`.

- [ ] **Step 1: Write the failing tests**

Add to `mobile-native/src/board/boardMemory.test.ts` (add `OrganizeByPreference` to its import from `./boardMemory`):

```ts
describe("the Organize by choice", () => {
	it("starts at Project, then host and round-trips per hub", () => {
		const storage = memoryStorage();
		const first = new OrganizeByPreference(storage, "hub-a");
		expect(first.get()).toBe("project-host");
		first.set("host-project");
		expect(new OrganizeByPreference(storage, "hub-a").get()).toBe("host-project");
		expect(new OrganizeByPreference(storage, "hub-b").get()).toBe("project-host");
		expect(storage.values.get("evener.native.board-organize.hub-a")).toBe('"host-project"');
	});

	it("reads a value it doesn't know, or one that isn't JSON, as the default", () => {
		for (const stored of ['"sideways"', "host-project", "{not json"]) {
			const storage = memoryStorage(new Map([["evener.native.board-organize.hub-a", stored]]));
			expect(new OrganizeByPreference(storage, "hub-a").get()).toBe("project-host");
		}
	});

	it("never touches the folded sections, and they never touch it", () => {
		const storage = memoryStorage();
		const folded = new FoldedSections(storage, "hub-a");
		folded.setFolded("idle", false);
		new OrganizeByPreference(storage, "hub-a").set("host-project");
		folded.setFolded("projects", true);
		expect(JSON.parse(storage.values.get("evener.native.board-sections.hub-a") as string)).toEqual({
			idle: false,
			projects: true,
		});
		expect(new OrganizeByPreference(storage, "hub-a").get()).toBe("host-project");
	});

	it("keeps working in memory when storage throws", () => {
		const broken: BoardStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {},
		};
		const choice = new OrganizeByPreference(broken, "hub-a");
		expect(choice.get()).toBe("project-host");
		choice.set("host-project");
		expect(choice.get()).toBe("host-project");
	});
});
```

In the `forgetBoard` describe block, replace the test "removes both of a hub's keys and nothing else, without throwing" with:

```ts
	it("removes every key of a hub's own and nothing else, without throwing", () => {
		const storage = memoryStorage(
			new Map([
				["evener.native.seen.hub-a", "{}"],
				["evener.native.board-sections.hub-a", "{}"],
				["evener.native.board-organize.hub-a", '"host-project"'],
				["evener.native.seen.hub-b", "{}"],
				["evener.native.board-organize.hub-b", '"host-project"'],
			]),
		);
		expect(() => forgetBoard(storage, "hub-a")).not.toThrow();
		expect([...storage.values.keys()].sort()).toEqual([
			"evener.native.board-organize.hub-b",
			"evener.native.seen.hub-b",
		]);
	});
```

and, in "still removes the folded key when the seen key's removal fails, then throws", rename it "still removes the other keys when the seen key's removal fails, then throws" and change its last line to:

```ts
		expect(removed).toEqual(["evener.native.board-sections.hub-a", "evener.native.board-organize.hub-a"]);
```

If part 2's Task 15 has already landed, these tests also list its `evener.native.recent-searches.hub-a` key among the removed ones (and a hub-b one among the kept): keep both tasks' keys.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/boardMemory.test.ts`
Expected: FAIL: `OrganizeByPreference` is not exported, the forget test keeps `evener.native.board-organize.hub-a`, and the failure test never removes it.

- [ ] **Step 3: Implement**

In `mobile-native/src/board/boardMemory.ts`, beside `foldedKey`:

```ts
const organizeKey = (hubId: string) => `evener.native.board-organize.${hubId}`;
```

After the `FoldedSections` class:

```ts
/** How the Board's Projects section nests sessions once the hub has more
 * than one host (spec 7.1's "Organize by"): by project, then host (the
 * default, as on the web), or by host, then project. */
export type OrganizeBy = "project-host" | "host-project";

/** The Organize by choice, per device and hub, stored as a JSON string. It
 * has its own key: FoldedSections stores a flat map of fold flags, and a mode
 * is not a fold. */
export class OrganizeByPreference {
	private value: OrganizeBy;

	constructor(
		private readonly storage: BoardStorage,
		private readonly hubId: string,
	) {
		// Unreadable storage, or a value this build doesn't know, reads as the default.
		this.value = readJson(storage, organizeKey(hubId)) === "host-project" ? "host-project" : "project-host";
	}

	get(): OrganizeBy {
		return this.value;
	}

	set(value: OrganizeBy): void {
		this.value = value;
		writeJson(this.storage, organizeKey(this.hubId), value);
	}
}
```

In `forgetBoard`, change the key list to `[seenKey(hubId), foldedKey(hubId), organizeKey(hubId)]` (plus part 2's recent-searches key if Task 15 has landed). Its try-each-key-then-throw loop stays as it is.

In `mobile-native/src/board/nativeBoardMemory.ts`, import `OrganizeByPreference` beside the other classes and add:

```ts
const organize = new Map<string, OrganizeByPreference>();

export function organizeByPreference(hubId: string): OrganizeByPreference {
	let choice = organize.get(hubId);
	if (!choice) {
		choice = new OrganizeByPreference(Storage, hubId);
		organize.set(hubId, choice);
	}
	return choice;
}
```

and `organize.delete(hubId);` in `forgetBoardForHub`, beside `seen.delete(hubId)` and `folded.delete(hubId)`.

- [ ] **Step 4: Run them and watch them pass, then type-check**

Run: `cd mobile-native && npx vitest run src/board/boardMemory.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/board/boardMemory.ts mobile-native/src/board/nativeBoardMemory.ts mobile-native/src/board/boardMemory.test.ts
git commit -m "feat(native): the Board remembers how you organize projects, under its own key"
```

### Task 10.4: The Projects, Test runs and Archived sections as items

**Files:**
- Create: `mobile-native/src/board/projectTree.ts`
- Test: `mobile-native/src/board/projectTree.test.ts`

**Interfaces:**
- Consumes: the shared host grouping (Task 10.1); `ProjectBrowserSnapshot` and `ProjectSessionTier` (Task 10.2); `OrganizeBy` (Task 10.3); `boardState` (part 1 Task 2, in the tests).
- Produces (all exported from `projectTree.ts`):
  - `type Grouping = "flat" | OrganizeBy` and `type ProjectSection = "projects" | "test-runs" | "archived"`
  - `interface TierPage { rows: readonly NavigationSessionSummary[]; remaining: number; loaded: boolean; error: string | null }` and `type ProjectPages = Record<ProjectSessionTier, TierPage>`
  - `type ProjectTreeItem` (below): `host`, `project`, `branch`, `tier`, `archivedGroup`, `session`, `more`, `loading` and `failed` items, each with a `key` unique in the Board's list, and a `fold` on every item that folds
  - `interface ProjectTreeInput` (below) and `projectTreeItems(input: ProjectTreeInput): ProjectTreeItem[]`
  - `grouping(sources, organizeBy): Grouping`, `SECTION_FOLDS`, `projectFold(section, projectKey, hostId?)`, `hostFold(hostId)`, `projectLiveCount(project)`
  - `interface RevealTarget { unfold: string[]; scrollTo: string }` and `projectRevealTarget(input): RevealTarget`
  - `expandedProjectKeys(items: readonly ProjectTreeItem[]): Set<string>`
  - `morePagesToLoad(visible): { projectKey: string; tier: ProjectSessionTier }[]`
  - `interface ProjectsView { projects: readonly NavigationProjectSummary[]; loaded: boolean; pages: ReadonlyMap<string, ProjectPages> }` and `projectsView(fresh: ProjectBrowserSnapshot, retained: ProjectsView | null): ProjectsView`
  - `liveCountsByHost(liveRows: readonly NavigationSessionSummary[], complete: boolean): (hostId: string) => number | null`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/board/projectTree.test.ts
import { describe, expect, it } from "vitest";
import type {
	NavigationProjectSummary,
	NavigationSessionSummary,
	Source,
} from "@evener/appwire-client";
import type { ProjectBrowserSnapshot } from "../projectBrowser";
import { boardState } from "./attention";
import {
	expandedProjectKeys,
	liveCountsByHost,
	morePagesToLoad,
	type ProjectPages,
	type ProjectTreeInput,
	type ProjectTreeItem,
	projectRevealTarget,
	projectTreeItems,
	type ProjectsView,
	projectsView,
} from "./projectTree";

const source = (id: string, label = id, online = true): Source => ({
	id,
	label,
	kind: id === "local" ? "local" : "appwire",
	online,
});
const LOCAL = source("local", "this host");
const PARK = source("paradise-park");
const project = (key: string, over: Partial<NavigationProjectSummary> = {}): NavigationProjectSummary => ({
	key,
	name: key,
	session_count: 3,
	...over,
});
const session = (
	ref: string,
	host_id = "local",
	over: Partial<NavigationSessionSummary> = {},
): NavigationSessionSummary => ({
	ref,
	host_id,
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const page = (rows: NavigationSessionSummary[] = [], remaining = 0) => ({
	rows,
	remaining,
	loaded: true,
	error: null,
});
const pages = (
	current: NavigationSessionSummary[] = [],
	recent: NavigationSessionSummary[] = [],
	archived: NavigationSessionSummary[] = [],
	remaining: Partial<Record<"current" | "recent" | "archived", number>> = {},
): ProjectPages => ({
	current: page(current, remaining.current),
	recent: page(recent, remaining.recent),
	archived: page(archived, remaining.archived),
});
const unfolded = () => false;
const defaults = (_fold: string, byDefault: boolean) => byDefault;
const evener = project("evener", { sources: ["local", "paradise-park"], rollup_live: 2, rollup_attn: 1 });

function items(over: Partial<ProjectTreeInput>): ProjectTreeItem[] {
	return projectTreeItems({
		section: "projects",
		projects: [],
		pages: new Map(),
		sources: [LOCAL, PARK],
		organizeBy: "host-project",
		isFolded: unfolded,
		hostLiveCount: () => null,
		...over,
	});
}
/** The list as a person reads it: indentation is depth. */
function outline(list: readonly ProjectTreeItem[]): string[] {
	const indent = (depth: number) => "  ".repeat(depth);
	return list.map((item) => {
		switch (item.kind) {
			case "host":
				return `host ${item.host.label}${item.host.online ? "" : " (offline)"}${item.liveCount ? ` · ${item.liveCount} live` : ""}`;
			case "project":
				return `${indent(item.depth)}project ${item.project.key}${item.liveCount ? ` · ${item.liveCount} live` : ""}`;
			case "branch":
				return `  branch ${item.host.label}`;
			case "tier":
				return `${indent(item.depth)}${item.label}`;
			case "archivedGroup":
				return `${indent(item.depth)}Archived${item.count === null ? "" : ` · ${item.count}`}`;
			case "session":
				return `${indent(item.depth)}${item.row.ref}`;
			case "more":
				return `${indent(item.depth)}${item.remaining} more ${item.tier}`;
			case "loading":
			case "failed":
				return `${indent(item.depth)}${item.kind}`;
		}
	});
}

describe("organized by host (spec 7.1, ruling 11)", () => {
	it("a project on two hosts shows each session once, under the host its row names", () => {
		const list = items({
			projects: [evener],
			pages: new Map([
				["evener", pages([session("local:a"), session("paradise-park:b", "paradise-park")], [session("local:c")])],
			]),
		});
		expect(outline(list)).toEqual([
			"host this host",
			"  project evener · 3 live",
			"    Today",
			"    local:a",
			"    Recent",
			"    local:c",
			"host paradise-park",
			"  project evener",
			"    Today",
			"    paradise-park:b",
		]);
		const refs = list.flatMap((item) => (item.kind === "session" ? [item.row.ref] : []));
		expect(refs.sort()).toEqual(["local:a", "local:c", "paradise-park:b"]);
	});

	it("an offline host shows only its own rows, and they read as shut down", () => {
		const a = session("local:a", "local", { state: "active" });
		const b = session("paradise-park:b", "paradise-park", { state: "active", offline: true });
		const list = items({
			sources: [LOCAL, source("paradise-park", "paradise-park", false)],
			projects: [evener],
			pages: new Map([["evener", pages([a, b])]]),
			hostLiveCount: () => 4,
		});
		expect(outline(list)).toEqual([
			"host this host · 4 live",
			"  project evener · 3 live",
			"    Today",
			"    local:a",
			"host paradise-park (offline)",
			"  project evener",
			"    Today",
			"    paradise-park:b",
		]);
		expect(boardState(a, false, false)).toBe("working");
		expect(boardState(b, false, false)).toBe("shutDown");
	});

	it("the project's counts and its more rows read once, on the first host with loaded rows", () => {
		const list = items({
			projects: [evener],
			pages: new Map([["evener", pages([session("paradise-park:b", "paradise-park")], [], [], { current: 12 })]]),
		});
		expect(outline(list)).toEqual([
			"host this host",
			"  project evener",
			"host paradise-park",
			"  project evener · 3 live",
			"    Today",
			"    paradise-park:b",
			"    12 more current",
		]);
	});

	it("reads an omitted or empty sources list as this hub's own project", () => {
		for (const sources of [undefined, []]) {
			const docs = project("docs", { sources });
			expect(outline(items({ projects: [docs], pages: new Map([["docs", pages([session("local:d")])]]) }))).toEqual([
				"host this host",
				"  project docs",
				"    Today",
				"    local:d",
			]);
		}
	});

	it("places a cluster under its newest member's host", () => {
		const cluster = session("cluster:x", "cluster", {
			kind: "cluster",
			children: [session("paradise-park:m", "paradise-park")],
		});
		// The project names no sources, so the one row it has decides its host.
		const list = items({ projects: [project("docs")], pages: new Map([["docs", pages([cluster])]]) });
		expect(outline(list)).toEqual(["host paradise-park", "  project docs", "    Today", "    cluster:x"]);
	});

	it("keeps a host's archived rows in that host's copy", () => {
		const list = items({
			projects: [evener],
			pages: new Map([["evener", pages([], [], [session("local:z"), session("paradise-park:y", "paradise-park")])]]),
		});
		expect(outline(list)).toEqual([
			"host this host",
			"  project evener · 3 live",
			"    Archived · 1",
			"      local:z",
			"host paradise-park",
			"  project evener",
			"    Archived · 1",
			"      paradise-park:y",
		]);
	});
});

describe("organized by project (spec 7.1)", () => {
	const spanning = new Map([
		["evener", pages([session("local:a"), session("paradise-park:b", "paradise-park")], [], [session("local:z")])],
	]);

	it("splits a project's rows into host branches only when they span hosts", () => {
		expect(outline(items({ organizeBy: "project-host", projects: [evener], pages: spanning }))).toEqual([
			"project evener · 3 live",
			"  branch this host",
			"    Today",
			"    local:a",
			"  branch paradise-park",
			"    Today",
			"    paradise-park:b",
			"  Archived · 1",
			"    local:z",
		]);
		expect(
			outline(items({ organizeBy: "project-host", projects: [project("docs")], pages: new Map([["docs", pages([session("local:d")])]]) })),
		).toEqual(["project docs", "  Today", "  local:d"]);
	});

	it("folds branches and the archived group by default, as the web does", () => {
		const shown = project("evener", { ...evener, default_expanded: true });
		expect(outline(items({ organizeBy: "project-host", projects: [shown], pages: spanning, isFolded: defaults }))).toEqual([
			"project evener · 3 live",
			"  branch this host",
			"  branch paradise-park",
			"  Archived · 1",
		]);
	});

	it("ignores Organize by while the hub has one host", () => {
		expect(
			outline(items({ sources: [LOCAL], projects: [project("docs")], pages: new Map([["docs", pages([session("local:d")])]]) })),
		).toEqual(["project docs", "  Today", "  local:d"]);
	});

	it("floats pinned projects to the top in both modes", () => {
		const list = [project("a"), project("b", { favorite: true })];
		const folded = () => true;
		expect(outline(items({ organizeBy: "project-host", projects: list, isFolded: folded }))).toEqual(["project b", "project a"]);
		expect(outline(items({ projects: list, isFolded: (fold) => fold.startsWith("project:") }))).toEqual([
			"host this host",
			"  project b",
			"  project a",
		]);
	});

	it("loads the next page from the project's own more rows", () => {
		expect(
			outline(
				items({
					organizeBy: "project-host",
					projects: [project("docs")],
					pages: new Map([["docs", pages([session("local:d")], [], [session("local:o")], { current: 20, recent: 3, archived: 40 })]]),
				}),
			),
		).toEqual(["project docs", "  Today", "  local:d", "  20 more current", "  3 more recent", "  Archived", "    local:o", "    40 more archived"]);
	});
});

describe("test runs and archived", () => {
	it("stay flat, and an archived project opens its archived group by default", () => {
		const old = project("old", { default_expanded: true });
		expect(
			outline(items({ section: "archived", projects: [old], pages: new Map([["old", pages([], [], [session("local:o")])]]), isFolded: defaults })),
		).toEqual(["project old", "  Archived · 1", "    local:o"]);
		expect(
			outline(items({ section: "test-runs", projects: [project("hub-test-env")], pages: new Map([["hub-test-env", pages([session("local:t")])]]) })),
		).toEqual(["project hub-test-env", "  Today", "  local:t"]);
	});

	it("show one placeholder until a project's pages land, and say so when a first read fails", () => {
		expect(outline(items({ organizeBy: "project-host", projects: [project("p")] }))).toEqual(["project p", "  loading"]);
		const failed = { ...pages(), current: { rows: [], remaining: 0, loaded: false, error: "offline" } };
		expect(outline(items({ organizeBy: "project-host", projects: [project("p")], pages: new Map([["p", failed]]) }))).toEqual([
			"project p",
			"  failed",
		]);
	});
});

describe("keys", () => {
	it("names every fold per section and host, and keeps every item key unique", () => {
		const list = items({ projects: [evener], pages: new Map([["evener", pages([session("local:a"), session("paradise-park:b", "paradise-park")], [], [session("local:z")])]]) });
		expect(list.flatMap((item) => ("fold" in item ? [item.fold] : []))).toEqual([
			"host:local",
			"project:evener@local",
			"project:evener@local:archived",
			"host:paradise-park",
			"project:evener@paradise-park",
		]);
		expect(new Set(list.map((item) => item.key)).size).toBe(list.length);
		const branches = items({ organizeBy: "project-host", projects: [evener], pages: new Map([["evener", pages([session("local:a"), session("paradise-park:b", "paradise-park")])]]) });
		expect(branches.flatMap((item) => ("fold" in item ? [item.fold] : []))).toEqual([
			"project:evener",
			"project:evener@host:local",
			"project:evener@host:paradise-park",
		]);
		expect(items({ section: "test-runs", projects: [project("t")], isFolded: () => true })[0]).toMatchObject({ fold: "test-run:t" });
		expect(items({ section: "archived", projects: [project("o")], isFolded: () => true })[0]).toMatchObject({ fold: "archived-project:o" });
	});
});

describe("revealing a project for search (spec 7.4)", () => {
	const unfoldOnly = (target: { unfold: string[] }) => (fold: string, byDefault: boolean) =>
		target.unfold.includes(fold) ? false : byDefault;

	it("opens the project row when projects come first", () => {
		const target = projectRevealTarget({ project: evener, pages: undefined, sources: [LOCAL, PARK], organizeBy: "project-host" });
		expect(target).toEqual({ unfold: ["projects", "project:evener"], scrollTo: "projects/project:evener" });
		expect(items({ organizeBy: "project-host", projects: [evener], isFolded: unfoldOnly(target) }).some((item) => item.key === target.scrollTo)).toBe(true);
	});

	it("opens the canonical copy of a project on several hosts when hosts come first", () => {
		const loaded = pages([session("paradise-park:b", "paradise-park")]);
		const target = projectRevealTarget({ project: evener, pages: loaded, sources: [LOCAL, PARK], organizeBy: "host-project" });
		expect(target).toEqual({
			unfold: ["projects", "host:paradise-park", "project:evener@paradise-park"],
			scrollTo: "projects/project:evener@paradise-park",
		});
		expect(
			items({ projects: [evener], pages: new Map([["evener", loaded]]), isFolded: unfoldOnly(target) }).some((item) => item.key === target.scrollTo),
		).toBe(true);
		expect(projectRevealTarget({ project: evener, pages: undefined, sources: [LOCAL, PARK], organizeBy: "host-project" }).scrollTo).toBe(
			"projects/project:evener@local",
		);
	});

	it("opens the project row on a hub with one host, whatever Organize by says", () => {
		expect(projectRevealTarget({ project: evener, pages: undefined, sources: [LOCAL], organizeBy: "host-project" }).scrollTo).toBe(
			"projects/project:evener",
		);
	});
});

describe("what the Board reads", () => {
	it("reads the pages of every project shown unfolded, under whichever host", () => {
		const list = items({ projects: [evener], isFolded: (fold) => fold === "project:evener@local" });
		expect([...expandedProjectKeys(list)]).toEqual(["evener"]);
		expect([...expandedProjectKeys(items({ projects: [evener], isFolded: () => true }))]).toEqual([]);
	});

	it("loads each visible more row's page once", () => {
		expect(
			morePagesToLoad([
				{ kind: "more", projectKey: "a", tier: "current" },
				{ kind: "more", projectKey: "a", tier: "current" },
				{ kind: "session" },
				{ kind: "more", projectKey: "a", tier: "archived" },
			]),
		).toEqual([
			{ projectKey: "a", tier: "current" },
			{ projectKey: "a", tier: "archived" },
		]);
	});

	it("counts a host's live rows once each, only when every Live page is loaded", () => {
		const rows = [session("local:a"), session("paradise-park:b", "paradise-park"), session("paradise-park:c", "paradise-park"), session("paradise-park:c", "paradise-park")];
		const counts = liveCountsByHost(rows, true);
		expect([counts("local"), counts("paradise-park"), counts("devbox")]).toEqual([1, 2, null]);
		expect(liveCountsByHost(rows, false)("local")).toBeNull();
	});
});

describe("keeping rows through a reconnect (part 1 Review Focus 1)", () => {
	const pageState = (rows: NavigationSessionSummary[], loaded = true) => ({
		loaded,
		truncated: false,
		rows,
		remaining: 0,
		loading: !loaded,
		error: null,
		stale: false,
	});
	function snapshot(catalogLoaded: boolean, groups: ProjectBrowserSnapshot["groups"] = []): ProjectBrowserSnapshot {
		return {
			projects: { ...pageState([], catalogLoaded), rows: catalogLoaded ? [project("a")] : [] },
			groups,
			loading: !catalogLoaded,
			error: null,
		};
	}
	const retained: ProjectsView = {
		projects: [project("a")],
		loaded: true,
		pages: new Map([["a", pages([session("local:old")], [session("local:older")])]]),
	};

	it("shows the earlier connection's projects and rows until the new reads land", () => {
		expect(projectsView(snapshot(false), retained)).toEqual(retained);
		const view = projectsView(
			snapshot(true, [
				{
					project: project("a"),
					expanded: true,
					current: pageState([session("local:new")]),
					recent: pageState([], false),
					archived: pageState([], false),
					sessions: [],
				},
			]),
			retained,
		);
		expect(view.projects.map((row) => row.key)).toEqual(["a"]);
		expect(view.pages.get("a")?.current.rows.map((row) => row.ref)).toEqual(["local:new"]);
		expect(view.pages.get("a")?.recent.rows.map((row) => row.ref)).toEqual(["local:older"]);
	});

	it("shows a first read as it is when nothing was shown before", () => {
		expect(projectsView(snapshot(false), null)).toEqual({ projects: [], loaded: false, pages: new Map() });
	});
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/projectTree.test.ts`
Expected: FAIL: `Cannot find module './projectTree'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/board/projectTree.ts
// The Board's Projects, Test runs and Archived sections as one flat list of
// items (spec 7.1): project rows, host groups, a project's Today and Recent
// captions and its folded Archived group, and the rows that load more. Pure:
// BoardScreen owns the reads and renders the items.
//
// Organizing by host mirrors the web rail (railNodes.ts hostProjectNodes and
// projectNodesWithHostBranches) through the shared package's host grouping.
// The hub has no host-scoped project read, so a project's pages are read once
// and every row sits under the host its own host_id names: a session shows
// once, however many hosts own its project (ruling 11).
import type {
	NavigationProjectSummary,
	NavigationSessionSummary,
	Source,
} from "@evener/appwire-client";
import {
	CONTROLLER_SOURCE_ID,
	canonicalHostId,
	type HostFacts,
	orderedHosts,
	projectHostIds,
	sessionGroupHostId,
} from "@evener/appwire-client/state/navigation";
import type {
	ProjectBrowserSnapshot,
	ProjectSessionTier,
} from "../projectBrowser";
import type { OrganizeBy } from "./boardMemory";

export type Grouping = "flat" | OrganizeBy;
export type ProjectSection = "projects" | "test-runs" | "archived";

/** One tier's page as the project data holds it. */
export interface TierPage {
	rows: readonly NavigationSessionSummary[];
	remaining: number;
	loaded: boolean;
	error: string | null;
}
export type ProjectPages = Record<ProjectSessionTier, TierPage>;

export type ProjectTreeItem =
	| { kind: "host"; key: string; fold: string; host: HostFacts; liveCount: number | null; folded: boolean }
	| {
			kind: "project";
			key: string;
			fold: string;
			depth: 0 | 1;
			project: NavigationProjectSummary;
			liveCount: number | null;
			folded: boolean;
	  }
	| { kind: "branch"; key: string; fold: string; host: HostFacts; folded: boolean }
	| { kind: "tier"; key: string; depth: number; label: "Today" | "Recent" }
	| { kind: "archivedGroup"; key: string; fold: string; depth: number; count: number | null; folded: boolean }
	| { kind: "session"; key: string; depth: number; row: NavigationSessionSummary; archived: boolean }
	| { kind: "more"; key: string; depth: number; projectKey: string; tier: ProjectSessionTier; remaining: number }
	| { kind: "loading"; key: string; depth: number }
	| { kind: "failed"; key: string; depth: number };

export interface ProjectTreeInput {
	section: ProjectSection;
	projects: readonly NavigationProjectSummary[];
	/** Each project's loaded pages; a project never expanded has none. */
	pages: ReadonlyMap<string, ProjectPages>;
	sources: readonly Source[];
	/** Applies to the Projects section alone: Test runs and Archived stay flat,
	 * as on the web (Rail.tsx projectsTierFor). */
	organizeBy: OrganizeBy;
	isFolded(fold: string, byDefault: boolean): boolean;
	/** A connected host group's live count, or null when it can't be known. */
	hostLiveCount(hostId: string): number | null;
}

/** The sections' own folds (part 1's FoldedSections keys): Projects starts
 * unfolded, Test runs and Archived folded (spec 7.1). */
export const SECTION_FOLDS: Record<ProjectSection, { fold: string; foldedByDefault: boolean }> = {
	projects: { fold: "projects", foldedByDefault: false },
	"test-runs": { fold: "test-runs", foldedByDefault: true },
	archived: { fold: "archived", foldedByDefault: true },
};

/** "flat" until the manifest names a host besides this hub, the web's rule
 * (Rail.tsx: displaySources.some(id !== "local")); then the person's choice. */
export function grouping(sources: readonly Source[], organizeBy: OrganizeBy): Grouping {
	return sources.some((source) => source.id !== CONTROLLER_SOURCE_ID) ? organizeBy : "flat";
}

/** The fold that holds a project's rows: the project row, or its copy under one host. */
export function projectFold(section: ProjectSection, projectKey: string, hostId?: string): string {
	const base =
		section === "projects"
			? `project:${projectKey}`
			: section === "test-runs"
				? `test-run:${projectKey}`
				: `archived-project:${projectKey}`;
	return hostId === undefined ? base : `${base}@${hostId}`;
}
export const hostFold = (hostId: string) => `host:${hostId}`;
const branchFold = (projectKey: string, hostId: string) => `project:${projectKey}@host:${hostId}`;
const itemKey = (section: ProjectSection, fold: string) => `${section}/${fold}`;

/** A project's live count: the hub's working and waiting-on-you sessions.
 * The hub counts idle live sessions in neither rollup (ruling 12). */
export function projectLiveCount(project: NavigationProjectSummary): number | null {
	const count = (project.rollup_live ?? 0) + (project.rollup_attn ?? 0);
	return count > 0 ? count : null;
}

const allRows = (pages: ProjectPages | undefined) =>
	pages ? [...pages.current.rows, ...pages.recent.rows, ...pages.archived.rows] : [];
const activeRows = (pages: ProjectPages | undefined) =>
	pages ? [...pages.current.rows, ...pages.recent.rows] : [];
const pinnedFirst = (projects: readonly NavigationProjectSummary[]) =>
	[...projects].sort((a, b) => Number(!!b.favorite) - Number(!!a.favorite));
const onHost = (hostId: string | null) => (row: NavigationSessionSummary) =>
	hostId === null || sessionGroupHostId(row) === hostId;
const tiersLoaded = (pages: ProjectPages | undefined): pages is ProjectPages =>
	!!pages && pages.current.loaded && pages.recent.loaded && pages.archived.loaded;

export function projectTreeItems(input: ProjectTreeInput): ProjectTreeItem[] {
	const out: ProjectTreeItem[] = [];
	const mode = input.section === "projects" ? grouping(input.sources, input.organizeBy) : "flat";
	if (mode === "host-project") hostFirst(out, input);
	else for (const project of pinnedFirst(input.projects)) projectFirst(out, input, project, mode === "project-host");
	return out;
}

/** One placeholder row until all three tiers have been read once. */
function placeholder(out: ProjectTreeItem[], prefix: string, pages: ProjectPages | undefined, depth: number): void {
	const failed =
		!!pages && [pages.current, pages.recent, pages.archived].some((page) => !page.loaded && page.error !== null);
	out.push(failed ? { kind: "failed", key: `${prefix}/failed`, depth } : { kind: "loading", key: `${prefix}/loading`, depth });
}

/** Today's and Recent's captions and rows, of one host or of every host. */
function activeItems(out: ProjectTreeItem[], prefix: string, pages: ProjectPages, hostId: string | null, depth: number): void {
	for (const [tier, label] of [
		["current", "Today"],
		["recent", "Recent"],
	] as const) {
		const rows = pages[tier].rows.filter(onHost(hostId));
		if (rows.length === 0) continue;
		out.push({ kind: "tier", key: `${prefix}/tier:${tier}`, depth, label });
		for (const row of rows) out.push({ kind: "session", key: `${prefix}/${tier}/${row.ref}`, depth, row, archived: false });
	}
}

/** Today's and Recent's rows that load more; they belong to the project, so
 * they appear once (ruling 12). */
function moreItems(out: ProjectTreeItem[], prefix: string, pages: ProjectPages, projectKey: string, depth: number): void {
	for (const tier of ["current", "recent"] as const)
		if (pages[tier].remaining > 0)
			out.push({ kind: "more", key: `${prefix}/more:${tier}`, depth, projectKey, tier, remaining: pages[tier].remaining });
}

/** The folded Archived group: its count only once every archived row is loaded. */
function archivedItems(
	out: ProjectTreeItem[],
	input: ProjectTreeInput,
	prefix: string,
	containerFold: string,
	pages: ProjectPages,
	hostId: string | null,
	depth: number,
	projectKey: string,
	carriesMore: boolean,
): void {
	const rows = pages.archived.rows.filter(onHost(hostId));
	const remaining = carriesMore ? pages.archived.remaining : 0;
	if (rows.length === 0 && remaining === 0) return;
	const fold = `${containerFold}:archived`;
	const folded = input.isFolded(fold, input.section !== "archived");
	const key = `${prefix}/archived`;
	out.push({ kind: "archivedGroup", key, fold, depth, count: pages.archived.remaining === 0 ? rows.length : null, folded });
	if (folded) return;
	for (const row of rows) out.push({ kind: "session", key: `${key}/${row.ref}`, depth: depth + 1, row, archived: true });
	if (remaining > 0) out.push({ kind: "more", key: `${key}/more`, depth: depth + 1, projectKey, tier: "archived", remaining });
}

/** "Project, then host", or a flat section: project rows, with per-host
 * branches inside a project whose Today and Recent rows span hosts. */
function projectFirst(
	out: ProjectTreeItem[],
	input: ProjectTreeInput,
	project: NavigationProjectSummary,
	branchByHost: boolean,
): void {
	const fold = projectFold(input.section, project.key);
	const prefix = itemKey(input.section, fold);
	const folded = input.isFolded(fold, !(project.default_expanded ?? false));
	out.push({ kind: "project", key: prefix, fold, depth: 0, project, liveCount: projectLiveCount(project), folded });
	if (folded) return;
	const pages = input.pages.get(project.key);
	if (!tiersLoaded(pages)) {
		placeholder(out, prefix, pages, 1);
		return;
	}
	const hosts = branchByHost ? orderedHosts(activeRows(pages).map(sessionGroupHostId), input.sources) : [];
	if (hosts.length > 1)
		for (const host of hosts) {
			const branch = branchFold(project.key, host.id);
			const branchFolded = input.isFolded(branch, true);
			out.push({ kind: "branch", key: itemKey(input.section, branch), fold: branch, host, folded: branchFolded });
			if (!branchFolded) activeItems(out, itemKey(input.section, branch), pages, host.id, 2);
		}
	else activeItems(out, prefix, pages, null, 1);
	moreItems(out, prefix, pages, project.key, 1);
	archivedItems(out, input, prefix, fold, pages, null, 1, project.key, true);
}

/** "Host, then project": a group per host that owns a project, each holding a
 * copy of every project it owns, and each copy holding only that host's rows. */
function hostFirst(out: ProjectTreeItem[], input: ProjectTreeInput): void {
	const projects = pinnedFirst(input.projects);
	const owners = new Map<string, string[]>();
	for (const project of projects)
		owners.set(project.key, projectHostIds(project.sources, allRows(input.pages.get(project.key))));
	for (const host of orderedHosts([...owners.values()].flat(), input.sources)) {
		const fold = hostFold(host.id);
		const folded = input.isFolded(fold, false);
		out.push({
			kind: "host",
			key: itemKey(input.section, fold),
			fold,
			host,
			liveCount: host.online ? input.hostLiveCount(host.id) : null,
			folded,
		});
		if (folded) continue;
		for (const project of projects) {
			const hostIds = owners.get(project.key) ?? [];
			if (!hostIds.includes(host.id)) continue;
			const pages = input.pages.get(project.key);
			const canonical = canonicalHostId(hostIds, activeRows(pages), input.sources) === host.id;
			const copyFold = projectFold(input.section, project.key, host.id);
			const prefix = itemKey(input.section, copyFold);
			const copyFolded = input.isFolded(copyFold, !(project.default_expanded ?? false));
			out.push({
				kind: "project",
				key: prefix,
				fold: copyFold,
				depth: 1,
				project,
				liveCount: canonical ? projectLiveCount(project) : null,
				folded: copyFolded,
			});
			if (copyFolded) continue;
			if (!tiersLoaded(pages)) {
				placeholder(out, prefix, pages, 2);
				continue;
			}
			activeItems(out, prefix, pages, host.id, 2);
			if (canonical) moreItems(out, prefix, pages, project.key, 2);
			archivedItems(out, input, prefix, copyFold, pages, host.id, 2, project.key, canonical);
		}
	}
}

export interface RevealTarget {
	/** Folds to open, outermost first. */
	unfold: string[];
	/** The item key to scroll to once they are open. */
	scrollTo: string;
}

/** Where search's project hit goes (spec 7.4): the project row, or, when
 * hosts come first, its canonical copy, the one carrying its counts. */
export function projectRevealTarget(input: {
	project: NavigationProjectSummary;
	pages: ProjectPages | undefined;
	sources: readonly Source[];
	organizeBy: OrganizeBy;
}): RevealTarget {
	const { project, pages, sources } = input;
	const section = SECTION_FOLDS.projects.fold;
	if (grouping(sources, input.organizeBy) !== "host-project") {
		const fold = projectFold("projects", project.key);
		return { unfold: [section, fold], scrollTo: itemKey("projects", fold) };
	}
	const host = canonicalHostId(projectHostIds(project.sources, allRows(pages)), activeRows(pages), sources);
	const fold = projectFold("projects", project.key, host);
	return { unfold: [section, hostFold(host), fold], scrollTo: itemKey("projects", fold) };
}

/** The projects whose rows are on screen, so the Board reads their pages:
 * every project row or copy that is unfolded (items exist only under
 * unfolded parents), the web's projectLoadExpansionKeys. */
export function expandedProjectKeys(items: readonly ProjectTreeItem[]): Set<string> {
	const keys = new Set<string>();
	for (const item of items) if (item.kind === "project" && !item.folded) keys.add(item.project.key);
	return keys;
}

/** The pages the visible "more" rows ask for, once each. */
export function morePagesToLoad(
	visible: readonly { kind: string; projectKey?: string; tier?: ProjectSessionTier }[],
): { projectKey: string; tier: ProjectSessionTier }[] {
	const wanted = new Map<string, { projectKey: string; tier: ProjectSessionTier }>();
	for (const item of visible)
		if (item.kind === "more" && item.projectKey !== undefined && item.tier !== undefined)
			wanted.set(`${item.projectKey}:${item.tier}`, { projectKey: item.projectKey, tier: item.tier });
	return [...wanted.values()];
}

export interface ProjectsView {
	projects: readonly NavigationProjectSummary[];
	/** A catalog read has landed for this section, on this connection or an earlier one. */
	loaded: boolean;
	pages: ReadonlyMap<string, ProjectPages>;
}

/** What a section shows: the current connection's reads where they have
 * landed, and the rows shown before until they do, so a reconnect never
 * blanks a section (part 1's Review Focus 1). `retained` is the view shown
 * last; pass null for a new hub. */
export function projectsView(fresh: ProjectBrowserSnapshot, retained: ProjectsView | null): ProjectsView {
	const before = retained ?? { projects: [], loaded: false, pages: new Map<string, ProjectPages>() };
	const pages = new Map(before.pages);
	for (const group of fresh.groups) {
		const previous = pages.get(group.project.key);
		const pick = (tier: ProjectSessionTier): TierPage =>
			group[tier].loaded || !previous ? group[tier] : previous[tier];
		pages.set(group.project.key, { current: pick("current"), recent: pick("recent"), archived: pick("archived") });
	}
	return fresh.projects.loaded
		? { projects: fresh.projects.rows, loaded: true, pages }
		: { projects: before.projects, loaded: before.loaded, pages };
}

/** Each host's rows in Live, once each (a session that needs you is in both
 * Live and the needs_you section), when every page of both is loaded;
 * otherwise no host has a count (ruling 12). */
export function liveCountsByHost(
	liveRows: readonly NavigationSessionSummary[],
	complete: boolean,
): (hostId: string) => number | null {
	if (!complete) return () => null;
	const counts = new Map<string, number>();
	const seen = new Set<string>();
	for (const row of liveRows) {
		if (seen.has(row.ref)) continue;
		seen.add(row.ref);
		const host = sessionGroupHostId(row);
		counts.set(host, (counts.get(host) ?? 0) + 1);
	}
	return (hostId) => counts.get(hostId) ?? null;
}
```

- [ ] **Step 4: Run them and watch them pass, then type-check**

Run: `cd mobile-native && npx vitest run src/board/projectTree.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/board/projectTree.ts mobile-native/src/board/projectTree.test.ts
git commit -m "feat(native): the Board's project sections as items, each session under its own host"
```

Open PR 3a: "feat(native): the Board's project data, and host grouping in the shared package (phase 2, PR 3a)". The description says Tasks 10.1-10.4 have no Board screen yet (PR 3b draws them), that the web rail's host grouping moved into the shared package unchanged, and that `projectBrowser.ts` had no caller since part 1's Task 8.

---

## PR 3b: the project sections on the Board

### Task 10.5: The Board's one organization journal

**Files:**
- Create: `mobile-native/src/board/organizationCheck.ts` and `mobile-native/src/board/useBoardOrganization.ts`
- Create: `mobile-native/src/board/organizationTestUtils.ts` (a hub answering the reads a check makes; tests only. The `*TestUtils.*` name is one of the places `check-package-tests` lets import `@evener/appwire-client/testing/`)
- Test: `mobile-native/src/board/organizationCheck.test.ts` and `mobile-native/src/board/useBoardOrganization.test.tsx`

**Interfaces:**
- Consumes: `NavigationActions` (`navigationActions.ts`), `organizationJournal` (`nativeOrganization.ts`), `readOrganizationNavigation` (`organizationNavigation.ts`), `refreshPinNavigation` (`pinNavigation.ts`), `NavigationPages`.
- Produces:
  - `checkOrganizationChange(client: ConversationClientLike, pinPages: NavigationPages<NavigationPinSectionDescriptor>, checkpoint: NavigationActionCheckpoint, current: () => boolean, confirmReceipt: boolean): Promise<boolean>`
  - `organizationFree(state: { pending: boolean; uncertain: boolean; storageUnavailable: boolean } | null): boolean`
  - `interface BoardOrganization { actions: NavigationActions | null; state: NavigationActions snapshot | null; ready: boolean }` and `useBoardOrganization(hubId: string): BoardOrganization`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/board/organizationTestUtils.ts
// A hub answering the navigation reads an organization check makes
// (organizationCheck.ts): the manifest, a session's location with its tier
// and pin section, and the pin catalog. Anything else is a write, recorded
// and answered by `answer`. Tests only.
import type { NavigationReadParams } from "@evener/appwire-client";
import { manifest, wireV2 } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

/** A session id localSessionId accepts (22 base62 characters). */
export const SESSION_ID = "034Kc9793pXlhHyCRXdeAk";

export function organizationHub({ tier = "archived", pinSectionId = "release" } = {}) {
	const reads: NavigationReadParams[] = [];
	const writes: { method: string; params: unknown }[] = [];
	let answer: (method: string, params: unknown) => unknown = () => {
		throw new Error("unexpected mutation");
	};
	const client = {
		onNotification: () => () => {},
		request: async (method: string, params: NavigationReadParams) => {
			if (method !== "evener/navigation/read") {
				writes.push({ method, params });
				return answer(method, params);
			}
			reads.push(params);
			if (params.resource === "location") {
				const response = wireV2(
					params,
					{
						session: {
							ref: params.ref,
							session_id: String(params.ref).replace(/^local:/, ""),
							host_id: "local",
							title: "Session",
							live: true,
						},
					},
					'"location"',
					3,
					"g",
				);
				const metadata = (response.data as { metadata: Record<string, unknown> }).metadata;
				metadata.tier = tier;
				metadata.project_key = "p";
				metadata.pin_section_id = pinSectionId;
				return response;
			}
			if (params.resource === "pin_catalog")
				return wireV2(
					params,
					{ pin_sections: [{ id: "release", name: "Release", count: 1 }], remaining: 0 },
					'"pins"',
					3,
					"g",
				);
			return wireV2(params, manifest(), '"manifest"', 3, "g");
		},
	} as unknown as ConversationClientLike;
	return {
		client,
		reads,
		writes,
		answerWrites(handler: (method: string, params: unknown) => unknown) {
			answer = handler;
		},
	};
}
```

```ts
// mobile-native/src/board/organizationCheck.test.ts
import { expect, it } from "vitest";
import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import type { NavigationActionCheckpoint } from "../navigationActionRepository";
import { NavigationPages } from "../navigationPages";
import { organizationHub, SESSION_ID } from "./organizationTestUtils";
import { checkOrganizationChange, organizationFree } from "./organizationCheck";

function setup(options?: Parameters<typeof organizationHub>[0]) {
	const hub = organizationHub(options);
	const pinPages = new NavigationPages<NavigationPinSectionDescriptor>(
		hub.client,
		{ resource: "pin_catalog" },
		"pin_sections",
		(section) => section.id,
	);
	return { ...hub, pinPages };
}
const archive = (receipt: NavigationActionCheckpoint["receipt"]): NavigationActionCheckpoint => ({
	id: "archive",
	operation: { kind: "archive", params: { kind: "session", id: SESSION_ID, archived: true } },
	receipt,
});
const always = () => true;

it("checks an archive through the session's location, settled once the hub shows it", async () => {
	const { client, pinPages, reads } = setup();
	expect(await checkOrganizationChange(client, pinPages, archive({ generation_id: "g", targets: [] }), always, true)).toBe(true);
	expect(reads.map((read) => read.resource)).toEqual(["manifest", "location", "manifest"]);
	expect(reads[1]?.ref).toBe(`local:${SESSION_ID}`);
});

it("reports an archive the hub doesn't show as unsettled, without rejecting", async () => {
	const { client, pinPages } = setup({ tier: "recent" });
	expect(await checkOrganizationChange(client, pinPages, archive({ generation_id: "g", targets: [] }), always, true)).toBe(false);
});

it("reports an archive whose reply was lost as unsettled even when the hub shows it", async () => {
	const { client, pinPages } = setup();
	expect(await checkOrganizationChange(client, pinPages, archive(null), always, false)).toBe(false);
});

it("checks a pin change through the pin reads", async () => {
	const { client, pinPages, reads } = setup();
	const assign: NavigationActionCheckpoint = {
		id: "pin",
		operation: { kind: "assignPin", params: { sessionRef: "local:s", sectionId: "release" } },
		receipt: null,
	};
	expect(await checkOrganizationChange(client, pinPages, assign, always, false)).toBe(true);
	expect(reads.map((read) => read.resource)).toEqual(["location", "pin_catalog"]);
});

it("leaves a session deletion to the screen that made it, reading nothing", async () => {
	const { client, pinPages, reads } = setup();
	const deletion: NavigationActionCheckpoint = {
		id: "delete",
		operation: { kind: "deleteSession", params: { ref: `local:${SESSION_ID}` } },
		receipt: null,
	};
	await expect(checkOrganizationChange(client, pinPages, deletion, always, false)).rejects.toThrow(
		"checked on the screen that made it",
	);
	expect(reads).toEqual([]);
});

it("is free only with nothing pending, unresolved or unsaved", () => {
	const idle = { pending: false, uncertain: false, storageUnavailable: false };
	expect(organizationFree(idle)).toBe(true);
	expect(organizationFree(null)).toBe(false);
	expect(organizationFree({ ...idle, pending: true })).toBe(false);
	expect(organizationFree({ ...idle, uncertain: true })).toBe(false);
	expect(organizationFree({ ...idle, storageUnavailable: true })).toBe(false);
});
```

```tsx
// mobile-native/src/board/useBoardOrganization.test.tsx
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { organizationJournal } from "../nativeOrganization";
import { renderHook } from "../renderNative.testkit";
import { organizationHub, SESSION_ID } from "./organizationTestUtils";
import { useBoardOrganization } from "./useBoardOrganization";

const harness = vi.hoisted(() => {
	const values = new Map<string, string>();
	return {
		values,
		storage: {
			getItemSync: (key: string) => values.get(key) ?? null,
			setItemSync: (key: string, value: string) => void values.set(key, value),
			removeItemSync: (key: string) => void values.delete(key),
			getAllKeysSync: () => [...values.keys()],
		},
		connection: {} as Record<string, unknown>,
		focused: true,
	};
});
vi.mock("expo-sqlite/kv-store", () => ({ Storage: harness.storage }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "change-1" }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("@react-navigation/native", () => ({ useIsFocused: () => harness.focused }));

const hub = { id: "hub-1", name: "Work hub" };
async function until(check: () => void) {
	await act(async () => {
		await vi.waitFor(check);
	});
}
beforeEach(() => {
	harness.values.clear();
	harness.focused = true;
});

it("settles a change another screen left unresolved once the Board is focused and connected", async () => {
	const server = organizationHub();
	organizationJournal("hub-1").begin({
		kind: "archive",
		params: { kind: "session", id: SESSION_ID, archived: true },
	});
	harness.connection = { client: server.client, activeProfile: hub, state: "ready" };
	const hook = renderHook(() => useBoardOrganization("hub-1"));
	await until(() => expect(organizationJournal("hub-1").load()).toBeNull());
	expect(hook.result.current.ready).toBe(true);
	expect(server.reads.map((read) => read.resource)).toEqual(["manifest", "location", "manifest"]);
	hook.unmount();
});

it("reads nothing while the Board is covered, and settles when it comes back", async () => {
	const server = organizationHub();
	organizationJournal("hub-1").begin({
		kind: "archive",
		params: { kind: "session", id: SESSION_ID, archived: true },
	});
	harness.focused = false;
	harness.connection = { client: server.client, activeProfile: hub, state: "ready" };
	const hook = renderHook(() => useBoardOrganization("hub-1"));
	await act(async () => {});
	expect(server.reads).toEqual([]);
	expect(hook.result.current.ready).toBe(false);
	harness.focused = true;
	hook.rerender();
	await until(() => expect(organizationJournal("hub-1").load()).toBeNull());
	hook.unmount();
});

it("settles again when the connection comes back", async () => {
	const server = organizationHub();
	organizationJournal("hub-1").begin({
		kind: "archive",
		params: { kind: "session", id: SESSION_ID, archived: true },
	});
	harness.connection = { client: server.client, activeProfile: hub, state: "reconnecting" };
	const hook = renderHook(() => useBoardOrganization("hub-1"));
	await act(async () => {});
	expect(server.reads).toEqual([]);
	expect(hook.result.current.actions).toBeNull();
	harness.connection = { client: server.client, activeProfile: hub, state: "ready" };
	hook.rerender();
	await until(() => expect(organizationJournal("hub-1").load()).toBeNull());
	hook.unmount();
});

it("keeps organization closed while a change it can't check here is unresolved", async () => {
	const server = organizationHub();
	organizationJournal("hub-1").begin({ kind: "deleteSession", params: { ref: `local:${SESSION_ID}` } });
	harness.connection = { client: server.client, activeProfile: hub, state: "ready" };
	const hook = renderHook(() => useBoardOrganization("hub-1"));
	// The journal's own text proves the settle ran and refused; the Board never shows it.
	await until(() => expect(hook.result.current.state?.error).toMatch(/Could not confirm/));
	expect(hook.result.current.ready).toBe(false);
	expect(organizationJournal("hub-1").load()).not.toBeNull();
	expect(server.reads).toEqual([]);
	hook.unmount();
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/organizationCheck.test.ts src/board/useBoardOrganization.test.tsx`
Expected: FAIL: `Cannot find module './organizationCheck'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/board/organizationCheck.ts
// How the Board confirms an organization change against the hub, whichever
// screen journaled it. The organization journal holds one change per hub
// (navigationActionRepository.ts), so the Board's one NavigationActions has to
// settle archive and favorite changes (the Projects screen's read) and pin
// changes (the pin screens' read) alike (ruling 16).
import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "../navigationActionRepository";
import type { NavigationPages } from "../navigationPages";
import { readOrganizationNavigation } from "../organizationNavigation";
import { refreshPinNavigation } from "../pinNavigation";

/** Reads the hub's navigation for a journaled change. Resolves true when the
 * hub shows it, false when the hub shows otherwise (the Board then shows what
 * the hub has), and rejects for a change checked elsewhere (a session
 * deletion, on its own screen) or a read that fails. */
export async function checkOrganizationChange(
	client: ConversationClientLike,
	pinPages: NavigationPages<NavigationPinSectionDescriptor>,
	checkpoint: NavigationActionCheckpoint,
	current: () => boolean,
	confirmReceipt: boolean,
): Promise<boolean> {
	switch (checkpoint.operation.kind) {
		case "archive":
		case "favorite":
			return (await readOrganizationNavigation(client, checkpoint, current, confirmReceipt)).settled;
		case "assignPin":
		case "unpin":
		case "renamePinSection":
		case "deletePinSection":
			await refreshPinNavigation(client, pinPages, { checkpoint, current, confirmReceipt });
			return true;
		default:
			throw Error("This change is checked on the screen that made it.");
	}
}

/** Whether the journal can take a change now. */
export function organizationFree(
	state: { pending: boolean; uncertain: boolean; storageUnavailable: boolean } | null,
): boolean {
	return !!state && !state.pending && !state.uncertain && !state.storageUnavailable;
}
```

```ts
// mobile-native/src/board/useBoardOrganization.ts
// The Board's one NavigationActions (ruling 16). It checks whatever change
// the hub's organization journal holds (organizationCheck.ts), settles it
// whenever the Board is focused and connected, as usePinNavigation does, and
// never shows the journal's own error text: while a change is unresolved,
// `ready` is false and the Board hides its organization actions.
import { useIsFocused } from "@react-navigation/native";
import {
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useSyncExternalStore,
} from "react";
import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import { useConnection } from "../ConnectionProvider";
import { organizationJournal } from "../nativeOrganization";
import { NavigationActions } from "../navigationActions";
import { NavigationPages } from "../navigationPages";
import { checkOrganizationChange, organizationFree } from "./organizationCheck";

const noSnapshot = () => null;
const noSubscription = () => () => {};
const sectionKey = (section: NavigationPinSectionDescriptor) => section.id;

export interface BoardOrganization {
	/** Null unless this hub's connection is ready. */
	actions: NavigationActions | null;
	state: ReturnType<NavigationActions["getSnapshot"]> | null;
	/** The journal can take a change now. */
	ready: boolean;
}

export function useBoardOrganization(hubId: string): BoardOrganization {
	const { client, activeProfile, state } = useConnection();
	const focused = useIsFocused();
	const belongs = activeProfile?.id === hubId;
	const ready = belongs && !!client && state === "ready";
	const journal = useMemo(() => organizationJournal(hubId), [hubId]);
	const pinPages = useMemo(
		() =>
			client && belongs
				? new NavigationPages<NavigationPinSectionDescriptor>(
						client,
						{ resource: "pin_catalog" },
						"pin_sections",
						sectionKey,
					)
				: null,
		[client, belongs],
	);
	const binding = useMemo(
		() => ({ client, pinPages, ready, focused }),
		[client, pinPages, ready, focused],
	);
	const owner = useRef<typeof binding | null>(binding);
	owner.current = binding;
	const isCurrent = useCallback(
		() => owner.current === binding && binding.ready && binding.focused,
		[binding],
	);
	const actions = useMemo(() => {
		if (!client || !pinPages) return null;
		return new NavigationActions(
			client,
			async (_receipt, checkpoint) => {
				if (!checkpoint) throw Error("The organization change was not saved.");
				if (!(await checkOrganizationChange(client, pinPages, checkpoint, isCurrent, true)))
					throw Error("The hub doesn't show the change yet.");
			},
			isCurrent,
			async (checkpoint) => {
				// A read that succeeds settles the journal: the Board then shows
				// wherever the hub has the row, with nothing to review.
				if (checkpoint) await checkOrganizationChange(client, pinPages, checkpoint, isCurrent, false);
			},
			journal,
		);
	}, [client, pinPages, journal, isCurrent]);
	const actionState = useSyncExternalStore(
		actions?.subscribe ?? noSubscription,
		actions?.getSnapshot ?? noSnapshot,
	);
	useEffect(() => {
		if (ready && focused) void actions?.reconcile();
		return () => {
			if (owner.current === binding) owner.current = null;
			actions?.dispose();
			pinPages?.cancel();
		};
	}, [actions, ready, focused, pinPages, binding]);
	return {
		actions: ready ? actions : null,
		state: actionState,
		ready: ready && organizationFree(actionState),
	};
}
```

- [ ] **Step 4: Run them and watch them pass, then type-check**

Run: `cd mobile-native && npx vitest run src/board/organizationCheck.test.ts src/board/useBoardOrganization.test.tsx && npm run check`
Expected: PASS, with no act() warnings in the output.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/board/organizationCheck.ts mobile-native/src/board/organizationCheck.test.ts mobile-native/src/board/useBoardOrganization.ts mobile-native/src/board/useBoardOrganization.test.tsx mobile-native/src/board/organizationTestUtils.ts
git commit -m "feat(native): the Board settles the hub's organization journal on its own"
```

### Task 10.6: Projects or Hosts, Test runs and Archived on the Board

This task draws Tasks 10.2-10.5 on the Board. It is a screen task: the requirements and tests below are exact, and the layout follows part 1's `BoardRow` and band headers.

**Files:**
- Create: `mobile-native/src/board/useProjectSections.ts` (the three sections' data), `mobile-native/src/board/ProjectTreeRow.tsx` (one row for every `ProjectTreeItem` kind except `session`), `mobile-native/src/board/projectMenu.ts` (pure)
- Modify: `mobile-native/src/board/BoardScreen.tsx` (section headers, Organize by, the items, the project menu, `revealProject`; PR 2's Projects and Archived link rows removed)
- Modify: `mobile-native/src/organizationNavigation.ts` (export `controllerOwnedProject`, moved from `ProjectsScreen.tsx:606-608`) and `mobile-native/src/ProjectsScreen.tsx` (import it from there)
- Modify: `mobile-native/src/dev/demoFleet.ts` (project summaries carry `sources`)
- Test: `mobile-native/src/board/projectMenu.test.ts`, `mobile-native/src/board/ProjectTreeRow.test.tsx`, `mobile-native/src/board/BoardScreen.test.tsx`, `mobile-native/src/dev/demoFleet.test.ts`

**Interfaces:**
- Consumes: Tasks 10.2-10.5; part 1's `BoardRow`, `boardState`, `seenMarkers`, `foldedSections`, the Board's `manifest`, Live and Needs you snapshots (`BoardSnapshot`, part 1 Task 5), and `useConnection()`.
- Produces:
  - `useProjectSections(hubId: string, sources: readonly Source[]): Record<ProjectSection, { view: ProjectsView; controller: ProjectBrowserController | null; open(): void }>`
  - `type ProjectMenuAction = "pin" | "unpin" | "archive" | "unarchive"`, `PROJECT_MENU_LABELS: Record<ProjectMenuAction, string>` and `projectMenuActions(project: NavigationProjectSummary, context: { connected: boolean; organizationReady: boolean; archived: boolean }): ProjectMenuAction[]`
  - `controllerOwnedProject(sources?: readonly string[]): boolean`, now exported from `organizationNavigation.ts`
  - `revealProject(projectKey: string): void`, a function inside `BoardScreen` that part 2's Task 15 calls from a project search result
  - The Board holds one `useBoardOrganization(hubId)` (Task 12 uses the same one).

**Requirements (spec 7.1; rulings 11-15):**
1. **Sections, after the pinned categories, each a header item in the Board's list:**
   - "PROJECTS", or "HOSTS" while `grouping(manifest.sources, organizeBy)` is `"host-project"`, in the band headers' style (part 1 Task 7: 13pt semibold, uppercase, `inkMid`, 0.4 letter-spacing), with a fold chevron. Hidden once the projects catalog has loaded empty.
   - "Test runs · N" (N is `manifest.catalogs.test_runs.count`) and "ARCHIVED · N" (N is `manifest.catalogs.archived_projects.count`), folded by default, hidden while N is 0.
   - Section folds use `SECTION_FOLDS` in `foldedSections(hubId)`; every fold in a section (`ProjectTreeItem.fold`) persists there too, and toggling one re-renders.
   - The Board reads a section's catalog (`controller.initialLoad()`) when the section is first shown unfolded, so a folded Test runs or Archived costs no read.
2. **Organize by.** While `grouping(…)` isn't `"flat"`, the Projects header carries, at its trailing edge, "Project, then host" or "Host, then project" in `accentInk` with the `arrow.left.arrow.right` symbol, in a 44pt target. Tapping it flips the choice and saves it (`organizeByPreference(hubId).set`). Its accessibility label is "Organize by: Project, then host", with the hint "Changes to Host, then project" (and the reverse).
3. **Items:** each unfolded section renders `projectTreeItems({ section, projects: view.projects, pages: view.pages, sources: manifest.sources, organizeBy, isFolded, hostLiveCount })`, where `hostLiveCount` is `liveCountsByHost([...live.rows, ...needsYou.rows], live.loaded && live.remaining === 0 && needsYou.remaining === 0)`. Item keys are the list's keys.
4. **`ProjectTreeRow`** draws every item but sessions, indented 16pt per `depth` after the 16pt margin:
   - `host`: `server.rack`, the label (semibold 17/22, `inkHi`), and at the trailing edge an amber "Offline" (13pt semibold, `attentionInk`) when offline, else "N live" (13pt tabular, `inkLow`) when `liveCount` is set; then a fold chevron (`chevron.right` folded, `chevron.down` open). 48pt.
   - `project`: `folder`, a `pin.fill` in `inkLow` before the name when the project is pinned, the name (`project.name || project.working_dir || "Untitled project"`), "N live", the chevron. 48pt. Long-press opens the project menu (requirement 7).
   - `branch`: `server.rack` at 15pt, the host label in 15pt semibold `inkMid`, "Offline" in amber when offline, the chevron. 44pt.
   - `tier`: "Today" or "Recent", 13pt semibold `inkMid`, 32pt.
   - `archivedGroup`: "Archived · N" (or "Archived" while its count is null), 15pt `inkMid`, the chevron. 44pt.
   - `more`: "N more" in 13pt `inkLow`, 44pt, pressable.
   - `loading`: one 48pt skeleton on `inset`, no shimmer. `failed`: "Couldn't load these sessions." in 13pt `inkMid`, with no button: the Board reads it again on its own (requirement 6).
   - Every folding row is one pressable with `accessibilityRole="button"` and `accessibilityState={{ expanded: !folded }}`.
   - Session items render as part 1's quiet `BoardRow` (`variant: "quiet"`, `moving: false`) with `boardState(row, false, seenMarkers(hubId).isSeen(row))`, indented by `depth`.
5. **Reads:** `useProjectSections` makes one `createProjectBrowserController(client, catalog)` per section and client, pauses them when the Board blurs and resumes them on focus, and disposes them when the client changes or the Board unmounts. Each section's `view` is `projectsView(controller.getSnapshot(), retained)`, where `retained` is the view shown last; it resets to null when `hubId` changes, so a reconnect never blanks a section and a hub switch never shows another hub's projects. After each render, the projects in `expandedProjectKeys(items)` are expanded and the rest collapsed (`controller.expand` / `collapse`).
6. **Loading more, and reading again:** the list's `onViewableItemsChanged` (a stable callback, `viewabilityConfig: { itemVisiblePercentThreshold: 50 }`) passes the visible items to `morePagesToLoad`, and each result calls `controller.loadMoreSessions(projectKey, tier)`; pressing a `more` row does the same. When the Board is focused and the connection turns ready, and when a folded project is unfolded, each tier whose first read failed is read again with `controller.retry(projectKey, tier)`, and a failed catalog with `controller.retry()`. Nothing asks you to refresh.
7. **The project menu** (ruling 15): long-pressing a `project` row, while `projectMenuActions(project, { connected, organizationReady: organization.ready, archived: section === "archived" })` is not empty, opens `ActionSheetIOS.showActionSheetWithOptions` titled with the project's name, listing `PROJECT_MENU_LABELS` for each action and "Cancel". "Pin to top" and "Unpin" call `organization.actions.favorite(project.key, true | false)`; "Archive project" and "Unarchive project" call `organization.actions.archive({ kind: "project", id: project.key, workingDir: project.working_dir }, true | false)`. The project's rows dim (opacity 0.5) while the journal holds that project's change. Off iOS, `Alert.alert` with the same buttons.
8. **One journal:** the Board calls `useBoardOrganization(hubId)` once. If part 2's Task 9 mounted `usePinNavigation` on the Board for a category's Rename and Delete, those calls move to this hook's `actions.renamePinSection` and `deletePinSection` (ruling 16).
9. **Links:** delete PR 2's "Projects · n ›" and "Archived · n ›" rows. The Projects and Archived chips (part 1 Task 7) scroll to these sections' headers.
10. **`revealProject(projectKey)`:** finds the project in the projects view, unfolds each fold in `projectRevealTarget({ project, pages, sources, organizeBy }).unfold`, and once the `scrollTo` key is in the list, scrolls it to `viewPosition: 0.3` (animated unless Reduce Motion is on).
11. **The demo fleet** (`dev/demoFleet.ts` `projectSummary`): a project whose sessions sit on more than one host carries `sources`, listing `"local"` and each other host (the hub's own shape, `types.gen.ts:1983-1994`); a project on this hub alone carries none. Its `evener` project, with sessions on both hosts, shows the host grouping in the simulator.
12. **The host notice's count** (part 2's Task 14): the rows of every page in the three sections' views, all tiers, join `BoardScreen`'s `loadedRows`, the union Task 14 counts a host's sessions over (deduplicated by ref there). If Task 14 hasn't landed when this task runs, Task 14's PR adds them, as its text says.

- [ ] **Step 1: Write the failing tests**
  - `projectMenu.test.ts`, a table over `projectMenuActions`: a local, unpinned project with a working directory offers `["pin", "archive"]`; pinned, `["unpin", "archive"]`; in the Archived section or with `is_archived`, `"unarchive"`; with no `working_dir`, no archive action; a project any other host owns (`sources: ["local", "paradise-park"]` or `["paradise-park"]`), the `"no-project"` key, offline, or an organization that isn't ready: `[]`. Also `controllerOwnedProject(undefined)`, `([])` and `(["local"])` are true, and `(["local", "paradise-park"])` false.
  - `ProjectTreeRow.test.tsx` (render with `render` from `../renderNative.testkit`; mock `react-native` and `expo-symbols` as part 1's Task 4 does): an offline host shows "Offline" and no count; a connected one "3 live"; a pinned project shows `pin.fill` before its name; the `failed` item says "Couldn't load these sessions." and renders no pressable; each folding row reports `accessibilityState.expanded`.
  - `BoardScreen.test.tsx` (part 1's harness; add `wireV2` answers for `catalog` and `project_page` reads, and a manifest with sources `local` and `paradise-park`):
    - the Organize by control appears only when the manifest names a second host; pressing it flips the header from "PROJECTS" to "HOSTS" and saves `"host-project"` under `evener.native.board-organize.hub-1`;
    - organized by host, a project with `sources: ["local", "paradise-park"]` whose current page holds one row per host renders each session's title exactly once, under its own host, and an offline paradise-park reads "Offline";
    - unfolding a project reads its current, recent and archived pages once, and folding it again reads nothing more; the fold survives a remount (the fold is in the kv-store mock);
    - Test runs and Archived start folded and read no catalog until unfolded;
    - dropping the client and supplying a new one keeps every project row on screen until the new reads land;
    - long-pressing a local project row and choosing "Pin to top" sends `evener/favorite/set` `{ kind: "project", id, favorited: true }` once (mock `ActionSheetIOS` in the `react-native` mock and invoke its callback with the option's index);
    - pressing "12 more" asks for `offset: 20` of that tier;
    - no rendered text is "Projects ›", "Archived ›", "Reconnect" or "Refresh";
    - if part 2's Task 14 has landed: with paradise-park offline, a paradise-park row loaded only through a project page counts in "paradise-park is offline · N sessions".
  - `demoFleet.test.ts`: the projects catalog's `evener` summary carries `sources: ["local", "paradise-park"]`, and a project with sessions only on this hub carries no `sources`.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/board/projectMenu.test.ts src/board/ProjectTreeRow.test.tsx src/board/BoardScreen.test.tsx src/dev/demoFleet.test.ts`
- [ ] **Step 3: Implement** to the requirements. `projectMenu.ts` is:

```ts
// mobile-native/src/board/projectMenu.ts
// A project row's long-press actions (ruling 15): the project changes the
// Projects screen offers, for projects the phone can organize today.
import type { NavigationProjectSummary } from "@evener/appwire-client";
import { controllerOwnedProject } from "../organizationNavigation";

export type ProjectMenuAction = "pin" | "unpin" | "archive" | "unarchive";

export const PROJECT_MENU_LABELS: Record<ProjectMenuAction, string> = {
	pin: "Pin to top",
	unpin: "Unpin",
	archive: "Archive project",
	unarchive: "Unarchive project",
};

export function projectMenuActions(
	project: NavigationProjectSummary,
	context: { connected: boolean; organizationReady: boolean; archived: boolean },
): ProjectMenuAction[] {
	if (
		!context.connected ||
		!context.organizationReady ||
		project.key === "no-project" ||
		!controllerOwnedProject(project.sources)
	)
		return [];
	const actions: ProjectMenuAction[] = [project.favorite ? "unpin" : "pin"];
	if (project.working_dir)
		actions.push(context.archived || project.is_archived ? "unarchive" : "archive");
	return actions;
}
```

   Move `controllerOwnedProject` and the comment above it (`ProjectsScreen.tsx:597-608`) into `organizationNavigation.ts` unchanged, adding only `export`, and import it in `ProjectsScreen.tsx`:

```ts
export function controllerOwnedProject(sources?: readonly string[]): boolean {
	return (sources ?? []).every((source) => source === "local");
}
```

- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Build Release in the simulator against the demo hub (`EVENER_DEMO_FLEET=1 npx tsx scripts/demo-hub.mts`, and again with `EVENER_DEMO_FLEET_OFFLINE_HOST=1`): flip Organize by, unfold evener under each host and see each session once, see paradise-park "Offline", open Test runs and Archived. Against a local `evener-hub` (the demo hub answers no mutations), pin a project to the top and unpin it.
- [ ] **Step 5: Commit** (`feat(native): projects, hosts, test runs and archive on the Board`).

Open PR 3b: "feat(native): the Board's project sections (phase 2, PR 3b)". The description names the host rule (ruling 11), the project menu (ruling 15), the journal hook (ruling 16), and that part 2's Task 15 project tap can now land on `revealProject`.

---

## PR 4a: swipes (after part 2's PR 4)

Part 2's PR 4 (Task 11) installs `react-native-gesture-handler`, `react-native-reanimated` and `react-native-worklets` and wraps the app in `GestureHandlerRootView`. Tasks 12.1-12.5 build on it.

### Task 12.1: The shared toast

Create `mobile-native/src/Toast.tsx` and `mobile-native/src/Toast.test.tsx` exactly as phase 3's Task 1 ("A shared toast", `docs/superpowers/plans/2026-09-26-iphone-redesign-phase3-session.md`) writes them, Steps 1-5, commit message included: `useToast()`, `<Toast>`, `TOAST_MS = 4000` and `TOAST_ACTION_MS = 8000` (spec 7.3's 8-second Undo). The code lives in that plan, reviewed and on main, rather than in a second copy here that could drift from it (ruling 25). If phase 3's Task 1 has already landed, skip this task.

Interfaces produced: `interface ToastAction { label: string; run(): void }`, `interface ToastMessage { text: string; action?: ToastAction }`, `useToast(): { toast; show(message: ToastMessage): void; dismiss(): void }` and `<Toast toast dismiss />`.

### Task 12.2: Stop from the Board, through the durable runtime

**Files:**
- Create: `mobile-native/src/board/boardStops.ts`
- Test: `mobile-native/src/board/boardStops.test.ts`

**Interfaces:**
- Consumes: `createNativeMutationHost` (`nativeMutationHost.ts:57`), `NativeMutationRuntime` and `nativeMutationTargetKey` (`nativeMutationRuntime.ts`), `sessionControls` (`@evener/appwire-client`, `submitRouting.ts:126`), `READ_ITEM_LIMIT` (`mobile/src/services/conversation.ts:51`).
- Produces:
  - `type StopOutcome = "stopped" | "notWorking" | "unavailable"`
  - `class BoardStops`: constructor `(runtime: () => NativeMutationRuntime, hubId: string)`; `stop(client: AppwireClientLike, ref: string): Promise<StopOutcome>`; `stopping(ref: string): boolean`; `releaseAll(): void`; `dispose(): void`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/board/boardStops.test.ts
// Stop from the Board over the real runtime and SQLite storage, with only the
// wire faked (the composition nativeMutationDispatch.test.ts uses).
import { afterEach, expect, it, vi } from "vitest";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { NativeMutationRuntime, nativeMutationTargetKey } from "../nativeMutationRuntime";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "../sqliteSync.testkit";
import { BoardStops } from "./boardStops";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", () => ({
	randomUUID: () => "test-uuid",
	getRandomValues: (array: Uint8Array) => array,
}));

const CAPS: ThreadCapabilities = {
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	sharedNotes: true,
	queue: true,
	goal: true,
	rename: true,
};
function thread(status: string, evener: Partial<Thread["evener"]>): Thread {
	return {
		id: "thread-1",
		sessionId: "session-1",
		preview: "",
		ephemeral: false,
		modelProvider: "anthropic",
		createdAt: 1,
		updatedAt: 1,
		status: { type: status },
		cwd: "/tmp",
		cliVersion: "1.0.0",
		source: "local",
		turns: [],
		evener: { ref: "ref-1", capabilities: CAPS, queue: { revision: 0, depth: 0, preview: [] }, ...evener },
	};
}
const applied = (params: unknown) =>
	({
		receipt: {
			clientMutationId: (params as { clientMutationId: string }).clientMutationId,
			disposition: "applied",
			threadId: "thread-1",
			projectionState: "pending",
		},
	}) as never;

let databases: SqliteDoubleDatabase[] = [];
afterEach(() => {
	for (const database of databases) database.close();
	databases = [];
});
function setup(status = "active", evener: Partial<Thread["evener"]> = { instanceId: "instance-1" }) {
	const opened = openSqliteSyncDouble();
	databases.push(opened.database);
	let next = 0;
	const runtime = new NativeMutationRuntime(opened.port, {
		createMutationId: () => `mutation-${++next}`,
		now: () => 1,
		getOwnClientId: () => "origin-a",
	});
	const client = new FakeClient("ready");
	client.on("thread/read", () => ({ thread: thread(status, evener) }) as ThreadReadResponse);
	const stops = new BoardStops(() => runtime, "hub-1");
	return { runtime, client, stops, targetKey: nativeMutationTargetKey("hub-1", "ref-1") };
}
const calls = (client: FakeClient, method: string) => client.calls.filter((call) => call.method === method);
/** No registration is left: a ready client can't open a read for the target. */
const unregistered = (runtime: NativeMutationRuntime, client: FakeClient) =>
	runtime.beginAuthoritativeRead("hub-1", "ref-1", client) === undefined;

it("sends the interrupt now, fenced by a fresh read, and lets go once it has landed", async () => {
	const { runtime, client, stops, targetKey } = setup();
	client.on("turn/interrupt", applied);
	expect(await stops.stop(client, "ref-1")).toBe("stopped");
	await vi.waitFor(() => expect(calls(client, "turn/interrupt")).toHaveLength(1));
	expect(calls(client, "thread/read")[0]?.params).toEqual({
		ref: "ref-1",
		includeTurns: true,
		itemsView: "fragment",
		itemLimit: 40,
	});
	expect(calls(client, "turn/interrupt")[0]?.params).toEqual({
		ref: "ref-1",
		expectedInstanceId: "instance-1",
		clientMutationId: "mutation-1",
	});
	await vi.waitFor(() => expect(stops.stopping("ref-1")).toBe(false));
	expect(unregistered(runtime, client)).toBe(true);
	expect((await runtime.read(targetKey)).outbox).toEqual([]);
	await runtime.stop();
});

it("fences the interrupt with the thread id when the read names no instance", async () => {
	const { runtime, client, stops } = setup("active", {});
	client.on("turn/interrupt", applied);
	await stops.stop(client, "ref-1");
	await vi.waitFor(() => expect(calls(client, "turn/interrupt")).toHaveLength(1));
	expect(calls(client, "turn/interrupt")[0]?.params).toMatchObject({ expectedInstanceId: "thread-1" });
	await runtime.stop();
});

it("cancels the session's never-sent messages before anything can send, as the Session's Stop does", async () => {
	const { runtime, client, stops, targetKey } = setup();
	client.on("turn/interrupt", applied);
	client.on("turn/start", applied);
	// A message admitted while no screen held this session: durable, never sent.
	await runtime.submit({
		kind: "send",
		hubId: "hub-1",
		targetRef: "ref-1",
		threadId: "thread-1",
		instanceId: "instance-1",
		input: [{ type: "text", text: "sent before the Stop" }],
	});
	expect((await runtime.read(targetKey)).outbox).toMatchObject([{ method: "turn/start", state: "submitting" }]);
	expect(await stops.stop(client, "ref-1")).toBe("stopped");
	await vi.waitFor(() => expect(calls(client, "turn/interrupt")).toHaveLength(1));
	expect(calls(client, "turn/start")).toEqual([]);
	expect((await runtime.read(targetKey)).outbox).toMatchObject([{ method: "turn/start", state: "canceled" }]);
	await runtime.stop();
});

it("sends nothing to a session whose turn already ended", async () => {
	const { runtime, client, stops, targetKey } = setup("idle");
	expect(await stops.stop(client, "ref-1")).toBe("notWorking");
	expect(calls(client, "turn/interrupt")).toEqual([]);
	expect(stops.stopping("ref-1")).toBe(false);
	expect(unregistered(runtime, client)).toBe(true);
	expect((await runtime.read(targetKey)).outbox).toEqual([]);
	await runtime.stop();
});

it("sends nothing to a session the hub says must be resumed first", async () => {
	const { runtime, client, stops } = setup("active", { instanceId: "instance-1", resumeRequired: true });
	expect(await stops.stop(client, "ref-1")).toBe("unavailable");
	expect(calls(client, "turn/interrupt")).toEqual([]);
	expect(unregistered(runtime, client)).toBe(true);
	await runtime.stop();
});

it("leaves nothing registered when the read fails", async () => {
	const { runtime, client, stops } = setup();
	client.on("thread/read", () => {
		throw new Error("gone");
	});
	expect(await stops.stop(client, "ref-1")).toBe("unavailable");
	expect(unregistered(runtime, client)).toBe(true);
	await runtime.stop();
});

it("never opens a client that isn't connected", async () => {
	const { runtime, stops } = setup();
	const reconnecting = new FakeClient("reconnecting");
	expect(await stops.stop(reconnecting, "ref-1")).toBe("unavailable");
	expect(reconnecting.calls).toEqual([]);
	await runtime.stop();
});

it("lets go when the connection drops before the interrupt lands", async () => {
	const { runtime, client, stops } = setup();
	client.on("turn/interrupt", () => new Promise(() => {}) as never);
	expect(await stops.stop(client, "ref-1")).toBe("stopped");
	await vi.waitFor(() => expect(calls(client, "turn/interrupt")).toHaveLength(1));
	expect(stops.stopping("ref-1")).toBe(true);
	client.emitStateChange("reconnecting");
	expect(stops.stopping("ref-1")).toBe(false);
	client.emitStateChange("ready");
	expect(unregistered(runtime, client)).toBe(true);
	await runtime.stop();
});

it("sends one interrupt for two Stops at once", async () => {
	const { runtime, client, stops } = setup();
	client.on("turn/interrupt", applied);
	expect(await Promise.all([stops.stop(client, "ref-1"), stops.stop(client, "ref-1")])).toEqual(["stopped", "stopped"]);
	await vi.waitFor(() => expect(stops.stopping("ref-1")).toBe(false));
	expect(calls(client, "turn/interrupt")).toHaveLength(1);
	await runtime.stop();
});

it("dispose lets go of every Stop still being delivered and refuses new ones", async () => {
	const { runtime, client, stops } = setup();
	client.on("turn/interrupt", () => new Promise(() => {}) as never);
	await stops.stop(client, "ref-1");
	stops.dispose();
	expect(stops.stopping("ref-1")).toBe(false);
	expect(unregistered(runtime, client)).toBe(true);
	expect(await stops.stop(client, "ref-1")).toBe("unavailable");
	await runtime.stop();
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/boardStops.test.ts`
Expected: FAIL: `Cannot find module './boardStops'`.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/board/boardStops.ts
// Stop from the Board (spec 7.3), dispatched exactly as the Session's Stop
// is (ruling 17): a durable turn/interrupt through the process's mutation
// runtime, whose one write cancels the session's never-sent rows and bumps
// its stop epoch (NativeMutationRuntime.submit, enqueueInterruptAndCancel).
// The runtime sends only for a target a live host registered and opened with
// an authoritative read, so a Stop does what a Session screen's mount does
// (createNativeMutationHost, start, a fenced thread/read), enqueues the
// interrupt before it opens that gate, and holds the registration until the
// interrupt has left the outbox.
import type { AppwireClientLike, ThreadReadResponse } from "@evener/appwire-client";
import { sessionControls } from "@evener/appwire-client";
import { READ_ITEM_LIMIT } from "../../../mobile/src/services/conversation";
import {
	createNativeMutationHost,
	type NativeMutationHost,
} from "../nativeMutationHost";
import {
	type NativeMutationRuntime,
	nativeMutationTargetKey,
} from "../nativeMutationRuntime";

/** "stopped": the interrupt is durably admitted, the Session's "Stopped"
 * moment. "notWorking": a fresh read shows no turn to stop, so nothing was
 * sent. "unavailable": the session couldn't be read or opened for mutations. */
export type StopOutcome = "stopped" | "notWorking" | "unavailable";

export class BoardStops {
	readonly #runtime: () => NativeMutationRuntime;
	readonly #hubId: string;
	readonly #inFlight = new Map<string, Promise<StopOutcome>>();
	readonly #holds = new Map<string, () => void>();
	#disposed = false;

	/** `runtime` is called at the first Stop, so a Board that never stops never
	 * opens the mutations database (as ConversationScreen defers it). */
	constructor(runtime: () => NativeMutationRuntime, hubId: string) {
		this.#runtime = runtime;
		this.#hubId = hubId;
	}

	/** Whether a Stop for `ref` is still being read, sent or delivered. */
	stopping(ref: string): boolean {
		return this.#inFlight.has(ref) || this.#holds.has(ref);
	}

	stop(client: AppwireClientLike, ref: string): Promise<StopOutcome> {
		const running = this.#inFlight.get(ref);
		if (running) return running;
		if (this.#holds.has(ref)) return Promise.resolve("stopped");
		const run = this.#stop(client, ref).finally(() => this.#inFlight.delete(ref));
		this.#inFlight.set(ref, run);
		return run;
	}

	/** Lets go of every Stop still being delivered (the Board's client changed).
	 * An interrupt not yet sent stays in the outbox, delivered as any durable
	 * Stop is. */
	releaseAll(): void {
		for (const release of [...this.#holds.values()]) release();
	}

	dispose(): void {
		this.#disposed = true;
		this.releaseAll();
	}

	async #stop(client: AppwireClientLike, ref: string): Promise<StopOutcome> {
		if (this.#disposed || client.state !== "ready") return "unavailable";
		let runtime: NativeMutationRuntime;
		let host: NativeMutationHost;
		try {
			runtime = this.#runtime();
			host = createNativeMutationHost(runtime, this.#hubId, ref, client);
		} catch {
			return "unavailable";
		}
		try {
			await host.start();
		} catch {
			host.dispose();
			return "unavailable";
		}
		const lease = host.beginRead(ref);
		if (!lease) {
			host.dispose();
			return "unavailable";
		}
		let response: ThreadReadResponse;
		try {
			// The Session's own bounded projection read (conversation.ts
			// readProjection), without the subscription the Board doesn't hold.
			response = await client.request("thread/read", {
				ref,
				includeTurns: true,
				itemsView: "fragment",
				itemLimit: READ_ITEM_LIMIT,
			});
		} catch {
			host.dispose();
			return "unavailable";
		}
		const { thread } = response;
		const status = thread.status.type;
		// What reconcileAuthoritativeRead won't open the gate for: an interrupt
		// enqueued then would sit in the outbox until the session is opened.
		if (thread.evener.resumeRequired === true || status === "restartRequired" || status === "notLoaded") {
			host.dispose();
			return "unavailable";
		}
		// The store's requireControl(conversation, "stop", "interrupt").
		if (!sessionControls(status, thread.evener.capabilities, thread.evener.queue.depth).stop) {
			host.dispose();
			return "notWorking";
		}
		try {
			// Enqueued before the gate opens, so its cancel of never-sent rows
			// lands before anything on this session can send.
			await host.submit({
				kind: "interrupt",
				hubId: this.#hubId,
				targetRef: ref,
				threadId: thread.id,
				instanceId: thread.evener.instanceId ?? thread.id,
				input: [],
			});
		} catch {
			host.dispose();
			return "unavailable";
		}
		this.#hold(runtime, host, client, ref);
		// Opens the dispatch gate on this read; the runtime then sends the
		// interrupt. A lease gone stale (the connection dropped in these few
		// milliseconds) leaves it in the outbox, delivered as any durable Stop is.
		await host.reconcileRead(lease, response);
		return "stopped";
	}

	/** Keeps the registration until the interrupt leaves `submitting` (sent,
	 * refused into recovery, or blocked on an unknown outcome), or until the
	 * connection drops, which blocks the target anyway. */
	#hold(runtime: NativeMutationRuntime, host: NativeMutationHost, client: AppwireClientLike, ref: string): void {
		const targetKey = nativeMutationTargetKey(this.#hubId, ref);
		let released = false;
		const release = () => {
			if (released) return;
			released = true;
			stopStorage();
			stopState();
			host.dispose();
			if (this.#holds.get(ref) === release) this.#holds.delete(ref);
		};
		const check = async () => {
			const { outbox } = await runtime.read(targetKey);
			if (!outbox.some((record) => record.method === "turn/interrupt" && record.state === "submitting")) release();
		};
		const stopStorage = runtime.subscribeStorage((targetKeys) => {
			if (targetKeys.includes(targetKey)) void check().catch(() => undefined);
		});
		const stopState = client.onStateChange((state) => {
			if (state !== "ready") release();
		});
		this.#holds.set(ref, release);
		if (this.#disposed) release();
	}
}
```

- [ ] **Step 4: Run them and watch them pass, then type-check**

Run: `cd mobile-native && npx vitest run src/board/boardStops.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/board/boardStops.ts mobile-native/src/board/boardStops.test.ts
git commit -m "feat(native): Stop from the Board goes through the durable runtime, as the Session's does"
```

### Task 12.3: What a row can do, and the edge band

**Files:**
- Create: `mobile-native/src/board/rowActions.ts` and `mobile-native/src/board/swipeEdge.ts`
- Test: `mobile-native/src/board/rowActions.test.ts` and `mobile-native/src/board/swipeEdge.test.ts`

**Interfaces:**
- Consumes: `ClassifiedRow` and `BoardState` (part 1 Task 2); `NavigationActions`; `organizationFree` (Task 10.5).
- Produces:
  - `type RowAction = "pin" | "markRead" | "markUnread" | "stop" | "shutDown" | "archive" | "unarchive" | "rename"`
  - `interface RowActionContext { connected: boolean; organizationReady: boolean; archived: boolean }`
  - `rowMenuActions(item: ClassifiedRow, context: RowActionContext): RowAction[]` (phase 6's Task 16 calls it "the function that lists a row's menu actions per state")
  - `interface SwipeActions { leading: "archive" | "unarchive" | null; trailing: ("stop" | "pin" | "more")[] }` and `swipeActions(item, context): SwipeActions`
  - `ROW_ACTION_LABELS: Record<RowAction, string>`
  - `isTopLevel(row: NavigationSessionSummary): boolean` and `archiveTarget(row: NavigationSessionSummary): Omit<ArchiveParams, "archived"> | null`
  - `archiveSession(actions: NavigationActions, target: Omit<ArchiveParams, "archived">, archived: boolean): Promise<boolean>`
  - `pinSession(actions: NavigationActions, target: SessionPinAssignParams): Promise<boolean>`
  - `archivingSessionId(state: NavigationActions snapshot | null): string | null`
  - `shutDownSession(client: ConversationClientLike, ref: string): Promise<void>` and `renameSession(client: ConversationClientLike, ref: string, name: string): Promise<boolean>`
  - From `swipeEdge.ts`: `EDGE_ZONE_PT = 24` and `startsInEdgeZone(pageX: number): boolean`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/board/swipeEdge.test.ts
import { expect, it } from "vitest";
import { EDGE_ZONE_PT, startsInEdgeZone } from "./swipeEdge";

it("gives the screen's left 24 points to the system's back gesture (spec 7.3)", () => {
	expect(EDGE_ZONE_PT).toBe(24);
	expect([0, 12, 23.9, 24, 200].map(startsInEdgeZone)).toEqual([true, true, true, false, false]);
});
```

```ts
// mobile-native/src/board/rowActions.test.ts
import { describe, expect, it } from "vitest";
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { nativeNavigationActions } from "../navigationActionRepository";
import { NavigationActions } from "../navigationActions";
import type { BoardState } from "./attention";
import { organizationHub, SESSION_ID } from "./organizationTestUtils";
import {
	archiveSession,
	archiveTarget,
	archivingSessionId,
	pinSession,
	type RowActionContext,
	renameSession,
	rowMenuActions,
	shutDownSession,
	swipeActions,
} from "./rowActions";

const local = `local:${SESSION_ID}`;
const row = (over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref: local,
	host_id: "local",
	session_id: SESSION_ID,
	title: "Session",
	project: "evener",
	state: "active",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const remote = { ref: "paradise-park:x", host_id: "paradise-park", session_id: "x" };
const online: RowActionContext = { connected: true, organizationReady: true, archived: false };

describe("the long-press menu per state (spec 7.3)", () => {
	it.each([
		["a working session of this hub", row({ rename: true }), "working", online, ["pin", "stop", "shutDown", "archive", "rename"]],
		["a finished one", row({ state: "awaiting" }), "finished", online, ["pin", "markRead", "shutDown", "archive"]],
		["one seen since", row({ state: "idle" }), "idle", online, ["pin", "markUnread", "shutDown", "archive"]],
		["one asking a question", row({ state: "awaiting", ask_pending: true }), "question", online, ["pin", "shutDown", "archive"]],
		["one needing a restart", row({ state: "restartRequired" }), "restartNeeded", online, ["pin", "archive"]],
		["one working on another host", row({ ...remote }), "working", online, ["pin", "stop", "shutDown"]],
		["one on an offline host", row({ ...remote, offline: true, live: false }), "shutDown", online, ["pin"]],
		["a fork", row({ kind: "fork" }), "working", online, ["stop", "shutDown"]],
		["one in an archived tier", row({ state: "ended", live: false }), "shutDown", { ...online, archived: true }, ["pin", "unarchive"]],
		["one while a change is unresolved", row({ rename: true }), "working", { ...online, organizationReady: false }, ["pin", "stop", "shutDown", "rename"]],
		["a finished one offline", row({ state: "awaiting" }), "finished", { ...online, connected: false }, ["markRead"]],
		["a seen one offline", row({ state: "idle" }), "idle", { ...online, connected: false }, ["markUnread"]],
		["a working one offline", row(), "working", { ...online, connected: false }, []],
	] as const)("%s", (_name, summary, state, context, expected) => {
		expect(rowMenuActions({ row: summary, state: state as BoardState }, context)).toEqual(expected);
	});
});

describe("swipes (spec 7.3)", () => {
	it.each([
		["a working session of this hub", row(), "working", online, { leading: "archive", trailing: ["stop", "pin", "more"] }],
		["a finished one", row({ state: "awaiting" }), "finished", online, { leading: "archive", trailing: ["pin", "more"] }],
		["one working on another host", row({ ...remote }), "working", online, { leading: null, trailing: ["stop", "pin", "more"] }],
		["one in an archived tier", row({ state: "ended", live: false }), "shutDown", { ...online, archived: true }, { leading: "unarchive", trailing: ["pin", "more"] }],
		["any row offline", row(), "working", { ...online, connected: false }, { leading: null, trailing: ["more"] }],
	] as const)("%s", (_name, summary, state, context, expected) => {
		expect(swipeActions({ row: summary, state: state as BoardState }, context)).toEqual(expected);
	});
});

describe("archiving (rulings 16 and 20)", () => {
	it("archives only this hub's own top-level sessions, which the journal can confirm", () => {
		expect(archiveTarget(row())).toEqual({ kind: "session", id: SESSION_ID });
		expect(archiveTarget(row({ ...remote }))).toBeNull();
		expect(archiveTarget(row({ ref: "cluster:abc" }))).toBeNull();
		expect(archiveTarget(row({ ref: "local:not-a-session", session_id: "not-a-session" }))).toBeNull();
		for (const kind of ["subagent", "fork", "cluster"]) expect(archiveTarget(row({ kind }))).toBeNull();
	});

	function journal() {
		const values = new Map<string, unknown>();
		let id = 0;
		return nativeNavigationActions("hub", {
			createId: () => String(++id),
			get: (key) => values.get(key),
			set: (key, value) => {
				values.set(key, value);
			},
			deleteIf: (key, value) => JSON.stringify(values.get(key)) === JSON.stringify(value) && values.delete(key),
		});
	}
	function organization({ current = true, accept = true } = {}) {
		const hub = organizationHub();
		hub.answerWrites(() => {
			if (!accept) throw new Error("refused");
			return { ok: true, changed: true, navigation: { generation_id: "g", targets: [] } };
		});
		const storage = journal();
		const actions = new NavigationActions(hub.client, async () => {}, () => current, async () => {}, storage);
		return { hub, storage, actions };
	}

	it("archives through the journal and reports it confirmed", async () => {
		const { hub, storage, actions } = organization();
		expect(await archiveSession(actions, { kind: "session", id: SESSION_ID }, true)).toBe(true);
		expect(hub.writes).toEqual([
			{ method: "evener/archive/set", params: { kind: "session", id: SESSION_ID, archived: true } },
		]);
		expect(storage.load()).toBeNull();
	});

	it("reports a refused archive as unconfirmed, with the journal holding it for the Board to settle", async () => {
		const { storage, actions } = organization({ accept: false });
		expect(await archiveSession(actions, { kind: "session", id: SESSION_ID }, true)).toBe(false);
		expect(storage.load()).not.toBeNull();
		expect(actions.getSnapshot().uncertain).toBe(true);
	});

	it("sends nothing while another change is unresolved, or when the Board isn't on screen", async () => {
		const busy = organization();
		busy.storage.begin({ kind: "unpin", params: { sessionRef: "local:other" } });
		const blocked = new NavigationActions(busy.hub.client, async () => {}, () => true, async () => {}, busy.storage);
		expect(await archiveSession(blocked, { kind: "session", id: SESSION_ID }, true)).toBe(false);
		const covered = organization({ current: false });
		expect(await archiveSession(covered.actions, { kind: "session", id: SESSION_ID }, true)).toBe(false);
		expect([...busy.hub.writes, ...covered.hub.writes]).toEqual([]);
	});

	it("pins select mode's sessions through the same journal", async () => {
		const { hub, actions } = organization();
		expect(await pinSession(actions, { sessionRef: local, sectionName: "Release" })).toBe(true);
		expect(hub.writes).toEqual([
			{ method: "evener/session-pin/assign", params: { sessionRef: local, sectionName: "Release" } },
		]);
	});

	it("names the session an unresolved archive is about, so its rows dim", () => {
		const recovery = {
			id: "1",
			operation: { kind: "archive" as const, params: { kind: "session", id: SESSION_ID, archived: true } },
			receipt: null,
		};
		const idle = { pending: false, uncertain: false, recovery: null };
		expect(archivingSessionId({ ...idle, pending: true, recovery })).toBe(SESSION_ID);
		expect(archivingSessionId({ ...idle, uncertain: true, recovery })).toBe(SESSION_ID);
		expect(archivingSessionId(idle)).toBeNull();
		expect(
			archivingSessionId({
				...idle,
				pending: true,
				recovery: { ...recovery, operation: { kind: "archive", params: { kind: "project", id: "p", workingDir: "/w", archived: true } } },
			}),
		).toBeNull();
	});
});

describe("the Session's direct requests (ruling 19)", () => {
	function recorder() {
		const requests: { method: string; params: unknown }[] = [];
		const client = {
			request: async (method: string, params: unknown) => {
				requests.push({ method, params });
				return {};
			},
			onNotification: () => () => {},
		} as unknown as ConversationClientLike;
		return { client, requests };
	}

	it("shuts a session down with thread/shutdown", async () => {
		const { client, requests } = recorder();
		await shutDownSession(client, local);
		expect(requests).toEqual([{ method: "thread/shutdown", params: { ref: local } }]);
	});

	it("renames with the trimmed name, and sends nothing for a blank one", async () => {
		const { client, requests } = recorder();
		expect(await renameSession(client, local, "  Fix the settle race ")).toBe(true);
		expect(await renameSession(client, local, "   ")).toBe(false);
		expect(requests).toEqual([
			{ method: "evener/thread/name/set", params: { ref: local, name: "Fix the settle race" } },
		]);
	});
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/rowActions.test.ts src/board/swipeEdge.test.ts`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/board/swipeEdge.ts
/** A swipe that begins this close to the screen's left edge belongs to the
 * system's back gesture and never acts on a row (spec 7.3). */
export const EDGE_ZONE_PT = 24;

export function startsInEdgeZone(pageX: number): boolean {
	return pageX < EDGE_ZONE_PT;
}
```

```ts
// mobile-native/src/board/rowActions.ts
// What a Board row can do (spec 7.3), and how each change reaches the hub:
// the path the Session or the Projects screen already takes for it (this
// plan's "How the Board's actions reach the hub"). Stop is BoardStops.
import type {
	ArchiveParams,
	NavigationSessionSummary,
	SessionPinAssignParams,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import type { NavigationActionCheckpoint } from "../navigationActionRepository";
import type { NavigationActions } from "../navigationActions";
import { localSessionId } from "../sessionDeletionResult";
import type { ClassifiedRow } from "./attention";
import { organizationFree } from "./organizationCheck";

export type RowAction =
	| "pin"
	| "markRead"
	| "markUnread"
	| "stop"
	| "shutDown"
	| "archive"
	| "unarchive"
	| "rename";

export const ROW_ACTION_LABELS: Record<RowAction, string> = {
	pin: "Pin to category…",
	markRead: "Mark as read",
	markUnread: "Mark as unread",
	stop: "Stop",
	shutDown: "Shut down",
	archive: "Archive",
	unarchive: "Unarchive",
	rename: "Rename",
};

export interface RowActionContext {
	/** The hub connection is ready (ruling 21). */
	connected: boolean;
	/** The Board's organization journal can take a change now (ruling 16). */
	organizationReady: boolean;
	/** The row sits in an archived tier. */
	archived: boolean;
}

const NESTED = new Set(["subagent", "fork", "cluster"]);
/** A session of its own, not a subagent, fork or cluster: the rows the
 * organization changes act on. */
export function isTopLevel(row: NavigationSessionSummary): boolean {
	return !NESTED.has(row.kind);
}

/** The archive change for a row, or null when the organization journal can't
 * confirm one: only this hub's own top-level sessions (ruling 20), the rule
 * ProjectsScreen applies (ProjectsScreen.tsx:749-757), with the ref shape
 * readOrganizationNavigation checks (`localSessionId`). */
export function archiveTarget(row: NavigationSessionSummary): Omit<ArchiveParams, "archived"> | null {
	if (!isTopLevel(row) || row.host_id !== "local" || localSessionId(row.ref) !== row.session_id) return null;
	return { kind: "session", id: row.session_id };
}

/** The long-press menu, in spec 7.3's order. Copy link waits for a session
 * deep link (ruling 27). Offline, only the phone's own read marks remain. */
export function rowMenuActions({ row, state }: ClassifiedRow, context: RowActionContext): RowAction[] {
	const { connected } = context;
	const actions: RowAction[] = [];
	if (connected && isTopLevel(row)) actions.push("pin");
	if (state === "finished") actions.push("markRead");
	if (state === "idle") actions.push("markUnread");
	if (connected && state === "working") actions.push("stop");
	if (connected && row.live && !row.offline && row.state !== "restartRequired") actions.push("shutDown");
	if (connected && context.organizationReady && archiveTarget(row)) actions.push(context.archived ? "unarchive" : "archive");
	if (connected && row.rename === true) actions.push("rename");
	return actions;
}

export interface SwipeActions {
	leading: "archive" | "unarchive" | null;
	trailing: ("stop" | "pin" | "more")[];
}

/** A swipe right archives, or unarchives in an archived tier; a swipe left
 * reveals Stop (while working), Pin and More (spec 7.3). */
export function swipeActions(item: ClassifiedRow, context: RowActionContext): SwipeActions {
	const menu = rowMenuActions(item, context);
	const leading = menu.includes("archive") ? "archive" : menu.includes("unarchive") ? "unarchive" : null;
	const trailing: SwipeActions["trailing"] = [];
	if (menu.includes("stop")) trailing.push("stop");
	if (menu.includes("pin")) trailing.push("pin");
	trailing.push("more");
	return { leading, trailing };
}

/** Runs one journaled change. True only when it ran and the hub confirmed
 * it; false when the journal refused it (busy, unresolved, or the Board not
 * on screen) or its outcome is unknown, which leaves the journal holding it
 * for the Board to settle. */
async function journaled(actions: NavigationActions, change: () => Promise<void>): Promise<boolean> {
	if (!organizationFree(actions.getSnapshot())) return false;
	let ran = false;
	const stop = actions.subscribe(() => {
		if (actions.getSnapshot().pending) ran = true;
	});
	try {
		await change();
	} finally {
		stop();
	}
	const state = actions.getSnapshot();
	return ran && organizationFree(state) && state.recovery === null;
}

/** Archive or Unarchive, as the Projects screen does it
 * (NavigationActions.archive and the organization journal). */
export function archiveSession(
	actions: NavigationActions,
	target: Omit<ArchiveParams, "archived">,
	archived: boolean,
): Promise<boolean> {
	return journaled(actions, () => actions.archive(target, archived));
}

/** Select mode's Pin (ruling 18); a single row's Pin opens PinAssignment. */
export function pinSession(actions: NavigationActions, target: SessionPinAssignParams): Promise<boolean> {
	return journaled(actions, () => actions.assignPin(target));
}

/** The session an unresolved archive is about, so its rows dim until the hub
 * confirms (spec 14). */
export function archivingSessionId(
	state: { pending: boolean; uncertain: boolean; recovery: NavigationActionCheckpoint | null } | null,
): string | null {
	const operation = state?.recovery?.operation;
	if (!state || (!state.pending && !state.uncertain) || operation?.kind !== "archive" || operation.params.kind !== "session")
		return null;
	return operation.params.id;
}

/** Shut down: the Session's own request (conversation.ts:1128-1132). */
export async function shutDownSession(client: ConversationClientLike, ref: string): Promise<void> {
	await client.request("thread/shutdown", { ref });
}

/** Rename: the Session's own request (conversation.ts:1197-1201). */
export async function renameSession(client: ConversationClientLike, ref: string, name: string): Promise<boolean> {
	const trimmed = name.trim();
	if (!trimmed) return false;
	await client.request("evener/thread/name/set", { ref, name: trimmed });
	return true;
}
```

- [ ] **Step 4: Run them and watch them pass, then type-check**

Run: `cd mobile-native && npx vitest run src/board/rowActions.test.ts src/board/swipeEdge.test.ts && npm run check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile-native/src/board/rowActions.ts mobile-native/src/board/rowActions.test.ts mobile-native/src/board/swipeEdge.ts mobile-native/src/board/swipeEdge.test.ts
git commit -m "feat(native): what each Board row can do, and the path each change takes to the hub"
```

### Task 12.4: The swipeable row

**Files:**
- Create: `mobile-native/src/board/SwipeRow.tsx`
- Modify: `mobile-native/src/renderNative.testkit.tsx` (`gestureHandlerModuleMock()` and `swipeableCalls`)
- Test: `mobile-native/src/board/SwipeRow.test.tsx`

**Interfaces:**
- Consumes: `ReanimatedSwipeable` and `SwipeDirection` from `react-native-gesture-handler/ReanimatedSwipeable` (part 2 Task 11); `EDGE_ZONE_PT` and `startsInEdgeZone` (Task 12.3).
- Produces:
  - `interface SwipeAction { key: string; label: string; symbol: SFSymbol; fill: "inkHi" | "inkMid" | "inkLow"; run(): void }`
  - `<SwipeRow leading?: SwipeAction trailing?: readonly SwipeAction[] onActiveChange?: (active: boolean) => void>{children}</SwipeRow>`, generic over its children and actions so phase 3's queued-message swipes reuse it (phase 3 Task 34)
  - `swipeAccessibility(leading, trailing): { accessibilityActions; onAccessibilityAction }`, for the row's own accessible element
  - From the testkit: `gestureHandlerModuleMock()` and `swipeableCalls: { closes: number }`

- [ ] **Step 1: Add the gesture mock to the testkit**

In `mobile-native/src/renderNative.testkit.tsx`, add `forwardRef`, `useImperativeHandle` and `type ForwardedRef` to the `react` import, and:

```tsx
/** How many times a mocked swipeable's ref was closed; reset it per test. */
export const swipeableCalls = { closes: 0 };

/** react-native-gesture-handler/ReanimatedSwipeable as an inert host element:
 * it renders its children and carries every prop, so a test finds it by type
 * and drives its callbacks; its ref's close() is counted. */
export function gestureHandlerModuleMock() {
	const ReanimatedSwipeable = forwardRef(function ReanimatedSwipeable(
		props: { children?: ReactNode } & Record<string, unknown>,
		ref: ForwardedRef<unknown>,
	) {
		useImperativeHandle(ref, () => ({
			close: () => {
				swipeableCalls.closes += 1;
			},
			openLeft: () => {},
			openRight: () => {},
			reset: () => {},
		}));
		return createElement("ReanimatedSwipeable", props, props.children);
	});
	return {
		__esModule: true,
		default: ReanimatedSwipeable,
		SwipeDirection: { LEFT: "left", RIGHT: "right" },
	};
}
```

- [ ] **Step 2: Write the failing tests**

```tsx
// mobile-native/src/board/SwipeRow.test.tsx
import { Text } from "react-native";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { render, swipeableCalls } from "../renderNative.testkit";
import { type SwipeAction, SwipeRow, swipeAccessibility } from "./SwipeRow";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("../renderNative.testkit")).gestureHandlerModuleMock(),
);

const action = (key: string, label: string) => ({
	key,
	label,
	symbol: "archivebox" as const,
	fill: "inkMid" as const,
	run: vi.fn(),
});
function mount(options: { leading?: SwipeAction | null } = {}) {
	const leading = action("archive", "Archive");
	const pin = action("pin", "Pin");
	const onActiveChange = vi.fn();
	const tree: ReactTestRenderer = render(
		<SwipeRow
			leading={options.leading === null ? undefined : leading}
			trailing={[pin]}
			onActiveChange={onActiveChange}
		>
			<Text>row</Text>
		</SwipeRow>,
	);
	const swipeable = tree.root.findByType("ReanimatedSwipeable" as never);
	const touchStart = (pageX: number) =>
		act(() => tree.root.findByProps({ testID: "swipe-row-content" }).props.onTouchStart({ nativeEvent: { pageX } }));
	return { swipeable, leading, pin, onActiveChange, touchStart };
}
beforeEach(() => {
	swipeableCalls.closes = 0;
});

it("a full swipe right does the leading action once and closes the row", () => {
	const { swipeable, leading, touchStart } = mount();
	expect(swipeable.props.leftThreshold).toBe(195);
	touchStart(200);
	act(() => swipeable.props.onSwipeableOpen("right"));
	expect(leading.run).toHaveBeenCalledOnce();
	expect(swipeableCalls.closes).toBe(1);
});

it("a swipe that began in the left edge band closes without acting, whichever way it opened", () => {
	const { swipeable, leading, pin, touchStart } = mount();
	touchStart(10);
	act(() => swipeable.props.onSwipeableWillOpen("right"));
	act(() => swipeable.props.onSwipeableOpen("right"));
	act(() => swipeable.props.onSwipeableWillOpen("left"));
	act(() => swipeable.props.onSwipeableOpen("left"));
	expect(leading.run).not.toHaveBeenCalled();
	expect(pin.run).not.toHaveBeenCalled();
	expect(swipeableCalls.closes).toBe(4);
});

it("takes the left edge band out of the row's hit frame", () => {
	expect(mount().swipeable.props.hitSlop).toEqual({ left: -24 });
});

it("a revealed action acts and closes the row", () => {
	const { swipeable, pin } = mount();
	const panel = render(swipeable.props.renderRightActions());
	const button = panel.root.find(
		(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Pin",
	);
	act(() => button.props.onPress());
	expect(pin.run).toHaveBeenCalledOnce();
	expect(swipeableCalls.closes).toBe(1);
});

it("holds the list from a swipe's first drag until the row is closed again", () => {
	const { swipeable, onActiveChange } = mount();
	act(() => swipeable.props.onSwipeableOpenStartDrag("left"));
	expect(onActiveChange).toHaveBeenLastCalledWith(true);
	act(() => swipeable.props.onSwipeableClose("left"));
	expect(onActiveChange).toHaveBeenLastCalledWith(false);
});

it("has no leading panel without a leading action", () => {
	expect(mount({ leading: null }).swipeable.props.renderLeftActions).toBeUndefined();
});

it("gives VoiceOver every action", () => {
	const archive = action("archive", "Archive");
	const pin = action("pin", "Pin");
	const accessibility = swipeAccessibility(archive, [pin]);
	expect(accessibility.accessibilityActions).toEqual([
		{ name: "archive", label: "Archive" },
		{ name: "pin", label: "Pin" },
	]);
	accessibility.onAccessibilityAction({ nativeEvent: { actionName: "pin" } } as never);
	expect(pin.run).toHaveBeenCalledOnce();
	expect(archive.run).not.toHaveBeenCalled();
});
```

- [ ] **Step 3: Check two facts in the installed package, then implement**

Read `node_modules/react-native-gesture-handler/src/components/ReanimatedSwipeable/ReanimatedSwipeable.tsx` (version 2.32; this plan was checked against 2.29.1's copy of the same file):
1. Which `SwipeDirection` `onSwipeableOpen` reports when the left panel (`renderLeftActions`) opens. In 2.29.1, `dispatchEndEvents` passes `toValue > 0 ? SwipeDirection.RIGHT : SwipeDirection.LEFT`: the direction names the swipe, not the panel. If the installed version differs, change `LEADING_OPENED` and the test's `"right"` to match, and say so in the commit.
2. Where `hitSlop` goes. In 2.29.1 it is a view prop, `hitSlop={hitSlop ?? undefined}` on the outer `Animated.View` that the pan's `GestureDetector` wraps, not a gesture setting. React Native 0.86's `RCTViewComponentView` turns a negative `left` into a hit frame that starts 24pt in (`hitTestEdgeInsets`), and while none of the row's children overflow its bounds it never hit-tests them outside that frame (`betterHitTest:withEvent:`). So a touch in the band reaches neither the row's pan nor anything inside the row: a tap there opens nothing, which is deliberate (the band is the row's 16pt margin and the edge of its mark). If 2.32 moved `hitSlop` onto the gesture instead, the band still refuses the pan, and taps there then reach the row; either is fine.

```tsx
// mobile-native/src/board/SwipeRow.tsx
// A row that swipes (spec 7.3): a full swipe right does its leading action
// (Archive on the Board), and a swipe left reveals its trailing actions
// (Stop, Pin, More). It takes any children and actions, so phase 3's queued
// messages reuse it. A swipe that begins in the screen's left 24 points never
// acts: a touch there never reaches the row (hitSlop takes the band out of
// its hit frame), and anything a swipe from there opens is closed without
// acting. hitSlop measures the band from the row's own left edge, so a row
// that doesn't start at the screen's edge still has the start-x guard, which
// reads window points.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { type ReactNode, useRef } from "react";
import {
	type AccessibilityActionEvent,
	Pressable,
	Text,
	useWindowDimensions,
	View,
} from "react-native";
import ReanimatedSwipeable, {
	SwipeDirection,
	type SwipeableMethods,
} from "react-native-gesture-handler/ReanimatedSwipeable";
import { useColors } from "../ui";
import { EDGE_ZONE_PT, startsInEdgeZone } from "./swipeEdge";

export interface SwipeAction {
	key: string;
	label: string;
	symbol: SFSymbol;
	fill: "inkHi" | "inkMid" | "inkLow";
	run(): void;
}

export interface SwipeRowProps {
	/** Done by a full swipe right: past half the row. */
	leading?: SwipeAction;
	/** Revealed by a swipe left, each a button. */
	trailing?: readonly SwipeAction[];
	/** True from a swipe's first drag until the row is closed again. */
	onActiveChange?(active: boolean): void;
	children: ReactNode;
}

const ACTION_WIDTH = 76;
/** ReanimatedSwipeable names the swipe, not the panel: a swipe to the right
 * opens the left (leading) panel (checked in Task 12.4 Step 3). */
const LEADING_OPENED = SwipeDirection.RIGHT;

/** The row's actions for VoiceOver, spread on the row's accessible element. */
export function swipeAccessibility(leading: SwipeAction | undefined, trailing: readonly SwipeAction[]) {
	const actions = [...(leading ? [leading] : []), ...trailing];
	return {
		accessibilityActions: actions.map((action) => ({ name: action.key, label: action.label })),
		onAccessibilityAction: (event: AccessibilityActionEvent) =>
			actions.find((action) => action.key === event.nativeEvent.actionName)?.run(),
	};
}

export function SwipeRow({ leading, trailing = [], onActiveChange, children }: SwipeRowProps) {
	const { palette } = useColors();
	const { width } = useWindowDimensions();
	const swipeable = useRef<SwipeableMethods>(null);
	// Where the touch that may become a swipe began, in window points.
	const startX = useRef(Number.POSITIVE_INFINITY);
	const close = () => swipeable.current?.close();
	const button = (action: SwipeAction) => (
		<Pressable
			key={action.key}
			accessibilityRole="button"
			accessibilityLabel={action.label}
			onPress={() => {
				close();
				action.run();
			}}
			style={{
				width: ACTION_WIDTH,
				alignItems: "center",
				justifyContent: "center",
				gap: 4,
				backgroundColor: palette[action.fill],
			}}
		>
			<SymbolView name={action.symbol} tintColor={palette.page} size={20} />
			<Text style={{ color: palette.page, fontSize: 13, fontWeight: "600" }}>{action.label}</Text>
		</Pressable>
	);
	return (
		<ReanimatedSwipeable
			ref={swipeable}
			// Takes the left edge band out of the row's hit frame, so the system's
			// back gesture keeps those touches (spec 7.3; Step 3 has the details).
			hitSlop={{ left: -EDGE_ZONE_PT }}
			leftThreshold={width / 2}
			renderLeftActions={leading ? () => <View style={{ flexDirection: "row" }}>{button(leading)}</View> : undefined}
			renderRightActions={
				trailing.length > 0
					? () => <View style={{ flexDirection: "row" }}>{trailing.map((action) => button(action))}</View>
					: undefined
			}
			onSwipeableOpenStartDrag={() => onActiveChange?.(true)}
			onSwipeableWillOpen={() => {
				if (startsInEdgeZone(startX.current)) close();
			}}
			onSwipeableOpen={(direction) => {
				if (startsInEdgeZone(startX.current)) {
					close();
					return;
				}
				if (direction === LEADING_OPENED && leading) {
					close();
					leading.run();
				}
			}}
			onSwipeableClose={() => onActiveChange?.(false)}
		>
			<View
				testID="swipe-row-content"
				onTouchStart={(event) => {
					startX.current = event.nativeEvent.pageX;
				}}
			>
				{children}
			</View>
		</ReanimatedSwipeable>
	);
}
```

- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Look in the simulator (Release build).** On a Board row: a short swipe right springs back and does nothing; a swipe right past half the row archives; a swipe left reveals the trailing buttons; a swipe that begins within 24pt of the left edge moves nothing and goes back to Hubs instead (the Board always sits on `Hubs`: `restoredStack`, `location.ts:286-287`); a tap anywhere right of the band opens the session. Tune `friction` and `overshootFriction` only if the feel needs it. If a swipe from the band still moves the row, fall back to covering the band: a transparent 24pt-wide sibling view absolutely positioned over the row's left edge, above the swipeable and outside its view tree, so the row never receives those touches (taps there open nothing, as with `hitSlop`); keep the start-x guard. Record which held, and why, in the PR description.
- [ ] **Step 6: Commit** (`feat(native): a swipeable row that leaves the left edge to the back gesture`).

### Task 12.5: Swipes on every Board row

**Files:**
- Modify: `mobile-native/src/board/BoardScreen.tsx` (swipes on every session row, the toast, `BoardStops`), `mobile-native/src/board/BoardRow.tsx` (`dimmed`, accessibility actions)
- Test: `mobile-native/src/board/BoardScreen.test.tsx` and `mobile-native/src/board/BoardRow.test.tsx`

**Interfaces:**
- Consumes: Tasks 12.1-12.4; `useBoardOrganization` (Task 10.5); `getNativeMutationRuntime` (`nativeMutationRuntime.ts:420`).
- Produces:
  - `BoardRow` gains `dimmed?: boolean`, `accessibilityActions?` and `onAccessibilityAction?` (passed to its pressable).
  - Inside `BoardScreen`, one `rowContext(item, archived): RowActionContext` and one `runRowAction(item, action)` that Task 12.7's menu reuses for Stop, Pin, Archive and Unarchive.

**Requirements (spec 7.3 and 14; rulings 16-21):**
1. **Every session row** (Live's bands, pinned categories, Projects, Test runs, Archived) sits in a `SwipeRow`. Its context is `{ connected: state === "ready" && activeProfile?.id === hubId, organizationReady: organization.ready, archived }`, where `archived` is the `ProjectTreeItem` session's `archived` (false elsewhere).
2. **Swipe actions** from `swipeActions(item, context)`: Archive or Unarchive (`archivebox`, fill `inkMid`), Stop (`stop.fill`, fill `inkHi`), Pin (`pin.fill`, fill `inkMid`). Leave out its `"more"` until Task 12.7 brings the menu More opens; until then the trailing side ends at Pin. The same actions reach VoiceOver through `swipeAccessibility`, spread on `BoardRow`.
3. **Archive and Unarchive:** `archiveSession(organization.actions, archiveTarget(row), archived)`. Confirmed: the toast "Archived" (or "Unarchived") with the action "Undo", which runs the opposite change the same way. Not confirmed: call `organization.actions.reconcile()` once and show no failure text; the row then shows wherever the hub has it. While `archivingSessionId(organization.state)` is the row's `session_id`, every copy of the row renders `dimmed` (opacity 0.5, `accessibilityState.busy`).
4. **Stop:** `stops.stop(client, row.ref)` with one `BoardStops(getNativeMutationRuntime, hubId)` per hub (`releaseAll()` when the client changes, `dispose()` on unmount). "stopped": the toast "Stopped" (spec 8.3). "notWorking": the toast "Nothing to stop: its turn had already ended." "unavailable": the toast "Couldn't stop “<title>”. Open it to stop it there."
5. **Pin:** `navigation.navigate("PinAssignment", { hubId, ref: row.ref, title: row.title })`.
6. **The toast** sits 10pt above the bottom toolbar (phase 3's `Toast`, one at a time).
7. **Offline** (ruling 21) comes from the context: the leading swipe reveals nothing and the trailing swipe reveals nothing (More arrives with Task 12.7).

- [ ] **Step 1: Write the failing tests** (part 1's `BoardScreen.test.tsx` harness; add `vi.mock("react-native-gesture-handler/ReanimatedSwipeable", …gestureHandlerModuleMock())`, and an `expo-sqlite` mock whose `openDatabaseSync` returns `openSqliteSyncDouble().port`, as `ConversationScreen.recovery.test.tsx` does; local rows use real session ids, such as `organizationTestUtils.ts`'s `SESSION_ID`, since `archiveTarget` checks their shape):
  - a working local row's swipeable has a leading Archive and trailing Stop and Pin; a finished one has no Stop; a row on paradise-park has no leading action;
  - a full leading swipe on a local row sends `evener/archive/set` `{ kind: "session", id, archived: true }`; the row renders dimmed while the answer is pending; once confirmed, the toast reads "Archived" with "Undo", and Undo sends `archived: false`;
  - Stop from the trailing swipe sends `thread/read` and then `turn/interrupt` through the real runtime, and the toast reads "Stopped";
  - Pin navigates to `PinAssignment` with `{ hubId, ref, title }`;
  - with the connection `"reconnecting"`, no row has a leading or trailing action;
  - no rendered text is "Refresh" or "Reconnect".
  - `BoardRow.test.tsx`: `dimmed` renders at opacity 0.5 with `accessibilityState.busy`; accessibility actions reach the pressable.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/board/BoardScreen.test.tsx src/board/BoardRow.test.tsx`
- [ ] **Step 3: Implement** to the requirements.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check` and `make test-native-bundle`. Look in the simulator against a local `evener-hub` with a few sessions (the demo hub answers no mutations, and its session ids aren't real ones): archive and undo, stop a working session, pin one, and stop the hub to see the swipes go quiet.
- [ ] **Step 5: Commit** (`feat(native): swipe actions on Board rows`).

Open PR 4a: "feat(native): swipe actions on Board rows (phase 2, PR 4a)". The description names each action's path (this plan's table), and the edge band's two layers and what the simulator showed.

---

## PR 4b: the long-press menu

### Task 12.6: The long-press menu, after a spike

**Files:**
- Create: `mobile-native/src/board/RowMenu.tsx` (`RowPreviewCard`, `RowMenuSheet`, and the long-press wrapper the spike keeps)
- Modify (only for the library the spike keeps): `mobile-native/package.json`, `mobile-native/package-lock.json` and `mobile-native/Podfile.lock`
- Test: `mobile-native/src/board/RowMenu.test.tsx`

**Interfaces:**
- Consumes: `ClassifiedRow`, `whyLine`, `stateWord` (part 1 Task 2); `StateMark` (part 1 Task 4); `RowAction` and `ROW_ACTION_LABELS` (Task 12.3).
- Produces:
  - `ROW_ACTION_SYMBOLS: Record<RowAction, SFSymbol>`: `pin.fill`, `circle` (Mark as read), `circle.fill` (Mark as unread), `stop.fill`, `power`, `archivebox` (both), `pencil`
  - `<RowPreviewCard item hostLabel onPress />`
  - `<RowMenuSheet item actions hostLabel visible onOpenSession onAction onClose />`
  - `<RowMenu item actions hostLabel onOpenSession onAction onOpenChange>{row}</RowMenu>`: the long-press presentation; with the sheet it opens `RowMenuSheet`, with a native menu the native menu. The trailing swipe's More always opens `RowMenuSheet`, since a native context menu can't be opened from code.

- [ ] **Step 1: Spike, in this order, and keep the first that passes all six checks.**
  1. `@expo/ui`'s SwiftUI `ContextMenu`: from `mobile-native`, with a real `node_modules` (`[ -L node_modules ]` prints nothing), `npx expo install @expo/ui` (SDK 57 pins ~57.0.16), then regenerate the lock with part 1 Task 4 Step 6's commands (the diff adds the `ExpoUI` pod and nothing else) and build Release. Wrap one Board row in `Host` + `ContextMenu`: `ContextMenu.Trigger` hosts the row through `RNHostView`, `ContextMenu.Preview` hosts `RowPreviewCard` through `RNHostView matchContents`, and `ContextMenu.Items` holds a `Button` per action (Shut down with `role="destructive"`).
  2. If it fails a check: revert that install and its lock change, then try `@react-native-menu/menu`'s `MenuView` with `shouldOpenOnLongPress` the same way (`npx expo install @react-native-menu/menu`, lock regenerated, Release build).
  3. If that fails too: revert it, and keep `RowMenuSheet` (no dependency).

  The checks, each in the simulator:
  1. Long-pressing a row in the Board's list opens the menu with `RowPreviewCard` above the actions.
  2. Tapping the preview card opens the session and closes the menu (spec 7.3: there is no "Open" item).
  3. The row's tap, its swipes and the list's scroll still work, and a long-press during a fling stops the list and opens nothing.
  4. VoiceOver reads the preview card's label and each action.
  5. Scrolling a 200-row Board mounts and unmounts rows without a crash or a menu left behind.
  6. Shut down reads as destructive.

  Record the outcome and the failing check of each rejected option in the PR description. Only the kept library is installed; with the sheet, neither.
- [ ] **Step 2: Write the failing tests** (render with `../renderNative.testkit`; mock `react-native` with `nativeModuleMock()` and `expo-symbols` as part 1's Task 4 does):
  - `RowPreviewCard` shows the title, the state word and reason (`whyLine`), and the project and host; its accessibility label is "Open <title>"; pressing it calls `onPress`.
  - `RowMenuSheet` passes `visible` to its `Modal` (`presentationStyle: "pageSheet"`), lists `ROW_ACTION_LABELS` in the order of `actions`, calls `onAction` with the pressed action, calls `onOpenSession` from the card, renders "Shut down" in `dangerInk` and every other label in `inkHi`, and calls `onClose` from its "Close" button and from `onRequestClose`.
  - `RowMenu`, with the sheet: a long press on the row (the wrapper's `onLongPress`, 500ms `delayLongPress`) opens the sheet and calls `onOpenChange(true)`; closing it calls `onOpenChange(false)`.
- [ ] **Step 3: Implement.**
  - `RowPreviewCard`: a pressable card on `surface` with a hairline `edge` border and 12pt radius: `StateMark` (still), the title (semibold 17/22, two lines), then the `WhyLine` (the word semibold in its hue, " · ", the reason in `inkHi`; for Finished, Idle and Shut down the state word alone in `inkMid`), then 13/18 `inkLow`: `folder` + the project, `server.rack` + `hostLabel(row.host_id)`. Task progress, subagent failures, model and effort, and the last message's excerpt join when S13, S3 and S1 reach the rows (spec 18).
  - `RowMenuSheet`: `Modal` (`presentationStyle="pageSheet"`, `animationType="slide"`, `onRequestClose={onClose}`), a header row with "Close" (`accentInk`, trailing), the card, then one 52pt pressable per action: its `ROW_ACTION_SYMBOLS` glyph at 18pt in `inkMid` and its label at 17pt.
  - `RowMenu`: with the sheet, a wrapper whose long press opens `RowMenuSheet`; with a native menu, the spike's wrapper, and its open and close report through `onOpenChange` (Task 13.3 holds the list while it's open).
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`, and `make test-native-bundle` if a library was added.
- [ ] **Step 5: Commit** (`feat(native): the Board row's long-press menu, with a preview card`).

### Task 12.7: The long-press menu on every Board row

**Files:**
- Modify: `mobile-native/src/board/BoardScreen.tsx` (menus and More on every session row)
- Test: `mobile-native/src/board/BoardScreen.test.tsx`

**Interfaces:**
- Consumes: Task 12.6; Task 12.5's `rowContext` and `runRowAction`; `shutDownSession`, `renameSession` and `rowMenuActions` (Task 12.3); `seenMarkers` (part 1 Task 3).

**Requirements (spec 7.3; rulings 18-21, 27):**
1. **Every session row** sits in a `RowMenu` inside its `SwipeRow`, with `rowMenuActions(item, rowContext(item, archived))`.
2. **More** (`ellipsis.circle`, fill `inkLow`) ends the trailing swipe, online or not, and opens `RowMenuSheet` for the row.
3. **Menu actions:** Pin, Stop, Archive and Unarchive run as the swipes do (`runRowAction`); Mark as read `seenMarkers(hubId).markSeen(row)`; Mark as unread `seenMarkers(hubId).markUnread(row.ref)`; Shut down asks first with `Alert.alert("Shut down “<title>”?", "The agent stops. Send it a message to resume it.", [{ text: "Cancel", style: "cancel" }, { text: "Shut down", style: "destructive", onPress }])`, then `shutDownSession(client, row.ref)`: the toast "Session shut down", or "Couldn't shut down “<title>”: <the hub's message>"; Rename opens `Alert.prompt("Rename session", undefined, [{ text: "Cancel", style: "cancel" }, { text: "Rename", onPress: (name) => … }], "plain-text", row.title)`, then `renameSession(client, row.ref, name)`: the toast "Renamed", or "Couldn't rename “<title>”: <the hub's message>". Off iOS, Rename isn't offered. The preview card opens the session the way a tap does (part 1 Task 7: mark it seen, then navigate).
4. **Offline** (ruling 21): the trailing swipe shows only More, and the menu keeps only Mark as read and Mark as unread.

- [ ] **Step 1: Write the failing tests** (Task 12.5's harness, with `Alert.prompt` added to the `react-native` mock):
  - a working local row's trailing swipe ends with More, which opens the sheet listing Pin to category…, Stop, Shut down, Archive and (with `rename: true`) Rename;
  - Shut down asks first; the destructive button sends `thread/shutdown { ref }` and the toast reads "Session shut down";
  - Rename sends `evener/thread/name/set` with the prompt's text;
  - Mark as read marks a finished row seen, and the row moves to Idle;
  - with the connection `"reconnecting"`, the trailing swipe offers only More, and the menu offers only the read marks;
  - pressing the menu's preview card opens the session.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/board/BoardScreen.test.tsx`
- [ ] **Step 3: Implement** to the requirements.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. Look in the simulator against a local `evener-hub`: long-press a row, open a session from its preview, shut one down, rename one, mark one read and unread, and stop the hub to see only More and the read marks.
- [ ] **Step 5: Commit** (`feat(native): the long-press menu on Board rows`).

Open PR 4b: "feat(native): the long-press menu on Board rows (phase 2, PR 4b)". The description names the menu spike's outcome and each rejected option's failing check, and that Copy link waits for a session deep link.

---

## PR 4c: select mode, and a list that holds still

### Task 13.1: When the list may change: the state machine and HeldOrder

Spec 7.3: "The list never reorders while a finger is on it or it is scrolling. Changes apply when the list settles." Part 2's review found a hole in each settle rule it tried. The rule here is a state machine over six states and nine events, enumerated as a table that the test walks row by row.

**States:**
- `idle`: nothing touches or moves the list. The only state in which it changes.
- `touching`: a finger is on the list and no drag has begun.
- `dragging`: the list follows a finger.
- `lifted`: the finger is up, and a drag or momentum may still begin. React Native never says "momentum will not start": `onMomentumScrollBegin` follows `onScrollEndDrag`, and the `onTouchCancel` the scroll view sends when it takes a touch over can arrive before `onScrollBeginDrag`. So `lifted` waits 100ms.
- `momentum`: the list glides after a fling.
- `appScrolling`: a scroll the app started (a chip, the summary's counts, a reveal) is animating.

**Events:** `touchStart` (`onTouchStart`), `touchEnd` (`onTouchEnd` or `onTouchCancel` with no finger left), `scrollBeginDrag`, `scrollEndDrag`, `momentumBegin`, `momentumEnd` (also sent when an app scroll's animation ends), `appScrollStart` (the Board, just before an animated scroll), `deadline` (the waiting state's time is up), `reset` (the Board left the screen or the app went to the background). A data change and a Reduce Motion change are not settle events: data applies at once in `idle` and is held otherwise (`HeldOrder`), and Reduce Motion only chooses whether rows animate to their places.

**Deadlines** (`SETTLE_DEADLINE_MS`): `lifted` 100ms; `momentum` 6000ms, past the longest iOS deceleration (about 5 seconds from the fastest fling), so a glide whose end event is lost can't hold the list forever; `appScrolling` 1000ms, since a scroll to the offset the list is already at sends no end event. `touching` and `dragging` have none: the finger decides. A deadline starts when its state is entered and is cancelled when the state is left.

**Transitions** (row: the state now; column: the event; cell: the next state):

| State | touchStart | touchEnd | scrollBeginDrag | scrollEndDrag | momentumBegin | momentumEnd | appScrollStart | deadline | reset |
|---|---|---|---|---|---|---|---|---|---|
| idle | touching | idle | dragging | idle | momentum | idle | appScrolling | idle | idle |
| touching | touching | lifted | dragging | touching | touching | touching | touching | touching | idle |
| dragging | dragging | dragging | dragging | lifted | momentum | dragging | dragging | dragging | idle |
| lifted | touching | lifted | dragging | lifted | momentum | idle | appScrolling | idle | idle |
| momentum | touching | momentum | dragging | momentum | momentum | idle | appScrolling | idle | idle |
| appScrolling | touching | appScrolling | dragging | appScrolling | appScrolling | idle | appScrolling | idle | idle |

Reading it: a finger down anywhere but a drag makes `touching` (a touch stops a glide or an app scroll). Only `touching` lifts into `lifted`; a drag lifts through `scrollEndDrag`. Momentum is ignored while a finger is down or the app is scrolling. `momentumEnd` settles a glide, an app scroll, or a `lifted` list that never saw the glide begin.

The list is **held** while the machine isn't `idle`, or while an interaction holds it: a row's swipe actions open, the long-press menu open, or select mode on (ruling 22). `HeldOrder` keeps the list as it was when the hold began: same order, same membership, each row with its freshest content. A row that leaves keeps its last content until the release, and a row that arrives waits for it. The release applies departures, arrivals and moves in one step.

**Files:**
- Create: `mobile-native/src/board/listSettle.ts` and `mobile-native/src/board/heldOrder.ts`
- Test: `mobile-native/src/board/listSettle.test.ts` and `mobile-native/src/board/heldOrder.test.ts`

**Interfaces:**
- Produces:
  - `type SettleState = "idle" | "touching" | "dragging" | "lifted" | "momentum" | "appScrolling"`
  - `type SettleEvent = "touchStart" | "touchEnd" | "scrollBeginDrag" | "scrollEndDrag" | "momentumBegin" | "momentumEnd" | "appScrollStart" | "deadline" | "reset"`
  - `SETTLE_DEADLINE_MS: Readonly<Partial<Record<SettleState, number>>>` and `nextSettleState(state: SettleState, event: SettleEvent): SettleState`
  - `interface HoldableItem { key: string; needsYou?: boolean }`
  - `class HeldOrder<T extends { key: string }>`: `held: boolean`, `hold(current: readonly T[]): void`, `order(next: readonly T[]): T[]`, `release(next: readonly T[]): T[]`
  - `enteredNeedsYou(before: readonly HoldableItem[] | null, after: readonly HoldableItem[]): Set<string>`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/board/listSettle.test.ts
import { describe, expect, it } from "vitest";
import { nextSettleState, SETTLE_DEADLINE_MS, type SettleEvent, type SettleState } from "./listSettle";

// Written from the plan's table, independently of the implementation.
const TABLE: [SettleState, SettleEvent, SettleState][] = [
	["idle", "touchStart", "touching"],
	["idle", "touchEnd", "idle"],
	["idle", "scrollBeginDrag", "dragging"],
	["idle", "scrollEndDrag", "idle"],
	["idle", "momentumBegin", "momentum"],
	["idle", "momentumEnd", "idle"],
	["idle", "appScrollStart", "appScrolling"],
	["idle", "deadline", "idle"],
	["idle", "reset", "idle"],
	["touching", "touchStart", "touching"],
	["touching", "touchEnd", "lifted"],
	["touching", "scrollBeginDrag", "dragging"],
	["touching", "scrollEndDrag", "touching"],
	["touching", "momentumBegin", "touching"],
	["touching", "momentumEnd", "touching"],
	["touching", "appScrollStart", "touching"],
	["touching", "deadline", "touching"],
	["touching", "reset", "idle"],
	["dragging", "touchStart", "dragging"],
	["dragging", "touchEnd", "dragging"],
	["dragging", "scrollBeginDrag", "dragging"],
	["dragging", "scrollEndDrag", "lifted"],
	["dragging", "momentumBegin", "momentum"],
	["dragging", "momentumEnd", "dragging"],
	["dragging", "appScrollStart", "dragging"],
	["dragging", "deadline", "dragging"],
	["dragging", "reset", "idle"],
	["lifted", "touchStart", "touching"],
	["lifted", "touchEnd", "lifted"],
	["lifted", "scrollBeginDrag", "dragging"],
	["lifted", "scrollEndDrag", "lifted"],
	["lifted", "momentumBegin", "momentum"],
	["lifted", "momentumEnd", "idle"],
	["lifted", "appScrollStart", "appScrolling"],
	["lifted", "deadline", "idle"],
	["lifted", "reset", "idle"],
	["momentum", "touchStart", "touching"],
	["momentum", "touchEnd", "momentum"],
	["momentum", "scrollBeginDrag", "dragging"],
	["momentum", "scrollEndDrag", "momentum"],
	["momentum", "momentumBegin", "momentum"],
	["momentum", "momentumEnd", "idle"],
	["momentum", "appScrollStart", "appScrolling"],
	["momentum", "deadline", "idle"],
	["momentum", "reset", "idle"],
	["appScrolling", "touchStart", "touching"],
	["appScrolling", "touchEnd", "appScrolling"],
	["appScrolling", "scrollBeginDrag", "dragging"],
	["appScrolling", "scrollEndDrag", "appScrolling"],
	["appScrolling", "momentumBegin", "appScrolling"],
	["appScrolling", "momentumEnd", "idle"],
	["appScrolling", "appScrollStart", "appScrolling"],
	["appScrolling", "deadline", "idle"],
	["appScrolling", "reset", "idle"],
];

describe("the settle machine (spec 7.3)", () => {
	it.each(TABLE)("%s + %s → %s", (state, event, next) => {
		expect(nextSettleState(state, event)).toBe(next);
	});

	it("the table covers every state and event once", () => {
		const pairs = new Set(TABLE.map(([state, event]) => `${state}:${event}`));
		expect(pairs.size).toBe(6 * 9);
		expect(TABLE).toHaveLength(6 * 9);
	});

	it("gives only the waiting states a deadline", () => {
		expect(SETTLE_DEADLINE_MS).toEqual({ lifted: 100, momentum: 6000, appScrolling: 1000 });
	});

	it("holds a fling from the first touch to the glide's end, even when the touch is cancelled before the drag begins", () => {
		const events: SettleEvent[] = ["touchStart", "touchEnd", "scrollBeginDrag", "scrollEndDrag", "momentumBegin", "momentumEnd"];
		const trail: SettleState[] = [];
		let state: SettleState = "idle";
		for (const event of events) {
			state = nextSettleState(state, event);
			trail.push(state);
		}
		expect(trail).toEqual(["touching", "lifted", "dragging", "lifted", "momentum", "idle"]);
	});
});
```

```ts
// mobile-native/src/board/heldOrder.test.ts
import { describe, expect, it } from "vitest";
import { enteredNeedsYou, HeldOrder } from "./heldOrder";

const item = (key: string, text = key, needsYou?: boolean) => ({ key, text, needsYou });
type Item = ReturnType<typeof item>;

describe("HeldOrder (spec 7.3, ruling 22)", () => {
	it("passes lists through while nothing holds them", () => {
		const order = new HeldOrder<Item>();
		expect(order.order([item("b"), item("a")])).toEqual([item("b"), item("a")]);
		expect(order.held).toBe(false);
	});

	it("keeps the held order and membership, with each row's freshest content", () => {
		const order = new HeldOrder<Item>();
		order.hold([item("a"), item("b"), item("c")]);
		expect(order.order([item("c", "c2"), item("a", "a2"), item("d")])).toEqual([
			item("a", "a2"),
			item("b"),
			item("c", "c2"),
		]);
	});

	it("keeps a departed row's last content until the release", () => {
		const order = new HeldOrder<Item>();
		order.hold([item("a"), item("b")]);
		expect(order.order([item("a"), item("b", "b2")])).toEqual([item("a"), item("b", "b2")]);
		expect(order.order([item("a")])).toEqual([item("a"), item("b", "b2")]);
	});

	it("applies departures, arrivals and moves in one step on release", () => {
		const order = new HeldOrder<Item>();
		order.hold([item("a"), item("b")]);
		order.order([item("d"), item("a")]);
		expect(order.release([item("d"), item("a")])).toEqual([item("d"), item("a")]);
		expect(order.held).toBe(false);
		expect(order.order([item("a"), item("d")])).toEqual([item("a"), item("d")]);
	});

	it("keeps the order of the first hold when asked to hold again", () => {
		const order = new HeldOrder<Item>();
		order.hold([item("a"), item("b")]);
		order.hold([item("b"), item("a")]);
		expect(order.order([item("b"), item("a")])).toEqual([item("a"), item("b")]);
	});
});

describe("rows entering Needs you (spec 7.3's wash)", () => {
	it("names rows that moved into Needs you or arrived there", () => {
		const before = [item("a", "a", false), item("b", "b", true), item("s")];
		const after = [item("a", "a", true), item("b", "b", true), item("c", "c", true), item("s")];
		expect([...enteredNeedsYou(before, after)].sort()).toEqual(["a", "c"]);
	});

	it("names none on the first list, or when the list before had no Live rows", () => {
		const after = [item("a", "a", true)];
		expect(enteredNeedsYou(null, after).size).toBe(0);
		expect(enteredNeedsYou([item("header")], after).size).toBe(0);
	});
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/listSettle.test.ts src/board/heldOrder.test.ts`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/board/listSettle.ts
// When the Board's list may change (spec 7.3: it "never reorders while a
// finger is on it or it is scrolling"). A state machine over the list's touch
// and scroll events; the list is held in every state but idle. React Native
// never says "momentum will not start", and the touch cancel a scroll view
// sends as it takes a touch over can come before its drag begins, so a lifted
// finger waits a moment before the list settles. A glide or an app-driven
// scroll whose end event never arrives is let go by a deadline.

export type SettleState = "idle" | "touching" | "dragging" | "lifted" | "momentum" | "appScrolling";
export type SettleEvent =
	| "touchStart"
	| "touchEnd"
	| "scrollBeginDrag"
	| "scrollEndDrag"
	| "momentumBegin"
	| "momentumEnd"
	| "appScrollStart"
	| "deadline"
	| "reset";

/** How long each waiting state lasts before it settles on its own. lifted:
 * a drag or momentum that begins within 100ms keeps the list held. momentum:
 * past the longest iOS deceleration (about 5 seconds from the fastest
 * fling). appScrolling: a scroll to the offset the list already has sends no
 * end event. */
export const SETTLE_DEADLINE_MS: Readonly<Partial<Record<SettleState, number>>> = {
	lifted: 100,
	momentum: 6000,
	appScrolling: 1000,
};

export function nextSettleState(state: SettleState, event: SettleEvent): SettleState {
	switch (event) {
		case "reset":
			return "idle";
		case "touchStart":
			// A finger stops a glide or an app scroll; a second finger during a
			// drag changes nothing.
			return state === "dragging" ? "dragging" : "touching";
		case "touchEnd":
			// A drag ends with its own event.
			return state === "touching" ? "lifted" : state;
		case "scrollBeginDrag":
			return "dragging";
		case "scrollEndDrag":
			return state === "dragging" ? "lifted" : state;
		case "momentumBegin":
			// Ignored while a finger is down or the app is scrolling.
			return state === "touching" || state === "appScrolling" ? state : "momentum";
		case "momentumEnd":
			return state === "momentum" || state === "appScrolling" || state === "lifted" ? "idle" : state;
		case "appScrollStart":
			return state === "touching" || state === "dragging" ? state : "appScrolling";
		case "deadline":
			return SETTLE_DEADLINE_MS[state] === undefined ? state : "idle";
	}
}
```

```ts
// mobile-native/src/board/heldOrder.ts
// What the Board's list shows while it is held (spec 7.3): the order and
// membership it had when the hold began, each row with its freshest content.
// A row that leaves keeps its last content, and a row that arrives waits,
// until the release applies everything in one step (ruling 22).

/** A list item: `key` is unique in the list and stable across reads. */
export interface HoldableItem {
	key: string;
	/** Set on Live's rows only: whether the row sits in the Needs you band. */
	needsYou?: boolean;
}

export class HeldOrder<T extends { key: string }> {
	#frame: readonly T[] | null = null;
	#latest = new Map<string, T>();

	get held(): boolean {
		return this.#frame !== null;
	}

	/** Freezes `current`, the list as shown, as the order to keep. */
	hold(current: readonly T[]): void {
		if (this.#frame) return;
		this.#frame = [...current];
		this.#latest = new Map(current.map((item) => [item.key, item]));
	}

	/** While held: the held order, with each row's freshest content, departed
	 * rows kept and new rows withheld. Otherwise `next` itself. */
	order(next: readonly T[]): T[] {
		const frame = this.#frame;
		if (!frame) return [...next];
		for (const item of next) if (this.#latest.has(item.key)) this.#latest.set(item.key, item);
		return frame.map((item) => this.#latest.get(item.key) ?? item);
	}

	/** Ends the hold and returns `next`: departures, arrivals and moves all
	 * apply at once. */
	release(next: readonly T[]): T[] {
		this.#frame = null;
		this.#latest = new Map();
		return [...next];
	}
}

/** The Live rows that entered Needs you between two lists the Board applied
 * (spec 7.3's amber wash): none on the first list shown, or while the list
 * before held no Live rows yet. */
export function enteredNeedsYou(before: readonly HoldableItem[] | null, after: readonly HoldableItem[]): Set<string> {
	const entered = new Set<string>();
	if (!before || !before.some((item) => item.needsYou !== undefined)) return entered;
	const was = new Map(before.map((item) => [item.key, item.needsYou === true]));
	for (const item of after) if (item.needsYou === true && was.get(item.key) !== true) entered.add(item.key);
	return entered;
}
```

- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): when the Board's list may change, as a state machine`).

### Task 13.2: The settled list, and Reduce Motion read live

**Files:**
- Create: `mobile-native/src/board/settledList.ts`, `mobile-native/src/board/useSettledList.ts` and `mobile-native/src/reduceMotion.ts`
- Test: `mobile-native/src/board/settledList.test.ts` and `mobile-native/src/reduceMotion.test.tsx`

**Interfaces:**
- Consumes: Task 13.1.
- Produces:
  - `WASH_MS = 1200`
  - `interface SettledSnapshot<T> { display: readonly T[]; held: boolean; washed: ReadonlySet<string>; washToken: number }`
  - `interface SettleTimers { set(run: () => void, ms: number): unknown; clear(handle: unknown): void }`
  - `class SettledList<T extends HoldableItem>`: constructor `(timers?: SettleTimers)`; `getSnapshot()`, `subscribe(listener)`, `state: SettleState`, `setItems(items: readonly T[] | null)`, `send(event: SettleEvent)`, `setInteraction(id: string, active: boolean)`, `dispose()`
  - `listScrollHandlers(send: (event: SettleEvent) => void)`: `onTouchStart`, `onTouchEnd`, `onTouchCancel`, `onScrollBeginDrag`, `onScrollEndDrag`, `onMomentumScrollBegin`, `onMomentumScrollEnd`, spread on the list
  - `useSettledList<T extends HoldableItem>(items: readonly T[] | null): { list: SettledList<T>; snapshot: SettledSnapshot<T> }`
  - `useReduceMotion(): boolean`

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/board/settledList.test.ts
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { listScrollHandlers, SettledList, WASH_MS } from "./settledList";

type Item = { key: string; text: string; needsYou?: boolean };
const item = (key: string, text = key, needsYou?: boolean): Item => ({ key, text, needsYou });
const keys = (list: SettledList<Item>) => list.getSnapshot().display.map((entry) => entry.key);
const texts = (list: SettledList<Item>) => list.getSnapshot().display.map((entry) => entry.text);
function loaded(...entries: Item[]) {
	const list = new SettledList<Item>();
	list.setItems(entries);
	return list;
}
beforeEach(() => {
	vi.useFakeTimers();
});
afterEach(() => {
	vi.useRealTimers();
});

it("applies a change at once while nothing holds the list", () => {
	const list = loaded(item("a"), item("b"));
	list.setItems([item("b"), item("a"), item("c")]);
	expect(keys(list)).toEqual(["b", "a", "c"]);
	expect(list.getSnapshot().held).toBe(false);
});

it("under a finger keeps order and membership with fresh content, and applies it all 100ms after the finger lifts", () => {
	const list = loaded(item("a"), item("b"), item("c"));
	list.send("touchStart");
	list.setItems([item("c", "c2"), item("a"), item("d")]);
	expect(keys(list)).toEqual(["a", "b", "c"]);
	expect(texts(list)).toEqual(["a", "b", "c2"]);
	list.send("touchEnd");
	vi.advanceTimersByTime(99);
	expect(keys(list)).toEqual(["a", "b", "c"]);
	vi.advanceTimersByTime(1);
	expect(keys(list)).toEqual(["c", "a", "d"]);
	expect(list.getSnapshot().held).toBe(false);
});

it("holds through a drag, the moment before momentum and the glide, and applies when the glide ends", () => {
	const list = loaded(item("a"), item("b"));
	list.send("touchStart");
	list.send("scrollBeginDrag");
	list.setItems([item("b"), item("a")]);
	list.send("scrollEndDrag");
	vi.advanceTimersByTime(50);
	list.send("momentumBegin");
	vi.advanceTimersByTime(3000);
	expect(keys(list)).toEqual(["a", "b"]);
	list.send("momentumEnd");
	expect(keys(list)).toEqual(["b", "a"]);
});

it("never fires the lifted deadline once momentum has begun", () => {
	const list = loaded(item("a"), item("b"));
	list.send("scrollBeginDrag");
	list.send("scrollEndDrag");
	list.send("momentumBegin");
	list.setItems([item("b"), item("a")]);
	vi.advanceTimersByTime(150);
	expect(list.state).toBe("momentum");
	expect(keys(list)).toEqual(["a", "b"]);
});

it("applies 100ms after a drag that ends without momentum", () => {
	const list = loaded(item("a"), item("b"));
	list.send("scrollBeginDrag");
	list.setItems([item("b"), item("a")]);
	list.send("scrollEndDrag");
	vi.advanceTimersByTime(99);
	expect(keys(list)).toEqual(["a", "b"]);
	vi.advanceTimersByTime(1);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("lets no change through when a touch is cancelled just before its drag begins", () => {
	const list = loaded(item("a"), item("b"));
	const handlers = listScrollHandlers((event) => list.send(event));
	handlers.onTouchStart();
	handlers.onTouchCancel({ nativeEvent: { touches: [] } });
	list.setItems([item("b"), item("a")]);
	vi.advanceTimersByTime(20);
	handlers.onScrollBeginDrag();
	vi.advanceTimersByTime(500);
	expect(keys(list)).toEqual(["a", "b"]);
	handlers.onScrollEndDrag();
	vi.advanceTimersByTime(100);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("keeps holding while a finger is still down", () => {
	const list = loaded(item("a"), item("b"));
	const handlers = listScrollHandlers((event) => list.send(event));
	handlers.onTouchStart();
	handlers.onTouchStart();
	handlers.onTouchEnd({ nativeEvent: { touches: [{}] } });
	list.setItems([item("b"), item("a")]);
	vi.advanceTimersByTime(500);
	expect(keys(list)).toEqual(["a", "b"]);
	handlers.onTouchEnd({ nativeEvent: { touches: [] } });
	vi.advanceTimersByTime(100);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("lets go of a glide whose end never arrives after six seconds", () => {
	const list = loaded(item("a"), item("b"));
	list.send("scrollBeginDrag");
	list.send("scrollEndDrag");
	list.send("momentumBegin");
	list.setItems([item("b"), item("a")]);
	vi.advanceTimersByTime(5999);
	expect(keys(list)).toEqual(["a", "b"]);
	vi.advanceTimersByTime(1);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("holds an app scroll until it ends, or for a second at most", () => {
	const list = loaded(item("a"), item("b"));
	list.send("appScrollStart");
	list.setItems([item("b"), item("a")]);
	list.send("momentumEnd");
	expect(keys(list)).toEqual(["b", "a"]);
	list.send("appScrollStart");
	list.setItems([item("a"), item("b")]);
	vi.advanceTimersByTime(999);
	expect(keys(list)).toEqual(["b", "a"]);
	vi.advanceTimersByTime(1);
	expect(keys(list)).toEqual(["a", "b"]);
});

it("holds while a row's actions are open, past the finger lifting, until they close", () => {
	const list = loaded(item("a"), item("b"));
	list.send("touchStart");
	list.setInteraction("swipe:a", true);
	list.send("touchEnd");
	vi.advanceTimersByTime(500);
	list.setItems([item("b"), item("a")]);
	expect(keys(list)).toEqual(["a", "b"]);
	list.setInteraction("swipe:a", false);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("reset releases at once and forgets open interactions", () => {
	const list = loaded(item("a"), item("b"));
	list.send("touchStart");
	list.setInteraction("menu", true);
	list.setItems([item("b"), item("a")]);
	list.send("reset");
	expect(keys(list)).toEqual(["b", "a"]);
	expect(list.getSnapshot().held).toBe(false);
	list.setItems([item("a"), item("b")]);
	expect(keys(list)).toEqual(["a", "b"]);
});

it("washes rows that entered Needs you, never on the first list, and clears the wash after 1.2 seconds", () => {
	const list = loaded(item("a", "a", false), item("b", "b", true));
	expect(list.getSnapshot().washed.size).toBe(0);
	list.setItems([item("a", "a", true), item("b", "b", true), item("c", "c", true)]);
	expect([...list.getSnapshot().washed].sort()).toEqual(["a", "c"]);
	const token = list.getSnapshot().washToken;
	vi.advanceTimersByTime(WASH_MS);
	expect(list.getSnapshot().washed.size).toBe(0);
	expect(list.getSnapshot().washToken).toBe(token);
});

it("washes a row that entered Needs you while held only when the list settles, in its new place", () => {
	const list = loaded(item("w", "w", false), item("n", "n", true));
	list.send("touchStart");
	list.setItems([item("n", "n", true), item("w", "w2", true)]);
	expect(list.getSnapshot().washed.size).toBe(0);
	list.send("touchEnd");
	vi.advanceTimersByTime(100);
	expect(keys(list)).toEqual(["n", "w"]);
	expect([...list.getSnapshot().washed]).toEqual(["w"]);
});
```

```tsx
// mobile-native/src/reduceMotion.test.tsx
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { useReduceMotion } from "./reduceMotion";
import { renderHook } from "./renderNative.testkit";

const accessibility = vi.hoisted(() => ({
	listener: null as ((value: boolean) => void) | null,
	removed: 0,
}));
vi.mock("react-native", () => ({
	AccessibilityInfo: {
		isReduceMotionEnabled: () => Promise.resolve(true),
		addEventListener: (_event: string, listener: (value: boolean) => void) => {
			accessibility.listener = listener;
			return {
				remove: () => {
					accessibility.removed += 1;
				},
			};
		},
	},
}));

it("reads Reduce Motion at mount and follows it when it changes", async () => {
	const hook = renderHook(() => useReduceMotion());
	await act(async () => {});
	expect(hook.result.current).toBe(true);
	act(() => accessibility.listener?.(false));
	expect(hook.result.current).toBe(false);
	hook.unmount();
	expect(accessibility.removed).toBe(1);
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd mobile-native && npx vitest run src/board/settledList.test.ts src/reduceMotion.test.tsx`
Expected: FAIL: the modules don't exist.

- [ ] **Step 3: Implement**

```ts
// mobile-native/src/board/settledList.ts
// The Board's list as shown (spec 7.3): held while the settle machine
// (listSettle.ts) isn't idle, or while an interaction holds it (a row's open
// swipe, the row menu, select mode; ruling 22). Every change applies in one
// step when it settles, and the rows that entered Needs you then wash amber.
import { enteredNeedsYou, HeldOrder, type HoldableItem } from "./heldOrder";
import {
	nextSettleState,
	SETTLE_DEADLINE_MS,
	type SettleEvent,
	type SettleState,
} from "./listSettle";

/** How long a row that entered Needs you stays washed (spec 7.3: 1.2s). */
export const WASH_MS = 1200;

export interface SettledSnapshot<T> {
	/** What the list shows. */
	display: readonly T[];
	held: boolean;
	/** The rows that entered Needs you at the last change applied, for WASH_MS. */
	washed: ReadonlySet<string>;
	/** Moves on whenever `washed` gains rows, so a row can wash again. */
	washToken: number;
}

export interface SettleTimers {
	set(run: () => void, ms: number): unknown;
	clear(handle: unknown): void;
}
const systemTimers: SettleTimers = {
	set: (run, ms) => setTimeout(run, ms),
	clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

export class SettledList<T extends HoldableItem> {
	#state: SettleState = "idle";
	#deadline: unknown = null;
	readonly #interactions = new Set<string>();
	#latest: readonly T[] | null = null;
	#applied: readonly T[] | null = null;
	readonly #order = new HeldOrder<T>();
	#washTimer: unknown = null;
	#snapshot: SettledSnapshot<T> = { display: [], held: false, washed: new Set(), washToken: 0 };
	readonly #listeners = new Set<() => void>();
	#disposed = false;
	readonly #timers: SettleTimers;

	constructor(timers: SettleTimers = systemTimers) {
		this.#timers = timers;
	}

	getSnapshot = (): SettledSnapshot<T> => this.#snapshot;

	subscribe = (listener: () => void): (() => void) => {
		this.#listeners.add(listener);
		return () => this.#listeners.delete(listener);
	};

	get state(): SettleState {
		return this.#state;
	}

	/** The list the Board would show now, or null before its first load. */
	setItems(items: readonly T[] | null): void {
		this.#latest = items;
		if (items === null) return;
		if (this.#held()) this.#publish({ display: this.#order.order(items) });
		else this.#apply(items);
	}

	/** An event from the list (listScrollHandlers), "appScrollStart" before an
	 * animated scroll the Board starts, or "reset" when it leaves the screen. */
	send(event: SettleEvent): void {
		const wasHeld = this.#held();
		if (event === "reset") this.#interactions.clear();
		const next = nextSettleState(this.#state, event);
		if (next !== this.#state) {
			this.#timers.clear(this.#deadline);
			this.#deadline = null;
			this.#state = next;
			const ms = SETTLE_DEADLINE_MS[next];
			if (ms !== undefined)
				this.#deadline = this.#timers.set(() => {
					this.#deadline = null;
					this.send("deadline");
				}, ms);
		}
		this.#settle(wasHeld);
	}

	/** An interaction that holds the list opening (true) or closing (false). */
	setInteraction(id: string, active: boolean): void {
		const wasHeld = this.#held();
		if (active) this.#interactions.add(id);
		else this.#interactions.delete(id);
		this.#settle(wasHeld);
	}

	dispose(): void {
		this.#disposed = true;
		this.#timers.clear(this.#deadline);
		this.#timers.clear(this.#washTimer);
		this.#listeners.clear();
	}

	#held(): boolean {
		return this.#state !== "idle" || this.#interactions.size > 0;
	}

	#settle(wasHeld: boolean): void {
		const held = this.#held();
		if (held === wasHeld) return;
		if (held) {
			this.#order.hold(this.#snapshot.display);
			this.#publish({ held: true });
			return;
		}
		if (this.#latest === null) {
			this.#order.release([]);
			this.#publish({ held: false });
			return;
		}
		this.#apply(this.#order.release(this.#latest));
	}

	#apply(items: readonly T[]): void {
		const entered = enteredNeedsYou(this.#applied, items);
		this.#applied = items;
		if (entered.size === 0) {
			this.#publish({ display: items, held: false });
			return;
		}
		this.#timers.clear(this.#washTimer);
		this.#washTimer = this.#timers.set(() => {
			this.#washTimer = null;
			this.#publish({ washed: new Set() });
		}, WASH_MS);
		this.#publish({ display: items, held: false, washed: entered, washToken: this.#snapshot.washToken + 1 });
	}

	#publish(change: Partial<SettledSnapshot<T>>): void {
		if (this.#disposed) return;
		this.#snapshot = { ...this.#snapshot, ...change };
		for (const listener of [...this.#listeners]) listener();
	}
}

/** The list's touch and scroll events as settle events. A touch ends when its
 * last finger lifts, or when the scroll view takes it over (onTouchCancel). */
export function listScrollHandlers(send: (event: SettleEvent) => void) {
	const lastFinger = (event: { nativeEvent: { touches: readonly unknown[] } }) => {
		if (event.nativeEvent.touches.length === 0) send("touchEnd");
	};
	return {
		onTouchStart: () => send("touchStart"),
		onTouchEnd: lastFinger,
		onTouchCancel: lastFinger,
		onScrollBeginDrag: () => send("scrollBeginDrag"),
		onScrollEndDrag: () => send("scrollEndDrag"),
		onMomentumScrollBegin: () => send("momentumBegin"),
		onMomentumScrollEnd: () => send("momentumEnd"),
	};
}
```

```ts
// mobile-native/src/board/useSettledList.ts
import { useLayoutEffect, useState, useSyncExternalStore } from "react";
import type { HoldableItem } from "./heldOrder";
import { type SettledSnapshot, SettledList } from "./settledList";

/** Binds a SettledList to the Board's render: the items it would show (memoized
 * by the caller, null before the first load) go in; what to show comes out.
 * A layout effect hands them over, so the re-render it causes lands before the
 * frame is shown and the list never flashes the previous items (or none).
 * Unmounting drops the subscription; a deadline that fires later publishes to
 * nobody, so there is no dispose to undo when React remounts in development. */
export function useSettledList<T extends HoldableItem>(
	items: readonly T[] | null,
): { list: SettledList<T>; snapshot: SettledSnapshot<T> } {
	const [list] = useState(() => new SettledList<T>());
	useLayoutEffect(() => {
		list.setItems(items);
	}, [list, items]);
	const snapshot = useSyncExternalStore(list.subscribe, list.getSnapshot);
	return { list, snapshot };
}
```

```ts
// mobile-native/src/reduceMotion.ts
import { useEffect, useState } from "react";
import { AccessibilityInfo } from "react-native";

/** Whether Reduce Motion is on, following the setting as it changes
 * (reanimated's useReducedMotion reads it once, at launch). */
export function useReduceMotion(): boolean {
	const [reduce, setReduce] = useState(false);
	useEffect(() => {
		let live = true;
		void AccessibilityInfo.isReduceMotionEnabled().then((value) => {
			if (live) setReduce(value);
		});
		const subscription = AccessibilityInfo.addEventListener("reduceMotionChanged", setReduce);
		return () => {
			live = false;
			subscription.remove();
		};
	}, []);
	return reduce;
}
```

- [ ] **Step 4: Run them and watch them pass**, then `npm run check`.
- [ ] **Step 5: Commit** (`feat(native): the Board's list holds still and applies changes when it settles`).

### Task 13.3: The Board holds still, moves with a spring, and washes what needs you

**Files:**
- Create: `mobile-native/src/board/boardMotion.ts`
- Modify: `mobile-native/src/board/BoardScreen.tsx` (the list), `mobile-native/src/board/BoardRow.tsx` (the wash), `mobile-native/src/renderNative.testkit.tsx` (`reanimatedModuleMock()` and `animatedListProps`)
- Test: `mobile-native/src/board/BoardScreen.test.tsx` and `mobile-native/src/board/BoardRow.test.tsx`

**Interfaces:**
- Consumes: Task 13.2; `SwipeRow`'s `onActiveChange` and `RowMenu`'s `onOpenChange` (Tasks 12.4, 12.6).
- Produces:
  - `ROW_MOVE`: `LinearTransition.springify().duration(250).dampingRatio(1)` (spec 16.6: rows move with a 250ms spring; critically damped, so nothing bounces)
  - `BoardRow` gains `wash?: number`
  - From the testkit: `reanimatedModuleMock()` and `animatedListProps: { current: Record<string, unknown> | null }`

**Requirements (spec 7.3, 16.6; rulings 22-23):**
1. **One keyed list.** Every item in the Board's list has a `key` unique in the list and stable across reads; Live's session rows are keyed by ref alone (`live:${ref}`), so a row that changes band moves instead of leaving and arriving, and they carry `needsYou` (true in the Needs you band, false in the other Live bands). If part 1's Task 7 rendered the Board with a `SectionList`, flatten it into one keyed item list first: layout transitions and `HeldOrder` both need one.
2. **The list shows `useSettledList(loaded ? items : null).snapshot.display`** (the skeleton and empty states stay part 1's). `items` is memoized, so an unchanged Board doesn't re-apply.
3. **The list** is `Animated.FlatList` from `react-native-reanimated`, with `keyExtractor={(item) => item.key}`, `itemLayoutAnimation={reduceMotion ? undefined : ROW_MOVE}` (`useReduceMotion()`), and `listScrollHandlers((event) => list.send(event))` spread on it (memoized).
4. **Interactions hold it:** each `SwipeRow`'s `onActiveChange` calls `list.setInteraction("swipe:" + item.key, active)`; `RowMenu`'s and `RowMenuSheet`'s open state through `list.setInteraction("menu", open)`; select mode through `list.setInteraction("select", selecting)` (Task 13.4).
5. **App scrolls hold it:** every animated scroll the Board starts (a section chip, a count in the Live summary, `revealProject`, and phase 6's board jump when it lands) calls `list.send("appScrollStart")` just before `scrollToIndex` or `scrollToOffset`. With Reduce Motion they scroll without animation and send nothing.
6. **Leaving releases it:** `list.send("reset")` in the focus effect's cleanup (the Board blurs) and when `AppState` changes to `"background"` or `"inactive"`.
7. **The wash:** each session row gets `wash={snapshot.washed.has(item.key) ? snapshot.washToken : 0}`. When `wash` changes to a non-zero token, `BoardRow` shows an `Animated.View` behind its content (`StyleSheet.absoluteFill`, `pointerEvents="none"`, `backgroundColor: palette.attentionBg`) whose opacity is set to 1 and animated to 0 with `withTiming(0, { duration: WASH_MS })`. Reduce Motion keeps the wash (ruling 23).

- [ ] **Step 1: Add the reanimated mock to the testkit**

```tsx
/** The props the mocked Animated.FlatList last rendered with. */
export const animatedListProps: { current: Record<string, unknown> | null } = { current: null };

/** react-native-reanimated for vitest: Animated.FlatList is the FlatList stub
 * above (recording its props), Animated.View a host element, shared values
 * plain objects, withTiming its target, and LinearTransition a builder chain. */
export function reanimatedModuleMock() {
	const { FlatList } = nativeModuleMock();
	const AnimatedFlatList = (props: Record<string, unknown>) => {
		animatedListProps.current = props;
		return createElement(FlatList as (props: Record<string, unknown>) => ReactNode, props);
	};
	const transition: Record<string, () => unknown> = {};
	for (const step of ["springify", "duration", "dampingRatio"]) transition[step] = () => transition;
	return {
		__esModule: true,
		default: { FlatList: AnimatedFlatList, View: "Animated.View" },
		LinearTransition: transition,
		useSharedValue: (value: unknown) => ({ value }),
		useAnimatedStyle: (build: () => unknown) => build(),
		withTiming: (value: unknown) => value,
	};
}
```

- [ ] **Step 2: Write the failing tests** (`BoardScreen.test.tsx`, with `vi.mock("react-native-reanimated", …reanimatedModuleMock())`, the gesture mock, and `AccessibilityInfo` in the `react-native` mock; drive the list through `animatedListProps.current`'s handlers and the fleet through navigation invalidations):
  - a working row that turns into a question while a finger is on the list (`onTouchStart` called) keeps its place and shows its new mark and "Question"; 100ms after `onTouchEnd` with no touches left it sits in Needs you with a non-zero `wash`;
  - a change during a fling (`onScrollBeginDrag`, `onScrollEndDrag`, `onMomentumScrollBegin`) waits for `onMomentumScrollEnd`;
  - tapping the Live chip sends an app scroll: a change arriving before the list's `onMomentumScrollEnd` waits for it;
  - with a row's swipe open (its swipeable's `onSwipeableOpenStartDrag`), a change waits until `onSwipeableClose`;
  - blurring the Board applies a held change at once;
  - `itemLayoutAnimation` is set, and is undefined while `AccessibilityInfo.isReduceMotionEnabled()` resolves true;
  - `BoardRow.test.tsx`: `wash={2}` renders an `Animated.View` filled `attentionBg`; `wash={0}` renders none.
- [ ] **Step 3: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/board/BoardScreen.test.tsx src/board/BoardRow.test.tsx`
- [ ] **Step 4: Implement** to the requirements. `boardMotion.ts`:

```ts
// mobile-native/src/board/boardMotion.ts
import { LinearTransition } from "react-native-reanimated";

/** Rows move to their new places with a 250ms spring (spec 16.6), critically
 * damped so nothing bounces. BoardScreen drops it under Reduce Motion. */
export const ROW_MOVE = LinearTransition.springify().duration(250).dampingRatio(1);
```

- [ ] **Step 5: Run them and watch them pass**, then `npm run check` and `make test-native-bundle`. In the simulator against the demo hub, with a staged change arriving (phase 6's demo steps if they've landed, otherwise a second phone or the web moving a session): hold a finger on the list and watch nothing move until you lift; fling and watch the glide finish before rows move; watch a row that starts asking glide into Needs you with the amber wash; turn on Reduce Motion and watch rows jump, still washed.
- [ ] **Step 6: Commit** (`feat(native): the Board holds still under your finger and moves rows with a spring`).

### Task 13.4: Select mode

**Files:**
- Create: `mobile-native/src/board/selection.ts` (pure) and `mobile-native/src/board/SelectBar.tsx`
- Modify: `mobile-native/src/board/BoardScreen.tsx`, `mobile-native/src/board/BoardToolbar.tsx` (the leading Select) and `mobile-native/src/board/BoardRow.tsx` (`selected`)
- Test: `mobile-native/src/board/selection.test.ts`, `mobile-native/src/board/SelectBar.test.tsx` and `mobile-native/src/board/BoardScreen.test.tsx`

**Interfaces:**
- Consumes: `archiveTarget`, `isTopLevel`, `archiveSession`, `pinSession` (Task 12.3); `useBoardOrganization` (Task 10.5); `SettledList.setInteraction` (Task 13.2); `seenMarkers` (part 1).
- Produces:
  - `interface SelectedRow { item: ClassifiedRow; archived: boolean }` and `interface SelectionActions { archive: NavigationSessionSummary[]; pin: NavigationSessionSummary[]; markRead: NavigationSessionSummary[] }`
  - `selectionActions(selected: readonly SelectedRow[], context: { connected: boolean; organizationReady: boolean }): SelectionActions`
  - `toggleSelected(selected: ReadonlySet<string>, ref: string): Set<string>`
  - `<SelectBar counts: { archive: number; pin: number; markRead: number } onDone onArchive onPin onMarkRead />`
  - `BoardRow` gains `selected?: boolean` (select mode's checkbox when defined)

- [ ] **Step 1: Write the failing tests**

```ts
// mobile-native/src/board/selection.test.ts
import { expect, it } from "vitest";
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { BoardState } from "./attention";
import { selectionActions, toggleSelected } from "./selection";

/** A ref localSessionId accepts: "local:" and 22 base62 characters. */
const local = (letter: string) => `local:${letter.repeat(22)}`;
const row = (ref: string, over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref.replace(/^local:/, ""),
	title: ref,
	project: "evener",
	state: "awaiting",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const chosen = (summary: NavigationSessionSummary, state: BoardState, archived = false) => ({
	item: { row: summary, state },
	archived,
});
const refs = (rows: NavigationSessionSummary[]) => rows.map((summary) => summary.ref);
const online = { connected: true, organizationReady: true };

it("applies each action to the sessions it can act on, once each", () => {
	const finished = row(local("a"));
	const working = row(local("b"), { state: "active" });
	const remote = row("paradise-park:c", { host_id: "paradise-park", session_id: "c" });
	const fork = row(local("d"), { kind: "fork" });
	const archivedAlready = row(local("e"), { state: "ended", live: false });
	const actions = selectionActions(
		[
			chosen(finished, "finished"),
			chosen(finished, "finished"),
			chosen(working, "working"),
			chosen(remote, "finished"),
			chosen(fork, "finished"),
			chosen(archivedAlready, "shutDown", true),
		],
		online,
	);
	expect(refs(actions.archive)).toEqual([local("a"), local("b")]);
	expect(refs(actions.pin)).toEqual([local("a"), local("b"), "paradise-park:c", local("e")]);
	expect(refs(actions.markRead)).toEqual([local("a"), "paradise-park:c", local("d")]);
});

it("keeps only Mark as read while offline or while a change is unresolved", () => {
	const selected = [chosen(row(local("a")), "finished")];
	for (const context of [
		{ connected: false, organizationReady: true },
		{ connected: true, organizationReady: false },
	]) {
		const actions = selectionActions(selected, context);
		expect([actions.archive, actions.pin].map(refs)).toEqual([[], []]);
		expect(refs(actions.markRead)).toEqual([local("a")]);
	}
});

it("toggles one session in and out of the selection", () => {
	const once = toggleSelected(new Set(), local("a"));
	expect([...once]).toEqual([local("a")]);
	expect([...toggleSelected(once, local("a"))]).toEqual([]);
});
```

  - `SelectBar.test.tsx`: it shows "Done", "Archive", "Pin" and "Mark as read"; an action whose count is 0 renders with `accessibilityState.disabled` and doesn't call its handler; pressing each enabled one calls its handler; "Done" always works.
  - `BoardScreen.test.tsx`:
    - "Select" leads the toolbar when the Board shows a session; pressing it shows a checkbox mark on every session row and replaces the toolbar with the select bar;
    - a tap on a row toggles its checkbox (`accessibilityState.selected`) and doesn't open the session;
    - while selecting, a hub change doesn't reorder the list; "Done" applies it;
    - Archive with two local sessions chosen (real session ids) sends `evener/archive/set` for each, in order, then leaves select mode and shows "Archived 2 sessions" with "Undo";
    - Pin opens the category sheet (`ActionSheetIOS` mocked) listing the pin catalog's categories, "New category…" and "Cancel"; choosing "Release" sends `evener/session-pin/assign { sessionRef, sectionId: "release" }` for each chosen session and shows "Pinned 2 sessions to Release";
    - Mark as read marks each finished session seen (the seen markers' storage) and leaves select mode.
- [ ] **Step 2: Run them and watch them fail.** Run: `cd mobile-native && npx vitest run src/board/selection.test.ts src/board/SelectBar.test.tsx src/board/BoardScreen.test.tsx`
- [ ] **Step 3: Implement.** `selection.ts`:

```ts
// mobile-native/src/board/selection.ts
// Select mode (spec 7.1): which of the chosen sessions each of the select
// bar's actions applies to.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ClassifiedRow } from "./attention";
import { archiveTarget, isTopLevel } from "./rowActions";

export interface SelectedRow {
	item: ClassifiedRow;
	/** The row was chosen in an archived tier. */
	archived: boolean;
}

export interface SelectionActions {
	archive: NavigationSessionSummary[];
	pin: NavigationSessionSummary[];
	markRead: NavigationSessionSummary[];
}

/** Each session once, however many sections it was chosen in. Archive takes
 * this hub's own top-level sessions not already archived (ruling 20) and Pin
 * top-level sessions, both only while connected with the journal free
 * (ruling 21); Mark as read takes the finished ones. */
export function selectionActions(
	selected: readonly SelectedRow[],
	context: { connected: boolean; organizationReady: boolean },
): SelectionActions {
	const actions: SelectionActions = { archive: [], pin: [], markRead: [] };
	const organize = context.connected && context.organizationReady;
	const seen = new Set<string>();
	for (const { item, archived } of selected) {
		if (seen.has(item.row.ref)) continue;
		seen.add(item.row.ref);
		if (organize && !archived && archiveTarget(item.row)) actions.archive.push(item.row);
		if (organize && isTopLevel(item.row)) actions.pin.push(item.row);
		if (item.state === "finished") actions.markRead.push(item.row);
	}
	return actions;
}

export function toggleSelected(selected: ReadonlySet<string>, ref: string): Set<string> {
	const next = new Set(selected);
	if (!next.delete(ref)) next.add(ref);
	return next;
}
```

  The screen, to these requirements (spec 7.1; rulings 18, 21, 22, 26):
  1. **Entering.** `BoardToolbar` leads with "Select" (`accentInk`, 17pt) while the Board shows at least one session row. Pressing it turns select mode on, calls `list.setInteraction("select", true)`, and replaces the toolbar with `SelectBar`.
  2. **Rows.** In select mode every session row renders without `SwipeRow` and `RowMenu`, and `BoardRow` gets `selected`: its mark column shows `checkmark.circle.fill` in `accent` when selected and `circle` in `inkLow` otherwise, its accessibility role is a button with `accessibilityState.selected`, and a press toggles the row's ref in the selection (`toggleSelected`) instead of opening it, then announces "N selected" to VoiceOver (`AccessibilityInfo.announceForAccessibility`). Folds still fold.
  3. **The bar.** `SelectBar` shows "Done" at the leading edge and "Archive", "Pin" and "Mark as read" after it, each 17pt in `accentInk`, or `inkLow` and disabled while its count in `selectionActions(chosen rows, { connected, organizationReady })` is 0. "Done" leaves select mode, clears the selection and calls `list.setInteraction("select", false)`.
  4. **Archive** runs `archiveSession(actions, archiveTarget(row), true)` for each session in order and stops at the first that isn't confirmed (then `actions.reconcile()` once). With N confirmed of M: the toast "Archived N sessions" ("Archived 1 session"), or "Archived N of M sessions", with "Undo", which unarchives those N in order. None confirmed: no toast.
  5. **Pin** opens `ActionSheetIOS.showActionSheetWithOptions` titled "Pin to category", listing the pin catalog's categories by name (the Board's `pins` page), then "New category…" and "Cancel". A category pins each session with `pinSession(actions, { sessionRef: row.ref, sectionId })`; "New category…" opens `Alert.prompt("New category", undefined, [{ text: "Cancel", style: "cancel" }, { text: "Create", onPress }], "plain-text")`, and a trimmed name of 1-80 characters (the journal's own bound) pins each with `{ sessionRef, sectionName }` (the hub reuses the category it creates for the first one); a longer name shows the toast "Category names can be up to 80 characters." and pins nothing. Then the toast "Pinned N sessions to <name>".
  6. **Mark as read** marks each finished session seen (`seenMarkers(hubId).markSeen(row)`); no toast.
  7. **Leaving.** Each of the three actions ends select mode when it completes (ruling 26), which releases the list, so the rows it changed move with the spring.
- [ ] **Step 4: Run them and watch them pass**, then `npm run check`. In the simulator against a local `evener-hub`: select three sessions, archive them, undo; select two and pin them to a new category; mark a finished one as read.
- [ ] **Step 5: Commit** (`feat(native): select mode on the Board`).

Open PR 4c: "feat(native): select mode, and a Board that holds still (phase 2, PR 4c)". The description carries the settle table, what the simulator showed for a fling and a question arriving mid-scroll, and the Reduce Motion behavior (ruling 23), and, unless another phase 2 PR lands after it, part 1's Task 17 screenshots.

---

## Self-review against the spec

- **7.1:**
  - Projects/Hosts with the Organize by toggle, pinned projects first, live counts, the host's Offline state: Tasks 10.1, 10.3, 10.4 and 10.6.
  - Inside a project, today, recent and a folded archived group: Task 10.4 (ruling 14).
  - Test runs and Archived, folded: Tasks 10.2, 10.4 and 10.6.
  - The toolbar's Select with Archive, Pin and Mark as read: Task 13.4.
  - Every section's folded state persists per device: Task 10.6 (`foldedSections`).
- **7.3:**
  - Swipe right archives, full swipe, "Archived · Undo" for 8 seconds: Tasks 12.4 and 12.5.
  - The 24pt edge band: Tasks 12.3 and 12.4.
  - Swipe left: Stop only while working, Pin, More: Tasks 12.3, 12.5 and 12.7.
  - Long-press menu with a preview card and no "Open" item: Tasks 12.6 and 12.7; its actions: Task 12.3; Copy link waits (ruling 27).
  - The list never reorders under a finger or while scrolling; changes apply on settle; the 250ms spring; the amber wash: Tasks 13.1-13.3.
- **7.4:** search's project hit lands through `projectRevealTarget` (Task 10.4) and `revealProject` (Task 10.6); part 2's Task 15 wires the tap.
- **14:** an archived row dims until confirmed (Task 12.5); offline Board actions: ruling 21 and Question 2.
- **16.1, 16.5, 16.6:** the Global Constraints' swipe fills and symbols; `ROW_MOVE` and the wash (Task 13.3); Reduce Motion (ruling 23, Task 13.2).
