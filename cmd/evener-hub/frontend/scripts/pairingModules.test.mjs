// Pins the pairing loader's dep-optimizer cache. Vite defaults cacheDir to
// <frontend>/node_modules/.vite, which follows a fleet worktree's shared-install
// symlink and is unwritable in a read-only checkout (issue #4482). The loader
// gives each server a run directory under os.tmpdir() and removes it on close.

import assert from "node:assert/strict";
import { existsSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { loadPairingModules } from "./pairingModules.mjs";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const nodeModules = path.join(frontend, "node_modules");

// isUnder reports whether child is parent or sits inside it, by path
// components - so "/a/node_modules" does not match "/a/node_modulesX".
function isUnder(parent, child) {
  const rel = path.relative(parent, child);
  return rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel));
}

test("the pairing dep cache is a per-run directory outside node_modules", async () => {
  const server = await loadPairingModules();
  const cacheDir = server.config.cacheDir;
  try {
    // A cache under node_modules follows the fleet's shared-install symlink,
    // where concurrent lanes stage deps in the same place. It is also the path
    // a read-only checkout cannot create.
    assert.ok(!isUnder(nodeModules, cacheDir), `cacheDir ${cacheDir} must not live under ${nodeModules}`);
    assert.ok(isUnder(tmpdir(), cacheDir), `cacheDir ${cacheDir} must live under ${tmpdir()}`);
    assert.ok(statSync(cacheDir).isDirectory(), `cacheDir ${cacheDir} must exist while the server is open`);
  } finally {
    await server.close();
  }
  assert.equal(existsSync(cacheDir), false, `close() must remove ${cacheDir}`);
});

test("each pairing server gets its own run cache", async () => {
  const first = await loadPairingModules();
  const firstCacheDir = first.config.cacheDir;
  await first.close();
  const second = await loadPairingModules();
  try {
    assert.notEqual(second.config.cacheDir, firstCacheDir, "two servers must not share a run cache");
  } finally {
    await second.close();
  }
});

test("the pairing modules still load through the server", async () => {
  const server = await loadPairingModules();
  try {
    const protocol = await server.ssrLoadModule("/src/dev/pairingEnvelope.ts");
    const recipient = protocol.readRecipient(`EVENER1:${"ab".repeat(40)}`);
    assert.equal(recipient.session.byteLength, 8);
    assert.equal(recipient.publicKey.byteLength, 32);
    const settings = protocol.hubSettings("http://127.0.0.1:9180", "synthetic-token", true);
    assert.deepEqual(
      { host: settings.host, port: settings.port, path: settings.path, tailnet: settings.tailnet, tls: settings.tls },
      { host: "127.0.0.1", port: 9180, path: "/rpc", tailnet: true, tls: false },
    );
    assert.equal(typeof protocol.crc32(new Uint8Array([1, 2, 3])), "number");
  } finally {
    await server.close();
  }
});

test("a rejected server start removes its run cache", async () => {
  const failure = new Error("synthetic pairing server failure");
  let rejectedCacheDir;
  const pending = loadPairingModules(async (options) => {
    rejectedCacheDir = options.cacheDir;
    throw failure;
  });
  try {
    await assert.rejects(pending, (error) => error === failure);
  } catch (error) {
    // The unfixed loader ignores the injected factory and resolves to a real
    // server. Close it before reporting the failure so a pre-fix run cannot
    // leave node --test hanging.
    const server = await pending.catch(() => null);
    await server?.close();
    throw error;
  }
  assert.ok(rejectedCacheDir, "the injected factory must receive the run cacheDir");
  assert.equal(existsSync(rejectedCacheDir), false, `a rejected createServer must remove ${rejectedCacheDir}`);
});
