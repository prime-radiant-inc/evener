import { prepare, headings, processor } from "./prepare.mjs";
import { readFile, readdir, stat } from "node:fs/promises";
import { dirname, join, posix, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { validateCandidate, fullMatrix, repositoryRoot, provenanceMatches } from "./tours/artifacts.mjs";

// Dated design records and research are kept for provenance, not maintained as
// current references: their links point at paths and line numbers from the
// moment they were written. Each directory below says so in its own README.
// Everything else is checked, including those directories' README indexes.
const ARCHIVE = [
  "docs/superpowers/",
  "docs/design/",
  "docs/specs/",
  "docs/subagent-management/",
  "docs/research/",
  "docs/web-ui/history/",
  "docs/web-ui/specs/",
  "docs/web-ui/mockups/",
  "docs/original-attractor-specs/",
];

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

// checkDocLinks resolves every local Markdown link and fragment outside the
// dated archive. Published pages already get this treatment from prepare();
// this covers the repository references readers reach from README and the
// contributor docs, which were previously unchecked.
export async function checkDocLinks({ root = repositoryRoot } = {}) {
  const problems = [];
  const targets = new Map();
  // One read per target: a file reached by many links is stat'ed once, read
  // once, and parsed for anchors at most once.
  async function target(path) {
    if (!targets.has(path)) {
      const info = await stat(join(root, path)).catch(() => null);
      const kind = info?.isFile() ? "file" : info?.isDirectory() ? "directory" : null;
      const text = kind === "file" ? await readFile(join(root, path), "utf8") : null;
      targets.set(path, { kind, text, anchors: null });
    }
    return targets.get(path);
  }
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
        const relativeTarget = pathname.startsWith("/")
          ? pathname.slice(1)
          : posix.normalize(posix.join(dirname(file), pathname));
        // A source file may live outside docs/, but a path that climbs out of
        // the repository is a broken link, not something to read.
        if (relativeTarget.startsWith("../") || posix.isAbsolute(relativeTarget)) {
          problems.push(`${file}:${index + 1}: target outside repository ${url}`);
          continue;
        }
        const found = await target(relativeTarget);
        if (!found.kind) {
          problems.push(`${file}:${index + 1}: missing target ${url}`);
          continue;
        }
        if (!hash) continue;
        // Source references use GitHub's #L1 or #L1-L3 syntax.
        if (found.kind === "file" && /^L\d+(-L\d+)?$/.test(hash)) {
          const lineCount = found.text.replace(/\n$/, "").split("\n").length;
          const last = Number(hash.includes("-L") ? hash.split("-L")[1] : hash.slice(1));
          if (last > lineCount) problems.push(`${file}:${index + 1}: line ${last} beyond ${relativeTarget} (${lineCount} lines)`);
          continue;
        }
        if (found.kind !== "file" || !relativeTarget.endsWith(".md")) continue;
        found.anchors ??= headings(processor.parse(found.text));
        if (!found.anchors.has(decodeURIComponent(hash))) {
          problems.push(`${file}:${index + 1}: missing fragment ${url}`);
        }
      }
    }
  }
  if (problems.length) throw new Error(`Broken documentation links:\n${problems.join("\n")}`);
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
  // evidence older than the UI it shows. Report that to the caller rather than
  // warning here, so a test asserting successful acceptance stays silent and
  // the build entry point decides how loud to be.
  return { manifest, current: await provenanceMatches(manifest, root) };
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  await checkDocLinks();
  const { manifest, current } = await checkTutorialAssets();
  if (!current) {
    process.emitWarning(
      `tutorial evidence was captured at ${manifest.sourceRevision}, which no longer matches this tree. ` +
        "Refresh before relying on the screenshots: node docs-site/scripts/tours/refresh.mjs refresh " +
        "--surfaces cli,web --out /ABS/NEW-CANDIDATE",
    );
  }
  const result = await prepare();
  console.log(`Validated and staged ${result.pages} pages and ${result.assets} assets, including Markdown exports and llms.txt.`);
}
