# Composer Circular Context Meter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `variant?: "bar" | "ring"` to the `Meter` widget and move `StatusRow`'s context usage indicator inside the grey `.identity` pill before the model name as a 14px circular progress ring at every width.

**Architecture:** Extend `src/widgets/meter` with a `"ring"` SVG variant sharing one tone-token mapping (`--meter-tone`) and single `role="meter"` accessibility contract, keeping `"bar"` as the default for `DetailsPanel` and `taskCard`. Move `StatusRow`'s context span inside `.identity` ahead of `ModelSwitch` with `flex: none`, remove the `<400px` clip/percentage fallback, and update unit tests, token contracts, widget gallery, design-system docs, and browser layoutguards to match.

**Tech Stack:** React 19, TypeScript, CSS Modules, Vitest, Testing Library, Biome, headless Chrome layoutguard (`scripts/layoutguard`).

**Spec:** `docs/superpowers/specs/2026-11-25-composer-context-ring-design.md`

## Global Constraints

- Design-system widget API contract (`docs/developing-evener/design-system.md`): export `MeterVariant` from `src/widgets/index.ts`, show every documented `Meter` state in `src/dev/gallery-sections/meter.tsx`, update the `Meter` row in §3 of `docs/web-ui/design-system.md`, and drive widget tests by `role="meter"` and accessible name.
- Token contract (`src/styles/token-contract.test.ts`): zero color literals outside `tokens.css`; `.track` keeps `background: var(--field)`; `.ringTrack` uses `var(--edge)`; semantic hues (`--attention`, `--alive`, `--danger`) stay inside allowlisted `widgets/meter/meter.module.css`.
- Motion contract (`src/styles/motion.contract.test.ts`): no transitions or autonomous animations on the ring fill.
- Copy is not a test oracle (`docs/developing-evener/testing.md`): assert roles, ARIA attributes, classes, data attributes, and layout geometry rather than hardcoded copy phrases.
- Never run `npm ci` through a symlinked `node_modules`. Check `[ -L cmd/evener-hub/frontend/node_modules ]` before installing; symlink to the main checkout's `node_modules` only when `package-lock.json` is byte-identical.
- Run Biome (`cd cmd/evener-hub/frontend && npx biome check --write <touched files>`) before running `make test-web` from the repo root.

## Review Focus

1. **Zero-fill stray cap dot:** With `stroke-linecap: round`, an SVG `<circle>` at `0%` (`strokeDashoffset === circumference`) paints a dot at 12 o'clock unless hidden. Pinned in Task 1 (`meter.test.tsx`) by asserting `value={0}` (and negative values) applies the empty-fill hiding class (`ringFillEmpty`) while positive values do not.
2. **Non-positive `max` or non-finite `value`:** Passing `max={0}`, `max={-5}`, or `value={Number.NaN}` must never emit `NaN` in `strokeDashoffset` or `aria-valuenow`. Pinned in Task 1 (`meter.test.tsx`).
3. **Ring squashed by narrow-pane flex shrinking inside `.identity`:** `.identity > span:not([data-effort-control])` has higher specificity than `.context`, which would give `.context` `flex: 0 1 auto; min-width: 0` and shrink the 14px ring when the model name ellipsizes. Pinned in Task 2 (`statusrow.module.css` selector exclusion) and Task 3 (`compact-session-footer` and `composer-tap-floor-phone` Chrome geometry checks asserting full 14×14px ring containment at 320px).
4. **Desktop pill track contrast against `--field` and phone surface contrast against `--surface-1`:** The ring track must use `var(--edge)` and maintain visible contrast against both `--field` and `--surface-1` in dark and light themes. Pinned in Task 1 (`token-contract.test.ts`).
5. **Context absent (`contextWindow === 0`) inside `.identity`:** When a session reports no context window, `.identity` renders only model and effort with no empty context spacer or leftover padding. Pinned in Task 2 (`StatusRow.test.tsx`).

---

### Task 1: Extend `Meter` widget with `variant="ring"`

**Files:**
- Modify: `cmd/evener-hub/frontend/src/widgets/meter/index.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/meter/meter.module.css`
- Modify: `cmd/evener-hub/frontend/src/widgets/index.ts`
- Test: `cmd/evener-hub/frontend/src/widgets/meter/meter.test.tsx`
- Test: `cmd/evener-hub/frontend/src/styles/token-contract.test.ts`
- Modify: `cmd/evener-hub/frontend/src/dev/gallery-sections/meter.tsx`
- Modify: `cmd/evener-hub/frontend/src/dev/gallery-sections/meter.module.css`
- Modify: `docs/web-ui/design-system.md`

**Interfaces:**
- Consumes: existing tokens `--field`, `--edge`, `--accent`, `--attention`, `--alive`, `--danger`, `--radius-pill` from `src/styles/tokens.css`.
- Produces:
  - `export type MeterVariant = "bar" | "ring"` (from `src/widgets/meter/index.tsx` and re-exported from `src/widgets/index.ts`)
  - `export interface MeterProps { label: string; value: number; max: number; tone?: MeterTone; variant?: MeterVariant }`
  - `data-variant="bar" | "ring"` on the `role="meter"` root element, and `data-testid="meter-fill"` carrying `TONE_CLASS[tone]` (`fillNeutral` | `fillAttention` | `fillAlive` | `fillDanger`) in both variants.

- [ ] **Step 1: Ensure worktree frontend dependencies are linked safely**

Run:
```bash
if [ ! -e cmd/evener-hub/frontend/node_modules ]; then
  cmp -s /home/jesse/git/prime-radiant-inc/evener/cmd/evener-hub/frontend/package-lock.json cmd/evener-hub/frontend/package-lock.json && \
    ln -s /home/jesse/git/prime-radiant-inc/evener/cmd/evener-hub/frontend/node_modules cmd/evener-hub/frontend/node_modules
fi
```
Expected: `cmd/evener-hub/frontend/node_modules` resolves cleanly without running `npm ci` through a symlink.

- [ ] **Step 2: Write failing unit tests for `Meter` ring variant and token contract**

In `cmd/evener-hub/frontend/src/widgets/meter/meter.test.tsx`, add tests covering:
- default `data-variant="bar"` when `variant` is omitted, and `data-variant="ring"` when `variant="ring"`.
- ring variant renders `role="meter"` with `aria-label`, `aria-valuenow`, `aria-valuemin="0"`, `aria-valuemax`, no `tabindex`, and an `aria-hidden="true"` SVG (`viewBox="0 0 16 16"`, `width="14"`, `height="14"`).
- ring variant clamps values above `max` (offset `"0"`), below `0` (offset equals circumference, has `ringFillEmpty` class), and `value={0}` (has `ringFillEmpty` class so no 12 o'clock round-cap dot paints); positive values omit `ringFillEmpty` and produce a `stroke-dashoffset` matching `2 * Math.PI * 5.7 * (1 - percent / 100)`.
- ring variant handles non-positive `max` (`max={0}`) and non-finite `value` (`Number.NaN`) without emitting `"NaN"` in `stroke-dashoffset` or `aria-valuenow`, applying `ringFillEmpty`.
- every `MeterTone` (`neutral`, `attention`, `alive`, `danger`) maps to `fillNeutral` / `fillAttention` / `fillAlive` / `fillDanger` on `[data-testid="meter-fill"]` in `variant="ring"`.

In `cmd/evener-hub/frontend/src/styles/token-contract.test.ts`, right after the existing `"Meter track sinks into --field..."` test (reusing its `themes` block extraction, since `THEME_BLOCKS` is declared further down the file), add:
```ts
test("Meter ringTrack uses --edge, distinct from --field and --surface-1 in both themes", () => {
  expect(METER_STYLESHEET).toBeDefined();
  if (!METER_STYLESHEET) return;
  expect(declarationInRule(METER_STYLESHEET, "ringTrack", "stroke")).toBe("var(--edge)");
  const themes = [
    { name: "dark", block: extractBlock(TOKENS_CSS, /(?:^|\n):root(?:\s*,\s*\[data-theme="dark"\])?\s*\{/) },
    { name: "light", block: extractBlock(TOKENS_CSS, /\[data-theme="light"\][^{]*\{/) },
  ];
  for (const theme of themes) {
    const edge = declaredToken(theme.block, "--edge");
    const field = declaredToken(theme.block, "--field");
    const surface1 = declaredToken(theme.block, "--surface-1");
    expect([edge, field, surface1], `${theme.name} theme declares ring track contrast tokens`).not.toContain(undefined);
    if (!edge || !field || !surface1) continue;
    const edgeRgb = parseHexColor(edge);
    expect(contrastRatio(edgeRgb, parseHexColor(field)), `${theme.name} ringTrack vs field`).toBeGreaterThanOrEqual(1.15);
    expect(contrastRatio(edgeRgb, parseHexColor(surface1)), `${theme.name} ringTrack vs surface-1`).toBeGreaterThanOrEqual(1.15);
  }
});
```

- [ ] **Step 3: Run the targeted tests to verify they fail**

Run:
```bash
cd cmd/evener-hub/frontend && npx vitest run src/widgets/meter/meter.test.tsx src/styles/token-contract.test.ts
```
Expected: FAIL because `Meter` ignores `variant` and `meter.module.css` defines no `ringTrack` / `ringFillEmpty` classes.

- [ ] **Step 4: Implement `variant="ring"` in `Meter`, update CSS, barrel export, gallery, and §3 doc**

1. Update `cmd/evener-hub/frontend/src/widgets/meter/meter.module.css`:
   - Keep `.track` with `background: var(--field)` unchanged.
   - Change `.fill` to read `background: var(--meter-tone);` and `.fillNeutral`/`.fillAttention`/`.fillAlive`/`.fillDanger` to set `--meter-tone: var(--accent | --attention | --alive | --danger);` so both bar and ring share one semantic-hue table.
   - Add `.ring` (`display: inline-flex; align-items: center; justify-content: center; width: 14px; height: 14px; flex: none;`), `.ringSvg` (`display: block;`), `.ringTrack` (`fill: none; stroke: var(--edge);`), `.ringFill` (`fill: none; stroke: var(--meter-tone); stroke-linecap: round;`), and `.ringFillEmpty` (`stroke-opacity: 0;`).
2. Update `cmd/evener-hub/frontend/src/widgets/meter/index.tsx`:
   - Export `MeterVariant = "bar" | "ring"` and add `variant?: MeterVariant` (default `"bar"`) to `MeterProps`.
   - Normalize `safeMax = Number.isFinite(max) && max > 0 ? max : 0`, `safeValue = Number.isFinite(value) ? value : 0`, `clamped = safeMax > 0 ? Math.min(safeMax, Math.max(0, safeValue)) : 0`, `percent = safeMax > 0 ? (clamped / safeMax) * 100 : 0`.
   - Emit `data-variant={variant}` on the `role="meter"` root in both branches.
   - In the `"ring"` branch, render `<svg className={BASE_CLASS.ringSvg} viewBox="0 0 16 16" width={14} height={14} aria-hidden="true" focusable="false">` with track `<circle cx={8} cy={8} r={5.7} strokeWidth={2.6} />` and fill `<circle data-testid="meter-fill" ... transform="rotate(-90 8 8)" />` using `RING_CIRCUMFERENCE = 2 * Math.PI * 5.7`, `strokeDasharray`, `strokeDashoffset`, and appending `BASE_CLASS.ringFillEmpty` when `percent <= 0`.
3. Re-export `MeterVariant` in `cmd/evener-hub/frontend/src/widgets/index.ts`.
4. Update `cmd/evener-hub/frontend/src/dev/gallery-sections/meter.tsx` and `meter.module.css` to render both `"bar"` and `"ring"` across all four tones plus an empty `0%` ring sample in both themes.
5. Update the `**Meter**` row in §3 of `docs/web-ui/design-system.md` to document `variant?: "bar"|"ring"`, ring sizing, `--edge` track contrast, and 0% empty-stroke behavior.

- [ ] **Step 5: Run tests, contracts, gallery test, and typecheck**

Run:
```bash
cd cmd/evener-hub/frontend && npx vitest run src/widgets/meter/meter.test.tsx src/styles/token-contract.test.ts src/styles/motion.contract.test.ts src/dev/WidgetGallery.test.tsx && npm run typecheck
```
Expected: All tests PASS and `tsc --noEmit` exits 0.

- [ ] **Step 6: Format touched files and commit Task 1**

Run:
```bash
cd cmd/evener-hub/frontend && npx biome check --write src/widgets/meter/index.tsx src/widgets/meter/meter.module.css src/widgets/meter/meter.test.tsx src/widgets/index.ts src/styles/token-contract.test.ts src/dev/gallery-sections/meter.tsx src/dev/gallery-sections/meter.module.css
```
Then commit those files plus `docs/web-ui/design-system.md` with message `feat(web): add ring variant to Meter widget` and the `Co-authored-by` trailer.

---

### Task 2: Move `StatusRow` context indicator inside `.identity` as `Meter variant="ring"`

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/StatusRow.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/statusrow.module.css`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`

**Interfaces:**
- Consumes: `Meter` with `variant="ring"` and `data-variant="ring"` from Task 1 (`../../../widgets`).
- Produces:
  - `StatusRow` renders `[data-testid="status-row-context"]` inside `[data-testid="status-row-identity"]`, immediately preceding `[data-testid="model-switch-trigger"]`, only when `status.ceiling > 0`.
  - No `status-row-context-meter` or `status-row-context-percent` spans are rendered.
  - `.identity > span:not([data-effort-control]):not(.context)` keeps `.context` at `flex: none`.

- [ ] **Step 1: Update `StatusRow.test.tsx` and `SessionChrome.test.tsx` to pin the new ring placement**

In `cmd/evener-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx`:
- Keep `"groups model and effort visually while retaining two independent controls"` verifying that without context data (`contextWindow: 0`), `identity.children` has length 2 (model and effort only, no empty context node).
- Update `"context announces through one exposed meter..."` to verify:
  - `identity.contains(context)` is `true` and `context` precedes `model-switch-trigger` in document order (`context.compareDocumentPosition(modelTrigger) & Node.DOCUMENT_POSITION_FOLLOWING`).
  - `context` is not inside `model-switch-trigger` (sibling, not inside the clickable model button).
  - `meter.getAttribute("data-variant")` is `"ring"`, `meter.closest('[aria-hidden="true"]')` is `null`, `screen.getAllByRole("meter")` has length 1, and `screen.queryByTestId("status-row-context-percent")` is `null`.
- Update the `compactionThreshold` test to check `aria-label` / `aria-valuenow` / `aria-valuemax` and `meterToneClass.attention` without querying the removed `status-row-context-percent`.

In `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`:
- Update the inline composer layout test (around lines 228-246) to assert `identity.contains(context)` is `true`, `context` precedes `model-switch-trigger` inside `identity`, and `identity` precedes `actions` (`Session actions`).

- [ ] **Step 2: Run the updated tests to confirm they fail against the current code**

Run:
```bash
cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/StatusRow.test.tsx src/panes/session/chrome/SessionChrome.test.tsx
```
Expected: FAIL because `status-row-context` is still outside `.identity` after effort and renders `variant="bar"` + `status-row-context-percent`.

- [ ] **Step 3: Update `StatusRow.tsx` and `statusrow.module.css`**

1. In `cmd/evener-hub/frontend/src/panes/session/chrome/StatusRow.tsx`:
   - Remove `contextMeter` and `contextPercent` from `CLASS` and update the top file comment to describe the compact ring inside `.identity`.
   - Move `{hasContext && (<span className={CLASS.context} data-testid="status-row-context" title={contextLabel}><Meter label={contextLabel} value={model.contextUsed} max={status.ceiling} tone={contextTone(status.pressure)} variant="ring" /></span>)}` inside `<span className={CLASS.identity} data-testid="status-row-identity">` right before `<ModelSwitch sessionRef={sessionRef} model={model} shrink />`.
2. In `cmd/evener-hub/frontend/src/panes/session/chrome/statusrow.module.css`:
   - Change `.identity > span:not([data-effort-control])` to `.identity > span:not([data-effort-control]):not(.context)` so `.context` stays `flex: none`.
   - Set `.context { display: inline-flex; align-items: center; justify-content: center; flex: none; padding-right: var(--space-1); }`.
   - Inside `@media (min-width: 900px) and (not (any-pointer: coarse))`, add `.context { padding-left: var(--space-1); padding-right: var(--space-half); }`.
   - Remove `.contextMeter`, `.contextPercent` (keeping `.queueCompact { display: none; }`), and remove the `.contextMeter` clip and `.contextPercent` rules inside `@container (max-width: 399px)`.

- [ ] **Step 4: Run session chrome, details, and composer unit tests**

Run:
```bash
cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/StatusRow.test.tsx src/panes/session/chrome/SessionChrome.test.tsx src/panes/session/chrome/DetailsPanel.test.tsx src/panes/session/chrome/statusFormat.test.ts src/panes/session/composer/Composer.controls.test.tsx
```
Expected: All tests PASS (`DetailsPanel.test.tsx` confirms DetailsPanel stays on horizontal `variant="bar"`).

- [ ] **Step 5: Format touched files and commit Task 2**

Run:
```bash
cd cmd/evener-hub/frontend && npx biome check --write src/panes/session/chrome/StatusRow.tsx src/panes/session/chrome/statusrow.module.css src/panes/session/chrome/StatusRow.test.tsx src/panes/session/chrome/SessionChrome.test.tsx
```
Commit with message `feat(web): show circular context meter inside composer model pill` and the `Co-authored-by` trailer.

---

### Task 3: Update layoutguards, run visual verification, and pass full web gates

**Files:**
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/compact-session-footer/harness.html`
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/compact-session-footer/assert.mjs`
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/compact-session-footer/case.json`
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/composer-tap-floor-phone/harness.html`
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/composer-tap-floor-phone/assert.mjs`

**Interfaces:**
- Consumes: production CSS from `widgets/meter/meter.module.css` (`.ring`, `.ringSvg`, `.ringTrack`, `.ringFill`, `.fillNeutral`) and `panes/session/chrome/statusrow.module.css` (`.identity`, `.context`).
- Produces: passing browser geometry assertions proving the ring stays 14×14px, unclipped, inside `.identity`, before `modelTrigger` at every width from 320px phone to 900px desktop (expanded and folded).

- [ ] **Step 1: Run existing layoutguards to observe the expected failure from Task 2's CSS/placement change**

Run:
```bash
cd cmd/evener-hub/frontend && node scripts/layoutguard/run.mjs compact-session-footer composer-tap-floor-phone
```
Expected: `compact-session-footer` FAILS on stale `.contextMeter`/`.contextPercent` and `effort → context` assertions.

- [ ] **Step 2: Update `compact-session-footer` and `composer-tap-floor-phone` harnesses, assertions, and mutation notes**

1. In `compact-session-footer/harness.html` and `composer-tap-floor-phone/harness.html`:
   - Move `<span class="context">` inside `<span class="identity">` immediately before the first `<span class="model">`, containing the exact production `Meter variant="ring"` markup (`<div class="ring" data-variant="ring" role="meter" aria-label="..." aria-valuenow="67000" aria-valuemin="0" aria-valuemax="100000"><svg class="ringSvg" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false"><circle class="ringTrack" cx="8" cy="8" r="5.7" stroke-width="2.6"></circle><circle class="ringFill fillNeutral" cx="8" cy="8" r="5.7" stroke-width="2.6" stroke-dasharray="35.814" stroke-dashoffset="11.819" transform="rotate(-90 8 8)"></circle></svg></div>`).
   - Update `compact-session-footer/harness.html`'s `window.measure` to read `contextSemantic` from `.context [role="meter"]` and drop the deleted `contextMeter`/`contextPercent` probes.
   - Update `composer-tap-floor-phone/harness.html`'s `window.measure` to also report `context: box(fixture.querySelector(".context [role=\"meter\"]"))`.
2. In `compact-session-footer/assert.mjs`:
   - Replace `expectBefore(m.effort, m.context, ...)` and `expectBefore(m.context, m.sessionActions, ...)` with:
     - `expectContained(m.context, m.identity, "context inside identity")`
     - `expectBefore(m.attachment, m.context, "attachment → context")`
     - `expectBefore(m.context, m.modelTrigger, "context → model")`
     - `expectBefore(m.modelTrigger, m.effort, "model → effort")`
     - `expectBefore(m.effort, m.sessionActions, "effort → session actions")`
   - Replace the 399/400px bar-to-percent clip check with an assertion that `context.meterBox` stays visible at full ring dimensions (`>= 13px` × `>= 13px`) at every width, including 320px and 399px, with `context.meterCount === 1` and `context.meterAriaHidden === false`.
3. In `compact-session-footer/case.json`:
   - Update the `mutation` declaration/expect notes for the ring placement and non-shrinking ring check (verify at least one real CSS mutation fails the guard, then restore).
4. In `composer-tap-floor-phone/assert.mjs`:
   - Add assertion that `fixture.context` stays visible (`width >= 13`, `height >= 13`) and contained inside `fixture.identity` at both 390px and 320px coarse viewports.

- [ ] **Step 3: Run layoutguards and verify a mutation catches regressions**

Run:
```bash
cd cmd/evener-hub/frontend && node scripts/layoutguard/run.mjs compact-session-footer composer-tap-floor-phone
```
Expected: Both layoutguard cases PASS. Verify one mutation (for example, removing `:not(.context)` in `statusrow.module.css` or reordering `.context`) causes a failure, then restore with `git restore`.

- [ ] **Step 4: Run `ds-audit`, `make test-web`, and `make test-web-browser` (one gate at a time from repo root)**

Run sequentially from the worktree root:
```bash
cd cmd/evener-hub/frontend && node scripts/ds-audit.mjs
```
Then from repo root:
```bash
make test-web
```
Then from repo root:
```bash
make test-web-browser
```
Expected: Zero new `ds-audit` violations, `make test-web` exits 0 (unit tests + token/motion/gallery contracts + typecheck + Biome), and `make test-web-browser` exits 0 (all layoutguards + overflow harness).

- [ ] **Step 5: Commit Task 3**

Commit the updated layoutguard files with message `test(web): update composer layoutguards for circular context ring` and the `Co-authored-by` trailer.
