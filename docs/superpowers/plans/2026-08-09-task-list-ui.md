# Task List Panel Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the web UI tasks panel (`TasksPanelBody`) as status-grouped "live rows" — two-line collapsed rows with latest-update excerpts, a collapsed settled group, a dense expanded view (meta strip, timestamps, markdown prompt inline disclosure, updates timeline).

**Architecture:** Presentation-only changes in `cmd/serf-hub/frontend/src/panes/session/chrome/`, plus three small pure modules (`taskTime.ts`, `taskGroups.ts`) and parser additions in `taskData.ts`. No wire, daemon, or Go changes — the parser starts carrying `created_at`/`updated_at`/`completed_at`, which the daemon already sends. Fetch/stale/error logic is untouched.

**Tech Stack:** React 19, TypeScript, CSS Modules, Vitest + Testing Library, Biome. Spec: `docs/superpowers/specs/2026-08-09-task-list-ui-design.md`.

## Global Constraints

- Read `docs/testing.md` before adding or changing tests. Default tests must be deterministic: no provider credentials, network, quota, or ambient machine state.
- Frontend gates, in order, on every touched file: `npx biome check --write <files>` (run from `cmd/serf-hub/frontend`), then `make test-web` from the repo root; `make test-web-browser` at the end (Chrome-capable host).
- Biome rules that bite this code: no `noNonNullAssertion` (no `!` postfixes), no array-index keys without a scoped `biome-ignore` comment carrying a justification (the notes list keeps the existing precedent: append-only, position is stable identity).
- Token contract (`src/styles/token-contract.test.ts`): no hex/rgb/hsl/oklch literals outside `tokens.css`; `--attention`/`--alive`/`--danger` only in allowlisted widget stylesheets or exact-path exceptions. Task 8 adds the one exception this design needs.
- `STATUS_GLYPH` / `STATUS_TONE` mapping is unchanged: open ○ neutral, in_progress ● alive, done ✓ neutral, cancelled ✕ neutral. Cancelled is never danger-tinted.
- Commits are named-path only: `git add` exact files, never `git add -A` or `git add .`. Do not commit `mockups-draft/` (Task 9 handles it).
- Single-test runs from `cmd/serf-hub/frontend`: `npx vitest run src/panes/session/chrome/<file>.test.ts[x]`.

---

### Task 1: Parser carries timestamps

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/taskData.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/taskData.test.ts`

**Interfaces:**
- Consumes: nothing new — the wire already sends `created_at`/`updated_at`/`completed_at` (`agent/task/task_store.go`).
- Produces: `TaskRow` gains optional `createdAt?: string`, `updatedAt?: string`, `completedAt?: string`. Tasks 2, 4, 5, 6 consume them.

- [ ] **Step 1: Write the failing tests**

Add to `taskData.test.ts` (fixture style follows the file's existing wire-true fixtures):

```ts
test("carries created_at/updated_at/completed_at onto the row", () => {
  const rows = parseTaskListData([
    {
      id: 1,
      type: "implement",
      description: "Wire store ownership",
      prompt: "",
      status: "done",
      created_at: "2026-08-08T22:03:48.707849-07:00",
      updated_at: "2026-08-09T12:02:22.237482-07:00",
      completed_at: "2026-08-09T12:02:22.237482-07:00",
    },
  ]);
  expect(rows).toEqual([
    {
      id: 1,
      type: "implement",
      description: "Wire store ownership",
      prompt: "",
      status: "done",
      createdAt: "2026-08-08T22:03:48.707849-07:00",
      updatedAt: "2026-08-09T12:02:22.237482-07:00",
      completedAt: "2026-08-09T12:02:22.237482-07:00",
    },
  ]);
});

test("a row without timestamp fields still parses, with the fields absent", () => {
  const rows = parseTaskListData([
    { id: 1, type: "implement", description: "Gate green", prompt: "", status: "open" },
  ]);
  expect(rows).toHaveLength(1);
  expect(rows[0]?.createdAt).toBeUndefined();
  expect(rows[0]?.updatedAt).toBeUndefined();
  expect(rows[0]?.completedAt).toBeUndefined();
});

test("non-string timestamp values are ignored, not carried", () => {
  const rows = parseTaskListData([
    { id: 1, type: "implement", description: "Gate green", prompt: "", status: "open", created_at: 42, updated_at: null },
  ]);
  expect(rows).toHaveLength(1);
  expect(rows[0]?.createdAt).toBeUndefined();
  expect(rows[0]?.updatedAt).toBeUndefined();
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/taskData.test.ts`
Expected: FAIL — the first test's `toEqual` shows the parsed row lacks the three fields.

- [ ] **Step 3: Implement**

In `taskData.ts`:

1. Extend `TaskRow` (after the `started` field):

```ts
  // Wire timestamps (agent/task/task_store.go), carried as ISO strings.
  // Optional: the parser never drops a row for lacking them, and views omit
  // time displays for absent fields. created_at/updated_at are always present
  // on the real wire; completed_at exists only for done tasks.
  createdAt?: string;
  updatedAt?: string;
  completedAt?: string;
```

2. In `parseRow`, destructure `created_at, updated_at, completed_at` alongside the existing fields, and after the existing optional-field blocks add:

```ts
  if (typeof created_at === "string" && created_at !== "") row.createdAt = created_at;
  if (typeof updated_at === "string" && updated_at !== "") row.updatedAt = updated_at;
  if (typeof completed_at === "string" && completed_at !== "") row.completedAt = completed_at;
```

3. Replace the header comment paragraph that says the fields are "intentionally not carried into TaskRow" (lines ~17-20) with:

```ts
// created_at/updated_at/completed_at ARE carried (as createdAt/updatedAt/
// completedAt): the 2026-08-09 panel redesign (docs/superpowers/specs/
// 2026-08-09-task-list-ui-design.md) shows per-task recency and completion
// times, which the legacy panel's field set predates.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/taskData.test.ts`
Expected: PASS (all existing tests too — the fields are additive).

Also run the transcript-side consumer, which reuses this parser:
Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/taskData.test.ts src/panes/session/transcript/tools/taskCard.test.tsx`
Expected: PASS.

- [ ] **Step 5: Biome, then commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/taskData.ts src/panes/session/chrome/taskData.test.ts
cd - && git add cmd/serf-hub/frontend/src/panes/session/chrome/taskData.ts cmd/serf-hub/frontend/src/panes/session/chrome/taskData.test.ts
git commit -m "feat(web): carry task timestamps in tasks panel parser"
```

---

### Task 2: `taskTime.ts` — relative/absolute time formatting

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/taskTime.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/taskTime.test.ts`

**Interfaces:**
- Consumes: ISO strings from `TaskRow.createdAt`/`updatedAt`/`completedAt`.
- Produces:
  - `relativeTime(iso: string, now?: Date): string` — `"now"`, `"<m>m ago"`, `"<h>h ago"`, `"<d>d ago"`, or the raw `iso` when unparseable.
  - `absoluteTime(iso: string): string` — `"Aug 8, 22:03"`, or the raw `iso` when unparseable.

- [ ] **Step 1: Write the failing tests**

Create `taskTime.test.ts`:

```ts
import { expect, test } from "vitest";
import { absoluteTime, relativeTime } from "./taskTime";

const NOW = new Date("2026-08-09T13:02:17-07:00");

test("under a minute reads as now", () => {
  expect(relativeTime("2026-08-09T13:02:10-07:00", NOW)).toBe("now");
});

test("minutes round to the nearest minute", () => {
  expect(relativeTime("2026-08-09T12:25:17-07:00", NOW)).toBe("37m ago");
});

test("hours up to a day", () => {
  expect(relativeTime("2026-08-09T11:02:17-07:00", NOW)).toBe("2h ago");
  expect(relativeTime("2026-08-08T22:03:48-07:00", NOW)).toBe("15h ago");
});

test("days past 24 hours", () => {
  expect(relativeTime("2026-08-07T13:02:17-07:00", NOW)).toBe("2d ago");
});

test("a future timestamp clamps to now", () => {
  expect(relativeTime("2026-08-09T14:00:00-07:00", NOW)).toBe("now");
});

test("invalid input falls back to the raw string", () => {
  expect(relativeTime("not-a-date", NOW)).toBe("not-a-date");
  expect(absoluteTime("not-a-date")).toBe("not-a-date");
});

test("absolute renders month-day and 24-hour time", () => {
  expect(absoluteTime("2026-08-08T22:03:48-07:00")).toBe("Aug 8, 22:03");
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/taskTime.test.ts`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement**

Create `taskTime.ts`:

```ts
// Timestamp formatting for the tasks panel (spec: docs/superpowers/specs/
// 2026-08-09-task-list-ui-design.md). The wire's ISO strings stay strings in
// TaskRow; these two functions are the only place they become display text.
// Both tolerate invalid input by returning the raw string - a malformed
// timestamp is a display detail, never a reason to blank a task row.

export function relativeTime(iso: string, now: Date = new Date()): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return iso;
  const mins = Math.round((now.getTime() - t.getTime()) / 60000);
  if (mins < 1) return "now"; // covers the future-skew case too
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  return `${Math.round(hrs / 24)}d ago`;
}

export function absoluteTime(iso: string): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return iso;
  return t.toLocaleString("en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/taskTime.test.ts`
Expected: PASS. (`absoluteTime` runs in the machine's local TZ; the fixture's `-07:00` matches this host. If CI runs elsewhere, the assertion still holds because the ISO offset and the formatter agree on the host zone — do not "fix" it by pinning UTC; the panel shows local time deliberately.)

- [ ] **Step 5: Biome, then commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/taskTime.ts src/panes/session/chrome/taskTime.test.ts
cd - && git add cmd/serf-hub/frontend/src/panes/session/chrome/taskTime.ts cmd/serf-hub/frontend/src/panes/session/chrome/taskTime.test.ts
git commit -m "feat(web): add tasks panel time formatting helpers"
```

---

### Task 3: `taskGroups.ts` — status grouping

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/taskGroups.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/taskGroups.test.ts`

**Interfaces:**
- Consumes: `TaskRow` from `./taskData`.
- Produces:
  - `interface TaskGroups { inProgress: TaskRow[]; open: TaskRow[]; settled: TaskRow[] }`
  - `groupTasks(rows: TaskRow[]): TaskGroups` — stable partition; done and cancelled land in `settled`; wire order preserved within each group. Task 5 consumes this.

- [ ] **Step 1: Write the failing tests**

Create `taskGroups.test.ts`:

```ts
import { expect, test } from "vitest";
import type { TaskRow } from "./taskData";
import { groupTasks } from "./taskGroups";

function row(id: number, status: TaskRow["status"]): TaskRow {
  return { id, type: "implement", description: `task ${id}`, prompt: "", status };
}

test("partitions by status, settled holding done and cancelled", () => {
  const groups = groupTasks([
    row(1, "done"),
    row(2, "in_progress"),
    row(3, "open"),
    row(4, "cancelled"),
  ]);
  expect(groups.inProgress.map((r) => r.id)).toEqual([2]);
  expect(groups.open.map((r) => r.id)).toEqual([3]);
  expect(groups.settled.map((r) => r.id)).toEqual([1, 4]);
});

test("wire order is preserved within each group", () => {
  const groups = groupTasks([
    row(5, "done"),
    row(2, "done"),
    row(9, "open"),
    row(1, "open"),
  ]);
  expect(groups.settled.map((r) => r.id)).toEqual([5, 2]);
  expect(groups.open.map((r) => r.id)).toEqual([9, 1]);
});

test("an all-settled list yields empty live groups", () => {
  const groups = groupTasks([row(1, "done"), row(2, "cancelled")]);
  expect(groups.inProgress).toEqual([]);
  expect(groups.open).toEqual([]);
  expect(groups.settled).toHaveLength(2);
});

test("an empty list yields three empty groups", () => {
  expect(groupTasks([])).toEqual({ inProgress: [], open: [], settled: [] });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/taskGroups.test.ts`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement**

Create `taskGroups.ts`:

```ts
// Status grouping for the tasks panel's focus-group layout (spec:
// docs/superpowers/specs/2026-08-09-task-list-ui-design.md §Groups).
// Presentational only: a stable partition that never reorders within a
// group - the wire's id order is the session's own chronology and the panel
// has no business re-sorting it. done and cancelled share `settled`: the
// per-row glyph and strikethrough keep the distinction visible inside the
// collapsed history group.
import type { TaskRow } from "./taskData";

export interface TaskGroups {
  inProgress: TaskRow[];
  open: TaskRow[];
  settled: TaskRow[];
}

export function groupTasks(rows: TaskRow[]): TaskGroups {
  const groups: TaskGroups = { inProgress: [], open: [], settled: [] };
  for (const row of rows) {
    if (row.status === "in_progress") groups.inProgress.push(row);
    else if (row.status === "open") groups.open.push(row);
    else groups.settled.push(row);
  }
  return groups;
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/taskGroups.test.ts`
Expected: PASS.

- [ ] **Step 5: Biome, then commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/taskGroups.ts src/panes/session/chrome/taskGroups.test.ts
cd - && git add cmd/serf-hub/frontend/src/panes/session/chrome/taskGroups.ts cmd/serf-hub/frontend/src/panes/session/chrome/taskGroups.test.ts
git commit -m "feat(web): add tasks panel status grouping"
```

---

### Task 4: Panel body header — progress meter + count

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx`

**Interfaces:**
- Consumes: `model.tasks` (`{ total, done } | null`), the `Meter` widget (`import { Meter } from "../../../widgets"`; props `label: string, value: number, max: number, tone: "neutral"` — same call shape as `transcript/tools/taskCard.tsx`).
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Write the failing tests**

Add to `TasksPanel.test.tsx`. Follow the file's existing fetch-driving pattern (render `<TasksPanelBody sessionRef="ref_a" model={...} />` inside `<Toast.Host />`, connect a `FakeClient`, and stub its `listTasks` — copy the setup from the existing "renders every fetched task as a row" test):

```ts
test("the body header shows the meter and count when the aggregate is known", async () => {
  // ... same FakeClient/listTasks stub as the existing row-render test ...
  render(
    <Toast.Host>
      <TasksPanelBody sessionRef="ref_a" model={testModel({ tasks: { total: 20, done: 16 } })} />
    </Toast.Host>,
  );
  await waitFor(() => expect(screen.getByTestId("tasks-body-head")).toBeTruthy());
  expect(screen.getByTestId("tasks-body-head").textContent).toContain("16/20 done");
  expect(screen.getByRole("progressbar", { name: "Task progress: 16 of 20 complete" })).toBeTruthy();
});

test("the body header is absent while no aggregate has arrived", async () => {
  // ... same stub ...
  render(
    <Toast.Host>
      <TasksPanelBody sessionRef="ref_a" model={testModel({ tasks: null })} />
    </Toast.Host>,
  );
  await waitFor(() => expect(screen.getByTestId("task-row")).toBeTruthy());
  expect(screen.queryByTestId("tasks-body-head")).toBeNull();
});
```

Note: if `Meter` does not expose `role="progressbar"`, read `src/widgets/meter/index.tsx` first and assert on whatever role/aria it actually exposes; adjust the first assertion to match the widget's real accessibility contract.

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx`
Expected: FAIL — `tasks-body-head` not found (and no other test breaks).

- [ ] **Step 3: Implement**

In `TasksPanel.tsx`:

1. Add `Meter` to the existing `../../../widgets` import.
2. Add to the `CLASS` map: `bodyHead`, `count`.
3. In `TasksPanelBody`'s `renderBody`, inside the final rows branch's returned fragment (directly above the `{rows.length === 0 ? ...` conditional), add:

```tsx
        {model.tasks && (
          <div className={CLASS.bodyHead} data-testid="tasks-body-head">
            <Meter
              label={`Task progress: ${model.tasks.done} of ${model.tasks.total} complete`}
              value={model.tasks.done}
              max={model.tasks.total}
              tone="neutral"
            />
            <span className={CLASS.count}>
              {model.tasks.done}/{model.tasks.total} done
            </span>
          </div>
        )}
```

In `taskspanel.module.css` add:

```css
/* The body header repeats the trigger's aggregate so an open panel reads as
 * a whole: the same done/total the trigger badge shows, plus the meter. */
.bodyHead {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-bottom: var(--space-3);
}

.count {
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-mid);
  white-space: nowrap;
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx`
Expected: PASS.

- [ ] **Step 5: Biome, then commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/TasksPanel.tsx src/panes/session/chrome/taskspanel.module.css src/panes/session/chrome/TasksPanel.test.tsx
cd - && git add cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx
git commit -m "feat(web): add tasks panel body progress header"
```

---

### Task 5: Grouped list + collapsed live rows

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx` (the `TaskRowView` summary and the rows branch of `renderBody`)
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx`

**Interfaces:**
- Consumes: `groupTasks` (Task 3), `relativeTime`/`absoluteTime` (Task 2), `TaskRow.notes`/`updatedAt` (Task 1), `Disclosure` (`widgets/disclosure`), `taskDisclosureId`.
- Produces: the settled-group disclosure id `${sessionRef}\0settled-group`; `data-testid` hooks `task-group-live` (with `data-status="in_progress"|"open"`), `task-settled-group`, `task-latest`, `task-row-time`. Task 6 replaces the expanded body; this task keeps rendering the existing `TaskDetails` inside the disclosure so nothing regresses between tasks.

- [ ] **Step 1: Write the failing tests**

First **revise the existing test** "renders every fetched task as a row, in the SAME order the wire returned them (no client-side re-sort)" — the grouped layout deliberately reorders across statuses while preserving order within groups. Replace its assertions with:

```ts
test("rows group by status: in progress, then open, then the collapsed settled group; wire order holds within a group", async () => {
  // TASKS_DATA fixture order is done(1), in_progress(2), open(3).
  // ... same render as before ...
  await waitFor(() => expect(screen.getAllByTestId("task-row")).toHaveLength(2)); // settled row is behind the collapsed group
  const liveGroups = screen.getAllByTestId("task-group-live");
  expect(liveGroups.map((g) => g.getAttribute("data-status"))).toEqual(["in_progress", "open"]);
  expect(screen.getByTestId("task-settled-group").textContent).toContain("1");
  // settled row appears after opening the group
  await userEvent.click(screen.getByTestId("task-settled-group-summary"));
  await waitFor(() => expect(screen.getAllByTestId("task-row")).toHaveLength(3));
});
```

Add new tests (a second fixture with timestamps and notes — add near `TASKS_DATA`):

```ts
const DATED_TASKS = [
  {
    id: 1, type: "implement", description: "Implement artifact store", prompt: "",
    status: "done",
    notes: ["Implemented secure artifact store in commits 9853cf561 and 162d0d41e."],
    created_at: "2026-08-08T22:03:48-07:00", updated_at: "2026-08-09T10:53:57-07:00",
    completed_at: "2026-08-09T10:53:57-07:00",
  },
  {
    id: 2, type: "implement", description: "Extend transcript API", prompt: "Execute Task 6.",
    status: "in_progress",
    created_at: "2026-08-08T22:03:48-07:00", updated_at: "2026-08-09T13:02:17-07:00",
  },
  {
    id: 3, type: "implement", description: "Transition to implementation plan", prompt: "",
    status: "cancelled",
    notes: ["Cancelled at the user's request."],
    created_at: "2026-08-08T20:25:33-07:00", updated_at: "2026-08-08T21:49:49-07:00",
  },
];

test("a live row shows its latest note inline; a settled row does not", async () => {
  // ... render with DATED_TASKS, open the settled group ...
  const rows = screen.getAllByTestId("task-row");
  // done row (settled): no latest excerpt even though it has notes
  const settledRow = rows.find((r) => r.textContent?.includes("Implement artifact store"));
  expect(settledRow?.querySelector("[data-testid='task-latest']")).toBeNull();
});

test("a cancelled row renders struck-through inside the settled group", async () => {
  // ... render with DATED_TASKS, open the settled group ...
  const cancelled = screen.getByText("Transition to implementation plan");
  expect(cancelled.getAttribute("data-struck")).toBe("true");
});

test("a live row shows a relative updated time", async () => {
  // ... render with DATED_TASKS ...
  const row = screen.getByText("Extend transcript API").closest("[data-testid='task-row']");
  expect(row?.querySelector("[data-testid='task-row-time']")).toBeTruthy();
});

test("the settled group defaults to collapsed and remembers being opened per session", async () => {
  // ... render, open group, unmount, render again for the same sessionRef ...
  // second render: settled rows visible without another click
});
```

The live-row excerpt positive case: task id 2 has no notes, so also add a note to a fixture in-progress task or assert on an open task with notes — the implementer should extend `DATED_TASKS` with a fourth task (`id: 4, status: "open"`, one note) and assert its excerpt text appears.

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx`
Expected: FAIL on the new/revised tests; pre-existing fetch/error tests still PASS.

- [ ] **Step 3: Implement**

In `TasksPanel.tsx`:

1. Imports: add `groupTasks` from `./taskGroups`, `relativeTime`/`absoluteTime` from `./taskTime`.
2. `CLASS` additions: `groupHead`, `groupCount`, `settledSummary`, `summaryMain`, `summaryLine`, `descDim`, `descStruck`, `time`, `latest`, `latestLabel`, `latestText`.
3. Rewrite `TaskRowView`:

```tsx
// settled rows (the collapsed history group) render one line, dimmed, with
// no latest-update excerpt: history costs one line per task. Live rows earn
// their second line with the most recent note.
function TaskRowView({ task, sessionRef, settled = false }: { task: TaskRow; sessionRef: string; settled?: boolean }) {
  const notes = task.notes ?? [];
  const latest = !settled && notes.length > 0 ? notes[notes.length - 1] : null;
  const descClass =
    task.status === "cancelled" ? CLASS.descStruck : settled ? CLASS.descDim : CLASS.description;
  const summary = (
    <>
      <Chip tone={STATUS_TONE[task.status]}>{STATUS_GLYPH[task.status]}</Chip>
      <span className={CLASS.summaryMain}>
        <span className={CLASS.summaryLine}>
          <span className={descClass} data-struck={task.status === "cancelled" ? "true" : undefined}>
            {task.description}
          </span>
          {task.updatedAt && (
            <span className={CLASS.time} data-testid="task-row-time" title={absoluteTime(task.updatedAt)}>
              {relativeTime(task.updatedAt)}
            </span>
          )}
        </span>
        {latest && (
          <span className={CLASS.latest} data-testid="task-latest">
            <span className={CLASS.latestLabel}>latest</span>
            <span className={CLASS.latestText} title={latest}>
              {latest}
            </span>
          </span>
        )}
      </span>
    </>
  );
  return (
    <li data-testid="task-row">
      <Disclosure id={taskDisclosureId(sessionRef, task.id)} summary={summary}>
        <TaskDetails task={task} />
      </Disclosure>
    </li>
  );
}
```

4. Add a group section helper and rewire the rows branch of `renderBody`:

```tsx
function LiveGroup({ label, status, tasks, sessionRef }: { label: string; status: string; tasks: TaskRow[]; sessionRef: string }) {
  if (tasks.length === 0) return null;
  return (
    <section data-testid="task-group-live" data-status={status}>
      <h4 className={CLASS.groupHead}>
        {label} <span className={CLASS.groupCount}>{tasks.length}</span>
      </h4>
      <ul className={CLASS.list}>
        {tasks.map((row) => (
          <TaskRowView key={row.id} task={row} sessionRef={sessionRef} />
        ))}
      </ul>
    </section>
  );
}
```

Replace `<ul className={CLASS.list}>{rows.map(...)}</ul>` with:

```tsx
          <TaskListGroups rows={rows} sessionRef={sessionRef} />
```

and define:

```tsx
function TaskListGroups({ rows, sessionRef }: { rows: TaskRow[]; sessionRef: string }) {
  const groups = groupTasks(rows);
  return (
    <>
      <LiveGroup label="In progress" status="in_progress" tasks={groups.inProgress} sessionRef={sessionRef} />
      <LiveGroup label="Open" status="open" tasks={groups.open} sessionRef={sessionRef} />
      {groups.settled.length > 0 && (
        <Disclosure
          id={`${sessionRef}\0settled-group`}
          summary={
            <span className={CLASS.settledSummary} data-testid="task-settled-group-summary">
              Done · settled <span className={CLASS.groupCount}>{groups.settled.length}</span>
            </span>
          }
          data-testid="task-settled-group"
        >
          <ul className={CLASS.list}>
            {groups.settled.map((row) => (
              <TaskRowView key={row.id} task={row} sessionRef={sessionRef} settled />
            ))}
          </ul>
        </Disclosure>
      )}
    </>
  );
}
```

(`Disclosure` accepts `data-testid`; it lands on the `<details>`. The summary span gets its own testid for the click.)

5. CSS (`taskspanel.module.css`) — add:

```css
.groupHead {
  margin: var(--space-3) 0 var(--space-1);
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  font-weight: var(--font-weight-medium);
  color: var(--ink-mid);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.groupCount {
  color: var(--ink-low);
}

.settledSummary {
  font-family: var(--font-sans);
  font-size: var(--font-size-ui);
  color: var(--ink-mid);
}

/* The two-line live-row summary: line 1 is glyph + description + time,
 * line 2 (live rows with notes only) is the latest update excerpt. */
.summaryMain {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.summaryLine {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.descDim {
  composes: description;
  color: var(--ink-mid);
}

.descStruck {
  composes: description;
  color: var(--ink-low);
  text-decoration: line-through;
}

.description, .descDim, .descStruck {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}

.time {
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-low);
  white-space: nowrap;
  flex: none;
}

.latest {
  display: flex;
  gap: var(--space-2);
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-mid);
}

.latestLabel {
  color: var(--ink-low);
  flex: none;
}

.latestText {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}
```

Note: CSS Modules `composes` works in this codebase's Vite setup; if the build rejects it, flatten by repeating `.description`'s declarations instead.

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx`
Expected: PASS, including the revised ordering test. If the "a task row starts collapsed..." or "each row's expand state is independent" tests break because their fixture rows moved into groups, update their selectors (rows are still `task-row` testids; expand behavior is unchanged).

- [ ] **Step 5: Biome, then commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/TasksPanel.tsx src/panes/session/chrome/taskspanel.module.css src/panes/session/chrome/TasksPanel.test.tsx
cd - && git add cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx
git commit -m "feat(web): group tasks panel rows and show latest updates inline"
```

---

### Task 6: Expanded body — meta strip + timestamps line

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx` (replace `TaskDetails`/`TaskDetailField`)
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css` (remove `detailList`/`detailRow`/`detailLabel`/`detailValue`/`detailPrompt`; add new classes)
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx`

**Interfaces:**
- Consumes: `TaskRow` incl. timestamps (Task 1), `relativeTime`/`absoluteTime` (Task 2).
- Produces: `TaskExpandedBody` rendering `data-testid`s `task-meta`, `task-times`. Tasks 7 and 8 add the prompt disclosure and notes timeline into this same body.

- [ ] **Step 1: Write the failing tests**

Revise "a task with none of the optional fields still shows status and type, omitting depends-on/reasoning/prompt/notes entirely" — its assertions target the old `dl` testids. New version:

```ts
test("an expanded bare task shows only the type meta and 'No updates yet.'", async () => {
  // ... expand a fixture row with no optional fields ...
  const body = screen.getByTestId("task-expanded");
  expect(body.querySelector("[data-testid='task-meta']")?.textContent).toContain("implement");
  expect(body.querySelector("[data-testid='task-meta']")?.textContent).not.toContain("reasoning");
  expect(body.querySelector("[data-testid='task-times']")).toBeNull();
  expect(body.querySelector("[data-testid='task-prompt']")).toBeNull();
  expect(body.textContent).toContain("No updates yet.");
});
```

Add:

```ts
test("an expanded task shows the meta strip and timestamps line", async () => {
  // ... DATED_TASKS, expand "Implement artifact store" (done, has all fields plus depends_on [14] and reasoning_effort "high" - extend the fixture) ...
  const meta = screen.getByTestId("task-meta");
  expect(meta.textContent).toContain("implement");
  expect(meta.textContent).toContain("high");
  expect(meta.textContent).toContain("#14");
  const times = screen.getByTestId("task-times");
  expect(times.textContent).toContain("created Aug 8, 22:03");
  expect(times.textContent).toContain("updated");
  expect(times.textContent).toContain("completed");
});

test("the timestamps line omits updated when it equals created", async () => {
  // ... fixture task whose updated_at === created_at and no completed_at ...
  const times = screen.getByTestId("task-times");
  expect(times.textContent).toContain("created");
  expect(times.textContent).not.toContain("updated");
  expect(times.textContent).not.toContain("completed");
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx`
Expected: FAIL — `task-expanded`/`task-meta`/`task-times` not found.

- [ ] **Step 3: Implement**

In `TasksPanel.tsx`, delete `TaskDetailField` and `TaskDetails` and add:

```tsx
// The expanded body: dense, one line per concern (spec §Expanded row). Each
// part omits itself when its data is absent rather than rendering an empty
// shell - a freshly appended task with no deps, no reasoning override, no
// notes and no prompt shows the meta strip and "No updates yet." only.
function TaskExpandedBody({ task }: { task: TaskRow }) {
  return (
    <div className={CLASS.expandedBody} data-testid="task-expanded">
      <TaskMetaStrip task={task} />
      <TaskTimestamps task={task} />
      {/* Task 7 inserts TaskPromptDisclosure here; Task 8 replaces the notes fallback with TaskNotesTimeline */}
      <TaskNotesTimeline task={task} />
    </div>
  );
}

function TaskMetaStrip({ task }: { task: TaskRow }) {
  const deps = task.dependsOn ?? [];
  return (
    <div className={CLASS.metaStrip} data-testid="task-meta">
      <span className={CLASS.metaKey}>type</span>
      <span className={CLASS.metaValue}>{task.type}</span>
      {task.reasoningEffort && (
        <>
          <span className={CLASS.metaKey}>reasoning</span>
          <span className={CLASS.metaValue}>{task.reasoningEffort}</span>
        </>
      )}
      {deps.length > 0 && (
        <>
          <span className={CLASS.metaKey}>depends</span>
          <span className={CLASS.metaValue}>{deps.map((id) => `#${id}`).join(" ")}</span>
        </>
      )}
    </div>
  );
}

function TaskTimestamps({ task }: { task: TaskRow }) {
  if (!task.createdAt) return null;
  const showUpdated = task.updatedAt && task.updatedAt !== task.createdAt;
  return (
    <div className={CLASS.times} data-testid="task-times">
      <span>created {absoluteTime(task.createdAt)}</span>
      {showUpdated && task.updatedAt && (
        <span>
          updated <span title={absoluteTime(task.updatedAt)}>{relativeTime(task.updatedAt)}</span>
        </span>
      )}
      {task.completedAt && (
        <span>
          completed <span title={absoluteTime(task.completedAt)}>{relativeTime(task.completedAt)}</span>
        </span>
      )}
    </div>
  );
}
```

For this task only, `TaskNotesTimeline` is the minimal version (Task 8 builds the timeline):

```tsx
function TaskNotesTimeline({ task }: { task: TaskRow }) {
  const notes = task.notes ?? [];
  if (notes.length === 0) {
    return (
      <div className={CLASS.noNotes} data-testid="task-notes-empty">
        No updates yet.
      </div>
    );
  }
  return (
    <div data-testid="task-notes">
      <div className={CLASS.notesHead}>Updates · {notes.length}</div>
      <ol className={CLASS.notesList}>
        {notes.map((note, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: notes only ever append over a task's life (agent/task/task_store.go's update handling) - position is stable identity
          <li key={i}>{note}</li>
        ))}
      </ol>
    </div>
  );
}
```

Update `TaskRowView`'s disclosure body to `<TaskExpandedBody task={task} />`. Remove the old `detailList`/`detailRow`/`detailLabel`/`detailValue`/`detailPrompt` entries from `CLASS` and the stylesheet. Add CSS:

```css
.expandedBody {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  margin: var(--space-2) 0 var(--space-2);
}

.metaStrip {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1) var(--space-2);
  align-items: baseline;
}

.metaKey {
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-low);
}

.metaValue {
  font-family: var(--font-mono);
  font-size: var(--font-size-caption);
  color: var(--ink-mid);
}

.times {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1) var(--space-3);
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-low);
}

.notesHead {
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-mid);
  margin-bottom: var(--space-1);
}

.noNotes {
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
  color: var(--ink-low);
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx`
Expected: PASS. The "clicking its summary expands it to show the full detail fields" test likely asserts old testids (`task-detail-status` etc.) — revise it to assert `task-expanded` and `task-meta` instead.

- [ ] **Step 5: Biome, then commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/TasksPanel.tsx src/panes/session/chrome/taskspanel.module.css src/panes/session/chrome/TasksPanel.test.tsx
cd - && git add cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx
git commit -m "feat(web): dense expanded task body with meta strip and timestamps"
```

---

### Task 7: Prompt as markdown inline disclosure

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx`

**Interfaces:**
- Consumes: `Markdown` widget (`import { Markdown } from "../../../widgets"`; props `{ source: string }` — do NOT pass `live`, that is for streaming), `isDisclosureOpen`/`toggleDisclosure` from `../../../widgets/disclosure/disclosureStore`, `taskDisclosureId`.
- Produces: `TaskPromptDisclosure` with `data-testid="task-prompt"`, disclosure id `${taskDisclosureId(sessionRef, task.id)}\0prompt`.

**Why hand-rolled:** the shared `Disclosure` widget renders its chevron before the summary content; the session inline grammar (SteeringItem.tsx, spec decision 3) puts the chevron after the label, followed by the preview on the same line. Copy SteeringItem's store-backed `<details>` pattern.

- [ ] **Step 1: Write the failing tests**

```ts
test("the prompt disclosure shows a one-line markdown preview collapsed and the full markdown body open", async () => {
  // fixture task with prompt: "Execute **Task 6** from the plan:\nread_transcript `job/artifact` API and errors."
  // ... expand the row ...
  const prompt = screen.getByTestId("task-prompt");
  // collapsed: preview line renders markdown (bold becomes <strong>, not literal **)
  expect(prompt.querySelector(".promptPreview strong, [class*='promptPreview'] strong")).toBeTruthy();
  expect(prompt.textContent).not.toContain("**");
  // open it via its summary
  await userEvent.click(screen.getByTestId("task-prompt-summary"));
  const body = await screen.findByTestId("task-prompt-body");
  expect(body.querySelector("code")?.textContent).toBe("job/artifact");
});

test("a task with a blank prompt renders no prompt disclosure", async () => {
  // ... expand a row whose prompt is "" ...
  expect(screen.queryByTestId("task-prompt")).toBeNull();
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx`
Expected: FAIL — `task-prompt` not found.

- [ ] **Step 3: Implement**

In `TasksPanel.tsx`:

```tsx
function TaskPromptDisclosure({ task, sessionRef }: { task: TaskRow; sessionRef: string }) {
  if (task.prompt.trim() === "") return null;
  const id = `${taskDisclosureId(sessionRef, task.id)}\0prompt`;
  const open = isDisclosureOpen(id, false);
  const firstLine = task.prompt.split("\n").find((line) => line.trim() !== "") ?? "";
  return (
    <details className={CLASS.promptDetails} data-testid="task-prompt" open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; see SteeringItem.tsx */}
      <summary
        className={CLASS.promptSummary}
        data-testid="task-prompt-summary"
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(id, false);
        }}
      >
        <span className={CLASS.promptLabel}>Prompt</span>
        <span className={CLASS.promptChevron} aria-hidden="true" data-open={open ? "true" : "false"}>
          ▸
        </span>
        <span className={CLASS.promptPreview}>
          <Markdown source={firstLine} />
        </span>
      </summary>
      {open && (
        <div className={CLASS.promptBody} data-testid="task-prompt-body">
          <Markdown source={task.prompt} />
        </div>
      )}
    </details>
  );
}
```

Insert `<TaskPromptDisclosure task={task} sessionRef={sessionRef} />` into `TaskExpandedBody` between `TaskTimestamps` and `TaskNotesTimeline` (pass `sessionRef` into `TaskExpandedBody` and down from `TaskRowView`). Import `Markdown` and the disclosure store functions.

CSS:

```css
.promptDetails {
  font-family: var(--font-sans);
  font-size: var(--font-size-caption);
}

.promptSummary {
  display: flex;
  align-items: baseline;
  gap: var(--space-1);
  list-style: none;
  cursor: pointer;
  color: var(--ink-mid);
}

.promptSummary::-webkit-details-marker {
  display: none;
}

.promptLabel {
  flex: none;
}

.promptChevron {
  display: inline-flex;
  flex: none;
  color: var(--ink-low);
}

.promptChevron[data-open="true"] {
  transform: rotate(90deg);
}

.promptPreview {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--ink-low);
}

/* Flatten the Markdown widget's block structure into the single preview
 * line: paragraphs and headings go inline, margins die, the ellipsis comes
 * from the parent's nowrap+overflow. */
.promptPreview p,
.promptPreview h1,
.promptPreview h2,
.promptPreview h3,
.promptPreview h4,
.promptPreview ul,
.promptPreview ol,
.promptPreview blockquote,
.promptPreview pre {
  display: inline;
  margin: 0;
  padding: 0;
  border: none;
  background: none;
}

.promptBody {
  margin-top: var(--space-1);
  font-size: var(--font-size-ui);
  color: var(--ink-hi);
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx`
Expected: PASS.

- [ ] **Step 5: Biome, then commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/TasksPanel.tsx src/panes/session/chrome/taskspanel.module.css src/panes/session/chrome/TasksPanel.test.tsx
cd - && git add cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx
git commit -m "feat(web): render task prompt as markdown inline disclosure"
```

---

### Task 8: Updates timeline + token-contract exception

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx` (`TaskNotesTimeline` becomes the rail timeline)
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css`
- Modify: `cmd/serf-hub/frontend/src/styles/token-contract.test.ts` (exact-path exception)
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx`

**Interfaces:**
- Consumes: `TaskRow.notes`.
- Produces: `data-latest="true"` on the final note's `<li>`; the `taskspanel.module.css` entry in `SEMANTIC_PATH_EXCEPTIONS`.

- [ ] **Step 1: Write the failing tests**

```ts
test("notes render as a timeline with the latest note marked", async () => {
  // fixture task with two notes
  // ... expand the row ...
  const notes = screen.getByTestId("task-notes");
  const items = notes.querySelectorAll("li");
  expect(items).toHaveLength(2);
  expect(items[0]?.getAttribute("data-latest")).toBeNull();
  expect(items[1]?.getAttribute("data-latest")).toBe("true");
  expect(notes.textContent).toContain("Updates · 2");
});
```

And in `token-contract.test.ts`, following the file's existing per-exception scoping tests (e.g. "the askdock.module.css semantic-var exception is scoped to its exact path"):

```ts
test("the taskspanel.module.css semantic-var exception is scoped to its exact path, not just its basename", () => {
  expect(SEMANTIC_PATH_EXCEPTIONS.has("panes/session/chrome/taskspanel.module.css")).toBe(true);
  expect(SEMANTIC_PATH_EXCEPTIONS.has("widgets/taskspanel.module.css")).toBe(false);
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx src/styles/token-contract.test.ts`
Expected: FAIL — `data-latest` missing; exception not in the set.

- [ ] **Step 3: Implement**

1. `TasksPanel.tsx` — replace the Task 6 `TaskNotesTimeline` list body:

```tsx
  return (
    <div data-testid="task-notes">
      <div className={CLASS.notesHead}>Updates · {notes.length}</div>
      <ol className={CLASS.notesRail}>
        {notes.map((note, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: notes only ever append over a task's life (agent/task/task_store.go's update handling) - position is stable identity
          <li key={i} className={CLASS.note} data-latest={i === notes.length - 1 ? "true" : undefined}>
            {note}
          </li>
        ))}
      </ol>
    </div>
  );
```

2. `taskspanel.module.css` — replace `.notesList` with:

```css
/* The updates rail: a neutral 2px spine with one dot per note. The LATEST
 * note's dot is the one --alive reach in this file (see the exact-path
 * exception in token-contract.test.ts): position, not colour, is already
 * the signal, so every other marker and all text stay on the ink scale. */
.notesRail {
  margin: 0 0 0 3px;
  padding: 0 0 0 var(--space-3);
  border-left: 2px solid var(--edge);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  list-style: none;
}

.note {
  position: relative;
  font-family: var(--font-sans);
  font-size: var(--font-size-ui);
  color: var(--ink-hi);
}

.note::before {
  content: "";
  position: absolute;
  left: calc(-1 * var(--space-3) - 5px);
  top: 7px;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--surface-1);
  border: 2px solid var(--ink-low);
}

.note[data-latest="true"]::before {
  border-color: var(--alive);
}
```

3. `token-contract.test.ts` — add to the kata-comment block above `SEMANTIC_PATH_EXCEPTIONS` (same shape as the activitypanel entry):

```ts
// task-list-ui task 8: panes/session/chrome/taskspanel.module.css earns the
// same exception for the same structural reason - it lives under
// panes/session/chrome/, not widgets/<name>/, so it can never match
// WIDGET_STYLESHEET_RE either. Its one semantic reach is --alive on the
// latest-update dot of the tasks panel's notes rail (docs/superpowers/
// specs/2026-08-09-task-list-ui-design.md): position already marks the
// latest note, the hue is a glyph-level accent, and all panel text stays
// on the ink scale.
```

and add `"panes/session/chrome/taskspanel.module.css",` to the `SEMANTIC_PATH_EXCEPTIONS` set.

- [ ] **Step 4: Run to verify pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/TasksPanel.test.tsx src/styles/token-contract.test.ts`
Expected: PASS.

- [ ] **Step 5: Biome, then commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/TasksPanel.tsx src/panes/session/chrome/taskspanel.module.css src/panes/session/chrome/TasksPanel.test.tsx src/styles/token-contract.test.ts
cd - && git add cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx cmd/serf-hub/frontend/src/panes/session/chrome/taskspanel.module.css cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx cmd/serf-hub/frontend/src/styles/token-contract.test.ts
git commit -m "feat(web): render task notes as updates timeline"
```

---

### Task 9: Final gates, mockup archival, and spec conformance

**Files:**
- Move: `mockups-draft/index.html` → `docs/superpowers/specs/2026-08-09-task-list-ui-mockup.html`
- Move: `mockups-draft/shot-final.png` → `docs/superpowers/specs/2026-08-09-task-list-ui-mockup.png`
- Modify: `docs/superpowers/specs/2026-08-09-task-list-ui-design.md` (fix the mockup path references)
- Delete: `mockups-draft/` (template.html, remaining shots)

- [ ] **Step 1: Full unit gate**

Run: `make test-web` (repo root)
Expected: PASS — typecheck, unit tests, Biome all green.

- [ ] **Step 2: Browser gate**

Run: `make test-web-browser` (repo root)
Expected: PASS — real geometry and browser guards.

- [ ] **Step 3: Spec conformance sweep**

Re-read `docs/superpowers/specs/2026-08-09-task-list-ui-design.md` sections Layout, Component structure, Edge cases. For each normative statement, point at the implementing code or test. Fix any miss at its root. In particular verify: empty groups render nothing; the all-settled list shows only the settled line; the body header renders only when `model.tasks` is non-null; cancelled rows are ✕ + struck, neutral tone; no live-ticking timer exists.

- [ ] **Step 4: Archive the mockup**

```bash
mv mockups-draft/index.html docs/superpowers/specs/2026-08-09-task-list-ui-mockup.html
mv mockups-draft/shot-final.png docs/superpowers/specs/2026-08-09-task-list-ui-mockup.png
rm -rf mockups-draft
```

Update the spec's two `mockups-draft/...` references to the new paths (`mockups-draft/index.html` → `2026-08-09-task-list-ui-mockup.html`, same directory as the spec).

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/specs/2026-08-09-task-list-ui-mockup.html docs/superpowers/specs/2026-08-09-task-list-ui-mockup.png docs/superpowers/specs/2026-08-09-task-list-ui-design.md
git commit -m "docs: archive task list panel mockup with spec"
```

- [ ] **Step 6: Verify the worktree is clean**

Run: `git status --short`
Expected: no output (nothing untracked, nothing dirty).

---

## Self-Review Notes

- Spec coverage: parser timestamps (T1), time helpers (T2), grouping (T3, T5), body header (T4), collapsed rows incl. settled/cancelled (T5), expanded meta+times (T6), prompt markdown disclosure (T7), updates timeline + token route (T8), gates + edge-case sweep (T9). Edge cases table is covered by T5/T6 tests plus the T9 sweep.
- Type consistency: `TaskRow.createdAt/updatedAt/completedAt` (T1) are the same names consumed in T2/T5/T6; `groupTasks`→`TaskGroups` (T3) is what T5 imports; `taskDisclosureId` reuse in T7 matches the existing signature; `Meter` props copied from `taskCard.tsx`'s call.
- The one deliberate spec deviation: T6's intermediate `TaskNotesTimeline` ships a plain `<ol>` for one commit before T8 replaces it with the rail — reviewer should not reject T6 for the missing timeline; it is T8's deliverable.
