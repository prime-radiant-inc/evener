# Plugins UI Unification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One shared plugin row and one mode-aware detail sheet (installed / catalog / launcher) across the settings Installed list, the Browse tree, and the session launcher, with a new hub read that exposes each plugin's loaded components.

**Architecture:** A single-target hub read (`evener/plugin/components`) loads a plugin's `agent/plugin.Instance` from either an installed `plugin@marketplace` pair or an explicit directory path, fail-soft. The client caches it per host (settings store) and per launch (scoped read). The frontend extends `ListRow` with a trailing control on interactive rows (the one widget change, approved in the spec), then composes shared row pieces and a shared sheet body that all three surfaces render.

**Tech Stack:** Go (appwire protocol + `internal/plugins` + `cmd/evener-hub`), TypeScript/React (`cmd/evener-hub/frontend/src`, `appwire-client/typescript`), vitest + @testing-library, Biome.

**Spec:** `docs/superpowers/specs/2026-11-08-plugins-ui-unification-design.md` — read it first; this plan argues from it. Visual reference: `/dev/plugins-mockups` (branch `plugins-ui-mockups`).

## Global Constraints

- Biome runs only from `cmd/evener-hub/frontend` on touched paths (`npx biome check --write <files>`); NEVER `npx biome` from the repo root.
- `npm run typecheck` (or `make test-web`) is the type gate; a green vitest run alone typechecks nothing.
- Never run `npm ci` in this worktree: `node_modules` is a symlink to the shared install. `make test-web`'s preflight manages it.
- No test asserts on copy: never check human-readable strings against hardcoded phrases or constants imported from the code (docs/developing-evener/testing.md). Assert behavior and accessible names.
- Default tests are deterministic: no network, no provider credentials, no wall-clock budgets.
- Generated output stays fresh: after any `appwire/` change run `make generate` and commit the regenerated `types.gen.ts`; `make lint-generated` must stay green.
- Widgets import through the barrel (`src/widgets/index.ts`); a widget change lands with its gallery section and its docs/web-ui/design-system.md §3 row in the same task.
- Comments state invariants, not history; commit messages carry the history.
- Run heavy local gates one at a time (make test-web, make vet), not as parallel jobs.
- Commit after every task with the repo's conventional style (`feat(scope): …` / `test(scope): …`).

## Review Focus

The five inputs most likely to bite a person, each pinned to the task whose tests must exercise it:

1. **A broken plugin directory** (registry entry exists, load fails) — the components call succeeds with an `error` field, and the sheet shows the failure where Contents would be, never "Manifest only". Tests: Task 3 (handler) and Task 7 (sheet).
2. **A plugin that loads but ships nothing** (MCP-only, manifest-only) — Contents reads "Manifest only"; not an error, not empty-styled. Tests: Task 3 and Task 7.
3. **A catalog row whose plugin is already installed** — row shows the "installed" chip, no Install button, and its sheet opens in INSTALLED mode (full detail), not catalog mode. Tests: Task 6 (row) and Task 7 (mode selection).
4. **An older hub without the new method** — the sheet degrades to metadata with a quiet notice and a retry, never a crash or a failed pane. Tests: Task 4 (store entry error state) and Task 7 (sheet notice).
5. **A components read in flight when a mutation lands** (install/remove/upgrade from this or another client) — the cache entry is retired with the list it was keyed to; a stale sheet never shows stale contents. Tests: Task 4 (store retirement, mirroring the marketplaces browse-cache retirement tests).

---

### Task 1: AppWire — the plugin components read's types and method

**Files:**
- Modify: `appwire/types_auth_commands.go` (neighbor style: the file that declares `PluginEntry`, `PluginListResponse`, `PluginPreviewParams`)
- Modify: `appwire/protocol.go` (method constants + the METHOD TABLE around line 268)
- Generated: `appwire-client/typescript/types.gen.ts` (via `make generate`)

**Interfaces:**
- Produces (Go, consumed by Tasks 3 and 4):
  - `MethodEvenerPluginComponents` = `"evener/plugin/components"` (copy the exact constant style of `MethodEvenerPluginList`)
  - `PluginComponentsTarget{ Plugin string; Marketplace string; Path string }` — JSON tags match neighboring params types. Exactly one form is set per target: the installed pair (`plugin`+`marketplace`) XOR `path` (launch-directory and bundled candidates).
  - `PluginHookEvent{ Event string; Count int }`
  - `PluginComponents{ Skills []string; Agents []string; Commands []string; Hooks []PluginHookEvent; McpServers []string; Warnings []string }` — names and tags copy the file's own spelling conventions for MCP (see `PluginLaunchCandidate`'s field naming).
  - `PluginComponentsResult{ PluginComponentsTarget; Components *PluginComponents; Error string }` — `Components == nil` with a non-empty `Error` is a broken/unreadable target; both empty is a loadable plugin with zero components.
  - `PluginComponentsParams{ Targets []PluginComponentsTarget }` and `PluginComponentsResponse{ Results []PluginComponentsResult }` — one call carries many targets (the installed list asks for all its rows in one call); results echo their targets in order; the call itself returns 200 with per-target errors, never fails the batch.
- Produces (TS, generated): `PluginComponentsTarget`, `PluginHookEvent`, `PluginComponents`, `PluginComponentsResult`, `PluginComponentsParams`, `PluginComponentsResponse` in `types.gen.ts`.

- [ ] **Step 1: Declare the types and the method**

Add the six Go types to `appwire/types_auth_commands.go` next to the other plugin types, and add the constant + table row to `appwire/protocol.go` next to `MethodEvenerPluginList`'s row: `{MethodEvenerPluginComponents, PluginComponentsParams{}, PluginComponentsResponse{}, ScopeHub, "Returns plugins' loaded components - skills, agents, commands, hook events, and MCP server names - plus load diagnostics; one target per installed plugin by name or any directory by path, results fail-soft per target."}`.

- [ ] **Step 2: Regenerate and check freshness**

Run: `make generate && make lint-generated`
Expected: both succeed; `git status` shows only `appwire-client/typescript/types.gen.ts` (and any other generated output) updated.

- [ ] **Step 3: Run the appwire suite**

Run: `go test ./appwire/... -count=1`
Expected: PASS (if any protocol-coverage test enumerates methods and fails, add the new method to that table — the test failure names it).

- [ ] **Step 4: Commit**

```bash
git add appwire/types_auth_commands.go appwire/protocol.go appwire-client/typescript/types.gen.ts
git commit -m "feat(appwire): evener/plugin/components read for plugin component inventories"
```

---

### Task 2: internal/plugins — Manager.Inspect loads one plugin for display

**Files:**
- Create: `internal/plugins/components.go`
- Test: `internal/plugins/components_test.go` (fixture dirs under `internal/plugins/testdata/` follow the existing naming; add new fixture dirs there if the existing ones don't cover the cases below)

**Interfaces:**
- Consumes: `agent/plugin.Load(dir string) (Instance, error)`; the registry items `resolveForLaunch` iterates (see `resolve.go`'s installed-candidates path, which reads `item.InstallPath`, `item.Plugin`, `item.Marketplace`).
- Produces (consumed by Task 3):
  - `type InspectTarget struct { Plugin, Marketplace, Path string }` — installed pair XOR path.
  - `func (m *Manager) Inspect(ctx context.Context, target InspectTarget) (plugin.Instance, error)` — resolves the target to a directory (`item.InstallPath` from the registry for the installed form, `target.Path` directly) and returns `plugin.Load`'s result. A target that matches nothing returns a wrapped "not found" error; the caller (Task 3) maps any error to the response's `Error` field — fail-soft lives there, so this method does no swallowing.

- [ ] **Step 1: Write the failing test**

Test cases (one `t.Run` each, real fixture dirs, no mocks):
1. A fixture plugin dir with at least one skill, one agent, one command, and an `.mcp.json` with one server: `Inspect` with `Path` returns an `Instance` whose `Skills`/`Agents`/`Commands`/`MCPConfigs` maps are non-empty.
2. An installed-style target: register a fixture plugin in a temp store (follow `install_test.go`/`resolve_test.go` harness), then `Inspect` with the pair returns the `Instance` from its `InstallPath`.
3. A broken dir (a directory with no manifest): `Inspect` returns an error and the empty `Instance` — the error is the evidence, and the test asserts it is non-nil.
4. A target matching nothing: non-nil error.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/plugins/ -run TestInspect -count=1`
Expected: FAIL (undefined: Inspect / InspectTarget).

- [ ] **Step 3: Implement `Inspect`**

Small body: resolve the target to a directory (registry lookup for the installed form — reuse the same registry iteration `resolveForLaunch` uses for installed entries, do not add a second registry API; `target.Path` directly otherwise), then delegate to `agent/plugin.Load`. One comment stating the invariant: the method performs no fail-soft — error mapping is the hub handler's job.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/plugins/ -run TestInspect -count=1 && go test ./internal/plugins/ -count=1`
Expected: PASS both.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/components.go internal/plugins/components_test.go internal/plugins/testdata/
git commit -m "feat(plugins): Manager.Inspect loads one plugin directory for display"
```

---

### Task 3: Hub handler — evener/plugin/components + the S17 product row

**Files:**
- Modify: `cmd/evener-hub/app_plugins.go` (the `hubPluginsController`)
- Modify: `cmd/evener-hub/app_rpc.go` (registration, next to the other plugin `appserver.HandleTyped` calls around line 2209)
- Test: `cmd/evener-hub/app_plugin_components_test.go` (new; harness follows `app_plugin_autoupgrade_test.go`)
- Modify: `docs/product/subsystems.md` — the S17 row's hub surface sentence gains: the hub exposes a per-plugin component read (skills, agents, commands, hook events, MCP servers, load diagnostics) for installed plugins and launch directories, and the settings and launcher surfaces render one shared row and sheet.

**Interfaces:**
- Consumes: Task 1's `PluginComponentsParams`/`PluginComponentsResponse`; Task 2's `Manager.Inspect` + `InspectTarget`.
- Produces (consumed by Task 4): the live `evener/plugin/components` RPC: params carry a `targets` array (each `{"plugin","marketplace"}` or `{"path"}`); `results` echo the targets in order, each with `components` (loadable) or `error` (broken/unreadable). **The wire carries UN-namespaced names**: skills/agents/commands strip the `"plugin-name:"` prefix (`strings.Cut`, first `":"`), MCP servers strip the `"plugin_<plugin-name>_"` prefix, hooks become `PluginHookEvent{Event, Count}` per event with the count of `RegisteredHook`s for that event, and `Warnings` concatenates `MCPConfigWarnings`, `SkippedComponent` reasons, and unsupported/unknown-hook notes (format follows the loader's own strings).

- [ ] **Step 1: Write the failing test**

Follow `app_plugin_autoupgrade_test.go`'s harness (controller over a temp plugin root). Cases, issued as one call with a multi-target `targets` array wherever the case allows:
1. An installed fixture plugin: its result lists the bare skill names, the bare MCP server names, and hook events with counts; `error` empty.
2. A broken registered dir (registered by the harness, no manifest on disk): `components` nil, `error` non-empty, and the batch result still contains the other targets' components.
3. A `path` target pointing at a launch-directory fixture: same shape as case 1.
4. A target that is neither a known pair nor a readable path: `error` non-empty, call still succeeds.
5. An empty or malformed target (both forms set, or neither): the call returns a params error for the whole request — that is a caller bug, not a broken plugin.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/evener-hub/ -run TestPluginComponents -count=1`
Expected: FAIL (no such handler / method).

- [ ] **Step 3: Implement the handler and registration**

`func (c *hubPluginsController) Components(ctx context.Context, params appwire.PluginComponentsParams) (appwire.PluginComponentsResponse, error)`: validate every target (whole-request error on a malformed one), loop `Manager.Inspect` per target, map each `Instance` → wire per the Interfaces block, map any `Inspect` error to that result's `error` field. Register with `appserver.HandleTyped` beside `MethodEvenerPluginList`.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./cmd/evener-hub/ -run TestPluginComponents -count=1 && go test ./cmd/evener-hub/ -count=1`
Expected: PASS (the full package run takes a while; if unrelated tests fail on this loaded machine, re-run only them once before believing a regression).

- [ ] **Step 5: Update the S17 product row and commit**

```bash
git add cmd/evener-hub/app_plugins.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_plugin_components_test.go docs/product/subsystems.md
git commit -m "feat(hub): evener/plugin/components handler; S17 row notes the read"
```

---

### Task 4: Client package — the scoped read input and the settings-store cache

**Files:**
- Modify: `appwire-client/typescript/scopedHostRead.ts` (+ its test file, which exists beside it)
- Modify: `appwire-client/typescript/state/extensions/plugins.ts` and `plugins.test.ts`
- Modify: `appwire-client/typescript/state/extensions/index.ts` (barrel re-exports, follow the existing pattern)

**Interfaces:**
- Consumes: generated `PluginComponentsParams`/`PluginComponentsResponse` (Task 1); the `hostRequest` + `ScopedHostReadInputs` pattern of `pluginPreviewReadInputs`; the `keyedRevision`/retirement pattern of `marketplaces.ts`'s `browseCatalogs`.
- Produces (consumed by Tasks 6, 7 and 8):
  - In `scopedHostRead.ts`: `export interface PluginComponentsReadArgs { client: Pick<AppwireClientLike, "request">; host?: string; target: { plugin: string; marketplace: string } | { path: string } }` and `export function pluginComponentsReadInputs(args: PluginComponentsReadArgs): ScopedHostReadInputs<PluginComponentsResponse>` — `inputKey` is `JSON.stringify(args.target)`; the read calls `hostRequest(client, host, "evener/plugin/components", { targets: [target] })` (the launcher reads one target; the method is batch-shaped so this stays one call) and unwraps `response.results[0]` (the store and the hook both tolerate an empty `results` array as an error entry).
  - In `state/extensions/plugins.ts`: `PluginsState` gains `pluginComponents: Map<string, PluginComponentsEntry>` keyed `"<plugin>@<marketplace>"` or `"path:<path>"`, where `PluginComponentsEntry = { status: "loading" } | { status: "loaded"; components: PluginComponents } | { status: "error"; error: string }` (export the type), and the action `fetchPluginComponents(targets: ReadonlyArray<{ plugin: string; marketplace: string } | { path: string }>): Promise<void>` — a FETCH convention (never throws; state tracks it). One in-flight request per set of not-yet-cached keys: the installed list's single batch call coalesces concurrent calls for the same missing keys. `fetchPlugins`, once the fresh list lands, issues one `fetchPluginComponents` for every installed entry's pair key (the list's rows show counts without per-row reads). Every plugin-list mutation (install, upgrade, remove) retires all `pluginComponents` entries the same way `marketplaces.ts` retires `browseCatalogs` on its publication bump; the list-triggered batch then re-fetches on the next `fetchPlugins`.

- [ ] **Step 1: Write the failing tests**

In `scopedHostRead`'s test file: `pluginComponentsReadInputs` issues `evener/plugin/components` with `targets: [pair]` and `targets: [path]`, on the local host and through a remote host's request path (follow the file's existing fake-client assertions — they assert method + params shape, never copy).
In `plugins.test.ts` (follow its existing fake-client store harness):
1. `fetchPluginComponents` with two pair targets lands both `loaded` entries under the right keys.
2. A concurrent second call for a key still loading coalesces into the first request, not a second one.
3. A broken-plugin result (`error` set on one target) lands that key as an `error` entry with the string, and its sibling target still lands `loaded`.
4. A hub that errors the method (older hub) lands `error` entries for every requested key — the store does not throw.
5. An install mutation's updated-list landing retires the cache; the next `fetchPlugins` re-issues the batch and re-lands entries.
6. `fetchPlugins` landing a fresh list issues the batch read for the list's pair keys.

- [ ] **Step 2: Run to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/scopedHostRead.test.ts ../../../appwire-client/typescript/state/extensions/plugins.test.ts`
Expected: FAIL (undefined exports).

- [ ] **Step 3: Implement both halves**

Follow `pluginPreviewReadInputs`'s exact shape for the read input; follow `browseMarketplace`'s exact load-once/retire patterns for the cache (the retirement helper mirrors `retireBrowseCatalogs`).

- [ ] **Step 4: Run to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/scopedHostRead.test.ts ../../../appwire-client/typescript/state/extensions/plugins.test.ts && npx biome check --write ../../../appwire-client/typescript/scopedHostRead.ts ../../../appwire-client/typescript/state/extensions/plugins.ts`
Expected: PASS + clean (Biome runs from the frontend directory over the touched package files — its enforced scope includes them).

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/scopedHostRead.ts appwire-client/typescript/scopedHostRead.test.ts appwire-client/typescript/state/extensions/plugins.ts appwire-client/typescript/state/extensions/plugins.test.ts appwire-client/typescript/state/extensions/index.ts
git commit -m "feat(appwire-client): plugin components read input and settings-store cache"
```

---

### Task 5: ListRow — a trailing control on interactive rows

**Files:**
- Modify: `cmd/evener-hub/frontend/src/widgets/listrow/index.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/listrow/listrow.module.css`
- Test: `cmd/evener-hub/frontend/src/widgets/listrow/listrow.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/dev/gallery-sections/listrow.tsx`
- Modify: `docs/web-ui/design-system.md` — the §3 ListRow row (around line 564) and its "an interactive row cannot take `action` at all" clause

**Interfaces:**
- Consumes: the existing `ListRowProps` union; `Chevron`.
- Produces (consumed by Task 6): the interactive variant gains `trailingAction?: ReactNode`. **Conditional DOM, approved in the spec:** when `trailingAction` is undefined, interactive rows render exactly today's markup (whole row one `<button>`); when defined, the row renders as a container `<div>` (the row's own frame/hover/focus-within classes) whose main area is a real `<button>` (leading, label, chips, meta) and whose trailing span holds the `trailingAction` followed by the drill-in chevron. No nested buttons. Two tab stops is the accepted keyboard shape (main, then control). Clicking the trailing control must not fire `onSelect`.

- [ ] **Step 1: Write the failing tests**

Extend `listrow.test.tsx` (follow its existing role/accessible-name style):
1. An interactive row with `trailingAction` renders both the control and the chevron; the row root is not itself a button; the main area is a button whose accessible name is the label.
2. Clicking the main area fires `onSelect`; clicking the trailing control does not fire `onSelect`.
3. An interactive row without `trailingAction` renders today's single-button row (the test the existing suite already writes for — keep it green).
4. Keyboard: Tab reaches the main button, then the control; Enter on the main button fires `onSelect`.

- [ ] **Step 2: Run to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/listrow/listrow.test.tsx`
Expected: FAIL on the new cases.

- [ ] **Step 3: Implement the conditional variant**

Extend `InteractiveListRowProps` with `trailingAction?: ReactNode`; add the container/main/trailing classes to the CSS module using tokens only (the main button inherits the app font, keeps the inset focus ring and the `--tap-min` floor; the container keeps the row frame, `--hover-1` wash on hover/focus-within). The comment at the variant states the invariant: the container is never a button; the control swallows its own clicks.

- [ ] **Step 4: Update the gallery section and the design-system row**

`dev/gallery-sections/listrow.tsx`: add one interactive row with a trailing `Button` beside today's rows (both states visible). `docs/web-ui/design-system.md` §3 ListRow row: document `trailingAction` on the interactive variant and replace the "cannot take `action` at all" clause with the two-shape rule (whole-row button without it; container with main-button + control with it).

- [ ] **Step 5: Run the widget, gallery, and layout guards**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/listrow/listrow.test.tsx src/dev/WidgetGallery.test.tsx && npx biome check --write src/widgets/listrow/index.tsx src/widgets/listrow/listrow.module.css src/widgets/listrow/listrow.test.tsx src/dev/gallery-sections/listrow.tsx && npm run layoutguard`
Expected: PASS, Biome clean, layoutguard green (if its listrow cases pin the old DOM and fail, update the case's expectation to the new two-shape contract — the failure names the file).

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/frontend/src/widgets/listrow/ cmd/evener-hub/frontend/src/dev/gallery-sections/listrow.tsx docs/web-ui/design-system.md
git commit -m "feat(widgets): ListRow interactive rows take a trailing control (spec-approved)"
```

---

### Task 6: Shared plugin rows — panes/plugins/ and the settings lists

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/plugins/pluginCounts.ts` — `countsSummary(skills: number, agents: number, commands: number, hooks: number, mcpServers: number): string` in the launcher's existing pluralized vocabulary ("7 skills · 9 MCP servers", "no components" when all zero). Move `PluginSelectionPanel.tsx`'s `componentCounts` body here and have the panel import it.
- Create: `cmd/evener-hub/frontend/src/panes/plugins/pluginStatusChips.tsx` — moved from `panes/settings/sections/marketplacesPlugins/pluginStatusChips.tsx` (same component, new path; update the two existing importers).
- Create: `cmd/evener-hub/frontend/src/panes/plugins/PluginRow.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/plugins/pluginRow.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/InstalledSection.tsx` and its test
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/BrowseSection.tsx` and its test
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/PluginSelectionPanel.tsx` (import `countsSummary`; no visual change yet)

**Interfaces:**
- Consumes: Task 5's `trailingAction`; `pluginStatusChips`; the wire types `PluginEntry`, `MarketplaceCatalogPlugin`; Task 4's `PluginComponentsEntry` and `countsSummary`.
- Produces (consumed by Tasks 7, 8, and the two lists):
  - `export function InstalledPluginRow({ entry, components, onOpen }: { entry: PluginEntry; components?: PluginComponentsEntry; onOpen: () => void }): ReactElement` — `ListRow` with `leading` StatusDot (broken → failed, !enabled → ended, else idle), `chips` from `pluginStatusChips`, `meta` = `` `@ ${entry.marketplace} · v${entry.version || "unknown"}` `` plus ` · ${countsSummary(...)}` when `components?.status === "loaded"` (absent or loading renders today's identity-only meta — the list-triggered batch from Task 4 fills it in, and the row does not fetch).
  - `export function CatalogPluginRow({ plugin, installed, onOpen, onInstall, installDisabled }: { plugin: MarketplaceCatalogPlugin; installed: boolean; onOpen: () => void; onInstall: () => void; installDisabled: boolean })` — `ListRow` interactive with `trailingAction` = an Install `Button` (size `sm`) when `!installed`, chips = an `installed`-kind `Chip` when `installed`, meta = `` `${description}${category ? ` · ${category}` : ""}` ``.
  - `InstalledSection` reads `useExtensionsHostState((s) => s.pluginComponents)` and passes each row its cache entry (the section itself never fetches — Task 4's `fetchPlugins` chain does); `BrowseSection` replaces its hand-composed row divs with `CatalogPluginRow` (its `isInstalled` helper already exists).

- [ ] **Step 1: Write the failing tests**

`pluginRow.test.tsx` (plain props, no store):
1. `InstalledPluginRow` with a `broken` entry renders the failed dot and the status chips; with a `loaded` cache entry its meta contains the marketplace, the version, and the counts (assert the counts segment equals `countsSummary(...)`'s output for those numbers, which the test computes, not hardcodes); with no cache entry it renders identity-only meta.
2. `CatalogPluginRow` not installed renders an Install button and the chevron, both reachable, button click firing `onInstall` not `onOpen`, main area firing `onOpen`.
3. `CatalogPluginRow` installed renders the installed chip, no Install button.
Update `InstalledSection.test.tsx`: rows show counts in meta (drive via the section with a seeded store — its harness exists). Update `BrowseSection.test.tsx`: rows are the shared shape (Install action + installed chip cases; the existing tests for install flow keep passing).

- [ ] **Step 2: Run to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/plugins/pluginRow.test.tsx src/panes/settings/sections/marketplacesPlugins/InstalledSection.test.tsx src/panes/settings/sections/marketplacesPlugins/BrowseSection.test.tsx`
Expected: FAIL (module missing / expectations).

- [ ] **Step 3: Implement the shared pieces and rewire the lists**

Move, then compose, per the Interfaces block. `InstalledSection`'s row body shrinks to the `InstalledPluginRow` call; `BrowseSection`'s `MarketplaceNode` rows shrink to `CatalogPluginRow` (keep its loading/error/empty children as they are).

- [ ] **Step 4: Run to verify they pass, then Biome**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/plugins/ src/panes/settings/sections/marketplacesPlugins/ src/panes/spawn/PluginSelectionPanel.test.tsx && npx biome check --write src/panes/plugins/ src/panes/settings/sections/marketplacesPlugins/InstalledSection.tsx src/panes/settings/sections/marketplacesPlugins/BrowseSection.tsx src/panes/spawn/PluginSelectionPanel.tsx && npm run typecheck`
Expected: PASS, clean, typecheck green.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/plugins/ cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/ cmd/evener-hub/frontend/src/panes/spawn/PluginSelectionPanel.tsx
git commit -m "feat(hub): shared plugin rows; settings lists render from them"
```

---

### Task 7: The detail sheet — modes, Contents, and the broken/empty/older-hub states

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/plugins/PluginContents.tsx` (+ colocated test)
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/PluginDetailSheet.tsx` and its test
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/index.tsx` and `index.test.tsx`

**Interfaces:**
- Consumes: Task 4's store cache + `fetchPluginComponents`; the `Sheet`/`PropertyList`/`Badge`/`Eyebrow`/`InlineNotice` widgets; `countsSummary`.
- Produces (consumed by Task 8):
  - `PluginContents({ components }: { components: PluginComponents })` — the spec's Contents block: an `Eyebrow` "Contents" then one grid row per present kind (Skills / Agents / Commands / Hooks / MCP servers): kind label, `Badge` count, mono names (first eight; a `Button variant="link"` `+N more` expands — stateful inside the component). Hooks list `` `${event} (${count})` ``. All-absent reads "Manifest only". This component is the shared sheet body piece; Task 8 reuses it.
  - `PluginDetailSheet` gains `mode: "installed" | "catalog"`, keeping today's installed behavior byte-for-byte where unchanged: installed mode adds the Contents block between description and metadata (a broken plugin — cache entry `error` — renders a danger `InlineNotice` with that error where Contents would be; an `error` entry from an older hub renders a default-tone `InlineNotice` with a Retry `Button` re-calling `fetchPluginComponents`), catalog mode renders description + metadata (Marketplace, Source, Author when present) + Install footer (the existing `ConfirmDialog` flow), no switches, no Contents (spec decision 2).
  - `index.tsx`: a catalog row's `onSelect` opens the sheet; the sheet target union widens to installed-or-catalog (an already-installed catalog row resolves to its installed entry and opens in installed mode — the `isInstalled` helper again).

- [ ] **Step 1: Write the failing tests**

`PluginContents.test.tsx`: a components object with 9 MCP servers renders nine names after expanding `+1 more` (drive the expansion by role/name); a manifest-only object renders the empty case; a hooks list renders event-with-count rows.
`PluginDetailSheet.test.tsx` (extend the existing suite): installed mode with a loaded cache entry renders the Contents block and today's switches/footer; a broken entry (error in cache) renders the failure notice and still offers Remove; an older-hub error entry renders the degrade notice with a working Retry; catalog mode renders no switches and an Install footer wired to the existing confirm flow.
`index.test.tsx`: a catalog row opens the sheet in catalog mode; an already-installed catalog row opens it in installed mode.

- [ ] **Step 2: Run to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/plugins/PluginContents.test.tsx src/panes/settings/sections/marketplacesPlugins/PluginDetailSheet.test.tsx src/panes/settings/sections/marketplacesPlugins/index.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement per the Interfaces block**

The sheet fetches components lazily on open (installed mode only) via `fetchPluginComponents([target])`; the existing entry-vanished close behavior stays for installed mode and is skipped for catalog mode.

- [ ] **Step 4: Run to verify they pass, then Biome + typecheck**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/plugins/ src/panes/settings/sections/marketplacesPlugins/ && npx biome check --write src/panes/plugins/PluginContents.tsx src/panes/settings/sections/marketplacesPlugins/ && npm run typecheck`
Expected: PASS, clean, green.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/plugins/PluginContents.tsx cmd/evener-hub/frontend/src/panes/plugins/PluginContents.test.tsx cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/
git commit -m "feat(hub): mode-aware plugin detail sheet with contents, failure and degrade states"
```

---

### Task 8: The launcher — Details action, launcher-mode sheet

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/spawn/usePluginComponents.ts` (one-file hook mirroring `usePluginPreview.ts`)
- Create: `cmd/evener-hub/frontend/src/panes/spawn/LauncherPluginSheet.tsx` (+ colocated test)
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/PluginSelectionPanel.tsx` and its test

**Interfaces:**
- Consumes: Task 4's `pluginComponentsReadInputs` (through a `usePluginComponents` hook exactly shaped like `usePluginPreview`: `useScopedHostRead(pluginComponentsReadInputs(args))`); Task 7's `PluginContents`; `Sheet`, `Switch`.
- Produces:
  - `LauncherPluginSheet({ open, onClose, candidate, selected, onToggleSelected })` — sheet in launcher mode: title = candidate name, `PluginContents` (via the hook; the candidate's `marketplace`+`name` or `path` picks the read target), PropertyList (Version when present, Marketplace, Source), one `Switch` "Included in this session" bound to `selected`/`onToggleSelected`, no footer actions.
  - `PluginSelectionPanel`'s `LauncherSwitchRow` description gains a `Button variant="link" size="xs"` Details action as its last element; the panel owns the open sheet's state (one open at a time).

- [ ] **Step 1: Write the failing tests**

`PluginSelectionPanel.test.tsx` (extend): each row's description ends in a Details action that opens the sheet for that candidate; the sheet's switch reflects and toggles the row's selection; the sheet's Contents come from the scoped read (fake client per the spawn harness — `usePluginPreview` tests show the pattern).
`LauncherPluginSheet.test.tsx`: a directory-source candidate (no marketplace) reads by `path`; an erroring read degrades with the quiet notice (same behavior class as Task 7's older-hub case).

- [ ] **Step 2: Run to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/spawn/PluginSelectionPanel.test.tsx src/panes/spawn/LauncherPluginSheet.test.tsx src/panes/spawn/usePluginComponents.test.ts 2>/dev/null || npx vitest run src/panes/spawn/`
Expected: FAIL on the new cases.

- [ ] **Step 3: Implement per the Interfaces block**

- [ ] **Step 4: Run to verify they pass, then Biome + typecheck**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/spawn/ && npx biome check --write src/panes/spawn/ && npm run typecheck`
Expected: PASS, clean, green.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/spawn/
git commit -m "feat(hub): launcher plugin rows open the shared detail sheet via Details"
```

---

### Task 9: Whole-branch verification

**Files:** none (verification only; fix-forward anything this turns up in the tasks' own files)

- [ ] **Step 1: Full frontend gate**

Run: `make test-web` (from the repo root; this runs the token contract, gallery completeness, typecheck, and Biome — the gates Tasks 5-8 already ran piecemeal, now together)
Expected: PASS.

- [ ] **Step 2: Go gates, one at a time**

Run: `go test ./appwire/... ./internal/plugins/... -count=1`, then `go test ./cmd/evener-hub/ -count=1`, then `make vet`
Expected: PASS each.

- [ ] **Step 3: Confirm the mockup page still renders the shipped design**

Run: `cd cmd/evener-hub/frontend && EVENER_DEV_ALLOWED_HOSTS=magic-kingdom npm run dev -- --host 0.0.0.0 --port 5199 --strictPort` and open `/dev/plugins-mockups?section=sheets` in a browser
Expected: the page renders (it renders the real widgets, so it exercises the shipped components as a smoke check).

- [ ] **Step 4: The whole-branch review this plan's execution skill prescribes**
