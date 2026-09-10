import assert from "node:assert/strict";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const methods = {
  add: "evener/marketplace/add",
  remove: "evener/marketplace/remove",
  refresh: "evener/marketplace/refresh",
};
const sourceFields = {
  github: ["repo"],
  url: ["url"],
  directory: ["path"],
  "git-subdir": ["url", "path"],
};
const validText = (value) => typeof value === "string" && value.trim().length > 0;
function exactKeys(value, allowed) {
  assert.ok(value && typeof value === "object" && !Array.isArray(value), "Provide marketplace parameters.");
  assert.ok(
    Object.keys(value).every((key) => allowed.includes(key)),
    "Unknown marketplace field.",
  );
}
function decodeList(value) {
  assert.ok(value && Array.isArray(value.marketplaces), "Invalid marketplace list.");
  assert.ok(
    value.marketplaces.every((entry) => entry && validText(entry.name)),
    "Invalid marketplace identity.",
  );
  return value;
}
function decodeBrowse(value) {
  // Catalog metadata names the manifest, which can differ from the registry alias.
  assert.ok(value && validText(value.name) && Array.isArray(value.plugins), "Invalid marketplace catalog.");
  assert.ok(
    value.plugins.every((plugin) => plugin && validText(plugin.name)),
    "Invalid catalog plugin identity.",
  );
  return value;
}

export async function runMarketplaces(hub, { action = "list", params = {}, ownedHub } = {}) {
  const read = async () => decodeList(await hub.request("evener/marketplace/list", {}));
  if (action === "list") {
    exactKeys(params, []);
    await hub.connect();
    return { outcome: "read", readback: await read() };
  }
  if (action === "browse") {
    exactKeys(params, ["name"]);
    assert.ok(validText(params.name), "Provide a marketplace name.");
    const name = params.name;
    await hub.connect();
    return { outcome: "read", readback: decodeBrowse(await hub.request("evener/marketplace/browse", { name })) };
  }
  requireOwnedHub(process.env.EVENER_MARKETPLACE_MUTATION, ownedHub);
  assert.ok(Object.hasOwn(methods, action), "Unknown marketplace action.");
  exactKeys(params, action === "add" ? ["name", "source"] : ["name"]);
  const mutationParams = {};
  if (action !== "add" || Object.hasOwn(params, "name")) {
    assert.ok(validText(params.name), "Provide a marketplace name.");
    mutationParams.name = params.name;
  }
  if (action === "add") {
    exactKeys(params.source, ["kind", "repo", "url", "path", "ref", "sha"]);
    assert.ok(Object.hasOwn(sourceFields, params.source.kind), "Unknown marketplace source kind.");
    assert.ok(
      Object.values(params.source).every((value) => typeof value === "string"),
      "Invalid marketplace source field.",
    );
    assert.ok(
      sourceFields[params.source.kind].every((key) => validText(params.source[key])),
      "Incomplete marketplace source.",
    );
    mutationParams.source = { ...params.source };
  }
  await hub.connect();
  const before = await read();
  const present = before.marketplaces.some((entry) => entry.name === mutationParams.name);
  if (action === "add") {
    // A manifest-derived name cannot be checked until the hub loads the source.
    assert.ok(mutationParams.name === undefined || !present, "Marketplace already exists.");
  } else {
    assert.ok(present, "Marketplace no longer exists.");
  }
  return mutateAndReadback(hub, methods[action], mutationParams, decodeList, read);
}
