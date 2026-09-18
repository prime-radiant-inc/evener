#!/usr/bin/env node
// Rewrite deep relative imports of the AppWire TypeScript package onto the
// package name, for SDK migration row A4.
//
// When to use it: a tree still spells the package as a path
// (`../../protocol/errors`, `../../appwire-client/typescript/errors`) and
// should spell it as a name (`@evener/appwire-client`). Run it once per
// migration step, read the per-specifier counts it prints, commit the result.
// `make lint-package-imports` is the standing check that no path spelling
// survives -- a grep, since the lint lane installs no dependency tree and this
// script needs TypeScript; this script is the one-time rewrite, not a gate.
//
// Why a script instead of sed: the target specifier depends on the SYMBOLS an
// import names, not on its path prefix. Where a symbol is published decides the
// specifier: the root, a subpath from the package's `exports` map, or the
// in-repo `testing/` subpath for test support. So each statement is parsed, its
// module resolved on disk, and its bindings checked against what the package
// actually publishes. A symbol the package does not publish is reported and
// nothing is written — that is a missing export, not something to paper over
// with a deep path.
//
// The set of published subpaths is the package's own package.json `exports`
// map: the specifier a module maps to is whatever that map says, and nothing
// here enumerates subpaths.

import { createRequire } from "node:module";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { moduleSpecifierSites, namesBindings, parseSource } from "./module-specifiers.mjs";
import { resolveSourceFile } from "./resolve-source.mjs";
import { CONSUMER_TREES, SKIPPED_DIRS, sourceFiles } from "./source-files.mjs";

const checkoutRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

const PACKAGE_NAME = "@evener/appwire-client";
const TESTING_PREFIX = `${PACKAGE_NAME}/testing/`;

// TypeScript always comes from this checkout's frontend, never from --root: a
// fixture tree has no dependency tree of its own.
const require = createRequire(path.join(checkoutRoot, "cmd", "evener-hub", "frontend", "package.json"));
const ts = require("typescript");

// Where the package, the A3 seam and the trees to sweep sit under a given root.
// Every stub in the seam re-exports the package module of the same name, so a
// resolved seam path maps to a package module by basename.
function layoutOf(root) {
  const frontendDir = path.join(root, "cmd", "evener-hub", "frontend");
  return {
    root,
    packageDir: path.join(root, "appwire-client", "typescript"),
    seamDir: path.join(frontendDir, "src", "protocol"),
    // The trees are the shared list; the label a count is printed under is the
    // tree's own relative path.
    trees: CONSUMER_TREES.map((rel) => ({ label: rel, dir: path.join(root, rel) })),
  };
}

// The source module an exports entry names, or null when its target cannot be
// normalized. The target is `types` where the entry has one, else the runtime
// `import`/`require`: the object form spells the declaration, the string
// shorthand the runtime file, and both are `./dist/<module>` plus an extension
// over the same source, so the prefix and the extension both come off.
function moduleIDOf(entry) {
  const target = [entry?.types, entry?.import, entry?.require, typeof entry === "string" ? entry : null].find(
    (candidate) => typeof candidate === "string",
  );
  const match = target?.match(/^\.\/dist\/(.+?)\.(?:d\.ts|d\.mts|d\.cts|ts|mts|cts|js|mjs|cjs)$/);
  return match ? match[1] : null;
}

// The specifiers the package publishes, keyed by the source module they name.
// Read from the package's own `exports` map rather than a list kept here, so a
// subpath added there is picked up with no edit to this tool; an entry whose
// target cannot be normalized is skipped.
function publishedSpecifiers(packageDir) {
  const manifest = JSON.parse(readFileSync(path.join(packageDir, "package.json"), "utf8"));
  const byModule = new Map();
  for (const [subpath, entry] of Object.entries(manifest.exports ?? {})) {
    const moduleID = moduleIDOf(entry);
    if (moduleID === null) continue;
    const specifier = subpath === "." ? PACKAGE_NAME : `${PACKAGE_NAME}${subpath.slice(1)}`;
    byModule.set(moduleID, specifier);
  }
  return byModule;
}

function usage() {
  const published = [...publishedSpecifiers(path.join(checkoutRoot, "appwire-client", "typescript")).values()];
  console.log(`Usage: node scripts/sdk/rewrite-package-imports.mjs [--root DIR]

Rewrites every import of the AppWire TypeScript package in the web and native
trees from a relative path to the package name, choosing the specifier the
package publishes the named symbols at. The specifiers are the package's own
package.json exports map:

${published.map((specifier) => `  ${specifier}`).join("\n")}
  ${TESTING_PREFIX}<module>   in-repo test support, never a runtime import

After rewriting, statements that collapsed onto one specifier are merged: a
file ends up with at most one value import and one type import per specifier,
rather than the three identical lines three deep paths turn into.

Options:
  --root DIR sweep DIR instead of this checkout (for this script's own test)
  --help     this text

Exits 2, writing nothing, if an import names a symbol the package does not
publish — fix the package's exports first.`);
}

// Every source file in a tree, the shared walker skipping SKIPPED_DIRS and, on
// top of that, the A3 seam directory: its stubs re-export the package and are
// not something to rewrite.
const treeSourceFiles = (tree, layout) =>
  sourceFiles(tree, { skipDir: (name, full) => SKIPPED_DIRS.has(name) || full === layout.seamDir });

// A relative specifier is resolved with resolveSourceFile to tell whether it
// lands inside the package or the seam. TypeScript only: this tool never
// resolves a .js or .mjs import into the package.
const PACKAGE_SOURCE_EXTENSIONS = [".ts", ".tsx"];

// The package module a resolved path names: "errors", "testing/fakeClient",
// "types.gen". Returns null for anything outside the package and the seam.
function packageModuleOf(resolved, layout) {
  for (const root of [layout.packageDir, layout.seamDir]) {
    const relative = path.relative(root, resolved);
    if (relative.startsWith("..") || path.isAbsolute(relative)) continue;
    return relative.replace(/\.(ts|tsx)$/, "").split(path.sep).join("/");
  }
  return null;
}

function parse(file) {
  return parseSource(ts, file, readFileSync(file, "utf8"));
}

// Names a module exports, by parsing it. Handles the two forms index.ts uses:
// explicit named exports, and `export type * from "./types.gen"`. Memoised per
// module, so a module a dozen imports name -- the root, a published subpath, a
// testing module -- is parsed once for all of them.
const exportsCache = new Map();
function exportedNames(file) {
  let names = exportsCache.get(file);
  if (!names) {
    names = collectExports(file, new Set());
    exportsCache.set(file, names);
  }
  return names;
}
function collectExports(file, seen) {
  const names = new Set();
  if (seen.has(file)) return names;
  seen.add(file);
  const source = parse(file);
  for (const statement of source.statements) {
    if (ts.isExportDeclaration(statement)) {
      if (statement.exportClause && ts.isNamedExports(statement.exportClause)) {
        for (const element of statement.exportClause.elements) names.add(element.name.text);
        continue;
      }
      if (statement.exportClause && ts.isNamespaceExport(statement.exportClause)) {
        // `export * as ns from "./x"` publishes ONE name, the namespace. Its
        // members are reachable through that name and are not exports of this
        // module, so recursing here would have said the root publishes each
        // of them.
        names.add(statement.exportClause.name.text);
        continue;
      }
      // `export * from "./x"` / `export type * from "./x"`: every name x
      // exports becomes a name this module exports.
      if (!statement.moduleSpecifier) continue;
      const target = resolveSourceFile(file, statement.moduleSpecifier.text, PACKAGE_SOURCE_EXTENSIONS);
      if (!target) continue;
      for (const name of collectExports(target, seen)) names.add(name);
      continue;
    }
    const modifiers = ts.canHaveModifiers(statement) ? ts.getModifiers(statement) ?? [] : [];
    if (!modifiers.some((modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword)) continue;
    if (ts.isVariableStatement(statement)) {
      for (const declaration of statement.declarationList.declarations) {
        if (ts.isIdentifier(declaration.name)) names.add(declaration.name.text);
      }
    } else if (statement.name && ts.isIdentifier(statement.name)) {
      names.add(statement.name.text);
    }
  }
  return names;
}

// Biome sorts named members case-insensitively; match it so the native trees,
// which no formatter gate reaches, come out looking like the web tree.
function compareMembers(a, b) {
  const left = a.replace(/^type /, "").toLowerCase();
  const right = b.replace(/^type /, "").toLowerCase();
  return left < right ? -1 : left > right ? 1 : a < b ? -1 : a > b ? 1 : 0;
}

function indentUnitOf(text) {
  const match = text.match(/\n(\t+| +)\S/);
  if (!match) return "  ";
  return match[1].startsWith("\t") ? "\t" : " ".repeat(Math.min(match[1].length, 4));
}

function renderImport(members, specifier, typeOnly, indent) {
  const keyword = typeOnly ? "import type" : "import";
  const oneLine = `${keyword} { ${members.join(", ")} } from "${specifier}";`;
  if (oneLine.length <= 120) return oneLine;
  return `${keyword} {\n${members.map((member) => `${indent}${member},`).join("\n")}\n} from "${specifier}";`;
}

function renderMember(member) {
  const base = member.imported === member.local ? member.local : `${member.imported} as ${member.local}`;
  return member.typeOnly ? `type ${base}` : base;
}

// What a file's merged import of one specifier should name, keyed by the LOCAL
// binding each member introduces. Keying by rendered text instead produced
// duplicate identifiers two ways: `{ type Thing }` and `{ Thing }` render
// differently but bind one name, and `{ A as X }` and `{ B as X }` bind the
// same name from different exports.
//
// Same export, one side inline-`type`: the value form wins, since it satisfies
// both uses. Different exports behind one local name is a real conflict — a
// name cannot mean two things — so it is reported and nothing is merged.
function mergedMembers(entries, where, conflicts) {
  const byLocal = new Map();
  for (const member of entries.flatMap((entry) => entry.members)) {
    const seen = byLocal.get(member.local);
    if (!seen) {
      byLocal.set(member.local, { ...member });
      continue;
    }
    if (seen.imported !== member.imported) {
      conflicts.push(
        `${where}: ${seen.imported} and ${member.imported} are both imported as ${member.local}; rename one before merging`,
      );
      continue;
    }
    seen.typeOnly = seen.typeOnly && member.typeOnly;
  }
  return [...byLocal.values()].map(renderMember).sort(compareMembers);
}

// Collapse the duplicate statements the rewrite creates: several deep paths
// that all resolved to one specifier become several imports of that specifier.
// Only plain named-binding imports are merged — a namespace or default import
// keeps its own statement, and nothing is merged across the type-only line.
//
// Pure: it takes the file's text and returns the merged text, so a conflict
// found in the last file leaves the first one unwritten, the same way a
// refused import does.
function mergeDuplicateImports(file, text, conflicts) {
  const source = parseSource(ts, file, text);
  const groups = new Map();
  for (const site of moduleSpecifierSites(ts, source)) {
    // Only a named import of the package, and read through the shared reader so
    // the member shapes are not hand-parsed a second time. A named import's
    // specifier node sits directly under its ImportDeclaration; an inline
    // `import("x").Name` is also kind import-named but its parent is not one.
    // Exactly the package or one of its subpaths, not a sibling whose name it
    // is a prefix of (@evener/appwire-client-extra).
    if (site.kind !== "import-named") continue;
    if (site.text !== PACKAGE_NAME && !site.text.startsWith(`${PACKAGE_NAME}/`)) continue;
    const statement = site.node.parent;
    if (!ts.isImportDeclaration(statement)) continue;
    const key = `${site.text}\t${site.typeOnly ? "type" : "value"}`;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push({ statement, members: site.bindings, typeOnly: site.typeOnly, specifier: site.text });
  }

  const indent = indentUnitOf(text);
  const edits = [];
  for (const group of groups.values()) {
    if (group.length < 2) continue;
    const [first, ...rest] = group;
    const where = `${file}:${source.getLineAndCharacterOfPosition(first.statement.getStart(source)).line + 1}`;
    const before = conflicts.length;
    const members = mergedMembers(group, where, conflicts);
    if (conflicts.length > before) continue;
    edits.push({
      start: first.statement.getStart(source),
      end: first.statement.getEnd(),
      text: renderImport(members, first.specifier, first.typeOnly, indent),
    });
    for (const entry of rest) {
      // Remove the duplicate by whole lines, from the start of its own line and
      // absorbing standalone comment or blank lines directly above it -- a doc
      // comment written for it goes with it. Stop at a line carrying code, so a
      // trailing `// keep` on the statement above is not eaten. getFullStart
      // would reach back over that trailing comment (it is this statement's
      // leading trivia), which is the bug this avoids.
      const statementStart = entry.statement.getStart(source);
      let start = text.lastIndexOf("\n", statementStart - 1) + 1;
      while (start > 0) {
        const aboveStart = text.lastIndexOf("\n", start - 2) + 1;
        const above = text.slice(aboveStart, start - 1).trim();
        if (above !== "" && !above.startsWith("//")) break;
        start = aboveStart;
      }
      let end = entry.statement.getEnd();
      if (text[end] === "\n") end += 1;
      edits.push({ start, end, text: "" });
    }
  }
  if (edits.length === 0) return null;
  let updated = text;
  for (const edit of edits.sort((a, b) => b.start - a.start)) {
    updated = updated.slice(0, edit.start) + edit.text + updated.slice(edit.end);
  }
  return updated;
}

function main() {
  const args = process.argv.slice(2);
  if (args.includes("--help") || args.includes("-h")) {
    usage();
    return 0;
  }
  const rest = args.filter((arg) => !["--help", "-h"].includes(arg));
  let root = checkoutRoot;
  if (rest[0] === "--root") {
    if (rest.length < 2) {
      console.error("--root needs a directory");
      return 2;
    }
    root = path.resolve(rest[1]);
    rest.splice(0, 2);
  }
  if (rest.length > 0) {
    console.error(`unknown argument: ${rest.join(" ")}`);
    usage();
    return 2;
  }
  const layout = layoutOf(root);
  const published = publishedSpecifiers(layout.packageDir);

  const rootExports = exportedNames(path.join(layout.packageDir, "index.ts"));

  const counts = new Map(); // `${tree}\t${specifier}` -> count
  const problems = [];
  // Rewritten contents are held here until the whole sweep has come back
  // clean. A refusal is a missing export, and the fix for it is a change to
  // the package -- so the tree this runs against has to be the one the last
  // run left, not a half-migrated mix of both.
  const pending = new Map();
  // Each file's text, read once here and reused by the merge pass, so no file
  // is read twice across the two sweeps.
  const texts = new Map();
  let statementsRewritten = 0;

  for (const tree of layout.trees) {
    for (const file of treeSourceFiles(tree.dir, layout)) {
      const original = readFileSync(file, "utf8");
      texts.set(file, original);
      const source = parseSource(ts, file, original);
      const edits = [];
      for (const site of moduleSpecifierSites(ts, source)) {
        const specifier = site.text;
        if (!specifier.startsWith(".")) continue;
        const resolved = resolveSourceFile(file, specifier, PACKAGE_SOURCE_EXTENSIONS);
        if (!resolved) continue;
        const moduleID = packageModuleOf(resolved, layout);
        if (moduleID === null) continue;

        const where = `${path.relative(root, file)}:${source.getLineAndCharacterOfPosition(site.node.getStart(source)).line + 1}`;
        // A site that names no binding cannot be mapped onto the root: there is
        // nothing to look up, and only the published subpath can stand in for a
        // whole module.
        const wholeModule = !namesBindings(site);
        const named = site.bindings.map((binding) => binding.imported);
        // How this module maps onto a package specifier: what a whole-module
        // load becomes (null where the package publishes no such subpath), and
        // the (published names -> specifier) pairs a named import may satisfy,
        // tried in order so a name both the root and a subpath publish maps to
        // the root. testing/<module> is its own in-repo subpath; any other
        // module the exports map publishes keeps that specifier; every other
        // module reaches the package only through the root.
        let dest;
        if (moduleID.startsWith("testing/")) {
          const subpath = TESTING_PREFIX + moduleID.slice("testing/".length);
          dest = { wholeModule: subpath, named: [[exportedNames(resolved), subpath]] };
        } else {
          const subpath = published.get(moduleID) ?? null;
          const candidates = [[rootExports, PACKAGE_NAME]];
          if (subpath && subpath !== PACKAGE_NAME) candidates.push([exportedNames(resolved), subpath]);
          dest = { wholeModule: subpath, named: candidates };
        }
        const [primaryNames, primarySpecifier] = dest.named[0];
        let target = null;
        if (site.kind === "import-default") {
          problems.push(`${where}: default import of "${moduleID}"; the package publishes no default export`);
        } else if (wholeModule) {
          // A namespace object, a re-export of everything or a runtime load of
          // one module all need the module itself; a module the package exposes
          // no subpath for cannot be taken whole by name.
          if (dest.wholeModule) target = dest.wholeModule;
          else problems.push(`${where}: ${site.kind} of "${moduleID}", which the package does not publish as a subpath`);
        } else if (named.length === 0) {
          // `import {} from "./errors"` binds nothing: it has the semantics of a
          // side-effect import, and every() over no members would have said the
          // specifier satisfies it.
          problems.push(`${where}: empty named import of "${moduleID}" binds nothing, so there is nothing to map onto ${primarySpecifier}`);
        } else {
          const match = dest.named.find(([published]) => named.every((name) => published.has(name)));
          if (match) target = match[1];
          else {
            const missing = named.filter((name) => !primaryNames.has(name));
            problems.push(`${where}: "${moduleID}" exports ${missing.join(", ")}, which ${primarySpecifier} does not publish`);
          }
        }
        if (target === null) continue;
        edits.push({ start: site.node.getStart(source), end: site.node.getEnd(), target });
        counts.set(`${tree.label}\t${target}`, (counts.get(`${tree.label}\t${target}`) ?? 0) + 1);
        statementsRewritten += 1;
      }
      if (edits.length === 0) continue;
      let updated = original;
      for (const edit of edits.sort((a, b) => b.start - a.start)) {
        updated = `${updated.slice(0, edit.start)}"${edit.target}"${updated.slice(edit.end)}`;
      }
      pending.set(file, updated);
    }
  }

  if (problems.length > 0) {
    console.error(`${problems.length} import(s) name a symbol the package does not publish; nothing was written:`);
    for (const problem of problems) console.error(`  ${problem}`);
    return 2;
  }

  const conflicts = [];
  let mergedFiles = 0;
  // Only a file this run edited, or one that already names the package at least
  // twice, can hold duplicate imports to merge. The rest are neither re-read
  // (their text is in hand) nor parsed.
  for (const [file, original] of texts) {
    const text = pending.get(file) ?? original;
    if (!pending.has(file) && text.split(PACKAGE_NAME).length - 1 < 2) continue;
    const merged = mergeDuplicateImports(file, text, conflicts);
    if (merged === null) continue;
    pending.set(file, merged);
    mergedFiles += 1;
  }
  if (conflicts.length > 0) {
    console.error(`${conflicts.length} import(s) cannot be merged; nothing was written:`);
    for (const conflict of conflicts) console.error(`  ${conflict}`);
    return 2;
  }
  for (const [file, contents] of pending) writeFileSync(file, contents);
  if (mergedFiles > 0) console.log(`merged duplicate package imports in ${mergedFiles} file(s)`);

  const rows = [...counts.entries()].map(([key, count]) => [...key.split("\t"), count]);
  rows.sort((a, b) => a[0].localeCompare(b[0]) || a[1].localeCompare(b[1]));
  for (const [tree, specifier, count] of rows) console.log(`${String(count).padStart(4)}  ${tree.padEnd(14)} ${specifier}`);
  console.log(`${statementsRewritten} import statement(s) across ${pending.size} file(s) rewritten`);
  return 0;
}

process.exit(main());
