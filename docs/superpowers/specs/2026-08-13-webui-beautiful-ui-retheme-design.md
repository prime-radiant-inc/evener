# WebUI re-theme: full adoption of the Beautiful UI design language

Status: proposed
Decision: Jesse, 2026-08-13 — "move toward their aesthetic", full adoption
(palette, fonts, motion, chrome), superseding the 2026-07-31 Fjord/Ledger
re-theme. Structure (token contract, attention semantics, spacing grid,
honest liveness) is retained; the visual language is replaced.

## Source and attribution

Design language adapted from **Beautiful UI** (https://www.beautifului.dev),
MIT License, Copyright (c) 2026 Shane Levine. The license text ships at
`cmd/evener-hub/frontend/LICENSES/beautiful-ui.txt`; `tokens.css` and any
widget whose structure is ported from a Beautiful UI component carry a
one-line attribution comment. Their components are React + Tailwind; nothing
is copy-pasted — every value and structure is translated into evener's
CSS-module + token system, which is why the token contract continues to
hold.

## What does NOT change

- The token-contract enforcement machinery (no literals outside tokens.css,
  attention-family allowlist, z-ladder, focus-ring rules, dark/light parity).
- The attention-semantics thesis: one meaning per hue — `--attention` (human
  needed), `--alive` (agent working), `--danger` (failure), `--accent`
  (interaction chrome). Beautiful UI's orange/green/red/blue map 1:1.
- The 4px spacing grid (`--space-1..9`) and the type ramp's size steps
  (12/13/14/16/20 × `--font-scale`).
- Cadence's honest liveness: no idle motion, ever. The motion budget widens
  for input-response transitions (below) but idle pulses/shimmer stay banned.
- Widget inventory, APIs, and the one-dir-per-widget convention.
- The AA guarantee: any hue used as text clears 4.5:1 on the surfaces it
  sits on, in both themes.

## 1. Palette

Neutral grays replace Fjord (cool blue) and Ledger (warm paper). Dark stays
the default.

| Token | Dark | Light | Notes |
|---|---|---|---|
| `--surface-0` | `#17181A` | `#FAFAFB` | page |
| `--surface-canvas` | `#1C1D1F` | `#F1F2F3` | NEW — pane wells, rail |
| `--surface-1` | `#232427` | `#FFFFFF` | cards/panes; now *lighter* than its surroundings in light theme — cards pop instead of blending |
| `--surface-inset` | `#1F2022` | `#F7F8F9` | NEW — card header bands, code gutters |
| `--hover-1` | `#2A2B2E` | `#F4F5F6` | NEW — resting hover wash (replaces reusing `--surface-2`) |
| `--hover-2` | `#313236` | `#E7E9EB` | NEW — pressed/selected |
| `--field` | `#2B2C2F` | `#F2F2F3` | NEW — form control background (sunken fields) |
| `--edge` | `#2E3033` | `#ECEDEF` | hairline |
| `--edge-strong` | `#3A3C40` | `#E0E2E5` | NEW — control borders, overlay rings |
| `--ink-hi` | `#F2F3F4` | `#1F2124` | |
| `--ink-mid` | `#A5A8AD` | `#62656B` | |
| `--ink-low` | `#6C6F75` | `#9A9DA3` | |
| `--accent` | `#3D9AFF` | `#0285FF` | |
| `--alive` | `#3DBB72` | `#189A4D` | |
| `--attention` | `#F68F3C` | `#EF720C` | |
| `--danger` | `#EE5C61` | `#E3474C` | |

`--surface-2` (raised: menus, dialogs) keeps its name; dark `#232427` with
elevation now carried by shadow, light `#FFFFFF`. During implementation
`--surface-2`-as-hover call sites migrate to `--hover-1`; `--surface-2`
remains only on genuinely raised layers.

**Hue companions.** The computed `-bg` (15% mix) and `-edge` (40% mix)
companions survive unchanged — Beautiful UI's own tints are ≈15% alpha
washes, so the mechanism is already theirs. NEW: a `-ink` companion per hue
for *text* usage (`--accent-ink`, `--alive-ink`, `--attention-ink`,
`--danger-ink`). Rationale: Beautiful UI's bare light hues measure
2.8–3.9:1 on white — fine for glyphs and borders, failing AA for text. Their
own fix is `--accent-ink` (`#0170DD`, 4.6:1); we generalize it. Light `-ink`
values are darkened to ≥4.5:1 on both light surfaces (computed at
implementation, contract-tested like the diff pair); dark `-ink` values are
the brightened forms (accent `#7EC0FF`, others computed). Existing call
sites using a bare hue as text color migrate to `-ink`.

**Diff + ANSI.** `--diff-add-bg`/`--diff-del-bg` are re-derived against the
new surfaces to keep passing the quiet-contrast contract (1.05–1.2× vs
`--surface-0`, AA for content). The 16 ANSI colors are re-tuned to the new
neutral palette (dark: keep current values as a starting point, nudge grays
to the neutral axis; light: recompute the four semantic-adjacent ones for
AA).

**Tooltip.** Inverted mini-palette, per Beautiful UI: `--tooltip-bg`
(near-black in both themes), `--tooltip-fg`, `--tooltip-muted`,
`--tooltip-border`. The tooltip stops being a `--surface-2` bubble.

## 2. Type

Inter (variable) replaces IBM Plex Sans; JetBrains Mono (variable) replaces
IBM Plex Mono. Both OFL, self-hosted from npm (`@fontsource-variable/inter`,
`@fontsource-variable/jetbrains-mono`), latin subset, same `@font-face`
wiring in `global.css`.

- Ramp sizes and the `--font-scale` mechanism unchanged.
- `--tracking-display` (-0.01em) extends to -0.02em on titles (theirs).
- NEW `--tracking-micro: 0.08em` + the **micro-label pattern**: card/pane
  section headers become 11.5–12px uppercase, `--font-weight-medium`,
  `--tracking-micro`, `--ink-low`/`--ink-mid`, usually on a `--surface-inset`
  band. Applied via widget chrome (PaneScaffold, Card, Dialog headers), not
  per-page.

## 3. Elevation

Beautiful UI's five-shadow system replaces the two-token system shipped
2026-08-13 (same token file section, new contents). Every shadow embeds its
own 1px ring, so borders and shadows stop being separate decisions:

```
--shadow-hairline:    0 0 0 1px var(--edge);
--shadow-btn:         0 0 0 1px var(--edge-strong), 0 1px 2px <soft>;
--shadow-card:        0 0 0 1px var(--edge), 0 1px 2px <soft>, 0 2px 6px <soft>;
--shadow-raised:      0 0 0 1px var(--edge), 0 2px 10px <soft>;
--shadow-overlay:     0 0 0 1px var(--edge-strong), 0 8px 28px <soft>;
--shadow-inset-field: inset 0 1px 2px <soft>;
```

`<soft>` alphas are per-theme (dark heavier, light whisper-light), declared
as literal color values in tokens.css per the contract. Mapping: buttons →
`btn`; cards/panes → `card` (border-only where density demands); menus /
popovers / toasts / dialogs / sheets → `overlay` (sheets keep their
edge-directed geometry); inputs/selects/textareas → `inset-field` on
`--field`. `--shadow-modal` retires. The tooltip uses `hairline` with its
inverted palette.

## 4. Shape

`--radius-chip: 6px` (NEW), `--radius-control: 4px → 8px`,
`--radius-pane: 8px → 10px`, `--radius-pill` unchanged. Chips/badges take
`--radius-chip`; everything already on the two existing tokens inherits the
softer values for free.

## 5. Motion

The design law widens from "default none" to **"no idle motion"**:

- NEW `--motion-duration-hover: 150ms` — color/background/border/shadow
  transitions on hover/focus/press for interactive chrome.
- Idle animation remains banned (no shimmer, no pulses; Cadence unchanged).
- Existing attention-onset (200ms) and overlay (120ms) budgets unchanged.
- Blanket `transition: all` is banned; transitions name their properties.

## 6. Widget chrome pass

Applied through widget stylesheets so every pane inherits it:

- **Button/IconButton**: `--shadow-btn` resting, `--hover-1` hover,
  `--hover-2` active, 150ms transitions. Quiet variants: transparent resting,
  `--hover-1` hover (no shadow).
- **Input/Textarea/Select/PathField**: `--field` background,
  `--shadow-inset-field`, `--edge-strong` border on focus-within alongside
  the standard focus ring.
- **Card/PaneScaffold/Dialog/Sheet**: `--shadow-card`/`--shadow-overlay`,
  `--radius-pane`, optional `--surface-inset` header band + micro-label.
- **Menu/Popover/CommandPalette**: `--shadow-overlay`, `--hover-1` item
  hover, `--radius-control` items inside `--radius-pane` panels.
- **Chip/Badge**: `--radius-chip`, tint backgrounds (`-bg`), `-ink` text.
- **Tooltip**: inverted palette, `--radius-control`, no shadow beyond
  hairline.
- **Toast**: `--shadow-overlay`, `--surface-1`.
- **Tree/Rail/rows**: `--hover-1` hover, `--hover-2` selection.

## 7. Contract-test updates (ride phase 1)

- The two hardcoded palette assertions (`#171E28` scoping probe) re-anchor
  to new values.
- Diff-contrast and dark/light-parity tests: unchanged logic, new inputs
  must pass.
- NEW: an AA check for the four `-ink` text companions against both light
  surfaces, mirroring the diff-contrast test's computation.
- Hover tokens join the theme-parity check automatically (color values).
- The focus-ring/z-ladder checks are untouched.

## 8. Phasing

1. **Tokens + fonts** — new palette, type, radii, shadows, motion tokens;
   contract tests updated; the whole app flips at once. Gallery + ThemeFlip
   screenshots reviewed by Jesse before proceeding.
2. **Widget chrome** — §6, widget by widget, gallery-verified. Fan-out
   friendly (per-widget, disjoint files).
3. **Pane polish + ports** — `--surface-2`→`--hover-1` migration at hover
   sites, micro-label adoption in pane headers, then the earlier-agreed
   component ports (Thinking trace tabs, Selection Actions, Table) in the
   new language.

## Risks

- **Readability regression risk** at phase 1: neutral grays have less
  separation than Fjord's blue-cast steps; the gallery review gate exists to
  catch this before widget work builds on it.
- **Light-theme card-pops-white** inverts Ledger's surface order
  (surface-1 was darker than page); any pane that assumed the old ordering
  for contrast gets caught by the existing guards (overflowguard/layoutguard)
  and the gallery review.
- **Font metrics**: Inter runs slightly wider than Plex at the same size;
  truncation-sensitive spots (Rail rows, chips, StatusRow) need a look
  during phase 2.
