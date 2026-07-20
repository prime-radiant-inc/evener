# Web Rewrite Wave 2 — Style Guide + Widget Library (M2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** The complete visual system for the new workspace — tokens, fonts, the widget library,
a living gallery — under one written style guide, with the color-is-attention rule
machine-enforced.

**Architecture:** CSS Modules + custom-property tokens; React function components; every widget
keyboard-accessible and theme-aware by construction. Widgets live one-per-directory under
`src/widgets/<name>/` (index.tsx + <name>.module.css + <name>.test.tsx) so parallel streams
own disjoint files. A dev-only `/dev/widgets` gallery renders every widget in every state.

**Tech Stack:** React 19, CSS Modules, vitest+RTL. New deps (justified): `@ibm/plex-sans` and
`@ibm/plex-mono` (dev-time font source; woff2 copied into `src/assets/fonts/`, self-hosted —
the CSP forbids external fonts).

## Global Constraints (every task)

- **Design direction is law** (§Direction below): all colors via `var(--…)` tokens; NO chromatic
  literal outside `tokens.css`; chroma only through the four semantic families (attention,
  alive, danger, accent). The token-contract test (Task 1) enforces this — never weaken it.
- Dark is default; light theme via `:root[data-theme="light"]` overrides of the same tokens.
- Every interactive widget: keyboard operable, visible `:focus-visible` ring (accent token),
  `prefers-reduced-motion` respected, labels/aria per its test.
- No new npm deps beyond the two font packages without a written reason.
- TS strict; CSS Modules only; no inline styles except where a value is genuinely dynamic
  (e.g. a meter's width) — and then via style custom properties (`style={{"--fill": …}}`).
- Sentence case for all UI copy; no ALL-CAPS; active-voice labels ("Save changes").
- TDD per widget: behavior tests (RTL), not snapshot dumps. Pristine output.
- Wave-1 rules still bind: commits per green cycle; parallel streams own disjoint files.

## Direction (the design spec — derive everything from this)

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

## Locked widget APIs (later waves import these; streams must not rename)

All from `src/widgets/<dir>/index.tsx`, re-exported by controller-owned `src/widgets/index.ts`:

- `Button {variant?: "primary"|"quiet"|"danger"; size?: "sm"|"md"; icon?; children; onClick;
  disabled?; type?}` · `IconButton {label: string (required aria); icon; …Button}`
- `Chip {children; tone?: "neutral"|"attention"|"alive"|"danger"; onRemove?}` ·
  `Badge {count: number; tone?}` · `StatusDot {state: CadenceState}`
- `Cadence {state; frameTimes; now}` (signature — §Direction)
- `PaneScaffold {title; cadence?; actions?; footer?; children}` · `Card` · `EmptyState {title;
  hint?; action?}` · `Skeleton {lines?}` (static, no shimmer)
- `Dialog {open; onClose; title; children; footer?}` · `Sheet {side?: "right"|"bottom"; …Dialog}`
  · `Menu {trigger; items: MenuItem[]}` · `Tooltip {label; children}` ·
  `Toast` + `useToasts() {push(kind, text)}`
- `Input` · `Textarea {autoGrow?}` · `Select` · `Switch` · `Combobox {options; onQuery;
  onPick; renderOption?}` · `KeyHint {keys: string[]}`
- `Tree {nodes: TreeNode[]; onActivate; onToggle; renderRow}` (roving tabindex, aria-tree) ·
  `VirtualList {count; estimateSize; renderRow}` (wraps @tanstack/react-virtual) ·
  `Meter {value; max; tone?}` · `Markdown {source}` (marked+DOMPurify, mono code blocks) ·
  `CodeBlock {text; language?}` · `DiffBlock {unified: string}` · `FocusScope {trap?; children}`

Task boundaries (streams): T1 foundation (tokens/fonts/gallery/contract-test + Button+Cadence
exemplars) — sequential, blocks the rest. Then three parallel streams: T2 primitives
(Chip/Badge/StatusDot/Input/Textarea/Select/Switch/KeyHint/Meter/Skeleton/EmptyState/Card),
T3 overlays (Dialog/Sheet/Menu/Tooltip/Toast/FocusScope/Combobox), T4 data
(Tree/VirtualList/Markdown/CodeBlock/DiffBlock/PaneScaffold). T5 gallery completeness +
design-system.md v2 + wave gate (sequential, after merges).

---

### Task 1: Foundation — tokens, fonts, contract test, gallery scaffold, Button + Cadence exemplars

**Files:**
- Replace: `cmd/serf-hub/frontend/src/styles/tokens.css` (full token system per §Direction)
- Modify: `cmd/serf-hub/frontend/src/styles/global.css` (font-face, base text/bg wiring)
- Create: `src/assets/fonts/` (Plex woff2 subset: sans 400/500/600 + mono 400/500, latin),
  `src/widgets/button/{index.tsx,button.module.css,button.test.tsx}`,
  `src/widgets/cadence/{index.tsx,cadence.module.css,cadence.test.tsx}`,
  `src/widgets/index.ts` (controller-owned barrel; streams DO NOT edit — they report exports
  for the controller to add at merge),
  `src/dev/WidgetGallery.tsx` (+ route wiring in App.tsx dev-only),
  `src/styles/token-contract.test.ts`
- Modify: `package.json` (add @ibm/plex-sans @ibm/plex-mono as devDeps + a copy script or
  direct import from node_modules via vite asset handling — pick the simplest that keeps
  woff2 out of git if importable, else commit subset files and justify)

**Interfaces:**
- Consumes: Wave-1 scaffold.
- Produces: every token name in §Direction; `Button`/`Cadence` as the canonical widget pattern
  (structure, css-module conventions, test style) all stream tasks must mirror; the gallery
  page other tasks register into via `src/dev/gallery-sections/<name>.tsx` (one file per
  widget dir — stream-owned, so no gallery merge conflicts); the token-contract test.

- [ ] **Step 1: token-contract test (RED).** `src/styles/token-contract.test.ts`: reads every
  `.module.css` + `global.css` under src (vite `import.meta.glob` with `as: "raw"`, or fs in
  vitest) and asserts: (a) no hex/rgb/hsl/oklch literal outside `tokens.css`; (b) no
  `--attention|--alive|--danger|--accent` var used in a file whose name isn't on the
  semantic-use allowlist the test carries (starts: cadence, button[danger], chip, badge,
  statusdot, meter, toast, dialog[danger footer]); (c) `tokens.css` dark block and light block
  declare identical custom-property name sets. Watch it fail against the current stub tokens.
- [ ] **Step 2: tokens.css + global.css + fonts** per §Direction exactly (all names, all
  values, the color-mix companions, both themes, font-faces with `font-display: swap`).
  Contract test GREEN. `npm run build` shows woff2 emitted to webassets.
- [ ] **Step 3: Button (exemplar).** TDD: variants render distinct classes; disabled blocks
  onClick; focus-visible class present; danger uses only tokens. CSS module uses ONLY tokens.
- [ ] **Step 4: Cadence (signature, exemplar for canvas-free SVG widgets).** TDD: given
  frameTimes spanning 60s, renders ticks with age-derived opacity buckets; state maps to the
  right token class; no timers of its own (pure render from props); `now` prop controls decay
  (deterministic tests). SVG, ≤ 24×10 viewBox, title attr for a11y.
- [ ] **Step 5: gallery scaffold.** `/dev/widgets` route (React lazy, excluded from prod build
  via `import.meta.env.DEV` guard); renders sections from `import.meta.glob` over
  `src/dev/gallery-sections/*.tsx`; add sections for button + cadence (every variant × state ×
  both themes side-by-side via a theme-flip wrapper).
- [ ] **Step 6: gate + commit(s).** `make test-web` green; `npm run build` green; commit
  "webui: design tokens, Plex fonts, token-contract test, Button+Cadence exemplars, gallery".

---

### Tasks 2-4: widget batches (three parallel streams — dispatch per stream worktree)

Each stream: own worktree off the Task-1 tip; owns ONLY `src/widgets/<its dirs>/**` and
`src/dev/gallery-sections/<its names>.tsx`; mirrors the Button/Cadence pattern (file layout,
tokens-only CSS, test style); TDD per widget (behavior: keyboard interaction, aria, controlled
props, tone/token mapping); adds a gallery section per widget; NEVER edits the barrel,
tokens.css, or another stream's dirs. Batch contents + API contracts: §Locked widget APIs.
Per-widget acceptance floor: renders all documented states; keyboard path tested (Tab/Enter/
Space/Arrows as applicable); focus-visible visible; reduced-motion honored where animated;
gallery section shows every state in both themes.

Merge protocol (controller): review per stream → merge → controller adds the stream's exports
to `src/widgets/index.ts` in the merge commit → `make test-web` gate between merges.

---

### Task 5: Gallery completeness + design-system.md v2 + wave gate

- [ ] Gallery renders every §Locked widget; a completeness test asserts one gallery section
  per `src/widgets/*` directory.
- [ ] Rewrite `docs/web-ui/design-system.md` (v2): §Direction verbatim as the canonical spec,
  widget inventory table, the color-is-attention rule + its enforcement test, motion budget,
  copy rules; mark the old content superseded (git history preserves it).
- [ ] Wave gate: `make lint && make test && make test-web`, `make build-hub`; screenshot pass
  of the gallery in both themes at 390/900/1440 widths (chrome skill) — visual self-critique
  against §Direction; fix what reads templated or inconsistent; wave report.
