# Evener Web Hub — Design System & Style Guide (v3)

Status: **current editorial design system**. The canonical
system for the React + CSS Modules frontend under `cmd/evener-hub/frontend/src/`.
The real `/dev/widgets`, `/dev/type`, and `/dev/surfaces` galleries show it in both themes.

The approved [editorial-instrument design](../superpowers/specs/2026-09-09-tufte-webui-design.md)
supersedes the Beautiful UI aesthetic mandate for palette, typography, enclosure and elevation.
It does **not** replace the interaction law, widget APIs, semantic color meanings,
accessibility floors or honest-liveness rules. Source now implements tokens, shared
widgets, typography, inline tool/delegate hierarchy, shell, forms, composer and ledgers.
Some surfaces inherit shared styling rather than a separate redesign; see
[source coverage](#editorial-source-coverage). Release verification is recorded separately
under [acceptance and limits](#acceptance-and-limits); this guide is not a release certificate.

**Provenance and attribution.** The 2026-08-13 system adapted
[Beautiful UI](https://www.beautifului.dev), MIT License, Copyright (c) 2026 Shane Levine.
Its licensed attribution remains at `cmd/evener-hub/frontend/LICENSES/beautiful-ui.txt`.
The interaction patterns and widget inventory below retain that history. Earlier visual
systems (the server-rendered hub, wave-2, Fjord/Ledger, Beautiful UI) are recorded in git
and [decisions.md](decisions.md), not repeated here as competing current mandates.
Source Serif 4 is self-hosted from `@fontsource-variable/source-serif-4` 5.3.0,
SIL Open Font License 1.1; the package notice and license are reproduced below.

---

## Design model: an editorial instrument

**Conversation supports understanding; evidence supports verification; controls support
intervention.** This is our design argument, inspired by Tufte, not a quotation or a claim
of measured usability improvement. Typography and spacing serve that division of work.
The reader should be able to follow a claim, inspect what happened, and act without
losing the conversation that made the evidence relevant.

- **Put evidence beside claims.** A tool row leads with authored intent, then the exact
  action and target, then native code, diff, output or structured evidence. Intent explains
  why; it does not replace the action or prove the result. This adds hierarchy rather than
  another generated summary for the reader to trust.
- **Disclose detail, not consequences.** Collapse bulky evidence to keep a conversation
  readable, but leave failure and actionable status legible. Disclosure inspects in place;
  Open navigates independently. Neither action should accidentally trigger the other.
  Progressive disclosure costs an extra action to inspect detail; it must not conceal
  the reason to inspect it.
- **Keep different facts distinct.** Delegate lifecycle, attention, immutable launch receipt
  and child-authored report answer different questions. A successful launch is not finished
  work; an earlier report is not current activity. Prefer an honest unknown to an inferred
  success. This is less reassuring than a single green badge, but more useful for supervision.
- **Preserve the path into evidence.** Nested transcript inspection retains the owner and
  immediate parent. Back returns to that parent, not an arbitrary focused pane or the root;
  when the exact originating surface survives, retain its identity even if a session and a
  read-only transcript share a ref. Context bookkeeping is the cost of letting a reader
  investigate without rebuilding their place. See
  [Open routing](../../cmd/evener-hub/frontend/src/panes/session/transcript/openTranscript.tsx)
  and [retained origins](../../cmd/evener-hub/frontend/src/shell/workspace.ts).
- **Quiet the frame, not the controls.** Fine rules and aligned columns replace decorative
  enclosure. Fields still look editable, overlays retain boundaries, focus remains visible,
  and semantic color distinguishes attention, activity, failure and selection with text or
  shape as well as hue. Minimalism is not permission to hide an affordance.
- **Optimize readable density, not maximum density.** Prose gets a reading face and measure;
  operations use compact sans; machine evidence uses mono. Larger prose occupies more space
  but separates reading from scanning. Shared size and width preferences remain available.
  Phones reflow speaker rows, controls and evidence rather than shrinking a desktop page;
  wide evidence scrolls within its surface instead of widening the page or shrinking targets.

The provenance corrections and nested navigation/focus repairs change behavior. Folding,
disclosure persistence, keyboard rules, preferences and semantic hue roles are retained
contracts, not inventions of this redesign. Other surfaces gain a coherent shared system
without necessarily gaining a new workflow; the coverage section identifies that boundary.

**Review questions.** Can a reader distinguish intent from execution and a receipt from a
report? Is failure visible before expansion? Can they open evidence without toggling its
disclosure, then return to the immediate context? Are editable fields and keyboard focus
unmistakable in both themes? At phone widths and larger text settings, do prose, controls
and wide evidence remain usable without hiding content? Review the real components and
workflows, not only token swatches.

## Inline tools and delegates

**Tools stay in the conversation.** Use the shared
[ToolRow](../../cmd/evener-hub/frontend/src/panes/session/transcript/ToolRow.tsx)
grammar: authored intent first, then action/target and compact result metadata.
Descriptors supply content and native evidence, not independent row layouts.
Keep disclosure separate from file/transcript opening; retain existing folding and
disclosure persistence. Evidence stays in its native code, diff, output or structured
renderer rather than becoming a second prose summary.

**Delegates are durable collaborators, not launch-tool status.** The inline
[delegate renderer](../../cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx)
and [row model](../../cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.ts)
take lifecycle from the owning thread's stable delegate projection. Keep the immutable
launch receipt separate: a completed launch call does not prove the delegate completed.
Without authoritative owner state, only an explicitly in-flight launch proves activity;
otherwise show unknown rather than infer success or liveness from the receipt or child
transcript. Attention is a separate signal from lifecycle.

Use the current run's start for elapsed time, not the old launch receipt. Child-authored
words remain distinct from machine metadata and may remain visible on resumption; they
do not establish current lifecycle. Expanded activity shows the five most recent authored/activity
items, with full history behind Open transcript. Omit unavailable counts and timing;
distinguish unavailable activity from an empty loaded transcript. These are provenance
rules, not permission to fabricate a summary or rewrite stored evidence.

## Editorial source coverage

The [approved surface scope](../superpowers/specs/2026-09-09-tufte-webui-design.md#other-surfaces)
and [real-component SurfaceGallery](../../cmd/evener-hub/frontend/src/dev/SurfaceGallery.tsx)
provide the coverage references. Direct source work covers transcript tool/delegate rows,
shell/rail/mobile structure, welcome/spawn/settings/provider forms, composer/queue/AskDock/
attachments, and activity/task/detail ledgers. This extends beyond a token-only restyle.

Documents, read-only transcripts, menus, dialogs and notices inherit shared Markdown,
CodeBlock, PaneScaffold, palette, radius and overlay styling; this is not a separate
interaction rewrite for each surface. Gallery examples and deterministic tests support
source coverage, not a claim that every workflow, theme, viewport or assistive technology
has passed browser acceptance.

## Acceptance and limits

Release snapshot, 2026-09-10: the implementation is integrated and
[PR #1124](https://github.com/prime-radiant-inc/evener/pull/1124) is an open draft, not
merged-main acceptance. Source review, automation and hands-on review are separate evidence:

- **Source and automation:** source changes were reviewed and integrated. The latest
  unchanged browser suite and build passed; the final integrated twelve-gate run and its
  full-output and power-state audits passed. An earlier writer scroll-guard failure was not
  reproduced and remains unresolved; later passes do not explain it.
- **Hands-on panel:** phone and workspace reviewers endorsed their reviewed surfaces.
  The same tools reviewer still owes the mixed-origin native retest; its rejection remains
  open. Source regression tests for nested Open/Back repairs do not substitute for that retest.
- **Preview:** the detached preview still serves an older build; the authorized refresh
  has not happened. It is not evidence for the final integrated implementation.

No perceptual color/leading A/B study, real-device, Safari, assistive-technology or live-provider
validation is claimed. Automated contrast and geometry checks constrain the design; they do
not establish reading comfort or usability. The accessibility gaps in §8 remain documented.

## 1. Direction (the design law)

**Reading before chrome.** Warm neutral paper/ink, not sepia decoration. Dark remains the
default; `system`, `light`, and `dark` preferences keep their existing behavior. A page is
flat, a field is visibly editable, and a floating layer retains a boundary and depth.
Use whitespace, aligned columns and fine rules instead of nested boxes and tinted bands.

**Three faces with distinct jobs.** Source Serif 4 for reading prose and editorial page
headings; Inter for controls, labels, tables and compact operations; JetBrains Mono for code,
commands, paths and machine identifiers. Comparable operational figures use tabular Inter,
not monospace. Keep authored text and machine evidence visibly distinct.

**Geometry is not decoration.** Preserve the shared reading/wide measure, alignment,
font-size preferences, timestamp rail, safe-area/keyboard rules and 899px mobile boundary.
Do not shrink targets or hide overflow as a substitute for containing wide evidence.

**Color is meaning.** Attention = a human/action is needed; alive = active work;
danger = failure/destruction; accent = links, focus and selection. No decorative status
hues, invented activity, speculative progress bars or color-only distinctions.

**Motion is evidence.** Default none. Preserve measured cadence and reduced-motion
behavior; nothing animates during silence. See §5 for the exact budgets and exceptions.

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

**Color** — the warm neutral roles below are implemented in both theme blocks. All existing
token names are retained. The table is a reference; `tokens.css` is authoritative.

| Role | Dark | Light |
|---|---|---|
| Page `--surface-0` | `#191918` | `#FAF9F6` |
| Wells/rail `--surface-canvas` | `#1D1D1B` | `#F1F0EB` |
| Evidence/structural fill `--surface-1` | `#232320` | `#FCFBF8` |
| Floating layer `--surface-2` | `#232320` | `#FCFBF8` |
| Evidence inset `--surface-inset` | `#20201E` | `#F4F3EE` |
| Hover / selected `--hover-1/2` | `#2B2B28` / `#33332F` | `#F0EFE9` / `#E5E4DD` |
| Editable field `--field` | `#292926` | `#F4F3EF` |
| Fine / strong edge | `#34342F` / `#51514A` | `#DDDCD4` / `#B7B6AC` |
| High / medium / low ink | `#F2F1EB` / `#B0AFA6` / `#99998F` | `#252521` / `#5F5F57` / `#6D6D64` |

Card, InspectorCard and PaneScaffold share the page ground rather than manufacturing raised
surfaces. Evidence and overlays still have their own grounds. Fine borders are structure,
not text. Low ink clears 4.5:1 on page and evidence surfaces: dark 6.12/5.48, light 4.96/5.05.
Readable secondary metadata uses medium ink; low ink stays placeholders, disabled and timestamps.

The four semantic hues keep their names, roles, 15% OKLab `-bg` washes and 40% `-edge`
companions. Text uses `-ink`, never the bare hue. Light alive/danger/accent text colors are
`#12763B` / `#C51D23` / `#0064C2`, darkened to preserve AA on their own warm tinted grounds.
The tests compute every semantic pairing, including each hue's own wash, at ≥4.5:1.
Tooltips retain their inverted mini-palette. ANSI roles and values are unchanged.

Diff notation remains independent of semantic status. Dark add/delete stays `#19251A` /
`#170B17`; light is `#E9F4EE` / `#F5EAF0`. Background contrast against the page stays within
1.05–1.2, content ≥4.5, marker ≥3, with additions lighter in grayscale and explicit +/− signs.
The PWA manifest and HTML theme-color are synchronized to the dark page `#191918`.

**Type** — three shared families, no CDN font requests:

- `--font-sans`: `"Inter Variable"` plus system sans fallbacks. Controls, labels, operational tables.
- `--font-prose`: `"Source Serif 4 Variable", Georgia, "Times New Roman", serif`. Reading and page headings.
- `--font-mono`: `"JetBrains Mono Variable"` plus system monospace fallbacks. Machine evidence.

Inter and JetBrains Mono remain wired to their package Latin variable WOFF2 files. Source Serif 4
uses the package's actual exported normal CSS and `wght-italic.css` for real italics; all assets
are served locally by Vite/the built frontend, with package Unicode-range subsetting.

The existing `--font-size-caption/ui/body/pane-title/page-title/display` ramp remains
12/13/15/18/22/28px. **`--font-size-prose: calc(18px * var(--font-scale))` is new**, declared on
`body` alongside that ramp so S/M/L/XL (0.9/1/1.1/1.25) scale reading text too. Phone body remains
16px; editable phone controls use `max(16px, var(--font-size-body))`, even under S. The serif
reading step is 18px at M in both viewports. Weights are 400/500/600; leading body/ui/title is
1.6/1.4/1.25; display tracking remains −0.02em.

Markdown defaults to the prose face/size while retaining `--prose-font-size` and `--markdown-ink`
caller hooks. Inline code stays JetBrains Mono at 0.86em with a quiet underline; fenced code
explicitly keeps the mono face. Markdown tables use Inter at body size, tabular figures,
wrapping labels and fine horizontal rules. PaneScaffold and EmptyState headings use the serif;
buttons, fields, dialog labels and other compact operational headings do not inherit it globally.
`faces.test.ts` pins the new contract and the unchanged operational sans rules; `/dev/type`
shows all three faces (including real serif italic), the ramp and both measures.

**The eyebrow recipe.** `--tracking-eyebrow` (0.06em) replaced `--tracking-micro` and the four
other tracking values that were in use, and it is now THE uppercase tracking in the app. The
recipe it backs is one rule with no variants: `--font-size-caption`, `--font-weight-medium` (or
semibold where a header band wants more), `--ink-mid` or darker, `text-transform: uppercase`,
`letter-spacing: var(--tracking-eyebrow)`, at most two words. It is reserved for short labels inside a
page (RecommendationCard's kicker and existing rail/settings section labels); InspectorCard
and Table/DiffTable headers now remain sentence-case. An eyebrow is NEVER a pane title: a PaneScaffold title
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
grid; unchanged by the re-theme). Radii are restrained: `--radius-chip` 3px,
`--radius-control` 4px, `--radius-pane` 4px and `--radius-pill` 999px for switch tracks.
Structural Card/InspectorCard/PaneScaffold and tables are square; floating layers and fields
retain their token radii. Existing widget dimensions and touch targets are unchanged.

**Vertical rhythm** is four named steps over that same grid, so the transcript has a vocabulary
for how far apart two things sit: `--rhythm-line` (4px, inside one item, an intent line above
its call), `--rhythm-item` (8px, between items in a run of tool calls), `--rhythm-group` (16px,
between a run and the next speaker header, and above a turn footer) and `--rhythm-exchange`
(24px, above a user message). `src/styles/rhythm.test.ts` pins each step to the site that names
it.

**`--session-measure` is the app's one reading measure**: 44rem (704px; actual character count depends on the face and preference), declared on `<body>` and raised to 64rem under
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

**Elevation** — `--shadow-card: none` in both themes. Keep the token for existing consumers,
not as a reason to reintroduce raised structure. Card uses padding and page ground;
InspectorCard uses a sentence-case label and fine property rules; PaneScaffold uses a page
heading and separating rule without a rounded outside border or tinted header. Table/DiffTable
use horizontal rules rather than framed grids. Do not remove a field's affordance along with
card decoration: Input, Select, Textarea and shared picker triggers keep a strong 1px border,
`--field` fill and `--shadow-inset-field`, with unchanged focus and phone sizing.

`--shadow-overlay` still embeds a 1px strong-edge ring and soft per-theme shadow for menus,
popovers, toasts, dialogs and sheets. Do not double that same edge with another border.
`--shadow-color` supports sheets' directional variants. Tooltips retain their inverted palette
and boundary. Flattening structure never means erasing a modal or interactive layer.

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
| **InspectorCard** | `{title; properties: {key; label; value; options?; onChange?}[]}` | Property inspector: sentence-case section label, hairline rows, editable rows compose Select, read-only values in mono. Ported from Beautiful UI's Fine-tune inspector. |
| **Card** | `{children: ReactNode}` | Passive flat section; padding and page ground, no shadow or outside border. |
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
RecommendationCard's kicker and existing rail/settings section labels. InspectorCard and
Table/DiffTable column labels are sentence-case, not eyebrows. Three-word labels were the tell that the rule was being
broken, so "Agents & models" became **"Agent setup"**.

Never an eyebrow: buttons, sentences, and above all **titles**. A pane title is the page's own
heading, sentence-case in `--font-prose` at `--font-size-pane-title`, semibold, `--ink-hi` (§2); a session title is
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
It is the reference implementation for the two idioms below, with Settings → Providers &
credentials (`panes/settings/sections/credentials/InstanceSheet.tsx`) the reference for the
sheet-as-editor form; any future page with the same shape (several sibling lists, plus per-item
detail and actions) should reuse them rather than inventing a third layout.

**One list at a time, chosen by a page-level SegmentedControl.** Each sibling collection becomes
a segment; the segment labels carry the counts (`Installed (7)`, `Marketplaces (3)`), and the
per-section headers — title plus count — are deleted, because duplicating that identity under
the segment control is noise. The default segment is the one the user maintains most (Installed,
not Browse). Switching segments is a page-level navigation act: page-scoped overlays owned by
the outgoing segment close (see the sheet rule below), while per-segment UI state that is
expensive to rebuild (the browse tree's expansion and its lazy catalog cache) is lifted to the
page so it survives the round trip.

**Rows are single tappable targets; the detail sheet is the item's editor.** A collection row
carries identity and status only — `StatusDot`, name, state chips, one mono meta line
(`@ marketplace · v1.2.0`) — and a trailing chevron; it is one full-width `<button>`, so the
whole row is the target on desktop and touch alike. A row NEVER grows a trailing cluster of
small action buttons (the pre-redesign installed row had four): everything about the item lives
in its **detail sheet**, a `Sheet` with `side="right"` on desktop and `side="bottom"` at the
mobile breakpoint (chosen via `useIsMobile`, the same source the shell uses), `size="wide"`
when it carries a form. The sheet is the item's editor, not an inspector (2026-09-07): every
authored, editable fact renders as a prefilled form field in place — `FormRow` over `Input` /
`Select`, one column — with a dirty-gated **Save** as the footer's primary `Button`, and
renaming is editing the name field. Read-only facts keep the meta-table idiom below.
Display-only content a row does not carry (a plugin's catalog description) is pulled lazily
through the browse cache — one store-level entry per marketplace, so re-open is free.
A separate `Dialog` is reserved for write-only secret entry (an API key, a credential JSON) and
multi-step flows (OAuth); it never exists to edit a field the sheet could show. Binary state
(Enabled, Auto-upgrade) is a `Switch` row that applies immediately, disabled while its RPC is
in flight; the destructive action keeps its `ConfirmDialog` even though that nests a second
modal over the sheet — `OverlayPanel` instances stack in DOM order, each traps and restores
focus down the stack, and its `preventDefault` on Escape is what keeps the settings pane's own
document-level Escape handler from closing the pane out from under an open overlay. Closing a
sheet with unsaved edits discards them silently; the sheet reseeds only when a different item
opens or its own save lands, so another client's refresh never clobbers a draft.

**The meta table idiom.** Inside a detail sheet, read-only facts render as label/value rows: a
fixed-width (96px) caption-color label column, values in the UI font, and `var(--font-mono)` for
anything machine-shaped — versions, sources, paths — truncating with ellipsis rather than
wrapping. This is the same vocabulary as the list row's meta line, one zoom level up.

**A detail sheet is only as alive as its subject.** The detail sheet reads its entity from the
store rather than a prop snapshot, so cross-client changes land while it is open; when the
entity disappears from the store (its own Remove completing, or another client's), the sheet
closes itself instead of offering actions on a ghost — except when the disappearance is the
sheet's own rename landing, where the section re-selects the item under its new name and the
sheet stays open — and a failed Remove keeps the sheet and dialog open for retry. Segments own
their overlays: switching away closes the sheet, coming back does not reopen it.

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


## Source Serif 4 license notice

From `@fontsource-variable/source-serif-4` 5.3.0, distributed unmodified.

```text
Google Inc.

This Font Software is licensed under the SIL Open Font License, Version 1.1.
This license is copied below, and is also available with a FAQ at:
http://scripts.sil.org/OFL


-----------------------------------------------------------
SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007
-----------------------------------------------------------

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide
development of collaborative font projects, to support the font creation
efforts of academic and linguistic communities, and to provide a free and
open framework in which fonts may be shared and improved in partnership
with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The
fonts, including any derivative works, can be bundled, embedded,
redistributed and/or sold with any software provided that any reserved
names are not used by derivative works. The fonts and derivatives,
however, cannot be released under any other type of license. The
requirement for fonts to remain under this license does not apply
to any document created using the fonts or their derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright
Holder(s) under this license and clearly marked as such. This may
include source files, build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the
copyright statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting,
or substituting -- in part or in whole -- any of the components of the
Original Version, by changing formats or by porting the Font Software to a
new environment.

"Author" refers to any designer, engineer, programmer, technical
writer or other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining
a copy of the Font Software, to use, study, copy, merge, embed, modify,
redistribute, and sell modified and unmodified copies of the Font
Software, subject to the following conditions:

1) Neither the Font Software nor any of its individual components,
in Original or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy
contains the above copyright notice and this license. These can be
included either as stand-alone text files, human-readable headers or
in the appropriate machine-readable metadata fields within text or
binary files as long as those fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font
Name(s) unless explicit written permission is granted by the corresponding
Copyright Holder. This restriction only applies to the primary font name as
presented to the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font
Software shall not be used to promote, endorse or advertise any
Modified Version, except to acknowledge the contribution(s) of the
Copyright Holder(s) and the Author(s) or with their explicit written
permission.

5) The Font Software, modified or unmodified, in part or in whole,
must be distributed entirely under this license, and must not be
distributed under any other license. The requirement for fonts to
remain under this license does not apply to any document created
using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are
not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT
OF COPYRIGHT, PATENT, TRADEMARK, OR OTHER RIGHT. IN NO EVENT SHALL THE
COPYRIGHT HOLDER BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
INCLUDING ANY GENERAL, SPECIAL, INDIRECT, INCIDENTAL, OR CONSEQUENTIAL
DAMAGES, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
FROM, OUT OF THE USE OR INABILITY TO USE THE FONT SOFTWARE OR FROM
OTHER DEALINGS IN THE FONT SOFTWARE.
```
