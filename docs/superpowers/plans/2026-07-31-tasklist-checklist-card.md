# Task-List Changes Card Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the task_list mutation card as a static checklist — one progress bar, one done-count, and checkbox-square rows (done/cancelled struck through) per the approved spec `docs/superpowers/specs/2026-07-31-tasklist-renderer-design.md`.

**Architecture:** Pure presentation change inside the transcript's per-tool descriptor for `task_list`. A new local `TaskCheck` SVG glyph component (16-grid stroke grammar, per-touch color via CSS) leads each touched-task row; the data layer (`taskData.ts`, `mutationRows`) is untouched.

**Tech Stack:** React 19 + TypeScript, CSS Modules, Vitest + Testing Library, Biome.

## Global Constraints

- All work happens in the `tasklist-checklist-card` worktree; frontend root is `cmd/serf-hub/frontend`.
- Deterministic tests only (per `docs/testing.md`): no network, no provider credentials.
- Icon grammar: 16x16 viewBox, `stroke="currentColor"`, strokeWidth 1.75, round caps/joins, `fill="none"`, square box, inline `display: block` style, `aria-hidden="true"`, `focusable="false"`.
- Semantic color ONLY on checkbox glyphs (`--success` done, `--accent` started, `--ink-mid` added/cancelled); all text stays on the ink scale. This is a scoped, user-approved exception to the neutral-card rule documented in `taskcard.module.css`.
- Every CSS class referenced from TSX must be wrapped in `requireClass(styles.X, "<module>", "X")`.
- Tests run from `cmd/serf-hub/frontend`: `npx vitest run <path>`; typecheck `npm run typecheck`; lint `npm run lint`.

---

### Task 1: TaskCheck checkbox glyph component

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCheck.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskcheck.module.css`
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCheck.test.tsx`

**Interfaces:**
- Consumes: `requireClass` from `../../../../widgets/internal/requireClass`.
- Produces: `export const TOUCHES` (`readonly ["added","done","cancelled","started"]`), `export type TaskTouch`, `export function TaskCheck({ touch, size? }: { touch: TaskTouch; size?: number })`. Task 2 imports all three. The svg carries `data-testid="task-check"` and `data-touch={touch}`.

- [ ] **Step 1: Write the failing test**

```tsx
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { TaskCheck, TOUCHES } from "./taskCheck";

afterEach(cleanup);

test("every touch renders one square, aria-hidden checkbox glyph tagged with its touch", () => {
  for (const touch of TOUCHES) {
    const { unmount } = render(<TaskCheck touch={touch} />);
    const svg = screen.getByTestId("task-check");
    expect(svg.getAttribute("data-touch")).toBe(touch);
    expect(svg.getAttribute("aria-hidden")).toBe("true");
    expect(svg.getAttribute("width")).toBe(svg.getAttribute("height"));
    expect(svg.className).toContain(touch); // per-touch color modifier class
    unmount();
  }
});

test("the default box is 16px and a caller can override the size", () => {
  const { unmount } = render(<TaskCheck touch="done" />);
  expect(screen.getByTestId("task-check").getAttribute("width")).toBe("16");
  unmount();
  render(<TaskCheck touch="done" size={20} />);
  expect(screen.getByTestId("task-check").getAttribute("width")).toBe("20");
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/taskCheck.test.tsx`
Expected: FAIL — module `./taskCheck` does not exist.

- [ ] **Step 3: Write the component and its styles**

`taskcheck.module.css`:

```css
/* TaskCheck: the task card's per-touch checkbox glyph. This file is the ONE
 * scoped exception to the transcript card's neutral-color house rule
 * (user-approved, docs/superpowers/specs/2026-07-31-tasklist-renderer-
 * design.md): the glyph alone carries subtle semantic colour so a row's
 * state reads at a glance; every piece of TEXT on the card stays on the
 * ink scale. */
.check {
  flex: none;
}

.added {
  color: var(--ink-mid);
}

.done {
  color: var(--success);
}

.cancelled {
  color: var(--ink-mid);
}

.started {
  color: var(--accent);
}
```

`taskCheck.tsx`:

```tsx
// The task card's per-touch checkbox glyph: a square box whose inner mark
// names what happened to the task (plus = added, check = done, x =
// cancelled, arrow = started). Drawn in the app's shared 16x16 line-art
// grammar (stroke currentColor, 1.75 width, round caps/joins, fill none,
// square box - the same contract widgets/toolicon makes), so the glyph
// reads as family next to the transcript's tool-kind icons and the row's
// own CSS class governs colour. Deliberately NOT part of the ToolIcon set:
// that set is per tool KIND, this is per task STATUS. The glyph is a
// picture of state, not a control - aria-hidden, never focusable; the
// row's visually-hidden status word (taskCard.tsx) is what assistive tech
// reads.
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./taskcheck.module.css";

export const TOUCHES = ["added", "done", "cancelled", "started"] as const;
export type TaskTouch = (typeof TOUCHES)[number];

const CLASS = {
  check: requireClass(styles.check, "taskcheck.module.css", "check"),
  added: requireClass(styles.added, "taskcheck.module.css", "added"),
  done: requireClass(styles.done, "taskcheck.module.css", "done"),
  cancelled: requireClass(styles.cancelled, "taskcheck.module.css", "cancelled"),
  started: requireClass(styles.started, "taskcheck.module.css", "started"),
};

// The box outline every touch shares; only the inner mark varies.
const BOX = "M2.5 2.5 H13.5 V13.5 H2.5 Z";
const MARKS: Record<TaskTouch, string> = {
  added: "M8 5.5 V10.5 M5.5 8 H10.5",
  done: "M4.8 8.4 L7.2 10.8 L11.4 5.6",
  cancelled: "M5.5 5.5 L10.5 10.5 M10.5 5.5 L5.5 10.5",
  started: "M5 8 H11 M8.8 5.8 L11 8 L8.8 10.2",
};

const DEFAULT_SIZE = 16;

export function TaskCheck({ touch, size = DEFAULT_SIZE }: { touch: TaskTouch; size?: number }) {
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      aria-hidden="true"
      focusable="false"
      className={`${CLASS.check} ${CLASS[touch]}`}
      data-testid="task-check"
      data-touch={touch}
      // Inline rather than a class (same rationale as widgets/toolicon):
      // `display` here is correctness - an inline SVG would sit in a line
      // box taller than itself, undoing the square box.
      style={{ display: "block" }}
    >
      <path
        d={`${BOX} ${MARKS[touch]}`}
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/taskCheck.test.tsx`
Expected: PASS (2 tests).

- [ ] **Step 5: Typecheck, lint, commit**

Run: `cd cmd/serf-hub/frontend && npm run typecheck && npm run lint`
Expected: both clean (Biome may reformat; run `npm run check` first if it complains about formatting).

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCheck.tsx \
        cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskcheck.module.css \
        cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCheck.test.tsx
git commit -m "feat(web): TaskCheck per-touch checkbox glyph for the task card"
```

### Task 2: Checklist rows in the task card

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx` (CLASS map, `TouchedRow.touch` type, `TOUCH_BY_STATUS` type, `TaskCardRow`)
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskcard.module.css` (drop `.flag`; add `.rowText`, `.descStruck`, `.srOnly`; restructure `.row`, `.note`; new header comment)
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx`

**Interfaces:**
- Consumes: `TaskCheck`, `TaskTouch` from `./taskCheck` (Task 1).
- Produces: unchanged row contract — `data-testid="task-card-row"` + `data-touch` per row; label text remains the exact text content of the label span (existing tests keep passing). New: label span class is `descStruck` for done/cancelled, `desc` otherwise; each row carries a `.srOnly` status word ("added"/"done"/"cancelled"/"started") as a sibling BEFORE the label span; the note span moves inside a `.rowText` column under the label.

- [ ] **Step 1: Write the failing tests**

Append to `taskCard.test.tsx` (the existing `taskItem`/`renderItem` helpers are already in scope):

```tsx
test("every row leads with a TaskCheck glyph whose touch matches the row's", () => {
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 4, status: "done" },
          { id: 5, status: "in_progress" },
        ],
      },
      "Updated 4→done, 5→in_progress. Progress: 4/7 tasks complete.",
      { raw: sevenTaskState() },
    ),
  );
  const rows = screen.getAllByTestId("task-card-row");
  for (const row of rows) {
    const glyph = within(row).getByTestId("task-check");
    expect(glyph.getAttribute("data-touch")).toBe(row.getAttribute("data-touch"));
  }
});

test("done and cancelled labels are struck through; added and started labels are not", () => {
  const { unmount } = renderItem(
    taskItem(
      { action: "update", updates: [{ id: 2, status: "done" }] },
      "Updated 2→done. Progress: 2/2 tasks complete.",
    ),
  );
  expect(screen.getByText("#2").className).toContain("descStruck");
  unmount();

  renderItem(
    taskItem(
      { action: "append", tasks: [{ type: "implement", description: "build the thing" }] },
      "Added 1 task(s). Progress: 0/1 tasks complete.",
    ),
  );
  expect(screen.getByText("build the thing").className).not.toContain("descStruck");
});

test("the visible flag word is gone; the status rides along visually-hidden", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 2, status: "done" }] },
      "Updated 2→done. Progress: 2/2 tasks complete.",
    ),
  );
  const row = screen.getByTestId("task-card-row");
  const spoken = within(row).getByText("done");
  expect(spoken.className).toContain("srOnly");
});

test("a note renders under its row's label, inside the row's text column", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "cancelled", notes: "superseded by #5" }] },
      "Updated 4→cancelled. Progress: 1/2 tasks complete.",
    ),
  );
  const note = screen.getByText("superseded by #5");
  expect(note.className).toContain("note");
  // The note sits in the same column wrapper as the label, not on the
  // glyph's baseline row.
  expect(note.parentElement?.className).toContain("rowText");
});
```

Add `within` to the Testing Library import at the top of the test file:

```tsx
import { cleanup, render, screen, within } from "@testing-library/react";
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/taskCard.test.tsx`
Expected: FAIL — no `task-check` testid, no `descStruck`/`srOnly`/`rowText` classes yet.

- [ ] **Step 3: Rework the row markup and styles**

In `taskCard.tsx`:

1. Add the import: `import { TaskCheck, type TaskTouch } from "./taskCheck";`
2. Change `TouchedRow.touch` from `string` to `TaskTouch`, and `TOUCH_BY_STATUS` from `Record<string, string>` to `Record<string, TaskTouch>`.
3. In the CLASS map: remove `flag`; add
   `rowText: requireClass(styles.rowText, "taskcard.module.css", "rowText"),`
   `descStruck: requireClass(styles.descStruck, "taskcard.module.css", "descStruck"),`
   `srOnly: requireClass(styles.srOnly, "taskcard.module.css", "srOnly"),`
4. Replace `TaskCardRow` with:

```tsx
// The word assistive tech reads for each touch - the visible flag label is
// gone, so the status rides along visually-hidden beside the glyph.
const TOUCH_WORD: Record<TaskTouch, string> = {
  added: "added",
  done: "done",
  cancelled: "cancelled",
  started: "started",
};

function TaskCardRow({ row }: { row: TouchedRow }) {
  const struck = row.touch === "done" || row.touch === "cancelled";
  return (
    <div className={CLASS.row} data-testid="task-card-row" data-touch={row.touch}>
      <TaskCheck touch={row.touch} />
      <div className={CLASS.rowText}>
        <span className={CLASS.srOnly}>{TOUCH_WORD[row.touch]}</span>
        <span className={struck ? CLASS.descStruck : CLASS.desc}>{row.label}</span>
        {row.note && <span className={CLASS.note}>{row.note}</span>}
      </div>
    </div>
  );
}
```

In `taskcard.module.css`, replace the whole file with:

```css
/* Task-update card: a checklist of what the mutation changed. Neutral
 * throughout EXCEPT one scoped, user-approved exception (docs/superpowers/
 * specs/2026-07-31-tasklist-renderer-design.md): the TaskCheck glyph alone
 * carries subtle semantic colour (see taskcheck.module.css). All text stays
 * on the ink scale - done/cancelled read through strikethrough + low ink,
 * never colour, and a failed task_list still renders no card at all. */
.card {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-2) 0;
}

.head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
}

.progress {
  font-family: var(--font-mono);
  font-size: var(--font-size-caption);
  color: var(--ink-mid);
  white-space: nowrap;
}

.rows {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.row {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  font-size: var(--font-size-ui);
}

/* The label + note column: the note hangs under its label, aligned with the
 * label text rather than the checkbox glyph. */
.rowText {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.desc {
  color: var(--ink-hi);
}

.descStruck {
  color: var(--ink-low);
  text-decoration: line-through;
}

.note {
  color: var(--ink-mid);
  font-size: var(--font-size-caption);
  font-style: italic;
}

/* Standard visually-hidden recipe (same as statusrow.module.css's .srOnly):
 * carries the row's status word for assistive tech now that the visible
 * flag label is gone. */
.srOnly {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/taskCard.test.tsx`
Expected: PASS — all pre-existing tests plus the 4 new ones (24 total). The pre-existing tests pass unchanged because the label span keeps its exact text content and `data-testid`/`data-touch` hooks.

- [ ] **Step 5: Typecheck, lint, commit**

Run: `cd cmd/serf-hub/frontend && npm run typecheck && npm run lint`
Expected: both clean.

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx \
        cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskcard.module.css \
        cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx
git commit -m "feat(web): render task_list changes as a struck-through checklist"
```

### Task 3: One count, one bar — summary dedupe

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx` (`TaskCardBody` head text, descriptor `summary`)
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx`

**Interfaces:**
- Consumes: nothing new.
- Produces: the card head's count span (`data-testid="task-card-progress"`) reads `"<done> of <total> done"`; the descriptor summary is the constant string `"Tasks"`. `parseProgress` stays — the body still uses it.

- [ ] **Step 1: Update the failing test, add the dedupe test**

In `taskCard.test.tsx`, change the existing progress-head test to the new copy:

```tsx
test("the progress head reads '<done> of <total> done' from the tool output footer", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 3, status: "done" }] },
      "Updated 3→done. Progress: 3/3 tasks complete.",
    ),
  );
  expect(screen.getByTestId("task-card-progress").textContent).toBe("3 of 3 done");
});
```

Append the dedupe test:

```tsx
test("the count appears exactly once - the tool-row summary is just 'Tasks'", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 3, status: "done" }] },
      "Updated 3→done. Progress: 3/3 tasks complete.",
    ),
  );
  expect(screen.getAllByTestId("task-card-progress")).toHaveLength(1);
  expect(screen.queryByText(/Tasks ·/)).toBe(null);
  expect(screen.getByText("Tasks")).toBeTruthy();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/taskCard.test.tsx`
Expected: FAIL — head still reads `3 / 3` and the summary still renders `Tasks · 3 / 3`.

- [ ] **Step 3: Change the head copy and the summary**

In `taskCard.tsx`, change the head span inside `TaskCardBody` to:

```tsx
          <span className={CLASS.progress} data-testid="task-card-progress">
            {progress.done} of {progress.total} done
          </span>
```

and the descriptor summary to the constant (the count lives in the card head, which auto-expands, so it stays visible without a click):

```tsx
  summary: () => "Tasks",
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/taskCard.test.tsx`
Expected: PASS (25 tests).

- [ ] **Step 5: Typecheck, lint, commit**

Run: `cd cmd/serf-hub/frontend && npm run typecheck && npm run lint`
Expected: both clean.

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx \
        cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx
git commit -m "feat(web): single done-count beside the task card's progress bar"
```

---

## Final Verification

- [ ] Full frontend suite: `cd cmd/serf-hub/frontend && npx vitest run` — all green.
- [ ] `npm run typecheck` and `npm run lint` — clean.
- [ ] From the repo root: `make test-web` — green (matches CI's entry point).
- [ ] Manual smoke (optional): `npm run dev`, open a session with task_list mutations, confirm the checklist card renders per spec.
