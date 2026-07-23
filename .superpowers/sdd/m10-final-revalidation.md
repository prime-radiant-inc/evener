# M10 Final Re-validation — independent merge gate

**Role:** independent final gate (built none of this). Last check before the legacy-UI deletion
merges and the React SPA becomes the only web UI.
**Worktree / branch:** `webui-m10-deletion` / `m10-deletion`
**HEAD:** `9826fc3a3` (absorb `cf5e42d6b` + raw-only `5d04baf7e` + editorurl `9826fc3a3`, atop the
reviewed+minors-closed deletion `6ecc2dafd`)
**Base (pre-deletion integration tip):** `b51d99f0f`
**Reviewed tip (m10-deletion-review.md):** `24edd0739` — confirmed an ancestor of HEAD.
**Method:** every check re-run against this exact tree; no report claim trusted where verifiable.

---

## VERDICT: **GATE-CLEAN**

No Critical, Important, or Minor findings. The deletion is bounded exactly by the sanctioned
inventory (270 whole-file deletions, **zero** outside the four M10 categories + editorurl + wave-8's
own frontend file); the flag flip is complete; every SPA-consumed and TUI-contract endpoint survives
routed **and** handled; the `/doc/file` raw-only reshape preserves 403/404 containment parity; the
three unreviewed commits resolve and delete exactly what they claim. Every gate is green, vitest at
the expected 243/3490. The merge gate's last condition is satisfied.

---

## GATE LINE (all re-run, AND-chained, this tree)

`go build ./...` **0** · `go test ./cmd/serf-hub/... ./server/... ./internal/... ./agent/... ./appwire/...` **0 (all pkgs ok)** · `make lint` **PASS (7 modules)** · `tsc --noEmit` **0** · `vitest run` (bare) **243 files / 3490 tests passed** (== expected) · `npm run lint` (biome) **668 files, 0** · `npm run build` **OK, dist/PLACEHOLDER restored** · (bonus) inventory-boundary sweep **270 deletions, 0 outside sanctioned**

---

## DUTY 1 — Appendix-C re-validation (per-check)

1. **Flag flip / dead env — PASS.** `newWebEnabled` appears only as a comment in `webnext_test.go`
   (function gone). `grep SERF_HUB_WEB` (Go) hits only `webnext_test.go` (the dead-env test).
   `TestSerfHubWebEnvIsDead` genuinely loops `""`/`"new"`/`"garbage"`, asserts each → 200 + `id="root"`,
   **and** asserts all three bodies byte-identical (`bodies[i] != bodies[0]` → fatal).
2. **Frontend REST sweep — PASS.** Every non-test `fetch(`/`new WebSocket(` call site enumerated; each
   consumed endpoint is routed (web.go mux) **and** handled: `/rpc`→ServeWebSocket, `/api/{git/head,
   dirs/create,search,favorite,archive,project/delete,tree,tree/project,models,health}`, `/api/sessions/{ref}/rename`→handleAPIRename (sub-dispatch), `/s/{ref}/images/{sha}`→handleSessionImage,
   `/manifest.webmanifest`, `/popout.html`, `/webassets/*`, `/assets/` icons. `/doc/file?format=raw`→
   handleDocFile and `/doc/image`→handleDocImage both present; **guard order verified**: session lookup
   → `ResolveInRoot` (403 escape / 404 miss) → `readDocFile` (404) all run BEFORE the `format!="raw"`→400
   gate, so a non-raw request rejects out-of-cwd/unknown-session input identically to raw.
3. **Orphaned-KEPT trio — PASS.** `/api/upgrade`→handleAPIUpgrade (web.go:150), `/api/path/validate`→
   handleAPIPathValidate (web.go:146), `reasoning-effort`→handleAPIReasoningEffort (sub-dispatch,
   web_api_tree.go) all still routed and handled.
4. **hubapi TUI contract — PASS.** `git diff b51d99f0f..HEAD -- hubapi/` is **exactly** the one reviewed
   comment-only delta: `SessionDetail.ActiveTurnStartedAt` doc "unix-seconds"→"Unix epoch-milliseconds"
   (the timestamps-migration doc fix). The struct field and all 13 route shapes are byte-unchanged.
5. **htmx gate — PASS.** `git grep -i htmx` outside `docs/` + `.superpowers/` = **exactly one** hit,
   `test/scenarios/ask-cross-session-notify.md:107` (scenario-doc prose, docs-class). No functional dep.
6. **assets/ + auth — PASS.** `cmd/serf-hub/assets/` = exactly the 4 icons + `manifest.webmanifest`
   (filesystem == git-tracked). `isAuthExempt` byte-identical to base (empty diff) — lists only `/auth`,
   `/api/health`, and the 4 icon paths. `/popout.html` is registered on the normal mux (`servePopoutShell`),
   **not** in the exempt set → correctly auth-guarded.
7. **editorurl — PASS.** `cmd/serf-hub/internal/editorurl` gone; zero importers / `EditorURL(` callers;
   `FuzzEditorURL` deregistered from `scripts/run-fuzz.sh` (`bash -n` clean; no editorurl residue in code).
   `SERF_HUB_EDITOR_URL_TEMPLATE` (envvars.go:66,171 + main.go:480 + 2 tests) is **still registered with no
   consumer** — accurately reported as an orphaned knob pending Jesse's decision, not mine.

---

## DUTY 2 — Delta re-review of the three unreviewed commits

- **`cf5e42d6b` absorb — CLEAN.** Parents: P1 `6ecc2dafd` (deletion) / P2 `5fe6804c1` (wave 8);
  merge-base `b51d99f0f`. **web_test.go OURS**: the wave's *single* changed line was `.Unix()`→`.UnixMilli()`
  inside `TestWeb_WorkspaceRendersBottomStopForActiveSession`; the deletion side removed that whole test
  (P1=0, P2=1, absorb=0, HEAD=0), so OURS is correct — HEAD web_test.go == P1. **doc_serve_test.go union**:
  `TestWriteDocPages_NoDeadAssetReferences` (P1=1/P2=0) + the three truncation tests (P1=0/P2=1 each) all
  present at the absorb → complete union; truncation tests byte-intact. The combined diff's third file,
  `web.go`, is a clean auto-merged union (P2's `/popout.html` registration + P1's M10 removals both present),
  **not** a hand conflict — the commit's only recorded conflicts are the two named test files. No stray
  conflict markers anywhere in the tree.
- **`5d04baf7e` raw-only — CLEAN.** At the parent, the only production callers of `writeDocPage`/
  `writeDocMarkdownPage`/`formatDocBytes` were `handleDocFile`'s own non-raw branches (doc_serve.go:83/88/91);
  the rest were the two fuzz files + the NoDeadAssets test — all removed here. Post-commit grep receipt = zero
  Go hits. 400 hint copy = `format=raw required`. Containment-before-format order preserves parity: both raw
  **and** non-raw `RejectsTraversalDotDot`/`RejectsAbsolutePathEscape`/`RejectsSymlinkEscape`/`UnknownSession404`/
  `NonLocalSession404` tests pass (`go test -run TestDocFile|TestDocImage` → ok). RED credible: pre-deletion had
  no format gate, so missing/empty/non-raw fell to the 200+HTML branch (the 3 new 400 tests would have gotten
  200). The 4 subjectless deletions each asserted 200 + server-rendered HTML (`<pre>`, `marked`/`doc-markdown`,
  `binary` notice, file-content) — equivalent serving now covered by the kept `TestDocFile_Raw_Serves*` trio.
  CSP/httpsec untouched (not in the 4-file stat).
- **`9826fc3a3` editorurl — CLEAN.** Deletes exactly the 3 package files (editorurl.go / _test.go / _fuzz_test.go)
  + the one `scripts/run-fuzz.sh` TARGETS line + the report §9 appendix. Grep receipts hold (no importers, no
  residue). Deregistration is a clean single-line removal; syntax clean.

---

## Inventory-boundary sweep (the catastrophic-failure gate)

270 whole files deleted in `b51d99f0f..HEAD`, fully bucketed with an **empty residual**:
assets `*.js`+`style.css` **34**, `templates/**` **25**, `jstest/**` **204**, `internal/editorurl/**` **3**,
sanctioned `cmd/serf-hub/*_test.go` **3** (`cov_web_settings_pass5_fuzz_test.go`, `web_launchconfig_test.go`,
`web_launchpad_test.go`), `frontend/**` **1** (`panes/spawn/modelField.module.css` — wave-8's own merged/reviewed
removal). Reconciles with the reviewer's 266 + 3 editorurl + 1 wave-8 frontend. **Zero deletions outside the
sanctioned inventory.**

---

## FINDINGS

**None.** (GATE-CLEAN.)

Non-blocking observations, all pre-disclosed and correct as-is, none actionable by this gate:
- `SERF_HUB_EDITOR_URL_TEMPLATE` is now a dead env knob (no consumer) but stays registered — Jesse's call.
- `internal/httpsec/httpsec.go` CSP comment still names the deleted `app.html`; the `unsafe-inline` exemption
  is correctly retained and untouched (discrepancy #3; unrelated to the three new commits).
- The absorb's combined diff surfaces `web.go` as an auto-merged union (expected `--cc` behavior; `web_test.go`
  is compressed out because OURS matches one parent) — verified a correct union, not improvised resolution.

---

## Addendum — 2026-07-23: delta-bless two env-var removal commits (CLEAN)

After the GATE-CLEAN verdict above, Jesse ruled **REMOVE** on the orphaned `SERF_HUB_EDITOR_URL_TEMPLATE`
knob. Two follow-up commits landed on `m10-deletion` (tip now `6e0bdbc7d`, atop my report `28454f115`).
**This supersedes the FINDINGS-observation "stays registered — Jesse's call": the knob is now removed.**
Both commits re-reviewed against the merged tree; each does exactly and only what it claims.

- **`ac9d1ebf9`** (env registration removal) — CLEAN. Removes `SERFHubEditorURLTemplate` from all four Go
  sites and nothing else: `envvars/envvars.go` (the `Var` catalog entry + its `allVars` registration),
  `cmd/serf-hub/main.go` (`printHubEnvVars` slice line), `cmd/serf-hub/testmain_test.go` (TestMain unset
  list line), `cmd/serf-hub/main_test.go` (the `wantSummary` map entry, with gofmt re-aligning the map
  because the removed key was the longest — pure formatting-tool churn, remaining 7 entries content-identical).
  Plus a `m10-deletion-report.md` §9-follow-up addendum recording the receipts (doc, expected).
- **`6e0bdbc7d`** (user-doc removal) — CLEAN. Removes exactly the two stale rows that still named the knob:
  the `README.md` "Open-in-editor links" bullet (it described the legacy `/settings/*` `vscode://` feature
  deleted with the old UI; the SPA has no editor links — confirmed by grep) and the `docs/environment.md`
  table row. Two single-line deletions, nothing else.

**Receipts (re-run against `6e0bdbc7d`):** Go-sites grep `SERF_HUB_EDITOR_URL_TEMPLATE|SERFHubEditorURLTemplate`
`-- '*.go'` → **0**; whole-tree residue → only the two internal sdd reports (prior-state refs), user docs now
clean; `frontend/src` editor-link grep (`vscode://|editor-url|SERF_HUB_EDITOR`) → **0** (README bullet was
legacy-only); `go build ./...` → **0**; `go test . -run TestSupportedEnvVars -count=1` → **PASS 2/2**
(`AreDocumented` + `UseRegistryRows`, catalog ⊆ doc holds); `go test ./cmd/serf-hub/... ./envvars/...` → **0**;
`make lint` → **PASS (7 modules)**. **Appendix-C protected inventory untouched** — the 7 changed files include
no `hubapi/`, `web*.go` route/handler, `auth_token`, `/assets/`, `doc_serve`, `app_rpc`, or `webnext` surface.
Working tree clean at `6e0bdbc7d`.

Noted (not mine to act on, disclosed by the committer): root-package `go test .` fails
`TestMakeRuntimeAliasesBuildThePair/build-hub` — a `make build-hub`→`build-web` fixture needing the frontend
dir from a temp dir; independent of the env-var removal (which touches only the 7 files above) and outside
the scoped receipts. The env-var removal cannot affect a build-web Makefile fixture.

### Addendum verdict: **CLEAN** — validated tree == merged tree at `6e0bdbc7d`.
