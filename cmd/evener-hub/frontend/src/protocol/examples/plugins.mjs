import assert from "node:assert/strict";
import { clientFromEnvironment } from "./connection.mjs";

// Preview is read-only. Each request resolves the same directory against a
// different per-session allow-list; none installs, enables, or starts anything.
const { hub, cwd } = clientFromEnvironment();
try {
  await hub.connect();
  const defaults = await hub.request("evener/plugin/preview", { cwd });
  const none = await hub.request("evener/plugin/preview", {
    cwd,
    launchOverrides: { enabledPlugins: [] },
  });
  assert.deepEqual(
    none.plugins.filter((plugin) => plugin.selected),
    [],
  );
  const first = defaults.plugins[0];
  if (first) {
    const one = await hub.request("evener/plugin/preview", {
      cwd,
      launchOverrides: { enabledPlugins: [first.name] },
    });
    assert.deepEqual(one.selectionErrors ?? [], []);
    assert.deepEqual(
      one.plugins.filter((plugin) => plugin.selected).map((plugin) => plugin.name),
      [first.name],
    );
  }
  const available = new Set(defaults.plugins.map((plugin) => plugin.name));
  let missing = "__evener_example_missing__";
  while (available.has(missing)) missing += "_";
  const stale = await hub.request("evener/plugin/preview", {
    cwd,
    launchOverrides: { enabledPlugins: [missing] },
  });
  assert.ok(stale.selectionErrors?.some((error) => error.name === missing));
  console.log(
    JSON.stringify(
      {
        available: defaults.plugins.length,
        defaultSelected: defaults.plugins.filter((plugin) => plugin.selected).length,
        explicitNoneVerified: true,
        explicitOneVerified: !!first,
        staleSelectionReported: true,
        note: first
          ? "Preview semantics verified; no session created."
          : "No available plugin: single-plugin selection was not exercised.",
      },
      null,
      2,
    ),
  );
} finally {
  hub.close();
}
