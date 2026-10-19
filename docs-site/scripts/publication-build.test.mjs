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
import { createHash } from "node:crypto";
import { unified } from "unified";
import parse from "remark-parse";
import { visit } from "unist-util-visit";

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
  const imagePath = "docs/assets/tutorials/receipt/cli/verified.png";
  const textPath = "docs/assets/tutorials/receipt/cli/verified.txt";
  await mkdir(path.dirname(path.join(root, imagePath)), { recursive: true });
  const image = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j8ioAAAAASUVORK5CYII=", "base64");
  const text = Buffer.from("python3 check.py\nexit 0\n");
  await writeFile(path.join(root, imagePath), image);
  await writeFile(path.join(root, textPath), text);
  await writeFile(path.join(root, "docs/assets/tutorials/unselected.txt"), "not selected");
  await writeFile(
    path.join(root, "docs/start.md"),
    "# Start\n\n[next](two.md#target)\n\n![Receipt check succeeds](assets/tutorials/receipt/cli/verified.png)\n\n[Selectable command output](assets/tutorials/receipt/cli/verified.txt)\n",
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
    assets: [imagePath, textPath],
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
    ["index.html", ["/evener/reference/two.html#target", `/evener/assets/${textPath}`]],
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
  const $ = load(await readFile(path.join(dist, "index.html"), "utf8"));
  assert.equal($(".vp-doc img").attr("src"), `/evener/assets/${imagePath}`);
  const urls = [];
  visit(unified().use(parse).parse(await readFile(path.join(dist, "markdown/index.md"), "utf8")), node => {
    if (node.type === "image" || node.type === "link") urls.push(node.url);
  });
  assert.ok(urls.includes(`https://primeradiant.com/evener/assets/${imagePath}`));
  assert.ok(urls.includes(`https://primeradiant.com/evener/assets/${textPath}`));
  const hash = bytes => createHash("sha256").update(bytes).digest("hex");
  for (const [file, bytes] of [[imagePath, image], [textPath, text]]) {
    assert.equal(hash(await readFile(path.join(dist, "assets", file))), hash(bytes));
  }
  await assert.rejects(readFile(path.join(dist, "assets/docs/assets/tutorials/unselected.txt")), {code:"ENOENT"});
});
