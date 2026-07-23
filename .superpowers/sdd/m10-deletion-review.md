# M10 Deletion Review — legacy htmx web UI removal + SPA flag flip

**Reviewer role:** adversarial merge-gate reviewer (highest-stakes review of the project).
**Branch:** `m10-deletion` — HEAD `24edd07399ca3d3bd14afceff0a0acd7fabec07d`
**Base (integration tip, pre-wave-8):** `b51d99f0f`
**Range reviewed:** `b51d99f0f..24edd0739` (7 deletion commits + 1 report commit)
**Method:** inventory-comparison for deletions (name-status vs kill-list, not line-by-line body
reads); full reads of every non-deletion hunk (Go surgical edits, flip, doc_serve strip, test
repoints, comment scrubs); all gates re-run from the worktree root.

---

## VERDICT: **APPROVED**

No Critical or Important findings. Three Minor findings (all cosmetic / disclosure-gap class,
none altering the deletion set or breaking a protected surface). The deletion is bounded exactly
by the kill-list as amended by Appendix D + the recorded adjudications; the flip is complete and
correct; every protected endpoint survives, routed **and** handled; `hubapi/` is byte-identical to
base; the doc_serve strip is exact and the CSP `unsafe-inline` exemption is correctly retained.
Every gate is green. The `TestWeb_Send_*` / `TestWeb_SessionAction_*` repoint is **ENDORSED**.

---

## Gate summary (all re-run by me, AND-chained from worktree root)

`go build ./...` **0** · `go test ./cmd/serf-hub/... ./hubapi/...` **0 (12 pkg ok, 0 fail,
hubapi TUI-contract green)** · `tsc --noEmit` **0** · `vitest run` (bare) **217 files / 3192 tests
passed** (matches expected base count) · biome lint **611 files, 0** · `npm run build` **OK**
(dist/PLACEHOLDER restored, tree clean) · `make lint` **PASS (7 modules, `unused` in standard
preset)** · `git grep -i htmx` **empty in all code** (only docs/superpowers prose + one `*.md`
scenario file remain). Working tree clean after all gates + bisect.

---

## Probe outcomes

- **P0 — inventory boundary (THE probe): PASS.** 266 files deleted; **0** outside the four
  sanctioned categories (assets 34, templates 25, jstest 204, `_test.go` 3). Every deleted path
  maps to a kill-list §1 row (as amended by Appendix D) or a recorded adjudication. All 4 recorded
  discrepancies verified exactly: (#1) jstest 204→headline 263 not 262 [34+25+204]; (#2) the two
  serf-tui `jstest/test-ask-compose.js` refs are comments, KEPT; (#3) httpsec CSP comment names
  deleted `app.html` but `unsafe-inline` correctly retained + httpsec.go byte-untouched; (#4)
  unreachable `case "clear"` in the kept `handleSessionAction`. None altered the deletion set.
- **P1 — protected surface: PASS.** All §2 endpoints routed + handled (mux at web.go 104–154:
  `/rpc /api/{health,tree,tree/project,spawn,spawn-schema,models,archive,favorite,project/delete,
  search,upgrade,dirs/create,path/validate,git/head} /api/sessions/ /doc/{file,image}
  /manifest.webmanifest /webassets/ /assets/ /auth` + page routes). `git diff b51d99f0f..HEAD --
  hubapi/` **empty (byte-identical)**; web_api.go + web_api_tree.go byte-untouched (the
  `/api/sessions/{ref}/*` sub-dispatch — send/tasks/interrupt/compact/clear/fork/model/rename/
  reasoning-effort — intact). 3 orphaned-KEPT handlers alive (handleAPIUpgrade,
  handleAPIReasoningEffort, handleAPIPathValidate). `assets/` = exactly the 4 icons +
  manifest.webmanifest. `isAuthExempt` (hubedge/auth_token.go) byte-identical to base.
  Gone as intended: `/_partials/`, `/_partials/credentials`, `/_api/subagent-preview`.
- **P2 — the flip: PASS.** `newWebEnabled` deleted (survives only as a comment in
  webnext_test.go); all 5 former call sites now serve the SPA unconditionally (handleIndex,
  handleSettings, handleCredentials, handleThreadDocument = bare `serveSPAIndex`; handleSession =
  SPA + the kept `/s/{id}/images/{sha}` sub-route only). `grep -rn SERF_HUB_WEB` → only
  webnext_test.go. `TestSerfHubWebEnvIsDead` genuinely asserts `""`/`"new"`/`"garbage"` all →
  200 + SPA shell (`id="root"`) **and** all three bodies byte-identical.
- **P3 — adjudication A (doc_serve strip): PASS.** Removed exactly the two dead asset references
  (`/assets/style.css` from writeDocPage + writeDocMarkdownPage; `/assets/marked.min.js` from
  writeDocMarkdownPage); the inline theme `<script>` and everything else are byte-untouched;
  the `?format=raw` / `writeDocFileRaw` contract untouched (its tests unchanged). RED-first
  `TestWriteDocPages_NoDeadAssetReferences` locks both functions to emit no `/assets/` ref;
  non-raw mode still writes functional (unstyled) content.
- **P4 — test surgery: PASS + repoint ENDORSED.** 111 deleted test/fuzz funcs; spot-checked
  >20 by name against the report groups A–E — every one exercises deleted behavior (WorkspacePartial,
  DetailsPanel, ThreadDocument, Steer/Queue/DrainAsSteer/Fork/Aside/Promote/Cancel, InternalPartials,
  AppShell, WorkspaceSpawn, CredentialsPartial, SubagentPreview, Assets_ServeHtmx/Renderer,
  Settings_*_Renders, appwireUsageFromHub). The ~15 repointed `TestWeb_Send_*` /
  `TestWeb_SessionAction_*` tests read at HEAD are **meaningful, not vacuous** (real appwire daemon
  stubs capturing TurnStartParams/params, image forwarding asserted by media-type/name/data-len,
  resume-past-thread with ref + resumeCalls assertions, 204/404 codes). See repoint endorsement +
  Finding 1 below.
- **P5 — gates: PASS.** See gate summary. Vitest exactly 217/3192 (base predates wave 8).
- **P6 — bisectability: PASS.** Checked out C2 (`169edf4f2`, the riskiest — render-layer removal),
  C3 (`660376f78`), C5 (`af21a8c20`); `go build ./...` exit 0 at each; returned cleanly to
  tip `24edd0739`, tree clean. Always-compiling claim holds for the sampled points.
- **P7 — punch-list accuracy: PASS with Finding 2.** `internal/editorurl` is genuinely
  unimported at HEAD yet kept (verified: 0 importers; package + its own tests remain). golangci
  `unused` is clean (no dead unexported symbols anywhere in the 7 modules). No *other* now-dead Go
  surfaced: the helpers the surgery could have orphaned (defaultMCPConfigPath, tildeHome,
  fileSizeHuman, serfLaunchModelList, subagentPreview{FromThread,Item}) are all still consumed by
  the AppWire settings-overview / models / rpc paths.

---

## Findings

### Minor

**1. Two `_NotLive_404` tests left pointing at deleted `/s/` routes (undisclosed test drift).**
`TestWeb_SessionAction_NotLive_404` and `TestWeb_Steer_NotLive_404` (web_test.go) are byte-identical
to base — they still POST to the now-deleted `/s/<id>/{interrupt,compact,shutdown,clear}` and
`/s/<id>/steer` routes and assert 404. Post-flip that 404 comes from `handleSession`'s mux-default
case, **not** the handlers' `isLive` logic — and `handleSteer` no longer exists at all. They pass,
but no longer exercise their named behavior; `TestWeb_Steer_NotLive_404`'s comment ("no auto-resume
— steering an ended model isn't meaningful") is now false (there is no steer route). This class of
drift is exactly what the report disclosed for discrepancies #2/#3/#4, but these two were not
called out (the §6 repoint note covers only the repointed tests). No hard coverage loss — the kept
`handleSessionAction` not-live path is still fuzz-covered via `/api/sessions/{id}/{action}` seeds
(cov_core_api_pass4, cov_session_residue_pass5, web_mutating_fuzz). *Recommend:* delete or
rename+repoint these two (they would be a fine explicit "legacy `/s/` action routes are gone → 404"
deletion guard, but should say that rather than claim not-live handler semantics).

**2. `internal/editorurl` is now dead (unimported) but kept, and the report's punch-list omits it.**
Kill-list §2.2 explicitly predicted editorurl becomes dead after the settings surgery ("verify no
other importer … can be removed too"). At HEAD it has zero importers. Keeping it is harmless — the
package carries its own passing tests, so golangci `unused` will never flag it — but the deletion
report never carried the §2.2 follow-up forward (0 mentions). *Recommend:* a follow-up to delete
`cmd/serf-hub/internal/editorurl`, or at minimum a punch-list line so it isn't silently orphaned.

**3. Vestigial `t.Setenv("SERF_HUB_ASSETS_DIR", root)` in cov_small_tails_pass6_fuzz_test.go:156.**
The env var's only reader (`devAssetsDir()`) was deleted in C5, so this setenv is now a no-op —
`web.Handler()` no longer branches on it. The builder trimmed this seed's deleted-handler calls
(`handleSubagentPreview`, `handleInternalPartial`) but left the now-dead env set. Same stale-reference
class as discrepancies #2/#3. Cosmetic; *recommend* scrub in the same follow-up.

### Observation (not a finding)

`test/scenarios/ask-cross-session-notify.md:107` still contains an "htmx" prose reference outside
the 4 scrubbed comments. It is a `*.md` file (within the gate's stated `docs/ + .superpowers/ +
*.md` exclusion) and is accurate prose ("a plain JSON API route, not an htmx partial"). The gate
passes as defined; noted only for completeness.

---

## Repoint endorsement (P4 — flagged judgment call for Jesse)

**ENDORSE.** Repointing the deep-logic `TestWeb_Send_*` and
`TestWeb_SessionAction_*(interrupt/compact/shutdown)` tests from the deleted `/s/<id>/{action}`
routes to `/api/sessions/{ref}/{action}` is the correct call:

- `handleSend` and `handleSessionAction` are the **same kept handlers** the deleted `/s/` routes
  used to reach; `/api/sessions/{ref}/…` is the surviving, TUI-contracted entry point (pinned by
  hubapi/client_test.go). The repoint tests the identical handler through its real surviving route.
- The repointed tests **retain their full assertions** (verified by reading them at HEAD): appwire
  daemon stubs capture the forwarded params; text+image payloads are asserted by media-type / name /
  data-length; the compact-resume test asserts `resumeCalls==1` + ref + `compactCalled`; codes are
  204/404. These are not vacuous 200-checks.
- The alternative of *deleting* them would drop real coverage of the kept handlers (violating
  "reducing coverage is worse than failing tests"); the alternative of *leaving them at `/s/`* would
  make them vacuous — which is precisely what happened to the two `_NotLive_404` tests (Finding 1).

The endorsement carries the recommendation in Finding 1: finish the job by repointing/renaming (or
deleting) the two `_NotLive_404` stragglers so the whole action-test set is consistent.
