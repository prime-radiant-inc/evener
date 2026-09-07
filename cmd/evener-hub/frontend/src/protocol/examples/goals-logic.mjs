import assert from "node:assert/strict";
import { isDeepStrictEqual } from "node:util";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value) => typeof value === "string" && value.trim().length > 0;

function validateGoal(value) {
  if (value === null) return;
  assert.ok(record(value), "Provide the complete reviewed goal or null.");
  assert.ok(
    Object.keys(value).every((key) => ["objective", "status", "iterations"].includes(key)),
    "Unknown goal field.",
  );
  assert.ok(value.objective === undefined || typeof value.objective === "string", "Invalid goal objective.");
  assert.ok(
    text(value.status) && Number.isSafeInteger(value.iterations) && value.iterations >= 0,
    "Invalid goal state.",
  );
}
function decodeRead(value, ref, expectedInstanceId, threadId) {
  assert.ok(record(value) && record(value.thread) && record(value.thread.evener), "Invalid thread read.");
  const { thread } = value;
  assert.ok(text(thread.id) && text(thread.evener.instanceId), "Invalid thread identity.");
  assert.equal(thread.evener.ref, ref, "Thread reference changed.");
  if (expectedInstanceId !== undefined)
    assert.equal(thread.evener.instanceId, expectedInstanceId, "Thread instance changed.");
  if (threadId !== undefined) assert.equal(thread.id, threadId, "Thread identity changed.");
  validateGoal(thread.evener.goal ?? null);
  return value;
}

export async function runGoals(hub, { action = "list", params = {}, ownedHub } = {}) {
  assert.ok(typeof action === "string" && ["list", "set", "clear"].includes(action), "Invalid goal action.");
  assert.ok(record(params) && text(params.ref), "Provide goal parameters and reference.");
  const allowed =
    action === "list"
      ? ["ref"]
      : ["ref", "expectedInstanceId", "reviewedGoal", ...(action === "set" ? ["objective"] : [])];
  assert.ok(
    Object.keys(params).every((key) => allowed.includes(key)),
    "Unknown goal parameter.",
  );
  if (action !== "list") {
    requireOwnedHub(process.env.EVENER_GOAL_MUTATION, ownedHub);
    assert.ok(text(params.expectedInstanceId), "Provide the reviewed session instance.");
    assert.ok(Object.hasOwn(params, "reviewedGoal"), "Provide the reviewed goal, including null for none.");
    validateGoal(params.reviewedGoal);
    if (action === "set") assert.ok(text(params.objective), "Provide the authored objective.");
  }
  const input = structuredClone(params);
  await hub.connect();
  let threadId;
  const read = async () =>
    decodeRead(
      await hub.request("thread/read", { ref: input.ref, includeTurns: false, subscribe: false }),
      input.ref,
      input.expectedInstanceId,
      threadId,
    );
  const before = await read();
  if (action === "list") return { outcome: "read", execution: "unverified", readback: before };
  threadId = before.thread.id;
  assert.equal(before.thread.evener.capabilities?.goal, true, "Session cannot change its goal.");
  assert.ok(
    isDeepStrictEqual(before.thread.evener.goal ?? null, input.reviewedGoal),
    "Goal changed; review current state.",
  );
  let started;
  // goal/set has no instance, revision or mutation-ID precondition; this review is nonatomic.
  const result = await mutateAndReadback(
    hub,
    "goal/set",
    {
      ref: input.ref,
      objective: action === "set" ? input.objective : "",
    },
    (value) => {
      assert.ok(record(value) && typeof value.started === "boolean", "Invalid goal acknowledgment.");
      started = value.started;
    },
    read,
  );
  // Another goal or a completed goal can appear before readback. Neither authorizes replay.
  return { ...result, started, execution: "unverified" };
}
