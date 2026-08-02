# Task-list mutation summaries Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show task titles and labeled notes in expanded task-list mutation cards, and show the same changed tasks with checkbox/status markers in collapsed tool summaries.

**Architecture:** Keep `mutationRows(item)` as the single source of truth for task mutation rows. Add the literal `Notes:` prefix in `TaskCardRow`, and derive the renderer's collapsed summary string from those same rows using a compact text representation of each row's status and label. Do not change the daemon wire shape, shared widgets, or the task side panel.

**Tech Stack:** React 19, TypeScript, Vitest, Testing Library, CSS Modules, Biome.

## Global Constraints

- Work only in `cmd/serf-hub/frontend/src/panes/session/transcript/tools/` and the implementation-plan/spec documentation unless tests reveal a directly related issue.
- Preserve `mutationRows`, `finalUpdates`, authoritative `item.raw` labels, auto-start rows, suppression, failure handling, and progress rendering.
- Keep `ToolRendererDescriptor.summary` a plain `string`; do not widen the renderer registry contract.
- Notes render in expanded rows only; do not add notes or progress counts to collapsed summaries.
- Tests must be deterministic and follow `docs/testing.md`; do not use provider credentials or network access.

---

### Task 1: Add failing coverage for titles, notes, and collapsed summaries

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx`
- Read: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx`
- Read: `cmd/serf-hub/frontend/src/panes/session/transcript/ToolRow.tsx`

**Interfaces:**
- Consumes: Existing `taskItem`, `renderItem`, `TaskCardRow`, and `ToolRow` test IDs.
- Produces: Failing assertions that specify the new user-visible contract without changing production code.

- [ ] **Step 1: Add an expanded-row title assertion.**

Use an append fixture whose task description is a recognizable title and assert the row contains it beside the task check. Keep the existing append test and add an explicit assertion that the title is rendered in the row containing `data-testid="task-check"`.

```tsx
const row = screen.getAllByTestId("task-card-row")[0]!;
expect(within(row).getByText("build the thing")).toBeTruthy();
expect(within(row).getByTestId("task-check")).toBeTruthy();
```

- [ ] **Step 2: Change the note assertion to require the literal `Notes:` prefix.**

In the existing note test, assert the displayed text is exactly `Notes: superseded by #5` and retain the class/column assertions on the note element.

```tsx
const note = screen.getByText("Notes: superseded by #5");
expect(note.className).toContain("note");
expect(note.parentElement?.className).toContain("rowText");
```

- [ ] **Step 3: Add a collapsed append-summary assertion.**

Render an append mutation with one task and inspect `data-testid="tool-row-summary"`. The summary must contain the status marker for an added task and the title. Use the existing `TaskCheck` status vocabulary (`added`) in the expected text contract rather than relying on an SVG's inaccessible internals.

```tsx
const summary = screen.getByTestId("tool-row-summary");
expect(summary.textContent).toContain("+ build the thing");
```

- [ ] **Step 4: Add a multi-row collapsed-summary assertion.**

Use the existing authoritative auto-start fixture. Assert the collapsed summary contains both final mutation labels in order, including the auto-started task, and that the expanded rows still contain the same labels in the same order.

```tsx
const summary = screen.getByTestId("tool-row-summary");
expect(summary.textContent).toContain("✓ fourth");
expect(summary.textContent).toContain("→ fifth");
expect(summary.textContent!.indexOf("fourth")).toBeLessThan(summary.textContent!.indexOf("fifth"));
```

- [ ] **Step 5: Run the focused tests and verify the new assertions fail.**

Run from `cmd/serf-hub/frontend`:

```bash
npm exec vitest run src/panes/session/transcript/tools/taskCard.test.tsx
```

Expected: existing tests may pass, but the new `Notes:` and collapsed-summary assertions fail because notes have no prefix and the descriptor summary is currently `Tasks`.

- [ ] **Step 6: Commit the failing tests.**

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx
git commit -m "test: specify task mutation summary titles"
```

---

### Task 2: Implement shared task-row summary and labeled notes

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskcard.module.css` only if the existing note styling needs a targeted prefix treatment
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx`

**Interfaces:**
- Consumes: `TouchedRow`, `TaskTouch`, `mutationRows(item)`, `TaskCardRow`, and `ToolRendererDescriptor.summary(item): string`.
- Produces: A summary helper with a concrete signature such as `function taskMutationSummary(item: ItemModel): string`, returning one plain-text compact summary for every row returned by `mutationRows(item)`.

- [ ] **Step 1: Define the compact status marker map.**

Add a local constant beside `TOUCH_WORD` in `taskCard.tsx`:

```tsx
const TOUCH_SUMMARY_MARK: Record<TaskTouch, string> = {
  added: "+",
  done: "✓",
  cancelled: "×",
  started: "→",
};
```

These are text equivalents of the existing checkbox/status glyphs for the collapsed string; do not add SVG or React markup to the registry summary.

- [ ] **Step 2: Implement the summary helper from `mutationRows`.**

Add:

```tsx
function taskMutationSummary(item: ItemModel): string {
  const rows = mutationRows(item) ?? [];
  return rows.map((row) => `${TOUCH_SUMMARY_MARK[row.touch]} ${row.label}`).join(" · ");
}
```

This must preserve mutation-row order and automatically include authoritative labels and auto-started rows. Do not parse `argumentsJSON` again.

- [ ] **Step 3: Prefix expanded notes.**

In `TaskCardRow`, change the note span from `{row.note}` to `Notes: {row.note}` while retaining its existing `note` class and row-text placement.

- [ ] **Step 4: Register the dynamic summary.**

Change the descriptor from:

```tsx
summary: () => "Tasks",
```

to:

```tsx
summary: taskMutationSummary,
```

Keep `icon`, `body`, `autoExpand`, and `suppress` unchanged.

- [ ] **Step 5: Run the focused tests and verify they pass.**

```bash
npm exec vitest run src/panes/session/transcript/tools/taskCard.test.tsx
```

Expected: PASS, including existing suppression, failure, duplicate-ID, auto-start, checkbox, strikethrough, and progress tests.

- [ ] **Step 6: Commit the implementation.**

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskcard.module.css cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx
git commit -m "feat: show task titles in mutation summaries"
```

---

### Task 3: Run frontend verification and review the diff

**Files:**
- Verify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx`
- Verify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx`
- Verify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskcard.module.css`

**Interfaces:**
- Consumes: Task 2's dynamic summary and expanded note rendering.
- Produces: Verified implementation with no unrelated changes and a clean working tree apart from intentional deliverables.

- [ ] **Step 1: Run the focused task-card test file again.**

```bash
cd cmd/serf-hub/frontend
npm exec vitest run src/panes/session/transcript/tools/taskCard.test.tsx
```

Expected: PASS.

- [ ] **Step 2: Run the related transcript renderer tests.**

```bash
npm exec vitest run src/panes/session/transcript/toolRenderers.test.ts src/panes/session/transcript/toolRowGrammar.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx
```

Expected: PASS with no summary-contract regressions.

- [ ] **Step 3: Run the repository frontend checks.**

```bash
npm run typecheck
npm run build
```

Expected: both commands exit 0. If this repository's package scripts use different names, inspect `cmd/serf-hub/frontend/package.json` and run the defined type/build equivalents; do not skip type or build verification.

- [ ] **Step 4: Inspect the final diff and status.**

```bash
git diff HEAD~2 --check
git diff HEAD~2 -- cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskcard.module.css
git status --short
```

Expected: no whitespace errors, only the intended renderer/test/style changes, and no untracked scratch files.

- [ ] **Step 5: Commit any final formatting-only adjustment separately.**

If verification requires a formatting adjustment, run the repository formatter on only the touched frontend files, rerun the focused tests, and commit:

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskcard.module.css
git commit -m "chore: format task mutation renderer"
```

Do not make unrelated cleanup changes.
