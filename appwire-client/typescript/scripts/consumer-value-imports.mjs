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

// mobile-native/scripts/*.mts is the SDK migration's named carve-out: those
// files run under tsx and keep a relative import, so they are not consumers of
// the package name and their imports say nothing about the tarball.
const CONSUMER_TREES = ["mobile-native", join("mobile", "src"), join("cmd", "evener-hub", "frontend", "src")];
const CARVE_OUT = join("mobile-native", "scripts");
// Tests and test support are not shipped and are not consumers of the tarball;
// the in-repo `testing` specifier they reach for is deliberately absent from
// the exports map.
const SKIPPED = new Set(["node_modules", "dist", "ios", "android", ".git", "__snapshots__", "testing"]);

function sources(dir, found = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!SKIPPED.has(entry.name) && full !== CARVE_OUT) sources(full, found);
    } else if (entry.isFile() && [".ts", ".tsx"].includes(extname(entry.name)) && !entry.name.includes(".test.")) {
      found.push(full);
    }
  }
  return found;
}

export function consumerValueImports(repoRoot) {
  const bySpecifier = new Map(PACKAGE_SPECIFIERS.map((specifier) => [specifier, new Set()]));
  for (const tree of CONSUMER_TREES) {
    for (const file of sources(join(repoRoot, tree))) {
      const source = ts.createSourceFile(
        file,
        readFileSync(file, "utf8"),
        ts.ScriptTarget.Latest,
        true,
        file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
      );
      for (const statement of source.statements) {
        if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
        const names = bySpecifier.get(statement.moduleSpecifier.text);
        if (!names) continue;
        const clause = statement.importClause;
        // `import type { … }` and inline `type` members are erased before
        // anything runs, so they are not what a runtime check is about.
        if (!clause || clause.isTypeOnly || !clause.namedBindings) continue;
        if (!ts.isNamedImports(clause.namedBindings)) continue;
        for (const element of clause.namedBindings.elements) {
          if (!element.isTypeOnly) names.add((element.propertyName ?? element.name).text);
        }
      }
    }
  }
  return new Map([...bySpecifier].map(([specifier, names]) => [specifier, [...names].sort()]));
}
