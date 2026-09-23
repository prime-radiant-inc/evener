import assert from "node:assert/strict";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { loadConfigFromFile } from "vite";

const scripts = path.dirname(fileURLToPath(import.meta.url));
const configFile = path.join(scripts, "browserguard.vite.config.mjs");
const env = { command: "serve", mode: "development" };

// make test-web-browser runs guards side by side and gives each one its own
// Vite dep cache through BROWSER_GUARD_VITE_CACHE_DIR, because two Vite
// processes optimizing into one cache race (issue #1586). The variable only
// helps if the guards' config actually resolves it.
test("the browser-guard config puts Vite's dep cache where BROWSER_GUARD_VITE_CACHE_DIR says", async () => {
  const previous = process.env.BROWSER_GUARD_VITE_CACHE_DIR;
  process.env.BROWSER_GUARD_VITE_CACHE_DIR = "/guard/owned/vite-cache";
  try {
    const loaded = await loadConfigFromFile(env, configFile, undefined, "silent");
    assert.equal(loaded?.config.cacheDir, "/guard/owned/vite-cache");
  } finally {
    if (previous === undefined) delete process.env.BROWSER_GUARD_VITE_CACHE_DIR;
    else process.env.BROWSER_GUARD_VITE_CACHE_DIR = previous;
  }
});

test("without BROWSER_GUARD_VITE_CACHE_DIR the browser-guard config keeps the app's cache", async () => {
  const previous = process.env.BROWSER_GUARD_VITE_CACHE_DIR;
  delete process.env.BROWSER_GUARD_VITE_CACHE_DIR;
  try {
    const loaded = await loadConfigFromFile(env, configFile, undefined, "silent");
    assert.equal(loaded?.config.cacheDir, path.join(path.dirname(scripts), ".vite-cache"));
  } finally {
    if (previous !== undefined) process.env.BROWSER_GUARD_VITE_CACHE_DIR = previous;
  }
});
