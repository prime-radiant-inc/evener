# Task-list changes card: checklist redesign

Date: 2026-07-31
Status: approved (design), pending implementation
Component: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx`
(+ `taskcard.module.css`, `taskCard.test.tsx`)

## Problem

The task_list mutation card renders its state changes as uppercase text flags
(`DONE #4`, `STARTED #5`) and shows the done/total count twice: once in the
tool-row summary (`Tasks · 4 / 5`) and again in the card head next to the
progress meter. It reads as debug output, not as a task list.

## Goal

Render task-list mutations as a clean, modern checklist: one progress bar,
one done-count, and touched tasks drawn as checkbox rows — finished tasks
checked and struck through, newly-started and newly-added tasks open. Static:
no write-in animation. Subtle semantic color on the checkbox glyphs only.
Approved decisions (2026-07-31, approach B "checkbox metaphor"):

- **Static checklist** — no motion when the card appears.
- **Subtle semantic color** — a scoped bend of the neutral-card house rule
  (`taskcard.module.css` header: "color-is-attention"), applied only to the
  checkbox glyphs; all text stays on the ink scale.
- **Keep cancelled rows and notes**, styled (✕ box + strikethrough; muted
  italic note lines).

## Design

### Card anatomy (top → bottom)

1. **Header line:** a single count — `4 of 5 done` (mono caption, mid ink) —
   beside the existing `Meter` bar, on one row. This is the only count and
   the only bar the card renders.
2. **Rows:** one per touched task (same `TouchedRow` set as today). A
   checkbox-square glyph leads, then the task label:

   | touch       | glyph                       | glyph color | label                    |
   |-------------|-----------------------------|-------------|--------------------------|
   | `done`      | box + check                 | `--success` | struck through, low ink  |
   | `started`   | box + rightward arrow       | `--accent`  | full ink                 |
   | `added`     | box + faint centered plus   | mid ink     | full ink                 |
   | `cancelled` | box + ✕                     | mid ink     | struck through, low ink  |

3. **Note:** an update's `notes` render as a small muted italic line under
   its row, indented to align with the label text, not the box.

### Summary dedupe

The tool-row summary changes from `Tasks · 4/5` to `Tasks`, so the count
appears exactly once (in the card head). The card keeps `autoExpand: () =>
true`, so the count is visible without a click; a collapsed row reads
`Tasks`, which is acceptable chrome.

### Checkbox glyphs

A new local `TaskCheck` component in
`src/panes/session/transcript/tools/` — deliberately **not** added to the
shared `ToolIcon` set, whose kinds are per tool family, not per status.
`TaskCheck` follows the same drawing grammar as `ToolIcon`/`chevron`:

- 16×16 viewBox, square box, `display: block` inline style;
- `stroke="currentColor"`, `strokeWidth` 1.75, round caps/joins, `fill="none"`;
- one path per touch state; color supplied by the row's CSS class, never
  hardcoded;
- rendered at ~15–16px (today's flags sit at 14px text size), so the boxes
  read as "big icons" against the label text;
- `aria-hidden="true"`, non-interactive (no button/checkbox role — the box
  is a picture of state, not a control).

Because the visible `DONE`/`STARTED` flag words disappear, each row carries a
visually-hidden status word (`<span className={CLASS.srOnly}>done</span>`
etc.) so screen readers keep the information the flags conveyed. The
`data-touch` attribute on the row stays, so existing tests and any DOM-level
consumers keep their hook.

### Data layer — unchanged

- `taskData.ts` (`parseTaskState`, `taskLabel`, `autoStartedTask`) and
  `mutationRows`/`finalUpdates` in `taskCard.tsx` are untouched: same touch
  set (`added`/`done`/`cancelled`/`started`), the authoritative auto-started
  "and now working on X" row, notes, and the `#<id>` label fallback when
  `item.raw` state is absent.
- Suppression rules unchanged: `view` and malformed non-mutations render
  nothing; a failed mutation renders no card (ToolCallItem's generic
  failed-row treatment shows the error).
- `Meter` widget reused as-is for the single bar.

### CSS

`taskcard.module.css` is restructured: the `.flag` class goes away; new
classes for `.check` (the glyph box, per-touch color modifiers), `.done` /
`.cancelled` label treatment (`text-decoration: line-through`,
`color: var(--ink-low)`), `.note` indentation, `.srOnly`, and the single
`.head` row (count + meter). The file-header comment is updated to document
the scoped semantic-color exception and why (user-approved, this spec).

## Testing

Rework `taskCard.test.tsx` (deterministic, per `docs/testing.md`):

- each touch renders a row with the correct `data-touch` and a checkbox glyph;
- strikethrough class present on `done`/`cancelled` labels, absent on
  `added`/`started`;
- exactly one progress count in the document (`Tasks ·` no longer appears);
- notes render under their row; auto-started row still renders;
- suppression of `view`/malformed calls and no-card-on-failure unchanged.

## Out of scope

- The tasks side panel (chrome) — untouched.
- The `Meter` widget and `ToolIcon` set — untouched.
- Any change to the daemon's task_list wire shape (`StateResult.State`).
- Write-in animation (explicitly rejected in favor of static).
