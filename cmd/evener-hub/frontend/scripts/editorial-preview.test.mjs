import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { createServer, resolveConfig } from "vite";
import { findAvailablePort, parseViteReadyAnnouncement } from "./browserGuardProcess.mjs";
import { isSharedNodeModules } from "./editorial-preview-install.mjs";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const appwirePackage = fileURLToPath(new URL("../../../../appwire-client/typescript/", import.meta.url));
const configFile = fileURLToPath(new URL("./editorial-preview.vite.config.mjs", import.meta.url));
// A fleet worktree shares node_modules through a symlink; the preview's
// isolation contract (fs.allow pinned to the checkout, private dep cache)
// cannot hold there, so the config refuses to load and its tests skip with
// this reason. CI's fresh npm ci checkout is where they actually run.
const sharedInstall = isSharedNodeModules(frontend);
const skipIfSharedInstall = (t) =>
  t.skip(
    "fleet worktree shares node_modules with other lanes; the isolated preview requires its own npm ci install",
  );

test("editorial preview removes RESOLVED inherited proxy and restricts filesystem", async (t) => {
  if (sharedInstall) return skipIfSharedInstall(t);
  const config = await resolveConfig({ root: frontend, configFile }, "serve");
  assert.equal(config.server.proxy, undefined);
  assert.equal(config.server.host, "0.0.0.0");
  assert(Array.isArray(config.server.allowedHosts));
  assert(config.server.allowedHosts.includes("m5"));
  // Vite getAdditionalAllowedHosts appends the bind host during resolution.
  assert(config.server.allowedHosts.every((host) => host === "m5" || host === "0.0.0.0"));
  // The AppWire package is the one path outside the checkout the preview has
  // to serve; everything else stays denied, which the sentinel test below proves.
  assert.deepEqual(config.server.fs.allow, [frontend, appwirePackage]);
  assert.equal(config.server.fs.strict, true);
});

test("normal app-route reloads remain fixture-backed; backend and outside files denied", async (t) => {
  if (sharedInstall) return skipIfSharedInstall(t);
  const phase = (message) => {
    if (process.env.EDITORIAL_ISOLATION_TRACE) console.error(`[editorial-isolation] ${message}`);
  };
  phase("scratch:start");
  const scratch = await mkdtemp(path.join(os.tmpdir(), "editorial-isolation-"));
  const sentinel = path.join(scratch, "not-served.txt");
  await writeFile(sentinel, "private sentinel — never serve");
  phase("port:start");
  const port = await findAvailablePort([9180]);
  phase("create:start");
  // Exercise cold dependency discovery every run without altering the preview's cache.
  const server = await createServer({
    root: frontend,
    configFile,
    cacheDir: path.join(scratch, "vite-cache"),
    server: { port },
    logLevel: "silent",
  });
  phase("create:done");
  try {
    phase("listen:start");
    await server.listen();
    phase("listen:done");
    const origin = `http://127.0.0.1:${port}`;
    for (const route of [
      "/",
      "/index.html",
      "/s/local%3Aeditorial-parent",
      "/settings/theme",
      "/new",
      "/accidental-normal-route",
    ]) {
      phase(`fetch:start ${route}`);
      const response = await fetch(`${origin}${route}`, { headers: { accept: "text/html", host: `m5:${port}` } });
      assert.equal(response.status, 200, route);
      const html = await response.text();
      assert(html.includes("/src/dev/editorial-preview-entry.tsx"), route);
      assert(!html.includes("/src/main.tsx"), route);
      phase(`fetch:done ${route}`);
    }
    for (const route of ["/rpc", "/api/test", "/auth/test", "/doc/test", "/s/local:editorial-parent/images/test"]) {
      phase(`fetch:start ${route}`);
      assert.equal((await fetch(`${origin}${route}`)).status, 403, route);
      phase(`fetch:done ${route}`);
    }
    phase("fetch:start outside");
    const outside = await fetch(`${origin}/@fs${sentinel}`);
    assert.equal(outside.status, 403);
    assert(!(await outside.text()).includes("private sentinel"));
    phase("fetch:done outside");
  } finally {
    // HTML responses start background module pretransforms. Let Vite finish its
    // initial crawl before close cancels crawl-end and awaits those transforms.
    phase("idle:start");
    await server.waitForRequestsIdle();
    phase("idle:done");
    phase("close:start");
    await server.close();
    phase("close:done");
    await rm(scratch, { recursive: true, force: true });
    phase("scratch:removed");
  }
});

test("browserguard wrapper serves a passed fixture config on its announced port", async (t) => {
  if (sharedInstall) return skipIfSharedInstall(t);
  // The editorial-preview runner goes through startBrowserGuard, which spawns
  // the Node wrapper rather than a `vite` binary, so the fixture config must
  // reach the wrapper as an argument (the runner's old spawn-argument rewrite
  // matched a command ending in /vite and never fired - roborev finding).
  const child = spawn(
    process.execPath,
    ["scripts/browserguard-vite.mjs", "scripts/editorial-preview.vite.config.mjs"],
    { cwd: frontend, stdio: ["ignore", "pipe", "pipe"] },
  );
  let stdout = "";
  const announced = new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code, signal) =>
      reject(new Error(`wrapper exited before readiness (code ${code ?? "unknown"}, signal ${signal ?? "none"})`)),
    );
    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
      const lines = stdout.split(/\r\n|\r|\n/);
      stdout = lines.pop() ?? "";
      for (const line of lines) {
        try {
          const address = parseViteReadyAnnouncement(line);
          if (address) resolve(address);
        } catch (error) {
          reject(error);
        }
      }
    });
  });
  try {
    const { port } = await announced;
    const origin = `http://127.0.0.1:${port}`;
    const html = await (await fetch(`${origin}/`, { headers: { accept: "text/html" } })).text();
    assert(html.includes("/src/dev/editorial-preview-entry.tsx"));
    assert(!html.includes("/src/main.tsx"));
    assert.equal((await fetch(`${origin}/rpc`)).status, 403);
  } finally {
    child.stdout.removeAllListeners("data");
    child.kill("SIGTERM");
    const exited = new Promise((resolve) => child.once("exit", resolve));
    if ((await Promise.race([exited, new Promise((resolve) => setTimeout(resolve, 5000))])) === undefined) {
      child.kill("SIGKILL");
      await exited;
    }
  }
});

test("browserguard wrapper rejects a config path that escapes the frontend directory", async () => {
  for (const bad of ["../../../etc/evil.config.mjs", "/absolute/path/to/evil.config.mjs"]) {
    const child = spawn(process.execPath, ["scripts/browserguard-vite.mjs", bad], {
      cwd: frontend,
      stdio: ["ignore", "pipe", "pipe"],
    });
    const stderr = await new Promise((resolve) => {
      let buf = "";
      child.stderr.on("data", (chunk) => {
        buf += chunk.toString();
      });
      child.once("exit", () => resolve(buf));
    });
    assert.match(stderr, /resolves outside the frontend directory/, `expected rejection for ${bad}`);
  }
});
