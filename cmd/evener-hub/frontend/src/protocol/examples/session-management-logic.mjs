import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const actions = {
  rename: { method: "evener/thread/name/set", capability: "rename", fields: ["name"] },
  compact: { method: "thread/compact/start", capability: "compact", fields: [] },
  clear: { method: "thread/clear", capability: "clear", fields: [] },
  shutdown: { method: "thread/shutdown", capability: "shutdown", fields: [] },
};
const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value) => typeof value === "string";
const nonBlank = (value) => text(value) && value.trim().length > 0;

function decodeThread(value, ref, expectedInstanceId, expectedThreadId) {
  assert.ok(record(value) && record(value.thread) && record(value.thread.evener), "Invalid thread read.");
  const { thread } = value;
  assert.ok(nonBlank(thread.id) && nonBlank(thread.evener.ref), "Invalid thread identity.");
  assert.equal(thread.evener.ref, ref, "Thread reference changed.");
  if (expectedInstanceId !== undefined)
    assert.equal(thread.evener.instanceId, expectedInstanceId, "Thread instance changed.");
  if (expectedThreadId !== undefined) assert.equal(thread.id, expectedThreadId, "Thread identity changed.");
  assert.ok(thread.evener.instanceId === undefined || nonBlank(thread.evener.instanceId), "Invalid session instance.");
  assert.ok(thread.name === undefined || text(thread.name), "Invalid session name.");
  const capabilities = thread.evener.capabilities;
  assert.ok(
    capabilities === undefined ||
      (record(capabilities) &&
        Object.values(actions).every(
          ({ capability }) => capabilities[capability] === undefined || typeof capabilities[capability] === "boolean",
        )),
    "Invalid session capabilities.",
  );
  return structuredClone(value);
}

function reviewState(thread) {
  if (!nonBlank(thread.evener.instanceId)) return undefined;
  return {
    threadId: thread.id,
    instanceId: thread.evener.instanceId,
    ...(Object.hasOwn(thread, "name") ? { name: thread.name } : {}),
  };
}

function validateParams(action, params) {
  assert.ok(Object.hasOwn(actions, action), "Invalid session management action.");
  assert.ok(record(params), "Provide session management parameters.");
  const allowed = ["ref", "expectedInstanceId", "reviewed", ...actions[action].fields];
  assert.ok(
    Object.keys(params).every((key) => allowed.includes(key)),
    "Unknown session management parameter.",
  );
  assert.ok(nonBlank(params.ref) && nonBlank(params.expectedInstanceId), "Provide the session reference and instance.");
  assert.ok(record(params.reviewed), "Provide the reviewed session state.");
  assert.ok(
    nonBlank(params.reviewed.threadId) && nonBlank(params.reviewed.instanceId),
    "Provide reviewed session identity.",
  );
  assert.ok(
    Object.keys(params.reviewed).every((key) => ["threadId", "instanceId", "name"].includes(key)),
    "Unknown reviewed session field.",
  );
  if (action === "rename") assert.ok(text(params.name), "Session name must be text.");
  return structuredClone(params);
}

function decodeEmpty(value) {
  assert.ok(record(value) && Object.keys(value).length === 0, "Invalid session management acknowledgment.");
}

export async function runSessionManagement(hub, { action = "list", params = {}, ownedHub } = {}) {
  if (action === "list") {
    assert.ok(record(params) && nonBlank(params.ref), "Provide the session reference.");
    assert.ok(
      Object.keys(params).every((key) => key === "ref"),
      "Unknown session read parameter.",
    );
    const ref = params.ref;
    await hub.connect();
    const readback = decodeThread(
      await hub.request("thread/read", { ref, includeTurns: false, subscribe: false }),
      ref,
    );
    return { outcome: "read", execution: "unverified", readback, review: reviewState(readback.thread) };
  }

  requireOwnedHub(process.env.EVENER_SESSION_MANAGEMENT_MUTATION, ownedHub);
  const input = validateParams(action, params);
  await hub.connect();
  const before = decodeThread(
    await hub.request("thread/read", { ref: input.ref, includeTurns: false, subscribe: false }),
    input.ref,
    input.expectedInstanceId,
    input.reviewed.threadId,
  );
  assert.deepEqual(reviewState(before.thread), input.reviewed, "Session state changed; review current state.");
  assert.equal(
    before.thread.evener.capabilities?.[actions[action].capability],
    true,
    "Session control is unavailable.",
  );

  if (action === "clear") {
    const clientMutationId = randomUUID();
    let response;
    const result = await mutateAndReadback(
      hub,
      actions[action].method,
      { ref: input.ref, clientMutationId, expectedInstanceId: input.expectedInstanceId },
      (value) => {
        assert.ok(record(value) && record(value.thread) && record(value.receipt), "Invalid clear response.");
        decodeThread(value, input.ref);
        assert.ok(nonBlank(value.thread.evener.instanceId), "Invalid replacement instance.");
        assert.equal(value.ref, input.ref, "Clear response reference changed.");
        const receipt = value.receipt;
        assert.deepEqual(
          Object.keys(receipt).sort(),
          ["clientMutationId", "disposition", "instanceId", "projectionState", "threadId"].sort(),
          "Invalid clear receipt fields.",
        );
        assert.equal(receipt.clientMutationId, clientMutationId, "Clear receipt mutation changed.");
        assert.ok(["applied", "replayed"].includes(receipt.disposition), "Clear receipt was not applied.");
        assert.equal(receipt.threadId, value.thread.id, "Clear receipt thread changed.");
        assert.equal(receipt.instanceId, value.thread.evener.instanceId, "Clear receipt instance changed.");
        assert.equal(receipt.projectionState, "reflected", "Clear receipt projection state changed.");
        response = structuredClone(value);
      },
      async () => {
        // The clear response is the atomic replacement snapshot; a second read could observe a later session.
        return response;
      },
    );
    return { ...result, readback: result.readback, execution: "unverified", clientMutationId };
  }

  if (action === "shutdown") {
    let acknowledged = false;
    try {
      decodeEmpty(await hub.request(actions[action].method, { ref: input.ref }));
      acknowledged = true;
    } catch (error) {
      return { outcome: "uncertain", execution: "unverified", error };
    }
    return { outcome: acknowledged ? "acknowledged" : "uncertain", execution: "unverified", readback: "unavailable" };
  }

  const result = await mutateAndReadback(
    hub,
    actions[action].method,
    { ref: input.ref, ...(action === "rename" ? { name: input.name } : {}) },
    decodeEmpty,
    async () =>
      decodeThread(
        await hub.request("thread/read", { ref: input.ref, includeTurns: false, subscribe: false }),
        input.ref,
        input.expectedInstanceId,
        input.reviewed.threadId,
      ),
  );
  return { ...result, execution: "unverified" };
}

export function safeSessionManagementSummary(result) {
  return {
    outcome: result.outcome,
    execution: result.execution,
    readback: result.readback === "unavailable" ? "unavailable" : result.readback ? "present" : "absent",
    review: result.review ? "present" : "absent",
  };
}
