# M10 Deletion Report — legacy htmx web UI removal + SPA flag flip

**Branch:** `m10-deletion` (off integration `b51d99f0f`)
**Commit range:** `a7633486e..5ed0c4cbc` (7 commits)
**Status:** DONE_WITH_CONCERNS (construction complete, all gates green; 4 inventory
discrepancies + 1 controller judgment call recorded below — the merge gate should read them)

---

## 1. Gate results (full union, from worktree root unless noted)

| Gate | Result |
|---|---|
| `go build ./...` | 0 |
| `go test ./cmd/serf-hub/... ./hubapi/...` | 0 — 12 packages ok (incl. `hubapi` = the TUI 13-route contract) |
| `go test -tags serffuzz -run '^Fuzz' ./cmd/serf-hub/` | 0 (seed corpus) |
| `npx tsc --noEmit` (frontend) | 0 |
| `npx vitest run` (frontend, bare) | 0 — **217 files / 3192 tests passed** (see discrepancy #5) |
| `npm run lint` (biome) | 0 (611 files) |
| `npm run build` + `git restore dist/PLACEHOLDER` | 0, PLACEHOLDER restored |
| `make lint` (golangci, all modules) | 0 — PASS (7 modules) |
| `git grep -i htmx` outside `docs/` + `.superpowers/` + `*.md` | **empty** |
| `unused` linter (golangci) on hub package | 0 |

---

## 2. Commit series ↔ kill-list mapping

Each commit compiles and passes `go build ./...` + `go test ./cmd/serf-hub/...`.

| # | Commit | Kill-list rows executed |
|---|---|---|
| C1 | `a7633486e` strip dead /assets/ tags from legacy doc pages | **Controller Adjudication A**. RED-first `TestWriteDocPages_NoDeadAssetReferences`, then stripped the `/assets/style.css` link from `writeDocPage` + `writeDocMarkdownPage` and the `/assets/marked.min.js` script from `writeDocMarkdownPage`. Functions NOT deleted; non-raw mode NOT rejected (that reshape stays reserved for Jesse); `?format=raw` / `writeDocFileRaw` byte-untouched. |
| C2 | `169edf4f2` remove legacy SSR fragment + render layer | **§1.5** (fragment/render excision): web.go `handleInternalPartial`/`handleSessionPartial`/`handleWorkspaceEmpty`(+`launchpadRow`/`workspaceEmptyData`); web_workspace.go `renderWorkspacePartial`/`renderDetailsPanel`/`renderInputStrip` + dead details cluster (`detailsSections`/`detailsRow`/`detailsSection`/`renderDetailsRow`/`tokensAndCostRows`/`contextMeterHTML`) + `appwireUsageFromHub`; web_settings.go `renderSettingsPartial`/`renderProjectSettingsPartial` + settings-display cluster (`discoverPlugins*`/`discoverMCPs*`/`pluginDirsFromConfig`/`defaultPluginsRoot`/`countHooks`/`skillsFromPlugins`/`collectSkillsForPlugin`/`readSkillFrontmatter`/`mcpConfigPathForSettings`/`errString` + the `settingsLaunchModelList`/`settingsAbsPath` vars); web_spawn.go `handleWorkspaceSpawn`/`safeSpawnEnv`/`launchModelListErrorDiagnostic`; web_launchconfig.go `handleCredentialsPartial`; app_subagent_preview.go `handleSubagentPreview`/`writeJSON`. **§1.5 web_types.go**: the settings-display + spawn-view structs. Mux registrations `/_partials/`, `/_partials/credentials`, `/_api/subagent-preview` removed. Fragment template fields + parsing removed (appTmpl/threadTmpl kept for the still-legacy page handlers). |
| C3 | `660376f78` delete legacy assets | **§1.1**: `assets/*.js` (33) + `assets/style.css`. Kept the 4 icons + `manifest.webmanifest` (§2.4). |
| C4 | `762c988c4` delete legacy jstest suite | **§1.3**: `cmd/serf-hub/jstest/` (whole dir — see discrepancy #1). |
| C5 | `af21a8c20` flip page routes to the SPA | **§3 (the flip) + §1.7 (embed)**: deleted `newWebEnabled`; flipped `handleIndex`/`handleSettings`/`handleCredentials`/`handleThreadDocument` to unconditional `serveSPAIndex`, and `handleSession` to SPA + the kept `/s/{id}/images/{sha}` sub-route only. Deleted the §1.5 form-POST handlers (`handleSteer`/`handleQueue`/`handleDrainAsSteer`/`handlePromoteQueued`/`handleCancelQueued`/`handleFork`/`handleAside`) + `asideSession`, plus `renderThreadDocument`/`workspaceDataForRender`. Removed the app/thread template fields, `inputStripTemplateFuncs`, `formatWorkMillis`, the form-POST web_types structs, and the §1.7 dev-asset/template plumbing (`templatesFS`, `//go:embed templates`, `templatesRoot`, `devAssetsDir`, `noStore`, `assetVersion*`; `assetsRoot` reduced to the embedded path). **Adjudication D**: SERF_HUB_WEB now read nowhere. |
| C6 | `c660b7b58` delete legacy HTML templates | **§1.2**: `cmd/serf-hub/templates/` (all 25 `.html`). |
| C7 | `5ed0c4cbc` scrub residual htmx comment references | **Controller Adjudication B**: reworded the 4 cosmetic htmx comments (doc_serve.go, composer/draft.ts, settings/theme.tsx, stores/prefs.ts) + neutralized the coincidental `"htmx swap"` fixture prompt in hubcore/tree_test.go. **§4 acceptance gate**: `git grep htmx` empty outside docs/.superpowers. |

---

## 3. Inventory discrepancies (⚠ merge gate: read these)

1. **jstest file count: 204 tracked, not the inventory's 203.** The tree carried
   **202 `.js` + 1 `.sh` + 1 `.md` = 204** git-tracked files under `jstest/`, one `.js`
   more than §1.3's stated 201 `.js` / 203 total. The §1.3 directive is a
   *whole-directory* deletion and its safety property (no `.go`/Makefile build
   reference — see #2) holds, so the extra file was deleted with the rest. **Consequence:
   the headline whole-file count is 263, not 262** (34 assets + 25 templates + 204 jstest).
2. **§1.3's "no `.go` file references jstest" is inaccurate.** Two *comment* references
   exist: `cmd/serf-tui/question_overlay.go:132` and `cmd/serf-tui/question_overlay_test.go:104`
   (cross-references to `jstest/test-ask-compose.js`). They are comments — deletion did not
   break the build — but they now point at a deleted path. Left untouched (KEPT TUI files,
   outside the inventory); recommend scrubbing in a follow-up.
3. **`internal/httpsec/httpsec.go` CSP comment is now stale.** Lines 8/20 justify the
   script-src `'unsafe-inline'` exemption by naming `app.html` (a deleted template). The
   exemption itself is KEPT and still required (doc_serve.go's legacy pages emit inline
   `<script>`; the SPA shell inlines too), so no code change was made — the §7.4 CSP
   tightening is explicitly reserved. Only the comment is stale.
4. **`handleSessionAction`'s `"clear"` case is now unreachable.** After the flip, the only
   path that reached `handleSessionAction("clear")` was the deleted `/s/{id}/clear`
   form-POST; `/api/sessions/{ref}/clear` routes to the distinct, kept `handleAPIClear`
   instead. The `"clear"` switch case inside the (kept) `handleSessionAction` is therefore
   dead — a harmless unreachable branch, not flagged by `unused` because the function is
   live for interrupt/compact/shutdown. Left as-is (surgical excision inside the switch was
   not in the inventory).
5. **Frontend base test count is 217/3192, not the expected ~223.** The dispatch brief
   expected "223 files green at this base"; `b51d99f0f` actually has **217 files / 3192
   tests**. My frontend edits are comment-only (`git diff` shows 0 non-comment lines across
   draft.ts/theme.tsx/prefs.ts), so the suite is unchanged — this is a base-count expectation
   mismatch, not a regression. All 217 files pass.

No protected endpoint was touched. No inventory entry pointed at a moved/renamed file. No
new deletion was improvised beyond the sweeps the inventory (§1.5/§4) explicitly mandates.

---

## 4. Appendix C re-validation (run against the final tree)

1. **`newWebEnabled` sites** — none remain in code (function deleted C5). One *comment*
   reference in `webnext_test.go` (the new `TestSerfHubWebEnvIsDead` doc). `SERF_HUB_WEB`
   and `SERF_HUB_ASSETS_DIR` are read in **no** non-test `.go` file. **PASS.**
2. **`frontend/src` REST/`fetch` sweep** — unchanged by this branch (no frontend source
   route changes); the 3 endpoints Appendix D reclassified to PROTECTED (`/api/search`,
   `/api/dirs/create`, `/api/git/head`) plus `/doc/file`/`/doc/image` remain registered.
3. **Orphaned-cluster grep** — the three genuinely-orphaned endpoints (`/api/upgrade`,
   `/api/sessions/{ref}/reasoning-effort`, `/api/path/validate`) were KEPT per the
   standing orphaned-kept-by-default rule; their handlers (`handleAPIUpgrade`,
   `handleAPIReasoningEffort`, `handleAPIPathValidate`) survive. The `/api/search`
   exclusive-helpers clause is MOOT (search is PROTECTED): `sortLiveForSearch`,
   `searchPastTitle`, `searchResult`/`searchResponse` all KEPT.
4. **`hubapi/client.go` route count** — **13**, unchanged (file never touched; the TUI
   contract survives byte-identical). `go test ./hubapi/...` green.
5. **`git grep htmx`** — **empty** outside `docs/` + `.superpowers/` + `*.md`. **PASS.**

**Final mux route set** (web.go): `/ /new /s/ /thread/ /settings /settings/ /credentials`
(page routes → SPA), `/doc/file /doc/image`, `/rpc`, `/manifest.webmanifest`, `/assets/`,
`/webassets/`, `/auth`, and the `/api/*` family (`archive favorite project/delete tree
tree/project sessions/ spawn spawn-schema models dirs/create path/validate git/head search
health upgrade`). **Gone:** `/_partials/`, `/_partials/credentials`, `/_api/subagent-preview`.
**assets/** holds exactly `icon-192.png`, `icon-512.png`, `icon-maskable-512.png`, `icon.svg`,
`manifest.webmanifest` (§ R3; `hubedge/auth_token.go` isAuthExempt unchanged, still matches).

---

## 5. Deleted tests (Adjudication C) — 111 functions, each exercises deleted behavior

Grouped by the deleted behavior they exercised. (`_test.go` files were never in the
inventory's file counts; this surgery is in-budget per Appendix D.)

**A. Legacy `/_partials/*` fragment + SSR-render tests** — hit deleted fragment routes /
render helpers / SSR templates, now 404 or gone:
`TestWeb_Workspace*` (ForkOriginalBanner, SubagentParentBreadcrumb, AwaitingRestDisablesStopAndSteer,
IconButtonsHaveAriaLabels, InitialMetaDoesNotDuplicateTitleOOB, RendersBottomStopForActiveSession,
RendersDisabledSteerControl...), all `TestWeb_WorkspacePartial_*`, all `TestWeb_State_*`,
all `TestDetailsPanel_*`, `TestWeb_WorkspaceTemplate_RendersDataObservers`,
`TestWeb_ProjectSettingsListEscapesWorkingDir`, all `TestWeb_SessionTasks_*` (hit
`/_partials/s/.../tasks`; the kept `renderSessionTasks` is exercised via `/api/.../tasks`
by cov-fuzz), all `TestWeb_Settings_*` + `TestWeb_SettingsProviders*` +
`TestWeb_SettingsErrorPathsAvoidHTMLInterpolation` + `TestWeb_SettingsLaunchListPanes*` +
`TestWeb_SettingsGeneralBearerTokenCopyIsAccurate` + `TestSettings_DisplaySectionRoutes` +
`TestSettings_LaunchSerfRendersDiagnostics` + `TestSettingsMCPStatus_PopulatedAndEmpty` +
`TestDiscoverMCPsForSettings_StdioAvailableHTTPUnreachable` +
`TestDiscoverPluginsForSettings_BrokenDirDoesNotBrickPane` + `TestLaunchSerfSettings_UsesSchemaRoot`,
all `TestWeb_WorkspaceSpawn_*` + `TestSpawnTemplate_HasSchemaAdvancedRoot`,
`TestInputStatus_NoDuplicateRunningIndicator` + `TestInputStatusGaugeAmberThreshold`,
`TestWeb_CredentialsPartial`, `TestSubagentPreviewEndpointReadsRefWithoutChangingJobSemantics`,
`TestWeb_CodexSessionRouteReadsConfiguredSource`, `TestAppwireUsageFromHub`,
`FuzzWebSettingsPass5` (whole serffuzz coverage-seed for the deleted settings-display cluster),
the 4 `TestWorkspaceEmpty*` launchpad tests.

**B. Legacy asset-serving tests** — served/read deleted assets:
`TestWeb_Assets_ServeHtmx`, `TestWeb_Assets_ServeRenderer`, `TestWebWorkspaceContentColumnCSSContract`.

**C. Legacy app-shell / thread-document page tests** — asserted legacy HTML now replaced by
the SPA shell: `TestWeb_AppShell_RendersSidebarAndWorkspaceMounts`, `TestWeb_AppShellHasSidePaneRegion`,
`TestWeb_AppShellScopesHtmxHistoryToWorkspace`, `TestWeb_Landing_Renders`,
`TestWeb_Index_NewRouteForwardsPromptToWorkspace`, `TestWeb_SessionRoute_FullPage_ServesAppShell`,
`TestWeb_SessionRoute_LocalRefCanonicalizesWorkspaceURL`, `TestWeb_SettingsFullPageLoadsInternalPartial`,
`TestWeb_CredentialsRoute`, `TestWeb_ThreadDocument_CompactsSubagentChromeAndFooter`,
`TestWeb_ThreadDocument_ComposerControlsLiveInsideInputCard`,
`TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument`,
`TestWeb_ThreadDocument_RouteEncoding`, `TestWeb_ThreadDocument_StateRefreshPreservesCompactLocationMode`,
`TestWeb_InternalPartialsRequireHXRequest`, `TestWeb_LegacyPartialRoutesDoNotServeFragments`.

**D. Legacy `/s/{id}/{action}` form-POST tests** — exercised the deleted form-POST handlers:
`TestWeb_Steer_ForwardsBodyToDaemon`, `TestWeb_Steer_RejectsEmptyText`,
`TestWeb_Queue_ItemsShapeForwardedToDaemonQueueTurn`, `TestWeb_DrainAsSteer_ItemsShapeSendsAtomicDrain`,
`TestWeb_Fork_CallsForkSession`, `TestWeb_Fork_DeferInput`, `TestWeb_Aside_CallsAsideSession`,
the 5 `TestWebPromoteQueued*`, the 6 `TestWebCancelQueued*`, `TestWeb_SessionAction_ClearForwards`
(the `/s/clear` path; `handleAPIClear` stays tested separately). Kept the RPC counterparts
(`TestHubRPCPromoteQueuedAsSteerRelays`, `TestHubRPCCancelQueuedRelays`, `TestHubRPCThreadForkAside*`).

**E. Dead SERF_HUB_WEB flag-parity tests** — gated on the deleted flag:
`TestWebNextLegacyDefaultUnchanged` (no legacy default exists now),
`TestWebNextSessionActionsKeepLegacyBehavior` (replaced by `TestSerfHubWebEnvIsDead`).
`TestWebNextServesSPAWhenEnabled` and `TestWebNextSessionImagesKeepLegacyBehavior` were
**renamed-not-removed** → `TestWebServesSPAForPageRoutes` / `TestSessionImageRouteNotSPAShell`
(flag setenv dropped, invariant retained).

**Tests added:** `TestWriteDocPages_NoDeadAssetReferences` (C1, RED-first for Adjudication A);
`TestSerfHubWebEnvIsDead` (C5, Adjudication D — unset/`new`/`garbage` all serve the SPA
identically); plus the two renamed webnext tests above.

---

## 6. Controller judgment call (offline authority #3 — Jesse vetoes on return)

**Send/SessionAction tests repointed to `/api`, not deleted, to preserve coverage of the
KEPT handlers.** `handleSend` and `handleSessionAction` survive (the TUI reaches them via
`/api/sessions/{ref}/send|interrupt|compact|shutdown`), but their deep logic tests were
written against the now-deleted `/s/{id}/{action}` route. Rather than lose that coverage
(Jesse's "reducing coverage is worse than failing tests"), the ~15 `TestWeb_Send_*` and
`TestWeb_SessionAction_*(interrupt/compact/shutdown)` tests were repointed to
`/api/sessions/local:{id}/...` — the same kept handlers — and now pass there. Only
`TestWeb_SessionAction_ClearForwards` was deleted (its `/s/clear` path routed to
`handleSessionAction("clear")`; `/api/clear` is the distinct `handleAPIClear`, already
tested at web_test.go). Coverage-seed cov-fuzz files were trimmed to drop pokes of deleted
symbols while keeping every kept-function poke; the settings-display seed
(`cov_web_settings_pass5_fuzz_test.go`, serffuzz) was ~90% deleted surface and its residual
kept-function pokes are redundantly covered by `cov_web_views_spawn`/`cov_provider_render`,
so it was deleted wholesale.

---

## 7. Final counts vs the inventory

| Category | Inventory | Actual | Δ |
|---|---|---|---|
| assets `*.js` + `style.css` | 33 + 1 = 34 | 34 | — |
| templates `**` | 25 | 25 | — |
| jstest `**` | 203 | **204** | **+1** (discrepancy #1) |
| **Whole non-test files** | **262** | **263** | **+1** |
| Whole Go (non-test) files | 0 | 0 | — |
| Go surgical sites (§1.5/§3/§1.7) | ~31 | all named sites executed + the §4-mandated dead-leaf sweep (details/settings clusters, form-POST structs, embed plumbing); `unused` linter clean | consistent |
| Protected shared endpoints | 24 (incl. 13-route TUI contract) | all present | — |
| Whole test files deleted (not in inventory counts) | n/a | 3 (`web_launchpad_test.go`, `web_launchconfig_test.go`, `cov_web_settings_pass5_fuzz_test.go`) | — |

Branch diffstat: **312 files changed, 164 insertions(+), 65,828 deletions(-)**.

---

## 8. Fix round — review Minors (commit after review `120a57f09`)

Review verdict: **APPROVED**, repoint **ENDORSED**, three Minor findings closed here.

- **Finding 1 (not-live stragglers, the real one).** `TestWeb_SessionAction_NotLive_404` and
  `TestWeb_Steer_NotLive_404` were left byte-identical, still POSTing to the deleted `/s/`
  routes — they passed only via the mux-default 404, not the handlers' not-live logic (vacuous).
  - `TestWeb_SessionAction_NotLive_404` → **renamed** `TestWeb_APISessionAction_NotLive_404` and
    **repointed** to `POST /api/sessions/local:{id}/{interrupt,compact,shutdown,clear}` — the kept
    handlers (`handleSessionAction` for the first three, `handleAPIClear` for clear). Still 404,
    but now from the real `!isLive` / not-known check. **RED-first proof:** short-circuiting
    `isLive()` to `return true` makes the test fail 4/4 with **503** (interrupt/shutdown/clear →
    `actionUnavailable`; compact → `sessionUnavailable`) instead of 404 — the 404 provably comes
    from the not-live check, not a route default. Mutation reverted (`web_api_tree.go` byte-restored).
  - `TestWeb_Steer_NotLive_404` → **deleted, not repointed.** Steer has **no** REST successor
    (the `/api/sessions/{ref}/*` sub-dispatch is send/tasks/interrupt/compact/shutdown/clear/fork/
    model/rename/reasoning-effort — no steer/queue/drain/promote/cancel/aside; the SPA steers over
    AppWire `turn/steer`). With no kept handler to exercise, the only correct resolution of a
    vacuous not-live test is removal.
- **Finding 2 (vestigial env).** Removed the no-op `t.Setenv("SERF_HUB_ASSETS_DIR", root)` in
  `cov_small_tails_pass6_fuzz_test.go` (dead since `devAssetsDir()`'s deletion). No comment to
  keep; the following `_ = web.Handler()` stays (still exercises Handler construction).
- **Finding 3 (punch-list).** `internal/editorurl` is **unimported-but-kept** at HEAD:
  kill-list §2.2 predicted this ("Legacy-only internal package candidate"); the zero-whole-Go-file-
  deletions boundary keeps it; `unused` is clean with it present. **Follow-up package removal is
  Jesse's call** (out of this deletion's scope).

Fix-commit gates (AND-chained, worktree root): `go build ./...` **0** · `go test ./cmd/serf-hub/...`
**0** · `make lint` **0 (7 modules)**. Frontend untouched (no frontend files in scope).

## 9. M10 completion commits — raw-only /doc/file + delete internal/editorurl

Two Jesse rulings executed on this branch (progress.md:491, items 3 and 5). Deletion discipline held:
every deleted symbol was verified caller-free first; two consequential edits and one out-of-ruling
discovery are called out explicitly.

### Item 1 — raw-only /doc/file (`5d04baf7e` "webui m10: raw-only /doc/file, delete legacy HTML writers")

`/doc/file` now serves one mode: the file's raw bytes (`?format=raw`). A request that omits format, or
sends any other value, is `400` with the plain-text hint `format=raw required`. The format gate sits
**after** the containment guard chain, so a raw and a non-raw request still reject the same
out-of-cwd / unknown-session input identically — required to keep the existing raw parity tests
(`TestDocFile_Raw_RejectsTraversalDotDot`, `TestDocFile_Raw_RejectsAbsolutePathEscape`, which assert
`rawRec.Code == htmlRec.Code` where `htmlRec` is a non-raw request) passing. Placing the check before
the guards (early 400) would have broken those two raw tests, so the ordering is load-bearing. The raw
path (`writeDocFileRaw`, `docRawTotalSize`, `X-Doc-Truncated`/`X-Doc-Total-Size`) and `/doc/image` are
byte-untouched. CSP/httpsec untouched — its `unsafe-inline` rationale names app.html/settings/
credentials, not the doc pages, so no httpsec comment went stale.

**RED-first evidence** — the 3 new tests, run against pre-deletion code, all failed serving the legacy
HTML page:
```
--- FAIL: TestDocFile_MissingFormat_400  status=200 body="<!DOCTYPE html>...<pre class=\"doc-pre\">hello</pre>..."
--- FAIL: TestDocFile_NonRawFormat_400    status=200 (format=html served the HTML page)
--- FAIL: TestDocFile_EmptyFormat_400     status=200 (format= served the HTML page)
```
Post-implementation all three PASS, alongside the untouched raw + guard-rejection tests.

**Deleted-symbol inventory** (each verified zero-caller before removal):

| symbol | kind | callers removed |
|---|---|---|
| `writeDocPage` | func, doc_serve.go | 2 non-raw branches in `handleDocFile` + 2 fuzz call sites |
| `writeDocMarkdownPage` | func, doc_serve.go | markdown branch in `handleDocFile` + 2 fuzz call sites |
| `formatDocBytes` | func, doc_serve.go (Go) | binary-notice branch + 2 fuzz `for` loops |
| `TestWriteDocPages_NoDeadAssetReferences` | test | task-directed; called the deleted writers directly |

Also deleted 4 now-subjectless legacy HTML-serve tests (they asserted `200` + server-rendered HTML,
the exact behavior removed): `TestDocFile_ServesTextFileEscaped`, `TestDocFile_RendersMarkdown`,
`TestDocFile_BinaryNotice`, `TestDocFile_ServesWorktreeRelativePathForPaneNavigation`. Equivalent
text/markdown/binary serving is fully covered by the kept `TestDocFile_Raw_Serves*` trio; guard
rejection by the kept `TestDocFile_Rejects*` / `_UnknownSession404` / `_NonLocalSession404` (which pass
unchanged — guards run before the format gate). No net loss of coverage of surviving behavior.

Grep receipt (post): `git grep -E '\b(writeDocPage|writeDocMarkdownPage|formatDocBytes)\b' -- '*.go'`
→ zero Go hits. (The surviving `formatDocBytes` is the separate TypeScript mirror in
`frontend/src/panes/doc/docFile.ts`, untouched.)

### Item 2 — delete internal/editorurl (this final commit "webui m10: delete internal/editorurl")

Grep receipt (pre): `git grep 'serf/cmd/serf-hub/internal/editorurl' -- '*.go'` → no import-path
reference anywhere; `git grep '\.EditorURL('` / `EditorURL(` → only inside the package. The package was
genuinely unimported. Deleted the directory: `editorurl.go`, `editorurl_test.go`,
`editorurl_fuzz_test.go` (symbol `EditorURL`, fuzz target `FuzzEditorURL`).

**Consequential edit:** removed the now-dangling `FuzzEditorURL` registration at
`scripts/run-fuzz.sh:541` — leaving it would aim the fuzz harness at a deleted package. `bash -n
scripts/run-fuzz.sh` clean after.

**Discovery OUTSIDE the ruling (reported, not acted on):** env var `SERF_HUB_EDITOR_URL_TEMPLATE`
(`envvars.SERFHubEditorURLTemplate`) loses its ONLY consumer with editorurl gone, yet stays registered
in the catalog — `envvars/envvars.go:66,171`, `cmd/serf-hub/main.go:480`, and asserted in
`main_test.go` / `testmain_test.go`. It remains referenced (a slice member), so `unused` stays green and
the build is unaffected; semantically it is now a dead knob. Removing it is a separate change to the
public env-var catalog + its tests — outside the "delete the package" ruling — so it is left in place
for Jesse's call.

### Pre-existing observation on the `git grep htmx` gate

`git grep htmx` outside `docs/` / `.superpowers/` is **not** empty at this branch's baseline: one prose
hit, `test/scenarios/ask-cross-session-notify.md:107` ("...not an htmx partial, so no `HX-Request`
header is needed"). It predates this work, is unchanged by it, and is outside both items' scope —
flagged, not touched.

### Gates

| gate | Item 1 (`5d04baf7e`) | Item 2 (final) |
|---|---|---|
| `go build ./...` | 0 | 0 |
| `go test ./cmd/serf-hub/...` | 0 | 0 |
| `make lint` (7 modules, incl. `unused`) | 0 | 0 |
| frontend `tsc --noEmit` | — | 0 |
| frontend `vitest run` (bare, logged) | — | 243 files / 3490 tests pass (baseline unchanged) |
| frontend `npm run lint` (biome, 668 files) | — | 0 |
| frontend `npm run build` + `git restore dist/PLACEHOLDER` | — | 0, dist tree clean |
| `git grep htmx` outside docs/.superpowers | — | unchanged (1 pre-existing prose hit, above) |

### §9 follow-up — REMOVE the orphaned SERF_HUB_EDITOR_URL_TEMPLATE (Jesse ruling)

Jesse ruled on the orphan surfaced above: **remove it.** Done in a third commit on this branch. The env
var `SERF_HUB_EDITOR_URL_TEMPLATE` / `envvars.SERFHubEditorURLTemplate` is deleted from the public
catalog and all its Go wiring.

Grep receipt — **before**, 5 Go sites (`git grep -E 'SERF_HUB_EDITOR_URL_TEMPLATE|SERFHubEditorURLTemplate' -- '*.go'`):
`envvars/envvars.go:66` (catalog `Var` entry) · `envvars/envvars.go:171` (`allVars` registration) ·
`cmd/serf-hub/main.go:480` (`printHubEnvVars` list) · `cmd/serf-hub/main_test.go:23` (`TestPrintHubEnvVars`
assertion) · `cmd/serf-hub/testmain_test.go:37` (TestMain unset list). **After: zero `.go` references.**

All 5 sites removed; nothing else. `gofmt` re-aligned the `TestPrintHubEnvVars` map — the removed key was
the longest, so the alignment column shrank (formatting-tool churn, not hand-edited whitespace). Catalog
audit tests pass unchanged: `TestSupportedEnvVarsAreDocumented` (catalog ⊆ doc; the removed var is simply
no longer required) and `TestSupportedEnvVarsUseRegistryRows`.

**Further orphans (reported, NOT chased — per the ruling's "nothing else"):** stale user-facing docs still
name the removed knob — `README.md:393` (open-in-editor bullet) and `docs/environment.md:14` (env-var
table row). Neither is gate-blocking: the audit test only checks catalog ⊆ doc, so a leftover doc row is
harmless (there is no doc ⊆ catalog check). The internal reports `m10-final-revalidation.md:58,106` and
the §9 discovery note above also reference it as prior state. These want a deliberate docs pass; left for
Jesse.

**Pre-existing unrelated failure (discovery, not mine):** `go test .` (root package) fails
`TestMakeRuntimeAliasesBuildThePair/build-hub` — its fixture runs `make build-hub` from a temp dir, and
`build-hub: build-web` (Makefile:35) then does `cd cmd/serf-hub/frontend && npm ...`, which the temp dir
lacks (`No such file or directory`). **Proven identical on the clean base `28454f115`** (throwaway
detached worktree, change-free tree), so it predates and is independent of this removal (which touches
only the 4 Go files above). Outside the coordinator's gate scope (`./cmd/serf-hub/... ./envvars/...`) and
outside this ruling; flagged, not touched.

Gates (AND-chained, worktree root): `go build ./...` **0** · `go test ./cmd/serf-hub/... ./envvars/...`
**0** · catalog audit `go test . -run TestSupportedEnvVars` **0 (2/2)** · `make lint` **0 (7 modules)**.
