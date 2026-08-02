# Task-list mutation summaries

Date: 2026-08-02
Status: approved (design), pending implementation
Component: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx`
(+ `taskcard.module.css`, `taskCard.test.tsx`)

## Problem

The expanded `task_list` mutation card shows a checkbox and task text, but the
text does not explicitly identify itself as the task title and notes have no
label. When the tool row is collapsed, its summary is only `Tasks`, so the
reader loses the task changes that the expanded card shows.

## Goal

Make task changes scannable in both states:

- Show each task title beside its checkbox.
- Prefix rendered notes with `Notes:`.
- Replace the generic collapsed `Tasks` summary with one compact line for each
  task change, including its checkbox/status glyph and title.
- Keep collapsed and expanded rows driven by the same task-change data.

## Design

### Expanded card

`TaskCardRow` keeps its current checkbox/status glyph and renders the task
label beside it as the task title. The existing `description`-based label is
the task title for this renderer; no new wire field is introduced. If a row has
a note, render `Notes: <note>` below the title, aligned with the title rather
than the checkbox.

The existing struck-through treatment for completed and cancelled rows remains
unchanged. The existing status text for assistive technology remains present.

### Collapsed summary

The `task_list` renderer's `summary` callback returns a compact, newline-free
text summary made from the same `mutationRows(item)` result used by the expanded
body. Each rendered row contributes its status marker and title, in mutation
order. Multiple rows are joined with a separator suitable for the tool-row's
single-line layout; the row remains truncatable by the existing tool-row
layout.

The collapsed summary is changes-only. The expanded card continues to own the
progress count and meter, so the summary does not duplicate aggregate progress
information. If no valid mutation rows exist, existing suppression behavior
continues to remove the row. Failed mutations continue to use the generic
failed-row treatment.

### Data flow and edge cases

- Reuse `mutationRows(item)` rather than parsing arguments a second time.
- Preserve authoritative labels from `item.raw`, including the fallback to
  `#<id>` when the authoritative state is unavailable.
- Preserve authoritative auto-started rows and duplicate-ID final-update
  handling.
- Preserve the current touch set and suppression rules: valid append/update
  mutations render; `view` and malformed non-mutations remain suppressed; a
  failed mutation renders no task card.
- Notes remain attached to the corresponding expanded row. The collapsed
  summary identifies each changed task by title but does not duplicate notes or
  the progress meter.
- The summary must remain a string because the renderer registry and tool-row
  layout consume it as plain text.

## CSS

Use the existing task-card row and note styles. Add only the label treatment
needed for the literal `Notes:` prefix if the current note styling does not
provide sufficient separation. Keep the title and notes aligned with the
existing row text column and preserve the current responsive/truncation rules.

## Testing

Extend `taskCard.test.tsx` with deterministic assertions that:

- expanded mutation rows contain the task title beside the checkbox;
- notes render with the literal `Notes:` prefix;
- the collapsed summary contains the checkbox/status marker and title for an
  appended task;
- the collapsed summary contains every rendered row for a multi-task update,
  including an authoritative auto-started task;
- collapsed and expanded rows use the same labels and mutation ordering;
- existing suppression, failure, duplicate-ID, cancelled/done styling, and
  progress behavior remain unchanged.

Run the focused task-card tests, then the frontend type/build checks required by
the repository before implementation is reported complete.

## Out of scope

- The tasks side panel in `src/panes/session/chrome/`.
- Changes to the daemon task-list wire shape or task storage.
- Changes to the shared checkbox/icon widgets.
- Progress-count redesign or animation.
- Rendering notes in the collapsed summary.
