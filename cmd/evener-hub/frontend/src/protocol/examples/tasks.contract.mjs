import assert from "node:assert/strict";
import test from "node:test";
import { runTasks, summarizeTasks } from "./tasks-logic.mjs";

const task = { id: 1, type: "implement", description: "03f14c2a", prompt: "39aecebb", status: "open" };
function fixture(response = { data: [task] }, connect = async () => {}) {
  const calls = [];
  return {
    calls,
    hub: {
      async connect() {
        calls.push({ method: "connect" });
        await connect();
      },
      async request(method, params) {
        calls.push({ method, params });
        return response;
      },
    },
  };
}

test("tasks captures only the selected reference before connecting", async () => {
  const input = { ref: "local:one" };
  const f = fixture(undefined, async () => {
    input.ref = "local:two";
  });
  const result = await runTasks(f.hub, input);
  assert.deepEqual(f.calls, [{ method: "connect" }, { method: "evener/tasks/list", params: { ref: "local:one" } }]);
  assert.deepEqual(result, { outcome: "read", available: true, readback: [task] });
});

test("tasks rejects invalid input without connecting", async () => {
  const f = fixture();
  for (const input of [
    undefined,
    null,
    [],
    {},
    { ref: "" },
    { ref: " " },
    { ref: 2 },
    { ref: "local:one", action: "remove" },
  ]) {
    await assert.rejects(runTasks(f.hub, input));
  }
  assert.deepEqual(f.calls, []);
});

test("task availability distinguishes null from an empty list", async () => {
  assert.deepEqual(await runTasks(fixture({ data: null }).hub, { ref: "local:one" }), {
    outcome: "read",
    available: false,
    readback: null,
  });
  assert.deepEqual(await runTasks(fixture({ data: [] }).hub, { ref: "local:one" }), {
    outcome: "read",
    available: true,
    readback: [],
  });
});

test("tasks preserves complete raw rows, order, optional strings, and future fields", async () => {
  const rows = [
    {
      ...task,
      depends_on: [2],
      notes: ["02dd9cb3"],
      reasoning_effort: "medium",
      insert: "after",
      created_at: "2026-09-07T13:00:00Z",
      updated_at: "2026-09-07T13:01:00Z",
      completed_at: "2026-09-07T13:02:00Z",
      extra: { retained: true },
    },
    { ...task, id: 2 },
  ];
  const result = await runTasks(fixture({ data: rows }).hub, { ref: "local:one" });
  assert.deepEqual(result.readback, rows);
  rows[0].extra.retained = false;
  rows[0].notes.push("external change");
  assert.equal(result.readback[0].extra.retained, true);
  assert.deepEqual(result.readback[0].notes, ["02dd9cb3"]);
});

test("tasks accepts every server task type and status", async () => {
  for (const type of ["research", "implement", "verify", "fix"]) {
    for (const status of ["open", "in_progress", "done", "cancelled"]) {
      const row = { ...task, type, status };
      assert.deepEqual((await runTasks(fixture({ data: [row] }).hub, { ref: "local:one" })).readback, [row]);
    }
  }
});

test("tasks rejects malformed envelopes, unsafe identities, duplicates, and optional fields", async () => {
  const rows = [
    null,
    [],
    {},
    ...[0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1].map((id) => ({ ...task, id })),
    { ...task, type: "unknown" },
    { ...task, status: "unknown" },
    { ...task, description: null },
    { ...task, prompt: false },
    { ...task, depends_on: [0] },
    { ...task, depends_on: null },
    { ...task, notes: [2] },
  ];
  for (const key of ["reasoning_effort", "insert", "created_at", "updated_at", "completed_at"])
    rows.push({ ...task, [key]: null });
  for (const key of ["id", "type", "description", "prompt", "status"]) {
    const row = { ...task };
    delete row[key];
    rows.push(row);
  }
  for (const response of [
    undefined,
    null,
    [],
    {},
    { data: {} },
    { data: [task, task] },
    ...rows.map((row) => ({ data: [row] })),
  ]) {
    const f = fixture();
    f.hub.request = async () => response;
    await assert.rejects(runTasks(f.hub, { ref: "local:one" }));
  }
});

test("task read failures propagate once without retries", async () => {
  for (const cause of [new Error("transport"), undefined]) {
    let requests = 0;
    const f = fixture();
    f.hub.request = async () => {
      requests++;
      throw cause;
    };
    await assert.rejects(runTasks(f.hub, { ref: "local:one" }), (error) => error === cause);
    assert.equal(requests, 1);
  }
});

test("task CLI summaries contain counts without raw task content", async () => {
  for (const data of [null, [], [task, { ...task, id: 2, status: "done" }, { ...task, id: 3, status: "open" }]]) {
    const result = await runTasks(fixture({ data }).hub, { ref: "local:one" });
    assert.deepEqual(summarizeTasks(result), {
      outcome: "read",
      available: data !== null,
      taskCount: data?.length ?? null,
      statusCounts: data?.length ? { open: 2, done: 1 } : {},
    });
  }
});
