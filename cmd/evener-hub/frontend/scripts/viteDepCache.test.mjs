// Pins the frontend's dep-optimizer cache. By default it stays in this
// checkout (#1586): a cache under node_modules follows the fleet's
// shared-install symlink into a directory every lane writes, where concurrent
// Vite processes race on the dep-cache temp dir. EVENER_FRONTEND_VITE_CACHE_DIR
// moves it, so the web gate (cmd/evener-dev/webchecks.go) can point it at the
// check's private root and a read-only checkout, which cannot hold .vite-cache,
// still runs (issue #4193). See vite.config.ts for the full rationale.

import assert from "node:assert/strict";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { resolveConfig } from "vite";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const configFile = fileURLToPath(new URL("../vite.config.ts", import.meta.url));

// The runner config loader imports vite.config.ts directly, so these loads
// write no bundled copy under node_modules/.vite-temp and the test itself stays
// inside a read-only checkout.
async function resolvedCacheDir() {
  const config = await resolveConfig(
    { root: frontend, configFile, configLoader: "runner", logLevel: "silent" },
    "serve",
  );
  return config.cacheDir;
}

// withCacheOverride runs body with EVENER_FRONTEND_VITE_CACHE_DIR set to value,
// or (value === undefined) cleared, and restores the variable afterward. The
// gate sets it for the whole `npm run test` process, so a test asserting the
// default has to clear it itself.
async function withCacheOverride(value, body) {
  const previous = process.env.EVENER_FRONTEND_VITE_CACHE_DIR;
  if (value === undefined) delete process.env.EVENER_FRONTEND_VITE_CACHE_DIR;
  else process.env.EVENER_FRONTEND_VITE_CACHE_DIR = value;
  try {
    return await body();
  } finally {
    if (previous === undefined) delete process.env.EVENER_FRONTEND_VITE_CACHE_DIR;
    else process.env.EVENER_FRONTEND_VITE_CACHE_DIR = previous;
  }
}

// isUnder reports whether child is parent or sits inside it, by path
// components - so "/a/node_modules" does not match "/a/node_modulesX".
function isUnder(parent, child) {
  const rel = path.relative(parent, child);
  return rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel));
}

test("the dep cache is pinned inside the checkout, not under node_modules", async () => {
  const cacheDir = await withCacheOverride(undefined, resolvedCacheDir);
  // A cache under node_modules follows the fleet's symlink into the shared
  // install, where concurrent lanes race on the same deps temp dir. The pin
  // has to keep it clear of that directory entirely.
  assert.ok(
    !isUnder(path.join(frontend, "node_modules"), cacheDir),
    `cacheDir ${cacheDir} must not live under node_modules`,
  );
  assert.ok(isUnder(frontend, cacheDir), `cacheDir ${cacheDir} must stay inside ${frontend}`);
});

test("the dep cache follows EVENER_FRONTEND_VITE_CACHE_DIR", async () => {
  const override = "/gate/owned/vite-cache";
  const cacheDir = await withCacheOverride(override, resolvedCacheDir);
  assert.equal(cacheDir, override, "the gate's cacheDir override must be honored");
});
