# Wave 8 fix-round report

**Status:** DONE
**Branch:** `w8-fixes` (off the wave tip `0713970d8`)
**Commit range:** `a2ce6b761..b1c151e75` (6 commits; items 5 and 6 required no code change — see below)
**Baseline re-verified:** 237 files / 3400 tests green at the wave tip before any work.
**Final gates (from `cmd/serf-hub/frontend`, AND-chained, honest exit codes):**
`npx tsc --noEmit` 0 · `npx vitest run` **239 files / 3405 tests, 0 failed**, exit 0 · `npm run lint` (biome ci, 659 files) 0 · `npm run build` 0 + `git restore dist/PLACEHOLDER` + tree clean.
Test delta +2 files / +5 tests: paneRestore.test.ts (item 1), popoutDormant.test.ts (item 3), + one new case each in Session.test.tsx (item 2), DocPane.test.tsx (item 4), ModelField.test.tsx (item 8). Item 7 corrected a fixture in place (no count change).

HARD CONSTRAINTS honored: only the two sanctioned chokepoint edits (AppShell.tsx item 1, Session.tsx:182 item 2). No `chrome/**`, no `StatusRow`, no `reducer.ts`, no `tokens.css`. No Go changes. No merges/pushes. Every commit AND-chain-gated (tsc → vitest → lint → build).

---

## 1. Layout-restore hazard — FIXED (commit `a2ce6b761`, sanctioned chokepoint: AppShell.tsx)

**Finding (confirmed against source, both stream reviews):** DockHost restores the persisted dockview layout at boot (`DockHost.tsx:282-285` in `handleReady`, before any lazy opener runs). `doc` registers only via `openDoc.ts` and `transcript` only via `paneActions.ts:20` — so at shell boot neither is registered (`AppShell.tsx` eager-registered only welcome/session/settings/spawn). A persisted layout containing either pane reaches `restoreLayout` → `readPanelParams` throws on the unregistered type (`workspace.ts:123-129`) → the catch calls `dockviewApi.clear()` and discards the **entire** saved workspace (`workspace.ts:208-230`), not just the one pane.

**Change:** Added two side-effect imports (`import "../panes/doc"` / `import "../panes/transcript"`) to AppShell's boot block, matching the existing welcome/session/settings/spawn pattern. Heavy components stay `lazy()`, so boot cost is negligible.

**RED evidence:** New `src/shell/paneRestore.test.ts` imports AppShell (running the real boot registrations) and drives `restoreLayout` against a layout containing a doc pane AND a transcript pane via the workspace.test.ts FakeDockviewApi pattern. Before the fix: `restoreLayout` returned `false` (whole layout discarded) — assertion `expected false to be true`.

**Mutation proof (2 nets):** removing `import "../panes/doc"` → test bites (doc unregistered → discard); removing `import "../panes/transcript"` → test bites. Both restored; GREEN with both imports.

**Note:** the test could not be added to `workspace.test.ts` (its line 95 asserts `openPane("transcript")` throws, which importing AppShell would break) — hence the dedicated file, which is also what couples the mutation net to AppShell's imports.

## 2. Turn-failure recovery wiring — FIXED (commit `e1c146660`, sanctioned chokepoint: Session.tsx:182)

**Finding:** `TurnFailureEndCap`'s Retry/Reconnect button renders only when `TurnBlock` receives `sessionRef` (its `canRetry` gate); `TurnBlock` gets it solely from Session.tsx's `renderRow`, which passed no `sessionRef` — so the shipped, fully-tested recovery affordance was dark in production (diagnostic rendered, button withheld). One-liner verified in w8-t3-review.md P6.

**Change:** `renderRow={(index) => <TurnBlock turn={turnAt(index)} sessionRef={ref} />}` (`ref` in scope, used identically at Session.tsx:145-188).

**RED evidence:** New integration test in `Session.test.tsx` mounts the real Session with a hydrated failed turn carrying a `userMessage` item + `error` (wire-true; `reducer.ts` `wireToTurnModel` maps both). Before the fix: `turn-failure` end-cap rendered but `getByRole("button", { name: "Retry" })` threw "Unable to find" (the button was dark).

**Mutation proof:** the pre-wiring RED run IS the exact "sessionRef absent" mutation. The fixture supplies both the ref (via the fix) and the retry text (userMessage) — the button requires both, so the net is tight. GREEN after wiring.

## 3. Popout gap — KEPT DORMANT (commit `96340c616`; evidence over invention)

**Finding (investigated against dockview source):** dockview 7.0.2's `addPopoutGroup` does `window.open(popoutUrl)` (default same-origin `/popout.html`), waits for `load`, then appends its container into that window's `document.body` (`popoutWindow.js` `open()`). Two hard facts make a **frontend-only fix impossible in this version**:
  1. serf-hub serves no `/popout.html`; its SPA fallback returns index.html → the default URL boots a **second full app instance** in the popout window.
  2. An `about:blank`/`data:`/`blob:` `popoutUrl` override is **rejected** by dockview's own `assertSameOriginPopoutUrl` guard (`popoutWindow.js:81-93`): the URL must be same-origin **http(s)**. Verified the guard is present in the rolled-up ESM bundle vite actually imports (`dist/package/main.esm.mjs`, one occurrence of the "same-origin http(s)" throw). **This guard is newer than the build T6's review examined** (`dockview-core.js:16060`), which is why T6's `about:blank` suggestion no longer holds against the installed 7.0.2.

The only working mechanism is a served same-origin http(s) blank shell — a Go route, reserved for Jesse. So popout stays dormant: `popOutPane` has **zero** application call sites (grep-confirmed; only its own def + tests + a workspace.ts comment) and there is no "pop out" affordance anywhere in the UI source.

**Change:** rewrote the `popOutPane` WHAT/WHY comment with the precise evidence above (no Go route added). Added `src/shell/popoutDormant.test.ts` — a node-fs static guard (same technique as `requireclass-contract.test.ts`) asserting no application source file calls `popOutPane(`, locking the dormant state so any future popout affordance fails loudly and confronts the served-shell decision.

**RED/GREEN + mutation proof:** guard GREEN today (no call sites). Mutation: a throwaway `src/shell/__popout_probe__.ts` calling `popOutPane("x")` → guard bites (lists the probe); removed → GREEN.

## 4. Doc-pane zoom button a11y — FIXED (commit `dba8f2afb`)

**Finding:** the click-to-zoom `<button>` (`DocPane.tsx:105`) derived its accessible name from the nested `<img alt={filename}>` — announcing the filename (a duplicate of the pane title) instead of the action.

**Change:** added `aria-label="Zoom image"` to the button (plain verb, sentence case, per the design-system copy register). The `<img>` keeps its filename `alt`.

**RED evidence:** new DocPane.test.tsx case asserts `getByRole("button", { name: "Zoom image" })` and that the image alt stays the filename. Before the fix: "Unable to find an accessible element with role button and name Zoom image" (the button's name was `pic.png`).

**Mutation proof:** the pre-fix RED is the exact "no aria-label" mutation (name falls back to the filename → test bites). GREEN after adding the label; full DocPane suite 14/14.

## 5. Lightbox convergence — ASSESSED, DIVERGE (no code change; do-not-force)

**Assessment:** DocPane's lightbox (`DocPane.tsx` `DocImageView`) is a **single** fit-to-pane image with image-load-error handling (`onError` → EmptyState) and no navigation. ImageGallery (`transcript/flow/ImageGallery.tsx`) is a **multi-image thumbnail strip** with prev/next wrap-around navigation and no per-image error handling. Their only overlap is the ~5-line "`<Dialog>` wrapping a full-size `<img className=lightboxImg>`" moment — and both already build on the shared `Dialog` widget (shared Escape/scrim/focus-trap), so the load-bearing modal behavior is already converged.

**Verdict: not a clear simplification — change nothing.** A shared `ImageLightbox` would need a bimodal prop surface (single vs. gallery+nav, differing alt/title conventions "Zoom image"/filename vs. "Image N of M", optional error handling): a ~30-40-line widget plus its own test to remove ~5-10 lines per site. That is a net **increase** in lines and indirection — it fails the roster's "fewer lines, one behavior" bar (and YAGNI). Recorded here for the T8 sweep as a conscious divergence; the two surfaces stay independent atop the shared `Dialog`.

## 6. Answerable-denied-ask — ALREADY RESOLVED at the wave tip (no code change)

**Finding:** wave-5 close recorded HIGH #1 (a denied/errored `ask_user` rendering an answerable card) as unfixed, with the fix predicted to land "with the absorb roster's ItemModel.error consumption." It **has** landed. Primary-source verification (not a narrative claim):
- `deriveAskQuestions.ts:44-50` — `isAckedAskUserItem` already gates on `item.error === undefined` (with a comment describing exactly this fix and the projector's `status:"completed"`-on-error behavior).
- `askDockStore.ts:200` — `reconcileRef` derives the live ask set **solely** from `liveAskQuestions(model)` on every model change; `reconcileBatches` layers batch state on the already-filtered set and can never re-add a denied ask. No non-test askDock code bypasses the guard.
- `deriveAskQuestions.test.ts:94-101` — an existing test ("excludes an errored/denied ask_user call") locks it; green in the 3400-test baseline.
- w8-t3-review.md P4 independently confirms the guard ("already excludes error!==undefined").

**Why no change:** the recorded fix and its test are already present and correct. Adding a duplicate test or a no-op commit would be dishonest churn. Instead I **mutation-verified the existing test is load-bearing** (not vacuous): removing `&& item.error === undefined` from the guard → the denied-ask test bites; restored → green, tree clean. The description was unambiguous (roster's STOP-on-ambiguity condition did not apply); the situation was simply "already done," which I verified from primary sources rather than assumed.

## 7. task_list fixture wire-fidelity — FIXED (commit `86347e755`, test-only)

**Finding (w8-t3-review.md M2, verified against Go):** the "note-only update" fixture `{action:"update", updates:[{id:1, notes:...}]}` → `"Updated 1."` is wire-impossible. `agent/task/task_store.go:463-467` `Update` rejects any status not in {open,in_progress,done,cancelled}, so a status-less update never succeeds; `formatTaskUpdates` (`session_tools_task.go:59-60`) emits `"1→open"` when a status is set. The real update that touches a task without earning a row is a **reopen** to `open`.

**Change:** replaced the fixture with `{id:1, status:"open", notes:"added a caveat"}` → `"Updated 1→open."` and reworded the test title/comment. Coverage identical: `TOUCH_BY_STATUS` (`taskCard.tsx:101-105`) maps only done/cancelled/in_progress, so `open` yields no row — the same empty-card path (task-card renders, no task-card-row). `taskCard.tsx:80` already documents "note-only or reopened update as no per-row change."

**RED/mutation proof:** GREEN with the new fixture (behavior unchanged). Mutation: adding `open: "started"` to `TOUCH_BY_STATUS` → the reopen fixture now renders a row → the `queryByTestId("task-card-row") === null` assertion bites (proving the new fixture genuinely exercises "open is not a flagged touch"). Reverted; taskCard.tsx pristine.

## 8. Harness-scoped catalog enrichment — FIXED (commit `b1c151e75`)

**Finding (w8-t2-report.md Concern 3):** `ModelField` called `fetchModelCatalog()` **unscoped**, so a non-default harness's models (scoped correctly by `loadModels`/`model/list`) got enriched against the default serf catalog, not their own. `fetchModelCatalog` already accepts `{harness, cwd}` (`catalogClient.ts:67-82`); it just wasn't passed them.

**Change:** added optional `harness?`/`cwd?` to `ModelFieldProps`; `Spawn.tsx` passes `harness || undefined` / `cwd || undefined` (matching how `loadModels` scopes `model/list`); `loadCatalog` calls `fetchModelCatalog({ harness, cwd })`. Applied the optional rider: the scoped list and the enrichment now load together via `Promise.all([loadModels(), fetchModelCatalog({harness,cwd}).catch(()=>null)])` — a `loadModels` rejection still rejects loadCatalog (inline error preserved), a failed enrichment still degrades to the plain scoped list.

**Safety property preserved:** `mergeScopedCatalog(scoped, ...)` is unchanged — the scoped `loadModels` SET stays authoritative; enrichment never adds entries.

**RED evidence:** new ModelField.test.tsx case renders with `harness="codex" cwd="/tmp/project"` and asserts `fetchModelCatalog` was called with `objectContaining({harness:"codex", cwd:"/tmp/project"})`. Before wiring: called unscoped → assertion bites.

**Mutation proof (2 nets):** (a) scoping net — the pre-wiring RED (unscoped call) bites; GREEN after. (b) safety net — temporarily returning `enrichment ?? mergeScopedCatalog(...)` (SET from enrichment) → the "keeps the scoped model SET" test bites (enrichment-only "Claude" leaks in); reverted. All three pre-existing scoping/safety tests (ModelField.test.tsx lines 50/111/134) stay green.

**Scope note:** `ModelField` is imported only by `Spawn.tsx` (grep-confirmed); the settings swap site (`fields.tsx`) uses the `ModelCatalog` widget directly and is unaffected — `harness`/`cwd` are optional, so absent = an unscoped enrichment as before.

---

## Concerns
None blocking. Two items (5, 6) were assessed to need no code change with primary-source rationale above; both are recorded for the T8 sweep. The popout served-shell (item 3) and any Go-side follow-ups remain reserved for Jesse.
