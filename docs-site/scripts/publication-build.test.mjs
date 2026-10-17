import test from "node:test";
import assert from "node:assert/strict";
import {
  mkdtemp,
  mkdir,
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

test("real VitePress builds mutual published links and applies the Pages base once", async (t) => {
  const root = await mkdtemp(
    path.join(
      process.env.EVENER_SCRATCH_DIR || os.tmpdir(),
      "vitepress-links-",
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
  const out = path.join(root, "staged");
  await prepare({ root, manifest, out });
  await mkdir(path.join(root, ".vitepress"));
  await writeFile(
    path.join(root, ".vitepress/config.mjs"),
    `export default ${JSON.stringify({ base: "/evener/", srcDir: "staged", srcExclude: ["public/**"], outDir: path.join(root, "dist"), vite: { publicDir: path.join(out, "public") } })};`,
  );
  const run = promisify(execFile);
  await run(process.execPath, [
    path.join(site, "node_modules/vitepress/bin/vitepress.js"),
    "build",
    root,
  ]);
  await checkBuilt(path.join(root, "dist"), manifest);
  for (const [file, expected] of [
    ["index.html", ["/evener/reference/two.html#target"]],
    [
      "reference/two.html",
      ["/evener/index.html#start", "/evener/reference/two.html#target"],
    ],
  ]) {
    const $ = load(await readFile(path.join(root, "dist", file), "utf8"));
    assert.deepEqual(
      $(".vp-doc p a")
        .toArray()
        .map((a) => $(a).attr("href")),
      expected,
    );
  }
});
