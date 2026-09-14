// The runtime values every in-repo consumer takes from this package, read off
// their import graph. resolve-check.mjs restates that list as real imports so
// a bare Node consumer can prove the installed tarball provides all of them;
// this derivation is what keeps the restatement honest.
//
// All three app trees count, not just the native two. Metro's resolveRequest
// treats cmd/evener-hub/frontend/src as shared headless source and bundles it
// into the native app, so a value only the web tree imports -- errorText, by
// way of panes/spawn/usePluginPreview.ts -- is still a value the tarball has
// to provide. Over-inclusion only strengthens the check: every name here is
// something some consumer imports.
import { readdirSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
import ts from "typescript";

export const PACKAGE_SPECIFIERS = ["@evener/appwire-client", "@evener/appwire-client/docContent"];

const SOURCE_EXTENSIONS = [".ts", ".tsx", ".mts"];

export function parse(file, text) {
  return ts.createSourceFile(
    file,
    text,
    ts.ScriptTarget.Latest,
    true,
    file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
}

// The runtime values a source file takes from each of this package's published
// specifiers, as a Map of specifier to the EXPORTED names it names (the left
// half of `X as Y`, since that is what the package has to provide).
//
// Re-exports count: `export { graftContinuationTree } from "@evener/appwire-client"`
// is a consumer taking a value, exactly like an import. Type-only statements
// and inline `type` members do not: they are erased before anything runs.
export function packageValuesIn(source) {
  const bySpecifier = new Map(PACKAGE_SPECIFIERS.map((specifier) => [specifier, new Set()]));
  for (const statement of source.statements) {
    const isImport = ts.isImportDeclaration(statement);
    const isExport = ts.isExportDeclaration(statement);
    if (!isImport && !isExport) continue;
    const moduleSpecifier = statement.moduleSpecifier;
    if (!moduleSpecifier || !ts.isStringLiteralLike(moduleSpecifier)) continue;
    const names = bySpecifier.get(moduleSpecifier.text);
    if (!names) continue;
    let elements;
    if (isImport) {
      const clause = statement.importClause;
      if (!clause || clause.isTypeOnly || !clause.namedBindings) continue;
      if (!ts.isNamedImports(clause.namedBindings)) continue;
      elements = clause.namedBindings.elements;
    } else {
      // `export * from` names nothing to check; only a named list does.
      if (statement.isTypeOnly || !statement.exportClause || !ts.isNamedExports(statement.exportClause)) continue;
      elements = statement.exportClause.elements;
    }
    for (const element of elements) {
      if (!element.isTypeOnly) names.add((element.propertyName ?? element.name).text);
    }
  }
  return bySpecifier;
}

const CONSUMER_TREES = ["mobile-native", join("mobile", "src"), join("cmd", "evener-hub", "frontend", "src")];
// Tests are not shipped and are not consumers of the tarball, which the
// `.test.` filter below handles. Directory names are not the place to say so:
// skipping every directory called `testing` also skipped the app's own
// src/stores/testing and src/panes/session/testing, which are ordinary source
// that happens to serve tests -- and the package's own testing/ tree is not
// under any of these directories to begin with.
const SKIPPED = new Set(["node_modules", "dist", "ios", "android", ".git", "__snapshots__"]);

function sources(dir, found = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!SKIPPED.has(entry.name)) sources(full, found);
    } else if (entry.isFile() && SOURCE_EXTENSIONS.includes(extname(entry.name)) && !entry.name.includes(".test.")) {
      found.push(full);
    }
  }
  return found;
}

export function consumerValueImports(repoRoot) {
  const bySpecifier = new Map(PACKAGE_SPECIFIERS.map((specifier) => [specifier, new Set()]));
  for (const tree of CONSUMER_TREES) {
    for (const file of sources(join(repoRoot, tree))) {
      const found = packageValuesIn(parse(file, readFileSync(file, "utf8")));
      for (const [specifier, names] of found) {
        for (const name of names) bySpecifier.get(specifier).add(name);
      }
    }
  }
  return new Map([...bySpecifier].map(([specifier, names]) => [specifier, [...names].sort()]));
}
