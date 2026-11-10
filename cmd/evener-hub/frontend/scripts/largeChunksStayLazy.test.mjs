// Pins vite.config.ts's largeChunksStayLazy guard: a chunk over 500 kB may
// stay in the build only when it sits below a dynamic import, never on an
// entry's static import graph. The guard runs on every build, but without this
// test a broken bundle-key or `imports` refactor would leave every build green
// with the policy silently unenforced (#4697).

import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { resolveConfig } from "vite";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const configFile = fileURLToPath(new URL("../vite.config.ts", import.meta.url));
const OVER_LIMIT = 600_000;

async function guard() {
  const config = await resolveConfig(
    { root: frontend, configFile, configLoader: "runner", logLevel: "silent" },
    "build",
  );
  const plugin = config.plugins.find((candidate) => candidate.name === "large-chunks-stay-lazy");
  assert.ok(plugin?.generateBundle, "vite.config.ts must register largeChunksStayLazy for builds");
  return plugin;
}

function chunk(fileName, { isEntry = false, imports = [], dynamicImports = [], size = 0 } = {}) {
  return { type: "chunk", fileName, isEntry, imports, dynamicImports, code: "x".repeat(size), modules: {} };
}

function run(plugin, bundle) {
  let error;
  plugin.generateBundle.call({ error: (message) => (error = String(message)) }, {}, bundle);
  return error;
}

test("fails an entry-reachable chunk over 500 kB", async () => {
  const error = run(await guard(), {
    "entry.js": chunk("entry.js", { isEntry: true, imports: ["eager.js"] }),
    "eager.js": chunk("eager.js", { size: OVER_LIMIT }),
  });
  assert.match(error ?? "", /eager\.js is 600\.00 kB and is reachable from an entry/);
});

test("allows a chunk over 500 kB behind a dynamic import", async () => {
  const error = run(await guard(), {
    "entry.js": chunk("entry.js", { isEntry: true, dynamicImports: ["lazy.js"] }),
    "lazy.js": chunk("lazy.js", { size: OVER_LIMIT }),
  });
  assert.equal(error, undefined);
});

test("allows a static import of a chunk behind a dynamic import", async () => {
  const error = run(await guard(), {
    "entry.js": chunk("entry.js", { isEntry: true, dynamicImports: ["lazy.js"] }),
    "lazy.js": chunk("lazy.js", { imports: ["lazy-child.js"] }),
    "lazy-child.js": chunk("lazy-child.js", { size: OVER_LIMIT }),
  });
  assert.equal(error, undefined);
});

test("allows a static chunk under 500 kB", async () => {
  const error = run(await guard(), {
    "entry.js": chunk("entry.js", { isEntry: true, imports: ["small.js"] }),
    "small.js": chunk("small.js", { size: 500_000 }),
  });
  assert.equal(error, undefined);
});
