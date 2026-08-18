// @vitest-environment node
import { expect, test } from "vitest";
import { parseTaskListData } from "./taskData";

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
//     (wired unconditionally by every real serf daemon session).
//   - server/appwire_runtime.go:713-721 (handleAppTasksList: Data is nil
//     only when no tasksFn is registered at all - an old daemon or a
//     source with no task support, e.g. cmd/evener-hub/internal/appsource/
//     codex_source.go:405-407 which instead rejects the call outright).
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
