# Sidebar one ground + soft shadow — design

Status: approved by Jesse 2026-11-28 ("Proposal B", after the mockup review at
https://artifacts.trout-rigel.ts.net/p/evener-sidebar-mockups/). Branch `sidebar-one-ground`.

## Intent

The sidebars and the main body use two different ground colors on web: the
rail and the Overview sidebar are `--surface-canvas` over a `--surface-0`
page. In light theme canvas is darker than page; in dark it is lighter, so
the rail sinks in one theme and floats in the other. Sidebars and main body
share one ground; the split is carried by the 1px `--edge` hairline both
platforms already draw, plus a soft one-sided shadow so the sidebar reads as
a layer on top of the page.

(Correction recorded during implementation: the first draft of this section
claimed the iPad split's detail pane was `palette.canvas`, citing
`DuoSplit.tsx:149`. That line is the grouped *sheet* ground; the
conversation's content comes from `rootStack`'s `palette.page` contentStyle,
and pixel-sampling the screenshot Jesse sent confirmed the split was already
one ground. The approved outcome is unchanged; the native behavior below is
corrected to match.)

## Approaches

- **Same ground + hairline only.** Flattest; Jesse judged it too quiet when
  choosing between the mockups.
- **Same ground + hairline + soft shadow (chosen).** The "little bit more
  visual weight" he asked for. Uses existing token vocabulary; no palette
  changes.
- **Replace the visual world (warm glow-up palette).** Larger than the
  finding, discards the cool-neutral ruling, and the 2026-11-05 spec it
  comes from never merged.

## Goals

- Web: rail and Overview sidebar share `--surface-0` with the page; each
  keeps its hairline and gains a soft shadow cast onto the page.
- iPad: the split stays one ground (it already was); the sidebar gains the
  soft shadow and the `zIndex` that keeps the cast visible, while the 1px
  `palette.edge` divider stays.
- Design law, decisions record and native design record updated in the same
  change (evergreen docs rule).

## Non-goals

- No palette token changes. `--surface-canvas`/`palette.canvas` keep their
  real jobs: dock tab strip and inactive tabs, status bar pills,
  SegmentedControl track, mermaid ground, zoom and settings preview wells,
  and the iOS grouped-sheet ground.
- The dock tab strip stays canvas (the pane well). Builder's call, flagged
  on the mockup page and not overridden.
- iPhone: untouched (Board and Session are already flat `palette.page`).
- iPad grouped sheets: untouched (they keep `palette.canvas`).
- Android: deferred as before.

## Anti-goals

- No decorative elevation on ordinary rows (the Flat Board Rule stands).
- No warm or beige ground: the cool-neutral ruling stands.
- No guard weakened: a new layoutguard case pins the new contract.

## Behavior

**Web.** `Rail.module.css` `.rail` and `activitybar.module.css`
`.sidebar`/`.sidebarMobile` backgrounds go `--surface-canvas` →
`--surface-0`. Two new tokens, declared in both theme blocks with literals
from the `--shadow-raised` alpha family: `--shadow-rail: 8px 0 24px -12px
#00000066` (dark) / `#1A1D230F` (light), and `--shadow-overview` the
left-pointing mirror (`-8px 0 24px -12px ...`). Both sidebars take
`z-index: var(--z-raised)` so the following sibling's background does not
paint over the cast shadow. At ≤899px the rail is drawer-hosted and keeps
no border and no shadow (the Sheet frames it), and `.sidebarMobile`
likewise drops border and shadow. Light-theme shadows stay ink-tinted and
much fainter than dark's, per the shadow tokens' own rule.

**iPad.** The split was already one ground (see the correction above):
`DuoSplit.tsx` seats the Board sidebar and the conversation on
`palette.page`, divided by a 1px `palette.edge` hairline. What it lacked is
the layering: the sidebar container gains `FloatingStack`'s shadow recipe
turned sideways — `shadowColor: "#000"`, `shadowOpacity: 0.12`,
`shadowRadius: 8`, `shadowOffset: {width: 2, height: 0}`, `elevation: 3` —
plus `zIndex: 1` so the detail pane does not paint over the cast. The
surface values are computed by a pure exported helper,
`duoSurfaceStyles(palette)`, beside the component, in the `FloatingStack`
exported-helper pattern. The grouped sheets keep their `palette.canvas`
ground (the iOS grouped-list convention).

## Recovery and preservation

Pure styling: no preferences, wire fields, or persistence change; the
sidebar width prefs are untouched. Reverting the commit restores the old
grounds exactly. Reduced-motion behavior is unaffected (nothing animates).

## Test plan

1. New layoutguard case `sidebar-one-ground`, modeled on
   `zscn-theme-flip-dark-surface`: computed rail and Overview backgrounds
   equal `--surface-0` in both themes, the hairlines are 1px `var(--edge)`,
   desktop `box-shadow` is non-`none` with `z-index` applied, and at ≤899px
   the rail's `box-shadow` is `none`. Fails before the CSS change, passes
   after.
2. `DuoSplit.test.tsx` gains a `duoSurfaceStyles` contract test: root and
   sidebar grounds are `palette.page` in both schemes, the divider stays 1px
   `palette.edge`, and the sidebar shadow matches the spec values with
   `zIndex: 1`. Fails before, passes after.
3. Gates: `make test-web` (token contract, typecheck, Biome, unit),
   `npm run layoutguard`, targeted native Vitest for the duo suite and
   `tsc --noEmit -p tsconfig.check.json`. CI owns the full suites and
   `make test-web-browser`.
4. Visual: the running hub in both themes; the iPad split in both schemes
   if a simulator is available, screenshots otherwise from the mockup page.

## What the builder may decide

Exact harness mechanics of the layoutguard case (copy the
`zscn-theme-flip-dark-surface` case's registration), the layoutguard
single-case invocation, and whether `npm run layoutguard` or
`make test-web-browser` runs the new case locally.
