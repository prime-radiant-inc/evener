import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { createServer, resolveConfig } from "vite";
import { findAvailablePort } from "./browserGuardProcess.mjs";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const configFile = fileURLToPath(new URL("./editorial-preview.vite.config.mjs", import.meta.url));

test("editorial preview removes RESOLVED inherited proxy and restricts filesystem", async () => {
  const config = await resolveConfig({root:frontend,configFile}, "serve");
  assert.equal(config.server.proxy, undefined);
  assert.equal(config.server.host, "0.0.0.0");
  assert(Array.isArray(config.server.allowedHosts));
  assert(config.server.allowedHosts.includes("m5"));
  // Vite getAdditionalAllowedHosts appends the bind host during resolution.
  assert(config.server.allowedHosts.every(host => host === "m5" || host === "0.0.0.0"));
  assert.deepEqual(config.server.fs.allow, [frontend]);
  assert.equal(config.server.fs.strict, true);
});

test("normal app-route reloads remain fixture-backed; backend and outside files denied", async () => {
  const scratch = await mkdtemp(path.join(os.tmpdir(), "editorial-isolation-"));
  const sentinel = path.join(scratch, "not-served.txt");
  await writeFile(sentinel, "private sentinel — never serve");
  const port = await findAvailablePort([9180]);
  const server = await createServer({root:frontend,configFile,server:{port}, logLevel:"silent"});
  try {
    await server.listen();
    const origin = `http://127.0.0.1:${port}`;
    for (const route of ["/", "/index.html", "/s/local%3Aeditorial-parent", "/settings/theme", "/new", "/accidental-normal-route"]) {
      const response = await fetch(`${origin}${route}`, {headers:{accept:"text/html",host:`m5:${port}`}});
      assert.equal(response.status,200,route);
      const html = await response.text();
      assert(html.includes("/src/dev/editorial-preview-entry.tsx"),route);
      assert(!html.includes("/src/main.tsx"),route);
    }
    for (const route of ["/rpc", "/api/test", "/auth/test", "/doc/test", "/s/local:editorial-parent/images/test"]) {
      assert.equal((await fetch(`${origin}${route}`)).status,403,route);
    }
    const outside = await fetch(`${origin}/@fs${sentinel}`);
    assert.equal(outside.status,403);
    assert(!(await outside.text()).includes("private sentinel"));
  } finally {
    await server.close();
    await rm(scratch,{recursive:true,force:true});
  }
});
