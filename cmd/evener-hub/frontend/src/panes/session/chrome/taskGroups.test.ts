import { expect, test } from "vitest";
import type { TaskRow } from "./taskData";
import { groupTasks } from "./taskGroups";

function row(id: number, status: TaskRow["status"]): TaskRow {
  return { id, type: "implement", description: `task ${id}`, prompt: "", status };
}

test("partitions by status, settled holding done and cancelled", () => {
  const groups = groupTasks([row(1, "done"), row(2, "in_progress"), row(3, "open"), row(4, "cancelled")]);
  expect(groups.inProgress.map((r) => r.id)).toEqual([2]);
  expect(groups.open.map((r) => r.id)).toEqual([3]);
  expect(groups.settled.map((r) => r.id)).toEqual([1, 4]);
});

test("wire order is preserved within each group", () => {
  const groups = groupTasks([row(5, "done"), row(2, "done"), row(9, "open"), row(1, "open")]);
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
