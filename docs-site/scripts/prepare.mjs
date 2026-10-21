import {
  readFile,
  writeFile,
  mkdir,
  stat,
  realpath,
  rm,
  copyFile,
} from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { unified } from "unified";
import parse from "remark-parse";
import stringify from "remark-stringify";
import gfm from "remark-gfm";
import { visit } from "unist-util-visit";
import GithubSlugger from "github-slugger";
import { load } from "cheerio";

export const base = "/evener/";
// GitHub Pages serves this project through the organization's custom domain,
// so the github.io address redirects here. Absolute links in the Markdown
// exports and llms.txt name the final host to avoid a redirect per link.
export const origin = "https://primeradiant.com";
export const repository = "https://github.com/prime-radiant-inc/evener";
export const site = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
export const processor = unified()
  .use(parse)
  .use(gfm)
  .use(stringify, { fences: true });
const encodePath = (value) =>
  value.split("/").map(encodeURIComponent).join("/");
function safe(value) {
  if (
    typeof value !== "string" ||
    !value ||
    path.posix.isAbsolute(value) ||
    value.split("/").some((p) => p === ".." || p === ".") ||
    value.includes("\\")
  )
    throw new Error(`Unsafe path: ${value}`);
  return value;
}
async function local(root, file) {
  const resolved = await realpath(path.join(root, file));
  if (!resolved.startsWith((await realpath(root)) + path.sep))
    throw new Error(`Outside repository: ${file}`);
  return resolved;
}
export function headings(tree) {
  const ids = new Set();
  const slugger = new GithubSlugger();
  visit(tree, "heading", (node) => {
    let text = "";
    visit(node, (n) => {
      if (n.type === "text" || n.type === "inlineCode") text += n.value;
    });
    ids.add(slugger.slug(text));
  });
  visit(tree, "html", (node) => {
    const html = load(node.value, {}, false);
    for (const element of html("[id]").toArray()) {
      const id = html(element).attr("id");
      if (id) ids.add(id);
    }
  });
  return ids;
}
export async function prepare({
  root = path.dirname(site),
  manifest,
  out = path.join(site, ".generated"),
} = {}) {
  manifest ||= JSON.parse(
    await readFile(path.join(site, "publication.json"), "utf8"),
  );
  const pages = new Map();
  const routes = new Set();
  const assets = new Set(manifest.assets || []);
  for (const row of manifest.pages) {
    safe(row.source);
    safe(row.path);
    if (
      !row.source.endsWith(".md") ||
      !row.path.endsWith(".md") ||
      !row.title ||
      !row.group ||
      pages.has(row.source) ||
      routes.has(row.path) ||
      row.path.startsWith("public/")
    )
      throw new Error(`Invalid publication row: ${JSON.stringify(row)}`);
    pages.set(row.source, row);
    routes.add(row.path);
  }
  if (!routes.has("index.md")) throw new Error("Publication needs index.md");
  const trees = new Map();
  async function tree(file) {
    if (!trees.has(file)) {
      const value = processor.parse(
        await readFile(await local(root, file), "utf8"),
      );
      trees.set(file, { value, anchors: headings(value) });
    }
    return trees.get(file).value;
  }
  async function rewrite(url, source, kind, image = false) {
    if (/^(https?:|mailto:|tel:)/i.test(url) || url.startsWith("//"))
      return url;
    if (/^[a-z][a-z\d+.-]*:/i.test(url))
      throw new Error(`Unsupported URL: ${url}`);
    const match = url.match(/^([^?#]*)(\?[^#]*)?(#.*)?$/);
    const pathname = decodeURIComponent(match[1]);
    const query = match[2] || "";
    const hash = match[3] || "";
    const target = pathname
      ? path.posix.normalize(
          pathname.startsWith("/")
            ? pathname.slice(1)
            : path.posix.join(path.posix.dirname(source), pathname),
        )
      : source;
    safe(target);
    const targetPath = await local(root, target);
    const info = await stat(targetPath);
    if (hash && target.endsWith(".md")) {
      await tree(target);
      if (!trees.get(target).anchors.has(decodeURIComponent(hash.slice(1))))
        throw new Error(`Missing fragment ${source}: ${url}`);
    }
    if (hash && !target.endsWith(".md") && !assets.has(target)) {
      const lines = hash.match(/^#L([1-9]\d*)(?:-L([1-9]\d*))?$/);
      if (!lines || !info.isFile())
        throw new Error(`Unsupported source fragment: ${url}`);
      const count = (await readFile(targetPath, "utf8"))
        .replace(/\n$/, "")
        .split("\n").length;
      const start = Number(lines[1]);
      const end = Number(lines[2] || lines[1]);
      if (start > end || end > count)
        throw new Error(`Missing source lines: ${url}`);
    }
    if (image && !assets.has(target))
      throw new Error(`Image not in assets: ${target}`);
    if (assets.has(target))
      return `${kind === "export" ? origin + base : "/"}assets/${encodePath(target)}${query}${hash}`;
    // VitePress resolves source routes before adding the deployment base.
    if (pages.has(target))
      return kind === "export"
        ? `${origin}${base}markdown/${encodePath(pages.get(target).path)}${query}${hash}`
        : `/${encodePath(pages.get(target).path.replace(/\.md$/, ".html"))}${query}${hash}`;
    return `${repository}/${info.isDirectory() ? "tree" : "blob"}/main/${encodePath(target)}${query}${hash}`;
  }
  const output = new Map();
  for (const row of pages.values()) {
    const original = await tree(row.source);
    if (
      original.children.filter((n) => n.type === "heading" && n.depth === 1)
        .length !== 1
    )
      throw new Error(`Expected one H1: ${row.source}`);
    const imageReferences = new Set();
    visit(original, "imageReference", (node) => {
      imageReferences.add(node.identifier);
    });
    visit(original, (n) => {
      // Component-bearing Markdown is not a complete static export. Fail closed.
      if (n.type === "html") {
        const html = load(n.value, {}, false);
        for (const element of html("*").toArray()) {
          if (
            !["a", "br", "details", "summary", "kbd", "sup", "sub"].includes(
              element.name,
            )
          )
            throw new Error(`Unsupported HTML in ${row.source}`);
          if (
            Object.keys(element.attribs).some(
              (name) => !["id", "title", "open"].includes(name),
            )
          )
            throw new Error(
              `Use static HTML anchors and Markdown links in ${row.source}`,
            );
        }
      }
    });
    for (const kind of ["site", "export"]) {
      const document = structuredClone(original);
      const links = [];
      visit(document, (n) => {
        if (n.type === "link" || n.type === "image" || n.type === "definition")
          links.push(n);
      });
      for (const node of links)
        node.url = await rewrite(
          node.url,
          row.source,
          kind,
          node.type === "image" || imageReferences.has(node.identifier),
        );
      output.set(
        kind === "site" ? row.path : `public/markdown/${row.path}`,
        processor.stringify(document),
      );
    }
  }
  output.set(
    "public/llms.txt",
    `# Evener\n\n> Documentation for main.\n\n${[...pages.values()].map((row) => `- [${row.title}](${origin}${base}markdown/${encodePath(row.path)})`).join("\n")}\n`,
  );
  // Only this generated destination is replaced, after every source passes validation.
  await rm(out, { recursive: true, force: true });
  for (const [file, text] of output) {
    await mkdir(path.dirname(path.join(out, file)), { recursive: true });
    await writeFile(path.join(out, file), text);
  }
  for (const asset of assets) {
    safe(asset);
    const source = await local(root, asset);
    const destination = path.join(out, "public/assets", asset);
    await mkdir(path.dirname(destination), { recursive: true });
    await copyFile(source, destination);
  }
  return { pages: pages.size, assets: assets.size, out };
}
if (
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
)
  console.log(await prepare());
