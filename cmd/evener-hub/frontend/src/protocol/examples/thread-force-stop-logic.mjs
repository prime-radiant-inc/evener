import assert from "node:assert/strict";
import { requireOwnedHub } from "./management-recovery.mjs";

export async function runThreadForceStop(hub, { ref, ownedHub } = {}) {
  requireOwnedHub(process.env.EVENER_THREAD_FORCE_STOP_MUTATION, ownedHub);
  assert.equal(typeof ref, "string");
  assert.ok(ref.trim().length > 0, "Provide the session reference.");
  await hub.connect();
  await hub.forceStop(ref);
  return { outcome: "acknowledged", execution: "unverified" };
}
