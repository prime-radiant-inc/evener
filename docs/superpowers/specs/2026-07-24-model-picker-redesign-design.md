# Model Picker Redesign

**Date:** 2026-07-24
**Status:** Approved pending Jesse's spec review
**Surfaces:** `widgets/modelCatalog` (`ModelCatalogPanel`, `ModelCatalog`), `widgets/combobox`, `widgets/popover`, `panes/session/chrome/ModelSwitch`, `panes/spawn/ModelField`

## Problem

The current model picker (a `Combobox` inside `ModelCatalogPanel`, opened as a
popover from a chip trigger) has five UX defects Jesse identified:

1. A Cancel button nobody needs (Escape and outside-click already close it).
2. Unavailable providers hide behind an "N providers unavailable" toggle
   button instead of being visible in place.
3. The search input opens empty; the previously selected value is only on the
   trigger chip, so you can't see-then-edit what's currently set.
4. The list only appears after typing or pressing an arrow key; opening the
   picker shows an empty input over nothing.
5. Scrolling anywhere — including inside the list — dismisses the popover
   (`Popover` closes on window capture-phase scroll; `ModelSwitch`'s
   hand-rolled popover does the same).

## Decisions (made in visual brainstorming, in Jesse's words where short)

- Toured five variants; explored two-pane provider-rail (B); final call:
  **"one grouped list... keep desktop and mobile the same."**
- Search **flattens/filters in place** rather than entering a mode.
- Recents: **first group in the list**, not chips, not dropped.
- Unavailable providers: **dim small-text in-place lines** carrying the
  diagnostic inline, positioned where their section would be.
- **No Cancel button.**
- **Same rendering at every width** — no mobile fork, no Sheet variant.

## Design

### Panel layout (`ModelCatalogPanel`, rebuilt)

One column, in serf tokens, inside the existing `Popover` panel:

```
┌──────────────────────────────────────────────┐
│ [anthropic/claude-fable-5]  (selected text)  │  ← input, focused
├──────────────────────────────────────────────┤
│ RECENT                                       │  ← group head (caption size)
│  Claude Fable 5      anthropic · $5/$25 · 200k │
│  GPT-5.5             oai-work · $2/$12 · 400k │
│ ANTHROPIC                                    │
│  Claude Fable 5    ✓        $5/$25 · 200k    │
│  Claude Opus 4.8            $5/$25 · 200k    │
│ OAI-WORK                                     │
│  GPT-5.5                    $2/$12 · 400k    │
│  ollama — connection refused. Is it running? │  ← dim, small, in place
│  openrouter — no API key configured          │
└──────────────────────────────────────────────┘
```

- The list renders **immediately on open**, fully populated (no keystroke or
  arrow needed). It scrolls internally (`overflow-y: auto`, max-height capped
  so the popover clamps within the viewport).
- **Recent** is the first group (from the `/api/models?diagnostics=1`
  envelope's `recent` array, already fetched by `catalogClient`). Recent rows
  show the provider in the small-text meta since the group mixes providers.
  When `recent` is empty the group is omitted entirely.
- Provider groups follow, in the server's provider-sorted order (existing
  `withGroupHeads` behavior). The row for the **currently selected** model
  carries a ✓ and is scrolled into view on open.
- **Unavailable providers** (from `diagnostics`) render as dim
  (`--ink-low`), caption-size, non-interactive lines in list order after the
  available groups: `provider — message. hint`. The "N providers unavailable"
  toggle button and its hidden list are deleted.
- **No Cancel button.** The panel's only dismissals are pick, Escape, and
  outside-click.

### Input behavior

- On open, the input is **pre-filled with the current qualified value**
  (`provider/model`, or empty for harness default), **fully selected**, and
  **focused** — the first character typed replaces it wholesale
  (`input.select()` on mount).
- Typing **filters the grouped list in place** using the existing
  `filterCatalog` (matches display name, raw model id, provider) +
  `withGroupHeads` recomputation. Recent filters too. Unavailable-provider
  lines stay only while their provider name matches the query; otherwise they
  filter out like any non-match.
- Clearing the query restores the full list. No debounce needed for display —
  filtering is local over the already-loaded catalog (the 150ms `onQuery`
  debounce in `Combobox` existed for remote queries; local filtering happens
  per-keystroke).

### Keyboard

Existing ARIA 1.2 activedescendant pattern (focus never leaves the input):

- **↑/↓** move the highlight through pickable rows (recent + models; group
  heads and unavailable lines are skipped — they are not options).
- **Enter** picks the highlighted row. When nothing is highlighted and the
  query exactly equals one row's qualified id or label, Enter picks it.
- **Escape** closes the picker without changing the value (consumed, not
  bubbled to an enclosing Dialog, as `Combobox` does today).
- **Home/End** jump to first/last pickable row.

### Dismissal / scrolling

- `Popover` gains `closeOnScroll?: boolean` (default `true`, so Menu and all
  other consumers keep today's behavior). The picker passes `false`: the
  window-scroll and resize close listeners are skipped. Outside-click and
  Escape still close.
- The list's own internal scrolling therefore never dismisses; neither does
  page scroll behind the popover. (Accepted trade-off: with `closeOnScroll`
  off, a page scroll can visually detach the panel from its trigger. Both
  call sites anchor to controls that don't scroll mid-interaction — spawn
  form field, session status row — and losing anchor alignment beats losing
  the picker mid-scroll.)
- `ModelSwitch` **drops its hand-rolled popover** (its own
  Escape/outside-click/scroll listeners, `modelswitch.module.css` popover
  classes) and renders the shared `Popover` with `closeOnScroll={false}`,
  same as `ModelCatalog`. One popover implementation, two call sites.

### Component/API changes

| Unit | Change |
|---|---|
| `widgets/popover` | Add `closeOnScroll?: boolean` prop (default true). |
| `widgets/modelCatalog` `ModelCatalogPanel` | Rebuild per this spec. Props change: `onCancel` is **removed**; add `value: string` (current qualified value, for pre-fill + ✓ + scroll-into-view). Keeps `loading`, `error`, `catalog`, `onPick`. |
| `widgets/modelCatalog` `ModelCatalog` | Passes `value` through; stops rendering Cancel plumbing. Trigger (chip + chevron) unchanged. |
| `widgets/combobox` | The generic widget stays for other consumers **unchanged**; `ModelCatalogPanel` stops using it and owns its input+listbox directly (the grouped list with non-option rows, pre-fill/select-on-open, and always-open-list semantics no longer match `Combobox`'s contract of options-only rows and closed-until-typed popup). If after implementation `ModelCatalogPanel` was `Combobox`'s only consumer beyond the gallery, leave `Combobox` in place — other pickers (directory picker) build on it. |
| `ModelSwitch` | Replace hand-rolled popover with shared `Popover closeOnScroll={false}`; pass current qualified value; drop Cancel wiring. |
| `ModelField` (spawn) | Pass current value; drop Cancel wiring. No other change. |

### Loading / error states

Unchanged in substance: while `loading`, the panel shows the input
(pre-filled, focused) over a skeleton list; on `error`, the input over the
existing `role="alert"` error line. Both still dismiss via Escape/outside
click.

### Accessibility

- Input keeps `role="combobox"` with `aria-expanded="true"` whenever the
  panel is open (the list is always shown), `aria-activedescendant` tracking
  the highlighted row.
- The list is `role="listbox"`; pickable rows are `role="option"` with
  `aria-selected` on the current-value row; group heads are
  `role="presentation"` text; unavailable lines are plain text within the
  scroll container but outside the listbox's option set.
- The ✓ current marker is supplemented by `aria-selected="true"` (not
  color/glyph alone).

### Testing

- `modelCatalog.test.tsx` rewritten: list renders populated on open;
  input pre-filled + fully selected (assert `selectionStart===0`,
  `selectionEnd===value.length`); first keystroke replaces the value; filter
  narrows groups and drops empty group heads; recent group renders first and
  omits when empty; unavailable lines render dim in place with message+hint;
  ✓ on current row; ↑↓ skip heads and unavailable lines; Enter picks;
  Escape closes without onChange; **no Cancel button in the DOM**.
- `popover.test.tsx`: `closeOnScroll={false}` keeps the panel open through a
  window scroll event; default still closes.
- `ModelSwitch.test.tsx`: shared-Popover migration keeps disabled-while-busy
  gating and optimistic-close-on-pick; scroll inside the list doesn't close.
- `ModelField.test.tsx`: updated for removed Cancel.
- Live e2e check on both call sites (spawn pane + session status row) before
  close.

## Out of scope

- The TUI model picker.
- Trigger styling (chip + chevron stays as-is).
- Server-side catalog/recents changes (`/api/models` envelope already carries
  everything needed).
