# Sidebar One Ground + Soft Shadow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the rail, Overview sidebar and iPad detail pane share the page ground, with the split carried by the existing hairline plus a new soft one-sided shadow.

**Architecture:** Call-site token swaps only — no palette changes. Web moves two stylesheets from `--surface-canvas` to `--surface-0` and gains two per-theme shadow tokens; iPad moves one `contentStyle` color and gains the `FloatingStack` shadow recipe via a pure exported helper.

**Tech Stack:** CSS modules + design tokens (web), React Native style objects (iPad), Vitest, layoutguard.

**Spec:** [docs/superpowers/specs/2026-11-28-sidebar-one-ground-design.md](../specs/2026-11-28-sidebar-one-ground-design.md)

## Global Constraints

- Tokens only in CSS: every color/shadow/z-index is `var(--name)`; new shadow tokens are declared in BOTH theme blocks of `tokens.css` (the file's own rule for color-carrying shadows) with literals dark `#00000066` / light `#1A1D230F` (the `--shadow-raised` second-component alphas).
- No palette (`appwire-client/typescript/palette.ts`) changes; `--surface-canvas` stays for wells (tab strip, status pills, SegmentedControl track, mermaid, zoom/settings preview wells, grouped sheets).
- Mobile ≤899px: the drawer-hosted rail keeps no border and no shadow; `.sidebarMobile` likewise.
- Tests assert behavior/computed style, never copy strings.
- Every commit ends with the session's `Co-authored-by` trailer.
- Docs evergreen in the same change: `docs/web-ui/design-system.md`, `docs/web-ui/decisions.md`, `mobile-native/DESIGN.md`, and the `--surface-canvas` role comment in `tokens.css`.

## Review Focus

1. **Sibling paint order eats the shadow** — the workspace host paints after the rail, so without `z-index` the cast shadow is invisible; both sidebars get `z-index: var(--z-raised)` and the layoutguard case asserts the computed `z-index` (Task 2).
2. **Light-theme shadow weight** — paper shows shadows a dark ground swallows; the light literal is `#1A1D230F`, never the dark `#00000066` (Task 2's token step pins both literally).
3. **Drawer double-framing** — at ≤899px the rail renders inside a Sheet that already frames it; the case asserts computed `box-shadow: none` at mobile width (Task 2).
4. **Mirror direction** — the Overview sidebar sits on the right, so its shadow points left (`-8px`); the case asserts each sidebar's computed `box-shadow` is non-`none` (direction verified visually) (Task 2).
5. **Native sibling paint order** — the detail pane paints after the sidebar, so the sidebar container gets `zIndex: 1`; asserted by the `duoSurfaceStyles` test (Task 3).

---

### Task 1: Spec and plan committed

**Files:**
- Create: `docs/superpowers/specs/2026-11-28-sidebar-one-ground-design.md`
- Create: `docs/superpowers/plans/2026-11-28-sidebar-one-ground.md`

- [ ] **Step 1: Commit both documents**

```bash
git add docs/superpowers/specs/2026-11-28-sidebar-one-ground-design.md docs/superpowers/plans/2026-11-28-sidebar-one-ground.md
git commit -m "docs: sidebar one-ground + soft-shadow design and plan"
```

### Task 2: Web sidebars share the page ground and cast the soft shadow

**Files:**
- Modify: `cmd/evener-hub/frontend/src/styles/tokens.css` (dark block after `--shadow-inset-field` ~line 260; light block ~line 387; `--surface-canvas` role comment line 34)
- Modify: `cmd/evener-hub/frontend/src/shell/rail/Rail.module.css:33` (and the ≤899px block ~line 43)
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css:9,23`
- Test: `cmd/evener-hub/frontend/scripts/layoutguard/cases/sidebar-one-ground/` (new case, mechanics copied from `zscn-theme-flip-dark-surface`)

**Interfaces:**
- Consumes: existing `--surface-0`, `--edge`, `--z-raised` tokens; the layoutguard case registration used by `zscn-theme-flip-dark-surface`.
- Produces: `--shadow-rail` and `--shadow-overview` tokens (both theme blocks); no exported code symbols.

- [ ] **Step 1: Write the failing layoutguard case**

New case `sidebar-one-ground` that loads the real `tokens.css`, `Rail.module.css` and `activitybar.module.css` the way `zscn-theme-flip-dark-surface` loads its stylesheets, renders a `.rail` element and a `.sidebar` element, and asserts in both themes:

- computed rail background equals computed `var(--surface-0)`
- computed Overview sidebar background equals computed `var(--surface-0)`
- computed rail `border-right` is `1px solid var(--edge)` and Overview `border-left` likewise
- desktop: computed `box-shadow` is not `none` on both, and computed `z-index` equals the `--z-raised` value
- at ≤899px width: computed rail `box-shadow` is `none`

- [ ] **Step 2: Run the case and verify it fails**

Run: `npm run layoutguard` from `cmd/evener-hub/frontend` (single-case invocation per the case runner's `--case` flag or equivalent)
Expected: FAIL — computed rail/sidebar backgrounds resolve to `#16181C`/`#EEF0F3` (canvas), not the page values.

- [ ] **Step 3: Add the shadow tokens and swap the backgrounds**

In `tokens.css`, after `--shadow-inset-field` in each theme block:

```css
--shadow-rail: 8px 0 24px -12px #00000066;      /* dark block */
--shadow-overview: -8px 0 24px -12px #00000066; /* dark block */
--shadow-rail: 8px 0 24px -12px #1A1D230F;      /* light block */
--shadow-overview: -8px 0 24px -12px #1A1D230F; /* light block */
```

Also update the `--surface-canvas` comment (line 34) from "pane wells, rail — the tinted structural ground" to name its remaining roles (pane wells, tab strip, status pills — the tinted structural ground).

In `Rail.module.css`: `.rail` background → `var(--surface-0)`; add `box-shadow: var(--shadow-rail); z-index: var(--z-raised);` (the `.rail` rule is already `position: relative`); in the ≤899px block add `box-shadow: none;` beside the existing `border-right: none`.

In `activitybar.module.css`: `.sidebar` and `.sidebarMobile` backgrounds → `var(--surface-0)`; `.sidebar` gains `box-shadow: var(--shadow-overview); z-index: var(--z-raised); position: relative;`; `.sidebarMobile` gains `box-shadow: none;` beside its `border-left: none`.

- [ ] **Step 4: Run the case and verify it passes**

Run: `npm run layoutguard` from `cmd/evener-hub/frontend`
Expected: PASS for `sidebar-one-ground`, no regressions in other cases.

- [ ] **Step 5: Run the web gate**

Run: `make test-web`
Expected: exit 0 (token contract, typecheck, Biome, unit).

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/frontend/src/styles/tokens.css cmd/evener-hub/frontend/src/shell/rail/Rail.module.css cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css cmd/evener-hub/frontend/scripts/layoutguard/cases/sidebar-one-ground
git commit -m "feat(web): rail and Overview share the page ground and cast a soft shadow"
```

### Task 3: iPad sidebar casts the shadow on the already-unified ground

> Corrected after implementation (the original text wrongly directed a
> `DetailStack.Group screenOptions.contentStyle` change to `palette.page`):
> the split was already one ground — the conversation inherits
> `palette.page` from `useRootStackOptions`, and the `palette.canvas` at
> `DuoSplit.tsx`'s `DetailStack.Group` is the grouped *sheet* routes'
> ground, which stays canvas per the Grouped List Rule. What shipped is the
> sidebar's shadow and the `zIndex` that keeps the cast visible, plus the
> exported helper.

**Files:**
- Modify: `mobile-native/src/duo/DuoSplit.tsx` (~lines 94-150; extract the surface styles into an exported pure helper beside the component, the `FloatingStack` pattern)
- Test: `mobile-native/src/duo/DuoSplit.test.tsx`

**Interfaces:**
- Consumes: `palette` from the design tokens; the test file's existing palette fixtures/harness.
- Produces: `duoSurfaceStyles(palette)` returning `{ root, sidebar, divider }` style objects — `root.backgroundColor: palette.page`, `sidebar.backgroundColor: palette.page` carrying the spec shadow + `zIndex: 1`, `divider: { width: 1, backgroundColor: palette.edge }`. `DuoSplit.tsx` consumes it; no other caller.

- [ ] **Step 1: Write the failing contract test**

In `DuoSplit.test.tsx`, add a test that calls `duoSurfaceStyles` with each scheme's palette and asserts: `root.backgroundColor === palette.page`; `sidebar.backgroundColor === palette.page`; `divider` is `{ width: 1, backgroundColor: palette.edge }`; `sidebar` carries `shadowColor: "#000"`, `shadowOpacity: 0.12`, `shadowRadius: 8`, `shadowOffset: { width: 2, height: 0 }`, `elevation: 3`, `zIndex: 1`. Match the file's existing style for obtaining palettes (its current fixtures). Add a wiring test that mounts the split and asserts the rendered Board wrapper View carries the shadow style (catches the helper being dropped from the render).

- [ ] **Step 2: Run the test and verify it fails**

Run: `cd mobile-native && npx vitest run src/duo/DuoSplit.test.tsx`
Expected: FAIL — `duoSurfaceStyles` is not a function.

- [ ] **Step 3: Implement `duoSurfaceStyles` and rewire `DuoSplit`**

Export `duoSurfaceStyles(palette)` from `DuoSplit.tsx` and use it for the root `View`, the sidebar container `View` (the spec shadow plus `zIndex: 1`), and the divider `View` (unchanged 1px `palette.edge`). The `DetailStack.Group` `screenOptions.contentStyle` stays `palette.canvas` (grouped sheets).

- [ ] **Step 4: Run the test and verify it passes**

Run: `cd mobile-native && npx vitest run src/duo/DuoSplit.test.tsx`
Expected: PASS, including the pre-existing cases. Mutation-prove the wiring test: drop the sidebar style from the rendered wrapper, watch it fail, restore.

- [ ] **Step 5: Typecheck and Biome the touched files**

Run: `cd mobile-native && npm run check && npx biome check --write src/duo/DuoSplit.tsx src/duo/DuoSplit.test.tsx`
Expected: tsc clean; Biome reports no changes.

- [ ] **Step 6: Commit**

```bash
git add mobile-native/src/duo/DuoSplit.tsx mobile-native/src/duo/DuoSplit.test.tsx
git commit -m "feat(native): iPad split sidebar casts the one-ground shadow"
```

### Task 4: Evergreen docs in the same change

**Files:**
- Modify: `docs/web-ui/design-system.md` (§1 law line ~375-377 "rail one visible step from the page"; §2 token table row ~522 "Wells/rail"; §2 paragraph ~536 "two from the rail's canvas"; §2 ruling ~563-566 "The rail stays one visible step from the page")
- Modify: `docs/web-ui/decisions.md` (new dated entry after the header, newest first)
- Modify: `mobile-native/DESIGN.md` (the DuoSplit/detail-pane ground description — find its canvas mention)

**Interfaces:**
- Consumes: the shipped behavior from Tasks 2-3.

- [ ] **Step 1: Update the design law**

Rewrite the four spots so the law reads: the rail and the Overview sidebar share the page's `--surface-0`; the split is the 1px `--edge` hairline plus the `--shadow-rail`/`--shadow-overview` soft one-sided shadows; `--surface-canvas` narrows to wells (dock tab strip and inactive tabs, status bar pills, SegmentedControl track, mermaid ground, preview wells). The token table row becomes "Wells `--surface-canvas`" and the shadow pair is named in the elevation vocabulary.

- [ ] **Step 2: Add the decisions.md entry**

New `## 2026-11-28 sidebars share the page ground and carry the layering` entry: what shipped, the inversion it fixed (web canvas/page vs iPad page/canvas), the alternatives considered (hairline-only, warm palette), and that the tab strip stays canvas as the pane well.

- [ ] **Step 3: Update mobile-native/DESIGN.md**

Update the DuoSplit/split description: both panes share `palette.page`, the divider stays 1px `palette.edge`, and the sidebar casts the `FloatingStack` shadow recipe sideways; grouped sheets keep `palette.canvas`.

- [ ] **Step 4: Run the docs check**

Run: `make docs-check`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add docs/web-ui/design-system.md docs/web-ui/decisions.md mobile-native/DESIGN.md
git commit -m "docs: record the sidebar one-ground ruling in the design law and decisions"
```

### Task 5: Simplify, push, shepherd

- [ ] **Step 1: Run the simplify-code skill on the branch diff**
- [ ] **Step 2: Push and open the PR** — body links the spec, the mockup page (https://artifacts.trout-rigel.ts.net/p/evener-sidebar-mockups/), and notes the builder calls (tab strip stays canvas; `--z-raised` chosen below dialog/menu layers). Note `par` is unavailable on this host.
- [ ] **Step 3: Shepherd** — use the shepherd-pr skill: one push, one CI run, one RoboRev round at a time; merge through the merge queue.

## Self-review notes

- Spec coverage: same ground (T2/T3), hairline kept and asserted (T2/T3), soft shadow both platforms (T2/T3), mobile exemption (T2), docs (T4), no palette change (Global Constraints).
- Every Review Focus line has its test named in Task 2 or 3.
- Types: `duoSurfaceStyles(palette) -> { root, sidebar, divider, detail }` is the only new signature; Task 3's test and implementation use it identically.
