import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { ToolCallItem } from "../ToolCallItem";
import "./taskCard"; // registers the real "task_list" descriptor

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

// A task_list commandExecution item, matching the wire the reducer preserves:
// argumentsJSON (kept through settle by mergeArguments / on reload by
// apptranscript.go) + the tool's own output text (which carries the
// "Progress: <done>/<total> tasks complete." footer for append/update).
function taskItem(args: unknown, output = "", overrides: Partial<ItemModel> = {}): ItemModel {
  return {
    id: "item_1",
    turnId: "turn_1",
    type: "commandExecution",
    toolName: "task_list",
    text: "",
    argumentsJSON: JSON.stringify(args),
    output,
    ...overrides,
  };
}

function renderItem(item: ItemModel) {
  return render(<ToolCallItem item={item} turn={turn} live={false} />);
}

test('action:"view" renders nothing at all (no card, no divider, no tool-call row)', () => {
  renderItem(taskItem({ action: "view" }, "1. [open] implement — a\n\nProgress: 0/1 tasks complete."));
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
  expect(screen.queryByTestId("task-card")).toBe(null);
});

test("appending N tasks renders one row per newly appended task", () => {
  renderItem(
    taskItem(
      {
        action: "append",
        tasks: [
          { type: "implement", description: "build the thing" },
          { type: "verify", description: "check the thing" },
        ],
      },
      "Added 2 task(s). Progress: 0/2 tasks complete.",
    ),
  );
  expect(screen.getByTestId("task-card")).toBeTruthy();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(2);
  expect(screen.getByText("build the thing")).toBeTruthy();
  expect(screen.getByText("check the thing")).toBeTruthy();
});

test("the progress head reads <done> / <total> from the tool output footer", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 3, status: "done" }] },
      "Updated 3→done. Progress: 3/3 tasks complete.",
    ),
  );
  expect(screen.getByTestId("task-card-progress").textContent).toBe("3 / 3");
});

test("a completed update renders a flagged touched-done row", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 2, status: "done" }] },
      "Updated 2→done. Progress: 2/2 tasks complete.",
    ),
  );
  const row = screen.getByTestId("task-card-row");
  expect(row.getAttribute("data-touch")).toBe("done");
  expect(row.textContent).toContain("#2");
});

test("a cancelled task renders a flagged touched-cancelled row carrying its note", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "cancelled", notes: "superseded by #5" }] },
      "Updated 4→cancelled. Progress: 1/2 tasks complete.",
    ),
  );
  const row = screen.getByTestId("task-card-row");
  expect(row.getAttribute("data-touch")).toBe("cancelled");
  expect(screen.getByText("superseded by #5")).toBeTruthy();
});

test("an in_progress update renders a started row", () => {
  renderItem(taskItem({ action: "update", updates: [{ id: 1, status: "in_progress" }] }, "Updated 1→in_progress."));
  expect(screen.getByTestId("task-card-row").getAttribute("data-touch")).toBe("started");
});

test("a failed task_list mutation renders NO card (its error is surfaced by the generic tool-error path instead)", () => {
  renderItem(taskItem({ action: "update", updates: [{ id: 9, status: "done" }] }, "", { error: "task 9 not found" }));
  // The row still exists (the generic error path owns it), but no task card.
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
  expect(screen.queryByTestId("task-card")).toBe(null);
  expect(screen.getByText("task 9 not found")).toBeTruthy();
});

test("a malformed / non-mutation task_list with no error renders nothing", () => {
  renderItem(taskItem({ action: "append" }, "")); // append with no tasks array = invalid
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
});

// A realistic StateResult.State snapshot (agent/task/task_store.go's Task[]
// shape): 7 tasks, the first three already done, #4 about to complete this
// call, #5 open with satisfied deps (the daemon's NextEligible pick), #6/#7
// still open. Matches the kata's own failure scenario: 7 tasks appended,
// completed one at a time, each completion auto-starting the next.
function sevenTaskState(overrides: Partial<Record<number, Record<string, unknown>>> = {}) {
  const base = [
    { id: 1, type: "implement", description: "first", prompt: "", status: "done" },
    { id: 2, type: "implement", description: "second", prompt: "", status: "done" },
    { id: 3, type: "implement", description: "third", prompt: "", status: "done" },
    { id: 4, type: "implement", description: "fourth", prompt: "", status: "done" },
    { id: 5, type: "implement", description: "fifth", prompt: "", status: "in_progress" },
    { id: 6, type: "implement", description: "sixth", prompt: "", status: "open" },
    { id: 7, type: "implement", description: "seventh", prompt: "", status: "open" },
  ];
  return base.map((t) => (overrides[t.id] ? { ...t, ...overrides[t.id] } : t));
}

test("completing a task shows the auto-started row the daemon advanced to (authoritative auto-activation)", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "done" }] },
      "Updated 4→done. Progress: 4/7 tasks complete.",
      { raw: sevenTaskState() },
    ),
  );
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(2);
  expect(rows[0]!.getAttribute("data-touch")).toBe("done");
  expect(rows[0]!.textContent).toContain("fourth");
  expect(rows[1]!.getAttribute("data-touch")).toBe("started");
  expect(rows[1]!.textContent).toContain("fifth");
});

test("an update row shows the task's description from authoritative state instead of a bare id", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 2, status: "cancelled", notes: "superseded" }] },
      "Updated 2→cancelled. Progress: 0/1 tasks complete.",
      { raw: [{ id: 2, type: "implement", description: "old approach", prompt: "", status: "cancelled" }] },
    ),
  );
  const row = screen.getByTestId("task-card-row");
  expect(row.textContent).toContain("old approach");
  expect(row.textContent).not.toContain("#2");
});

test("a batch that explicitly completes one task and starts another shows exactly those two rows (no duplicate auto-start)", () => {
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
  expect(rows).toHaveLength(2);
  expect(rows.map((r) => r.getAttribute("data-touch"))).toEqual(["done", "started"]);
  expect(rows[1]!.textContent).toContain("fifth");
});

test("a pure notes update leaves an unrelated in-progress task alone (no auto-start row fabricated)", () => {
  renderItem(
    taskItem({ action: "update", updates: [{ id: 6, status: "open", notes: "still blocked" }] }, "Updated 6→open.", {
      raw: sevenTaskState(),
    }),
  );
  // The reopen itself earns no row (matches the existing reopen contract
  // below), and task 5's pre-existing in_progress status must not be
  // mistaken for something this call just started.
  expect(screen.queryByTestId("task-card-row")).toBe(null);
});

test("completing the last task with nothing left eligible shows no auto-started row", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 7, status: "done" }] },
      "Updated 7→done. All tasks complete. Progress: 7/7 tasks complete.",
      { raw: sevenTaskState({ 5: { status: "done" }, 6: { status: "done" } }) },
    ),
  );
  const row = screen.getByTestId("task-card-row");
  expect(row.getAttribute("data-touch")).toBe("done");
});

test("absent raw (old daemon / replayed transcript) renders exactly today's argument-only behaviour, never a fabricated auto-start", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "done" }] },
      "Updated 4→done. Progress: 4/7 tasks complete.",
    ),
  );
  const row = screen.getByTestId("task-card-row");
  expect(row.getAttribute("data-touch")).toBe("done");
  expect(row.textContent).toContain("#4");
});

test("a reopen update (status open, not a flagged touch) shows the head but no per-row status change", () => {
  // Wire-true fixture: the Go tool rejects a status-less update (task_store.go
  // Update: status must be open/in_progress/done/cancelled), so a bare
  // {id, notes} → "Updated 1." pair can never reach the frontend. The real
  // update that touches a task without earning a row is a reopen to "open",
  // which formatTaskUpdates renders as "1→open" and the card treats as no
  // flagged touch (TOUCH_BY_STATUS covers only done/cancelled/in_progress).
  renderItem(
    taskItem({ action: "update", updates: [{ id: 1, status: "open", notes: "added a caveat" }] }, "Updated 1→open."),
  );
  expect(screen.getByTestId("task-card")).toBeTruthy();
  expect(screen.queryByTestId("task-card-row")).toBe(null);
});
