import assert from "node:assert/strict";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";

const methods = {
  install: "evener/plugin/install",
  upgrade: "evener/plugin/upgrade",
  remove: "evener/plugin/remove",
  enable: "evener/plugin/enable",
  disable: "evener/plugin/disable",
  setAutoUpgrade: "evener/plugin/setAutoUpgrade",
};
const validText = (value) => typeof value === "string" && value.trim().length > 0;
function decodePlugins(value) {
  assert.ok(value && Array.isArray(value.plugins), "Invalid plugin list.");
  assert.ok(
    value.plugins.every((entry) => entry && validText(entry.plugin) && validText(entry.marketplace)),
    "Invalid plugin identity.",
  );
  return value;
}

export async function runPluginManagement(hub, { action = "list", target, autoUpgrade, ownedHub } = {}) {
  const read = async () => decodePlugins(await hub.request("evener/plugin/list", {}));
  if (action === "list") {
    await hub.connect();
    return { outcome: "read", readback: await read() };
  }
  requireOwnedHub(process.env.EVENER_PLUGIN_MUTATION, ownedHub);
  assert.ok(Object.hasOwn(methods, action), "Unknown plugin action.");
  assert.ok(target && typeof target === "object" && !Array.isArray(target), "Provide a plugin target.");
  assert.ok(
    Object.keys(target).every((key) => key === "plugin" || key === "marketplace"),
    "Unknown plugin target field.",
  );
  assert.ok(
    validText(target.plugin) && validText(target.marketplace),
    "Provide the exact plugin and marketplace pair.",
  );
  const params = { plugin: target.plugin, marketplace: target.marketplace };
  if (action === "setAutoUpgrade") {
    assert.equal(typeof autoUpgrade, "boolean", "autoUpgrade must be a boolean.");
    params.autoUpgrade = autoUpgrade;
  }
  await hub.connect();
  const before = await read();
  const present = before.plugins.some(
    (entry) => entry.plugin === params.plugin && entry.marketplace === params.marketplace,
  );
  assert.ok(action === "install" ? !present : present, "Plugin preflight does not match the selected action.");
  return mutateAndReadback(hub, methods[action], params, decodePlugins, read);
}
