# Serf Web Hub — Design System & Style Guide (v2)

Status: **current**. This is the wave-2 rewrite's design system: tokens, fonts, and a widget
library under `cmd/serf-hub/frontend/src/widgets/`, built as React function components + CSS
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

---

## 1. Direction (the design law)

> Reproduced verbatim from the wave-2 plan. If this section and the plan ever disagree, the
> plan is source of truth and this section is stale — file it as a doc bug.

**Palette (dark, default):**

```css
--surface-0: #0E1116;  /* app background — deep neutral ink        */
--surface-1: #161B22;  /* panes, rail                              */
--surface-2: #1D242E;  /* raised: menus, dialogs, hover            */
--edge:      #262E3A;  /* hairline borders — replaces shadows      */
--ink-hi:    #E6EAF0;  /* primary text                             */
--ink-mid:   #9AA4B2;  /* secondary text                           */
--ink-low:   #5C6673;  /* placeholders, disabled, timestamps       */
--attention: #E8A33D;  /* a human is needed — THE amber. Nothing else may be amber. */
--alive:     #3FB68B;  /* agent working/streaming. Muted sea-green, never acid.     */
--danger:    #E5484D;  /* failure/destructive                       */
--accent:    #6CA0F5;  /* focus ring, selection, links — steel-blue */
```

Each semantic family gets `-bg` and `-edge` companions mixed in `tokens.css` via
`color-mix(in oklab, var(--attention) 15%, var(--surface-1))` — no new hex literals.
Light theme: same names, values flipped around `#F5F7FA`/`#FFFFFF` surfaces with the same four
semantic hues darkened one step for contrast (validated ≥ 4.5:1 for text, 3:1 for UI).

**Type:** IBM Plex Sans (UI + prose; display = weight 600, tracking −1%) and IBM Plex Mono
(code, tool output, paths, timings). Scale (px): 12 caption / 13 ui / 14 body / 16 pane-title /
20 page-title, line-heights 1.5 body, 1.3 titles. Mono never used for chrome labels (retired
pattern stays retired).

**Space/shape:** 4px grid (`--space-1..9` = 4..64); radius 4 (controls) / 8 (panes, dialogs);
depth = `--edge` borders + surface steps, shadows only on floating layers (menu, dialog, toast).

**Motion:** default none. Allowed: attention onset (one 200ms ease-out color/edge transition),
streaming caret blink, dialog/menu 120ms fade-scale. Forbidden: idle pulses, skeleton shimmer
loops on live data, anything that animates during silence (honest-liveness rule).

**Signature — the cadence instrument (`<Cadence>` widget):** one component rendered everywhere
a session appears (tree row, pane header, mobile card): a state dot plus a 24×10px activity
trace of the last ~60s of frame arrivals as vertical ticks that fade with age. Working = fresh
ticks (alive token); quiet = ticks visibly aging to `--ink-low`; needs-you = dot and trailing
edge in attention amber; failed = danger. It never animates on its own — it only re-renders
when frames actually arrive, so a busy agent shows a dense fresh trace and a stalled one shows
honest decay. Props: `{state: "idle"|"working"|"needs-you"|"failed"|"ended", frameTimes:
number[], now: number}`.

---

## 2. Tokens as shipped

`src/styles/tokens.css` is the single source of every color, type, space, radius, and motion
value in the app. Consumers only ever write `var(--name)` — no component CSS branches on
theme; the light-theme block (`[data-theme="light"]`) redeclares every color token under the
same name, and `token-contract.test.ts` (§4) fails CI if a token exists in one theme's block
but not the other's.

**Color** — surfaces/ink (`--surface-0/1/2`, `--edge`, `--ink-hi/mid/low`) plus the four
semantic families (`--attention`, `--alive`, `--danger`, `--accent`), each with `-bg` (15% mix
into the surface) and `-edge` (40% mix into the hairline border) companions — 19 color tokens
total, all declared identically-named in both theme blocks. Light-theme semantic hues are
WCAG-computed (not eyeballed): `--attention: #9D6513`, `--alive: #2B7D60`, `--danger: #DB1F25`,
`--accent: #1668EF`, each clearing ≥4.5:1 against both `#F5F7FA` and `#FFFFFF`.

**Type** — `--font-sans` / `--font-mono` (IBM Plex, self-hosted from the `@ibm/plex-sans` /
`@ibm/plex-mono` npm packages, latin1 subset, imported by relative `url()` straight from
`node_modules` — no binaries committed); `--font-weight-regular/medium/semibold` (400/500/600);
`--tracking-display` (−0.01em, display weight only); `--font-size-caption/ui/body/pane-title/
page-title` (12/13/14/16/20px); `--line-height-body/title` (1.5/1.3).

**Space & shape** — `--space-1` through `--space-9` (4/8/12/16/24/32/40/48/64px — IBM Carbon
Design System's own spacing progression, adopted because it's the only well-known scale that
exactly fits the plan's stated endpoints (4, 64) and step count (9) while staying on the 4px
grid); `--radius-control` (4px), `--radius-pane` (8px).

**Motion** — `--motion-duration-attention` (200ms), `--motion-duration-overlay` (120ms),
`--motion-easing-standard` (`ease-out`). See §5 for the budget these back.

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
| **Cadence** | `{state: "idle"\|"working"\|"needs-you"\|"failed"\|"ended"; frameTimes: number[]; now: number}` | The signature widget — see §1. Pure (no timers, no `Date.now()`); ticks render as SVG `<rect>`s, age→opacity in 4 buckets (15s each, half-open `Math.floor` boundaries); needs-you tints the freshest ticks amber too ("trailing edge"), not just the dot. |
| **Chip** | `{children: ReactNode; tone?: "neutral"\|"attention"\|"alive"\|"danger"; onRemove?: () => void}` | Small labeled pill; `onRemove` renders a remove button, `aria-label` derived from string children or `"Remove"`. |
| **Badge** | `{count: number; tone?: "neutral"\|"attention"\|"alive"\|"danger"}` | Numeric count indicator, caps display at "99+". |
| **StatusDot** | `{state: CadenceState}` | Just the dot (imports `CadenceState` from Cadence, doesn't redeclare it) — for tighter contexts than Cadence's full trace; carries its own accessible name since nothing else labels it standalone. |
| **Meter** | `{label: string; value: number; max: number; tone?: "neutral"\|"attention"\|"alive"\|"danger"}` | `role="meter"`; `label` is required (not optional as an early sketch had it) since role=meter needs an accessible name and a Meter can't ship without one. Fill width via a `--fill` style custom property, not an inline style rule. |
| **Skeleton** | `{lines?: number}` (default 3) | Static bars, no shimmer (honest-liveness rule) — announces "Loading" once for AT; bars themselves are decorative. |
| **EmptyState** | `{title: string; hint?: string; action?: ReactNode}` | `action` is optional (an early plan sketch showed it required; a pane with nothing actionable — e.g. a read-only empty log — is an ordinary case, and every sibling slot-style prop this wave is optional, so this was kept optional as the more consistent, more correct shape). |
| **Card** | `{children: ReactNode}` | Passive raised/bordered container. |
| **Input** | `{value: string; onChange; placeholder?; disabled?; type?: "text"\|"password"\|"email"\|"search"\|"number"\|"tel"\|"url"; id?; name?}` | Controlled only; labeling is the consumer's job via `<label htmlFor>`. |
| **Textarea** | `{value: string; onChange; placeholder?; disabled?; autoGrow?: boolean; rows?; id?; name?}` | `autoGrow` counts literal `"\n"` occurrences, not wrapped lines. |
| **Select** | `{value: string; onChange; options: {value; label}[]; disabled?; id?; name?}` | Native `<select>`, restyled — no custom listbox (Combobox covers richer cases). |
| **Switch** | `{checked: boolean; onChange: (checked: boolean) => void; disabled?; label: string}` | `role="switch"` on a real `<button>`, not a styled checkbox; `label` is required and always-visible, wired via `aria-labelledby`. |
| **KeyHint** | `{keys: string[]}` | One `<kbd>` per key, "+"-separated; the literal key name `"Mod"` renders as ⌘ on Apple platforms, `Ctrl` elsewhere. |
| **Combobox** | `{options: T[]; onQuery; onPick; renderOption?; "aria-label"?; "aria-labelledby"?}` (generic over `T extends {id; label}`) | ARIA 1.2 combobox-with-listbox-popup; real focus never leaves the input. `aria-label`/`aria-labelledby` forward to BOTH the input and the popup listbox (fix-wave: the listbox had no name of its own — see §4) — they're two roles describing one picker, sharing one label source. Debounces `onQuery` 150ms. Never traps focus. |
| **Menu** | `{trigger: ReactNode; items: {id; label; onSelect; disabled?}[]}` | Trigger + popup; roving tabindex among items (skipping disabled), no typeahead. Popup `role="menu"` gets `aria-labelledby` pointing at the trigger `<button>`'s own id (fix-wave — see §4). Traps focus (`FocusScope trap`). |
| **Dialog** | `{open; onClose; title; children; footer?}` | Modal: centered, 120ms fade-scale, Escape/scrim-click close, trapped + restored focus. Shares its whole contract with Sheet via the internal `OverlayPanel`. |
| **Sheet** | `{side?: "right"\|"bottom"; open; onClose; title; children; footer?}` | Same contract as Dialog (shared `OverlayPanel`); only geometry/slide-in animation differs. |
| **FocusScope** | `{trap?: boolean; children}` | The focus-management primitive Dialog/Sheet/Menu build on: moves focus in on mount, restores on unmount; traps Tab/Shift+Tab when `trap`. Does not (yet) set `inert` on anything outside the scope — see §4. |
| **Tooltip** | `{label: string; children: ReactNode}` | Hover/focus-triggered, 300ms delay, hidden on touch via CSS. `aria-describedby` wired via `cloneElement` onto a single-element child — works for a native element or any widget that forwards a ref + spreads rest props (Button/IconButton both do, since the fix-wave in §4). |
| **Toast** + `useToasts()` | `useToasts(): {push: (kind, text) => void}`; `<Toast/>` takes no props | Module-singleton queue (`useSyncExternalStore`), mounted once near the app root. 5s auto-dismiss, true pause/resume on hover (tracks remaining time, doesn't restart the full window — fix-wave, see §4). |
| **PaneScaffold** | `{title; cadence?; actions?; footer?; children}` | The standard pane chrome: header (title + cadence slot + actions) + scrollable body + optional footer. Most-copied layout primitive in the app. |
| **CodeBlock** | `{text: string; language?: string; showLineNumbers?: boolean}` | Mono block with a copy button (renders a real `Button` internally); no syntax highlighting (YAGNI this wave). |
| **Markdown** | `{source: string}` | `marked` → DOMPurify-sanitized HTML → `innerHTML`; fenced code renders through CodeBlock's stylesheet; links open in a new tab with no opener access. |
| **DiffBlock** | `{unified: string}` | Per-line tone on already-diffed text (additions alive, deletions danger); does not compute a diff itself. |
| **Tree** | `{nodes: T[]; onActivate; onToggle; renderRow}` (generic over `T extends {id; children?; expanded?}`) | Keyboard-navigable (`role="tree"`), roving tabindex, Up/Down/Right/Left/Enter. Fully controlled — `renderRow(node, {depth, expanded, hasChildren, toggle, activate})` owns each row's visible content; Tree owns structure/ARIA/keyboard path only. |
| **VirtualList** | `{count; estimateSize; renderRow; ref?: Ref<VirtualListHandle>}` | Wraps `@tanstack/react-virtual`; `ref` exposes `{scrollToIndex}` via the React 19 ref-as-prop pattern (not a `forwardRef` wrapper). Sizes come from `estimateSize` alone, no `measureElement`. |

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
four independent checks:

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
   `meter` (danger/attention fill), `diffblock` (add/del tints), `dialog` (danger footer). A
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

Every mechanism above is poison-tested against hand-written snippets proving both what it
catches and what it must not flag (see the test file itself) — not just asserted to work.

---

## 5. Motion budget

Default is none. The only motion this app plays, all on `--motion-easing-standard`
(`ease-out`):

- **Attention onset** (`--motion-duration-attention`, 200ms) — a state crossing into
  needs-you. Cadence's dot, StatusDot, and Switch all use it for their own state-driven color
  transitions.
- **Overlay fade-scale** (`--motion-duration-overlay`, 120ms) — Dialog, Sheet, and Menu's
  open/close.

Forbidden: idle pulses, shimmer loops on live data, anything that animates during silence (the
honest-liveness rule — a "working" indicator that looks identical whether the agent is
streaming or hung is worse than no indicator). Every widget with motion of its own respects
`prefers-reduced-motion: reduce` (currently: Cadence, Dialog, Menu, Sheet, StatusDot, Switch) —
collapses to instant, no exceptions.

---

## 6. Copy rules

Sentence case for all UI copy; no ALL-CAPS. Active-voice labels ("Save changes", not "Changes
saved" or "Save Changes"). Mono is for machine text only — code, tool output, paths, commands,
identifiers — never chrome labels, captions, or any text a human authored. (This tripped up
even this wave's own gallery scaffold once: three caption labels shipped on `--font-mono` in
the foundation task and were caught and fixed in wave-close review — see git history for
`gallery-section.module.css` and `theme-flip.module.css`. If it happened once, watch for it.)

---

## 7. Known gaps (documented, not fixed — wave-close adjudication)

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
  trade than leaving admittedly-redundant code running. Left as-is, flagged for a more careful
  pass that can validate actual AT behavior on a real touch+screen-reader device, not reasoned
  about in the abstract.
