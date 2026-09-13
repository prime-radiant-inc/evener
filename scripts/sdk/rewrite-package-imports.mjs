#!/usr/bin/env node
// Rewrite deep relative imports of the AppWire TypeScript package onto the
// package name, for SDK migration row A4.
//
// When to use it: a tree still spells the package as a path
// (`../../protocol/errors`, `../../appwire-client/typescript/errors`) and
// should spell it as a name (`@evener/appwire-client`). Run it once per
// migration step, read the per-specifier counts it prints, commit the result.
// `--check` makes no edit and exits nonzero if anything is left to rewrite,
// which is what `make lint-package-imports` asserts with a cheaper grep.
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
import { readFileSync, writeFileSync, readdirSync, statSync, existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const frontendDir = path.join(repoRoot, "cmd", "evener-hub", "frontend");
const packageDir = path.join(repoRoot, "appwire-client", "typescript");
// The A3 re-export seam. Every stub re-exports the package module of the same
// name, so a resolved seam path maps to a package module by basename.
const seamDir = path.join(frontendDir, "src", "protocol");

const PACKAGE_NAME = "@evener/appwire-client";
const DOC_CONTENT_SUBPATH = `${PACKAGE_NAME}/docContent`;
const TESTING_PREFIX = `${PACKAGE_NAME}/testing/`;

// Trees whose imports A4 rewrites. `mobile-native/scripts` is the plan's named
// carve-out: those .mts files run under `tsx`, which reads no tsconfig `paths`,
// so they keep the relative specifier A3 gave them.
const TREES = [
  { label: "web", dir: path.join(frontendDir, "src") },
  { label: "mobile-native", dir: path.join(repoRoot, "mobile-native") },
  { label: "mobile/src", dir: path.join(repoRoot, "mobile", "src") },
];
const SKIP_DIRS = new Set(["node_modules", "dist", "build", ".git", "ios", "android", "__snapshots__"]);
const CARVE_OUT = path.join(repoRoot, "mobile-native", "scripts");
const SOURCE_EXTENSIONS = new Set([".ts", ".tsx", ".mts"]);

const require = createRequire(path.join(frontendDir, "package.json"));
const ts = require("typescript");

function usage() {
  console.log(`Usage: node scripts/sdk/rewrite-package-imports.mjs [--check]

Rewrites every import of the AppWire TypeScript package in the web and native
trees from a relative path to the package name, choosing the specifier the
package publishes the named symbols at:

  ${PACKAGE_NAME}                  root exports (index.ts)
  ${DOC_CONTENT_SUBPATH}       the one published subpath
  ${TESTING_PREFIX}<module>   in-repo test support, never a runtime import

Options:
  --check    report only; exit 1 if any import still names a path
  --help     this text

Exits 2, writing nothing, if an import names a symbol the package does not
publish — fix the package's exports first.`);
}

function walk(dir, out) {
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
      if (full === CARVE_OUT || full === seamDir) continue;
      walk(full, out);
    } else if (entry.isFile() && SOURCE_EXTENSIONS.has(path.extname(entry.name))) {
      out.push(full);
    }
  }
  return out;
}

// Resolve a relative specifier the way the bundlers do, enough to tell whether
// it lands inside the package or the seam.
function resolveSpecifier(fromFile, specifier) {
  const base = path.resolve(path.dirname(fromFile), specifier);
  const candidates = [base, `${base}.ts`, `${base}.tsx`, path.join(base, "index.ts")];
  for (const candidate of candidates) {
    if (existsSync(candidate) && statSync(candidate).isFile()) return candidate;
  }
  return null;
}

// The package module a resolved path names: "errors", "testing/fakeClient",
// "types.gen". Returns null for anything outside the package and the seam.
function packageModuleOf(resolved) {
  for (const root of [packageDir, seamDir]) {
    const relative = path.relative(root, resolved);
    if (relative.startsWith("..") || path.isAbsolute(relative)) continue;
    return relative.replace(/\.(ts|tsx)$/, "").split(path.sep).join("/");
  }
  return null;
}

function parse(file) {
  return ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true, file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
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
function specifierSites(source) {
  const sites = [];
  const visit = (node) => {
    if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) {
      const clause = node.importClause;
      const bindings = [];
      let kind = "side-effect";
      if (clause) {
        if (clause.name) {
          bindings.push("default");
          kind = "default";
        }
        if (clause.namedBindings) {
          if (ts.isNamespaceImport(clause.namedBindings)) {
            kind = "namespace";
          } else {
            kind = kind === "default" ? "default" : "named";
            for (const element of clause.namedBindings.elements) {
              bindings.push((element.propertyName ?? element.name).text);
            }
          }
        }
      }
      sites.push({ node: node.moduleSpecifier, kind, bindings });
    } else if (ts.isExportDeclaration(node) && node.moduleSpecifier && ts.isStringLiteral(node.moduleSpecifier)) {
      const bindings = [];
      let kind = "namespace";
      if (node.exportClause && ts.isNamedExports(node.exportClause)) {
        kind = "named";
        for (const element of node.exportClause.elements) bindings.push((element.propertyName ?? element.name).text);
      }
      sites.push({ node: node.moduleSpecifier, kind, bindings });
    } else if (ts.isImportTypeNode(node) && ts.isLiteralTypeNode(node.argument) && ts.isStringLiteral(node.argument.literal)) {
      // `import("../protocol/types.gen").LaunchConfigLayer`
      const bindings = [];
      let qualifier = node.qualifier;
      if (qualifier) {
        while (ts.isQualifiedName(qualifier)) qualifier = qualifier.left;
        bindings.push(qualifier.text);
      }
      sites.push({ node: node.argument.literal, kind: qualifier ? "named" : "namespace", bindings });
    } else if (ts.isCallExpression(node)) {
      const isDynamicImport = node.expression.kind === ts.SyntaxKind.ImportKeyword;
      const isMock = ts.isPropertyAccessExpression(node.expression) && ["mock", "doMock", "importActual", "importMock", "unmock"].includes(node.expression.name.text);
      if ((isDynamicImport || isMock) && node.arguments.length > 0 && ts.isStringLiteral(node.arguments[0])) {
        sites.push({ node: node.arguments[0], kind: "namespace", bindings: [] });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return sites;
}

function main() {
  const args = process.argv.slice(2);
  if (args.includes("--help") || args.includes("-h")) {
    usage();
    return 0;
  }
  const checkOnly = args.includes("--check");
  const unknown = args.filter((arg) => !["--check", "--help", "-h"].includes(arg));
  if (unknown.length > 0) {
    console.error(`unknown argument: ${unknown.join(" ")}`);
    usage();
    return 2;
  }

  const rootExports = exportedNames(path.join(packageDir, "index.ts"));
  const docContentExports = exportedNames(path.join(packageDir, "docContent.ts"));

  const counts = new Map(); // `${tree}\t${specifier}` -> count
  const problems = [];
  let filesChanged = 0;
  let statementsRewritten = 0;

  for (const tree of TREES) {
    for (const file of walk(tree.dir, [])) {
      const original = readFileSync(file, "utf8");
      const source = parse(file);
      const edits = [];
      for (const site of specifierSites(source)) {
        const specifier = site.node.text;
        if (!specifier.startsWith(".")) continue;
        const resolved = resolveSpecifier(file, specifier);
        if (!resolved) continue;
        const moduleID = packageModuleOf(resolved);
        if (moduleID === null) continue;

        const where = `${path.relative(repoRoot, file)}:${source.getLineAndCharacterOfPosition(site.node.getStart(source)).line + 1}`;
        let target = null;
        if (moduleID.startsWith("testing/")) {
          target = TESTING_PREFIX + moduleID.slice("testing/".length);
        } else if (site.kind === "namespace") {
          // A namespace object has to come from the module itself, and the
          // package publishes exactly one module besides the root.
          if (moduleID === "docContent") target = DOC_CONTENT_SUBPATH;
          else problems.push(`${where}: namespace import of "${moduleID}", which the package does not publish as a subpath`);
        } else if (site.kind === "default") {
          problems.push(`${where}: default import of "${moduleID}"; the package publishes no default export`);
        } else if (site.bindings.every((binding) => rootExports.has(binding))) {
          target = PACKAGE_NAME;
        } else if (moduleID === "docContent" && site.bindings.every((binding) => docContentExports.has(binding))) {
          target = DOC_CONTENT_SUBPATH;
        } else {
          const missing = site.bindings.filter((binding) => !rootExports.has(binding));
          problems.push(`${where}: "${moduleID}" exports ${missing.join(", ")}, which ${PACKAGE_NAME} does not publish`);
        }
        if (target === null) continue;
        edits.push({ start: site.node.getStart(source), end: site.node.getEnd(), target });
        counts.set(`${tree.label}\t${target}`, (counts.get(`${tree.label}\t${target}`) ?? 0) + 1);
        statementsRewritten += 1;
      }
      if (edits.length === 0) continue;
      filesChanged += 1;
      if (checkOnly) continue;
      let updated = original;
      for (const edit of edits.sort((a, b) => b.start - a.start)) {
        updated = `${updated.slice(0, edit.start)}"${edit.target}"${updated.slice(edit.end)}`;
      }
      writeFileSync(file, updated);
    }
  }

  if (problems.length > 0) {
    console.error(`${problems.length} import(s) name a symbol the package does not publish; nothing was written:`);
    for (const problem of problems) console.error(`  ${problem}`);
    return 2;
  }

  const rows = [...counts.entries()].map(([key, count]) => [...key.split("\t"), count]);
  rows.sort((a, b) => a[0].localeCompare(b[0]) || a[1].localeCompare(b[1]));
  for (const [tree, specifier, count] of rows) console.log(`${String(count).padStart(4)}  ${tree.padEnd(14)} ${specifier}`);
  console.log(`${statementsRewritten} import statement(s) across ${filesChanged} file(s)${checkOnly ? " still name a path" : " rewritten"}`);
  return checkOnly && statementsRewritten > 0 ? 1 : 0;
}

process.exit(main());
