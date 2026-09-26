# iPhone redesign, Phase 2: the Board — Implementation Plan, part 2

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

This is part 2 of `docs/superpowers/plans/2026-09-26-iphone-redesign-phase2-board.md` (part 1: PRs A, 1, B and 2, Tasks 1-8, 16 and 17). Part 1's Goal, Architecture, Tech Stack, Spec, Global Constraints, Rulings and Review Focus bind every task here, and its task numbers continue here. The PRs below start once part 1's PR 2 (the Board, Live first) has landed; PR 4 starts once PRs 3 and 5 have landed.

---

## PR 3: pinned categories, projects and hosts, test runs, archived

### Task 9: Pinned categories inline

**Files:**
- Create: `mobile-native/src/board/PinnedSections.tsx`
- Modify: `mobile-native/src/board/boardData.ts` (per-category pages) and `mobile-native/src/board/BoardScreen.tsx`
- Test: `mobile-native/src/board/boardData.test.ts` and `mobile-native/src/board/PinnedSections.test.tsx`

**Interfaces:**
- Produces: `BoardSnapshot.pinSections: Record<string, Page<NavigationSessionSummary>>`, one `NavigationPages` per category (`{ resource: "pin_section", sectionId }`, field `"sessions"`, limit 50), created for each category in the catalog and dropped when a category leaves it.

**Requirements (spec 7.1):**
- Each category is its own section, in the hub's order. The header shows `pin.fill`, the name, the count, a fold toggle (`foldedSections` key `pin:${id}`, unfolded by default), and ⋯ with Rename and Delete.
  - Rename and Delete use `NavigationActions.renamePinSection` and `deletePinSection` through the existing `usePinNavigation` flow, or open `PinSectionEditor` with its existing params.
  - Delete confirms: "Delete "<name>"? Its sessions stay; they're only unpinned."
- Rows are quiet one-line `BoardRow`s with a still mark. A live session also stays in Live.
- An empty category says "Touch and hold a session and choose Pin to category."
- The pinned-category row links from PR 2 are removed.

- [ ] Steps follow Task 5's pattern: failing tests (pages per category, and fold state persists), implement, `make test-native`, then commit (`feat(native): pinned categories live on the Board`).

### Task 10: Projects and hosts, test runs, archived

**Files:**
- Create: `mobile-native/src/board/ProjectsSection.tsx`
- Modify: `mobile-native/src/board/BoardScreen.tsx` and `mobile-native/src/projectBrowser.ts` (reuse it: add a catalog parameter so it can read `projects`, `test_runs` and `archived_projects`, and widen `ProjectSessionTier` from `"current" | "recent"` to also include `"archived"`, since the shared `project_page` resource already carries that tier, per `appwire-client/typescript/state/navigation/types.ts` and its use in `navigationReveal.ts`; each project group then loads and exposes an `archived` page alongside `current` and `recent`)
- Test: `mobile-native/src/projectBrowser.test.ts` and `mobile-native/src/board/ProjectsSection.test.tsx`

**Requirements (spec 7.1):**
- **Projects.**
  - Projects come from `createProjectBrowserController(client, "projects")`. Keep today's default (`"projects"`) so existing callers and tests don't change.
  - Pinned projects (`favorite`) float to the top with `pin.fill`. Each project row shows its name and live count (`rollup_live`).
  - Inside an unfolded project, sessions split into today (`current`), recent, and a folded archived group (`foldedSections` key `project:${projectKey}:archived`), as quiet rows.
- **"Organize by."**
  - It appears only when the manifest has more than one source. The toggle in the section header flips between "Project, then host" (default) and "Host, then project", and the section title between "Projects" and "Hosts".
  - Its choice persists as its own field, not a section entry: add `organizeByHost(): boolean` / `setOrganizeByHost(value: boolean): void` to `FoldedSections`, reading and writing the same per-hub `board-sections` storage `isFolded`/`setFolded` use but a distinct top-level key in the stored JSON, so a generic fold-reset that walks the section map can't flip it.
  - Hosts group projects by `NavigationProjectSummary.sources`, defaulting an omitted or empty list to `["local"]` (the field's own doc comment: omitted means local-only), labelled with the manifest's source label. A host group's header shows an amber "Offline" when its source is offline, and no status text when connected.
- **Test runs** (`foldedSections` key `test-runs`, folded by default) reads the `test_runs` catalog; **Archived** (`foldedSections` key `archived`, folded by default) reads `archived_projects`, with each project's archived tier inside.
- Rows in Projects, Test runs and Archived are quiet `BoardRow`s. A session on an offline host is `ended`, so it shows as shut down.
- The Projects and Archived link rows from PR 2 are removed.

- [ ] Steps: failing tests (the catalog parameter reads each catalog; a project group's `archived` page loads and folds by default alongside `current` and `recent`; the host grouping including a project whose `sources` is omitted or `[]`; the offline label; fold defaults), implement, `make test-native`, then commit (`feat(native): projects, hosts, test runs and archive on the Board`). Open PR 3: "feat(native): the Board's own sections (phase 2, PR 3)".

---

## PR 5: notices and search

### Task 14: Notices

**Files:**
- Create: `mobile-native/src/board/notices.ts` (pure) and `mobile-native/src/board/Notices.tsx`
- Modify: `mobile-native/src/board/boardData.ts` (auth and plugin reads) and `mobile-native/src/board/BoardScreen.tsx`
- Test: `mobile-native/src/board/notices.test.ts`, `mobile-native/src/board/boardData.test.ts` and `mobile-native/src/board/Notices.test.tsx`

**Interfaces:**
- Produces: `type Notice = { key: string; text: string } & ({ kind: "signIn"; action: "Sign in"; providerId: string } | { kind: "host"; action: "Details"; sourceId: string } | { kind: "plugin"; action: "Plugins"; pluginId: string })` and `notices(input: { auth: AuthStatusResponse[]; sources: Source[]; plugins: PluginEntry[]; loadedRows: readonly NavigationSessionSummary[] }): Notice[]`. `providerId` is `AuthStatusResponse.provider`, `sourceId` is `Source.id`, `pluginId` is `PluginEntry.plugin`: `Notices.tsx` reads these to route the tap, never the display `text`. `loadedRows` is `BoardScreen`'s union of every page it has loaded so far: Live, the `needs_you` section (Task 5's separate page for a session that needs you past the loaded Live pages, per part 1's Review Focus 2), each pinned category, Projects' `current`/`recent`/`archived` groups, test runs and archived projects. A session can be visible only through one of these, so all of them count.

**Requirements (spec 7.1, ruling 8):**
- **Sign-in:** one notice per provider with `needsLogin`: "<provider> sign-in expired". Its action "Sign in" opens today's provider sign-in flow for that provider. #2483 is fixing `needsLogin` to mean "access token expired and no refresh token", so a sign-in that refreshes silently stops tripping the notice. A rejected refresh isn't recorded anywhere yet (#2479), so the notice can't fire for that case until #2479 lands.
- **Host:** one notice per offline source: "<label> is offline · 3 sessions", where the count is the number of distinct `loadedRows` (deduped by `ref`, since a pinned live session appears in both Live and its category) whose `host_id` is that source. The action "Details" opens `HubSettings` until phase 5.
  - `Source` carries no session count (`appwire-client/typescript/types.gen.ts`), so this count is a fallback over whatever pages the Board has already loaded: a session on a page not yet loaded (Live past 50, a folded pinned category, project group, test run or archived page) isn't counted. It can undercount but never overcounts. A server rollup would replace this in a later phase; note it as a known fallback limit, not a bug to fix here.
- **Plugin:** one notice per broken plugin: "<plugin> is broken". Its action "Plugins" opens today's `Plugins` screen. That route takes only `{ hubId }`, so opening at that plugin's row (spec 7.1) waits for phase 5's Hub; the notice already names the plugin.
- **Placement and style:** notices sit under the chips as rows: `exclamationmark.triangle.fill` in amber, the sentence in `inkHi`, the action in `accentInk`. No tinted box. They disappear when resolved.
- **Reads:**
  - `evener/auth/list` on focus and on `evener/auth/updated`.
  - `evener/plugin/list` on focus and every 5 minutes while the Board is focused.
  - The sources come from the manifest.

- [ ] Steps: failing tests (the pure `notices` table, including a host count deduped across rows loaded from more than one section; the reads and polling with fake timers; and rendering), implement, `make test-native`, then commit (`feat(native): Board notices`).

### Task 15: Search

**Files:**
- Create: `mobile-native/src/board/boardSearch.ts` (the controller and recent searches) and `mobile-native/src/board/SearchResults.tsx`
- Modify: `mobile-native/src/board/BoardScreen.tsx` (replace the `RosterSearch` field from Task 7) and `mobile-native/src/board/boardMemory.ts` (the pure `forgetBoard(storage, hubId)` removes `evener.native.recent-searches.${hubId}` too; its per-hub wrapper `forgetBoardForHub`, already wired into `ConnectionProvider.removeHub`, needs no change since it already calls `forgetBoard`)
- Test: `mobile-native/src/board/boardSearch.test.ts`, `mobile-native/src/board/SearchResults.test.tsx` and `mobile-native/src/board/boardMemory.test.ts` (the forget test covers all three keys)

**Requirements (spec 7.4, ruling 7):**
- **Showing search:**
  - Search shows from the header's Search button or by pulling the list down. Spike `headerSearchBarOptions` on the native stack first: with `hideWhenScrolling`, iOS reveals it by pulling down, which is exactly the spec.
  - If the spike fails, use a search field in the list header, hidden at an initial content offset.
  - Record the result in the PR description.
- **Scopes:** chips for All and Live. Archived waits for S14.
- **Results:**
  - `evener/search { query }` is debounced 250ms. A newer query wins, and a query change clears stale results.
  - **Sessions** lists `live` and then `past` results, each with a state mark from `boardState` over `{ state }` and the age.
  - **Projects** lists the loaded projects catalog filtered by name or `working_dir`, case-insensitive.
  - Tapping a session opens it; tapping a project scrolls to it and unfolds it, in whichever of Task 10's groupings is showing: directly in the Projects section ("Project, then host" mode), or inside its host group in the Hosts section ("Host, then project" mode). This needs Task 10's `ProjectsSection`, built in PR 3. See the Steps below for how this task's two commits sequence around it.
- **Recent searches** show when the field is empty: the last 8 queries submitted with a tap on a result, kept in kv-store under `evener.native.recent-searches.${hubId}` and cleared by `forgetBoardForHub`, with a "Clear" action.
- **Retire today's search:** `RosterSearch` and `rosterSearch.ts` are deleted if nothing else uses them. Check first with `grep -rn "rosterSearch\|RosterSearch" mobile-native/src`.

- [ ] Steps: failing tests (debounce and newest wins, scopes, grouping, recent searches bounded and per hub), implement, `make test-native`, then commit (`feat(native): Board search`) for everything above except the project-tap behavior.
  - If PR 3 has already landed, add a second commit now (failing test, then implementation, then `make test-native`): `feat(native): wire search's project tap to the Board`. Open PR 5 with both commits: "feat(native): Board notices and search (phase 2, PR 5)".
  - If PR 3 hasn't landed yet, open PR 5 with only the first commit. Task 15, and this PR, are not done: the moment PR 3 merges, open a follow-up PR with the second commit above, tracked the same way any other open task is.

---

## PR 4: row actions, select mode, list stability

### Task 11: Gesture and animation foundations

**Files:**
- Modify: `mobile-native/package.json`, `package-lock.json`, `babel.config.js` (the worklets plugin, if Expo 57's preset doesn't add it), `App.tsx` (`GestureHandlerRootView` at the root) and `Podfile.lock` (regenerated as in Task 4, Step 6)
- Modify: `mobile-native/src/renderNative.testkit.tsx` only if screen tests need a gesture-handler mock

- [ ] **Step 1:** Run `npx expo install react-native-gesture-handler react-native-reanimated react-native-worklets` and let Expo pick SDK 57's versions. Read the installed `react-native-reanimated` README's Expo section for the Babel plugin requirement, and follow it.
- [ ] **Step 2:** Wrap the app root in `GestureHandlerRootView style={{ flex: 1 }}`.
- [ ] **Step 3:** Regenerate the lock (Task 4, Step 6's commands). Expected: the diff adds the three pods only.
- [ ] **Step 4:** Run `make test-native`. The ConversationScreen tests import all of `screens.tsx`; if a new native import breaks them, mock it in that test's `vi.mock` list, as the test already does for other native modules.
- [ ] **Step 5:** Build Release in the simulator and launch. Commit (`build(native): gesture handler and reanimated`).

### Task 12: Swipes and the long-press menu

**Files:**
- Create: `mobile-native/src/board/SwipeRow.tsx`, `mobile-native/src/board/rowActions.ts` and `mobile-native/src/board/RowMenu.tsx`
- Modify: `mobile-native/src/board/BoardRow.tsx` and `mobile-native/src/board/BoardScreen.tsx`
- Modify (only for whichever menu library the spike keeps): `mobile-native/package.json`, `package-lock.json` and `Podfile.lock` (`npx expo install @expo/ui` or `npx expo install @react-native-menu/menu`, then regenerate the lock as in Task 4, Step 6). If the spike settles on the sheet fallback, install neither.
- Test: `mobile-native/src/board/rowActions.test.ts` and `mobile-native/src/board/RowMenu.test.tsx`

**Interfaces:**
- Produces `rowActions.ts`. Each function is at the request boundary and returns the hub's result:
  - `archiveSession(actions: NavigationActions, row, archived: boolean)`, using `evener/archive/set` through `NavigationActions.archive` so the recovery journal covers it;
  - `stopSession(client, runtime: NativeMutationRuntime, hubId: string, row)`. `runtime.submit(...)` alone is not enough: the shared runtime only dispatches to the hub for a *registered* target with an open read gate (`#getClient` returns nothing otherwise, `nativeMutationRuntime.ts`), and the Board never mounts a Conversation screen for `row.ref` to register it. Build a short-lived host instead, the same way a Conversation screen does: `const host = createNativeMutationHost(runtime, hubId, row.ref, client)` (`mobile-native/src/nativeMutationHost.ts`); `await host.start()`; read `thread/read { ref: row.ref, includeTurns: false }` and derive `instanceId` as `response.thread.evener.instanceId ?? response.thread.id` (the same fallback `mobile/src/services/conversation.ts` uses); open the gate with `const lease = host.beginRead(row.ref); await host.reconcileRead(lease, response)`; then `await host.submit({ kind: "interrupt", hubId, targetRef: row.ref, threadId, instanceId, input: [] })`; finally `host.dispose()`. This never uses a raw `client.request("turn/interrupt", ...)`: `TurnInterruptParams.clientMutationId` is required and the hub rejects a missing one (`cmd/evener-hub/app_rpc.go`), and `enqueueInterruptAndCancel` (reached through `submit`) is what cancels the target's queued rows and bumps the stop epoch in the same durable write the Conversation screen's own Stop relies on (`mobile-native/src/mutationOutboxStorage.ts`). Test that the interrupt actually reaches the client, not just that it's stored in the outbox;
  - `shutDownSession(client, row)` (`thread/shutdown`, params are just `{ ref }`, no `clientMutationId`);
  - `renameSession(client, row, name)` (`evener/thread/name/set`, params are `{ ref, name }`, no `clientMutationId`);
  - `pinSession(actions, row, target)` (`NavigationActions.assignPin`).

**Requirements (spec 7.3):**
- **Swipe right** (leading) is Archive, in blue-gray (`inkMid` fill, white label). A full swipe archives, and the toast "Archived · Undo" shows for 8 seconds; Undo unarchives.
- **Swipes that begin within 24pt of the screen's left edge never act on a row, and must not stop iOS's own edge-swipe-back gesture from acting instead.** These are two separate properties, both required:
  - The resulting action (archive, or revealing Stop/Pin/More) must never fire for a touch that started there. This is a pure start-x check, tested as a pure function.
  - The row's own gesture recognizer must not claim the touch at all when it starts there; deciding not to act after the fact isn't enough, because by then the recognizer has already taken the touch away from the OS's back-swipe recognizer. Spike how `ReanimatedSwipeable` refuses or fails to activate for a start position in that band (a `hitSlop` of `{ left: -24 }`, `activeOffsetX`, or another activation-boundary API), record which one works and why in the PR description, and verify it in the simulator: a left-edge swipe starting in that band must trigger back navigation, not the row. If no such API can be made to work, fall back to leaving that 24pt-wide strip of the row outside the swipeable entirely (its own plain `View`, not wrapped by `ReanimatedSwipeable`), so the recognizer is never attached there and has nothing to claim.
- **Swipe left** (trailing) shows Stop (only when the row is working), Pin and More (which opens the long-press menu).
- **Long-press:**
  - Spike `@expo/ui`'s SwiftUI `ContextMenu` with a preview first, then `@react-native-menu/menu`, and if neither gives a preview card, a sheet that shows the preview card on top with the actions below.
  - The preview card holds the title, state, why line, project and host (the task progress, subagent failures, model and excerpt come with S13, S3 and S1).
  - Tapping the card opens the session.
  - The actions are: Pin to category… (the categories plus "New category…"), Mark as read or Mark as unread (`seenMarkers`), Stop (when working), Shut down (destructive, confirmed), Archive, and Rename. Copy link waits for a session deep link, which the app doesn't have; say so in the PR.
  - Record the spike's outcome in the PR description.
- Every action shows its state where it was taken (spec 14's outbox): an archived row dims until the hub confirms.

- [ ] Steps: failing tests (each `rowActions` function sends the right request; `stopSession` registers its target through `createNativeMutationHost`, opens the read gate, and the interrupt actually reaches the client, the way `nativeMutationDispatch.test.ts` checks `turn/start` dispatch, not just that it lands in the outbox; the edge-zone rule; the menu's actions per state), implement, `make test-native`, look in the simulator, then commit (`feat(native): swipe and long-press actions on Board rows`).

### Task 13: Select mode and list stability

**Files:**
- Create: `mobile-native/src/board/listStability.ts` (pure) and `mobile-native/src/board/SelectBar.tsx`
- Modify: `mobile-native/src/board/BoardScreen.tsx` and `mobile-native/src/board/BoardToolbar.tsx`
- Test: `mobile-native/src/board/listStability.test.ts`

**Requirements (spec 7.1, 7.3):**
- **Select** leads the toolbar. It puts rows into multi-select, with a checkbox in the mark column and a bottom bar: Archive, Pin, and Mark as read. Done leaves.
- **The list never reorders while a finger is on it or it is scrolling.**
  - Hold the band ordering: `listStability.ts` exports `class HeldOrder` with `hold()`, `order(next: LiveBands): LiveBands` and `release(next: LiveBands): LiveBands`. While held, `order` returns the previous order with row contents updated in place; a row missing from `next` keeps its last known content rather than disappearing, so no row's position shifts while held. `release` takes the latest bands and atomically returns the settled order in one call (drops any row still missing, applies the new order), so there's no separate call a caller can forget and no window where the held state has cleared but the order hasn't updated.
  - The list is still "scrolling" during momentum, with no finger on it, so a settle needs both a drag end and a momentum end, not either alone: `onScrollEndDrag` fires before `onMomentumScrollEnd`, and there's no way to tell from `onScrollEndDrag` alone, at the instant it fires, whether momentum is about to start. Track three states instead of a boolean: `dragging` -> (`onScrollEndDrag`) -> `draggedOff` -> either `onMomentumScrollBegin` -> `momentum` -> (`onMomentumScrollEnd`) -> release, or nothing arrives and momentum never starts. Since RN never signals "momentum will not start," resolve `draggedOff` with a short deferred check (a `requestAnimationFrame` or a fixed short timeout, an implementation choice) that releases only if still in `draggedOff` when it runs; `onMomentumScrollBegin` firing first cancels that check and moves straight to `momentum`. Touch end while not scrolling (state `idle`) settles immediately, same as today.
  - Rows move with a 250ms spring (Reanimated layout transitions).
  - A row entering Needs you gets a brief amber wash: `attentionBg` fading out over 1.2s.
  - With Reduce Motion on, rows change places without animation and the wash is skipped.

- [ ] Steps: failing tests (`HeldOrder` keeps order and content-only updates while held, keeps a departed row's last content until `release`, and `release(next)` atomically drops it and applies the new order in one call; the settle state machine releases on `onMomentumScrollEnd` when momentum began during `draggedOff`, releases on the deferred check when it didn't, and a fake timer proves the deferred check never fires once `onMomentumScrollBegin` cancels it), implement, `make test-native`, look in the simulator, then commit (`feat(native): select mode and a Board that holds still under your finger`). Open PR 4: "feat(native): Board row actions and select mode (phase 2, PR 4)".

---
