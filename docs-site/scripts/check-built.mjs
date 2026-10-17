import { readFile, readdir, stat } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { load } from "cheerio";
import { base, origin, site } from "./prepare.mjs";
export async function checkBuilt(root, manifest) {
  const files = await readdir(root, { recursive: true });
  const expected = new Set(
    manifest.pages.map((p) => p.path.replace(/\.md$/, ".html")),
  );
  for (const file of files.filter((f) => f.endsWith(".html")))
    if (file !== "404.html" && !expected.has(file))
      throw new Error(`Unpublished HTML: ${file}`);
  const exports = new Set(manifest.pages.map((p) => "markdown/" + p.path));
  for (const file of files.filter((f) => f.endsWith(".md")))
    if (!exports.has(file)) throw new Error(`Unpublished export: ${file}`);
  for (const file of [...expected, ...exports, "llms.txt"])
    await stat(path.join(root, file));
  // The completed build is immutable for the duration of this check.
  const documents = new Map();
  async function document(file) {
    if (!documents.has(file)) {
      const $ = load(await readFile(path.join(root, file), "utf8"));
      const ids = new Set(
        $("[id]")
          .toArray()
          .map((el) => $(el).attr("id")),
      );
      documents.set(file, { $, ids });
    }
    return documents.get(file);
  }
  for (const file of [...expected, "404.html"].filter((f) =>
    files.includes(f),
  )) {
    const { $ } = await document(file);
    for (const element of $("[href], [src]").toArray()) {
      const value = $(element).attr("href") || $(element).attr("src");
      const url = new URL(value, origin + base + file);
      if (url.origin !== origin) continue;
      if (!url.pathname.startsWith(base))
        throw new Error(`Outside Pages base: ${file}: ${value}`);
      let target = decodeURIComponent(url.pathname.slice(base.length));
      if (!target || target.endsWith("/")) target += "index.html";
      if (!files.includes(target))
        throw new Error(`Missing output: ${file}: ${value}`);
      if (url.hash && target.endsWith(".html")) {
        const { ids } = await document(target);
        const id = decodeURIComponent(url.hash.slice(1));
        if (!ids.has(id))
          throw new Error(`Missing rendered fragment: ${file}: ${value}`);
      }
    }
  }
}
if (
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  await checkBuilt(
    path.join(site, ".vitepress/dist"),
    JSON.parse(await readFile(path.join(site, "publication.json"), "utf8")),
  );
  console.log("Built page, fragment, asset and export checks passed.");
}
