import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { checkBuilt } from "./check-built.mjs";

test("checks rendered pages, fragments, base-prefixed assets and export membership", async (t) => {
  const root = await mkdtemp(
    path.join(process.env.EVENER_SCRATCH_DIR || os.tmpdir(), "built-docs-"),
  );
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(path.join(root, "markdown"));
  await writeFile(path.join(root, "markdown/index.md"), "# X\n");
  await writeFile(path.join(root, "llms.txt"), "# Index");
  const manifest = { pages: [{ path: "index.md" }] };
  for (const href of [
    "/wrong/index.html",
    "/evener/absent.html",
    "/evener/index.html#absent",
  ]) {
    await writeFile(path.join(root, "index.html"), `<a href="${href}">x</a>`);
    await assert.rejects(checkBuilt(root, manifest));
  }
  await writeFile(
    path.join(root, "index.html"),
    '<h1 id="ok">X</h1><a href="/evener/index.html#ok">x</a>',
  );
  await checkBuilt(root, manifest);
  await writeFile(path.join(root, "secret.html"), "<h1>Secret</h1>");
  await assert.rejects(checkBuilt(root, manifest));
});
