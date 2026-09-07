import assert from "node:assert/strict";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const methods = {
  queue: "turn/queue",
  cancel: "turn/cancelQueued",
  promote: "turn/promoteQueuedAsSteer",
  drain: "turn/drainAsSteer",
};
const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value) => typeof value === "string" && value.trim().length > 0;
const integer = (value) => Number.isSafeInteger(value) && value >= 0;

function decodeRead(value, ref, expectedInstanceId, threadId) {
  assert.ok(record(value) && record(value.thread) && record(value.thread.evener), "Invalid thread read.");
  const { thread } = value;
  assert.ok(text(thread.id) && text(thread.evener.instanceId), "Invalid thread identity.");
  assert.equal(thread.evener.ref, ref, "Thread reference changed.");
  if (expectedInstanceId !== undefined)
    assert.equal(thread.evener.instanceId, expectedInstanceId, "Thread instance changed.");
  if (threadId !== undefined) assert.equal(thread.id, threadId, "Thread identity changed.");
  const queue = thread.evener.queue;
  assert.ok(record(queue) && integer(queue.revision), "Invalid queue revision.");
  const depth = queue.depth ?? 0;
  assert.ok(queue.depth !== null && integer(depth), "Invalid queue depth.");
  for (const key of ["ids", "texts", "preview", "clientMutationIds"]) {
    if (queue[key] === undefined) continue;
    assert.ok(
      Array.isArray(queue[key]) &&
        queue[key].length === depth &&
        queue[key].every((value) => typeof value === "string"),
      "Invalid queue entries.",
    );
  }
  if (queue.ids !== undefined)
    assert.ok(queue.ids.every(text) && new Set(queue.ids).size === depth, "Invalid queue identities.");
  return value;
}

function decodeReceipt(value, action, input, before) {
  assert.ok(record(value) && record(value.receipt), "Invalid queue acknowledgment.");
  const receipt = value.receipt;
  assert.equal(receipt.clientMutationId, input.clientMutationId, "Mutation receipt changed.");
  assert.equal(receipt.threadId, before.thread.id, "Receipt thread changed.");
  assert.equal(receipt.instanceId, input.expectedInstanceId, "Receipt instance changed.");
  assert.ok(["applied", "replayed"].includes(receipt.disposition), "Invalid receipt disposition.");
  assert.equal(receipt.projectionState, action === "cancel" ? "removed" : "pending", "Invalid receipt projection.");
  const ids = receipt.queueEntryIds;
  assert.ok(
    Array.isArray(ids) && ids.length > 0 && ids.every(text) && new Set(ids).size === ids.length,
    "Invalid receipt queue identities.",
  );
  if (action === "drain") assert.deepEqual(ids, before.thread.evener.queue.ids, "Drained queue changed.");
  else if (action === "queue") assert.equal(ids.length, 1, "Invalid queued receipt.");
  else assert.deepEqual(ids, [input.expectedEntryId], "Receipt entry changed.");
  if (action === "promote" || action === "drain") assert.ok(text(receipt.turnId), "Missing receipt turn.");
  else assert.equal(receipt.turnId, undefined, "Unexpected receipt turn.");
  if (action === "cancel") {
    assert.equal(typeof value.removedText, "string", "Invalid cancellation echo.");
    assert.ok(value.removedImages === undefined || integer(value.removedImages), "Invalid removed image count.");
  }
  return receipt;
}

export async function runQueue(hub, { action = "list", params = {}, ownedHub } = {}) {
  assert.ok(
    typeof action === "string" && (action === "list" || Object.hasOwn(methods, action)),
    "Invalid queue action.",
  );
  assert.ok(record(params) && text(params.ref), "Provide queue parameters and reference.");
  const allowed =
    action === "list"
      ? ["ref"]
      : [
          "ref",
          "expectedInstanceId",
          "clientMutationId",
          ...(action === "queue"
            ? ["input"]
            : action === "drain"
              ? ["expectedQueueRevision"]
              : ["index", "expectedEntryId"]),
        ];
  assert.ok(
    Object.keys(params).every((key) => allowed.includes(key)),
    "Unknown queue parameter.",
  );
  if (action !== "list") {
    requireOwnedHub(process.env.EVENER_QUEUE_MUTATION, ownedHub);
    assert.ok(text(params.expectedInstanceId), "Provide the reviewed session instance.");
    assert.ok(
      text(params.clientMutationId) && params.clientMutationId === params.clientMutationId.trim(),
      "Provide a stable caller-authored mutation ID.",
    );
    if (action === "queue") {
      assert.ok(
        Array.isArray(params.input) &&
          params.input.length > 0 &&
          params.input.every(
            (item) =>
              record(item) &&
              Object.keys(item).every((key) => ["type", "text"].includes(key)) &&
              item.type === "text" &&
              text(item.text),
          ),
        "This recipe requires nonempty text inputs.",
      );
    } else if (action === "drain") {
      assert.ok(integer(params.expectedQueueRevision), "Provide the reviewed queue revision.");
    } else {
      assert.ok(integer(params.index) && text(params.expectedEntryId), "Provide the reviewed queue entry.");
    }
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
  const { capabilities, queue } = before.thread.evener;
  if (action === "queue") assert.equal(capabilities?.queue, true, "Session cannot queue input.");
  if (action === "promote" || action === "drain")
    assert.ok(capabilities?.steer === true || capabilities?.send === true, "Session cannot run queued input.");
  if (action === "cancel" || action === "promote")
    assert.ok(
      (queue.depth ?? 0) > input.index && queue.ids?.[input.index] === input.expectedEntryId,
      "Queue entry changed; review current state.",
    );
  if (action === "drain") {
    assert.ok((queue.depth ?? 0) > 0 && queue.ids?.length === queue.depth, "Review the complete nonempty queue.");
    assert.equal(queue.revision, input.expectedQueueRevision, "Queue revision changed; review current state.");
  }
  let receipt;
  const result = await mutateAndReadback(
    hub,
    methods[action],
    input,
    (value) => {
      receipt = decodeReceipt(value, action, input, before);
    },
    read,
  );
  // Pending input may already have been consumed. Readback never authorizes replay.
  return { ...result, receipt, execution: "unverified" };
}
