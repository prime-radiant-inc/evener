# Wave 8 fix-round — adversarial review

**Verdict: APPROVED** (2 Minor findings, both on the popout item's *justification prose* — no code/behavior defect).
**Range:** `0713970d8..942aef99e` (branch `w8-fixes`, 6 code commits + report).
**Reviewer stance:** skeptical; every roster claim re-verified against code/Go/dockview source; all four gates re-run from clean tree; six mutation nets reproduced live by me and reverted.

## Gate summary (re-run by me, `cmd/serf-hub/frontend`, AND-chained, honest exit codes)

`npx tsc --noEmit` **0** → `npx vitest run` (bare) **239 files / 3405 tests, 0 failed, exit 0** → `npm run lint` (biome ci) **0** → `npm run build` **0** + `git restore dist/PLACEHOLDER` → **tree clean**. Measured counts match the report exactly (237→239 files, 3400→3405 tests: +2 new test files, +5 tests, all reconcilable from the diff).

## Probe outcomes

- **P0 sanction minimality — PASS.** AppShell.tsx diff is ONLY the two eager side-effect imports (`../panes/doc`, `../panes/transcript`) + their comment; Session.tsx diff is ONLY `sessionRef={ref}` on the `TurnBlock` renderRow. paneActions.ts change is comment-only (verified: zero non-comment code lines). No other chokepoint, no `protocol/reducer.ts`, no `styles/tokens.css`, no `chrome/**`/`StatusRow`, no `DockHost/paneRegistry/index.html/routing`, no `.go` changed.
- **P1 layout-restore — PASS.** `paneRestore.test.ts` drives the REAL `workspaceStore.restoreLayout` → real `readPanelParams`→`isRegistered`→`paneFor`→registry, with registration from the real `import "./AppShell"` side effect (FakeDockviewApi doubles only the dockview `fromJSON/panels/activePanel/clear` seam, as workspace.test.ts does). Panel-param shape (`params.paneType/paneParams`) is wire-faithful. Both mutations reproduced by me: remove doc import → `ok=false` (whole layout discarded); remove transcript import → same. Boot cost negligible — both index modules keep the component behind `lazy(()=>import("./DocPane"|"./Transcript"))`; eager registration pulls only the descriptor (+`filenameOf`), NOT the heavy chunk.
- **P2 turn-failure wiring — PASS.** Test renders the REAL Session tree with a wire-true failed turn (userMessage item + `error`; reducer.ts:216 `wireToTurnScalars` maps `error`, wireToTurnModel maps items). `canRetry = sessionRef!==undefined && text!==undefined` (TurnFailureEndCap.tsx:60); fixture yields badge "error"→`recoveryLabel "Retry"`. Mutation reproduced: drop `sessionRef` → "Unable to find button Retry" while `turn-failure` diagnostic still renders — exactly the pre-wiring RED.
- **P3 popout dormancy — PASS (2 Minor prose findings).** `assertSameOriginPopoutUrl` is present in the imported ESM (`node_modules/dockview-core/dist/package/main.esm.mjs`; source at `dist/esm/popoutWindow.js:19`, enforced in `open()` at :83). Empirically confirmed it REJECTS `about:blank`/`data:`/`blob:` (protocol not http(s)) and accepts only same-origin http(s) — T6's suggested about:blank override genuinely fails against 7.0.2. `popoutDormant.test.ts` guards that NO non-test app source calls `popOutPane(` (a static call-site guard = "uncalled"); mutation reproduced (added a probe call site → guard lists it → fail; removed → pass). See findings on the "guard is newer" prose.
- **P4 item-6 no-change — PASS.** All three cited nets exist at BASE (files untouched in fix round): `deriveAskQuestions.ts:44-50` `item.error===undefined` guard, `askDockStore.ts:200` `reconcileRef` derives solely from `liveAskQuestions(model)`, `deriveAskQuestions.test.ts:94-101` denied-ask test. Provenance = `7cf8eb4a7 webui absorb-a1: exclude errored/denied ask_user calls` — the absorb roster the report named. Load-bearing net mutation reproduced: drop the guard → errored ask leaks into the live set → test bites.
- **P5 task_list fixture — PASS.** Reopen fixture is wire-true: `task_store.go` Update's `default` case rejects any status ∉{open,in_progress,done,cancelled} (status-less update errors, so the old `{id,notes}`→"Updated 1." pair is impossible); `formatTaskUpdates` (session_tools_task.go:59-60) emits `"%d→%s"` and the caller wraps `"Updated "+…+"."`→"Updated 1→open.". Coverage identical (assertions unchanged; `TOUCH_BY_STATUS` lacks "open"→no row). Mutation reproduced: add `open` to `TOUCH_BY_STATUS` → reopen renders a row → `task-card-row===null` bites.
- **P6 harness-scoped enrichment — PASS.** `FetchCatalogOptions{harness?,cwd?,signal?}` pre-existed at BASE (catalogClient.ts unchanged) — the fix only threads them through. Scoping mutation reproduced: unscoped `fetchModelCatalog()` → scoping test fails, degradation+safety still pass. Degradation is proven by a dedicated pre-existing test (ModelField.test.tsx:157 "still lists the scoped models when /api/models is unavailable" mocks `fetchModelCatalog` to **reject**); `Promise.all([loadModels(), fetchModelCatalog({harness,cwd}).catch(()=>null)])` preserves both that path and the loadModels-rejection→inline-error path (test:50). `.catch` on the array element also prevents an unhandled rejection when loadModels rejects first. useCallback deps correctly add `harness,cwd`.
- **P7 zoom a11y — PASS.** `aria-label="Zoom image"` fits the design-system copy register (verb-noun, sentence case) — consistent with existing "Attach image", "Edit message", "Hide sidebar". Test-locked (button name asserted "Zoom image"; img keeps filename alt; falls back to filename without the label).
- **P8 gates — PASS.** See gate summary. All four green from a clean tree; final tree clean.

## Additional accuracy checks
- Report's HARD-CONSTRAINTS block (only the two sanctioned chokepoints; no chrome/StatusRow/reducer/tokens/Go) — all independently verified true.
- Two no-change items' primary-source rationale (5 lightbox divergence, 6 already-resolved) both check out; item-6's is mutation-verified load-bearing.
- Item-1 double-registration hazard checked: `registerPane` is `registry.set` (idempotent), each pane type has exactly one runtime registrant, eager+lazy paths resolve to the same once-evaluated module — safe.
- Test-count arithmetic verified against the MEASURED run (239/3405); the +2 files/+5 tests delta is directly reconstructable from the diff.

## Findings

### Critical
_None._

### Important
_None._

### Minor
1. **Unsupported "guard is newer" version-history claim (paneActions.ts `popOutPane` comment + report §3).** Both state the `assertSameOriginPopoutUrl` guard "is newer than the build T6's review read." Verified false: dockview-core was **7.0.2 at both** T6's base (`e3b9c188c`) and the fix base (`0713970d8`) with no package.json/lock change between them, and the guard is present in the exact `dockview-core.js` bundle T6 cited at `:16060`. T6 simply did not inspect the guard function — it is not newer. The dormancy DECISION and the empirical evidence (about:blank rejected) are correct; only the *why-T6-missed-it* rationale is invented. Per the project's "never invent technical details" rule, soften to e.g. "`assertSameOriginPopoutUrl` — a guard T6's review didn't examine — rejects about:blank." (No code/test/behavior impact.)
2. **Comment narrates review process/history + imprecise line cite.** The same sentence references "T6's review read" / "no longer holds" — process/history narration in a source comment rather than the code's current WHAT/WHY (CLAUDE.md comment rule). Also, `popoutWindow.js:81-93` points at the enforcement call site inside `open()` (the guard fires at :83); the `assertSameOriginPopoutUrl` definition is at `:19`. Trivial; the guard is real and correctly characterized. Fold into fix (1) if the prose is revised.

## Why APPROVED
All eight roster items are correctly resolved: five code fixes (1,2,4,7,8) each carry a mutation net I reproduced live; item 3 stays dormant on correct, empirically-verified dockview evidence with a real static call-site lock; items 5 and 6 are no-change with sound primary-source rationale (6's existing net mutation-verified). The two sanctioned chokepoint edits are exactly minimal and nothing else forbidden was touched. All four gates are green from a clean tree with counts matching the report. The only findings are Minor prose/accuracy issues in the popout item's justification — no behavioral, test, or design-system defect — so they do not warrant a fix round; fold the prose correction into the T8 sweep or a trivial follow-up.
