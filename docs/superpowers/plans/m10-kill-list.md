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

---

## Appendix D — Dry-run re-validation (pre-W8-final)

**Date:** 2026-07-22
**Performed against:** worktree `webui-workspace-shell` (integration) HEAD `3907b815f`, read-only cross-reference with `webui-w8-periphery` HEAD `e3b9c188c` (branch `w8-periphery`; only T1's chokepoint/seam commits have landed there — T2-T7 streams are not yet dispatched).

Wave 6 (spawn/palette/notifications/rail) is merged to integration (`ed5057be2`, closed out at `dedcf52dd`). Wave 8 (periphery) has landed only its controller-owned T1 commits, on its own wave branch — nothing from T1 is in the integration tree yet. This re-validates Appendix C against that state and folds in the wave-6/wave-8 deltas from the dispatch brief.

### Appendix C check outcomes

1. **`newWebEnabled` sites (§3).** `grep -rn "newWebEnabled" cmd/serf-hub --include='*.go'` → still exactly 5 call sites + the definition, unchanged: `web_workspace.go:45` (`handleSession`), `web_workspace.go:158` (`handleThreadDocument`), `web.go:226` (`handleIndex`), `web_launchconfig.go:8` (`handleCredentials`), `web_settings.go:46` (`handleSettings`); definition `webnext.go:16`. **PASS, no drift.**

2. **Re-sweep `frontend/src` for `fetch(`/URL literals (§1.6, R2).** Enumerated every `fetch(` and `new WebSocket(` in non-test source. The SPA now uses **11 REST paths + `/rpc`** (was 8 at the original sweep): the original 8 (`tree`, `tree/project`, `rename`, `archive`, `favorite`, `project/delete`, `images/{sha}`, `manifest.webmanifest`) plus three new ones wave 6 added — `/api/search`, `/api/dirs/create`, `/api/git/head` — all three are exactly the endpoints §1.6/R1 flagged as "plausible future consumers." Zero `/doc/*` references anywhere in the current integration tree — the doc pane doesn't exist on this side of the merge yet (it's wave-8 T5, unmerged). **Confirms new REST dependencies on 3 of the 6 §1.6 endpoints; confirms none yet on `/doc/*` in the merged tree** (see wave-8 pending-protection below).

3. **Orphaned-cluster literal grep (§1.6).** `grep -rn '/api/\(search\|upgrade\|git/head\|dirs/create\|path/validate\)\|reasoning-effort' cmd/serf-hub/frontend/src` → **not empty.** Decomposes into: real `fetch()` calls for `/api/search`, `/api/dirs/create`, `/api/git/head` (production dependencies — see reclassification table); and every `reasoning-effort` hit is the **AppWire method name** `thread/reasoning-effort/set`/`changed`, never the REST path (zero literal hits for the REST endpoint itself). Separately, zero hits anywhere for `/api/upgrade` or `/api/path/validate` as REST literals — both live only as AppWire `serf/upgrade` / `serf/path/validate` now. **The check no longer passes as originally written — 3 of 6 endpoints must leave the orphaned cluster; the other 3 are now confirmed (not just predicted) fully migrated to AppWire, which is stronger evidence than the original doc had.**

4. **`hubapi/client.go` route count (§2.1).** Exactly 13 `c.get`/`c.post` call sites, matching the 13 listed T-routes exactly (health, tree, sessions/{ref} bare, spawn-schema, spawn, models, send, tasks, interrupt, compact, clear, fork, model); confirmed `c.get`/`c.post` are the only two request-issuing helpers in the file (no stray `http.NewRequest`/`.Get`/`.Post`). **PASS, no drift.**

5. **`git grep htmx` (§4 acceptance gate).** Not empty — expected pre-deletion. Hits fall into three buckets: (a) the exact §1.1/§1.2/§1.3 DELETE-scoped files (`assets/*.js`, `style.css`, `templates/**`, `jstest/**`) — matches the plan exactly, nothing unexpected; (b) SDD/plan/spec prose under `docs/**`/`.superpowers/**` (out of M10 scope); (c) **5 files the current plan does not touch**, using "htmx" only as historical/explanatory prose or incidental test data, never as a dependency: `cmd/serf-hub/doc_serve.go:25`, `frontend/src/panes/session/composer/draft.ts:6`, `frontend/src/panes/settings/sections/theme.tsx:43`, `frontend/src/stores/prefs.ts:24,26` (comments contrasting new behavior with "the legacy's htmx world"), and `internal/hubcore/tree_test.go:93` (a test fixture string, `"htmx swap"`, coincidental). None is a functional dependency — the gate's *intent* ("nothing depends on htmx") is already satisfied once §1.1 executes. Recommend scrubbing the 4 comments in the same PR series so the gate is literally, not just functionally, empty — cosmetic, not a blocker. Also noted in passing: `web_test.go` carries htmx-named tests (`TestWeb_AppShellScopesHtmxHistoryToWorkspace`, `TestWeb_Assets_ServeHtmx`) exercising exactly the deleted behavior/asset — §1.5/Appendix A's reachability method explicitly excludes `_test.go`, so the kill list's function/file counts never included test-file surgery. That surgery is mandatory (the deletions won't compile/pass without it) but ordinary — not a new decision, just budget for it.

### Risk recap (the three named risks)

- **R1 (orphaned-REST cluster is a judgment call).** Substantially resolved by wave 6 — see reclassification below. The 3 endpoints that remain unconsumed are now **confirmed** migrated to live AppWire replacements in production code (not merely "planned to be"), stronger evidence than the original doc had.
- **R2 (`/doc/*` limbo, broken-asset trap).** MW-B (the `?format=raw` variant, commit `770800fe8`) has already landed in integration (`doc_serve.go:75`), ahead of where the wave-8 plan doc frames it (it still lists MW-B as an open go/no-go). But **the broken-asset trap itself is unresolved and unchanged**: `writeDocPage`/`writeDocMarkdownPage` (`doc_serve.go:237-270`, the default non-`raw` response) still hardcode `<link href="/assets/style.css">` and, for markdown, `<script src="/assets/marked.min.js">` — both §1.1 DELETE targets, byte-for-byte unchanged per the function's own doc comment. See Ambiguity below.
- **R3 (`assets/` partial keep).** Unchanged, confirmed no drift: `cmd/serf-hub/assets/` still holds exactly the 33 JS + `style.css` (DELETE) alongside the 4 icons + `manifest.webmanifest` (KEEP); `hubedge/auth_token.go:109-117`'s `isAuthExempt` still lists exactly the same 6 exact-match paths the kill list and the wave-8 plan both pin (`/auth`, `/api/health`, the 4 icon paths). No wave-6/8 change has touched this.

### Reclassification table

| Row (§ of origin) | Old class | New class | Evidence |
|---|---|---|---|
| `GET /api/search` (§1.6) | Orphaned REST | **PROTECTED (S)** | `frontend/src/shell/palette/search.ts:53` — real `fetch()`; wave-6 T3 palette search mode |
| `POST /api/dirs/create` (§1.6) | Orphaned REST | **PROTECTED (S)** | `frontend/src/panes/spawn/preflight.ts:41` — real `fetch()`; its own comment: "no appwire method exists for creation — verified" |
| `GET /api/git/head` (§1.6) | Orphaned REST | **PROTECTED (S)** | `frontend/src/panes/spawn/branch.ts:13` — real `fetch()`; wave-6 T2 branch-HEAD auto-resolution — exactly the scenario §4 R1 predicted |
| `GET /doc/file` (R2, §4) | Limbo (unconsumed, undeleted) | **PROTECTED-pending-W8** | Raw mode already shipped server-side (`doc_serve.go:75`, commit `770800fe8`); consumed by wave-8 T1's `protocol/docContent.ts` stub (`webui-w8-periphery`, unmerged) pending T5's fill; zero consumption in the current merged tree |
| `GET /doc/image` (R2, §4) | Limbo (unconsumed, undeleted) | **PROTECTED-pending-W8** | Pre-existing route (`output_images.go:202`, `web.go:188`); T1's `docContent.ts:31-33` `docImageURL()` already builds the real href; consumed once T5/T6 land the doc pane + `openBeside` body |

Consequence of the `/api/search` move: its "exclusive helpers" clause (§1.6 — "If `/api/search` goes, also delete `sortLiveForSearch`, `searchPastTitle`, `searchResult`/`searchResponse`") is now moot. The endpoint is not going, so those helpers are not deletion candidates either.

### Remaining genuinely-orphaned set (keep-by-default)

| Endpoint | Confirmed live replacement | REST literal anywhere in `frontend/src`? |
|---|---|---|
| `GET /api/upgrade` | AppWire `serf/upgrade` (`shell/palette/commands.ts:137`, palette `/upgrade` command) | none |
| `POST /api/sessions/{ref}/reasoning-effort` | AppWire `thread/reasoning-effort/set` (`stores/threads.ts:745`; `chrome/StatusRow.tsx`; palette `/reasoning-effort`) | none |
| `POST /api/path/validate` | AppWire `serf/path/validate` (`stores/extensions.ts:257`, `stores/launchConfig.ts:93`, spawn `preflight.ts`/`Spawn.tsx`, settings `dirListSetting.tsx`/`mcp.tsx`/`collectionFields.tsx`) | none |

Per the standing rule, all three are **KEPT, not deleted** — this needs no sign-off (sign-off was only ever for the delete path). TUI (`hubapi.Client`, check 4) still references none of the six original cluster members either, so nothing here depends on the TUI changing.

### Ambiguities

1. **Should the still-intact legacy HTML fallback in `doc_serve.go` be neutered before/alongside M10's asset deletion?** `writeDocPage`/`writeDocMarkdownPage` (the `/doc/file` response whenever `?format=raw` is absent) are untouched by MW-B and still emit `<link href="/assets/style.css">` (both) and `<script src="/assets/marked.min.js">` (markdown case) — both §1.1 DELETE targets. Once M10 lands, any hit to `/doc/file?session=&path=` without `?format=raw` (a stale link, a hand-typed URL, or a future caller that forgets the query param) will 200 with two dead asset references: the document content still renders (inline `<pre>` / binary notice / markdown source falls back to a `<pre>` when `window.marked` is undefined — see the markdown page's own inline script), just unstyled and, for markdown, unparsed — a degradation, not a hard break. It is not a page route (no bookmarkable or discoverable entry point; it requires a live session ref + a real path), and the original R2 write-up already anticipated this reshape happening "in wave 8" — but neither T5's scope nor the T8 close-sweep description in the wave-8 plan explicitly commits to closing this specific gap (both describe the *new* native pane's own behavior, not a check on the *legacy default mode's* continued reachability once the assets it references are gone).
   **Recommendation:** low severity, not an M10 blocker (no discoverable trigger; content still renders; degrades rather than 404s/500s). Add one explicit line item to wave-8's T8 close sweep (or a small MW-B follow-up): once the native doc pane is the only real caller, either (a) make `?format=raw` the only mode `doc_serve.go` serves and reject the legacy default outright, or (b) strip the two asset tags from `writeDocPage`/`writeDocMarkdownPage` so the default mode degrades with no dead links at all. Either is a handful of lines; the only bad option is leaving it undecided past M10.
