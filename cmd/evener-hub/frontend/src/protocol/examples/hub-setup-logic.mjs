import assert from "node:assert/strict";

const record = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const text = (v) => typeof v === "string";
function input(action, params) {
  assert.ok(["dirs", "pairing"].includes(action), "Select a hub setup action.");
  assert.ok(record(params), "Provide hub setup parameters.");
  assert.deepEqual(Object.keys(params), action === "pairing" ? ["origin"] : ["path"], "Unknown hub setup parameter.");
  assert.ok(
    text(params[action === "pairing" ? "origin" : "path"]) && params[action === "pairing" ? "origin" : "path"].trim(),
    "Provide a valid hub setup parameter.",
  );
  return structuredClone(params);
}
export async function runHubSetup(
  hub,
  {
    action,
    params = {},
    ownedHub,
    mutationOptIn = process.env.EVENER_HUB_SETUP_MUTATION,
    rpcUrl = process.env.EVENER_RPC_URL,
  } = {},
) {
  const p = input(action, params);
  if (action === "dirs") {
    assert.equal(mutationOptIn, "1", "Directory creation requires explicit opt-in.");
    assert.ok(text(ownedHub) && ownedHub.trim(), "Confirm ownership of the target hub.");
    assert.equal(ownedHub, rpcUrl, "Owned hub must match the connected endpoint.");
  }
  await hub.connect();
  try {
    const value = await hub.request(action === "dirs" ? "evener/dirs/create" : "evener/mobile/pairing", p);
    if (action === "dirs") {
      assert.ok(
        record(value) && text(value.path) && value.path.trim() && typeof value.created === "boolean",
        "Invalid directory response.",
      );
    } else {
      assert.ok(record(value) && text(value.authUrl) && value.authUrl.trim(), "Invalid pairing response.");
      const url = new URL(value.authUrl);
      assert.ok(
        ["http:", "https:"].includes(url.protocol) &&
          !url.username &&
          !url.password &&
          !url.search &&
          !url.hash &&
          url.pathname.startsWith("/auth/") &&
          url.pathname.slice(6) &&
          !url.pathname.slice(6).includes("/"),
        "Invalid pairing link.",
      );
    }
    return {
      outcome: action === "dirs" ? "acknowledged" : "read",
      action,
      execution: "unverified",
      readback: structuredClone(value),
    };
  } catch (error) {
    return { outcome: "uncertain", action, execution: "unverified", error };
  }
}
export function summarizeHubSetup(result) {
  return {
    action: result.action,
    outcome: result.outcome,
    execution: result.execution,
    ...(result.action === "dirs" && result.readback ? { created: result.readback.created } : {}),
  };
}
