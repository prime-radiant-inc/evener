import assert from "node:assert/strict";

const isRecord = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const nonempty = (value) => typeof value === "string" && value.trim().length > 0;

function validateAuthResponse(value, provider) {
  assert.ok(isRecord(value), "Invalid auth test response.");
  assert.equal(value.provider, provider, "Auth test provider changed.");
  assert.ok(nonempty(value.status) && nonempty(value.message), "Invalid auth test response fields.");
  return structuredClone(value);
}

function validatePluginResponse(value) {
  assert.ok(isRecord(value), "Invalid plugin check response.");
  for (const key of ["updated", "errors"])
    assert.ok(
      value[key] === undefined || (Array.isArray(value[key]) && value[key].every(nonempty)),
      "Invalid plugin check response.",
    );
  return structuredClone(value);
}

function validateParams(action, params) {
  assert.ok(isRecord(params), "Provide maintenance check parameters.");
  if (action === "ping" || action === "plugin/checkNow") {
    assert.equal(Object.keys(params).length, 0, "This maintenance check takes no parameters.");
    return {};
  }
  assert.equal(action, "auth/test", "Invalid maintenance check action.");
  assert.deepEqual(Object.keys(params), ["provider"], "Auth test requires only provider.");
  assert.ok(nonempty(params.provider), "Provide an auth test provider.");
  return { provider: params.provider.trim() };
}

export async function runMaintenanceCheck(
  hub,
  {
    action = "ping",
    params = {},
    ownedHub,
    mutationOptIn = process.env.EVENER_MAINTENANCE_MUTATION,
    rpcUrl = process.env.EVENER_RPC_URL,
  } = {},
) {
  const input = validateParams(action, params);
  const mutation = action === "plugin/checkNow";
  const providerProbe = action === "auth/test";
  if (mutation || providerProbe) {
    assert.equal(mutationOptIn, "1", "Management mutation requires explicit opt-in.");
    assert.ok(nonempty(ownedHub), "Confirm ownership of the target hub.");
    assert.equal(ownedHub, rpcUrl, "Owned hub must match the connected endpoint.");
  }
  await hub.connect();
  const method = action === "ping" ? "ping" : action === "auth/test" ? "evener/auth/test" : "evener/plugin/checkNow";
  try {
    const response = await hub.request(method, input);
    const readback =
      action === "ping"
        ? response
        : action === "auth/test"
          ? validateAuthResponse(response, input.provider)
          : validatePluginResponse(response);
    if (action === "ping")
      assert.ok(isRecord(readback) && Object.keys(readback).length === 0, "Invalid ping response.");
    return {
      action,
      outcome: mutation ? "acknowledged" : "read",
      execution: "unverified",
      readback: structuredClone(readback),
    };
  } catch (error) {
    if (mutation || action === "auth/test") return { action, outcome: "uncertain", execution: "unverified", error };
    throw error;
  }
}

export function safeMaintenanceSummary(result) {
  const value = result.readback;
  return {
    action: result.action,
    outcome: result.outcome,
    execution: result.execution,
    ...(result.action === "plugin/checkNow"
      ? { updatedCount: value?.updated?.length ?? 0, errorCount: value?.errors?.length ?? 0 }
      : {}),
  };
}
