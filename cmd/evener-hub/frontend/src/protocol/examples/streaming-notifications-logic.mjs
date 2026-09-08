import assert from "node:assert/strict";
import { observeNotifications } from "./bounded-notifications.mjs";

export const METHODS = [
  "item/reasoning/summaryTextDelta",
  "item/toolOutput/delta",
  "warning",
  "evener/thread/modelRetry",
  "evener/steering/injected",
];
const METHOD_SET = new Set(METHODS);
const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value) => typeof value === "string";
const nonempty = (value) => text(value) && value.trim() !== "";
const integer = (value, minimum = 0) => Number.isSafeInteger(value) && value >= minimum;
const has = (value, key) => Object.hasOwn(value, key);
const inputItem = (value) => record(value) && nonempty(value.type);

function validateIdentity(params, ref, threadId) {
  if (!record(params)) return null;
  if (!has(params, "ref") && !has(params, "threadId")) return null;
  assert.ok(nonempty(params.ref) && nonempty(params.threadId), "Invalid streaming notification identity.");
  if (params.ref !== ref || (threadId !== undefined && params.threadId !== threadId)) return null;
  return { ref: params.ref, threadId: params.threadId };
}

function decodeReasoning(params, identity) {
  assert.ok(
    nonempty(params.turnId) && nonempty(params.itemId) && integer(params.summaryIndex) && text(params.delta),
    "Invalid reasoning summary delta.",
  );
  return {
    ...identity,
    turnId: params.turnId,
    itemId: params.itemId,
    summaryIndex: params.summaryIndex,
    delta: params.delta,
  };
}

function decodeToolOutput(params, identity) {
  assert.ok(nonempty(params.itemId) && nonempty(params.callId) && text(params.delta), "Invalid tool output delta.");
  if (has(params, "turnId")) assert.ok(nonempty(params.turnId), "Invalid tool output turn.");
  return {
    ...identity,
    ...(has(params, "turnId") ? { turnId: params.turnId } : {}),
    itemId: params.itemId,
    callId: params.callId,
    delta: params.delta,
  };
}

function decodeWarning(params, identity) {
  for (const key of ["message", "source", "title", "hint"])
    if (has(params, key)) assert.ok(text(params[key]), `Invalid warning ${key}.`);
  return {
    ...identity,
    ...(has(params, "message") ? { message: params.message } : {}),
    ...(has(params, "source") ? { source: params.source } : {}),
    ...(has(params, "title") ? { title: params.title } : {}),
    ...(has(params, "hint") ? { hint: params.hint } : {}),
    ...(has(params, "warning") ? { warning: structuredClone(params.warning) } : {}),
    ...(has(params, "cause") ? { cause: structuredClone(params.cause) } : {}),
  };
}

function decodeModelRetry(params, identity) {
  const wallClockRetry = params.maxAttempts === 0 && params.attemptCap === 0;
  assert.ok(
    integer(params.attempt, 1) &&
      integer(params.maxAttempts) &&
      (wallClockRetry || (params.maxAttempts >= params.attempt && integer(params.attemptCap, 1))) &&
      integer(params.delayMs) &&
      integer(params.groupElapsedMs),
    "Invalid model retry.",
  );
  for (const key of ["turnId", "errorClass", "message", "model"])
    if (has(params, key)) assert.ok(text(params[key]), `Invalid model retry ${key}.`);
  if (has(params, "statusCode")) assert.ok(integer(params.statusCode), "Invalid model retry status code.");
  return structuredClone({ method: "evener/thread/modelRetry", ...identity, ...params });
}

function decodeSteering(params, identity) {
  for (const key of ["text", "source", "kind", "clientMutationId"])
    if (has(params, key)) assert.ok(text(params[key]), `Invalid steering ${key}.`);
  if (has(params, "startedAt")) assert.ok(integer(params.startedAt), "Invalid steering timestamp.");
  if (has(params, "images"))
    assert.ok(Array.isArray(params.images) && params.images.every(inputItem), "Invalid steering images.");
  return structuredClone({ method: "evener/steering/injected", ...identity, ...params });
}

export function decodeStreamingNotification(notification, { ref, threadId } = {}) {
  if (!record(notification) || !METHOD_SET.has(notification.method)) return null;
  const params = notification.params;
  const identity = validateIdentity(params, ref, threadId);
  if (identity === null) return null;
  switch (notification.method) {
    case "item/reasoning/summaryTextDelta":
      return { method: notification.method, ...decodeReasoning(params, identity) };
    case "item/toolOutput/delta":
      return { method: notification.method, ...decodeToolOutput(params, identity) };
    case "warning":
      return { method: notification.method, ...decodeWarning(params, identity) };
    case "evener/thread/modelRetry":
      return decodeModelRetry(params, identity);
    case "evener/steering/injected":
      return decodeSteering(params, identity);
    default:
      return null;
  }
}

function readThread(response, ref) {
  assert.ok(record(response) && record(response.thread), "Invalid thread read response.");
  const thread = response.thread;
  assert.ok(
    nonempty(thread.id) && record(thread.evener) && thread.evener.ref === ref,
    "Thread read belongs to another session.",
  );
  if (thread.turns !== undefined) assert.ok(Array.isArray(thread.turns), "Thread read returned invalid turns.");
  return structuredClone(response);
}

export async function runStreamingNotifications(
  hub,
  input,
  { observe = null, observeDurationMs = 1000, maxEvents = 100 } = {},
) {
  assert.ok(
    record(input) && Object.keys(input).every((key) => key === "ref") && nonempty(input.ref),
    "Provide a streaming notification reference.",
  );
  const captured = structuredClone(input);
  let canonicalThreadId;
  let subscriptionAttempted = false;
  const readSnapshot = async () => {
    subscriptionAttempted = true;
    const result = readThread(
      await hub.request("thread/read", { ref: captured.ref, includeTurns: true, subscribe: true }),
      captured.ref,
    );
    const threadId = result.thread.id;
    if (canonicalThreadId !== undefined)
      assert.equal(threadId, canonicalThreadId, "Thread identity changed during observation.");
    canonicalThreadId = threadId;
    return result;
  };
  try {
    const result = await observeNotifications(hub, {
      methods: METHODS,
      decodeNotification: (notification) =>
        decodeStreamingNotification(notification, { ref: captured.ref, threadId: canonicalThreadId }),
      readSnapshot,
      observe,
      observeDurationMs,
      maxEvents,
    });
    return { ref: captured.ref, ...result };
  } finally {
    if (subscriptionAttempted) await hub.request("thread/unsubscribe", { ref: captured.ref }).catch(() => {});
  }
}

export function summarizeStreamingNotifications(result) {
  return {
    outcome: result.outcome,
    eventCount: result.eventCount,
    overflow: result.overflow,
    changedDuringReadback: result.changedDuringReadback,
    connectionInterrupted: result.connectionInterrupted,
  };
}
