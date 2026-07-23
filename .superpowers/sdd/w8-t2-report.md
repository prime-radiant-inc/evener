# Wave 8 T2 — model-catalog restore — report

**Status:** DONE_WITH_CONCERNS
**Branch:** `w8-catalog`  **Commit range:** `e3b9c188c..dec5f6343` (4 commits, off the wave branch after T1)
**Gates (final tree, honest exit codes, AND-chained per commit):** tsc 0 · vitest 0 (**226 files / 3258 tests**, baseline 223/3217 → **+3 files, +41 tests**) · biome ci 0 · `npm run build` 0 (dist/PLACEHOLDER restored, tree clean). Mutation-verified the two load-bearing invariants (scoping guarantee + `diagnostics=1` decision) — both go RED when mutated.

No push, no merge.

## Commits
1. `a7209e2ff` rich ModelCatalog widget (badges, cost, provider grouping, Recent, on-demand diagnostics) + pure view helpers
2. `2f79fee49` `/api/models` wire loader (`fetchModelCatalog`, `diagnostics=1` envelope, snake→camel mapping)
3. `1bdea17a3` swap into spawn `ModelField` (scoped set from `loadModels` + best-effort `/api/models` enrichment via `mergeScopedCatalog`)
4. `dec5f6343` swap into settings `modelPicker` (`fields.tsx`)

## What shipped
- **`widgets/modelCatalog/` (rich picker).** A searchable Combobox popup: each row shows the display name + capability badges (tools / vision / web search / reasoning, as neutral Chips) + input/output cost (`$3 in · $15 out /Mtok`) + context window (`200k`); options carry provider group heads; a **Recent** quick-pick section from `recent[]`; and an **on-demand** "N providers unavailable" affordance listing the envelope's diagnostics. `value`/`onChange` are the byte-exact interim contract, so both swap sites are a one-import change.
- **Helper modules (all pure/unit-tested):** `catalogView.ts` (options, provider grouping, capability/cost/context formatting), `catalogClient.ts` (`fetchModelCatalog` + `mapApiEntry`), `scopedCatalog.ts` (`mergeScopedCatalog`).
- **Both swap sites:** `panes/spawn/ModelField.tsx` (thin scoped adapter) and `panes/settings/sections/launchShared/fields.tsx` (`modelPicker` kind). Existing tests preserved (only setup churn) + new catalog-behavior tests at every layer.

## Wire truth (traced against `web_spawn.go handleApiModels` + `web_test.go` before pinning any fixture)
- **The default `/api/models` is a BARE ARRAY** (`[{entry}, …]`, models only). The `{models, diagnostics, recent}` envelope exists **ONLY with `?diagnostics=1`** (`writeModelsResponse`). So `recent[]` and `diagnostics[]` are **bundled** — you cannot fetch Recent without diagnostics. Confirmed by `TestHandleApiModels_DiagnosticsEnvelopeIncludesRecent` and `TestWeb_ApiModels_DiagnosticsParamReturnsModelsAndDiagnostics`. **This partially contradicts the plan's framing** (see Concern 1).
- `/api/models` accepts `harness`, `cwd`, `diagnostics` query params (`web_spawn.go:177-183`).
- Entry fields match the pin exactly; capability/cost fields are `omitempty` and **absent** for a model the embedded catalog doesn't know → all optional client-side (mapped only when present).

## Concerns / decisions for the controller

1. **CONTRADICTS the plan's "do NOT fetch diagnostics on every open."** Wire truth: `recent[]` lives **only** in the `?diagnostics=1` envelope. Showing Recent (a required feature) therefore **requires** requesting `?diagnostics=1` on every open. Reconciliation applied: (a) it costs the server **nothing** — the diagnostics are computed regardless; the flag only toggles serialization (verified in the handler body); (b) I honor the **spirit** — diagnostics are NOT shown until the user opens the on-demand affordance. If the controller wants a genuinely diagnostics-free default fetch, `recent` must move to the default response (a Go change) or Recent drops from the picker. I chose fidelity-to-feature over fidelity-to-literal-text; flagging for a ruling.

2. **`ModelCatalog` (the return type) widened with an optional `diagnostics?: ModelCatalogDiagnostic[]`** (+ a new `ModelCatalogDiagnostic` type). The **locked seam that the swap sites construct — `ModelCatalogProps` (value/onChange/loadCatalog) — and `ModelCatalogEntry` are byte-exact unchanged.** Only the loader's RETURN type gained an optional field, which is backward-compatible: the dev gallery, the byte-exact test, and both swap sites compile unchanged. This was the minimal way to carry the on-demand diagnostics through the injected-loader architecture (props are frozen). `ModelCatalogDiagnostic` is intentionally NOT barrel-exported (internal); controller may add it for symmetry with `ModelCatalogEntry`.

3. **Spawn swap done WITHOUT editing `Spawn.tsx` (out of T2's manifest).** The plan pin says `loadCatalog` is "a /api/models call (harness-scoped)", but `ModelField` only receives `loadModels` (the harness/cwd-scoped `model/list`), never `harness`/`cwd`, and the injector `Spawn.tsx` is not in T2's manifest. Chosen approach — `mergeScopedCatalog`: `loadModels` supplies the authoritative **scoped SET** (so a non-default harness never shows the wrong models — a correctness guarantee I mutation-verified), and a best-effort `/api/models` call **enriches** it with badges/cost/Recent. For the default serf harness (common case) the sets coincide → fully rich; other harnesses degrade to label-only rows (no regression — the set is always scoped); an `/api/models` failure degrades to the plain scoped list, never an empty picker. **If the controller prefers a single harness-scoped `/api/models` call, that is a ~3-line `Spawn.tsx` change** (pass `harness`+`cwd` to `loadCatalog`, drop the `model/list` pre-fetch) — flagged, not done, because `Spawn.tsx` is outside my manifest and Rule #1 forbids the unilateral exception.

4. **`fields.test.tsx` ownership (PIN-D nuance).** T2 owns `fields.tsx`; I edited its test (`fields.test.tsx`) per the "existing tests keep passing with setup churn" instruction. Edits are localized to the `modelPicker` test + a top-of-file `vi.mock`. **T7 (owns `panes/settings/**` minus `fields.tsx`) should not also touch `fields.test.tsx`'s modelPicker portions** to avoid a serial-merge collision.

5. **Reasoning-effort "none-vs-(default)" split (plan T2 bullet) NOT built into the widget.** The `ModelCatalog` prop surface is `value`/`onChange` (a model string) — it carries no reasoning-effort in/out. The effort selector is a **separate field** at spawn (`Spawn.tsx REASONING_OPTIONS`, already `(default)` + levels + `none` — not my file) and mid-session (T4's `StatusRow` + `DEFAULT_EFFORT_LEVELS`). Catalog rows **display** the reasoning capability as a badge. A per-model effort control *inside* the picker would need a prop-surface (seam) change — flagged, not done.

6. **`dev/gallery-sections/modelCatalog.tsx` (T1's file, outside T2's manifest) left with its minimal fixture.** The rich widget renders correctly there (label-only rows, no Recent — the fixture omits badges/cost/recent), and the `WidgetGallery` completeness guard passes. Enriching the fixture to visually demo badges/cost/Recent is a nice follow-up but outside T2's manifest.

7. **`panes/spawn/modelField.module.css` deleted.** `ModelField` is now a thin adapter (all styling lives in the widget); the CSS was orphaned (no references). Manifest-listed file, safe delete (no guard breaks — verified).

## Verification depth
- TDD RED-first at every layer; wire-true fixture pinned against the Go handler + captured test frames (not invented).
- **Mutation checks:** (a) making the merged SET come from the enrichment instead of the scoped list → `scopedCatalog` + `ModelField` scoping tests go RED; (b) dropping `diagnostics=1` from the URL → `catalogClient` tests go RED. Reverted; tree clean.
- Full 226-file suite green after revert; `Spawn.test.tsx` and `LaunchConfigForm.test.tsx` (which render the swapped components) pass unregressed. No live-hub smoke at T2 (T8 owns the wave live-proof); jsdom DOM-level tests + passing build + the static `requireclass-contract`/`token-contract` guards cover the render path.

## Files
- **NEW:** `widgets/modelCatalog/{catalogView,catalogClient,scopedCatalog}.ts` + `{catalogView,catalogClient,scopedCatalog}.test.ts`
- **MODIFIED:** `widgets/modelCatalog/{index.tsx,modelCatalog.module.css,modelCatalog.test.tsx}`; `panes/spawn/ModelField.{tsx,test.tsx}`; `panes/settings/sections/launchShared/fields.{tsx,module.css,test.tsx}`
- **DELETED:** `panes/spawn/modelField.module.css`
- **NOT touched (as required):** `widgets/index.ts` (barrel — already exports ModelCatalog from T1), `panes/spawn/Spawn.tsx`, `src/protocol/reducer.ts`, `src/styles/tokens.css`.
