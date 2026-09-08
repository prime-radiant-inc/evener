import assert from "node:assert/strict";
import { test } from "node:test";
import { decodeWorkNotification, runWorkNotifications } from "./work-notifications-logic.mjs";

const identity = { threadId: "thread-1", ref: "local:one" };
const job = { jobId: "job-1", jobType: "shell", status: "running", outputBytes: 0 };

test("decodes six generated work notification payloads with safe metadata", () => {
  const payloads = [
    { method: "evener/job/started", params: { ...identity, job } },
    { method: "evener/job/finished", params: { ...identity, job: { ...job, status: "completed" } } },
    {
      method: "evener/delegate/updated",
      params: {
        ...identity,
        delegate: {
          delegateId: "d1",
          status: "running",
          lifecycle: "active",
          phase: "work",
          projectionRevision: 1,
          resumable: true,
          needsAttention: false,
        },
      },
    },
    { method: "evener/jobs/treeUpdated", params: { ...identity, revision: 2 } },
    {
      method: "evener/task/updated",
      params: { ...identity, total: 2, done: 1, cancelled: 0, remaining: 1, current: { id: 2, description: "verify" } },
    },
    { method: "evener/goal/updated", params: { ...identity, goal: null } },
  ];
  assert.deepEqual(
    payloads.map(decodeWorkNotification).map((value) => value.method),
    payloads.map((value) => value.method),
  );
  assert.equal(decodeWorkNotification(payloads[5]).goal, null);
});

test("ignores unrelated notifications and preserves goal omission as invalid", () => {
  assert.equal(decodeWorkNotification({ method: "turn/completed", params: {} }), null);
  assert.throws(() => decodeWorkNotification({ method: "evener/goal/updated", params: identity }), /Missing goal/);
});

test("rejects malformed correlated payloads", () => {
  assert.throws(
    () =>
      decodeWorkNotification({
        method: "evener/job/started",
        params: { ...identity, job: { ...job, outputBytes: -1 } },
      }),
    /Invalid work job/,
  );
  assert.throws(
    () => decodeWorkNotification({ method: "evener/jobs/treeUpdated", params: { ...identity, revision: 1.5 } }),
    /jobs tree revision/,
  );
  assert.throws(
    () =>
      decodeWorkNotification({ method: "evener/job/started", params: { ...identity, job: { ...job, reason: {} } } }),
    /reason/,
  );
  assert.throws(
    () =>
      decodeWorkNotification({
        method: "evener/delegate/updated",
        params: { ...identity, delegate: { delegateId: "d", status: "running", projectionRevision: 1 } },
      }),
    /Invalid delegate/,
  );
  assert.throws(
    () => decodeWorkNotification({ method: "evener/task/updated", params: { ...identity, total: 1, done: 2 } }),
    /Invalid task/,
  );
});

const activityTree = {
  revision: 1,
  root: {
    kind: "session",
    sessionId: "thread-1",
    ref: "local:one",
    label: "Thread",
    aggregate: "complete",
    counts: { active: 0, failed: 0, completed: 0, complete: true },
    entries: [],
    branch: {},
  },
};

function workflowHub({ tasks = [], onEvent = null, fail = null, goal } = {}) {
  const notifications = new Set();
  const states = new Set();
  const calls = [];
  let reads = 0;
  const hub = {
    calls,
    connect: async () => calls.push({ method: "connect" }),
    onNotification: (callback) => {
      notifications.add(callback);
      return () => notifications.delete(callback);
    },
    onStateChange: (callback) => {
      states.add(callback);
      return () => states.delete(callback);
    },
    emit: (params, method = "evener/goal/updated") => {
      for (const callback of notifications) callback({ method, params });
    },
    request: async (method, params) => {
      calls.push({ method, params });
      if (method === "thread/unsubscribe") return {};
      if (method === "thread/read") {
        reads += 1;
        if (fail === "thread" || (fail === "readback" && reads > 1)) throw new Error("thread read failed");
        if (reads === 1) await onEvent?.(hub);
        return {
          thread: {
            id: "thread-1",
            evener: { ref: "local:one", capabilities: {}, queue: {}, ...(goal === undefined ? {} : { goal }) },
          },
        };
      }
      if (method === "evener/tasks/list") {
        if (fail === "tasks") throw new Error("tasks failed");
        return { data: tasks };
      }
      if (method === "evener/jobs/list") {
        if (fail === "jobs") throw new Error("jobs failed");
        return { data: activityTree };
      }
      throw new Error(`unexpected method ${method}`);
    },
    listenerCount: () => notifications.size + states.size,
  };
  return hub;
}

test("workflow reads correlated thread, tasks, and bounded jobs before and after observation", async () => {
  const hub = workflowHub();
  const result = await runWorkNotifications(
    hub,
    { ref: "local:one" },
    {
      observe: async () => hub.emit({ threadId: "thread-1", ref: "local:one", goal: null }),
      observeDurationMs: 0,
    },
  );
  assert.equal(result.outcome, "read");
  assert.equal(result.eventCount, 1);
  assert.equal(result.initial.thread.evener.ref, "local:one");
  assert.equal(result.readback.tasks.length, 0);
  assert.equal(hub.calls.filter((call) => call.method === "thread/read").length, 2);
  assert.deepEqual(
    hub.calls.filter((call) => call.method === "thread/read").map((call) => call.params),
    [
      { ref: "local:one", includeTurns: false, subscribe: true },
      { ref: "local:one", includeTurns: false, subscribe: true },
    ],
  );
  assert.deepEqual(hub.calls.at(-1), { method: "thread/unsubscribe", params: { ref: "local:one" } });
  assert.equal(hub.listenerCount(), 0);
});

test("preserves null task availability and ignores events from another ref", async () => {
  const hub = workflowHub({ tasks: null });
  const result = await runWorkNotifications(
    hub,
    { ref: "local:one" },
    {
      observe: async () => hub.emit({ threadId: "thread-foreign", ref: "local:other", goal: null }),
      observeDurationMs: 0,
    },
  );
  assert.equal(result.eventCount, 0);
  assert.equal(result.initial.tasks, null);
  assert.equal(result.readback.tasks, null);
});

test("malformed read data and read failures clean up the thread subscription", async () => {
  for (const fail of ["tasks", "readback"]) {
    const hub = workflowHub({ fail });
    await assert.rejects(
      runWorkNotifications(
        hub,
        { ref: "local:one" },
        { observe: async () => hub.emit({ threadId: "thread-1", ref: "local:one", goal: null }), observeDurationMs: 0 },
      ),
    );
    assert.deepEqual(hub.calls.at(-1), { method: "thread/unsubscribe", params: { ref: "local:one" } });
    assert.equal(hub.listenerCount(), 0);
  }
});

test("CLI reserves private output and exposes metadata-only stdout", async () => {
  const { mkdtemp, readFile, rm, stat } = await import("node:fs/promises");
  const { join } = await import("node:path");
  const { runWorkNotificationsCLI } = await import("./work-notifications-cli.mjs");
  const directory = await mkdtemp("/tmp/work-notifications-");
  const outputPath = join(directory, "result.json");
  const lines = [];
  await runWorkNotificationsCLI(
    {
      EVENER_WORK_NOTIFICATIONS_OUTPUT_FILE: outputPath,
      EVENER_THREAD_REF: "local:one",
      EVENER_WORK_NOTIFICATIONS_OBSERVE_MS: "0",
    },
    workflowHub(),
    { log: (line) => lines.push(line) },
  );
  assert.equal((await stat(outputPath)).mode & 0o777, 0o600);
  const privateResult = JSON.parse(await readFile(outputPath, "utf8"));
  assert.equal(privateResult.initial.ref, "local:one");
  assert.equal(privateResult.events.length, 0);
  assert.deepEqual(
    Object.keys(JSON.parse(lines[0])).sort(),
    [
      "changedDuringReadback",
      "connectionInterrupted",
      "eventCount",
      "jobState",
      "outcome",
      "outputWritten",
      "overflow",
      "ref",
      "taskAvailable",
    ].sort(),
  );
  await rm(directory, { recursive: true, force: true });
});

test("distinguishes an explicitly cleared goal from omitted goal state", async () => {
  const cleared = await runWorkNotifications(
    workflowHub({ goal: null }),
    { ref: "local:one" },
    { observeDurationMs: 0 },
  );
  const omitted = await runWorkNotifications(workflowHub(), { ref: "local:one" }, { observeDurationMs: 0 });
  assert.equal(cleared.initial.thread.evener.goal, null);
  assert.equal(Object.hasOwn(omitted.initial.thread.evener, "goal"), false);
});

test("preserves unsupported or malformed projections without claiming a complete read", async () => {
  const unavailable = await runWorkNotifications(
    workflowHub({ fail: "jobs" }),
    { ref: "local:one" },
    { observeDurationMs: 0 },
  );
  assert.equal(unavailable.readback.jobs.state, "failed");
  const malformed = workflowHub({ tasks: {} });
  await assert.rejects(runWorkNotifications(malformed, { ref: "local:one" }, { observeDurationMs: 0 }), /task list/);
  assert.deepEqual(malformed.calls.at(-1), { method: "thread/unsubscribe", params: { ref: "local:one" } });
});

test("CLI refuses an occupied output before connecting", async () => {
  const { mkdtemp, rm, writeFile } = await import("node:fs/promises");
  const { join } = await import("node:path");
  const { runWorkNotificationsCLI } = await import("./work-notifications-cli.mjs");
  const directory = await mkdtemp("/tmp/work-notifications-collision-");
  const outputPath = join(directory, "result.json");
  await writeFile(outputPath, "existing");
  const hub = workflowHub();
  await assert.rejects(
    runWorkNotificationsCLI({ EVENER_WORK_NOTIFICATIONS_OUTPUT_FILE: outputPath, EVENER_THREAD_REF: "local:one" }, hub),
  );
  assert.deepEqual(hub.calls, []);
  await rm(directory, { recursive: true, force: true });
});
