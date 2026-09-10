import assert from "node:assert/strict";
import { test } from "node:test";
import { decodeStreamingNotification, runStreamingNotifications } from "./streaming-notifications-logic.mjs";

const ref = "local:one";
const identity = { threadId: "thread-1", ref };
const snapshot = (refValue = ref) => ({
  thread: {
    id: "thread-1",
    sessionId: "session-1",
    preview: "private",
    ephemeral: false,
    modelProvider: "provider",
    createdAt: 1,
    updatedAt: 2,
    status: { type: "active" },
    cwd: "/private",
    cliVersion: "test",
    source: "test",
    evener: { ref: refValue, instanceId: "instance-1", capabilities: {} },
    turns: [],
  },
});
const payloads = [
  {
    method: "item/reasoning/summaryTextDelta",
    params: { ...identity, turnId: "turn-1", itemId: "item-1", summaryIndex: 0, delta: "" },
  },
  {
    method: "item/toolOutput/delta",
    params: { ...identity, turnId: "turn-1", itemId: "item-2", callId: "call-1", delta: "output" },
  },
  { method: "warning", params: { ...identity, message: "careful", source: "provider", warning: { code: "x" } } },
  {
    method: "evener/thread/modelRetry",
    params: {
      ...identity,
      attempt: 1,
      maxAttempts: 2,
      delayMs: 10,
      groupElapsedMs: 20,
      attemptCap: 3,
      message: "retry",
    },
  },
  { method: "evener/steering/injected", params: { ...identity, text: "", images: [], source: "user", kind: "" } },
];

test("decodes the five generated streaming notification payloads and preserves empty optionals", () => {
  const decoded = payloads.map((event) => decodeStreamingNotification(event, { ref, threadId: "thread-1" }));
  assert.deepEqual(
    decoded.map((event) => event.method),
    payloads.map((event) => event.method),
  );
  assert.equal(decoded[0].delta, "");
  assert.equal(decoded[4].text, "");
  assert.equal(decoded[4].kind, "");
});

test("ignores unrelated and globally uncorrelated warnings", () => {
  assert.equal(decodeStreamingNotification({ method: "turn/completed", params: {} }, { ref }), null);
  assert.equal(decodeStreamingNotification({ method: "warning", params: { message: "global" } }, { ref }), null);
  assert.equal(
    decodeStreamingNotification(
      { method: "warning", params: { ...identity, message: "foreign" } },
      { ref: "local:other" },
    ),
    null,
  );
});

test("rejects malformed payloads and mismatched identities", () => {
  assert.throws(
    () =>
      decodeStreamingNotification(
        { method: "item/toolOutput/delta", params: { ...identity, itemId: "i", callId: "c" } },
        { ref },
      ),
    /Invalid tool output delta/,
  );
  assert.throws(
    () =>
      decodeStreamingNotification(
        {
          method: "evener/thread/modelRetry",
          params: { ...identity, attempt: 0, maxAttempts: 1, delayMs: 1, groupElapsedMs: 1, attemptCap: 1 },
        },
        { ref },
      ),
    /Invalid model retry/,
  );
  assert.equal(
    decodeStreamingNotification({ ...payloads[0], params: { ...payloads[0].params, ref: "local:other" } }, { ref }),
    null,
  );
});

test("accepts wall-clock retries and validates steering image items", () => {
  const retry = decodeStreamingNotification(
    {
      method: "evener/thread/modelRetry",
      params: { ...identity, attempt: 1, maxAttempts: 0, delayMs: 10, groupElapsedMs: 20, attemptCap: 0 },
    },
    { ref },
  );
  assert.equal(retry.maxAttempts, 0);
  const steering = decodeStreamingNotification(
    { method: "evener/steering/injected", params: { ...identity, images: [{ type: "image", data: "opaque" }] } },
    { ref },
  );
  assert.equal(steering.images[0].type, "image");
  assert.throws(
    () =>
      decodeStreamingNotification(
        { method: "evener/steering/injected", params: { ...identity, images: [{}] } },
        { ref },
      ),
    /Invalid steering images/,
  );
});

function workflowHub({ fail = null, onEvent = null, finalEvent = null, threadRef = ref, omitTurns = false } = {}) {
  const listeners = new Set();
  const states = new Set();
  const calls = [];
  let reads = 0;
  const hub = {
    calls,
    connect: async () => calls.push({ method: "connect" }),
    onNotification: (callback) => {
      listeners.add(callback);
      return () => listeners.delete(callback);
    },
    onStateChange: (callback) => {
      states.add(callback);
      return () => states.delete(callback);
    },
    emit: (event) => {
      for (const callback of [...listeners]) callback(event);
    },
    request: async (method, params) => {
      calls.push({ method, params });
      if (method === "thread/unsubscribe") return {};
      if (method !== "thread/read") throw new Error(`unexpected method ${method}`);
      reads += 1;
      if (fail === "read" || (fail === "readback" && reads > 1)) throw new Error("thread read failed");
      if (reads === 1) await onEvent?.(hub);
      if (reads > 1) await finalEvent?.(hub);
      const value = snapshot(threadRef);
      if (omitTurns) delete value.thread.turns;
      return value;
    },
    listenerCount: () => listeners.size + states.size,
  };
  return hub;
}

test("reads subscribed full turns before and after a bounded observation and unsubscribes", async () => {
  const hub = workflowHub({ onEvent: async (value) => value.emit(payloads[0]) });
  const result = await runStreamingNotifications(hub, { ref }, { observeDurationMs: 0 });
  assert.equal(result.eventCount, 1);
  assert.deepEqual(
    hub.calls.filter((call) => call.method === "thread/read").map((call) => call.params),
    [
      { ref, includeTurns: true, subscribe: true },
      { ref, includeTurns: true, subscribe: true },
    ],
  );
  assert.deepEqual(hub.calls.at(-1), { method: "thread/unsubscribe", params: { ref } });
  assert.equal(hub.listenerCount(), 0);
});

test("accepts a valid empty transcript whose wire response omits optional turns", async () => {
  const result = await runStreamingNotifications(workflowHub({ omitTurns: true }), { ref }, { observeDurationMs: 0 });
  assert.equal(result.initial.thread.turns, undefined);
});

test("cleans up after read errors and rejects a foreign authoritative snapshot", async () => {
  for (const options of [{ fail: "read" }, { fail: "readback" }, { threadRef: "local:other" }]) {
    const hub = workflowHub(options);
    await assert.rejects(runStreamingNotifications(hub, { ref }, { observeDurationMs: 0 }));
    assert.deepEqual(hub.calls.at(-1), { method: "thread/unsubscribe", params: { ref } });
    assert.equal(hub.listenerCount(), 0);
  }
});

test("rejects a malformed observed event and still unsubscribes", async () => {
  const hub = workflowHub({
    onEvent: async (value) => value.emit({ method: "warning", params: { ...identity, message: 7 } }),
  });
  await assert.rejects(runStreamingNotifications(hub, { ref }, { observeDurationMs: 0 }), /Invalid warning message/);
  assert.deepEqual(hub.calls.at(-1), { method: "thread/unsubscribe", params: { ref } });
  assert.equal(hub.listenerCount(), 0);
});

test("marks a notification arriving during final readback as uncertain", async () => {
  const hub = workflowHub({ finalEvent: async (value) => value.emit(payloads[1]) });
  const result = await runStreamingNotifications(hub, { ref }, { observeDurationMs: 0 });
  assert.equal(result.outcome, "uncertain");
  assert.equal(result.changedDuringReadback, true);
});

test("CLI reserves a new private output before connecting and prints metadata only", async () => {
  const { mkdtemp, readFile, rm, stat } = await import("node:fs/promises");
  const { join } = await import("node:path");
  const { runStreamingNotificationsCLI } = await import("./streaming-notifications-cli.mjs");
  const directory = await mkdtemp("/tmp/streaming-notifications-");
  const outputPath = join(directory, "result.json");
  const lines = [];
  await runStreamingNotificationsCLI(
    {
      EVENER_STREAMING_NOTIFICATIONS_OUTPUT_FILE: outputPath,
      EVENER_THREAD_REF: ref,
      EVENER_STREAMING_NOTIFICATIONS_OBSERVE_MS: "0",
    },
    workflowHub(),
    { log: (line) => lines.push(line) },
  );
  assert.equal((await stat(outputPath)).mode & 0o777, 0o600);
  const privateResult = JSON.parse(await readFile(outputPath, "utf8"));
  assert.equal(privateResult.initial.thread.evener.ref, ref);
  assert.deepEqual(
    Object.keys(JSON.parse(lines[0])).sort(),
    ["changedDuringReadback", "connectionInterrupted", "eventCount", "outcome", "outputWritten", "overflow"].sort(),
  );
  assert.equal(lines[0].includes("private"), false);
  await rm(directory, { recursive: true, force: true });
});

test("CLI refuses an occupied output before connecting", async () => {
  const { mkdtemp, rm, writeFile } = await import("node:fs/promises");
  const { join } = await import("node:path");
  const { runStreamingNotificationsCLI } = await import("./streaming-notifications-cli.mjs");
  const directory = await mkdtemp("/tmp/streaming-notifications-collision-");
  const outputPath = join(directory, "result.json");
  await writeFile(outputPath, "existing");
  const hub = workflowHub();
  await assert.rejects(
    runStreamingNotificationsCLI(
      { EVENER_STREAMING_NOTIFICATIONS_OUTPUT_FILE: outputPath, EVENER_THREAD_REF: ref },
      hub,
    ),
  );
  assert.deepEqual(hub.calls, []);
  await rm(directory, { recursive: true, force: true });
});

test("CLI refuses missing output before connecting", async () => {
  const hub = workflowHub();
  const { runStreamingNotificationsCLI } = await import("./streaming-notifications-cli.mjs");
  await assert.rejects(
    runStreamingNotificationsCLI({ EVENER_THREAD_REF: ref }, hub),
    /EVENER_STREAMING_NOTIFICATIONS_OUTPUT_FILE/,
  );
  assert.deepEqual(hub.calls, []);
});
