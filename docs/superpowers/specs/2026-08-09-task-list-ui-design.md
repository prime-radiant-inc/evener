# Task List Panel Redesign — Live Rows + Focus Groups

**Date:** 2026-08-09
**Status:** Approved direction; awaiting implementation plan
**Scope:** `cmd/evener-hub/frontend/src/panes/session/chrome/` — the tasks Sheet and desktop pane (`TasksPanelBody`). The transcript's inline `task_list` cards (`transcript/tools/taskCard.tsx`) are out of scope.

## Problem

The tasks panel wastes the space it occupies and hides the information readers want.

- An expanded task renders each field as a caption-over-mono pair at full width. Five fields cost about ten lines before the notes list starts.
- A collapsed row shows only a glyph and the description. It carries no signal about when the task last changed or what its latest update said.
- The wire sends `created_at` / `updated_at` / `completed_at` on every task (`agent/task/task_store.go`), but `taskData.ts`'s parser drops them, so no timestamp can be shown anywhere.

## Decisions (from mockup review, 2026-08-09)

The user reviewed four mockups rendered with a real 20-task session list ([interactive mockup](2026-08-09-task-list-ui-mockup.html), [final screenshot](2026-08-09-task-list-ui-mockup.png), session `local:0343wE3LB14m5xoC2CBMiD`) and chose:

1. **Live rows** (variant A): two-line collapsed rows that show the latest update inline.
2. **Focus groups** (variant C): group rows by status; collapse settled tasks behind one line.
3. **Prompt as session-style inline disclosure**: the label `Prompt`, the chevron after the label, then as much of the prompt as fits on the line.
4. **Prompt renders as markdown**, folded and unfolded. Not preformatted text. (The current panel renders the prompt verbatim in a `<pre>`; this changes.)

## Data changes

`chrome/taskData.ts`:

- `TaskRow` gains `createdAt?: string`, `updatedAt?: string`, and `completedAt?: string` (ISO strings from the wire, kept as strings; formatting is a view concern). They are optional in the type because the parser stays tolerant: it carries them when present, never drops a row for lacking them. `created_at` / `updated_at` are always present on the real wire; `completed_at` exists only for done tasks. Views omit time displays for absent fields.
- `parseRow` carries them across. `created_at` / `updated_at` are always present on the wire; `completed_at` exists only for done tasks.
- The file's header comment currently records that these fields are intentionally not carried. Update it: the redesign consumes them, and the reference to the legacy panel's field set no longer applies.

No wire, daemon, or Go changes. The fields already arrive in every task-list response.

A new small helper, `chrome/taskTime.ts`:

- `relativeTime(iso, now)`: `now`, `Nm ago`, `NH ago`, `Nd ago`.
- `absoluteTime(iso)`: `Aug 8, 22:03` (locale month-day, 24-hour).
- No existing frontend helper does this; the rail and details panel format durations, not timestamps.

## Layout

### Panel body header

Above the groups, one compact summary row: the `Meter` widget (neutral tone) plus `16/20 done`, sourced from `model.tasks`. Rendered only when `model.tasks` is non-null. The trigger button already shows this aggregate; the body repeats it so an open panel reads as a whole.

### Groups

Rows render in three groups, in this order:

1. **In progress** — tasks with `status === "in_progress"`.
2. **Open** — tasks with `status === "open"`.
3. **Done · settled N** — tasks with `status === "done"` or `"cancelled"`, behind a single collapsed-by-default disclosure line. "Settled" covers both because the distinction still shows per row (✓ vs ✕, struck title).

Within each group, rows keep wire order (task id order). Empty groups render nothing — no header, no placeholder. A list that is entirely settled shows only the settled disclosure line.

Group headers are quiet uppercase caption rows with the count inline (`IN PROGRESS 1`). The settled group header is a `Disclosure` (`widgets/disclosure`) with id `${sessionRef}\0settled-group` — the same NUL-separator idiom as `taskDisclosureId`, in a namespace task ids cannot reach (ids are numbers).

### Collapsed rows

**Live groups (in progress, open)** — two lines:

- Line 1: disclosure chevron, status glyph (unchanged `STATUS_GLYPH` / `STATUS_TONE` mapping), description (medium weight, single line, ellipsis), and the relative `updatedAt` time right-aligned in `--ink-low` with the absolute time as its tooltip.
- Line 2: only when the task has notes — the label `latest` in `--ink-low`, then the most recent note as plain text, single line, ellipsis, full note in the tooltip.

**Settled group** — one line: chevron, glyph, description, relative time. No excerpt line; history should cost one line per task. Done rows render dimmed. Cancelled rows keep the ✕ glyph and add a struck-through, `--ink-low` description so "won't happen" stays distinct from "finished" inside the shared group.

### Expanded row

The disclosure body holds four parts, in order. Omit a part when its data is absent rather than rendering an empty shell.

1. **Meta strip** — one wrapping line of caption-label/mono-value pairs: `type` (always), `reasoning` (when set), `depends` (when non-empty, rendered `#16 #17`). Replaces the stacked `dl` rows for these fields.
2. **Timestamps line** — `created <absolute>`, `updated <relative>` (omitted when equal to created), `completed <relative>` (done only). Relative times carry absolute tooltips. One line, caption size, wrapping allowed.
3. **Prompt** — an inline disclosure in the session's own pattern (ThinkBlock/SteeringItem grammar): the label `Prompt`, the rotate-on-open chevron after the label, then a single-line preview filling the rest of the line with ellipsis. **The prompt renders as markdown in both states**: the preview renders the first non-empty line through the `Markdown` widget constrained to one line (no block margins, `nowrap`); the open body renders the full prompt through the same `Markdown` widget. Omitted when the prompt is blank. Disclosure id: the task's disclosure id plus a `prompt` suffix.
4. **Updates** — the notes list as a left-rail timeline: a 2px `--edge` rail, one dot marker per note, the latest note's dot in `--alive` (a glyph-level status accent, not an attention signal; see Testing for the token-contract route). Header `Updates · N`. When the task has no notes: `No updates yet.` in `--ink-low`.

### What does not change

- The trigger button, its badge, and `triggerLabel`.
- Fetch, re-fetch-on-push, stale/daemon-gone/unsupported/empty handling in `TasksPanelBody`, including Try again and the toasts.
- Per-session disclosure id scoping (`taskDisclosureId`); task rows keep their existing ids.
- `STATUS_GLYPH` and `STATUS_TONE`: open ○ neutral, in_progress ● alive, done ✓ neutral, cancelled ✕ neutral. The design system's color-is-attention rule stands; grouping and weight carry the hierarchy, not new hues.

## Component structure

All in `cmd/evener-hub/frontend/src/panes/session/chrome/`, styles in `taskspanel.module.css`:

- `taskData.ts` — `TaskRow` gains the three timestamp fields; parser carries them.
- `taskTime.ts` (new) — `relativeTime`, `absoluteTime`. Pure, unit-tested.
- `taskGroups.ts` (new) — `groupTasks(rows): { inProgress, open, settled }`. Pure, unit-tested. Presentational only; never reorders within a group.
- `TasksPanel.tsx` —
  - `TaskRowView` restructured: two-line summary for live rows, one-line for settled; `dim` and `cancelled` presentation flags passed down from the group renderer.
  - `TaskDetails` replaced by `TaskExpandedBody`, composed from `TaskMetaStrip`, `TaskTimestamps`, `TaskPromptDisclosure`, and `TaskNotesTimeline`. `TaskPromptDisclosure` is hand-rolled on `isDisclosureOpen`/`toggleDisclosure` exactly the way `SteeringItem.tsx` does (the shared `Disclosure` widget renders its chevron before the summary content; the session inline grammar this design copies puts the chevron after the label) and renders through the `Markdown` widget.
  - `TasksPanelBody`'s list render maps over `groupTasks(rows)` instead of `rows`; adds the body header row.
  - The old `detailList`/`detailRow`/`detailLabel`/`detailValue`/`detailPrompt` CSS leaves with the `dl`; new classes follow the same token discipline (no color literals; token-contract test gates this in CI).

## Edge cases

- **No in-progress tasks** — the In progress header is omitted; Open leads.
- **All tasks settled** — only the collapsed settled line shows. The reader opens it to audit history.
- **Empty list, unsupported source, daemon gone, failed fetch** — exactly as today; this redesign changes none of those states.
- **`updated_at` equal to `created_at`** (never touched after append) — the timestamps line omits `updated`; the collapsed row still shows the relative creation time, since the row's time is `updatedAt` and the two are equal.
- **Long single-word prompt or note** — `overflow-wrap: anywhere` on the open prompt body; ellipsized single-line previews cannot overflow by construction.
- **Relative times** compute at render time from the fetch's data. They refresh on the next refetch (push or Try again); no timer is added.

## Testing

Follow `docs/testing.md` and the repo's frontend gates.

- `taskData.test.ts` — rows parse `created_at` / `updated_at` / `completed_at`; missing `completed_at` stays undefined; malformed timestamps do not drop the row (carry them as-is; formatting tolerates invalid input by falling back to the raw string).
- `taskTime.test.ts` (new) — boundary cases: <1 minute, 59 minutes, 23 hours, days; invalid input falls back.
- `taskGroups.test.ts` (new) — grouping, stable order within groups, cancelled lands in settled.
- `TasksPanel.test.tsx` — extend the existing suite:
  - collapsed live row shows the latest-note excerpt; settled row does not;
  - cancelled row renders ✕ + struck description inside the settled group;
  - settled group defaults to collapsed and remembers toggles per session;
  - expanded row shows meta strip, timestamps line (fields omitted correctly), updates timeline with N notes, and "No updates yet.";
  - prompt disclosure shows the one-line markdown preview collapsed and the full markdown body open; absent prompt renders no disclosure;
  - body header shows `Meter` + count only when `model.tasks` is non-null.
- Gates: `npx biome check --write` on touched files, then `make test-web`; `make test-web-browser` on this Chrome-capable host. The token-contract test forbids new color literals and gates `--attention`/`--alive`/`--danger` to allowlisted widget stylesheets. `taskspanel.module.css` is not a widget stylesheet, so its one semantic reach (`--alive` on the latest-note dot) earns an exact-path exception in `token-contract.test.ts` following the `taskcheck.module.css` precedent (kata entry, exact path, glyph-level color only; every piece of text in the panel stays on neutral ink).

## Out of scope

- Transcript inline task cards (`taskCard.tsx`).
- Per-note timestamps: the wire's notes are plain strings; inventing times for them is fabrication. The timeline's "latest" marker uses position, not time.
- Live-ticking relative times.
- Virtualizing the list. Twenty rows render fine; revisit only if real sessions outgrow it.
