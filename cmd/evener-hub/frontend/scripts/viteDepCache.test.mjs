// Pins the frontend's dep-optimizer cache to this checkout (#1586). A cache
// under node_modules follows the fleet's shared-install symlink into a
// directory every lane writes, where concurrent Vite processes race on the
// dep-cache temp dir. See vite.config.ts for the full rationale.

import assert from "node:assert/strict";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { resolveConfig } from "vite";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const configFile = fileURLToPath(new URL("../vite.config.ts", import.meta.url));

// isUnder reports whether child is parent or sits inside it, by path
// components - so "/a/node_modules" does not match "/a/node_modulesX".
function isUnder(parent, child) {
  const rel = path.relative(parent, child);
  return rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel));
}

test("the dep cache is pinned inside the checkout, not under node_modules", async () => {
  const config = await resolveConfig({ root: frontend, configFile }, "serve");
  // A cache under node_modules follows the fleet's symlink into the shared
  // install, where concurrent lanes race on the same deps temp dir. The pin
  // has to keep it clear of that directory entirely.
  assert.ok(
    !isUnder(path.join(frontend, "node_modules"), config.cacheDir),
    `cacheDir ${config.cacheDir} must not live under node_modules`,
  );
  assert.ok(isUnder(frontend, config.cacheDir), `cacheDir ${config.cacheDir} must stay inside ${frontend}`);
});
