import assert from "node:assert/strict";
import { isDeepStrictEqual } from "node:util";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const cardStrings = ["threadId", "ref", "escalationId", "mode", "tool", "kind", "deniedPath"];
const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const validText = (value) => typeof value === "string" && value.trim().length > 0;
function validateCard(card) {
  assert.ok(record(card), "Provide the complete reviewed approval.");
  assert.ok(
    Object.keys(card).every((key) => [...cardStrings, "command", "outputSoFar", "partiallyRan"].includes(key)),
    "Unknown approval field.",
  );
  assert.ok(
    cardStrings.every((key) => typeof card[key] === "string"),
    "Incomplete approval card.",
  );
  assert.ok(
    ["threadId", "ref", "escalationId"].every((key) => validText(card[key])),
    "Invalid approval identity.",
  );
  for (const key of ["command", "outputSoFar"])
    assert.ok(card[key] === undefined || typeof card[key] === "string", "Invalid approval detail.");
  assert.ok(
    card.partiallyRan === undefined || typeof card.partiallyRan === "boolean",
    "Invalid approval execution state.",
  );
}
function decodeRead(value, ref, expectedInstanceId) {
  assert.ok(record(value) && record(value.thread) && record(value.thread.evener), "Invalid thread read.");
  const evener = value.thread.evener;
  assert.equal(evener.ref, ref, "Thread reference changed.");
  if (expectedInstanceId !== undefined) assert.equal(evener.instanceId, expectedInstanceId, "Thread instance changed.");
  const pending = evener.pendingEscalations === undefined ? [] : evener.pendingEscalations;
  assert.ok(Array.isArray(pending), "Invalid pending approvals.");
  for (const card of pending) validateCard(card);
  return value;
}

export async function runApprovals(hub, { action = "list", params = {}, ownedHub } = {}) {
  assert.ok(action === "list" || action === "resolve", "Unknown approval action.");
  assert.ok(record(params), "Provide approval parameters.");
  const allowed = action === "list" ? ["ref"] : ["ref", "expectedInstanceId", "escalation", "approve"];
  assert.ok(
    Object.keys(params).every((key) => allowed.includes(key)),
    "Unknown approval parameter.",
  );
  assert.ok(validText(params.ref), "Provide the session reference.");
  if (action === "resolve") {
    requireOwnedHub(process.env.EVENER_APPROVAL_MUTATION, ownedHub);
    assert.ok(validText(params.expectedInstanceId), "Provide the reviewed session instance.");
    assert.equal(typeof params.approve, "boolean", "Provide an explicit decision.");
    validateCard(params.escalation);
    assert.equal(params.escalation.ref, params.ref, "Approval reference does not match.");
  }
  // Keep the reviewed decision stable while connection and preflight are pending.
  const input = structuredClone(params);
  const read = async () =>
    decodeRead(
      await hub.request("thread/read", {
        ref: input.ref,
        includeTurns: false,
        subscribe: false,
      }),
      input.ref,
      input.expectedInstanceId,
    );
  await hub.connect();
  const before = await read();
  if (action === "list") return { outcome: "read", execution: "unverified", readback: before };
  assert.ok(
    (before.thread.evener.pendingEscalations ?? []).some((card) => isDeepStrictEqual(card, input.escalation)),
    "Approval changed; review current state.",
  );
  const result = await mutateAndReadback(
    hub,
    "evener/sandbox/escalation/resolve",
    {
      ref: input.ref,
      escalationId: input.escalation.escalationId,
      approve: input.approve,
    },
    (value) => assert.ok(record(value), "Invalid approval acknowledgment."),
    read,
  );
  // A decision acknowledgment or absent card does not prove tool execution resumed.
  return { ...result, execution: "unverified" };
}
