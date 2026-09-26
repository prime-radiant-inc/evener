# iPhone redesign, Phase 2: the Board — Implementation Plan, part 2

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

This is part 2 of `docs/superpowers/plans/2026-09-26-iphone-redesign-phase2-board.md` (part 1: PRs A, 1, B and 2, Tasks 1-8, 16 and 17). Part 1's Goal, Architecture, Tech Stack, Spec, Global Constraints, Rulings and Review Focus bind every task here, and its task numbers continue here. The PRs below start once part 1's PR 2 (the Board, Live first) has landed.

Tasks 10 (projects and hosts, including test runs and archived), 12 (swipes and the long-press menu) and 13 (select mode and list stability) are designed in part 3, `docs/superpowers/plans/2026-09-26-iphone-redesign-phase2-board-part3.md`, which lands in its own PR. Test runs and Archived move with Task 10: they read through the same `projectBrowser.ts` reuse and render in the same `ProjectsSection.tsx` as Projects, so they don't stand on their own. This leaves PR 3 as Task 9 alone and PR 4 as Task 11 alone; PR 4 no longer waits on PRs 3 and 5, since Task 11 only installs gesture libraries and wraps the app root, and touches none of their screens.

---

## PR 3: pinned categories

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

- [ ] Steps follow Task 5's pattern: failing tests (pages per category, and fold state persists), implement, `make test-native`, then commit (`feat(native): pinned categories live on the Board`). Open PR 3: "feat(native): pinned categories on the Board (phase 2, PR 3)".

---

## PR 5: notices and search

### Task 14: Notices

**Files:**
- Create: `mobile-native/src/board/notices.ts` (pure) and `mobile-native/src/board/Notices.tsx`
- Modify: `mobile-native/src/board/boardData.ts` (auth and plugin reads) and `mobile-native/src/board/BoardScreen.tsx`
- Test: `mobile-native/src/board/notices.test.ts`, `mobile-native/src/board/boardData.test.ts` and `mobile-native/src/board/Notices.test.tsx`

**Interfaces:**
- Produces: `type Notice = { key: string; text: string } & ({ kind: "signIn"; action: "Sign in"; providerId: string } | { kind: "host"; action: "Details"; sourceId: string } | { kind: "plugin"; action: "Plugins"; pluginId: string })` and `notices(input: { auth: AuthStatusResponse[]; sources: Source[]; plugins: PluginEntry[]; loadedRows: readonly NavigationSessionSummary[] }): Notice[]`. `providerId` is `AuthStatusResponse.provider`, `sourceId` is `Source.id`, `pluginId` is `PluginEntry.plugin`: `Notices.tsx` reads these to route the tap, never the display `text`. `loadedRows` is `BoardScreen`'s union of every page it has loaded so far: Live, the `needs_you` section (Task 5's separate page for a session that needs you past the loaded Live pages, per part 1's Review Focus 2), and each pinned category (Task 9). Task 10's Projects `current`/`recent`/`archived` groups, test runs and archived projects join this union once part 3's Task 10 lands; until then the host notice's count is narrower, which is still on the safe, undercount-only side (see the fallback note below).

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
  - **Sessions** lists `live` and then `past` results, each with a state mark and the age. `SearchResult` (`id`, `title`, `project`, `state`, `age`, `ref`) carries none of the extra fields `boardState` needs (`ask_pending`, `offline`, `dormant`), so don't route it through `boardState`. Add a small dedicated `searchResultMark(state: string): BoardState` in `boardSearch.ts` that maps the same state strings `boardState`'s switch does (`errored`, `restartRequired`, `warning`, `ended`/`notLoaded`, `active`) and falls back to `finished` otherwise: a `state: "awaiting"` result (a pending question) shows as Finished in search until the Board's own row data can tell them apart.
  - **Projects** lists the loaded projects catalog filtered by name or `working_dir`, case-insensitive.
  - Tapping a session opens it; tapping a project scrolls to it and unfolds it, in whichever of Task 10's groupings is showing: directly in the Projects section ("Project, then host" mode), or inside its host group in the Hosts section ("Host, then project" mode). This needs Task 10's `ProjectsSection`, designed in part 3 (see the pointer at the top of this file) and landing in whichever PR part 3 gives it. See the Steps below for how this task's two commits sequence around that PR landing.
- **Recent searches** show when the field is empty: the last 8 queries submitted with a tap on a result, kept in kv-store under `evener.native.recent-searches.${hubId}` and cleared by `forgetBoardForHub`, with a "Clear" action.
- **Retire today's search:** `RosterSearch` and `rosterSearch.ts` are deleted if nothing else uses them. Check first with `grep -rn "rosterSearch\|RosterSearch" mobile-native/src`.

- [ ] Steps: failing tests (debounce and newest wins, scopes, grouping, recent searches bounded and per hub, `searchResultMark` including the `awaiting` fallback), implement, `make test-native`, then commit (`feat(native): Board search`) for everything above except the project-tap behavior.
  - If part 3's Task 10 PR has already landed, add a second commit now (failing test, then implementation, then `make test-native`): `feat(native): wire search's project tap to the Board`. Open PR 5 with both commits: "feat(native): Board notices and search (phase 2, PR 5)".
  - If it hasn't landed yet, open PR 5 with only the first commit. Task 15, and this PR, are not done: the moment that PR merges, open a follow-up PR with the second commit above, tracked the same way any other open task is.

---

## PR 4: gesture and animation foundations

### Task 11: Gesture and animation foundations

**Files:**
- Modify: `mobile-native/package.json`, `package-lock.json`, `babel.config.js` (the worklets plugin, if Expo 57's preset doesn't add it), `App.tsx` (`GestureHandlerRootView` at the root) and `Podfile.lock` (regenerated as in Task 4, Step 6)
- Modify: `mobile-native/src/renderNative.testkit.tsx` only if screen tests need a gesture-handler mock

- [ ] **Step 1:** Run `npx expo install react-native-gesture-handler react-native-reanimated react-native-worklets` and let Expo pick SDK 57's versions. Read the installed `react-native-reanimated` README's Expo section for the Babel plugin requirement, and follow it.
- [ ] **Step 2:** Wrap the app root in `GestureHandlerRootView style={{ flex: 1 }}`.
- [ ] **Step 3:** Regenerate the lock (Task 4, Step 6's commands). Expected: the diff adds the three pods only.
- [ ] **Step 4:** Run `make test-native`. The ConversationScreen tests import all of `screens.tsx`; if a new native import breaks them, mock it in that test's `vi.mock` list, as the test already does for other native modules.
- [ ] **Step 5:** Build Release in the simulator and launch. Commit (`build(native): gesture handler and reanimated`). Open PR 4: "build(native): gesture handler and reanimated foundations (phase 2, PR 4)".

---
