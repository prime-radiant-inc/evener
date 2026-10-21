import { prepare, headings, processor } from "./prepare.mjs";
import { readFile, readdir, realpath, stat } from "node:fs/promises";
import { dirname, join, posix, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";
import { visit } from "unist-util-visit";
import { load } from "cheerio";
import { validateCandidate, fullMatrix, repositoryRoot, revision, sourceTreeDigest } from "./tours/artifacts.mjs";

// Dated design records and research are kept for provenance, not maintained as
// current references: their links point at paths and line numbers from the
// moment they were written. Most directories below say so in their own README.
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

// A few documents inside those directories are living references rather than
// records: the native build procedure the user guide sends readers to, its
// acceptance and remaining-work companions, and the web design documents that
// own current behavior. They are checked; the dated records around them are not.
const LIVING_IN_ARCHIVE = new Set([
  "docs/design/mobile/ios-build-distribution.md",
  "docs/design/mobile/acceptance.md",
  "docs/design/mobile/ios-v1-remaining.md",
  "docs/web-ui/design-system.md",
  "docs/web-ui/keybindings.md",
  "docs/web-ui/shared-notes.md",
  "docs/web-ui/decisions.md",
]);

// Roots whose Markdown is maintained documentation. Test fixtures, eval seeds,
// and agent prompt testdata are deliberately outside this list: their links are
// data, not references a reader follows. Component and SDK READMEs are included
// because they are references readers follow, even when a directory around them
// holds fixtures or generated samples. Note that docs-site/ is walked whole, so
// its fixtures are checked; they are small, and a broken link there is still a
// broken link.
const DOC_ROOTS = ["README.md", "AGENTS.md", "ABOUT.md", "UPGRADING.md"];
const DOC_TREES = [
  "docs/",
  "cmd/",
  "mobile-native/",
  "docs-site/",
  "test/scenarios/",
  "internal/bundled/skills/",
  "appwire-client/typescript/",
  "execsupport/",
  "llm/",
];

// Individual guides whose surrounding directory holds fixtures or experiment
// data. They are listed as files so a walk can never quietly skip them.
const DOC_FILES = [
  "fuzz/README.md",
  "tools/tool-fluency/README.md",
  "tools/prompt-eval/README.md",
  "tools/prompt-eval/memory-lab/README.md",
];

// Generated and installed trees are not sources: `.generated` is restaged from
// docs/ by the publisher, and node_modules is a package install.
const SKIP = new Set([".git", "node_modules", ".generated", ".vitepress"]);

function maintained(file) {
  if (file.split("/").some((part) => SKIP.has(part))) return false;
  if (DOC_FILES.includes(file)) return true;
  if (file.startsWith("docs/") && file.endsWith("/README.md")) {
    // An archive's own README is a current index even when the dated records
    // around it are not.
    return true;
  }
  if (!DOC_ROOTS.some((name) => file === name) && !DOC_TREES.some((prefix) => file.startsWith(prefix)))
    return false;
  if (LIVING_IN_ARCHIVE.has(file)) return true;
  if (!file.startsWith("docs/")) return true;
  return !ARCHIVE.some((prefix) => file.startsWith(prefix));
}

async function markdownFiles(root) {
  const found = new Set();
  const rootReal = await realpath(root);
  // A source file or directory that resolves outside the repository is refused
  // rather than read: a symlinked README must not make the checker parse a file
  // it was never given.
  async function inside(path) {
    const resolved = await realpath(join(root, path)).catch(() => null);
    return resolved !== null && (resolved === rootReal || resolved.startsWith(rootReal + sep)) ? resolved : null;
  }
  for (const name of DOC_ROOTS) {
    if ((await inside(name)) && (await stat(join(root, name)).catch(() => null))?.isFile()) found.add(name);
  }
  // Explicit files are added directly; feeding a file path to walk() would
  // readdir() it and silently drop it.
  for (const name of DOC_FILES) {
    if ((await inside(name)) && (await stat(join(root, name)).catch(() => null))?.isFile()) found.add(name);
  }
  async function walk(dir) {
    if (!(await inside(dir))) return;
    let entries;
    try {
      entries = await readdir(join(root, dir), { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const path = `${dir}/${entry.name}`;
      if (entry.isDirectory()) {
        if (!SKIP.has(entry.name)) await walk(path);
      }
      else if (entry.name.endsWith(".md") && (await inside(path))) found.add(path);
    }
  }
  for (const dir of DOC_TREES) await walk(dir.replace(/\/$/, ""));
  return [...found].filter(maintained).sort();
}

// localLinks returns every local link, image, and reference definition the
// Markdown parser finds. Using the parser (rather than a line regex) is what
// makes reference-style links, titles, and links inside emphasis work.
function localLinks(tree) {
  const links = [];
  visit(tree, (node) => {
    if (node.type === "link" || node.type === "image" || node.type === "definition") links.push(node.url);
    // Raw HTML links are not Markdown, and the publisher refuses some component
    // forms. A repository page that uses one is still checked. An HTML parser
    // (not a regex) handles quoting styles, entity decoding, and comments.
    if (node.type === "html") {
      const $ = load(node.value, {}, false);
      $("a[href]").each((_, element) => links.push($(element).attr("href")));
      $("img[src]").each((_, element) => links.push($(element).attr("src")));
    }
  });
  return links.filter((url) => url && !/^(https?:|mailto:|tel:|file:)/i.test(url) && !url.startsWith("//"));
}

// checkDocLinks resolves every local Markdown link and fragment outside the
// dated archive. Published pages already get this treatment from prepare();
// this covers the repository references readers reach from README, the
// contributor docs, component READMEs, and maintained scenario cards.
export async function checkDocLinks({ root = repositoryRoot } = {}) {
  const problems = [];
  const targets = new Map();
  const rootReal = await realpath(root);

  // One stat and at most one parse per target; a file reached by many links is
  // not read or parsed again.
  async function target(path) {
    if (!targets.has(path)) {
      const info = await stat(join(root, path)).catch(() => null);
      const kind = info?.isFile() ? "file" : info?.isDirectory() ? "directory" : null;
      targets.set(path, { kind, text: null, anchors: null });
    }
    return targets.get(path);
  }
  async function text(path) {
    const found = await target(path);
    found.text ??= found.kind === "file" ? await readFile(join(root, path), "utf8") : null;
    return found;
  }

  for (const file of await markdownFiles(root)) {
    const source = await readFile(join(root, file), "utf8");
    const anchorCache = new Map([[file, headings(processor.parse(source))]]);
    for (const url of localLinks(processor.parse(source))) {
      const match = url.match(/^([^?#]*)(?:\?[^#]*)?(#.*)?$/);
      if (!match) continue;
      const pathname = match[1];
      const hash = match[2] || "";
      // A same-page fragment resolves against this file, not a sibling.
      if (!pathname) {
        if (hash && !anchorCache.get(file).has(decodeURIComponent(hash.slice(1)))) {
          problems.push(`${file}: missing fragment ${url}`);
        }
        continue;
      }
      let decoded;
      try {
        decoded = decodeURIComponent(pathname);
      } catch {
        problems.push(`${file}: undecodable path ${url}`);
        continue;
      }
      const relativeTarget = decoded.startsWith("/")
        ? posix.normalize(decoded.slice(1))
        : posix.normalize(posix.join(dirname(file), decoded));
      // Reject anything that leaves the repository before touching the disk.
      // Normalizing first is what catches "/docs/../../x" as well as "../x".
      if (relativeTarget.startsWith("../") || posix.isAbsolute(relativeTarget)) {
        problems.push(`${file}: target outside repository ${url}`);
        continue;
      }
      const absolute = join(root, relativeTarget);
      // stat follows symlinks, so a link to a symlink pointing outside the
      // repository would read that file. Resolve and re-check.
      const resolved = await realpath(absolute).catch(() => null);
      if (!resolved) {
        problems.push(`${file}: missing target ${url}`);
        continue;
      }
      if (resolved !== rootReal && !resolved.startsWith(rootReal + sep)) {
        problems.push(`${file}: target outside repository ${url}`);
        continue;
      }
      const found = await target(relativeTarget);
      if (!found.kind) {
        problems.push(`${file}: missing target ${url}`);
        continue;
      }
      if (!hash) continue;
      const fragment = decodeURIComponent(hash.slice(1));
      // Source references use GitHub's #L1 or #L1-L3 syntax.
      if (found.kind === "file" && /^L\d+(-L\d+)?$/.test(fragment)) {
        const body = (await text(relativeTarget)).text.replace(/\n$/, "");
        const lineCount = body.split("\n").length;
        const first = Number(fragment.slice(1).split("-L")[0]);
        const last = Number(fragment.includes("-L") ? fragment.split("-L")[1] : first);
        if (first < 1 || last < first || last > lineCount) {
          problems.push(`${file}: line range ${fragment} outside ${relativeTarget} (${lineCount} lines)`);
        }
        continue;
      }
      // A fragment on a directory target would need the rendered README;
      // report it rather than letting it pass unchecked.
      if (found.kind === "directory") {
        problems.push(`${file}: fragment on directory target ${url}`);
        continue;
      }
      if (found.kind !== "file" || !relativeTarget.endsWith(".md")) continue;
      const foundAnchors = (found.anchors ??= headings(processor.parse((await text(relativeTarget)).text)));
      if (!foundAnchors.has(fragment)) {
        problems.push(`${file}: missing fragment ${url}`);
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
  // Keep strict correspondence available to callers. Publication warnings use
  // relevant working bytes, not commit distance or unrelated documentation.
  const revisionCurrent = manifest.sourceRevision === revision(root);
  const sourceCurrent = manifest.sourceTreeDigest === await sourceTreeDigest(root);
  return { manifest, current: revisionCurrent && sourceCurrent, sourceCurrent };
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  await checkDocLinks();
  const { manifest, sourceCurrent } = await checkTutorialAssets();
  if (!sourceCurrent) {
    process.emitWarning(
      `tutorial evidence was captured at ${manifest.sourceRevision}, and relevant source has changed. ` +
        "Capture and review before relying on the screenshots: node docs-site/scripts/tours/refresh.mjs refresh " +
        "--surfaces cli,web --out /ABS/NEW-CANDIDATE",
    );
  }
  const result = await prepare();
  console.log(`Validated and staged ${result.pages} pages and ${result.assets} assets, including Markdown exports and llms.txt.`);
}
