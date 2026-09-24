// @vitest-environment node
import { expect, test } from "vitest";
import { parseTaskListData, taskAggregateLabel } from "./taskListData";

// parseTaskListData narrows TaskListResponse.data (typed `unknown` on the
// wire - appwire/types.go:896-898, `TaskListResponse{Data any}`) into a
// display-ready TaskRow[]. Ground truth for the shape, since the catalog
// itself only says `any`:
//   - agent/task/task_store.go:54-79 (Task struct, json tags): id/type/
//     description/prompt/status/depends_on/notes/reasoning_effort/insert/
//     created_at/updated_at/completed_at.
//   - agent/task/task_store.go:28-36 (TaskStatus): open/in_progress/done/
//     cancelled.
//   - agent/session_tools.go:957-959 (Session.Tasks -> TaskStore.View,
//     always a non-nil, possibly-empty []Task).
//   - server/server.go:625-631 (SetTasksFunc) + cmd/evener/serve.go:596
//     (wired unconditionally by every real evener daemon session).
//   - server/appwire_runtime.go:713-721 (handleAppTasksList: Data is nil
//     only when no tasksFn is registered at all - an old daemon or a
//     source with no task support, which instead rejects the call outright).
// This is the wire-true fixture the panel renders from once a store action
// exists to fetch it (see this stream's report for the NEEDS_CONTEXT gap).

const WIRE_TRUE_TASK_LIST = [
  {
    id: 1,
    type: "implement",
    description: "Wire up the status row",
    prompt: "Build the status row per the design doc.",
    status: "done",
    created_at: "2026-07-20T10:00:00Z",
    updated_at: "2026-07-20T10:05:00Z",
    completed_at: "2026-07-20T10:05:00Z",
  },
  {
    id: 2,
    type: "implement",
    description: "Wire up session actions",
    prompt: "Fork/aside/compact/clear/shutdown/rename.",
    status: "in_progress",
    depends_on: [1],
    reasoning_effort: "high",
    notes: ["started the menu"],
    created_at: "2026-07-20T10:05:00Z",
    updated_at: "2026-07-20T10:06:00Z",
  },
  {
    id: 3,
    type: "verify",
    description: "Gate green",
    prompt: "",
    status: "open",
    created_at: "2026-07-20T10:06:00Z",
    updated_at: "2026-07-20T10:06:00Z",
  },
];

test("parses the real daemon shape into display-ready rows, camelCasing the snake_case wire fields", () => {
  const rows = parseTaskListData(WIRE_TRUE_TASK_LIST);
  expect(rows).toEqual([
    {
      id: 1,
      type: "implement",
      description: "Wire up the status row",
      prompt: "Build the status row per the design doc.",
      status: "done",
      createdAt: "2026-07-20T10:00:00Z",
      updatedAt: "2026-07-20T10:05:00Z",
      completedAt: "2026-07-20T10:05:00Z",
    },
    {
      id: 2,
      type: "implement",
      description: "Wire up session actions",
      prompt: "Fork/aside/compact/clear/shutdown/rename.",
      status: "in_progress",
      dependsOn: [1],
      reasoningEffort: "high",
      notes: ["started the menu"],
      createdAt: "2026-07-20T10:05:00Z",
      updatedAt: "2026-07-20T10:06:00Z",
    },
    {
      id: 3,
      type: "verify",
      description: "Gate green",
      prompt: "",
      status: "open",
      createdAt: "2026-07-20T10:06:00Z",
      updatedAt: "2026-07-20T10:06:00Z",
    },
  ]);
});

test("preserves the explicit task-start marker from a mutation snapshot", () => {
  const rows = parseTaskListData([
    { id: 1, type: "implement", description: "a", prompt: "", status: "in_progress", started: false },
    { id: 2, type: "implement", description: "b", prompt: "", status: "in_progress", started: true },
  ]);
  expect(rows?.map((row) => ({ id: row.id, started: row.started }))).toEqual([
    { id: 1, started: false },
    { id: 2, started: true },
  ]);
});

test("preserves the explicit terminal-settle marker from a mutation snapshot", () => {
  const rows = parseTaskListData([
    { id: 1, type: "implement", description: "a", prompt: "", status: "done", settled: true },
    { id: 2, type: "implement", description: "b", prompt: "", status: "done", settled: false },
    { id: 3, type: "implement", description: "c", prompt: "", status: "cancelled", settled: "yes" },
  ]);
  expect(rows?.map((row) => ({ id: row.id, settled: row.settled }))).toEqual([
    { id: 1, settled: true },
    { id: 2, settled: false },
    { id: 3, settled: undefined },
  ]);
});

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
  const rows = parseTaskListData([{ id: 1, type: "implement", description: "Gate green", prompt: "", status: "open" }]);
  expect(rows).toHaveLength(1);
  expect(rows?.[0]?.createdAt).toBeUndefined();
  expect(rows?.[0]?.updatedAt).toBeUndefined();
  expect(rows?.[0]?.completedAt).toBeUndefined();
});

test("non-string timestamp values are ignored, not carried", () => {
  const rows = parseTaskListData([
    {
      id: 1,
      type: "implement",
      description: "Gate green",
      prompt: "",
      status: "open",
      created_at: 42,
      updated_at: null,
    },
  ]);
  expect(rows).toHaveLength(1);
  expect(rows?.[0]?.createdAt).toBeUndefined();
  expect(rows?.[0]?.updatedAt).toBeUndefined();
});

test("an empty array (a real daemon with zero tasks - TaskStore.View's own always-non-nil empty slice) parses to an empty, non-null array", () => {
  expect(parseTaskListData([])).toEqual([]);
});

test("null (no tasksFn registered at all - an old daemon or an unsupported source) is honestly 'no data', not zero tasks", () => {
  expect(parseTaskListData(null)).toBeNull();
});

test("undefined is also 'no data'", () => {
  expect(parseTaskListData(undefined)).toBeNull();
});

test("a non-array is malformed data, not zero tasks", () => {
  expect(parseTaskListData({})).toBeNull();
  expect(parseTaskListData("nope")).toBeNull();
});

test("skips individual malformed entries (missing a required field) rather than throwing or discarding the whole list", () => {
  const rows = parseTaskListData([
    { id: 1, type: "implement", description: "good", prompt: "", status: "open" },
    { description: "no id", status: "open" }, // missing id: dropped
    null,
    "garbage",
    { id: 3, type: "implement", description: "also good", prompt: "", status: "done" },
  ]);
  expect(rows).toEqual([
    { id: 1, type: "implement", description: "good", prompt: "", status: "open" },
    { id: 3, type: "implement", description: "also good", prompt: "", status: "done" },
  ]);
});

test("drops a row carrying a status outside the daemon's four-status enum rather than casting it to TaskStatus", () => {
  // TaskStatus is the closed enum the Go store mints (open/in_progress/
  // done/cancelled), but the wire field is a bare string: a status a
  // newer daemon adds must not survive the cast into a settled group it
  // does not belong to (groupTasks routes everything unmatched there).
  const rows = parseTaskListData([
    { id: 1, type: "implement", description: "good", prompt: "", status: "open" },
    { id: 2, type: "implement", description: "unrecognized", prompt: "", status: "blocked" },
  ]);
  expect(rows).toEqual([{ id: 1, type: "implement", description: "good", prompt: "", status: "open" }]);
});

test("the aggregate reads 'N of M tasks left' while work remains", () => {
  expect(taskAggregateLabel({ total: 7, done: 1, cancelled: 5, remaining: 1 })).toBe("1 of 7 tasks left");
  expect(taskAggregateLabel({ total: 7, done: 3 })).toBe("4 of 7 tasks left");
  expect(taskAggregateLabel({ total: 20, done: 16 })).toBe("4 of 20 tasks left");
});

test("the aggregate singularizes the noun for a one-task list", () => {
  expect(taskAggregateLabel({ total: 1, done: 0 })).toBe("1 of 1 task left");
  expect(taskAggregateLabel({ total: 1, done: 1 })).toBe("All 1 task done");
  expect(taskAggregateLabel({ total: 1, done: 0, cancelled: 1, remaining: 0 })).toBe("All 1 task settled");
});

test("the aggregate infers an omitted remaining the way the daemon computes it", () => {
  expect(taskAggregateLabel({ total: 7, done: 1, cancelled: 5 })).toBe("1 of 7 tasks left");
  expect(taskAggregateLabel({ total: 7, done: 1, remaining: 5 })).toBe("5 of 7 tasks left");
});

test("the aggregate distinguishes a clean finish from an all-cancelled tail", () => {
  expect(taskAggregateLabel({ total: 3, done: 3 })).toBe("All 3 tasks done");
  expect(taskAggregateLabel({ total: 3, done: 0, cancelled: 3, remaining: 0 })).toBe("All 3 tasks settled");
});

test("an empty aggregate is 'No tasks'", () => {
  expect(taskAggregateLabel({ total: 0, done: 0 })).toBe("No tasks");
});
