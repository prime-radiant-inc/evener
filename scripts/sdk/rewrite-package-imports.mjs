#!/usr/bin/env node
// Rewrite deep relative imports of the AppWire TypeScript package onto the
// package name, for SDK migration row A4.
//
// When to use it: a tree still spells the package as a path
// (`../../protocol/errors`, `../../appwire-client/typescript/errors`) and
// should spell it as a name (`@evener/appwire-client`). Run it once per
// migration step, read the per-specifier counts it prints, commit the result.
// `--check` makes no edit and exits nonzero if anything is left to rewrite.
// `make lint-package-imports` asserts the same thing with a grep, because the
// lint lane installs no dependency tree and this script needs TypeScript.
//
// Why a script instead of sed: the target specifier depends on the SYMBOLS an
// import names, not on its path prefix. `readDocFile` is published only at
// `@evener/appwire-client/docContent`; `FakeClient` only at the in-repo
// `@evener/appwire-client/testing/fakeClient`; everything else comes from the
// root. So each statement is parsed, its module resolved on disk, and its
// bindings checked against what the package actually publishes. A symbol the
// package does not publish is reported and nothing is written — that is a
// missing export, not something to paper over with a deep path.

import { createRequire } from "node:module";
import { readFileSync, writeFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { moduleSpecifierSites, parseSource } from "./module-specifiers.mjs";
import { resolveSourceFile } from "./resolve-source.mjs";

const checkoutRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

const PACKAGE_NAME = "@evener/appwire-client";
const DOC_CONTENT_SUBPATH = `${PACKAGE_NAME}/docContent`;
const TESTING_PREFIX = `${PACKAGE_NAME}/testing/`;

const SKIP_DIRS = new Set(["node_modules", "dist", "build", ".git", "ios", "android", "__snapshots__"]);
const SOURCE_EXTENSIONS = new Set([".ts", ".tsx", ".mts"]);

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
    trees: [
      { label: "web", dir: path.join(frontendDir, "src") },
      { label: "mobile-native", dir: path.join(root, "mobile-native") },
      { label: "mobile/src", dir: path.join(root, "mobile", "src") },
    ],
  };
}

function usage() {
  console.log(`Usage: node scripts/sdk/rewrite-package-imports.mjs [--root DIR] [--check]

Rewrites every import of the AppWire TypeScript package in the web and native
trees from a relative path to the package name, choosing the specifier the
package publishes the named symbols at:

  ${PACKAGE_NAME}                  root exports (index.ts)
  ${DOC_CONTENT_SUBPATH}       the one published subpath
  ${TESTING_PREFIX}<module>   in-repo test support, never a runtime import

After rewriting, statements that collapsed onto one specifier are merged: a
file ends up with at most one value import and one type import per specifier,
rather than the three identical lines three deep paths turn into.

Options:
  --root DIR sweep DIR instead of this checkout (for this script's own test)
  --check    report only; exit 1 if any import still names a path
  --help     this text

Exits 2, writing nothing, if an import names a symbol the package does not
publish — fix the package's exports first.`);
}

function walk(dir, layout, out) {
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (SKIP_DIRS.has(entry.name)) continue;
      if (full === layout.seamDir) continue;
      walk(full, layout, out);
    } else if (entry.isFile() && SOURCE_EXTENSIONS.has(path.extname(entry.name))) {
      out.push(full);
    }
  }
  return out;
}

// Resolve a relative specifier the way the bundlers do, enough to tell whether
// it lands inside the package or the seam. TypeScript only: this tool never
// resolves a .js or .mjs import into the package.
function resolveSpecifier(fromFile, specifier) {
  return resolveSourceFile(fromFile, specifier, [".ts", ".tsx"]);
}

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
// explicit named exports, and `export type * from "./types.gen"`.
function exportedNames(file, seen = new Set()) {
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
      // `export * from "./x"` / `export type * from "./x"`
      if (!statement.moduleSpecifier) continue;
      const target = resolveSpecifier(file, statement.moduleSpecifier.text);
      if (!target) continue;
      for (const name of exportedNames(target, seen)) names.add(name);
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

// Every module-specifier node in a file that could name the package, with the
// bindings it brings in. `kind` drives the error messages and the namespace
// rule: a namespace object cannot come from a root re-export.
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
  for (const statement of source.statements) {
    if (!ts.isImportDeclaration(statement) || !ts.isStringLiteralLike(statement.moduleSpecifier)) continue;
    const specifier = statement.moduleSpecifier.text;
    if (!specifier.startsWith(PACKAGE_NAME)) continue;
    const clause = statement.importClause;
    if (!clause || clause.name || !clause.namedBindings || !ts.isNamedImports(clause.namedBindings)) continue;
    const key = `${specifier}\t${clause.isTypeOnly ? "type" : "value"}`;
    const members = clause.namedBindings.elements.map((element) => ({
      local: element.name.text,
      imported: (element.propertyName ?? element.name).text,
      typeOnly: Boolean(element.isTypeOnly),
    }));
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push({ statement, members, typeOnly: Boolean(clause.isTypeOnly), specifier });
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
      // Take the trailing newline with the statement so no blank line is left.
      let end = entry.statement.getEnd();
      if (text[end] === "\n") end += 1;
      edits.push({ start: entry.statement.getStart(source), end, text: "" });
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
  const checkOnly = args.includes("--check");
  const rest = args.filter((arg) => !["--check", "--help", "-h"].includes(arg));
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

  const rootExports = exportedNames(path.join(layout.packageDir, "index.ts"));
  const docContentExports = exportedNames(path.join(layout.packageDir, "docContent.ts"));

  const counts = new Map(); // `${tree}\t${specifier}` -> count
  const problems = [];
  // Rewritten contents are held here until the whole sweep has come back
  // clean. A refusal is a missing export, and the fix for it is a change to
  // the package -- so the tree this runs against has to be the one the last
  // run left, not a half-migrated mix of both.
  const pending = new Map();
  let statementsRewritten = 0;

  for (const tree of layout.trees) {
    for (const file of walk(tree.dir, layout, [])) {
      const original = readFileSync(file, "utf8");
      const source = parse(file);
      const edits = [];
      for (const site of moduleSpecifierSites(ts, source)) {
        const specifier = site.text;
        if (!specifier.startsWith(".")) continue;
        const resolved = resolveSpecifier(file, specifier);
        if (!resolved) continue;
        const moduleID = packageModuleOf(resolved, layout);
        if (moduleID === null) continue;

        const where = `${path.relative(root, file)}:${source.getLineAndCharacterOfPosition(site.node.getStart(source)).line + 1}`;
        // A site that names no binding cannot be mapped onto the root: there
        // is nothing to look up, and `@evener/appwire-client` is a different
        // module from the one the author wrote. Only the published subpath can
        // stand in for a whole module.
        const wholeModule = ["import-namespace", "import-side-effect", "export-star-from", "dynamic-import", "require", "require-equals", "mock-call"].includes(
          site.kind,
        );
        const named = site.bindings.map((binding) => binding.imported);
        let target = null;
        if (moduleID.startsWith("testing/")) {
          target = TESTING_PREFIX + moduleID.slice("testing/".length);
        } else if (site.kind === "import-default") {
          problems.push(`${where}: default import of "${moduleID}"; the package publishes no default export`);
        } else if (site.kind === "import-side-effect") {
          problems.push(
            `${where}: side-effect import of "${moduleID}" names no binding, so there is nothing to map onto ${PACKAGE_NAME}`,
          );
        } else if (wholeModule) {
          // A namespace object, a re-export of everything, or a runtime load of
          // one module: all of them need the module itself, and the package
          // publishes two -- the root and ./docContent. `index` is the root
          // under another name: both `.../typescript` and `.../typescript/index`
          // resolve to index.ts, and refusing them said the root was not
          // published.
          if (moduleID === "index") target = PACKAGE_NAME;
          else if (moduleID === "docContent") target = DOC_CONTENT_SUBPATH;
          else problems.push(`${where}: ${site.kind} of "${moduleID}", which the package does not publish as a subpath`);
        } else if (named.every((binding) => rootExports.has(binding))) {
          target = PACKAGE_NAME;
        } else if (moduleID === "docContent" && named.every((binding) => docContentExports.has(binding))) {
          target = DOC_CONTENT_SUBPATH;
        } else {
          const missing = named.filter((binding) => !rootExports.has(binding));
          problems.push(`${where}: "${moduleID}" exports ${missing.join(", ")}, which ${PACKAGE_NAME} does not publish`);
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

  if (!checkOnly) {
    const conflicts = [];
    let mergedFiles = 0;
    for (const tree of layout.trees) {
      for (const file of walk(tree.dir, layout, [])) {
        const merged = mergeDuplicateImports(file, pending.get(file) ?? readFileSync(file, "utf8"), conflicts);
        if (merged === null) continue;
        pending.set(file, merged);
        mergedFiles += 1;
      }
    }
    if (conflicts.length > 0) {
      console.error(`${conflicts.length} import(s) cannot be merged; nothing was written:`);
      for (const conflict of conflicts) console.error(`  ${conflict}`);
      return 2;
    }
    for (const [file, contents] of pending) writeFileSync(file, contents);
    if (mergedFiles > 0) console.log(`merged duplicate package imports in ${mergedFiles} file(s)`);
  }

  const rows = [...counts.entries()].map(([key, count]) => [...key.split("\t"), count]);
  rows.sort((a, b) => a[0].localeCompare(b[0]) || a[1].localeCompare(b[1]));
  for (const [tree, specifier, count] of rows) console.log(`${String(count).padStart(4)}  ${tree.padEnd(14)} ${specifier}`);
  console.log(`${statementsRewritten} import statement(s) across ${pending.size} file(s)${checkOnly ? " still name a path" : " rewritten"}`);
  return checkOnly && statementsRewritten > 0 ? 1 : 0;
}

process.exit(main());
