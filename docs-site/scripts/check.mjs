import { prepare } from "./prepare.mjs";
import { readFile, readdir } from "node:fs/promises";
import { join, relative, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";
import GithubSlugger from "github-slugger";
import { validateCandidate, fullMatrix, repositoryRoot, sourceTreeDigest } from "./tours/artifacts.mjs";

// Dated design records and research are kept for provenance, not maintained as
// current references: their links point at paths and line numbers from the
// moment they were written. Everything else is checked.
const ARCHIVE = [
  "docs/superpowers/",
  "docs/design/",
  "docs/web-ui/history/",
  "docs/web-ui/specs/",
  "docs/web-ui/mockups/",
  "docs/original-attractor-specs/",
];

function anchored(text) {
  const ids = new Set();
  const slugger = new GithubSlugger();
  let fence = false;
  for (const line of text.split("\n")) {
    if (/^\s*```/.test(line)) {
      fence = !fence;
      continue;
    }
    if (fence) continue;
    const heading = /^#{1,6}\s+(.+)$/.exec(line);
    if (heading) ids.add(slugger.slug(heading[1].replace(/`/g, "").trim()));
    for (const attr of line.matchAll(/\bid=["']([^"']+)["']/g)) ids.add(attr[1]);
  }
  return ids;
}

async function markdownFiles(root) {
  const found = [];
  async function walk(dir) {
    for (const entry of await readdir(join(root, dir), { withFileTypes: true })) {
      const path = dir ? `${dir}/${entry.name}` : entry.name;
      if (entry.isDirectory()) await walk(path);
      else if (entry.name.endsWith(".md")) found.push(path);
    }
  }
  for (const entry of await readdir(root, { withFileTypes: true })) {
    if (entry.isDirectory()) continue;
    if (entry.name.endsWith(".md")) found.push(entry.name);
  }
  await walk("docs");
  return found.sort();
}

async function exists(path) {
  try {
    await readFile(path);
    return "file";
  } catch {}
  try {
    await readdir(path);
    return "directory";
  } catch {}
  return null;
}

// checkDocLinks resolves every local Markdown link and fragment outside the
// dated archive. Published pages already get this treatment from prepare();
// this covers the repository references readers reach from README and the
// contributor docs, which were previously unchecked.
export async function checkDocLinks({ root = repositoryRoot } = {}) {
  const problems = [];
  for (const file of await markdownFiles(root)) {
    // An archive's own README is a current index and is checked; the dated
    // records it points at are not.
    const archived = ARCHIVE.some((prefix) => file.startsWith(prefix));
    if (archived && !file.endsWith("/README.md")) continue;
    const text = await readFile(join(root, file), "utf8");
    let fence = false;
    const lines = text.split("\n");
    for (const [index, line] of lines.entries()) {
      if (/^\s*```/.test(line)) {
        fence = !fence;
        continue;
      }
      if (fence) continue;
      const scanned = line.replace(/`[^`]*`/g, "");
      for (const match of scanned.matchAll(/\]\(([^)\s]+)\)/g)) {
        const url = match[1];
        if (/^(https?:|mailto:|tel:|#|file:)/.test(url)) continue;
        const [pathname, hash] = url.split("#");
        if (!pathname) continue;
        const target = pathname.startsWith("/")
          ? pathname.slice(1)
          : join(relativePath(file), pathname).split(sep).join("/");
        const kind = await exists(join(root, target));
        if (!kind) {
          problems.push(`${file}:${index + 1}: missing target ${url}`);
          continue;
        }
        if (!hash) continue;
        // Source references use GitHub's #L1 or #L1-L3 syntax.
        if (kind === "file" && /^L\d+(-L\d+)?$/.test(hash)) {
          const lineCount = (await readFile(join(root, target), "utf8")).replace(/\n$/, "").split("\n").length;
          const last = Number(hash.includes("-L") ? hash.split("-L")[1] : hash.slice(1));
          if (last > lineCount) problems.push(`${file}:${index + 1}: line ${last} beyond ${target} (${lineCount} lines)`);
          continue;
        }
        if (kind !== "file" || !target.endsWith(".md")) continue;
        if (!anchored(await readFile(join(root, target), "utf8")).has(decodeURIComponent(hash))) {
          problems.push(`${file}:${index + 1}: missing fragment ${url}`);
        }
      }
    }
  }
  if (problems.length) throw new Error(`Broken documentation links:\n${problems.join("\n")}`);
}

function relativePath(file) {
  const dir = file.includes("/") ? file.slice(0, file.lastIndexOf("/")) : "";
  return dir || ".";
}

export async function checkTutorialAssets({root=repositoryRoot, assetRoot=join(root, "docs/assets/tutorials"), publication}={}) {
  publication ??= JSON.parse(await readFile(join(root, "docs-site/publication.json"), "utf8"));
  const manifest = await validateCandidate(assetRoot, fullMatrix, {sourceRoot:root, historicalProvenance:true});
  const selected = ["manifest.json", ...manifest.rows.flatMap(row => [row.file, ...(row.text ? [row.text.file] : [])])];
  for (const file of selected) {
    if (!publication.assets?.includes(`docs/assets/tutorials/${file}`)) {
      throw new Error(`Tutorial asset absent from publication.json.assets: ${file}`);
    }
  }
  // Publication keeps the capture's own provenance: a later commit does not
  // invalidate a screenshot that was correct when taken. It does make the
  // evidence older than the UI it shows, so say so rather than leaving the
  // stale manifest looking current. Making this a hard failure would block
  // every frontend change until all 24 captures are re-run; that is a policy
  // decision, not something a link check should impose.
  const current = await sourceTreeDigest(root);
  if (manifest.sourceTreeDigest !== current) {
    process.emitWarning(
      `tutorial evidence was captured at ${manifest.sourceRevision}, which no longer matches this tree. ` +
        "Refresh before relying on the screenshots: node docs-site/scripts/tours/refresh.mjs refresh " +
        "--surfaces cli,web --out /ABS/NEW-CANDIDATE",
    );
  }
  return manifest;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  await checkDocLinks();
  await checkTutorialAssets();
  const result = await prepare();
  console.log(`Validated and staged ${result.pages} pages and ${result.assets} assets, including Markdown exports and llms.txt.`);
}
