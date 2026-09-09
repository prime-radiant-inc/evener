import assert from "node:assert/strict";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const fields = {
  create: ["name", "base", "baseUrl", "protocol", "surface", "vars", "apiKeyEnv", "credentialHeader"],
  edit: [
    "name",
    "newName",
    "baseUrl",
    "clearBaseUrl",
    "protocol",
    "clearProtocol",
    "surface",
    "clearSurface",
    "vars",
    "apiKeyEnv",
    "clearApiKeyEnv",
    "credentialHeader",
    "clearCredentialHeader",
  ],
  remove: ["name"],
  setDefault: ["name"],
};
const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const validText = (value) => typeof value === "string" && value.trim().length > 0;
function decodeInstances(value) {
  assert.ok(
    isRecord(value) && Array.isArray(value.instances) && Array.isArray(value.availableProviders),
    "Invalid instance list.",
  );
  assert.ok(
    value.instances.every((entry) => isRecord(entry) && validText(entry.name)),
    "Invalid instance identity.",
  );
  assert.ok(
    value.writesRefused === undefined || typeof value.writesRefused === "boolean",
    "Invalid instance write status.",
  );
  return value;
}
function checkedParams(action, params) {
  assert.ok(Object.hasOwn(fields, action), "Unknown instance action.");
  assert.ok(isRecord(params), "Provide instance parameters.");
  assert.ok(
    Object.keys(params).every((key) => fields[action].includes(key)),
    "Unknown instance parameter.",
  );
  assert.ok(validText(params.name), "Provide an instance name.");
  if (action === "create") assert.ok(validText(params.base), "Provide a base provider.");
  for (const [key, value] of Object.entries(params)) {
    if (key === "vars")
      assert.ok(
        isRecord(value) && Object.values(value).every((item) => typeof item === "string"),
        "vars must contain string values.",
      );
    else assert.equal(typeof value, key.startsWith("clear") ? "boolean" : "string", "Invalid instance parameter type.");
  }
  // Omission, an empty value, and clearBaseUrl are distinct authored inputs.
  return { ...params, ...(Object.hasOwn(params, "vars") ? { vars: { ...params.vars } } : {}) };
}

export async function runInstanceOperation(hub, { action = "list", params, ownedHub } = {}) {
  const read = async () => decodeInstances(await hub.request("evener/instance/list", {}));
  if (action === "list") {
    await hub.connect();
    return { outcome: "read", readback: await read() };
  }
  requireOwnedHub(process.env.EVENER_INSTANCE_MUTATION, ownedHub);
  const selected = checkedParams(action, params);
  await hub.connect();
  const before = await read();
  assert.notEqual(before.writesRefused, true, "Resolve the provider registry error before writing.");
  const present = before.instances.some((entry) => entry.name === selected.name);
  assert.ok(action === "create" ? !present : present, "Instance preflight does not match the selected action.");
  return mutateAndReadback(hub, `evener/instance/${action}`, selected, decodeInstances, read);
}
