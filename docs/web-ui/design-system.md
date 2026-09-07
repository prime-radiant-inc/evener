# Evener Web Hub — Design System & Style Guide (v2)

Status: **current**. This is the wave-2 rewrite's design system: tokens, fonts, and a widget
library under `cmd/evener-hub/frontend/src/widgets/`, built as React function components + CSS
Modules, with a living gallery (`/dev/widgets`, dev builds only) showing every widget in every
documented state, in both themes.

**This supersedes the pre-wave-2 version of this document** (the `renderer.js`/`style.css`-era
transcript UI — audience/principles/component-grammar/sidebar/mobile-forms sections describing
the old server-rendered hub). That content isn't reproduced here; it's in git history
(`git log -- docs/web-ui/design-system.md`) if it's ever needed for migration reference. The
wave-2 widget library is a from-scratch visual system, not a reskin of the old one, so carrying
old rules forward inline would misrepresent what's actually enforced today.

Source of design law: `docs/superpowers/plans/2026-07-20-webui-rewrite-wave2-design-system.md`,
§Direction. §1 below reproduces it verbatim; everything after is derived from it or documents
what the implementation actually shipped.

**Visual language provenance.** As of the 2026-08-13 re-theme, the palette, type, shape,
elevation, and motion described below (§2 onward) are adapted from
[Beautiful UI](https://www.beautifului.dev), MIT License, Copyright (c) 2026 Shane Levine — full
license text at `cmd/evener-hub/frontend/LICENSES/beautiful-ui.txt`. Beautiful UI ships as React +
Tailwind components; nothing is copy-pasted from it — every value and structure is translated
into evener's own CSS-module + token system, which is why the token-contract machinery in §4
continues to hold unchanged. See
`docs/superpowers/specs/2026-08-13-webui-beautiful-ui-retheme-design.md` and this document's
sibling `decisions.md` (2026-08-13 entry) for what was kept, what changed, and why.

---

## 1. Direction (the design law)

> Reproduced verbatim from the wave-2 plan — largely superseded by now. The palette
> was first replaced by the 2026-07-31 Fjord/Ledger re-theme, then palette, type,
> shape, elevation, and motion were all replaced by the 2026-08-13 full adoption of
> the Beautiful UI design language (see `decisions.md`'s entries for both dates). §2
> below documents shipped reality; this section is kept as the original historical
> record of the plan as written. For anything not covered by those two re-themes, if
> this section and the plan ever disagree, the plan is source of truth and this
> section is stale — file it as a doc bug.

**Palette:** superseded twice since this plan was written — first by the 2026-07-31
Fjord/Ledger re-theme, then in full by the 2026-08-13 adoption of the Beautiful UI
design language. The wave-2 hex table originally reproduced here is dropped rather
than perpetuated as a third stale table; see §2 below for the palette as shipped,
and `decisions.md` for both re-themes' provenance and rationale.

**Type:** the plan's IBM Plex Sans / IBM Plex Mono pairing and its 12 caption / 13 ui /
14 body / 16 pane-title / 20 page-title scale at 1.5/1.3 are both superseded: first by the
2026-08-13 Beautiful UI adoption (Inter Variable + JetBrains Mono Variable), then by the
2026-09-06 typography pass. The ramp as shipped is 12 caption / 13 ui / 15 body (16 on phones) /
18 pane-title / 22 page-title / 28 display, line-heights 1.6 body, 1.4 ui, 1.25 titles; see §2
and `decisions.md`'s 2026-09-06 entry. Mono never used for chrome labels (retired pattern stays
retired) is the one rule in this paragraph that survived both re-themes intact.

**Space/shape:** 4px grid (`--space-1..9` = 4..64); radius 4 (controls) / 8 (panes, dialogs) / 999px pill (switch track);
depth = `--edge` borders + surface steps; shadows only on floating layers (menu, popover, toast: `--shadow-overlay`; dialog/sheet: `--shadow-modal`; the tooltip stays border-only — too small to shadow).

**Motion:** default none. Allowed: attention onset (one 200ms ease-out color/edge transition),
streaming caret blink, dialog/menu 120ms fade-scale. Forbidden: idle pulses, skeleton shimmer
loops on live data, anything that animates during silence (honest-liveness rule).

**Signature — the cadence instrument (`<Cadence>` widget):** one component rendered everywhere
a session appears (tree row, pane header, mobile card): a state dot plus a 64×10px activity
trace of the last ~60s of frame arrivals as vertical ticks that fade with age. (The plan's
original sketch said 24×10; implementation landed on 64×10 for tick legibility and every
consumer + test pins 64 — recorded here so the doc matches the shipped truth.) Working = fresh
ticks (alive token); quiet = ticks visibly aging to `--ink-low`; needs-you = dot and trailing
edge in attention amber; failed = danger. It never animates on its own — it only re-renders
when frames actually arrive, so a busy agent shows a dense fresh trace and a stalled one shows
honest decay. **A trace with no in-window frames renders no SVG at all** — the dot alone —
so callers without a live frame feed (rail rows) don't reserve 64px of dead width per row.
Props: `{state: "idle"|"working"|"needs-you"|"failed"|"ended", frameTimes: number[], now:
number}`.

---

## 2. Tokens as shipped

`src/styles/tokens.css` is the single source of every color, type, space, radius, and motion
value in the app. Consumers only ever write `var(--name)` — no component CSS branches on
theme; the light-theme block (`[data-theme="light"]`) redeclares every color token under the
same name, and `token-contract.test.ts` (§4) fails CI if a token exists in one theme's block
but not the other's.

**Color** — a six-step neutral surface ramp replaces the old three-step one: `--surface-0`
(app background — page), `--surface-canvas` (pane wells, rail), `--surface-1` (panes/cards),
`--surface-inset` (card header bands, code gutters), and the two interaction washes `--hover-1`
(resting hover) / `--hover-2` (pressed/selected). `--field` is the sunken form-control
background. `--surface-2` (raised: menus, dialogs) keeps its name and dark value, but now
carries its depth via `--shadow-overlay` (see Elevation below) rather than a lighter fill —
during the re-theme, call sites that used `--surface-2` purely as a hover wash migrated to
`--hover-1`, so `--surface-2` now appears only on genuinely raised layers and as the neutral
fill of small static elements (chip/badge neutral tone). Two border weights
replace the old single `--edge`: `--edge` (hairline) and `--edge-strong` (control borders,
overlay rings). `--ink-hi`/`--ink-mid` are unchanged in role. `--ink-low` was raised on
2026-09-06 until it clears 4.5:1 on `--surface-0` and `--surface-1` in both themes (4.7:1 and
5.4:1 dark, 4.8:1 and 4.6:1 light, contract-tested), because 96 text rules were already setting
it despite the token being documented for chrome. Its role did not change with the value: it is
still placeholder, disabled and hairline-adjacent ink, and the quiet text a reader actually
reads (speaker meta, thought summaries, the liveness line, rail section titles) moved up to
`--ink-mid`. The four semantic families
(`--attention`, `--alive`, `--danger`, `--accent`) keep their `-bg` (15% mix into the surface)
and `-edge` (40% mix into the hairline border) companions, and each now also gets a `-ink`
companion (`--attention-ink`, `--alive-ink`, `--danger-ink`, `--accent-ink`) for text usage.
Reason: Beautiful UI's bare light-theme hues measure 2.8–3.9:1 against white — fine for glyphs,
borders, and washes, but failing the 4.5:1 AA floor for text. Light `-ink` values are darkened
forms of all four hues; in dark only accent and danger needed brightening (attention/alive
already clear). Each is contract-tested ≥4.5:1 against its theme's lightest text grounds AND
the hue's own `-bg` tint — the fill chips/badges/toasts actually set `-ink` text on — the same
way as the diff-contrast pair (§4, item 3 below). The rule: a call site that sets a hue
as `color` uses `-ink`; glyphs, borders, and washes keep the bare hue. The tooltip carries its
own inverted mini-palette instead of sitting on `--surface-2` — `--tooltip-bg/-fg/-muted/-border`,
near-black in both themes. Light theme deliberately inverts the old surface order: `--surface-1`
(white) sits *lighter* than `--surface-0` (page), so cards pop instead of blending — see
`decisions.md`'s 2026-08-13 entry. The two dedicated diff-notation backgrounds
(`--diff-add-bg`/`--diff-del-bg`) are re-derived against the new surfaces each re-theme (quiet
1.05–1.2× vs `--surface-0`, AA for content); the sixteen ANSI colors are re-tuned to the new
neutral palette each time too. All color tokens are declared identically-named in both theme
blocks (§4, item 4).

**Type** — `--font-sans` / `--font-mono` (Inter Variable + JetBrains Mono Variable, self-hosted
from the `@fontsource-variable/inter` / `@fontsource-variable/jetbrains-mono` npm packages,
latin subset, `@font-face`-wired in `global.css` — no binaries committed);
`--font-weight-regular/medium/semibold` (400/500/600); `--tracking-display` (−0.02em, display
weight only — widened from −0.01em under IBM Plex);
`--font-size-caption/ui/body/pane-title/page-title/display`
(12/13/15/18/22/28px), with `--font-size-body` rising to 16px below 900px so no editable field
sits under iOS Safari's zoom threshold; `--line-height-body/ui/title` (1.6/1.4/1.25). The six
size steps are declared on `<body>` rather than `:root` because each is a `calc()` over
`--font-scale`, and a custom property resolves that reference against the element it is declared
on: the Settings → Theme font-size choice sets `--font-scale` on `<body>`, so the whole ramp
scales together. `src/styles/measure.test.ts` pins the six steps and the phone body off disk.

**The eyebrow recipe.** `--tracking-eyebrow` (0.06em) replaced `--tracking-micro` and the four
other tracking values that were in use, and it is now THE uppercase tracking in the app. The
recipe it backs is one rule with no variants: `--font-size-caption`, `--font-weight-medium` (or
semibold where a header band wants more), `--ink-mid` or darker, `text-transform: uppercase`,
`letter-spacing: var(--tracking-eyebrow)`, at most two words. It titles a container INSIDE a
page (InspectorCard's header band, RecommendationCard's kicker, Table headers, the rail's
section titles, the settings cluster headers) and it is NEVER a pane title: a PaneScaffold title
is the page's own heading, set sentence-case at `--font-size-pane-title`, semibold, `--ink-hi`
(§6). Literal `font-size` values outside `tokens.css`, `letter-spacing` outside the
`--tracking-*` tokens, and any uppercase rule that is not a complete eyebrow are all enforced by
`src/styles/token-contract.test.ts`.

**Faces and figures.** Mono is for machine text and nothing else: code, paths, shell command
summaries, diffs, and the identifiers in the Details panel. The chrome a reader looks at
constantly stays on the sans face with `font-variant-numeric: tabular-nums`, which buys column
alignment without switching faces: the model chip (`chrome/modelswitch.module.css:.value`), the
status row's percent/clock/queue figures, the rail's relative ages, and the turn footer.
`src/styles/faces.test.ts` pins those four rules off disk.

**Space & shape** — `--space-1` through `--space-9` (4/8/12/16/24/32/40/48/64px — IBM Carbon
Design System's own spacing progression, adopted because it's the only well-known scale that
exactly fits the plan's stated endpoints (4, 64) and step count (9) while staying on the 4px
grid; unchanged by the re-theme). Radii softened: `--radius-chip` (6px, NEW — chips, badges,
small tags), `--radius-control` (4px → 8px — buttons, inputs), `--radius-pane` (8px → 10px —
panes, cards, dialogs), `--radius-pill` (999px, unchanged — switch track). Everything already on
the two pre-existing tokens inherited the softer values for free.

**Vertical rhythm** is four named steps over that same grid, so the transcript has a vocabulary
for how far apart two things sit: `--rhythm-line` (4px, inside one item, an intent line above
its call), `--rhythm-item` (8px, between items in a run of tool calls), `--rhythm-group` (16px,
between a run and the next speaker header, and above a turn footer) and `--rhythm-exchange`
(24px, above a user message). `src/styles/rhythm.test.ts` pins each step to the site that names
it.

**`--session-measure` is the app's one reading measure**: 44rem (704px, about 90 characters at
15px, with room for a 100-column code block), declared on `<body>` and raised to 64rem under
`<body data-transcript-measure="wide">`. Settings → Theme → Transcript width is what writes that
attribute, through `stores/prefs.ts`. The transcript column, the composer, the cold start, the
spawn form and the settings content all read the same token, so they widen together and can
never drift apart the way a hand-copied literal did. The layoutguard case `transcript-measure`
fails when a plain agent paragraph runs past 100 characters per line at 1440 or 1920, or when
the column is not centred in its pane. Pane bodies pad `--space-5` on desktop and `--space-4`
below 900px (`panescaffold.module.css`).

**Motion** — `--motion-duration-attention` (200ms), `--motion-duration-overlay` (120ms),
`--motion-duration-hover` (150ms, NEW — color/background/border/shadow transitions on
hover/focus/press for interactive chrome; transitions name their properties explicitly, never
`transition: all`), `--motion-easing-standard` (`ease-out`). See §5 for the budget these back.

**Focus rings** — `--focus-ring` (2px solid accent) and `--focus-ring-danger` (2px solid danger).
Every interactive widget gets `outline: var(--focus-ring)` on `:focus-visible`; `--focus-ring-danger`
applies to destructive controls (a red cancel button's ring, for instance). The outline-offset
varies per site (positive, outside roomy controls; negative, for rows flush inside a clipping
container). One exception: `shell/palette/commandpalette.module.css` keeps a deliberate quiet
`2px var(--accent-edge)` ring on `.input:focus` — a full-strength `--focus-ring` on an actively-
typed text input is louder than the palette wants. Hand-rolled ring geometry (bare `px` + `solid`
outlines in any order/unit, the outline longhands, or the retired inset-ring `box-shadow` hack) is
contract-banned; see §4. Dropzone's dashed drag-target outline is signage, not a focus ring, and
holds the second (and last) contract exception.

**Stacking order** — The app's own z-index values are tokenized to keep stacking context
predictable. The ladder: `--z-raised` (1, local resize handles/overlay controls within a pane),
`--z-sticky-bar` (20, pane-level pinned bars), `--z-dialog` (1000, modal dialogs + scrim),
`--z-menu` (1020, menus, popovers, anchored floats), `--z-tooltip` (1030, tooltips beat menus),
`--z-toast` (1040, tops the app's own ladder). Raw z-index integers are contract-banned; see §4.
dockview's stylesheet sits outside the ladder: its drag overlay is 999 (`--z-dialog`'s 1000
clears it deliberately) and its drop-target container is 9999, which sits above everything for
the duration of a dock drag.

**Elevation** — Beautiful UI's ring-embedding shadows replace the old two-shadow system. Every
shadow embeds its own 1px ring, so a shadowed element never also declares a separate border for
the same edge: `--shadow-card` (panes/cards — Card uses this instead of a border now),
`--shadow-overlay` (menus/popovers/toasts/dialogs/sheets), and `--shadow-inset-field` (sinks
form controls into `--field`). Beautiful UI's btn/raised/hairline rungs were deliberately not
adopted — evener's Button has no bordered-neutral variant and nothing needed a mid float, so those
tokens would have shipped with zero consumers. The soft-layer alphas are per-theme
(dark heavier, light whisper-light), declared as literal color values in `tokens.css` per the
token contract — `--shadow-color` still feeds the sheet's edge-directed variants, which keep
their own directional geometry rather than the omnidirectional card/overlay shape. The tooltip
is deliberately shadowless beyond its hairline ring — its inverted mini-palette already carries
the depth at that size. `--shadow-modal` retired with the old system.

---

## 3. Widget inventory (locked API)

Every widget lives at `src/widgets/<name>/` (`index.tsx` + `<name>.module.css` +
`<name>.test.tsx`), is re-exported from the controller-owned barrel `src/widgets/index.ts`, and
has a gallery section at `src/dev/gallery-sections/<name>.tsx` showing every documented state in
both themes (enforced by `src/dev/WidgetGallery.test.tsx`'s completeness test — see §4).
`src/widgets/internal/` (currently just `requireClass`, a CSS-Modules type-safety helper) is
implementation machinery, not a widget: no gallery section, not in the barrel.

This table is the actual shipped surface, not the plan's original sketch — a few shapes evolved
during implementation (noted inline); this table is the one to trust.

| Widget | Props | Notes |
|---|---|---|
| **Button** | `{variant?: "primary"\|"quiet"\|"danger"; size?: "sm"\|"md"; icon?: ReactNode; children: ReactNode; onClick?; disabled?; type?: "button"\|"submit"\|"reset"} & Omit<ButtonHTMLAttributes, those>` | `forwardRef<HTMLButtonElement>`; spreads unrecognized native attributes (aria-\*, data-\*, id, ...) onto the `<button>` — `className` stays computed-only, never caller-overridable. The canonical exemplar every other widget's file layout/CSS/test style mirrors. |
| **IconButton** | `{label: string; icon: ReactNode; variant?; size?; onClick?; disabled?; type?} & Omit<ButtonHTMLAttributes, those \| "aria-label">` | Icon-only Button; `label` is required and becomes `aria-label` (no visible text). `forwardRef` + rest-spread, mirroring Button — reuses Button's CSS classes directly (read-only import), which does NOT carry over ref-forwarding/prop-spreading, so this is fixed independently. |
| **OpenButton** | `{label?: string; word?: string ("open"); iconOnly?: boolean; size?: "xs"\|"sm"; href?: string; onClick?; tabIndex?; title?}` | The standard "open out of this surface" affordance: the box-arrow **OpenIcon** glyph (exported alongside) after `word`, or glyph-only (`iconOnly`) for dense rows. Every open-out site routes through it — delegate/delegate_send rows' "Open transcript", notification cards' "Open subagent", tool rows' "Open beside", the activity tree's nested glyph, settings' "open in editor" (`href` renders an `<a>` new-tab/no-opener/no-referrer instead of a `<button>`). Owns `stopPropagation` because it always rides something clickable (a disclosure head, a tool row, a tree row). **Its rendering is planned to change** — centralization here is what makes that a one-place change. |
| **Cadence** | `{state: "idle"\|"working"\|"needs-you"\|"failed"\|"ended"; frameTimes: number[]; now: number}` | The signature widget — see §1. Pure (no timers, no `Date.now()`); ticks render as SVG `<rect>`s, age→opacity in 4 buckets (15s each, half-open `Math.floor` boundaries); needs-you tints the freshest ticks amber too ("trailing edge"), not just the dot. |
| **Chip** | `{children: ReactNode; tone?: "neutral"\|"attention"\|"alive"\|"danger"; onRemove?: () => void}` | Small labeled pill; `onRemove` renders a remove button, `aria-label` derived from string children or `"Remove"`. |
| **Badge** | `{count: number; tone?: "neutral"\|"attention"\|"alive"\|"danger"}` | Numeric count indicator, caps display at "99+". |
| **StatusDot** | `{state: CadenceState}` | Just the dot (imports `CadenceState` from Cadence, doesn't redeclare it) — for tighter contexts than Cadence's full trace; carries its own accessible name since nothing else labels it standalone. |
| **Meter** | `{label: string; value: number; max: number; tone?: "neutral"\|"attention"\|"alive"\|"danger"}` | `role="meter"`; `label` is required (not optional as an early sketch had it) since role=meter needs an accessible name and a Meter can't ship without one. Fill width via a `--fill` style custom property, not an inline style rule. |
| **Skeleton** | `{lines?: number}` (default 3) | Static bars, no shimmer (honest-liveness rule) — announces "Loading" once for AT; bars themselves are decorative. |
| **EmptyState** | `{title: string; hint?: string; action?: ReactNode; size?: "default"\|"display"}` | `size="display"` sets the title at `--font-size-display` for the one pane whose empty state IS the page (Welcome); everything else keeps `--font-size-pane-title`. `action` is optional (an early plan sketch showed it required; a pane with nothing actionable — e.g. a read-only empty log — is an ordinary case, and every sibling slot-style prop this wave is optional, so this was kept optional as the more consistent, more correct shape). |
| **Table** | `{columns: TableColumn<Row>[]; rows: Row[]; rowKey(row); sortKey?; sortDir?: "ascending"\|"descending"; onSortChange?; filters?: {key,label,active}[]; onFilterToggle?; empty?}` | Controlled sort + filter; semantic `<table>`, `aria-sort` on sortable headers, filter chips compose Chip, horizontal overflow scrolls inside the widget. Ported from Beautiful UI's Records/Filter Table. |
| **DiffTable** | `{columns: {key,label}[]; rows: {key; cells: Record<string,{value; proposed?}>}[]}` | Tabular proposed edits: struck-through old value beside the new one on the neutral `--diff-add-bg` wash — same hue-gate exemption as DiffBlock. Ported from Beautiful UI's Diff Table. |
| **Loader** | `{label?; startedAt?; now?}` | Indeterminate-wait indicator (pixel grid + mm:ss elapsed); prop-driven like Cadence, no internal timers. Its animation is the sanctioned exception for user-initiated waits — never agent liveness — and lives entirely inside the reduced-motion gate. Ported from Beautiful UI's Loading State. |
| **InsightCard** | `{insights: {title; body; series?: number[]}[]; page; onPageChange}` | Paged insights with an inline SVG sparkline (aria-hidden + visually-hidden min/max alternative); pagination composes IconButton. Ported from Beautiful UI's Insight Cards. |
| **RecommendationCard** | `{title; body; confidence?; onAccept?; onReject?; alternatives?}` | Agent-suggested action: micro-label eyebrow, confidence meter (`--accent` on `--field`; not the hue-gated Meter), Accept/Dismiss compose Button. Ported from Beautiful UI's Recommendation Card. |
| **ContextCard** | `{source; snippet; meta?; href?}` | Retrieved-knowledge chunk: inset card, ToolIcon source glyph, 3-line snippet clamp; renders as a link when `href` given. Ported from Beautiful UI's Context Cards. |
| **InspectorCard** | `{title; properties: {key; label; value; options?; onChange?}[]}` | Property inspector: micro-label header band, hairline rows, editable rows compose Select, read-only values in mono. Ported from Beautiful UI's Fine-tune inspector. |
| **Card** | `{children: ReactNode}` | Passive raised container; its ring is `--shadow-card`'s embedded 1px, not a border. |
| **Input** | `{value: string; onChange; placeholder?; disabled?; type?: "text"\|"password"\|"email"\|"search"\|"number"\|"tel"\|"url"; id?; name?}` | Controlled only; labeling is the consumer's job via `<label htmlFor>`. |
| **Textarea** | `{value: string; onChange; placeholder?; disabled?; autoGrow?: boolean; rows?; id?; name?}` | `autoGrow` counts literal `"\n"` occurrences, not wrapped lines. |
| **Select** | `{value: string; onChange; options: {value; label}[]; disabled?; id?; name?}` | Native `<select>`, restyled — no custom listbox (Combobox covers richer cases). |
| **SegmentedControl** | `{label: string; value: T; options: readonly SegmentedControlOption<T>[]; onChange(value: T); disabled?; size?: "sm"\|"md"; fullWidth?; id?; "aria-describedby"?}` (`T extends string`) | Two-to-six concise choices; horizontal radiogroup of native buttons with roving focus, neutral selected state, `md` default, and optional full-width track. |
| **Disclosure** | `{summary: ReactNode; children: ReactNode; disabled?; "data-testid"?} & ({id: string; defaultOpen?; open?: never; onOpenChange?: never} \| {open: boolean; onOpenChange(open: boolean); id?: never; defaultOpen?: never})` | Native `<details>/<summary>` with persistent store-backed or controlled state; disabled summaries are inert, removed from the tab order, and attenuated without dimming an open body. |
| **Switch** | `{checked: boolean; onChange: (checked: boolean) => void; disabled?; label: string}` | `role="switch"` on a real `<button>`, not a styled checkbox; `label` is required and always-visible, wired via `aria-labelledby`. |
| **KeyHint** | `{keys: string[]}` | One `<kbd>` per key, "+"-separated; the literal key name `"Mod"` renders as ⌘ on Apple platforms, `Ctrl` elsewhere. |
| **Combobox** | `{options: T[]; onQuery; onPick; renderOption?; "aria-label"?; "aria-labelledby"?}` (generic over `T extends {id; label}`) | ARIA 1.2 combobox-with-listbox-popup; real focus never leaves the input. `aria-label`/`aria-labelledby` forward to BOTH the input and the popup listbox (fix-wave: the listbox had no name of its own — see §4) — they're two roles describing one picker, sharing one label source. Debounces `onQuery` 150ms. Never traps focus. |
| **Menu** | `{trigger: ReactNode; items: {id; label; onSelect; disabled?}[]}` | Trigger + popup; roving tabindex among items (skipping disabled), no typeahead. Popup `role="menu"` gets `aria-labelledby` pointing at the trigger `<button>`'s own id (fix-wave — see §4). Traps focus (`FocusScope trap`). |
| **Dialog** | `{open; onClose; title; children; footer?}` | Modal: centered, 120ms fade-scale, Escape/scrim-click close, trapped + restored focus. Shares its whole contract with Sheet via the internal `OverlayPanel`. |
| **Sheet** | `{side?: "right"\|"bottom"; open; onClose; title; children; footer?; bodyClassName?}` | Same contract as Dialog (shared `OverlayPanel`); only geometry/slide-in animation differs. `bodyClassName` lands on the sheet's own body element, which is how the sessions drawer renders the Rail flush inside the sheet instead of as a bordered box nested in a bordered box. |
| **FocusScope** | `{trap?: boolean; children}` | The focus-management primitive Dialog/Sheet/Menu build on: moves focus in on mount, restores on unmount; traps Tab/Shift+Tab when `trap`. Does not (yet) set `inert` on anything outside the scope — see §4. |
| **Tooltip** | `{label: string; children: ReactNode}` | Hover/focus-triggered, 300ms delay, hidden on touch via CSS. `aria-describedby` wired via `cloneElement` onto a single-element child — works for a native element or any widget that forwards a ref + spreads rest props (Button/IconButton both do, since the fix-wave in §4). |
| **Toast** + `useToasts()` | `useToasts(): {push: (kind, text) => void}`; `<Toast/>` takes no props | Module-singleton queue (`useSyncExternalStore`), mounted once near the app root. 5s auto-dismiss, true pause/resume on hover (tracks remaining time, doesn't restart the full window — fix-wave, see §4). |
| **PaneScaffold** | `{title; cadence?; actions?; footer?; children}` | The standard pane chrome: header (title + cadence slot + actions) + scrollable body + optional footer. Most-copied layout primitive in the app. |
| **CodeBlock** | `{text: string; language?: string; showLineNumbers?: boolean}` | Mono block with a copy button (renders a real `Button` internally); no syntax highlighting (YAGNI this wave). |
| **Markdown** | `{source: string}` | `marked` → DOMPurify-sanitized HTML → `innerHTML`; fenced code renders through CodeBlock's stylesheet; links open in a new tab with no opener access. |
| **DiffBlock** | `{unified: string}` | Per-line domain notation on already-diffed text (dedicated add/remove tints plus `+`/`−` markers); does not compute a diff itself. |
| **Tree** | `{nodes: T[]; onActivate; onToggle; renderRow}` (generic over `T extends {id; children?; expanded?}`) | Keyboard-navigable (`role="tree"`), roving tabindex, Up/Down/Right/Left/Enter. Fully controlled — `renderRow(node, {depth, expanded, hasChildren, toggle, activate})` owns each row's visible content; Tree owns structure/ARIA/keyboard path only. |
| **VirtualList** | `{count; estimateSize; renderRow; ref?: Ref<VirtualListHandle>}` | Wraps `@tanstack/react-virtual`; `ref` exposes `{scrollToIndex}` via the React 19 ref-as-prop pattern (not a `forwardRef` wrapper). Sizes come from `estimateSize` alone, no `measureElement`. |

**ToolRunGroup is deliberately not a widget.** It lives at
`src/panes/session/transcript/ToolRunGroup.tsx` with its own stylesheet, not under
`src/widgets/`, because it is transcript grammar rather than a reusable primitive: no barrel
entry, no gallery section. It is what finally renders principle 2 of the mockup brief
(`decisions.md`, topic 06 Alt A). The rule, owned by `transcript/toolRuns.ts`: in a SETTLED
turn, three or more consecutive completed, non-failed tool calls whose renderer has opted in
collapse into one `<details>` row labelled `N steps · <last consequential summary>`. Folding is
opt-in: `fold: "quiet"` (the reads, searches, web fetches, transcript reads) folds and only
counts; `fold: "consequential"` (the edit tools, shell, worktree) folds and is the step the label
names, being a mutation; `fold: "never"` (delegate, ask_user, task_list, use_skill, the `job_*`
tools) and any descriptor with no policy, which is every unregistered or MCP tool, stay on their
own row, because a tool the UI does not know may have had a side effect the reader must see.
Scroll and focus anchors follow the same fold (`foldTurnEntries`): a folded run is one anchor. A live turn never
folds at all, and a failure, a call still in flight, an auto-expanding card or any non-tool entry
breaks the run rather than being spanned by it. Disclosure state goes through the shared
disclosure store, so a reader's choice survives re-projection and the transcript's
expand-all/collapse-all baselines reach it; the body mounts only while open.

---

## Directory selection: one shared interaction

Every web directory field **must use `DirectoryPicker`** from
`src/widgets/directorypicker`, normally through `PathField kind="dir"`.
This includes session start, schema-driven launch options, global/project settings,
plugin and skill directories, In-repo config, and local marketplace sources. Use the same
responsive dialog on desktop and mobile; do not add a directory popover, datalist,
or a feature-local browser. The working example is `/dev/widgets` → DirectoryPicker.

Directory mode requires injected `directory.validatePath` and
`directory.createDirectory` actions plus `complete`; the widget remains wire-free.
The TypeScript `PathFieldProps` contract requires these actions for directory mode.
Use the settings store's `directoryActions` or the caller's existing client closures.
Recent directories are optional and appear only where that history is meaningful.
Pass the field label through `ariaLabel` on labeled `PathField` controls so the
accessible name includes both the field name and selected path; a native label
alone overrides the button contents.

The behavioral contract is:

- Browsing, breadcrumbs, Up, recent locations, and typed paths change a local draft.
  **Go** (or Enter in Path) validates and navigates; **Use this folder** commits once.
  Cancel, Escape, and outside dismissal preserve the committed value.
- **New folder** explicitly creates a child of the viewed directory, displays errors
  inline, and navigates into the result. Creation does not select the directory,
  submit a settings row, save configuration, or start a session.
- Only validated directories can be confirmed. A failed or pending child listing
  does not invalidate a directory. Stale responses cannot overwrite newer navigation.
- External committed-value changes reset the draft. Custom callers key the picker
  by that value; `PathField` does this itself.
- Keep long paths readable by wrapping. Preserve the shared modal focus scope,
  restore focus to the trigger on close, and select Path text on its first focus.
  Opening does not force the mobile keyboard up. Mobile confirmation stays above
  the keyboard inset; desktop uses the roomy dialog.
- Paths belong to the supported Linux/macOS hub filesystem, regardless of browser OS.

`PathField kind="file"` and `kind="outputFile"` retain file completion and literal
file-path entry. Their internal completion panel is not a directory-selection API.
Collection **Add** and form **Save** remain separate actions after directory
confirmation; changing the picker must not bypass their domain validation.

Tests must exercise draft-versus-commit behavior, cancellation, creation/errors,
external value changes, and real browser geometry. Do not encode the retired
browse-immediately-commits behavior in new tests. This contract supersedes older
path-picker behavior in dated plans and legacy parity checklists.

---

## 4. The color-is-attention rule, machine-enforced

**The rule:** chroma is scarce and means something specific. `--attention`/`--alive`/`--danger`
each carry exactly one meaning everywhere in the app (a human is needed / the agent is working /
something failed); reaching for one outside a widget with a genuine matching state is a bug, not
a style choice. `--accent` is different in kind, not degree — see below.

**Enforcement:** `src/styles/token-contract.test.ts` reads every `.module.css` + `global.css`
under `src/` directly off disk (`node:fs`, not Vite's `?raw` import — see the file's own header
comment for why: under vitest's default config, a `.css?raw` import silently returns an empty
string, a real upstream issue this project works around rather than papering over) and runs
six independent checks:

1. **File naming.** Every stylesheet besides `tokens.css` is named `global.css` or
   `<name>.module.css` — the convention the rest of the contract, and the whole widget
   directory layout, assumes holds.
2. **No chromatic literal outside `tokens.css`.** Two mechanisms: hex / `rgb()` / `hsl()` /
   `oklch()` / `oklab()` / `lab()` / `lch()` scanned across whole files (comments included —
   these forms are distinctive enough not to false-positive on a selector or class name); the
   148 CSS named colors (`red`, `white`, `black`, ...; not `transparent`/`currentColor`, which
   aren't chromatic) scanned only inside extracted declaration *values*, after stripping block
   comments — named colors are ordinary English words that legitimately appear in class names
   and font stacks, so this one has to be scoped narrowly to avoid false positives (a class
   literally named `.red` is not a violation). `color-mix()` composing existing `var(--token)`
   values is never a violation, at any scope — it introduces no new color.
3. **The three attention-family vars stay on a reviewed allowlist.** Currently: `cadence`,
   `button` (danger variant), `chip`/`badge`/`toast` (tone props), `statusdot` (state color),
   `meter` (danger/attention fill), `dialog` (danger footer). A
   widget earns a place on this list only when it has a state that genuinely needs one of the
   three hues — never for decoration. **`--accent` is deliberately exempt from this check
   entirely** — it's interaction chrome by definition (every interactive widget needs an accent
   `:focus-visible` ring; accent also carries selection and links), so gating it would grow the
   allowlist by one entry per interactive widget forever while protecting nothing. The
   color-is-attention thesis guards the three *attention-class* hues' meanings; focus/selection
   chrome was never the thing it was protecting.
4. **Dark and light blocks declare identical color-token name sets.** A token declared in only
   one theme's block silently breaks the other (falls back to the wrong hue, or resolves to
   nothing) — checked by extracting both blocks via brace-depth counting and diffing their
   declared names.
5. **Z-index values are tokenized.** Every `z-index` declaration uses `var(--z-*)` (from the
   ladder above), `0` (reset), or `auto` (default) — never raw integers. Keeps stacking context
   predictable across the app.
6. **Focus rings use tokens, not hand-rolled geometry.** Every `:focus-visible` outline is
   `var(--focus-ring)` or `var(--focus-ring-danger)`, never an outline shorthand carrying its own
   length + line style (any order, any unit), never the outline longhands, and never the retired
   inset-ring `box-shadow` hack (an inset spread-only shadow in any color, or a shadow colored
   with bare `--accent`/`--danger`; `-bg`/`-edge` mixes in a shadow are tinting and stay legal).
   Two exact-path exceptions: `shell/palette/commandpalette.module.css` keeps a quiet
   `2px var(--accent-edge)` ring for active typing — softer than a full `--focus-ring` but still
   present — and `widgets/dropzone/dropzone.module.css` keeps its dashed accent drag-target
   outline, which is drop-here signage, not a focus ring.
7. **Every `var(--name)` without a fallback resolves to a declaration** somewhere under
   `src/` (tokens.css or a module's own local property). Exempt: dockview's `--dv-*` and the
   runtime-set names JS or an inline style declares (`--keyboard-inset`, `--rail-width`,
   `--tap-min`, `--fill`, `--markdown-ink`, `--prose-font-size`, `--prose-ink`,
   `--density-scale`, `--font-scale`). Four undefined tokens had shipped before this check
   (`--radius-sm` twice, `--edge-hi`, `--font-size-title`), each silently falling back to the
   property's initial value.
8. **No literal `font-size` in px outside `tokens.css`.** Only `var(--font-size-*)`, `inherit`,
   and relative units (`em`, `%`) are legal; inline code is sized relative to its line, which
   is why `em` stays.
9. **`letter-spacing` only through the two tracking tokens** (`--tracking-display`,
   `--tracking-eyebrow`), `inherit`, or `normal`.
10. **Uppercase means the eyebrow recipe.** Any rule block with `text-transform: uppercase`
    must also declare `font-size: var(--font-size-caption)` and
    `letter-spacing: var(--tracking-eyebrow)`, and must not set `color: var(--ink-low)`.

Every mechanism above is poison-tested against hand-written snippets proving both what it
catches and what it must not flag (see the test file itself) — not just asserted to work.

**Ruling (Jesse, 2026-07-26, kata 9jew): dedicated diff colors are syntax/domain notation, not
status.** DiffBlock uses exactly `--diff-add-bg` and `--diff-del-bg` in both themes. They are
quiet, conventionally green/rose structural washes, with the `+`/`−` marker carrying the
meaning independently of color; their foregrounds remain the existing neutral ink tokens.
DiffBlock must not expose or reuse `--alive`/`--danger`, and it must not join the semantic
allowlist or create a broader status hue family. This ruling supersedes mockup 19's historical
palette details as an implementation authority: the mockup may show the visual intent, but it
cannot reopen the semantic-color contradiction. Recorded here so the token contract and the
widget stay aligned.

---

## 5. Motion budget

The law widened 2026-08-13 from "default none" to **"no idle motion"**: idle animation stays
banned exactly as before, but input-response transitions on interactive chrome are now
budgeted. Three budgets, all on `--motion-easing-standard` (`ease-out`):

- **Attention onset** (`--motion-duration-attention`, 200ms) — a state crossing into
  needs-you. Cadence's dot, StatusDot, and Switch all use it for their own state-driven color
  transitions.
- **Overlay fade-scale** (`--motion-duration-overlay`, 120ms) — Dialog, Sheet, and Menu's
  open/close.
- **Hover/focus/press response** (`--motion-duration-hover`, 150ms, NEW) — color, background,
  border, and shadow transitions on interactive chrome (buttons, inputs, rows, chips, ...)
  triggered by hover, focus, or press. Transitions name their properties explicitly; a blanket
  `transition: all` is never used.

Forbidden, unchanged: idle pulses, shimmer loops on live data, anything that animates during
silence (the honest-liveness rule — a "working" indicator that looks identical whether the agent
is streaming or hung is worse than no indicator). Every widget with motion of its own respects
`prefers-reduced-motion: reduce` (currently: Cadence, Dialog, Disclosure, Menu, SegmentedControl,
SelectionQuote, Sheet, StatusDot, Switch) — collapses to instant, no exceptions.

---

## 6. Copy rules

Sentence case for all UI copy; no ALL-CAPS **in copy** (button labels, headings, messages,
hints). Active-voice labels ("Save changes", not "Changes saved" or "Save Changes"). Mono is
for machine text only — code, tool output, paths, commands, identifiers — never chrome labels,
captions, or any text a human authored. (This tripped up even this wave's own gallery scaffold
once: three caption labels shipped on `--font-mono` in the foundation task and were caught and
fixed in wave-close review — see git history for `gallery-section.module.css` and
`theme-flip.module.css`. If it happened once, watch for it.)

**The eyebrow recipe (2026-09-06, replacing the 2026-07-24 "section eyebrow" clarification
and the separate micro-label pattern it was distinguished from).** Small-caps *eyebrows* are a
sanctioned pattern, distinct from ALL-CAPS copy: the copy stays sentence-case in the source and
the transform is presentation-only. There is now exactly one recipe, one tracking token and one
size for it: `--font-size-caption`, `--font-weight-medium` (or semibold where a header band
wants more), `--ink-mid` or darker, `text-transform: uppercase`,
`letter-spacing: var(--tracking-eyebrow)` (0.06em, which replaced a spread of
0.02/0.04/0.05/0.08em), and **at most two words**. An eyebrow titles a container INSIDE a page:
InspectorCard's header band, RecommendationCard's kicker, Table headers, the rail's section
titles, the settings cluster headers. Three-word labels were the tell that the rule was being
broken, so "Agents & models" became **"Agent setup"**.

Never an eyebrow: buttons, sentences, and above all **titles**. A pane title is the page's own
heading, sentence-case at `--font-size-pane-title`, semibold, `--ink-hi` (§2); a session title is
the user's own prompt and is never transformed at all. Before this, both rendered as a 12px
uppercase micro-label, which turned a whole prompt into a shouted sentence.

---

## 7. The system voice

Three voices appear in a transcript: the human, the agent, and the system
steering the agent. The first two are marked. This section marks the third.

**The rule: a glyph in the gutter means the agent's instructions changed. An
empty gutter means it is a passive fact.**

The transcript already has a 10px glyph gutter, sized to `SteeringGlyph` and
`FailureGlyph`'s own SVG (`viewBox="0 0 10 10"` — neither widget declares a wider
box) — `toolcallitem`'s `.row` and `systemnoticeitem`'s `.failure` share one
`display: flex; align-items: baseline; gap: var(--space-2)` grammar. This section
assigns that column.

| gutter | member | treatment |
|---|---|---|
| `◇` | **steering** | `SteeringGlyph`, `--ink-mid` for the whole row, kind from the wire, chevron trailing |
| `✗` | **failure** | `FailureGlyph` in `--danger`, text in `--ink-hi` |
| *(empty)* | **lifecycle fact** | `--ink-low` one-liner; a run of 3+ collapses into one disclosure |
| `▸` box | **scaffolding** | hairline-bordered box: the system prompt, compaction summaries, round timings |

Notification cards sit outside the rule — a card is not a row and has no gutter.

**Steering labels come from the wire, never from the text.** `SteeringInjectedData.Kind`
(`agent/events/payloads.go`) is set at each injection site and reaches the
renderer on both the live and reload paths. A steer with no kind renders
`System steered` with no colon: a colon promises a value, and the UI does not
guess at one.

**The two sides cannot drift.** `make generate` emits the Go enum into
`types.gen.ts` as `STEERING_KINDS` plus the union `SteeringKind`, and
`SteeringItem.tsx` types its label map as `Record<LabelledKind, string>` over that
union. Adding a kind in Go and regenerating fails `tsc` with a missing-key error
naming the kind, until it is given a label, suppressed, or routed to a card. This
is the only mechanism enforcing that — deliberately, since a second one covering
the same property would be worse than either alone.

Pattern-matching is the alternative this forecloses. `steeringClassify.ts`
inferred a kind from 8 text patterns, against the seventeen kinds the daemon
actually names today — one pattern matched `/reading without writing/`, a string
that appears nowhere in the Go source, and nothing failed when it went stale.
The file's own header now explains why it stopped inferring one; that silent
gap, a renderer's idea of what the daemon says drifting from what it actually
says, is the failure mode this rule exists to prevent.

**Why `--ink-mid` and not `--ink-low`.** Every other quiet system row uses
`--ink-low`. Measured against `--surface-1` that token is 4.72:1 in dark and
4.76:1 in light since the 2026-09-06 raise (§2), so it clears the 4.5:1 AA
floor it used to sit under, but its role is still placeholder and
hairline-adjacent chrome rather than text a reader is meant to read. A reader
scanning steering is auditing which kind fired, so the kind is the payload rather than furniture, and
it sits one ink step up at 6.51:1 / 5.84:1. That step also separates a steer
from the lifecycle line beneath it by weight as well as by glyph.

**The glyph is a hollow diamond, drawn as SVG rather than set as a character.**
It was first tried as the reference mark ※, but at the row's actual 10px ship
size a faithful ※ collapses into a shape indistinguishable in monochrome from
`FailureGlyph`'s ✗ — a mark that can appear in the very same gutter column, the
two meanings then separated only by hue. A diamond has no such collision at any
size. SVG rather than the character ◇ (U+25C7) because `global.css`'s
`unicode-range` (`global.css:23-24`) subsets IBM Plex Sans to a range with no
U+25xx block at all, so a literal ◇ would be the one glyph in the app rendering
from a system fallback font. `SteeringGlyph` draws it, inherits `currentColor`,
and — unlike `FailureGlyph` — carries no accessible name, because the row's own
text already says "System steered: <kind>".

---

## 8. Known gaps (documented, not fixed — wave-close adjudication)

Two items reviewed at wave-close and deliberately left as documented gaps rather than quick
fixes, because the "quick fix" in both cases risked being wrong in a way that's worse than the
current gap:

- **FocusScope doesn't set `inert` on anything outside the trapped scope.** Tab-trapping
  (`trap=true`) covers keyboard navigation, which is what this project's tests exercise and
  what the large majority of real interaction is. The residual gap is a screen reader's virtual
  cursor (or touch exploration) reaching content outside the scope that a sighted keyboard user
  would never land on. A correct fix needs to know what "outside the scope" even means for a
  given consumer: Dialog/Sheet's `FocusScope` has no DOM siblings at all (the scrim wraps it
  alone), so there's nothing to make `inert` there; Menu's `FocusScope` sibling IS the trigger
  button, which needs to stay clickable to close the menu on a second click — naively making it
  `inert` would break that. The real fix is portal-rendering overlay content up to a stable
  app-root position (none of Dialog/Sheet/Menu do this — they render inline in the component
  tree today) and inerting siblings AT THAT level, which is a real architectural change, not a
  FocusScope-local one. Flagged for a future pass alongside adopting portals, not bolted on now.
- **Tooltip's timer and `aria-describedby` wiring stay fully active on touch devices**, even
  though the visual bubble is CSS-hidden there (`@media (hover: none)`, since a tap has no
  `mouseleave` to dismiss an open tooltip with). This looks like wasted work worth suppressing
  via a `matchMedia('(hover: none)')` gate, but doing that would also suppress the
  `aria-describedby` association for a touch/AT user navigating by focus (e.g. VoiceOver swipe
  navigation on a touchscreen) — who would genuinely benefit from the description being
  announced even though they'll never see the visual bubble. Suppressing the "dead" wiring and
  removing a real accessibility benefit for exactly the users who might need it most is a worse
  trade than leaving admittedly-redundant code running. A narrower alternative was considered —
  gate only the mouse path (`onMouseEnter`/`onMouseLeave`, which never fires on a real touch
  device anyway) behind `matchMedia('(hover: none)')` while leaving `onFocus`/`onBlur` fully
  ungated, since Tooltip already wires all four as independent handlers — but it's flagged for
  the same follow-up pass rather than made now, without real-device AT verification that it
  doesn't change touch+AT behavior in some non-obvious way. Left as-is, flagged for a more
  careful pass that can validate actual AT behavior on a real touch+screen-reader device, not
  reasoned about in the abstract.

---

## 9. Command surfaces: palette vs. composer

**The principle (2026-08-14): the palette is where you go; the composer is where you act on this
session.** Every command in the registry (`shell/palette/commands.ts`) is tagged with a surface —
`commandSurface()`, derived from the same `scope` field that already decided whether a session
needs to be focused. An **app-global** command (new session, spawn, theme, dashboard, search,
help, upgrade, next-needs-you, open settings) needs no session and stays palette-native: it is
listed, filtered, and run entirely inside the command palette (`Mod+K`), exactly as before. A
**session** command — every mutation or read that acts on the focused session, built-in (goal,
model, reasoning effort, status, compact, clear, steer, queue, interrupt, shutdown, aside,
drain-as-steer, copy-id, tasks, project) or a plugin's own slash command — runs ONLY from that
session's composer, never the palette.

**The composer is the session's own command line, Slack-model.** Its inline `/` menu
(`slashCompletion.ts`'s `mergeSlashCommands`) lists the session-scoped built-ins merged with the
plugin catalog — one list, each row stating what it does or, for a plugin command with no
description, naming its plugin provenance. Submitting a message that PARSES as a known built-in
invocation (a leading `/name` with optional args) runs that command's RPC instead of sending the
text — a literal message that happens to start with a recognized `/command` executes rather than
sends, matching the muscle memory Slack and Discord users already have. Feedback is a toast plus
whatever live chrome the mutation already drives (the goal chip, the status row); the draft clears
on success and is preserved verbatim on failure, so a rejected command never costs the user their
typed text. Anything that does NOT parse as a known built-in — an unrecognized `/foo`, or a
plugin's own slash command — sends as an ordinary chat message: that's the escape hatch, and it's
deliberate, not a gap.

**The palette delists every session command and offers a handoff instead.** Typing a `/`-prefixed
filter that matches a session command's name (built-in or plugin) shows exactly ONE row —
"Continue in the composer: /goal …" — carrying the raw text as typed. Activating it inserts that
text into the focused session's composer and moves focus there, closing the palette; the palette
itself never executes a session mutation or makes a wire call for one. With no session focused,
the same row explains that there's nowhere to hand off to yet, rather than silently doing nothing.

## 10. Collection pages: segmented workspaces and detail sheets

**The pattern (2026-08-29): when one page holds several same-weight collections, segment them —
never stack them.** The first collection page this shipped on is Settings → Marketplaces &
Plugins (`panes/settings/sections/marketplacesPlugins/`), which previously stacked three
sections (registered marketplaces, the browse tree, the installed list) down one long scroll.
It is now the reference implementation for the two idioms below; any future page with the same
shape (several sibling lists, plus per-item detail and actions) should reuse them rather than
inventing a third layout.

**One list at a time, chosen by a page-level SegmentedControl.** Each sibling collection becomes
a segment; the segment labels carry the counts (`Installed (7)`, `Marketplaces (3)`), and the
per-section headers — title plus count — are deleted, because duplicating that identity under
the segment control is noise. The default segment is the one the user maintains most (Installed,
not Browse). Switching segments is a page-level navigation act: page-scoped overlays owned by
the outgoing segment close (see the sheet rule below), while per-segment UI state that is
expensive to rebuild (the browse tree's expansion and its lazy catalog cache) is lifted to the
page so it survives the round trip.

**Rows are single tappable targets; actions live in a detail sheet.** A collection row carries
identity and status only — `StatusDot`, name, state chips, one mono meta line
(`@ marketplace · v1.2.0`) — and a trailing chevron; it is one full-width `<button>`, so the
whole row is the target on desktop and touch alike. A row NEVER grows a trailing cluster of
small action buttons (the pre-redesign installed row had four): every action on the item moves
into the item's **detail sheet**, a `Sheet` with `side="right"` on desktop and `side="bottom"`
at the mobile breakpoint (chosen via `useIsMobile`, the same source the shell uses). The sheet
is the item's inspector: state chips, its catalog description (pulled lazily through the browse
cache — re-open is free), a meta table, and its actions. Binary state (Enabled, Auto-upgrade)
is a `Switch` row inside the sheet, disabled while its RPC is in flight; the primary mutation
is a footer `Button`; the destructive action keeps its `ConfirmDialog` even though that nests a
second modal over the sheet — `OverlayPanel` instances stack in DOM order, each traps and
restores focus down the stack, and its `preventDefault` on Escape is what keeps the settings
pane's own document-level Escape handler from closing the pane out from under an open overlay.

**The meta table idiom.** Inside an inspector, facts render as label/value rows: a fixed-width
(96px) caption-color label column, values in the UI font, and `var(--font-mono)` for anything
machine-shaped — versions, sources, paths — truncating with ellipsis rather than wrapping.
This is the same vocabulary as the list row's meta line, one zoom level up.

**An inspector is only as alive as its subject.** The detail sheet reads its entity from the
store rather than a prop snapshot, so cross-client changes land while it is open; when the
entity disappears from the store (its own Remove completing, or another client's), the sheet
closes itself instead of offering actions on a ghost, and a failed Remove keeps the sheet and
dialog open for retry. Segments own their overlays: switching away closes the sheet, coming
back does not reopen it.

## 11. Mobile forms and honest cold starts

Mobile forms use settings-style rows when several related choices must remain
scannable on a narrow screen. A row fills the available width, is at least
48px tall, uses sentence-case sans labels, and truncates its value without
shrinking the label or the hit target. Interactive rows expose the whole row
as one control; read-only facts do not show a misleading caret. Use existing
surface, edge, ink, spacing, and tap-size tokens. Do not introduce mobile-only
colors, type tokens, chip backgrounds, or monospace labels.

Editable text is 16px on phones, and it gets there through the ramp rather
than per-field: `tokens.css`'s below-900px block sets `--font-size-body` to
16px and every field takes the body size, so iOS Safari has nothing to
auto-zoom into. That is what let the viewport meta stop locking zoom (WCAG
1.4.4 resize text): `index.html` no longer carries
`maximum-scale=1, user-scalable=no`, and `src/styles/viewport-pin.test.ts`
fails if either comes back. An auto-growing textarea has a real content-driven
minimum and maximum, keeps the resize behavior accessible, and reserves space
for any pinned actions below it. The field remains the same semantic textarea
and preserves keyboard submission, attachment, paste, and screen-reader
behavior across breakpoints.

When a form has one primary completion action, the mobile action band may be
fixed above `env(safe-area-inset-bottom)`. It is a raised surface with a top
edge and no shadow; the primary control is at least 52px tall and adjacent
secondary controls are at least 44px. The form body reserves the band's space
so content is never covered, and the desktop action layout remains unchanged.

Mobile choice controls use the existing bottom `Sheet` pattern. Sheet options
are at least 48px tall, expose selected state semantically, restore focus to
the invoking row after Escape or selection, and retain the shared focus trap
and scrim behavior. A feature should reuse its existing catalog/path panel
inside the sheet rather than create a second picker implementation.

Loading treatment follows the honest-liveness rule. A first-turn skeleton may
reserve the shape of the response only after a send or active first turn and
only until the first authoritative transcript item. It must disappear on an
authoritative frame, terminal/error/cancel state, session change, or pending
failure; it must never appear for an untouched empty session. Reuse the static
`Skeleton` widget, keep its accessible loading status and decorative bars, and
do not add shimmer, pulse, or other motion that implies live data.

Below 700px both speaker rows become a grid rather than a flex row: the avatar
and the speaker header share the first row and the prose spans the full pane
underneath them, instead of being indented into the avatar's column for its
whole height. Measured before this, agent prose got 260px of a 375px screen,
28 characters a line.

The sessions drawer renders the Rail flush inside the Sheet body (Sheet's
`bodyClassName`, §3): no inner surface box, no inner radius, because the sheet
already frames it.
