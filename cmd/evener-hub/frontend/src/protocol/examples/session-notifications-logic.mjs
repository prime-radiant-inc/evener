import assert from "node:assert/strict";
import { observeNotifications } from "./bounded-notifications.mjs";

const METHODS = [
  "thread/started",
  "thread/closed",
  "thread/status/changed",
  "thread/queueChanged",
  "evener/thread/name/changed",
  "thread/model/changed",
  "thread/reasoning-effort/changed",
  "thread/vision-model/changed",
];
const rec = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const text = (v) => typeof v === "string" && v.trim() !== "";
function readSnapshot(value, ref) {
  assert.ok(rec(value) && rec(value.thread), "Invalid thread snapshot.");
  const t = value.thread;
  assert.ok(text(t.id), "Thread snapshot omitted id.");
  assert.ok(rec(t.evener) && t.evener.ref === ref, "Thread snapshot ref mismatch.");
  return {
    ref,
    threadId: t.id,
    thread: structuredClone(t),
    status: t.status?.type ?? null,
    instanceId: t.evener.instanceId,
    capabilities: t.evener.capabilities ? Object.keys(t.evener.capabilities).sort() : [],
  };
}
export function decodeNotification(notification, ref) {
  if (!rec(notification) || !METHODS.includes(notification.method)) return null;
  const p = notification.params;
  assert.ok(rec(p) && text(p.ref) && text(p.threadId), "Invalid session notification identity.");
  if (p.ref !== ref) return null;
  const out = { method: notification.method, ref: p.ref, threadId: p.threadId };
  switch (notification.method) {
    case "thread/started":
      assert.ok(rec(p.thread) && p.thread.id === p.threadId, "thread/started identity mismatch.");
      assert.ok(rec(p.thread.evener) && p.thread.evener.ref === ref, "thread/started ref mismatch.");
      break;
    case "thread/closed":
      if (p.reason !== undefined) assert.equal(typeof p.reason, "string");
      out.reason = p.reason ?? "";
      break;
    case "thread/status/changed":
      assert.ok(rec(p.status) && text(p.status.type), "Invalid thread status.");
      if (p.status.activeFlags !== undefined) {
        assert.ok(Array.isArray(p.status.activeFlags), "Invalid active flags.");
        assert.ok(p.status.activeFlags.every(text), "Invalid active flag.");
      }
      out.status = p.status.type;
      break;
    case "thread/queueChanged":
      assert.ok(
        rec(p.queue) && Number.isSafeInteger(p.queue.revision) && p.queue.revision >= 0,
        "Invalid thread queue.",
      );
      if (p.queue.depth !== undefined)
        assert.ok(Number.isSafeInteger(p.queue.depth) && p.queue.depth >= 0, "Invalid queue depth.");
      for (const key of ["preview", "ids", "clientMutationIds", "texts"]) {
        if (p.queue[key] !== undefined) {
          assert.ok(
            Array.isArray(p.queue[key]) && p.queue[key].every((item) => typeof item === "string"),
            `Invalid queue ${key}.`,
          );
        }
      }
      out.queueKeys = Object.keys(p.queue).sort();
      break;
    case "evener/thread/name/changed":
      assert.ok(text(p.name), "Invalid thread name.");
      out.nameLength = [...p.name].length;
      break;
    case "thread/model/changed":
      assert.ok(text(p.modelProvider) && text(p.model), "Invalid thread model.");
      if (p.reasoningEffortLevels !== undefined) {
        assert.ok(
          Array.isArray(p.reasoningEffortLevels) && p.reasoningEffortLevels.every(text),
          "Invalid reasoning effort levels.",
        );
      }
      if (p.supportsReasoning !== undefined) assert.equal(typeof p.supportsReasoning, "boolean");
      out.modelProvider = p.modelProvider;
      out.model = p.model;
      break;
    case "thread/reasoning-effort/changed":
      if (p.reasoningEffort !== undefined) assert.equal(typeof p.reasoningEffort, "string");
      out.reasoningEffort = p.reasoningEffort ?? null;
      break;
    case "thread/vision-model/changed":
      assert.equal(typeof p.visionModel, "string", "Invalid vision model.");
      out.visionModel = p.visionModel;
      break;
  }
  return out;
}
export async function runSessionNotifications(
  hub,
  { ref, observe = null, observeDurationMs = 1000, maxEvents = 100 } = {},
) {
  assert.ok(text(ref), "Provide a session ref.");
  let subscribed = false;
  const snapshot = async () =>
    readSnapshot(await hub.request("thread/read", { ref, includeTurns: false, subscribe: true }), ref);
  try {
    const result = await observeNotifications(hub, {
      methods: METHODS,
      decodeNotification: (n) => decodeNotification(n, ref),
      readSnapshot: async () => {
        subscribed = true;
        return snapshot();
      },
      observe,
      observeDurationMs,
      maxEvents,
    });
    return {
      outcome: result.outcome,
      initial: result.initial,
      readback: result.readback,
      events: result.events,
      eventCount: result.eventCount,
      overflow: result.overflow,
      changedDuringReadback: result.changedDuringReadback,
      connectionInterrupted: result.connectionInterrupted,
    };
  } finally {
    if (subscribed) await hub.request("thread/unsubscribe", { ref }).catch(() => {});
  }
}
export { METHODS, readSnapshot };
