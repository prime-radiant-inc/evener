import assert from "node:assert/strict";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
function params(value) {
  assert.ok(value === undefined || record(value), "Update parameters must be an object.");
  assert.ok(value === undefined || Object.keys(value).every((key) => key === "channel"), "Unknown update parameter.");
  if (value?.channel !== undefined) assert.equal(typeof value.channel, "string", "Invalid update channel.");
  return value ? structuredClone(value) : {};
}
function decodeCheck(value) {
  assert.ok(record(value) && typeof value.channel === "string" && typeof value.buildChannel === "string");
  assert.equal(typeof value.updateAvailable, "boolean");
  assert.equal(typeof value.applicable, "boolean");
  return value;
}
function decodeApply(value) {
  assert.ok(record(value) && typeof value.release === "string" && typeof value.channel === "string");
  assert.ok(Array.isArray(value.installed) && typeof value.restarting === "boolean");
  return value;
}
export async function runUpdate(hub, { action = "check", params: authored, ownedHub } = {}) {
  const input = params(authored);
  if (action === "check") {
    await hub.connect();
    return { outcome: "read", readback: decodeCheck(await hub.request("evener/update/check", input)) };
  }
  assert.equal(action, "apply", "Unknown update action.");
  requireOwnedHub(process.env.EVENER_UPDATE_MUTATION, ownedHub);
  await hub.connect();
  const result = await mutateAndReadback(hub, "evener/update/apply", input, decodeApply, async () =>
    decodeCheck(await hub.request("evener/update/check", input)),
  );
  return { ...result, execution: "unverified" };
}
