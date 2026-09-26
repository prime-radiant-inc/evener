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
  - Inside an unfolded project, sessions split into today (`current`), recent, and a folded archived group, as quiet rows.
- **"Organize by."**
  - It appears only when the manifest has more than one source. The toggle in the section header flips between "Project, then host" (default) and "Host, then project", and the section title between "Projects" and "Hosts".
  - Its choice persists in `foldedSections` under the key `organize-by-host`.
  - Hosts group projects by `NavigationProjectSummary.sources`, defaulting an omitted or empty list to `["local"]` (the field's own doc comment: omitted means local-only), labelled with the manifest's source label. A host group's header shows an amber "Offline" when its source is offline, and no status text when connected.
- **Test runs** (folded by default) reads the `test_runs` catalog; **Archived** (folded by default) reads `archived_projects`, with each project's archived tier inside.
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
- Produces: `type Notice = { kind: "signIn" | "host" | "plugin"; key: string; text: string; action: "Sign in" | "Details" | "Plugins" }` and `notices(input: { auth: AuthStatusResponse[]; sources: Source[]; plugins: PluginEntry[]; loadedRows: readonly NavigationSessionSummary[] }): Notice[]`. `loadedRows` is `BoardScreen`'s union of every page it has loaded so far: Live, each pinned category, Projects' `current`/`recent`/`archived` groups, test runs and archived projects. A session can be visible only through one of these, so all of them count.

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
- Modify: `mobile-native/src/board/BoardScreen.tsx` (replace the `RosterSearch` field from Task 7) and `mobile-native/src/board/boardMemory.ts` (`forgetBoard` removes `evener.native.recent-searches.${hubId}` too)
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
  - Tapping a session opens it; tapping a project scrolls to it and unfolds it, in whichever of Task 10's groupings is showing: directly in the Projects section ("Project, then host" mode), or inside its host group in the Hosts section ("Host, then project" mode). This needs Task 10's `ProjectsSection`. PR 5 starts alongside PR 3, not after it, so if PR 3 hasn't landed yet when this task ships, land the rest of Task 15 first and wire the project tap once PR 3 merges.
- **Recent searches** show when the field is empty: the last 8 queries submitted with a tap on a result, kept in kv-store under `evener.native.recent-searches.${hubId}` and cleared by `forgetBoardForHub`, with a "Clear" action.
- **Retire today's search:** `RosterSearch` and `rosterSearch.ts` are deleted if nothing else uses them. Check first with `grep -rn "rosterSearch\|RosterSearch" mobile-native/src`.

- [ ] Steps: failing tests (debounce and newest wins, scopes, grouping, recent searches bounded and per hub), implement, `make test-native`, then commit (`feat(native): Board search`). Open PR 5: "feat(native): Board notices and search (phase 2, PR 5)".

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
- Produces `rowActions.ts`. Each function is at the request boundary, takes the client, and returns the hub's result:
  - `archiveSession(actions: NavigationActions, row, archived: boolean)`, using `evener/archive/set` through `NavigationActions.archive` so the recovery journal covers it;
  - `stopSession(client, row)`, calling `turn/interrupt`. Read `TurnInterruptParams` in `appwire/types.go`. If it needs the instance id, read it with `thread/read { ref }` first, and say so in a comment;
  - `shutDownSession(client, row)` (`thread/shutdown`);
  - `renameSession(client, row, name)` (`evener/thread/name/set`);
  - `pinSession(actions, row, target)` (`NavigationActions.assignPin`).

**Requirements (spec 7.3):**
- **Swipe right** (leading) is Archive, in blue-gray (`inkMid` fill, white label). A full swipe archives, and the toast "Archived · Undo" shows for 8 seconds; Undo unarchives.
- **Swipes that begin within 24pt of the screen's left edge never act on a row.** The rule is enforced by a pure start-x check, tested as a pure function, applied in the swipe gesture's start handler; this is the mechanism of record regardless of what else is tried. A `ReanimatedSwipeable` `hitSlop` of `{ left: -24 }` may also be worth trying, since it's unverified whether it alone satisfies the rule, but it never replaces the start-x check. Cover the iOS edge-swipe-back gesture in the same test: a swipe starting in that 24pt band must reach the OS, not this row.
- **Swipe left** (trailing) shows Stop (only when the row is working), Pin and More (which opens the long-press menu).
- **Long-press:**
  - Spike `@expo/ui`'s SwiftUI `ContextMenu` with a preview first, then `@react-native-menu/menu`, and if neither gives a preview card, a sheet that shows the preview card on top with the actions below.
  - The preview card holds the title, state, why line, project and host (the task progress, subagent failures, model and excerpt come with S13, S3 and S1).
  - Tapping the card opens the session.
  - The actions are: Pin to category… (the categories plus "New category…"), Mark as read or Mark as unread (`seenMarkers`), Stop (when working), Shut down (destructive, confirmed), Archive, and Rename. Copy link waits for a session deep link, which the app doesn't have; say so in the PR.
  - Record the spike's outcome in the PR description.
- Every action shows its state where it was taken (spec 14's outbox): an archived row dims until the hub confirms.

- [ ] Steps: failing tests (each `rowActions` function sends the right request; the edge-zone rule; the menu's actions per state), implement, `make test-native`, look in the simulator, then commit (`feat(native): swipe and long-press actions on Board rows`).

### Task 13: Select mode and list stability

**Files:**
- Create: `mobile-native/src/board/listStability.ts` (pure) and `mobile-native/src/board/SelectBar.tsx`
- Modify: `mobile-native/src/board/BoardScreen.tsx` and `mobile-native/src/board/BoardToolbar.tsx`
- Test: `mobile-native/src/board/listStability.test.ts`

**Requirements (spec 7.1, 7.3):**
- **Select** leads the toolbar. It puts rows into multi-select, with a checkbox in the mark column and a bottom bar: Archive, Pin, and Mark as read. Done leaves.
- **The list never reorders while a finger is on it or it is scrolling.**
  - Hold the band ordering: `listStability.ts` exports `class HeldOrder` with `hold()`, `release()` and `order(next: LiveBands): LiveBands`. While held, `order` returns the previous order with row contents updated in place; a row missing from `next` keeps its last known content rather than disappearing, so no row's position shifts while held. `release()` itself takes no snapshot: it only clears the held flag. `BoardScreen` calls `release()` and then immediately calls `order(next)` again with the latest bands; that call is what drops any row still missing from `next` and applies the settled order.
  - Changes apply when the list settles (`onScrollEndDrag`, `onMomentumScrollEnd`, touch end).
  - Rows move with a 250ms spring (Reanimated layout transitions).
  - A row entering Needs you gets a brief amber wash: `attentionBg` fading out over 1.2s.
  - With Reduce Motion on, rows change places without animation and the wash is skipped.

- [ ] Steps: failing tests (`HeldOrder` keeps order and content-only updates while held, keeps a departed row's last content until release, and drops it and applies the new order only once `release()` is followed by `order(next)`), implement, `make test-native`, look in the simulator, then commit (`feat(native): select mode and a Board that holds still under your finger`). Open PR 4: "feat(native): Board row actions and select mode (phase 2, PR 4)".

---
