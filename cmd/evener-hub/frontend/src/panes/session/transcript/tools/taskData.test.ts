// @vitest-environment node
import { expect, test } from "vitest";
import { autoStartedTask, parseTaskState, type TaskRow, taskLabel } from "./taskData";

function task(overrides: Partial<TaskRow> & Pick<TaskRow, "id" | "status">): TaskRow {
  return { type: "implement", description: `task ${overrides.id}`, prompt: "", ...overrides };
}

test("parseTaskState parses the same wire-true Task[] shape the tasks panel parses", () => {
  const rows = parseTaskState([{ id: 1, type: "implement", description: "a", prompt: "", status: "open" }]);
  expect(rows).toEqual([{ id: 1, type: "implement", description: "a", prompt: "", status: "open" }]);
});

test("parseTaskState is null for absent/malformed raw - never an empty list", () => {
  expect(parseTaskState(undefined)).toBeNull();
  expect(parseTaskState(null)).toBeNull();
  expect(parseTaskState({})).toBeNull();
});

test("taskLabel prefers the authoritative description over the bare id", () => {
  const tasks = [task({ id: 4, description: "Port the charge path", status: "done" })];
  expect(taskLabel(tasks, 4)).toBe("Port the charge path");
});

test("taskLabel falls back to #<id> when state is absent (old daemon / replayed transcript)", () => {
  expect(taskLabel(null, 4)).toBe("#4");
});

test("taskLabel falls back to #<id> when the id isn't found in state", () => {
  const tasks = [task({ id: 1, status: "done" })];
  expect(taskLabel(tasks, 99)).toBe("#99");
});

test("taskLabel renders a placeholder when the update carries no id at all", () => {
  expect(taskLabel(null, undefined)).toBe("(task)");
});

test("autoStartedTask finds the task the daemon auto-advanced after a completion", () => {
  const tasks = [
    task({ id: 4, status: "done" }),
    task({ id: 5, status: "in_progress" }),
    task({ id: 6, status: "open" }),
  ];
  const started = autoStartedTask(tasks, new Set([4]), true);
  expect(started?.id).toBe(5);
});

test("autoStartedTask is undefined when nothing completed this call", () => {
  // completedAny=false - e.g. a pure notes update while task 4 sits
  // untouched in_progress from an earlier call. Must never read as freshly
  // started; the daemon never even attempted NextEligible for this call.
  const tasks = [task({ id: 4, status: "in_progress" })];
  expect(autoStartedTask(tasks, new Set(), false)).toBeUndefined();
});

test("autoStartedTask does not double-count a task the caller itself explicitly started", () => {
  // The caller's own batch named both ids (an explicit start alongside a
  // completion) - 5 already earns its own row, so it must not earn a second.
  const tasks = [task({ id: 4, status: "done" }), task({ id: 5, status: "in_progress" })];
  expect(autoStartedTask(tasks, new Set([4, 5]), true)).toBeUndefined();
});

test("autoStartedTask is undefined when state is absent - never fabricated without authoritative proof", () => {
  expect(autoStartedTask(null, new Set(), true)).toBeUndefined();
});

test("autoStartedTask is undefined when every task is already settled (all done, nothing to advance to)", () => {
  const tasks = [task({ id: 4, status: "done" }), task({ id: 5, status: "done" })];
  expect(autoStartedTask(tasks, new Set([4]), true)).toBeUndefined();
});
