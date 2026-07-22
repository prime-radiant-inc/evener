# M10 Kill List — Legacy Web UI Deletion Inventory

**Date:** 2026-07-22
**Worktree / branch:** `.claude/worktrees/webui-workspace-shell` / `worktree-webui-workspace-shell`
**Tree state:** HEAD `06337983f` (`sdd: W6 fix round done, review out; T6 staged`)

> **THIS LIST IS NOT YET FINAL.** Waves 6 (spawn/palette) and 8 (periphery: doc viewer,
> `/thread` single-pane, PWA) are still landing. Two of the deletion decisions below (the
> orphaned-REST cluster in §1.6, and the `/doc/*` routes in §4) depend on what those waves
> wire up. **Re-run the verification greps in the Appendix against the final post-wave-8 tree
> before executing any deletion.** This document is written to be re-validated, not trusted blind.

Authority for the deletion is the approved design spec §10:
`docs/superpowers/specs/2026-07-20-webui-workspace-shell-rewrite-design.md`. This document turns
that one-paragraph mandate into a file- and symbol-level inventory, cross-referenced against the
two things that must not break: the **new SPA** (`cmd/serf-hub/frontend/src`) and the **TUI / Go
REST client** (`hubapi.Client`).

---

## 0. Headline counts

| Category | Count |
|---|---|
| Whole files to delete | **262** (33 `assets/*.js` + 1 `assets/style.css` + 25 `templates/**` + 203 `jstest/**`) |
| Whole **Go** files to delete | **0** — every Go deletion is a surgical excision inside a file that also holds shared code |
| Go surgical sites | **~31** functions removed or reduced to `serveSPAIndex`, across **11** `.go` files, plus embed/struct cleanup |
| Shared endpoints that MUST survive | **24** (see §2) — a shared endpoint on this kill list is the catastrophic failure mode |
| Lines deleted (approx.) | 19,725 JS + 5,951 CSS + 2,772 templates + 30,723 jstest ≈ **59k** |

---

## 1. Deletion inventory

### 1.1 Static assets — `cmd/serf-hub/assets/` (33 `.js` DELETE + `style.css` DELETE; 5 files KEEP)

Per spec §10: `assets/*.js` (all) and `assets/style.css`. The glob is deliberately `*.js` +
`style.css`, **not** `assets/*` — the PWA icons and manifest are excluded and **survive** (see
§2.4). Verification: no non-test `.go` file names any of these JS files (they are referenced only
by `templates/app.html` + `templates/thread.html`, themselves deleted). The 6 endpoints these JS
files reached over REST are handled in §1.6.

**Vendored libraries (DELETE):**
| File | Role |
|---|---|
| `htmx.min.js` | The entire htmx runtime — the legacy fragment-swap engine. `git grep htmx` must return nothing after M10. |
| `marked.min.js` | Vendored markdown parser (SPA ships its own `marked` via npm). |

**Core renderer stack (DELETE):**
| File | Role |
|---|---|
| `renderer.js` (343 KB) | The 7k-line hard-singleton transcript renderer — the thing the rewrite replaces. |
| `renderer-format.js` | Transcript formatting helpers (markdown, timestamps, token/cost). |
| `renderer-panels.js` | Panel/detail renderers; also a legacy `/api/search` caller. |
| `renderer-tools.js` | Per-tool card renderers (read/grep/shell/web/job). |
| `appwire.js` (45 KB) | Legacy hand-written AppWire WebSocket client (SPA has `src/protocol/client.ts`). |
| `pending.js` | Optimistic pending-message registry (ported to `stores/threads.ts`). |
| `thread-state.js`, `skeleton.js`, `toast.js`, `focus-trap.js`, `icons.js`, `theme.js` | Small legacy UI primitives; all re-implemented as SPA widgets/modules. |

**Sidebar / navigation / spawn (DELETE):**
| File | Role |
|---|---|
| `sidebar.js` (68 KB) | Legacy keyed-DOM sidebar tree. |
| `search.js` (45 KB) | ⌘K palette + session search; legacy caller of `/api/search` and `/api/upgrade`. |
| `spawn.js` (78 KB) | Spawn flow; legacy caller of `/api/git/head`, `/api/dirs/create`, `/api/path/validate`. |
| `panes.js` | iframe/postMessage multi-pane host — replaced by dockview (design §6.4). |
| `dir-picker.js` | Directory picker for spawn. |
| `launchconfig.js` (41 KB) | Launch-override editor; legacy caller of `/api/path/validate`. |
| `drafts.js` | Per-session composer drafts (ported to SPA localStorage). |
| `composer-attachments.js` | Image paste/drag-drop for the composer. |
| `model-switch.js`, `model-display.js` | Mid-session model switch UI; legacy caller of `/api/sessions/{ref}/reasoning-effort`. |
| `notifications.js` | OS-notification + favicon-badge single-tab election (ported to SPA `notifications` module). |
| `diagnostics.js` | Connection/diagnostics overlay. |
| `plugins.js` | Settings plugins glue. |

**Settings section scripts (DELETE):** `settings-appearance.js`, `settings-display.js`,
`settings-notifications.js`, `settings-pickers.js`, `settings-shell.js`, `settings-transcript.js`
— per-section legacy settings UI, all re-implemented under `frontend/src/panes/settings`.

**Stylesheet (DELETE):** `style.css` (5,951 lines) — the entire legacy design system; SPA ships
`frontend/src/styles/*` + per-component CSS modules, built to `/webassets/index-*.css`.

**KEEP (do NOT delete — see §2.4):** `icon-192.png`, `icon-512.png`, `icon-maskable-512.png`,
`icon.svg`, `manifest.webmanifest`.

### 1.2 Templates — `cmd/serf-hub/templates/` (all 25 `.html` DELETE)

Spec §10: `templates/` (all). Every template is consumed only by the SSR/fragment handlers in
§1.5, all of which are deleted or reduced to `serveSPAIndex`.

- `app.html` — legacy app shell (loads the 33 JS files + style.css).
- `thread.html` — standalone `/thread/{ref}` chrome-less document (replaced by SPA single-pane mode).
- `partials/workspace.html`, `partials/workspace_empty.html`, `partials/input_strip.html` — the workspace/launchpad/status-strip fragments.
- `partials/spawn.html` — spawn fragment.
- `partials/credentials.html` — credentials fragment.
- `partials/settings.html` + `partials/settings/{general,theme,transcript,display,notifications,providers,agents,launch-serf,launch-codex,inrepo,plugins,plugins-manager,skills,mcp,hub,storage,project}.html` (17 files) — the settings section fragments.

### 1.3 JS test suite — `cmd/serf-hub/jstest/` (all 203 files DELETE)

Spec §9/§10: the jstest suite tested the old DOM and is not ported. 201 `.js` + 1 `.sh` + 1 `.md`
= 203 files (30,723 JS lines). Verification: **no Makefile target and no `.go` file references
`jstest`** — it never ran in CI, so deletion needs no build-wiring change.

### 1.4 Go — delete-whole-file

**None.** Every legacy Go handler shares its file with code that backs the surviving `/rpc`
AppWire plane or the kept REST routes. All Go deletion is surgical (§1.5–1.6).

### 1.5 Go — surgical excision (legacy SSR / fragment / form-POST)

Reachability was verified by grepping every call site (Appendix A). Each function below is
reachable **only** through a legacy route.

**`web_session.go` — delete these 7 functions entirely** (the `/s/{id}/…` form-POST handlers; browser now uses AppWire `turn/*`):
| Function | Only caller | AppWire replacement (SPA) |
|---|---|---|
| `handleSteer` | `web_workspace.go:94` (`/s/{id}/steer`) | `turn/steer` |
| `handleQueue` | `web_workspace.go:100` (`/s/{id}/queue`) | `turn/queue` |
| `handleDrainAsSteer` | `web_workspace.go:106` | `turn/drainAsSteer` |
| `handlePromoteQueued` | `web_workspace.go:112` | `turn/promoteQueuedAsSteer` |
| `handleCancelQueued` | `web_workspace.go:118` | `turn/cancelQueued` |
| `handleFork` | `web_workspace.go:74` (`/s/{id}/fork`) | `thread/fork` — NB: distinct from `handleAPIFork`, which STAYS (TUI) |
| `handleAside` | `web_workspace.go:80` (`/s/{id}/aside`) | composer-side; no server route |

> `web_session.go` KEEPS: `handleSend` (also reached by `/api/sessions/{ref}/send` — TUI) and
> `handleSessionAction` (also reached by `/api/sessions/{ref}/{interrupt,compact,shutdown}` — TUI).
> Delete only the legacy `/s/{id}/…` dispatch cases that call them (in `web_workspace.go`).

**`web.go` — delete these 3 functions + their route registrations:**
| Function | Route |
|---|---|
| `handleInternalPartial` | `/_partials/` (line 184) — "the only route family that serves HTMX fragments" |
| `handleSessionPartial` | dispatched from `handleInternalPartial` (`/_partials/s/…`) |
| `handleWorkspaceEmpty` | dispatched from `handleInternalPartial` (`/_partials/workspace/empty`) |

**`web_workspace.go` — delete these 5 functions** (HTML fragment renderers), keep the data layer:
| Delete | Keep (feeds kept REST) |
|---|---|
| `renderWorkspacePartial`, `workspaceDataForRender`, `renderThreadDocument`, `renderDetailsPanel`, `renderInputStrip` | `workspaceData`, `workspaceDataFromAppThread`, `apiSessionCapabilities`, `fillObserverLink`, and the `WorkspaceData` struct — all consumed by `apiSessionDetail`/`apiSessionState` (`web_api_tree.go:30,785,889`), which serve the TUI's `/api/sessions/{ref}` + `/tasks`. |
| `renderSessionTasks` — **KEEP.** Named `render*` but returns **JSON**, and is reached by `/api/sessions/{ref}/tasks` (TUI keep-list). Delete only its legacy `/_partials/s/{id}/tasks` dispatch. | |

**`web_settings.go` — delete `renderSettingsPartial` + `renderProjectSettingsPartial`; keep the shared helpers:**
- KEEP `builtinAgentNames`, `settingsSpawnTimeoutDisplay` — consumed by `hubSettingsOverview` (the AppWire `serf/settings/overview` replacement, `app_rpc_settings_overview.go:58,68`).
- `handleSettings` → reduce to `serveSPAIndex` (§3).

**`web_launchconfig.go` — delete `handleCredentialsPartial`** (`/_partials/credentials`); reduce `handleCredentials` to `serveSPAIndex`.

**`web_spawn.go` — delete `handleWorkspaceSpawn`** (the `/_partials/workspace/spawn` fragment). KEEP `handleApiSpawn`, `handleApiModels` (TUI: `/api/spawn`, `/api/models`).

**`app_subagent_preview.go` — delete `handleSubagentPreview`** (the `/_api/subagent-preview` REST/htmx route) **and its writeJSON helper if orphaned**. **KEEP** `subagentPreviewFromThread`, `subagentPreviewItem`, `clampSubagentPreviewLimit`, and the constants — they are shared with the AppWire `serf/subagentPreview` handler (`app_rpc.go:224`), which the new protocol keeps.

**`web_types.go` — delete the legacy template-data + legacy-request structs** (verify with
`staticcheck U1000`/`deadcode` after the handlers above are removed — Go does not flag unused
package-level types):
- Template data (only `renderSettingsPartial`/`handleWorkspaceSpawn` used them; the AppWire settings overview uses its own `appwire.Settings*` wire types): `settingsData`, `agentDisplay`, `providerDisplay`, `pluginDisplay`, `skillDisplay`, `mcpDisplay`, `projectListItem`, `spawnViewData`, `launchHarness`.
- Legacy request/response bodies (only the deleted form-POST handlers): `steerRequest`, `queueRequest`, `drainAsSteerRequest`, `promoteQueuedRequest`, `cancelQueuedRequest`, `cancelQueuedResponse`.
- **KEEP:** `WorkspaceData` (feeds REST detail), `spawnRequest`, `sendRequest`, `forkRequest`, `sessionActionRequest`, `modelsCache`, `daemonStatus`.

**`web_format.go` — mostly KEEP** (it is the data-mapping layer feeding the kept `/api/sessions`
detail/state endpoints: `workspaceDataFromAppThread`, `stateLabel`, `formatContextNumbers`,
`formatTokenCount`, `completedTurnCount`, etc.). After the render handlers are gone, run
`deadcode` to remove any genuinely template-only leaf (candidate: `formatWorkMillis`, used today
only by `inputStripTemplateFuncs`; `htmlEscape`, if `doc_serve.go`'s HTML writers are also removed
— see §4).

### 1.6 Go — orphaned REST endpoints (**REQUIRES JESSE'S EXPLICIT SIGN-OFF**)

These six REST endpoints are consumed by **neither** the SPA (which moved each to an AppWire RPC)
**nor** the TUI (`hubapi.Client` never references them — verified). Their only historical callers
were the legacy JS files being deleted in §1.1 (confirmed by grep — column 3). They fall within
the **spirit** of §10 ("every `web_*.go` block that existed only to render or serve the [legacy
UI]") but are **not named in the §10 literal glob**, and deleting a REST route is a contract
change. Recommend deletion, but **flagged separately for a conscious decision**, and re-verify
against the final wave-6/8 tree (the wave-6 spawn pane + ⌘K palette are the plausible future
consumers — today they are unimplemented).

| Endpoint | Handler (file) | Legacy JS caller (all deleted §1.1) | SPA equivalent |
|---|---|---|---|
| `GET /api/search` | `handleApiSearch` (`web_api.go:32`) | `search.js`, `renderer-panels.js` | none yet (⌘K unimplemented, Wave 6) |
| `GET /api/upgrade` | `handleAPIUpgrade` (`web_api.go:159`) | `search.js` | AppWire `serf/upgrade` (declared, uncalled) |
| `POST /api/sessions/{ref}/reasoning-effort` | `handleAPIReasoningEffort` (`web_api.go:334`) | `model-switch.js` et al. | AppWire `thread/reasoning-effort/set` |
| `POST /api/path/validate` | `handleAPIPathValidate` (`web_api.go:363`) | `spawn.js`, `launchconfig.js` | AppWire `serf/path/validate` |
| `POST /api/dirs/create` | `handleAPIDirCreate` (`web_api.go:381`) | `spawn.js` | AppWire `serf/dirs/complete` |
| `GET /api/git/head` | `handleApiGitHead` (`web_api.go:446`) | `spawn.js` | none found (verify Wave 6 spawn) |

If `/api/search` goes, also delete its exclusive helpers: `sortLiveForSearch` (`web_api.go:71`),
`searchPastTitle` (`web_format.go:185`), and the `searchResult`/`searchResponse` structs
(`web_types.go`). Remove the corresponding `mux.HandleFunc` registrations in `web.go` for each
endpoint deleted.

### 1.7 Go — embed / asset plumbing (`embed.go`) surgical

Spec §10 deletes `SERF_HUB_ASSETS_DIR`; §7.2 replaces `?v=mtime` cache-busting with Vite hashing.
| Delete | Keep |
|---|---|
| `//go:embed templates/…` + `templatesFS` + `templatesRoot()` | `//go:embed assets/*` + `assetsFS` (now embeds only the 5 icon/manifest survivors) |
| `devAssetsDir()` (reads `SERF_HUB_ASSETS_DIR`) | `assetsRoot()` — but delete its `devAssetsDir()` branch |
| `assetVersionQuery()` + `assetVersionOnce`/`assetVersionVal` (only `app.html`'s `{{assetv}}` used it) | — |
| `noStore()` (only wrapped on-disk dev assets) | — |

`webnext.go` — **KEEP** `distFS`, `serveSPAIndex`, `webassetsHandler`. `newWebEnabled()` is
removed as part of the flag flip (§3).

---

## 2. Shared-surface protection list — MUST SURVIVE

A shared endpoint on the kill list is the one catastrophic failure mode. This list was built by
**exhaustively** enumerating (a) every `fetch()`/WebSocket call in `frontend/src` and (b) every
route in `hubapi.Client` (`hubapi/client.go`, all pinned by `client_test.go` exact-path
assertions). Consumers: **S** = new SPA, **T** = TUI/`hubapi.Client`.

### 2.1 Endpoints that survive

| Endpoint | Consumers | Handler / evidence |
|---|---|---|
| `GET /rpc` (AppWire WebSocket) | **S + T** | `s.appRPC.ServeWebSocket` (`web.go:177`). SPA `src/protocol/client.ts:52`; TUI `appwire.Client` (`hub_start.go:399`). **The primary data channel for the entire new UI.** |
| `GET /api/health` | **T** (+ auth-exempt) | `handleAPIHealth`. TUI `Health()`. |
| `GET /api/tree` | **S + T** | `handleAPITree`. SPA `tree.ts:167`; TUI `Tree()`. |
| `GET /api/tree/project?key=` | **S** | `handleAPITreeProject`. SPA `tree.ts:173`. |
| `GET /api/sessions/{ref}` (detail) | **T** | `handleAPISession` bare case. TUI `Session()`. |
| `POST /api/sessions/{ref}/send` | **T** | `handleSend`. TUI `Send()`. (Spec §10 explicitly: "stays for the TUI".) |
| `GET /api/sessions/{ref}/tasks` | **T** | `renderSessionTasks` (JSON). TUI `Tasks()`. |
| `POST /api/sessions/{ref}/interrupt` | **T** | `handleSessionAction`. TUI `Interrupt()`. |
| `POST /api/sessions/{ref}/compact` | **T** | `handleSessionAction`. TUI `Compact()`. |
| `POST /api/sessions/{ref}/clear` | **T** | `handleAPIClear`. TUI `Clear()`. |
| `POST /api/sessions/{ref}/fork` | **T** | `handleAPIFork`. TUI `Fork()`. |
| `POST /api/sessions/{ref}/model` | **T** | `handleAPIModel`. TUI `SetModel()`. |
| `POST /api/sessions/{ref}/rename` | **S** | `handleAPIRename`. SPA `shell/rail/actions.ts:48`. (NOT in TUI list — SPA-only, easy to miss.) |
| `POST /api/spawn` | **T** | `handleApiSpawn`. TUI `Spawn()`. |
| `GET /api/spawn-schema` | **T** | `handleAPISpawnSchema`. TUI `SpawnSchema()`. |
| `GET /api/models` | **T** | `handleApiModels`. TUI `Models()`. |
| `POST /api/archive` | **S** | `handleAPIArchive`. SPA `actions.ts:64`. |
| `POST /api/favorite` | **S** | `handleAPIFavorite`. SPA `actions.ts:40`. |
| `POST /api/project/delete` | **S** | `handleAPIProjectDelete`. SPA `actions.ts:72`. |
| `GET /s/{ref}/images/{sha}` | **S** | `handleSessionImage` (`image_serve.go`). SPA `ImageGallery.tsx`. The workspace code comment already flags this as "consumed by the future SPA client directly". |
| `GET /manifest.webmanifest` | **S** | `handleManifest` (`web.go:262`). SPA `index.html` `<link rel="manifest">`. |
| `GET /webassets/*` | **S** | `webassetsHandler`. The SPA's own hashed Vite bundle. |
| `GET /auth` (+ the guard on every route) | **S + T** | `hubedge.HandleAuth` / `AuthGuard`. Cookie/Bearer capability flow. |
| `GET /assets/icon-192.png`, `icon-512.png`, `icon-maskable-512.png`, `icon.svg` | **S** (via manifest + OS) | Served by the `/assets/` static handler; auth-exempt (`hubedge/auth_token.go`); manifest icon targets. |

> **Danger — do not misread spec §10.** §10 names only "`/api/sessions/{ref}/send` stays for the
> TUI." That is under-specified: the **whole** `hubapi.Client` 13-route contract survives (bare
> GET, send, tasks, interrupt, compact, clear, fork, model, spawn, spawn-schema, models, tree,
> health), **plus** the SPA-only routes (tree/project, rename, archive, favorite, project/delete,
> images). A literal reading of §10 that deletes "the rest of the `/api/sessions` family" is
> catastrophic.

### 2.2 Go files/symbols that survive wholesale

- **The entire AppWire `/rpc` data plane** (NOT deletion candidates): `app_rpc.go`,
  `app_rpc_settings_overview.go`, `app_relay.go`, `app_auth.go`, `app_instances.go`,
  `app_models.go`, `app_model.go`, `app_plugins.go`, `app_plugin_autoupgrade.go`,
  `app_launch.go`, `app_threadlifecycle.go`, `app_threadlist.go`, `app_threadread.go`,
  `app_transcripts.go`, `app_compact.go`, `app_sources.go`, `app_upgrade.go`,
  `appwire_validation.go`, and the shared parts of `app_subagent_preview.go`. Both UIs speak this;
  the TUI's live-session actions ride it. Untouched by M10 except where noted in §1.5.
- **REST handlers kept:** `web_api_tree.go` (minus the reasoning-effort dispatch case if §1.6
  proceeds), `web_api_archive.go`, `web_api_favorite.go`, `web_api_project_delete.go`,
  `web_api_rename.go`, `image_serve.go`, `output_images.go`, and the kept functions in
  `web_api.go` / `web_session.go` / `web_spawn.go`.
- **Infra:** `main.go`, `main_background.go`, `config.go`, `http_recorder.go`, `token.go`,
  `openai_state_dir.go`, `transcript_limits.go`, and `internal/{hubcore,hubedge,httpsec,appsource,codexlaunch,fspaths,hostlock,launchconfig,strutil}`.
- **AppWire methods the SPA doesn't call yet** (`thread/start`, `thread/resume`,
  `serf/subagentPreview`, `serf/command/list`, `serf/projects/recent`, `serf/harnesses/list`,
  `serf/upgrade`, `serf/auth/status`, …) are protocol surface, **not** legacy web. They stay
  (some are TUI/reserved; the wave-6 spawn pane will call several). Out of M10 scope.
- **Legacy-only internal package candidate:** `internal/editorurl` is imported **only** by
  `web_settings.go` (for settings "edit" links). If `renderSettingsPartial` is deleted and the
  AppWire settings overview never uses it (`EditPath` is always empty there), `editorurl` becomes
  dead and can be removed too — verify no other importer after the settings surgery.

---

## 3. The flag flip

**Mechanism (located):** a single **environment variable**, no CLI flag, no build tag.
`cmd/serf-hub/webnext.go:16`:

```go
func newWebEnabled() bool { return os.Getenv("SERF_HUB_WEB") == "new" }
```

Default (unset, or any value ≠ `"new"`) = **legacy**. It is checked at exactly **5 page-route
call sites**, each of the form `if newWebEnabled() { serveSPAIndex(w, r, distFS()); return }`
followed by the legacy body:

| Route | Handler | Site |
|---|---|---|
| `/`, `/new` | `handleIndex` | `web.go:226` |
| `/s/{id}` (bare) | `handleSession` | `web_workspace.go:45` |
| `/thread/{ref}` | `handleThreadDocument` | `web_workspace.go:158` |
| `/settings`, `/settings/{section}` | `handleSettings` | `web_settings.go:46` |
| `/credentials` | `handleCredentials` | `web_launchconfig.go:8` |

`/webassets/*` and `/manifest.webmanifest` are already registered unconditionally (flag-independent).

### The minimal M10 flip

Make the SPA THE UI at every page route by **deleting the branch, not adding a condition**:

1. In each of the 5 handlers, delete the legacy body and the `if newWebEnabled()` guard; the
   handler becomes an unconditional `serveSPAIndex(w, r, distFS())`.
   - **Exception — `handleSession` cannot become a bare one-liner:** it must still route
     `/s/{id}/images/{sha}` → `handleSessionImage` (that image path is SPA-consumed, §2.1). Keep
     that one `default`-case branch; delete every other sub-case (`send`/`steer`/`queue`/…/`state`/
     `details`/`tasks`).
2. Delete `newWebEnabled()` from `webnext.go`.
3. Delete the now-dead legacy routes from the mux (`web.go` `Handler()`): `/_partials/`,
   `/_partials/credentials`, `/_api/subagent-preview`, and (if §1.6 approved) the orphaned
   `/api/*` registrations.
4. Delete the template parsing + template fields from `NewWebServer`/`WebServer` and
   `inputStripTemplateFuncs` (§1.5). Keep `manifestFS` (feeds `handleManifest`).

**Sequencing (safety):** the flip and the legacy-branch deletion must land **together** in the
§10 commit series. If the legacy bodies are deleted while `newWebEnabled()` still defaults to
legacy, the handlers break. Deleting the guard and the body in the same edit is the safe atomic
change. (An optional pre-M10 **M9 cutover** can instead flip only the default — e.g. treat unset
as `new` — to dogfood with legacy retained as a one-env-var rollback; M10 then removes both.)

### What happens to legacy routes → **recommend 404 (the mux default), no redirects**

- **Page routes do not disappear** — `/`, `/new`, `/s/{ref}`, `/thread/{ref}`, `/settings`,
  `/settings/{section}`, `/credentials` are all **preserved** and now serve the SPA shell (design
  §6.7). So there is no bookmark-breaking page route to redirect; client-side routing owns them.
- The only routes that go away are **non-navigable internals** — `/_partials/*`,
  `/_api/subagent-preview`, the `/s/{id}/{action}` form-POSTs, and the orphaned `/api/*`. No
  user-facing bookmark or share-link targets these; unregistering them yields a natural `404` from
  the mux. A redirect would be dead weight. **Recommendation: let them 404.** (The auth guard's
  own 401-wall behavior is unchanged and out of scope.)

---

## 4. Risks and unknowns

### R1 — The orphaned-REST cluster is a judgment call, not a mechanical deletion (§1.6)
`/api/search`, `/api/upgrade`, `/api/git/head`, `/api/dirs/create`, `/api/path/validate`,
`/api/sessions/{ref}/reasoning-effort` have **no** current consumer (SPA → AppWire; TUI → never).
But they are not in the §10 literal glob, and the **wave-6** spawn pane + ⌘K palette are
unimplemented today and are the plausible future consumers. `/api/git/head` has **no** AppWire
equivalent found — if the wave-6 spawn pane needs branch detection, it may reach for this REST
route again. **Recommendation:** delete with Jesse's explicit sign-off, but **only after wave-6/8
land**, and re-run the Appendix-B grep against the final SPA source. If wave-6 spawn is built on
AppWire (it already has `serf/path/validate` + `serf/dirs/complete` in its stores), the whole
cluster is safe to delete; if any REST caller reappears, keep that endpoint.

### R2 — `/doc/file` + `/doc/image` are in limbo, with a broken-asset trap
Spec §6.7 preserves the doc-route contract as the future doc-pane data layer, and §7.2 proxies
`/doc` in dev — **but the SPA does not call these yet** (the `doc` pane type exists in the pane
registry with "No deep link yet" and makes no network request; verified zero `/doc/` references in
`frontend/src`). They are **not** in the §10 glob, so they are not auto-deleted. Two hazards: (a)
`handleDocFile`/`handleDocImage` are the **only** page routes never gated by `SERF_HUB_WEB` (they
always serve legacy HTML today); (b) that HTML references `/assets/style.css` and
`/assets/marked.min.js`, **both deleted in §1.1** — so a doc pane, once wired, would serve HTML
with dead asset links. **Recommendation:** do **not** delete the `/doc/*` routes or their
`ResolveInRoot` containment guards; in wave 8 reshape `writeDocPage`/`writeDocMarkdownPage` to
return raw content (or a JSON body the doc pane renders) so the legacy-asset dependency dies with
the assets. Track as a wave-8 blocker, not an M10 deletion.

### R3 — `assets/` is a partial keep; a naive `rm -rf assets/` breaks PWA + auth
The 4 icons + `manifest.webmanifest` survive (§2.4): the SPA `index.html` links
`/manifest.webmanifest`; `handleManifest` reads it via `assetsRoot()`; the manifest points at
`/assets/icon-*.png`; and those 4 icon paths are the auth-exempt allowlist in
`hubedge/auth_token.go`. Deleting the whole directory breaks PWA install and un-authenticated icon
fetch. The `//go:embed assets/*` directive and the `/assets/` static handler must stay. Design
§6.8 says icons are "re-generated to the new brand tokens" — **if** that regeneration moves them to
`/webassets/`, the manifest icon URLs **and** the auth-exempt list must be updated in the same
change. Confirm the final icon location before touching the auth allowlist.

### Lesser unknowns (not blockers)
- **`/new?dir=&prompt=` prefill:** legacy `handleIndex` forwards these to the spawn fragment. The
  SPA spawn pane is wave-6/unimplemented; confirm the new spawn pane honors `?prompt=`/`?dir=` so
  the palette's `/spawn` command and the sidebar "+" button keep working.
- **Dead leaf symbols:** after the §1.5 handler removals, run `staticcheck U1000` / `deadcode`
  across `cmd/serf-hub` — Go does not error on unused package-level types/functions, so the exact
  set of now-dead structs (`web_types.go`) and helpers (`web_format.go` `formatWorkMillis`/
  `htmlEscape`, `app_subagent_preview.go` `writeJSON`) must be swept mechanically, not by eye.
  (`web.go` `validAssetPath` is NOT dead — the `/assets/` handler survives to serve the icons.)
- **Docs to update (spec §10):** `docs/serf-hub-web-routing.md`, `cmd/serf-hub/README.md`,
  `docs/web-ui/*`. Acceptance gate: `git grep htmx` returns nothing.
- **CSP tightening (spec §7.4):** drop the inline-script `'unsafe-inline'` exemption once
  templates are gone (`internal/httpsec`). Not a deletion, but part of the same milestone.

---

## Appendix A — verification method: reachability of deletion candidates (§1.5)
For every candidate legacy function, all call sites were grepped across `cmd/serf-hub/**/*.go`
(excluding `_test.go`). A function was classed **delete-whole** only when its sole caller is a
legacy route (`/_partials/*`, `/_api/*`, or a `/s/{id}/{action}` form-POST case). **Exceptions the
method caught:** `handleSend` and `handleSessionAction` have a **second** caller under
`/api/sessions/` (TUI) → reduced to "delete the legacy dispatch case, keep the function";
`renderSessionTasks` is JSON + TUI-reachable despite its `render*` name → keep; `handleFork`
(legacy) is distinct from `handleAPIFork` (kept). `WorkspaceData`/`workspaceData` are reached by
`hubTreeWorkspaceData` (REST) → keep.

## Appendix B — verification method: shared-surface cross-reference (§2)
Two independent exhaustive sweeps: (1) every `fetch(`, URL literal, and WebSocket construction in
`cmd/serf-hub/frontend/src` (SPA uses exactly 8 REST paths + `/rpc`); (2) every route template in
`hubapi/client.go` (13 paths, all pinned by `client_test.go` exact-path assertions) plus the TUI's
`appwire.Client` dial of `/rpc`. An endpoint is **protected** if it appears in either sweep. The
sweeps also proved the negatives behind §1.6: `/api/search`, `/api/upgrade`, `/api/git/head`,
`/api/dirs/create`, `/api/path/validate`, `/api/sessions/{ref}/reasoning-effort`, `/doc/file`,
`/doc/image`, `/_api/subagent-preview` appear in **neither** — their only callers are the legacy
`assets/*.js` files (grep-confirmed: `search.js`, `spawn.js`, `launchconfig.js`, `model-switch.js`,
`renderer-panels.js`) that §1.1 deletes.

## Appendix C — re-validation before execution
Waves 6 and 8 are unlanded. Before deleting, re-run against the final tree:
1. `grep -rn "newWebEnabled" cmd/serf-hub --include=*.go` — confirm still exactly the 5 sites in §3.
2. Re-sweep `frontend/src` for `fetch(`/URL literals — confirm no new REST dependency on any §1.6
   endpoint or on `/doc/*` (wave-6 spawn / wave-8 doc pane are the likely changers).
3. `grep -rn "/api/\(search\|upgrade\|git/head\|dirs/create\|path/validate\)\|reasoning-effort" cmd/serf-hub/frontend/src` — must stay empty to delete the §1.6 cluster.
4. Confirm `hubapi/client.go` still lists exactly the 13 routes in §2.1 (it is the TUI contract).
5. `git grep htmx` at the end — must be empty.
