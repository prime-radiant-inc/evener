import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { lstat, mkdtemp, opendir, readFile, realpath, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import test, { after } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";
import { findAvailablePort, parseViteReadyAnnouncement } from "./browserGuardProcess.mjs";
import { createPrivateEditorialPreviewFixture } from "./editorial-preview-private-fixture.mjs";

const sourceFrontend = fileURLToPath(new URL("../", import.meta.url));
const fixture = await createPrivateEditorialPreviewFixture(sourceFrontend);
after(fixture.cleanup);
const frontend = `${fixture.frontend}${path.sep}`;
const appwirePackage = `${fixture.appwirePackage}${path.sep}`;
const { configFile } = fixture;
const fixtureRequire = createRequire(path.join(frontend, "package.json"));
const { createServer, resolveConfig } = await import(pathToFileURL(fixtureRequire.resolve("vite")));
let completedIsolationCases = 0;

async function dependencySymlinks(root) {
  const links = [];
  async function walk(directory) {
    const entries = await opendir(directory);
    for await (const entry of entries) {
      const entryPath = path.join(directory, entry.name);
      if (entry.isDirectory()) await walk(entryPath);
      else if (entry.isSymbolicLink()) links.push(entryPath);
    }
  }
  await walk(root);
  return links;
}

test("private editorial fixture owns lock-matching source, packages, and fonts", async () => {
  const privateNodeModules = path.join(frontend, "node_modules");
  assert.equal((await lstat(privateNodeModules)).isSymbolicLink(), false);
  assert.deepEqual(await readFile(path.join(frontend, "package-lock.json")), await readFile(path.join(sourceFrontend, "package-lock.json")));

  for (const relative of [
    "vite/package.json",
    "@ibm/plex-sans/fonts/complete/woff2/IBMPlexSans-Text.woff2",
  ]) {
    const source = path.join(fixture.sourceNodeModules, relative);
    const copy = path.join(privateNodeModules, relative);
    assert.deepEqual(await readFile(copy), await readFile(source), relative);
    const [sourceStat, copyStat] = await Promise.all([stat(source), stat(copy)]);
    assert.notDeepEqual([copyStat.dev, copyStat.ino], [sourceStat.dev, sourceStat.ino], relative);
  }
  const sourceConfig = path.join(sourceFrontend, "scripts/editorial-preview.vite.config.mjs");
  assert.deepEqual(await readFile(configFile), await readFile(sourceConfig));
  const [sourceConfigStat, configStat] = await Promise.all([stat(sourceConfig), stat(configFile)]);
  assert.notDeepEqual([configStat.dev, configStat.ino], [sourceConfigStat.dev, sourceConfigStat.ino]);

  for (const link of await dependencySymlinks(privateNodeModules)) {
    assert(path.relative(privateNodeModules, link).split(path.sep).includes(".bin"), link);
    const target = await realpath(link);
    const relativeTarget = path.relative(privateNodeModules, target);
    assert(relativeTarget === "" || (!relativeTarget.startsWith(`..${path.sep}`) && relativeTarget !== ".." && !path.isAbsolute(relativeTarget)), `${link} -> ${target}`);
  }

  const privateFont = path.join(privateNodeModules, "@ibm/plex-sans/fonts/complete/woff2/IBMPlexSans-Text.woff2");
  const sourceFont = path.join(fixture.sourceNodeModules, "@ibm/plex-sans/fonts/complete/woff2/IBMPlexSans-Text.woff2");
  const [privateBytes, sourceBytes] = await Promise.all([readFile(privateFont), readFile(sourceFont)]);
  await writeFile(privateFont, Buffer.concat([privateBytes, Buffer.from("private-write-proof")]));
  assert.deepEqual(await readFile(sourceFont), sourceBytes);
  await writeFile(privateFont, privateBytes);
});

test("production editorial config still refuses a shared writable install", async () => {
  await assert.rejects(
    resolveConfig({ root: fixture.sharedFrontend, configFile: fixture.sharedConfigFile, logLevel: "silent" }, "serve"),
    { message: "Editorial preview requires its own npm ci, not a shared writable install" },
  );
});

test("editorial preview removes RESOLVED inherited proxy and restricts filesystem", async () => {
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
  assert.equal(config.cacheDir, path.join(frontend, ".vite-cache"));
  completedIsolationCases += 1;
});

test("normal app-route reloads remain fixture-backed; backend and outside files denied", async () => {
  const phase = (message) => {
    if (process.env.EDITORIAL_ISOLATION_TRACE) console.error(`[editorial-isolation] ${message}`);
  };
  phase("scratch:start");
  const scratch = await mkdtemp(path.join(fixture.root, "editorial-isolation-"));
  const sentinel = path.join(scratch, "not-served.txt");
  await writeFile(sentinel, "private sentinel — never serve");
  phase("port:start");
  const port = await findAvailablePort([9180]);
  phase("create:start");
  // This HTTP routing test never executes modules. Do not admit background
  // dependency writes that can outlive server.close(); browser tests retain
  // the real preview optimizer.
  const server = await createServer({
    root: frontend,
    configFile,
    cacheDir: path.join(scratch, "vite-cache"),
    plugins: [{
      name: "editorial-http-routing-only",
      configResolved(config) {
        // React adds optimizer includes during resolution; clear them here.
        config.optimizeDeps.noDiscovery = true;
        config.optimizeDeps.include = [];
      },
    }],
    server: { port },
    logLevel: "silent",
  });
  phase("create:done");
  try {
    assert.equal(server.environments.client.depsOptimizer, undefined);
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
    await rm(scratch, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
    phase("scratch:removed");
  }
  completedIsolationCases += 1;
});

test("browserguard wrapper serves a passed fixture config on its announced port", async () => {
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
  let stderr = "";
  child.stderr.on("data", (chunk) => {
    stderr += chunk.toString();
  });
  const exited = new Promise((resolve) => child.once("exit", (code, signal) => resolve({ code, signal })));
  const announced = new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code, signal) =>
      reject(new Error(`wrapper exited before readiness (code ${code ?? "unknown"}, signal ${signal ?? "none"}): ${stderr}`)),
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
    const timeout = Symbol("timeout");
    let timeoutID;
    const timeoutReached = new Promise((resolve) => {
      timeoutID = setTimeout(resolve, 5000, timeout);
    });
    if ((await Promise.race([exited, timeoutReached])) === timeout) {
      child.kill("SIGKILL");
      await exited;
    }
    clearTimeout(timeoutID);
  }
  assert(child.exitCode !== null || child.signalCode !== null, "browserguard wrapper child did not exit");
  const cacheDir = await realpath(path.join(frontend, ".vite-cache"));
  const relativeCache = path.relative(fixture.root, cacheDir);
  assert(!relativeCache.startsWith(`..${path.sep}`) && relativeCache !== ".." && !path.isAbsolute(relativeCache), cacheDir);
  completedIsolationCases += 1;
});

test("all editorial isolation cases complete without shared-install skips", () => {
  assert.equal(completedIsolationCases, 3);
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
