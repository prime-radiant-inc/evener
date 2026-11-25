# Composer Circular Context Meter Design

## Intent and why

Replace the 64px horizontal context usage bar in the session composer's `StatusRow` with a compact 14px circular progress ring seated inside the grey `.identity` pill directly before the model name: `[ (◔) gemini-4-neon ⌄ · max ⌄ ]`. This reclaims ~50px on the composer's single-line control row, visually binds context occupancy to the active model, and keeps one compact representation at every width instead of swapping from a bar to percentage text on narrow viewports.

## Goals, non-goals, and anti-goals

- **Goals:**
  - Extend the shared `Meter` widget (`src/widgets/meter`) with a `variant?: "bar" | "ring"` prop (default `"bar"`).
  - Render `StatusRow`'s context gauge as `variant="ring"` inside `.identity` immediately before `ModelSwitch`, as a passive sibling keeping its own hover tooltip (`title={contextLabel}`) and single exposed `role="meter"`.
  - Keep the ring at all container widths and remove the `<400px` bar-to-percentage text swap.
  - Maintain clear track contrast inside the grey `--field` desktop pill and on the plain phone surface in both light and dark themes.
- **Non-goals:**
  - Changing `DetailsPanel` (Overview / About) or `taskCard`, which remain on the default horizontal `variant="bar"`.
  - Changing `Spawn` (New Session), which has no context occupancy and shares `ModelSwitchTrigger` unchanged.
  - Making the ring clickable or interactive; it remains a passive `role="meter"` readout with a hover tooltip.
  - Changing the native mobile app (`mobile-native`).
- **Anti-goals:**
  - An invisible empty track inside the `--field` pill (using `--field` for the ring track would vanish against `--field`).
  - A stray round-cap dot painted at 12 o'clock when usage is 0%.
  - Flex-shrinking or clipping the ring when the model label ellipsizes on narrow panes.
  - Duplicating semantic tone styles outside `src/widgets/meter` or adding a new pane-level exception to `SEMANTIC_PATH_EXCEPTIONS`.

## Constraints

- Design-system rules (`docs/developing-evener/design-system.md`): every widget API change ships with barrel exports in `src/widgets/index.ts`, a complete gallery section in `src/dev/gallery-sections/meter.tsx`, an updated §3 row in `docs/web-ui/design-system.md`, and tests driving the widget through its role and accessible name.
- Token contract (`src/styles/token-contract.test.ts`): no color literals outside `tokens.css`; `Meter` bar `.track` keeps `background: var(--field)`; semantic hues (`--attention`, `--alive`, `--danger`) remain inside allowlisted `widgets/meter/meter.module.css`.
- Motion contract (`src/styles/motion.contract.test.ts`): no raw transition durations or autonomous animations on the ring.
- Single exposed `role="meter"` in `StatusRow` with exact `aria-label`, `aria-valuenow`, `aria-valuemin="0"`, `aria-valuemax`, and shared `contextTone` ladder (`>= 0.95` danger, `>= 0.8` attention, else neutral).
- Copy is not a test oracle (`docs/developing-evener/testing.md`): assert roles, attributes, classes, and geometry rather than hardcoded copy phrases.

## Approaches

1. **Extend `Meter` with `variant?: "bar" | "ring"` and mount it as a sibling inside `.identity` (chosen):**
   - Keeps one shared meter widget, reuses `meter`'s existing semantic-hue allowlist and tone classes (`fillNeutral`/`fillAttention`/`fillAlive`/`fillDanger`), leaves `ModelSwitchTrigger` and `Spawn` untouched, and lets the ring carry its own hover tooltip without triggering model-button hover styling.
   - Trade-off: adds one documented prop to the locked `Meter` widget API (explicitly approved by Jesse on 2026-11-25).
2. **Bespoke SVG ring local to `StatusRow`:**
   - Rejected because it duplicates `Meter`'s clamping, ARIA attributes, and tone-to-color mapping and requires adding `statusrow.module.css` to `SEMANTIC_PATH_EXCEPTIONS` in `token-contract.test.ts`.
3. **Mount the ring inside `ModelSwitchTrigger`'s `<button>`:**
   - Rejected because hovering or clicking the ring would trigger model-picker hover and open behavior, fight with the button's own `title={label}`, and add session-context plumbing to a trigger shared with `Spawn`.

## Decided design details

- **`Meter` API (`src/widgets/meter/index.tsx`):**
  - Export `MeterVariant = "bar" | "ring"`.
  - Add optional `variant?: MeterVariant` to `MeterProps` (defaults to `"bar"`).
  - Expose `data-variant={variant}` on the `role="meter"` root element.
  - Ring geometry (Style 2, approved from interactive mockup): 14×14px SVG on `viewBox="0 0 16 16"`, `cx={8}`, `cy={8}`, `r={5.7}`, `strokeWidth={2.6}`, rotated `-90deg` around `(8, 8)` so fill sweeps clockwise from 12 o'clock with `stroke-linecap: round`.
  - Track stroke uses `var(--edge)` (contrasts cleanly against both `--field` in the desktop pill and `--surface-1` on phone in dark and light themes).
  - Fill arc shares `TONE_CLASS[tone]` (`fillNeutral` / `fillAttention` / `fillAlive` / `fillDanger`) via a local `--meter-tone` custom property in `meter.module.css` so both bar (`background`) and ring (`stroke`) read one tone mapping and keep `data-testid="meter-fill"` carrying the exact tone class.
  - At `percent <= 0`, apply a hidden-stroke class (`stroke-opacity: 0`) to the fill circle so 0% renders a clean empty track with no stray cap dot while keeping `data-testid="meter-fill"` in the DOM.
  - Guard non-positive `max` and non-finite `value` so `strokeDashoffset` is always finite.
- **`StatusRow` placement and layout (`src/panes/session/chrome/StatusRow.tsx`, `statusrow.module.css`):**
  - Move the `{hasContext && ...}` span inside `.identity` immediately before `<ModelSwitch sessionRef={sessionRef} model={model} shrink />`.
  - Remove the redundant `status-row-context-meter` wrapper, `status-row-context-percent` span, and the `<400px` CSS clip/percent-swap rules. The ring stays visible at every width.
  - Update `.identity`'s shrink selector to `.identity > span:not([data-effort-control]):not(.context)` so `.context` stays `flex: none` and never shrinks or clips.
  - Spacing: `.context` gets `padding-right: var(--space-1)` on plain/phone layouts and `padding-left: var(--space-1); padding-right: var(--space-half)` inside the desktop `--field` pill media query (`@media (min-width: 900px) and (not (any-pointer: coarse))`).
- **Layout guards and tests:**
  - Update `compact-session-footer` and `composer-tap-floor-phone` layoutguard harnesses and assertions for the ring inside `.identity` ahead of the model trigger, visible at every width without the `<400px` percentage fallback.
  - Update `StatusRow.test.tsx`, `SessionChrome.test.tsx`, `meter.test.tsx`, `token-contract.test.ts`, `gallery-sections/meter.tsx`, and `docs/web-ui/design-system.md`.

## Left to the builder

- Exact decimal precision formatting for SVG `strokeDasharray` / `strokeDashoffset` strings.
- Layout of the `ring` rows in `src/dev/gallery-sections/meter.tsx` (for example, showing bar and ring side by side per tone plus a 0% empty ring sample).
- Verification through unit tests, token/motion/gallery contracts, typecheck, Biome, `ds-audit`, and `make test-web-browser` (layoutguards + overflow harness).
