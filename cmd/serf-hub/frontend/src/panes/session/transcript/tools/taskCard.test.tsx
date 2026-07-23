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

test("a note-only update (no recognized status change, no progress footer) shows the head but no per-row status change", () => {
  renderItem(taskItem({ action: "update", updates: [{ id: 1, notes: "added a caveat" }] }, "Updated 1."));
  expect(screen.getByTestId("task-card")).toBeTruthy();
  expect(screen.queryByTestId("task-card-row")).toBe(null);
});
