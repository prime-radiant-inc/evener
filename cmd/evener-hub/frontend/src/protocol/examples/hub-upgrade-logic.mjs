import assert from "node:assert/strict";

const record = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const text = (v) => typeof v === "string" && v.trim().length > 0;
function paramsFor(action, params) {
  assert.ok(["review", "upgrade"].includes(action), "Select review or upgrade.");
  assert.ok(record(params), "Provide upgrade parameters.");
  if (action === "review") {
    assert.deepEqual(Object.keys(params), [], "Review takes no parameters.");
    return { wire: {} };
  }
  assert.ok(
    Object.keys(params).every((k) => ["requested", "reviewed"].includes(k)),
    "Unknown upgrade parameter.",
  );
  if (params.requested !== undefined) assert.ok(text(params.requested), "Provide a non-empty requested release.");
  assert.ok(record(params.reviewed), "Review the running hub identity before upgrading.");
  assert.ok(
    Object.keys(params.reviewed).every((key) => ["commit", "version"].includes(key)),
    "Review exact running identity.",
  );
  assert.ok(text(params.reviewed.version), "Review exact running identity.");
  assert.ok(
    params.reviewed.commit === undefined || typeof params.reviewed.commit === "string",
    "Review exact running identity.",
  );
  return {
    wire: params.requested === undefined ? {} : { requested: params.requested },
    reviewed: { version: params.reviewed.version, commit: params.reviewed.commit ?? "" },
  };
}
function identity(value) {
  assert.ok(record(value) && record(value.hub), "Invalid settings overview response.");
  assert.ok(text(value.hub.version), "Settings overview lacks running identity.");
  assert.ok(value.hub.commit === undefined || typeof value.hub.commit === "string", "Invalid running identity.");
  return { version: value.hub.version, commit: value.hub.commit ?? "" };
}
function upgradeResponse(value) {
  assert.ok(record(value), "Invalid upgrade response.");
  for (const key of ["release", "channel", "url", "archive", "prefix", "binDir", "shareBinDir", "restartMessage"])
    assert.equal(typeof value[key], "string", "Invalid upgrade response.");
  assert.ok(Array.isArray(value.installed) && value.installed.every(text), "Invalid upgrade response.");
  return structuredClone(value);
}
async function readIdentity(hub) {
  return identity(await hub.request("evener/settings/overview", {}));
}
export async function runHubUpgrade(
  hub,
  {
    action = "review",
    params = {},
    ownedHub,
    mutationOptIn = process.env.EVENER_HUB_UPGRADE_MUTATION,
    rpcUrl = process.env.EVENER_RPC_URL,
  } = {},
) {
  const input = paramsFor(action, params);
  if (action === "upgrade") {
    assert.equal(mutationOptIn, "1", "Upgrade requires explicit opt-in.");
    assert.ok(text(ownedHub), "Confirm ownership of the target hub.");
    assert.equal(ownedHub, rpcUrl, "Owned hub must match the connected endpoint.");
  }
  const wire = structuredClone(input.wire);
  await hub.connect();
  const current = await readIdentity(hub);
  if (action === "review")
    return { action, outcome: "read", execution: "unverified", identity: current, readback: current };
  assert.deepEqual(current, input.reviewed, "Running hub identity changed since review.");
  try {
    return {
      action,
      outcome: "acknowledged",
      execution: "unverified",
      identity: current,
      readback: upgradeResponse(await hub.request("evener/upgrade", wire)),
    };
  } catch (error) {
    return { action, outcome: "uncertain", execution: "unverified", error };
  }
}
export function summarizeHubUpgrade(result) {
  return {
    action: result.action,
    outcome: result.outcome,
    execution: result.execution,
    ...(result.readback?.installed ? { installedCount: result.readback.installed.length } : {}),
  };
}
