import assert from "node:assert/strict";
import { decodeStatus } from "./credentials-status.mjs";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const operations = {
  "apiKey/set": { method: "evener/auth/apiKey/set", mode: "apiKey", value: true },
  "apiKey/clear": { method: "evener/auth/apiKey/clear", mode: null, value: false },
  "credentialJson/set": { method: "evener/auth/credentialJson/set", mode: "credentialJson", value: true },
  logout: { method: "evener/auth/logout", mode: null, value: false },
};
const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const nonempty = (value) => typeof value === "string" && value.trim().length > 0;

function decodeList(value) {
  assert.ok(
    isRecord(value) && (value.providers === null || Array.isArray(value.providers)),
    "Invalid credential list.",
  );
  if (value.providers === null) return [];
  const seen = new Set();
  return value.providers.map((entry) => {
    const decoded = decodeStatus(entry, entry?.provider);
    assert.ok(!seen.has(decoded.provider), "Invalid credential list: duplicate credential provider.");
    seen.add(decoded.provider);
    return decoded;
  });
}

function validateParams(action, params) {
  assert.ok(isRecord(params), "Provide credential parameters.");
  const allowed =
    action === "status"
      ? ["provider"]
      : operations[action]?.value
        ? ["provider", "value", "reviewed"]
        : ["provider", "reviewed"];
  assert.ok(
    Object.keys(params).every((key) => allowed.includes(key)),
    "Unknown credential parameter.",
  );
  assert.ok(nonempty(params.provider), "Provide a provider.");
  if (operations[action]?.value) assert.ok(nonempty(params.value), "Provide credential data in the params file.");
  if (action !== "status") assert.ok(params.reviewed !== undefined, "Provide a complete reviewed credential state.");
  if (params.reviewed !== undefined) decodeStatus(params.reviewed, params.provider);
  return structuredClone(params);
}

function assertReviewed(status, reviewed) {
  assert.deepEqual(status, decodeStatus(reviewed, status.provider), "Credential state changed; review current state.");
}

function assertMode(status, mode) {
  if (mode !== null) assert.ok(status.authModes?.includes(mode), `Provider does not support ${mode} credentials.`);
}

function decodeMutation(action, value, provider) {
  if (action === "logout") {
    assert.ok(isRecord(value) && typeof value.removed === "boolean", "Invalid logout acknowledgment.");
    return { removed: value.removed, status: decodeStatus(value.status, provider) };
  }
  return decodeStatus(value, provider);
}

export async function runCredentials(hub, { action = "list", params = {}, ownedHub } = {}) {
  if (action === "list") {
    assert.ok(isRecord(params) && Object.keys(params).length === 0, "Credential list takes no parameters.");
    await hub.connect();
    return {
      action,
      outcome: "read",
      execution: "unverified",
      readback: decodeList(await hub.request("evener/auth/list", {})),
    };
  }
  if (action === "status") {
    const input = validateParams(action, params);
    await hub.connect();
    return {
      action,
      outcome: "read",
      execution: "unverified",
      readback: decodeStatus(await hub.request("evener/auth/status", { provider: input.provider }), input.provider),
    };
  }
  assert.ok(Object.hasOwn(operations, action), "Unknown credential action.");
  requireOwnedHub(process.env.EVENER_CREDENTIAL_MUTATION, ownedHub);
  const input = validateParams(action, params);
  const provider = input.provider;
  await hub.connect();
  const before = decodeStatus(await hub.request("evener/auth/status", { provider }), provider);
  assert.equal(before.supported, true, "Provider is not supported by this hub.");
  assertReviewed(before, input.reviewed);
  assertMode(before, operations[action].mode);
  const read = async () => decodeStatus(await hub.request("evener/auth/status", { provider }), provider);
  const mutationParams = { provider, ...(operations[action].value ? { value: input.value } : {}) };
  const decoded = (value) => decodeMutation(action, value, provider);
  const result = await mutateAndReadback(hub, operations[action].method, mutationParams, decoded, read);
  return { action, ...result, execution: "unverified" };
}

export function safeCredentialSummary(result) {
  if (Array.isArray(result.readback))
    return {
      action: result.action,
      outcome: result.outcome,
      execution: result.execution,
      providerCount: result.readback.length,
    };
  const status = result.readback;
  return {
    action: result.action,
    outcome: result.outcome,
    execution: result.execution,
    supported: status?.supported,
    signedIn: status?.signedIn,
  };
}
