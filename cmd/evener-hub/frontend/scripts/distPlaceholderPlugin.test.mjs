// Pins vite.config.ts's restoreDistPlaceholder to builds (#4179). Vite closes
// its plugin container - and so calls closeBundle - when a dev or test server
// shuts down too, so without `apply: "build"` every vitest run rewrote the
// tracked dist/PLACEHOLDER and could not run in a read-only checkout.

import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { resolveConfig } from "vite";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const configFile = fileURLToPath(new URL("../vite.config.ts", import.meta.url));

// The runner loader imports the config without writing a bundled copy under
// node_modules/.vite-temp, so this test writes nothing either.
async function pluginNames(command) {
  const config = await resolveConfig(
    { root: frontend, configFile, configLoader: "runner", logLevel: "silent" },
    command,
  );
  return config.plugins.map((plugin) => plugin.name);
}

test("a build restores the dist placeholder", async () => {
  assert.ok((await pluginNames("build")).includes("restore-dist-placeholder"));
});

test("a dev or test server leaves the dist placeholder alone", async () => {
  assert.ok(!(await pluginNames("serve")).includes("restore-dist-placeholder"));
});
