# Settings Organization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reorganize all web settings into clear task-based groups, unify plugin management, and preserve each control's behavior and scope.

**Architecture:** Keep the existing React settings shell, routing, stores, widgets and host ownership. Extend section metadata for grouped navigation and keyword discovery, share section resolution between pane title and content, and compose existing controls into their new destinations. General's unique fields move to existing owners; removed routes fall back to Appearance without compatibility aliases.

**Tech Stack:** TypeScript, React, Zustand, existing widget library and CSS tokens, Vitest/Testing Library, Vite and AppWire.

**Spec:** `docs/superpowers/specs/2026-10-16-settings-organization-design.md`

## Global Constraints

- Preserve all preference keys, stored values, defaults, backend methods, host ownership and project overrides.
- Use existing widgets from their barrel and existing design tokens. No new widget or widget prop without Jesse's approval.
- Preserve useful content and recovery when reorganizing. Do not trade independent availability for a shared all-or-nothing loader.
- No new dependencies, backend schema, migration, compatibility alias, or automatic mutation of real user settings.
- No new visual identity. Use the editorial instrument system in both themes.
- No broad notification, localStorage, plugin-launch or unrelated recovery redesign. Fix behavioral regressions caused by the consolidation at their source.
- Mockup files and its server remain separate from shipped source. Do not commit server state, credentials or logs.
- Read `docs/developing-evener/testing.md` and `docs/developing-evener/design-system.md` before changes. Assert behavior and structured values, never production copy.
- Jesse chose old `general`, `display`, and `plugins` URLs and remembered selections to fall back to Appearance. Do not forward them to their former owners.
- Apply changes within the existing isolated worktree. Leave the untracked critique archive and ignored prototype/server files alone.

## Review Focus

Each item is exercised in the named task rather than left to final inspection.

1. Explicit retired bookmark plus a valid remembered page: Appearance wins, title and current row agree, the URL is replaced rather than pushed, and host context survives (Tasks 1–3).
2. Phone Back/Forward and bare settings: the list stays a list, navigation filter/scroll survives detail visits, and a contextual project URL retains `cwd` and host (Task 1).
3. Mixed Input scope during hub failure: Enter-sends and character-key toggle remain browser-local and usable while synced shortcut editing remains read-only (Task 1).
4. Plugin inventory fails while directory configuration is healthy, then recovers or the host changes: directory controls stay usable and saves cannot target the old host/client (Task 3).
5. Missing optional runtime data and masked credentials: consolidation never fabricates past-index values or exposes a token, and credits/cache/update actions remain available under their existing contracts (Task 2).

## File boundaries

All `src/` paths below are relative to `cmd/evener-hub/frontend/`.

- `src/panes/settings/sections.ts`: ordered groups, labels, default ID, section recognition/resolution and search vocabulary. No stores or UI dependencies.
- `src/panes/settings/Settings.tsx` and `index.tsx`: pane content/title share resolution; shell retains URL/mobile/host ownership.
- `src/panes/settings/SettingsNav.tsx`: grouped rows, keyword filtering, empty results. No new search service.
- New `src/panes/settings/sections/inputShortcuts.tsx`: compose existing Enter-sends and keyboard configuration; no duplicated preference persistence.
- Existing `sections/{theme,display,keybindings,notifications,transcript}.tsx`: preserve logic, adjust grouping and truthful scope copy as needed.
- Existing `sections/{hub,storage,about}.tsx`: sole surviving owners of General's fields; remove `general.tsx` and its unused CSS only after migration.
- Existing `sections/marketplacesPlugins/index.tsx` and new `SourcesSection.tsx` in that directory: unify Sources under the manager's single host context; keep error boundaries independent.
- Existing `sections/pluginsDirs.tsx` and `dirListSetting.tsx`: reuse the actual directory editor. Remove the unused standalone host-wrapper export only if no callers remain.
- Existing `settingsHost.ts`, shell routing and stores retain current APIs unless a demonstrated bug requires a small change. No new wire or preference schema.
- `docs/product/subsystems.md` and `docs/web-ui/README.md`: evergreen ownership/navigation changes. Update current settings references in `docs/evener-hub.md` where applicable; do not rewrite historical parity specifications into current guides.

## Preparation

- [ ] Read the spec, current Git status and the project instructions. This lane currently contains the design/plan documents, the review archive and a separate live mockup; do not sweep these into code commits.
- [ ] Run `make help` from the root, inspect `scripts/web-preflight.sh` and the Makefile target it runs, then run `make web-preflight`. Never run `npm ci` through symlinked `node_modules`.
- [ ] Confirm `core.hooksPath` points to the clone's installed `scripts/hooks`; if not installed, inspect and run `make hooks`. Never bypass hooks.
- [ ] From `cmd/evener-hub/frontend`, establish a targeted baseline with `npm test -- src/panes/settings src/shell/routing.test.ts src/stores/prefs.test.ts src/stores/settingsOverview.test.ts src/stores/settingsHost.test.ts`. The npm script also runs package checks and the Node script tests; inspect all output. Record actual failures and fix those within scope before proceeding.

## Task 1: Preferences, groups and shared section resolution

**Files:**
- Modify `src/panes/settings/{sections.ts,Settings.tsx,index.tsx,SettingsNav.tsx,settings.module.css}`.
- Create `src/panes/settings/sections/inputShortcuts.tsx` and `inputShortcuts.test.tsx`; add a token-only CSS module only if existing layout primitives cannot arrange the two sections.
- Modify `src/panes/settings/sections/{display,keybindings,theme,notifications,transcript}.tsx` only where needed for composition/scope wording.
- Test `src/panes/settings/{sections.test.ts,Settings.test.tsx,index.test.tsx,SettingsNav.test.tsx,settingsHostRoute.test.tsx}`, existing display/keybindings tests and `src/shell/routing.test.ts`.

**Interfaces:**
- Produce `resolveSettingsSection(section: string | undefined, remembered: string | null): string` in `sections.ts`. Explicit known section or `project` wins; explicit unknown/retired resolves to `theme`; without an explicit section, use a known remembered navigation ID, otherwise `theme`. Never restore remembered `project`.
- Keep `isKnownSettingsSection(id: string): boolean` for navigation IDs only and `settingsSectionLabel(id: string): string` for presentation.
- Produce `InputShortcutsSection(): React.JSX.Element`, composing the existing `DisplaySection` and `KeybindingsSection` rather than copying control/store logic.
- Keep `settingsURL(section: string | undefined, host: string): string` and existing navigation callbacks unchanged.

- [ ] **Write failing resolver and navigation tests.** Add independent expected group/section ID arrays, not an assertion generated from production metadata. At this step remove only `display`; keep General under System and standalone plugin directories under Extensions until their content migrates in Tasks 2–3. Pin these pure cases:

```ts
expect(resolveSettingsSection(undefined, null)).toBe("theme");
expect(resolveSettingsSection(undefined, "notifications")).toBe("notifications");
expect(resolveSettingsSection("display", "notifications")).toBe("theme");
expect(resolveSettingsSection("unrecognized", "notifications")).toBe("theme");
expect(resolveSettingsSection("project", "theme")).toBe("project");
expect(resolveSettingsSection(undefined, "project")).toBe("theme");
```

- [ ] **Write integration regressions.** From an explicit `/settings/display?host=remote-test` with remembered `notifications`, assert actual Appearance controls mount, selected nav identity is `theme`, history length does not grow, pathname becomes `/settings/theme`, host remains `remote-test`, and title uses the same resolved ID. Repeat unknown ID, valid explicit ID and valid remembered ID. Preserve `/settings/project?cwd=/fixture/project&host=remote-test`, `/credentials`, and the existing `/settings/providers` alias. On mobile, assert bare settings shows the list and has no active row; select a section, Back/Forward, verify filter retention and correct content/list state.
- [ ] **Write Input integration tests.** Use the real prefs store and real keybinding UI against the existing external test transport. Change Enter-sends through the composed page and assert `prefsStore.getState().enterToSend` and its existing encoded storage value change; exercise real shortcut edits using existing fixtures and assert the hub request. With the keybindings read failing/unsupported, assert local controls remain operable while shortcut editing is disabled. Check character-key preference uses the existing local owner.
- [ ] **Run tests red.** From frontend: `npm test -- src/panes/settings/sections.test.ts src/panes/settings/Settings.test.tsx src/panes/settings/index.test.tsx src/panes/settings/SettingsNav.test.tsx src/panes/settings/settingsHostRoute.test.tsx src/panes/settings/sections/inputShortcuts.test.tsx`. Confirm behavioral failures, not missing dependencies or test-import setup errors.
- [ ] **Implement the shared resolver and composition.** Default becomes `theme`; group IDs are `preferences`, `agents-models`, `extensions`, `system`. Apply final labels/order from the spec, retaining General/plugin-directory entries only until migration. Use the resolver in both pane registration and content. Canonicalize an explicit invalid section with `navigate(settingsURL("theme", host), { replace: true })` after the URL host has been adopted; avoid a local-host overwrite on first render. Preserve valid project routing. Do not change default mobile list behavior.
- [ ] **Arrange Input & shortcuts with accurate scope.** Keep Enter-sends and character-key preference browser-local, and synced keybinding overrides visibly hub-owned. Reuse existing controls, errors and editing lifecycle. Keep the local controls outside any hub-error replacement boundary.
- [ ] **Run green and typecheck.** Rerun the red-test command, existing `display.test.tsx`/`keybindings.test.tsx`, and `npm run typecheck`. Format touched TS/CSS through the frontend's pinned Biome. Verify changes to existing tests preserve behavioral coverage rather than deleting failing expectations wholesale.
- [ ] **Commit this deliverable.** Stage only the named changed files and any affected behavior tests, inspect `git diff --cached`, then commit `feat(settings): organize preferences and preserve navigation scope`.

## Task 2: Consolidate General into System owners

**Files:**
- Modify `src/panes/settings/sections/{hub,storage,about}.tsx` as necessary; preserve existing row components.
- Modify `src/panes/settings/{sections.ts,Settings.tsx}` to retire General after migration.
- Remove `src/panes/settings/sections/general.tsx` and `general.module.css` once no production imports remain; migrate behavioral coverage from `general.test.tsx` before removing that test.
- Test `src/panes/settings/sections/{hub,storage,about,sessionCacheRow}.test.tsx`, resolver/shell tests, and affected `src/shell/AppShell.test.tsx` consumers of the old General route.

**Interfaces:**
- Consume Task 1's `resolveSettingsSection` and existing `settingsOverviewStore` snapshot fields.
- Preserve `HubSection`, `StorageSection`, and `AboutSection` component interfaces. No new backend response fields.

- [ ] **Write failing data-preservation tests.** Supply independent fixture values for all General fields. Assert Hub retains listen address/run directory/spawn timeout and receives bearer-token age beside a constant mask; do not read or create a real credential. Assert Storage renders optional index path/size/per-page values, state/config paths and its existing session cache control; omit index-dependent values when no index exists. Assert About continues to show fixture version/commit and keeps credits available on overview failure. Drive real read failure→retry→success for affected owners.
- [ ] **Write retired-General route tests.** Add `general` to explicit-retired and remembered-retired cases, including remote host and valid remembered competing section. Assert fallback to `theme`, not `hub`.
- [ ] **Run red.** From frontend: `npm test -- src/panes/settings/sections/hub.test.tsx src/panes/settings/sections/storage.test.tsx src/panes/settings/sections/about.test.tsx src/panes/settings/sections.test.ts src/panes/settings/Settings.test.tsx`.
- [ ] **Move only missing fields and remove the duplicate page.** Hub gets the constant token mask and age. Storage gets optional past-results-per-page beside its existing past index. About already owns version/commit, so do not duplicate it. Keep Hub updates/residents, Storage cache, mask-only credential handling and existing conditional/error behavior. Remove General navigation/dispatch and migrate its tests; update touched historical implementation comments to describe current responsibilities.
- [ ] **Run green.** Rerun the red command plus `npm test -- src/panes/settings/sections/sessionCacheRow.test.tsx src/shell/AppShell.test.tsx` and `npm run typecheck`. AppShell fixtures that intended a real overview fetch should navigate to Hub, rather than silently losing their setup through Appearance fallback.
- [ ] **Commit.** Stage exact touched/deleted files after reviewing the diff; commit `refactor(settings): consolidate runtime information under system`.

## Task 3: One host-scoped Plugins workspace

**Files:**
- Modify `src/panes/settings/sections/marketplacesPlugins/index.tsx` and its CSS module as needed.
- Create `src/panes/settings/sections/marketplacesPlugins/SourcesSection.tsx` and `SourcesSection.test.tsx`.
- Reuse `MarketplacesSection.tsx`, `PluginsDirsSection` in `sections/pluginsDirs.tsx`, and `dirListSetting.tsx`; modify only their necessary integration surfaces.
- Modify `src/panes/settings/{sections.ts,Settings.tsx}` to retire standalone `plugins` after composition.
- Test manager `index.test.tsx`, `indexHostScope.test.tsx`, `SourcesSection.test.tsx`, existing marketplace/plugin sheet tests, `pluginsDirs.test.tsx`, `dirListSetting.test.tsx`, and shell/resolver retired cases.

**Interfaces:**
- Produce `SourcesSection({ host, onSelectMarketplace }: { host: string; onSelectMarketplace: (name: string) => void }): React.JSX.Element`. Render `<MarketplacesSection onSelect={onSelectMarketplace} />` plus `<PluginsDirsSection host={host} />` in distinct sections, within the manager's existing `ExtensionsStoreProvider`. The parent passes its existing `setSelectedMarketplace` callback so sheets and mutation guards retain their owner.
- `MarketplacesPluginsBody({ host })` remains the orchestrator. Its segment union becomes `"installed" | "browse" | "sources"`; all old internal `marketplaces` comparisons/callers are updated together. No wire data fields change.
- Keep the manager's existing one host picker and `MarketplacesPluginsSection({ host })` public interface. Do not embed another `HostScopedSurface` around directories.

- [ ] **Write failing unified-Sources tests.** Navigate to Sources using the real segmented control. Assert marketplace operations and directory editor are reachable under one host picker. Add/remove a directory using an external test transport, assert the launch-layer request's `pluginDirs` and selected host; assert no request reaches local while remote is selected. Switch remote→local→remote and verify displayed/saved values belong to the current host.
- [ ] **Write independence and recovery tests.** Fail marketplace or plugin inventory reads while launch-layer reads succeed: navigation and directory editing remain usable. Then resolve the failed read/reconnect and verify the relevant manager content recovers without clearing a pending directory draft or duplicating a save. Retain the existing host/client mutation guards for sheets. Exercise the opposite failure: directories unavailable must not hide healthy installed plugins.
- [ ] **Write retired-directory route tests.** Explicit or remembered `plugins` resolves to `theme`, never Sources. `plugins-manager` remains a valid entry. Pin all final navigation IDs now that migrations are complete.
- [ ] **Run red.** From frontend: `npm test -- src/panes/settings/sections/marketplacesPlugins/SourcesSection.test.tsx src/panes/settings/sections/marketplacesPlugins/index.test.tsx src/panes/settings/sections/marketplacesPlugins/indexHostScope.test.tsx src/panes/settings/sections.test.ts src/panes/settings/Settings.test.tsx`.
- [ ] **Implement Sources with independent availability.** Reuse the real editor and marketplace list, rename the visible manager destination to Plugins and its third segment to Sources. Keep segment navigation outside the all-or-nothing loading/error gate; each inventory-dependent list gets its existing appropriate loading/error display, while directories retain their own owner. Retain sheet selection/close, removal/rename guards and update checks. State new-session effect without altering plugin resolution or installed revisions. Remove standalone directory navigation/dispatch only after the new path works.
- [ ] **Run green and preservation suites.** From frontend: `npm test -- src/panes/settings/sections/marketplacesPlugins src/panes/settings/sections/pluginsDirs.test.tsx src/panes/settings/sections/dirListSetting.test.tsx src/panes/settings/Settings.test.tsx src/panes/settings/sections.test.ts` followed by `npm run typecheck`.
- [ ] **Commit.** Review and stage exact affected files; commit `feat(settings): unify plugin management and sources`.

## Task 4: Find controls, document and verify the complete organization

**Files:**
- Modify `src/panes/settings/{sections.ts,SettingsNav.tsx,sections.test.ts,SettingsNav.test.tsx}` and nav CSS only if necessary.
- Modify `docs/product/subsystems.md`, `docs/web-ui/README.md`, and applicable current settings guidance in `docs/evener-hub.md`.
- Update any current browser guard fixtures that still assert retired navigation structure, without weakening geometry/behavior checks.

**Interfaces:**
- Extend `SettingsSection` with `searchTerms: readonly string[]`.
- Produce `matchesSettingsSection(section: SettingsSection, query: string): boolean` in `sections.ts`, matching a trimmed, case-insensitive substring against label and each explicit term. Empty/whitespace matches all. No store reads or separate search service.
- Preserve `SettingsNavProps` and its existing section-ID navigation callback.

- [ ] **Write failing search behavior tests.** Drive the real navigation filter with `font`, ` ENTER `, `sound`, `tokens`, `marketplace`, `plugin directory`, `skill directory`, `cache`, and `pairing`. Click each result and assert callback IDs `theme`, `keybindings`, `notifications`, `transcript`, `plugins-manager`, `plugins-manager`, `skills`, `storage`, and `mobile`, respectively. Assert no-match input presents an accessible empty state and no selectable rows; clearing restores all final IDs in order. Assert empty groups disappear and keyboard activation still navigates. Use independent query inputs and IDs, never production labels as the oracle.
- [ ] **Run red.** From frontend: `npm test -- src/panes/settings/SettingsNav.test.tsx src/panes/settings/sections.test.ts`.
- [ ] **Implement keyword discovery and empty results.** Populate explicit terms for current controls, filter through the shared matcher, and use existing `EmptyState`. Do not introduce field-level jump routing. Preserve retained nav state and use existing widgets for any changed controls; replace touched raw navigation buttons with a suitable existing widget without changing its API.
- [ ] **Update evergreen documentation.** Describe task groups, retired-route fallback, local versus hub/host scope, the one plugin home and independent directory recovery. Do not claim mocked preview controls, new syncing, or backend changes. Keep existing deferred friction cases deferred.
- [ ] **Run green and final gates.** From frontend, run targeted settings suites and `npm run typecheck`; run `npx biome check --write` with every touched frontend path explicitly listed. From root, run `make test-web`, then `make test-web-browser` sequentially. Read the gate scripts before relying on their summaries. CI remains the source of truth for the broad repository suite; do not launch heavy gates in parallel.
- [ ] **Inspect production UI.** Use a local safe test/dev environment with the real settings components and deterministic external transport, not the standalone HTML mockup. Inspect mobile and desktop in both themes in one batch: group visibility, search/no-result, Input controls, Plugins/Sources and host selection, retired URL/Back behavior, keyboard focus and no horizontal overflow. Fix defects in one batch and confirm. Do not mutate real user settings or treat the existing authenticated hub as test data. If access/setup prevents this, report the precise limit rather than claiming visual verification.
- [ ] **Review and commit.** Run the simplify-code skill on the change, then request an independent whole-branch review against the spec, especially the five Review Focus cases. Fix findings red-first. Inspect staged diff and commit only the intended UI/tests/docs. Keep review archive, mockup and server state out of the commit.

## Completion evidence

Report the new groups, one Plugins home, General field ownership, retired URL fallback, and the exact tests/gates that actually ran. Name any browser limitation and unresolved out-of-scope issue separately. Keep the mobile prototype available unless Jesse asks to stop it; its server auto-exits after its configured idle timeout. Do not claim a deploy, push or PR unless those actions were requested and completed.

## Plan self-review

The design's navigation, preference behavior, plugin unification, General inventory, search, route policy and phone preservation map to Tasks 1–4. All five Review Focus cases have tests above. Shared signatures are defined once and reused; the steps preserve existing storage/API owners. No compatibility aliases, widget APIs, dependencies or real-settings mutations are introduced. Builder review of the design found no consequential unanswered questions; the author additionally corrected Input's mixed local/hub scope after reading the actual keyboard implementation.
