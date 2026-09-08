import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { runSessionNotificationsCLI } from "./session-notifications-cli.mjs";
import { decodeNotification, METHODS, runSessionNotifications } from "./session-notifications-logic.mjs";

const ref = "local:session",
  base = { threadId: "thread", ref };
const samples = {
  "thread/started": { ...base, thread: { id: "thread", evener: { ref } } },
  "thread/closed": { ...base, reason: "shutdown" },
  "thread/status/changed": { ...base, status: { type: "idle", activeFlags: [] } },
  "thread/queueChanged": { ...base, queue: { revision: 0, preview: [] } },
  "evener/thread/name/changed": { ...base, name: "Renamed" },
  "thread/model/changed": {
    ...base,
    modelProvider: "fixture",
    model: "fixture-model",
    reasoningEffortLevels: [],
    supportsReasoning: false,
  },
  "thread/reasoning-effort/changed": { ...base, reasoningEffort: "medium" },
  "thread/vision-model/changed": { ...base, visionModel: "vision-model" },
};
test("decodes all Batch 1 session notification payloads", () => {
  for (const method of METHODS) {
    const event = decodeNotification({ method, params: samples[method] }, ref);
    assert.equal(event.method, method);
    assert.equal(event.ref, ref);
    assert.equal(event.threadId, "thread");
  }
});
test("ignores unrelated methods and refs", () => {
  assert.equal(decodeNotification({ method: "turn/started", params: base }, ref), null);
  assert.equal(decodeNotification({ method: "thread/closed", params: { ...base, ref: "local:other" } }, ref), null);
});
test("rejects malformed matching notification", () => {
  assert.throws(() => decodeNotification({ method: "thread/status/changed", params: { ...base, status: {} } }, ref));
  assert.throws(() => decodeNotification({ method: "thread/queueChanged", params: { ...base, queue: {} } }, ref));
  assert.throws(() =>
    decodeNotification(
      { method: "thread/started", params: { ...base, thread: { id: "other", evener: { ref } } } },
      ref,
    ),
  );
});
test("performs bounded initial and final reads", async () => {
  const calls = [];
  const hub = {
    connect: async () => {},
    request: async (method, params) => {
      calls.push({ method, params });
      if (method === "thread/unsubscribe") return {};
      return { thread: { id: "thread", evener: { ref, status: { type: "idle" }, capabilities: {} } } };
    },
    onNotification: () => () => {},
    onStateChange: () => () => {},
  };
  const result = await runSessionNotifications(hub, { ref, observeDurationMs: 0 });
  assert.equal(result.outcome, "read");
  assert.equal(calls.length, 3);
  assert.deepEqual(calls[0].params, { ref, includeTurns: false, subscribe: true });
  assert.deepEqual(calls[2].params, { ref });
});

test("captures correlated events and marks a connection interruption uncertain", async () => {
  const listeners = new Set();
  const states = new Set();
  let reads = 0;
  const hub = {
    connect: async () => {},
    request: async (method) => {
      if (method === "thread/unsubscribe") return {};
      reads += 1;
      return { thread: { id: "thread", evener: { ref, capabilities: {} } } };
    },
    onNotification: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    onStateChange: (listener) => {
      states.add(listener);
      return () => states.delete(listener);
    },
  };
  const result = await runSessionNotifications(hub, {
    ref,
    observe: async () => {
      for (const listener of listeners) {
        listener({ method: "thread/status/changed", params: { ...base, status: { type: "busy" } } });
        listener({
          method: "thread/status/changed",
          params: { ...base, ref: "local:other", status: { type: "idle" } },
        });
      }
      for (const listener of states) listener("closed");
    },
    observeDurationMs: 0,
  });
  assert.equal(reads, 2);
  assert.equal(result.eventCount, 1);
  assert.equal(result.events[0].status, "busy");
  assert.equal(result.connectionInterrupted, true);
  assert.equal(result.outcome, "uncertain");
});

test("CLI reserves private output, emits metadata only, and leaves hub closure to its entry", async () => {
  const root = await mkdtemp(join(tmpdir(), "session-notifications-"));
  const outputPath = join(root, "result.json");
  let closed = 0;
  const hub = {
    connect: async () => {},
    close: () => {
      closed += 1;
    },
    request: async (method) =>
      method === "thread/unsubscribe" ? {} : { thread: { id: "thread", evener: { ref, capabilities: {} } } },
    onNotification: () => () => {},
    onStateChange: () => () => {},
  };
  const lines = [];
  await runSessionNotificationsCLI(
    {
      EVENER_THREAD_REF: ref,
      EVENER_SESSION_NOTIFICATIONS_OUTPUT_FILE: outputPath,
      EVENER_SESSION_NOTIFICATIONS_OBSERVE_MS: "0",
    },
    hub,
    { log: (line) => lines.push(line) },
  );
  assert.equal(closed, 0);
  assert.equal((await stat(outputPath)).mode & 0o777, 0o600);
  assert.equal(JSON.parse(await readFile(outputPath, "utf8")).initial.thread.evener.ref, ref);
  assert.deepEqual(Object.keys(JSON.parse(lines[0])).sort(), [
    "changedDuringReadback",
    "connectionInterrupted",
    "eventCount",
    "outcome",
    "outputWritten",
    "overflow",
  ]);
  await rm(root, { recursive: true, force: true });
});

test("accepts clearing the vision model and rejects malformed queue depth", () => {
  assert.equal(
    decodeNotification({ method: "thread/vision-model/changed", params: { ...base, visionModel: "" } }, ref)
      .visionModel,
    "",
  );
  assert.throws(() =>
    decodeNotification({ method: "thread/queueChanged", params: { ...base, queue: { revision: 1, depth: -1 } } }, ref),
  );
});

test("cleans up a subscription even when its read response fails", async () => {
  const calls = [];
  const hub = {
    connect: async () => {},
    onNotification: () => () => {},
    onStateChange: () => () => {},
    request: async (method) => {
      calls.push(method);
      if (method === "thread/read") throw new Error("read response failed");
      return {};
    },
  };
  await assert.rejects(runSessionNotifications(hub, { ref, observeDurationMs: 0 }), /read response failed/);
  assert.deepEqual(calls, ["thread/read", "thread/unsubscribe"]);
});

test("CLI refuses occupied output before connecting", async () => {
  const root = await mkdtemp(join(tmpdir(), "session-notifications-occupied-"));
  const outputPath = join(root, "result.json");
  await writeFile(outputPath, "owned content");
  let connects = 0;
  try {
    await assert.rejects(
      runSessionNotificationsCLI(
        { EVENER_THREAD_REF: ref, EVENER_SESSION_NOTIFICATIONS_OUTPUT_FILE: outputPath },
        {
          connect: async () => {
            connects += 1;
          },
        },
      ),
    );
    assert.equal(connects, 0);
    assert.equal(await readFile(outputPath, "utf8"), "owned content");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
