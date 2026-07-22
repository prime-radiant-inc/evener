# Web Rewrite Wave 6 — T2 (spawn pane body) report

**Status:** DONE_WITH_CONCERNS (all concerns are conscious, plan-sanctioned scope divergences to annotate in the T6 parity sweep — nothing broken).

**Branch:** `w6-spawn` off `835c17e2b`. **Commit range:** `835c17e2b..62cd8da63` (8 commits, `a6a216002` → `62cd8da63`).

**Manifest discipline:** every change is under `cmd/serf-hub/frontend/src/panes/spawn/**`. No chokepoint, `widgets/index.ts`, `tokens.css`, or cross-stream file was touched.

## Gates (final)

- `npx tsc --noEmit` → exit 0.
- `npx vitest run` → **203 files / 2996 tests, exit 0** (baseline 194 / 2898 → +9 files, +98 tests; no pre-existing test regressed). Spawn manifest alone: 13 files / 102 tests, pristine output (no React act()/key warnings).
- `npm run lint` (biome ci) → exit 0.
- `npm run build` → exit 0; `dist/PLACEHOLDER` restored, tree clean.
- Every commit was gated tsc→vitest(count-up verified)→lint; densest logic mutation-verified (stale-model classifier, invalid-path drop).

## What landed (floor §1 coverage)

Pure logic (TDD, unit-tested):
- `accessMode.ts` — 4 fixed rows + sandbox map + schema-wins merge (§1.8), mirrors web_spawn.go.
- `spawnDefaults.ts` — per-project + global sticky-default layering, save rules (§1.9); stale-model classify (malformed/stale/unknown/valid) + full-sweep cleanup + discard reporting (§1.10).
- `branch.ts` — REST-only `GET /api/git/head?cwd=` HEAD resolution, fail-soft (§1.7).
- `urlPrefill.ts` — `?dir=`/`?prompt=` from `window.location.search` (spec §5).
- `schema.ts` — perLaunch/serf filter, collectAdvancedOverrides (tri-state boolean, skip unchecked/empty/invalid, non-empty collections), resolveScalars precedence (§1.11).
- `startThread.ts`/`preflight.ts` seams filled — accessMode→launchOverrides.sandbox merge; offer-create vs deterministic-abort discrimination (§1.13).

UI (TDD, RTL-tested):
- `DirField.tsx` — working-dir picker: recents (first-listing only) + completions (debounced 150ms) + browse-into-vs-accept-recent + `..` parent + use-current + Enter-commit + stale-requestID drop (§1.6).
- `ModelField.tsx` + `harnessModels.ts` — interim model/list Combobox (ModelSwitch pattern); serf-model-harness predicate (§1.4 interim).
- `AdvancedOptions.tsx` — schema-driven controls (select/radio/boolean/integer/text + pathList/modelList/envMap/mcpServerList via CollectionEditor), live path validation, "show resolved config" (§1.11).
- `Spawn.tsx` — full integration: 6-field launch bar; prompt + image attachments (reuses composer useAttachments/Dropzone/clipboard, ⌘/Ctrl+Enter submit, pending-block, attachment-only-allowed); sticky-default load/save; stale-model sweep + inline dismissible notice; preflight + in-form Create&start ConfirmDialog; startThread submit; `?dir=`/`?prompt=` prefill.

## Wire truth verified against Go source

- Branch is **display-only** — legacy `/api/spawn` drops `req.Branch` (web_spawn.go:135-144) and appwire `ThreadStartParams`/`LaunchConfigLayer` carry no branch field; startThread never sends it.
- Daemon makes **top-level scalars win over launchOverrides** (app_threadlifecycle.go:48) → schema model/reasoning precedence is hoisted into the top-level request (resolveScalars), not left only in launchOverrides.
- Daemon concatenates `modelProvider + "/" + model`, or uses a qualified `model` as-is → interim picker sends the qualified `model` alone.
- preflight abort strings pinned to fspaths.ValidateLaunchPath (`path is not a directory`/`absolute path required`/`path is required`); any other invalid reason (os.Stat miss) → offer-create.
- serf-model harness = descriptor `kind:"serf"` (app_models.go).

## Concerns (all for the T6 sweep to annotate — none are defects)

1. **Chip chrome → design-system controls.** The 6 params render as inline FormRow-labeled widgets (Select/Combobox/Input/DirField), not the legacy popup-chip DOM. §1.2's mechanisms (openPicker dispatch, three click-outside-dismiss impls, mobile bottom-sheet reparenting, toggle-off-on-re-click) are subsumed by the design-system widgets; "desktop+mobile mirrored rows" (§1.1) is one responsive CSS-grid control set, not two data-attribute-synced DOM trees. Capability parity met; annotate §1.2 as design-system-equivalent.
2. **Rich model/reasoning catalog = Wave 8** (interim Combobox/Select), per ledger-206. Floor §1.4/§1.5 rich rows (REST /api/models display_name/badges/grouping/Recent/pricing; none-vs-(default) split) are W8-deferred.
3. **Recent prompts dropped** (Jesse 2026-07-22) — floor §1.1 `.RecentPrompts`; built nothing.
4. **Reasoning-effort for a non-serf harness** is a disabled Select rather than the legacy inert note row (§1.5) — presentational only.
5. **Advanced-options control fidelity is interim**: modelPicker → plain text; multiline text → single-line Input; envMap/mcpServerList edited via CollectionEditor with a `NAME=value` / `name=command args` text encoding. The pure collect/precedence logic (schema.ts) is complete for all kinds.
6. **§1.15 (htmx re-init)** is N/A — React SPA, no htmx swap lifecycle.
7. No live-hub smoke was run — per the plan that is T1/T6, not a T2 stream gate (my gates are tsc/vitest/lint/build, all green).

## Cross-stream pins honored

- PIN-C: `InputAttachment`→`InputItem` via the mirrored buildInput shape; reused the shared Dropzone/useAttachments/clipboard leaves read-only.
- Qualified `thread.serf.ref` kept verbatim (SpawnResult ruling); routes via paneToURL.
- No global keydown added (PIN-D unaffected — ⌘/Ctrl+Enter is textarea-local, not a global chord).
