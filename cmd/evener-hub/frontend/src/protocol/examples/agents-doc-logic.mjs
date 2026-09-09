import assert from "node:assert/strict";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
export function decodeAgentsDoc(value) {
  assert.ok(
    record(value) &&
      typeof value.path === "string" &&
      typeof value.exists === "boolean" &&
      typeof value.content === "string",
    "Invalid agents document.",
  );
  return value;
}
export async function runAgentsDoc(hub, { action = "get", params = {}, ownedHub } = {}) {
  assert.ok(record(params), "Agents document parameters must be an object.");
  if (action === "get") {
    assert.deepEqual(Object.keys(params), []);
    await hub.connect();
    return { outcome: "read", readback: decodeAgentsDoc(await hub.request("evener/settings/agentsDoc/get", {})) };
  }
  assert.equal(action, "set", "Unknown agents document action.");
  requireOwnedHub(process.env.EVENER_AGENTS_DOC_MUTATION, ownedHub);
  assert.deepEqual(Object.keys(params).sort(), ["content", "reviewed"]);
  assert.equal(typeof params.content, "string", "Document content must be text.");
  const input = structuredClone(params);
  assert.deepEqual(Object.keys(input.reviewed).sort(), ["content", "exists", "path"]);
  await hub.connect();
  const before = decodeAgentsDoc(await hub.request("evener/settings/agentsDoc/get", {}));
  assert.deepEqual(before, input.reviewed, "Document changed; review current state.");
  return {
    ...(await mutateAndReadback(
      hub,
      "evener/settings/agentsDoc/set",
      { content: input.content },
      decodeAgentsDoc,
      async () => decodeAgentsDoc(await hub.request("evener/settings/agentsDoc/get", {})),
    )),
    execution: "unverified",
    reviewed: before,
  };
}
