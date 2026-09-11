import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, symlink } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { after } from "node:test";
import { fileURLToPath } from "node:url";
import { isSharedNodeModules } from "./editorial-preview-install.mjs";

const scratch = await mkdtemp(path.join(os.tmpdir(), "editorial-install-"));

after(() => rm(scratch, { recursive: true, force: true }));

async function makeFrontend(prefix) {
  const frontend = path.join(scratch, prefix);
  await mkdir(path.join(frontend, "node_modules"), { recursive: true });
  return frontend;
}

test("a checkout with its own real node_modules is not shared", async () => {
  const frontend = await makeFrontend("real-install");
  assert.equal(isSharedNodeModules(frontend), false);
});

test("a node_modules symlinked into a shared install is shared", async () => {
  const own = await makeFrontend("symlink-install-target");
  const linked = path.join(scratch, "symlink-install-linked");
  await mkdir(linked);
  await symlink(path.join(own, "node_modules"), path.join(linked, "node_modules"));
  assert.equal(isSharedNodeModules(linked), true);
});

// The predicate must test node_modules ITSELF, never realpath the whole path:
// a checkout under a symlinked PARENT (macOS /tmp -> /private/tmp, a symlinked
// worktree root) has its own perfectly real install, and resolving parents made
// the old realpath comparison call every such checkout shared - silently
// skipping the preview's tests where they should have run (roborev #1143).
test("a real install under a symlinked parent directory is not shared", async () => {
  const underReal = await makeFrontend("parent-symlink-real");
  const linkParent = path.join(scratch, "parent-symlink-link");
  await mkdir(path.dirname(linkParent), { recursive: true });
  await symlink(underReal, linkParent);
  assert.equal(isSharedNodeModules(linkParent), false);
});

// A missing install is its own failure (the caller's vite import fails loudly);
// it is not this predicate's verdict and must not throw from it.
test("a missing node_modules is not shared and does not throw", async () => {
  const frontend = path.join(scratch, "missing-install");
  await mkdir(frontend);
  assert.equal(isSharedNodeModules(frontend), false);
});
