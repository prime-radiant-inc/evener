import assert from "node:assert/strict";

const taskTypes = new Set(["research", "implement", "verify", "fix"]);
const taskStatuses = new Set(["open", "in_progress", "done", "cancelled"]);
const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const isString = (value) => typeof value === "string";
const positiveInteger = (value) => Number.isSafeInteger(value) && value > 0;

function decodeTask(value) {
  assert.ok(
    isRecord(value) &&
      positiveInteger(value.id) &&
      taskTypes.has(value.type) &&
      isString(value.description) &&
      isString(value.prompt) &&
      taskStatuses.has(value.status),
    "Invalid task response.",
  );
  assert.ok(
    value.depends_on === undefined || (Array.isArray(value.depends_on) && value.depends_on.every(positiveInteger)),
    "Invalid task dependencies.",
  );
  assert.ok(
    value.notes === undefined || (Array.isArray(value.notes) && value.notes.every(isString)),
    "Invalid task notes.",
  );
  for (const key of ["reasoning_effort", "insert", "created_at", "updated_at", "completed_at"])
    assert.ok(value[key] === undefined || isString(value[key]), "Invalid task field.");
  // Preserve timestamps and future fields as authored data; this recipe does
  // not interpret dates or normalize the task descriptions and prompts.
  return structuredClone(value);
}

export async function runTasks(hub, input) {
  assert.ok(
    isRecord(input) &&
      Object.keys(input).every((key) => key === "ref") &&
      isString(input.ref) &&
      input.ref.trim().length > 0,
    "Provide only a task-list reference.",
  );
  const params = { ref: input.ref };
  await hub.connect();
  const result = await hub.request("evener/tasks/list", params);
  assert.ok(isRecord(result) && Object.hasOwn(result, "data"), "Invalid task-list response.");
  if (result.data === null) return { outcome: "read", available: false, readback: null };
  assert.ok(Array.isArray(result.data), "Invalid task list.");
  const readback = result.data.map(decodeTask);
  assert.equal(new Set(readback.map((task) => task.id)).size, readback.length, "Duplicate task identity.");
  return { outcome: "read", available: true, readback };
}

export function summarizeTasks(result) {
  const statusCounts = {};
  for (const task of result.readback ?? []) statusCounts[task.status] = (statusCounts[task.status] ?? 0) + 1;
  return {
    outcome: result.outcome,
    available: result.available,
    taskCount: result.readback?.length ?? null,
    statusCounts,
  };
}
