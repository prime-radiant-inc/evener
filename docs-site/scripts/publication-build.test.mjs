import test from "node:test";
import assert from "node:assert/strict";
import {
  mkdtemp,
  mkdir,
  copyFile,
  writeFile,
  readFile,
  rm,
  symlink,
} from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { load } from "cheerio";
import { prepare, site } from "./prepare.mjs";
import { checkBuilt } from "./check-built.mjs";

test("production VitePress config publishes exports and links from a space and Unicode path", async (t) => {
  const root = await mkdtemp(
    path.join(
      process.env.EVENER_SCRATCH_DIR || os.tmpdir(),
      "vitepress links-é-",
    ),
  );
  t.after(() => rm(root, { recursive: true, force: true }));
  // Reuse the installed toolchain read-only, never install through this link.
  await symlink(
    path.join(site, "node_modules"),
    path.join(root, "node_modules"),
    "dir",
  );
  await mkdir(path.join(root, "docs"), { recursive: true });
  await writeFile(
    path.join(root, "docs/start.md"),
    "# Start\n\n[next](two.md#target)\n",
  );
  await writeFile(
    path.join(root, "docs/two.md"),
    "# Target\n\n[back](start.md#start)\n\n[self](#target)\n",
  );
  const manifest = {
    pages: [
      {
        source: "docs/start.md",
        path: "index.md",
        title: "Start",
        group: "Start",
      },
      {
        source: "docs/two.md",
        path: "reference/two.md",
        title: "Two",
        group: "Start",
      },
    ],
    assets: [],
  };
  const out = path.join(root, ".generated");
  await prepare({ root, manifest, out });
  await mkdir(path.join(root, ".vitepress"));
  await writeFile(
    path.join(root, "publication.json"),
    JSON.stringify(manifest),
  );
  await copyFile(
    path.join(site, ".vitepress/config.mjs"),
    path.join(root, ".vitepress/config.mjs"),
  );
  const run = promisify(execFile);
  await run(process.execPath, [
    path.join(site, "node_modules/vitepress/bin/vitepress.js"),
    "build",
    root,
  ]);
  const dist = path.join(root, ".vitepress/dist");
  await checkBuilt(dist, manifest);
  for (const [file, expected] of [
    ["index.html", ["/evener/reference/two.html#target"]],
    [
      "reference/two.html",
      ["/evener/index.html#start", "/evener/reference/two.html#target"],
    ],
  ]) {
    const $ = load(await readFile(path.join(dist, file), "utf8"));
    assert.deepEqual(
      $(".vp-doc p a")
        .toArray()
        .map((a) => $(a).attr("href")),
      expected,
    );
  }
});
