import test from "node:test";
import assert from "node:assert/strict";
import {
  mkdtemp,
  mkdir,
  writeFile,
  readFile,
  readdir,
  rm,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { prepare } from "./prepare.mjs";
import { checkBuilt } from "./check-built.mjs";

async function fixture(
  t,
  body = "[next](nested/two%20words.md#repeat-1)\n\n[code](../src/a.go#L1)\n\n[private](hidden.md#secret)\n\n[web](https://example.org/x)\n\n```md\n[not a link](missing.md)\n```",
) {
  const root = await mkdtemp(
    path.join(process.env.EVENER_SCRATCH_DIR || os.tmpdir(), "publication-"),
  );
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const [file, text] of Object.entries({
    "docs/start.md": "# Start\n\n" + body,
    "docs/nested/two words.md": "# Two\n\n## Repeat\n\n## Repeat\n",
    "docs/hidden.md": "# Secret\n",
    "src/a.go": "package a\n",
  })) {
    await mkdir(path.dirname(path.join(root, file)), { recursive: true });
    await writeFile(path.join(root, file), text);
  }
  const manifest = {
    pages: [
      {
        source: "docs/start.md",
        path: "index.md",
        title: "Start",
        group: "Start",
      },
      {
        source: "docs/nested/two words.md",
        path: "reference/two.md",
        title: "Two",
        group: "Reference",
      },
    ],
    assets: [],
  };
  return { root, manifest, out: path.join(root, "output") };
}

test("publishes only manifest members, rewrites nested links and emits complete exports", async (t) => {
  const f = await fixture(t);
  await prepare(f);
  const { unified } = await import("unified");
  const { default: parse } = await import("remark-parse");
  const { visit } = await import("unist-util-visit");
  for (const [file, want] of [
    ["index.md", "/reference/two.html#repeat-1"],
    [
      "public/markdown/index.md",
      "https://primeradiant.com/evener/markdown/reference/two.md#repeat-1",
    ],
  ]) {
    const tree = unified()
      .use(parse)
      .parse(await readFile(path.join(f.out, file), "utf8"));
    const links = [];
    const code = [];
    visit(tree, "link", (n) => {
      links.push(n.url);
    });
    visit(tree, "code", (n) => {
      code.push(n.value);
    });
    assert.deepEqual(links, [
      want,
      "https://github.com/prime-radiant-inc/evener/blob/main/src/a.go#L1",
      "https://github.com/prime-radiant-inc/evener/blob/main/docs/hidden.md#secret",
      "https://example.org/x",
    ]);
    assert.deepEqual(code, ["[not a link](missing.md)"]);
  }
  assert.deepEqual((await readdir(f.out)).sort(), [
    "index.md",
    "public",
    "reference",
  ]);
  const index = unified()
    .use(parse)
    .parse(await readFile(path.join(f.out, "public/llms.txt"), "utf8"));
  const urls = [];
  visit(index, "link", (n) => {
    urls.push(n.url);
  });
  assert.deepEqual(urls, [
    "https://primeradiant.com/evener/markdown/index.md",
    "https://primeradiant.com/evener/markdown/reference/two.md",
  ]);
});
for (const url of [
  "missing.md",
  "nested/two%20words.md#absent",
  "hidden.md#absent",
  "../../outside.md",
])
  test(`rejects invalid local reference ${url}`, async (t) => {
    const f = await fixture(t, `[bad](${url})`);
    await assert.rejects(prepare(f));
  });
test("supports reference links, images through explicit asset selection, and same-page fragments", async (t) => {
  const f = await fixture(t, "[ref][x]\n\n[x]: #start\n\n![image](pic.svg)");
  await writeFile(
    path.join(f.root, "docs/pic.svg"),
    '<svg xmlns="http://www.w3.org/2000/svg"/>',
  );
  await assert.rejects(prepare(f));
  f.manifest.assets = ["docs/pic.svg"];
  await prepare(f);
  assert.match(await readFile(path.join(f.out, "index.md"), "utf8"), /!\[image\]\(\/assets\/docs\/pic\.svg\)/);
  assert.match(await readFile(path.join(f.out, "public/markdown/index.md"), "utf8"), /!\[image\]\(https:\/\/primeradiant\.com\/evener\/assets\/docs\/pic\.svg\)/);
  assert.equal(
    await readFile(path.join(f.out, "public/assets/docs/pic.svg"), "utf8"),
    '<svg xmlns="http://www.w3.org/2000/svg"/>',
  );
});
test("rejects duplicate routes and paths outside repository", async (t) => {
  const f = await fixture(t);
  f.manifest.pages[1].path = "index.md";
  await assert.rejects(prepare(f));
  f.manifest.pages[1].path = "../escape.md";
  await assert.rejects(prepare(f));
});
test("reference images require explicit assets, just like inline images", async (t) => {
  const f = await fixture(t, "![image][pic]\n\n[pic]: pic.svg");
  await writeFile(path.join(f.root, "docs/pic.svg"), "<svg/>");
  await assert.rejects(prepare(f));
});
test("rejects component HTML rather than exporting renderer code", async (t) => {
  const f = await fixture(t, "<Widget />");
  await assert.rejects(prepare(f));
});
test("preserves tables and removes stale generated members on regeneration", async (t) => {
  const f = await fixture(t, "| a | b |\n| - | - |\n| 1 | 2 |");
  await prepare(f);
  const { unified } = await import("unified");
  const { default: parse } = await import("remark-parse");
  const { default: gfm } = await import("remark-gfm");
  const tree = unified()
    .use(parse)
    .use(gfm)
    .parse(
      await readFile(path.join(f.out, "public/markdown/index.md"), "utf8"),
    );
  assert.equal(
    tree.children.find((n) => n.type === "table").children.length,
    2,
  );
  f.manifest.pages.pop();
  await prepare(f);
  assert.deepEqual((await readdir(f.out)).sort(), ["index.md", "public"]);
});
test("rejects source-code line fragments outside the target file", async (t) => {
  const f = await fixture(t, "[code](../src/a.go#L2-L4)");
  await assert.rejects(prepare(f));
});
test("rejects components nested inside otherwise supported HTML blocks", async (t) => {
  const f = await fixture(t, "<details>\n<Widget />\n</details>");
  await assert.rejects(prepare(f));
});
test("retains Mermaid language and source in complete Markdown exports", async (t) => {
  const diagram = "flowchart TD\n  Session[Session] --> Turn[Turn]";
  const f = await fixture(t, "```mermaid\n" + diagram + "\n```");
  await prepare(f);
  const { unified } = await import("unified");
  const { default: parse } = await import("remark-parse");
  for (const file of ["index.md", "public/markdown/index.md"]) {
    const tree = unified()
      .use(parse)
      .parse(await readFile(path.join(f.out, file), "utf8"));
    const code = tree.children.find((node) => node.type === "code");
    assert.equal(code.lang, "mermaid");
    assert.equal(code.value, diagram);
  }
});

test("resolves entity-encoded HTML ids to percent-encoded source and rendered fragments", async (t) => {
  const source = await readFile(
    new URL("../fixtures/anchors/source.md", import.meta.url),
    "utf8",
  );
  const rendered = await readFile(
    new URL("../fixtures/anchors/rendered.html", import.meta.url),
    "utf8",
  );
  const f = await fixture(t, "");
  await writeFile(path.join(f.root, "docs/start.md"), source);
  await prepare(f);

  const { unified } = await import("unified");
  const { default: parse } = await import("remark-parse");
  const { visit } = await import("unist-util-visit");
  for (const [file, expected] of [
    ["index.md", "/index.html#a%26b"],
    [
      "public/markdown/index.md",
      "https://primeradiant.com/evener/markdown/index.md#a%26b",
    ],
  ]) {
    const tree = unified()
      .use(parse)
      .parse(await readFile(path.join(f.out, file), "utf8"));
    const links = [];
    visit(tree, "link", (node) => links.push(node.url));
    assert.deepEqual(links, [expected]);
  }

  const built = path.join(f.root, "rendered");
  await mkdir(path.join(built, "markdown"), { recursive: true });
  await writeFile(path.join(built, "markdown/index.md"), source);
  await writeFile(path.join(built, "llms.txt"), "# Index\n");
  await writeFile(path.join(built, "index.html"), rendered);
  await checkBuilt(built, { pages: [{ path: "index.md" }] });

  await writeFile(
    path.join(f.root, "docs/start.md"),
    source.replace("#a%26b", "#a%26c"),
  );
  await assert.rejects(prepare(f));
  await writeFile(
    path.join(built, "index.html"),
    rendered.replace("#a%26b", "#a%26c"),
  );
  await assert.rejects(checkBuilt(built, { pages: [{ path: "index.md" }] }));
});
