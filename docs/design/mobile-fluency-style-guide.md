# Mobile fluency style guide

**Source:** artifact `evener-fluent-prototype-v8a` v21 (`/tmp/evener-fluent-v8a.html`), philosophy `docs/superpowers/research/2026-11-22-mobile-philosophy.md`, spec `docs/superpowers/specs/2026-11-22-mobile-fluency-evolution.md`. Extracted 2026-11-23.

This is the visual contract for the fluency redesign: one product rendered at phone, tablet, and Duo interior sizes. Desktop stays as-is for now (see spec Phase 4 note).

## Tokens

All colors are dark-first, matching the live `cmd/evener-hub/frontend/src/styles/tokens.css`. The fluency pass does not add new colors; it removes backgrounds and borders that don't carry information.

| Token | Value | Use |
|---|---|---|
| `--surface-0` | `#111214` | page background |
| `--surface-canvas` | `#16181C` | wells (composer hover, deep wells) |
| `--surface-1` | `#1D2025` | cards, sheets (sparingly) |
| `--surface-inset` | `#191C21` | inset surfaces (activity chip pills) |
| `--hover-1` | `#252931` | hover |
| `--field` | `#23272E` | input fields (pill composer body) |
| `--edge` | `#333842` | hairlines — only where information separates |
| `--edge-strong` | `#4C535F` | divider handle (Duo hinge) |
| `--ink-hi` | `#F1F3F6` | primary text |
| `--ink-mid` | `#B2B7C0` | secondary text |
| `--ink-low` | `#9AA1AC` | tertiary/quiet text |
| `--attention` | `#F68F3C` | needs-answer state (dot only) |
| `--alive` | `#3DBB72` | working state (dot only) |
| `--danger` | `#EE5C61` | destructive (Stop button) |
| `--accent` | `#8A96FF` | the single accent: primary action, focus ring, send button |
| `--on-accent` | `#111214` | text on accent |
| `--font-sans` | `-apple-system, BlinkMacSystemFont, "SF Pro Text", system-ui, sans-serif` | all UI text |
| `--font-mono` | `ui-monospace, SFMono-Regular, Menlo, monospace` | tool output, evidence |

### Radii

- `--radius-control: 8px` — buttons, small controls
- `--radius-pane: 12px` — cards (question card, evidence inset)
- `--radius-panel: 16px` — bottom sheets (MenuSheet)
- `--radius-pill: 999px` — composer, search fields

### Shadows and blur

- `--shadow-card: 0 1px 2px #00000059` — cards at rest
- `--shadow-overlay: 0 0 0 1px var(--edge), 0 12px 40px #00000073` — floating sheets
- Composer pill: `0 4px 18px #00000055` + `backdrop-filter: blur(16px)`
- Floating header capsules: `0 2px 10px #00000030` + `backdrop-filter: blur(16px)`

## Chrome budget

The transcript owns the screen. At rest on phone (390×844):

| Part | Height |
|---|---|
| StackHost header | 44px |
| Whiteboard subtle line | 32px |
| Floating pill composer | ~50px (with safe-area padding) |
| **Total chrome** | **~126pt (15%)** |

On scroll down (>48px), the transcript header and whiteboard line collapse to floating capsules (back + ⋯), so chrome is effectively the pill composer alone. The board header never collapses — it stays sticky.

## Patterns

### Floating header (session transcript only)

- Header is `position: sticky` at rest; on scroll down >48px it becomes `position: absolute; top: calc(24px + env(safe-area-inset-top) + 6px)` with transparent background/border, title fades (`opacity: 0`), and back + ⋯ render as floating blur capsules (`backdrop-filter: blur(16px)`, `background: color-mix(in oklab, var(--surface-1) 88%, transparent)`, soft shadow).
- Scroll up 8px or reach top → header slides back to solid bar.
- Board header is always sticky and never collapses.
- Whiteboard line compacts with header (padding `6px 14px`, smaller type when collapsed).

### Floating pill composer

- No outer rectangle, no border-top, no hint row. Outer container is transparent and `pointer-events: none`; the pill itself is `pointer-events: auto`.
- Pill: `background: var(--field)`, `border-radius: var(--radius-pill)`, `backdrop-filter: blur(16px)`, `box-shadow: 0 4px 18px #00000055`, `max-width: 640px`, centered.
- Transcript scroll carries `padding-bottom: 72px` so the last turn slides under the pill.
- Focus ring: `border-color: var(--accent)` + `0 0 0 3px var(--accent-bg)`.

### Board (session list)

Preserves existing sidebar organization: **Live → Pinned → Projects → Idle → Archive.**

- Live: needs-first sort (needs answer, then working), dot + `8m · prime-radiant/evener`. No `needs answer`/`working` text badge — the dot and time carry it.
- Pinned: pinned sessions, same row shape.
- Projects: grouped by project (`Projects · prime-radiant/evener`).
- Idle: shows 8 immediately (no "Show more" tap), hint that more live in Archive.
- Archive: nav row → infinite scroll (6 at a time via scroll sentinel, `archiveVisible` state), search, no Prev/Next pagination.
- Group headers: flat `--surface-0` sticky with a 22% hairline bottom border, `14px 12px 6px` padding, uppercase 11px `--ink-low`.
- Rows: transparent background, `28%` hairline bottom border only, `12px 14px` padding.
- One search at top; no ⌘K hint text.

### Whiteboard

One subtle collapsed line at the scroll head — `— Whiteboard · 3 notes · 2 links —` — not a bar. Expands to your note / agent / links on tap. Compacts (smaller padding and type) when header collapses.

### Activity: single entry via ⋯ sheet

- Header ⋯ opens a bottom sheet (MenuSheet) with sections: Session (Model ▾, Context with meter, Effort, Work time, Queue), Activity (⌘/$/◷/☑ with counts, tap → ActivitySidebar tabs), Links.
- No chips-in-scroll for model/effort/context. Effort is settable on the settings page, not in chat chrome.
- Activity tabs are underline tabs (`border-bottom: 2px` active), never pills.

### Sidebar (tablet)

- Board 280px sidebar + conversation flex at 768×1024 (portrait) and 1024×768 (landscape).
- Collapse control: `◧` glyph on the interior divider — right edge of board when visible, left edge of conversation when hidden. Rotates 180° to indicate direction. No `<` / `☰` mismatch.
- Auto-collapse under 900px effective width (matches existing `MOBILE_QUERY` 899px gate).

### Duo

- Interior 840×600: same split as tablet with hinge math `min(380, max(280, round(w×0.36)))` + 1px divider + `DetailPaneWidthContext`, 600pt gate.
- Exterior: single phone stack (no Duo chrome), 580×800 cover.
- Both orientations.

## Bans

- No pills for model/effort/skills/plugins in the transcript scroll.
- No `needs answer`/`working` text badges on board rows.
- No ⌘K hint text in the UI.
- No outer rectangle around the composer.
- No double collapse buttons (one ◧ at a time, on the interior divider).
- No gigantic Duo sizes (1264×840 is out; 840×600 is in).

## Verification

- Screenshots at 390×844 (phone), 768×1024 (tablet portrait), 1024×768 (tablet landscape), 840×600 (Duo interior).
- Chrome budget: header + whiteboard + composer ≤126pt at rest on phone.
- Content scrolls behind composer pill (padding-bottom 72px on scroll).
- Header collapses only on transcript scroll, never on board.
- `impeccable detect --json` clean on changed targets.
