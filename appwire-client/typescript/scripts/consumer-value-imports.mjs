// The runtime values every in-repo consumer takes from this package, read off
// their import graph. qualify-package.mjs generates a real consumer program
// from this list and runs it against the installed tarball, so the names the
// apps import are proved to resolve from the packed package.
//
// All three app trees count, not just the native two. Metro's resolveRequest
// treats cmd/evener-hub/frontend/src as shared headless source and bundles it
// into the native app, so a value only the web tree imports -- errorText, by
// way of panes/spawn/usePluginPreview.ts -- is still a value the tarball has
// to provide. Over-inclusion only strengthens the check: every name here is
// something some consumer imports.
import { readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { moduleSpecifierSites, parseSource } from "../../../scripts/sdk/module-specifiers.mjs";
import { resolveSourceFile } from "../../../scripts/sdk/resolve-source.mjs";
import { CONSUMER_TREES, isTestFile, sourceFiles } from "../../../scripts/sdk/source-files.mjs";

// The published specifiers, from package.json's own exports rather than
// restated: each key is a subpath, "." the root. The in-repo testing/ alias is
// deliberately absent from exports and is not one of these.
export function packageSpecifiers(manifest) {
  return Object.keys(manifest.exports)
    .filter((subpath) => !subpath.startsWith("./testing"))
    .map((subpath) => (subpath === "." ? manifest.name : `${manifest.name}${subpath.slice(1)}`));
}

// import.meta.dirname where the runtime sets it (Node and Vitest do), the
// file-URL form only where it does not -- the frontend's Vitest imports this
// module across the app boundary with a non-file import.meta.url.
const packageDir = join(import.meta.dirname ?? fileURLToPath(new URL(".", import.meta.url)), "..");
export const PACKAGE_SPECIFIERS = packageSpecifiers(JSON.parse(readFileSync(join(packageDir, "package.json"), "utf8")));

export function parse(file, text) {
  return parseSource(ts, file, text);
}

// The value and type names a module exports, following its re-exports into the
// modules they name: `export *` re-exports both values and types, `export type
// *` the types only. So the type reachable only through
// `export type * from "./types.gen"` -- Thread and every other generated
// protocol type -- is in the surface, and a value reachable only through a
// value star is too. The rewriter's export collector follows the same stars;
// this one keeps the value/type split the qualification needs. Memoised per
// file, and the entry is set before recursing so a re-export cycle terminates.
const PACKAGE_SOURCE_EXTENSIONS = [".ts", ".tsx"];
function moduleSurface(file, readFile, cache) {
  const cached = cache.get(file);
  if (cached) return cached;
  const surface = { values: new Set(), types: new Set() };
  cache.set(file, surface);
  const source = parseSource(ts, file, readFile(file));
  for (const statement of source.statements) {
    if (ts.isExportDeclaration(statement)) {
      const clause = statement.exportClause;
      if (clause && ts.isNamedExports(clause)) {
        for (const element of clause.elements) {
          (statement.isTypeOnly || element.isTypeOnly ? surface.types : surface.values).add(element.name.text);
        }
      } else if (clause && ts.isNamespaceExport(clause)) {
        surface.values.add(clause.name.text);
      } else if (statement.moduleSpecifier && ts.isStringLiteralLike(statement.moduleSpecifier)) {
        const target = resolveSourceFile(file, statement.moduleSpecifier.text, PACKAGE_SOURCE_EXTENSIONS);
        if (target) {
          const inner = moduleSurface(target, readFile, cache);
          for (const name of inner.types) surface.types.add(name);
          if (!statement.isTypeOnly) for (const name of inner.values) surface.values.add(name);
        }
      }
      continue;
    }
    const modifiers = ts.canHaveModifiers(statement) ? (ts.getModifiers(statement) ?? []) : [];
    if (!modifiers.some((modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword)) continue;
    if (ts.isTypeAliasDeclaration(statement) || ts.isInterfaceDeclaration(statement)) {
      if (statement.name) surface.types.add(statement.name.text);
    } else if (ts.isVariableStatement(statement)) {
      for (const declaration of statement.declarationList.declarations) {
        if (ts.isIdentifier(declaration.name)) surface.values.add(declaration.name.text);
      }
    } else if (statement.name && ts.isIdentifier(statement.name)) {
      surface.values.add(statement.name.text);
    }
  }
  return surface;
}

// A published entry's runtime values and exported types, read off its barrel
// (and the modules it re-exports) so the qualification surface cannot drift
// from what the entry point exposes: an export added anywhere the barrel
// re-exports is qualified without editing anything here. The root's index.ts
// and a subpath's own index.ts are read the same way.
export function entrySurface(entryFile, readFile = (file) => readFileSync(file, "utf8")) {
  const { values, types } = moduleSurface(entryFile, readFile, new Map());
  return { values: [...values].sort(), types: [...types].sort() };
}

// The runtime values a source file takes from each of this package's published
// specifiers, as a Map of specifier to the EXPORTED names it names (the left
// half of `X as Y`, since that is what the package has to provide).
//
// Re-exports count: `export { graftContinuationTree } from "@evener/appwire-client"`
// is a consumer taking a value, exactly like an import. Type-only statements
// and inline `type` members do not: they are erased before anything runs.
//
// Only a site that NAMES what it takes can be accounted for, so the two kinds
// that do are an allowlist and every other kind is a problem. Naming the
// refused kinds instead left require, dynamic import, side-effect import,
// mock call and star re-export falling through in silence -- each of them a
// module taken whole, which is exactly what this derivation cannot enumerate.
// Type-only sites are the one silent skip, because they are erased before
// anything runs.
const ACCOUNTABLE_KINDS = new Set(["import-named", "export-from"]);

// `export * as ns from "pkg"` takes the module, not any value out of it, so
// there is nothing for this derivation to add -- and nothing it cannot
// account for either. Refusing it would fail a re-export that is perfectly
// resolvable against the tarball.
const NAMES_NO_VALUE_KINDS = new Set(["export-namespace-from"]);

function valuesFromSites(sites, file, problems) {
  const bySpecifier = new Map(PACKAGE_SPECIFIERS.map((specifier) => [specifier, new Set()]));
  for (const site of sites) {
    const names = bySpecifier.get(site.text);
    if (!names) continue;
    if (site.typeOnly) continue;
    if (NAMES_NO_VALUE_KINDS.has(site.kind)) continue;
    if (!ACCOUNTABLE_KINDS.has(site.kind)) {
      problems?.push(`${file}: ${site.kind} of ${site.text} names no binding this check can account for`);
      continue;
    }
    for (const binding of site.bindings) {
      if (!binding.typeOnly) names.add(binding.imported);
    }
  }
  return bySpecifier;
}

export function packageValuesIn(source, file, problems) {
  return valuesFromSites(moduleSpecifierSites(ts, source), file, problems);
}

// Tests are not shipped and are not consumers of the tarball, so isTestFile
// filters them out of the walk. A directory name is not the place to say so:
// skipping every directory called `testing` also skipped the app's own
// src/stores/testing and src/panes/session/testing, ordinary source that
// happens to serve tests.
const consumerSources = (dir) => sourceFiles(dir, { keep: (name) => !isTestFile(name) });

// What this repository's consumers do with each published specifier: the
// runtime VALUES they name, and whether they reach the module at all.
//
// The two are different questions. A consumer whose only use is
// `export * as ns from "pkg"`, or a type-only import, names no runtime value
// and still needs the module to resolve -- so a specifier can be legitimately
// used with an empty value list, and requiring a non-empty one failed
// qualification over a consumer that was doing nothing wrong.
export function consumerPackageUsage(repoRoot) {
  const usage = new Map(PACKAGE_SPECIFIERS.map((specifier) => [specifier, { values: new Set(), used: false }]));
  const problems = [];
  for (const tree of CONSUMER_TREES) {
    for (const file of consumerSources(join(repoRoot, tree))) {
      // Sites once per file, for both questions: whether the module is reached
      // and which values it names.
      const sites = moduleSpecifierSites(ts, parse(file, readFileSync(file, "utf8")));
      for (const site of sites) {
        const entry = usage.get(site.text);
        if (entry) entry.used = true;
      }
      const found = valuesFromSites(sites, relative(repoRoot, file), problems);
      for (const [specifier, names] of found) {
        for (const name of names) usage.get(specifier).values.add(name);
      }
    }
  }
  if (problems.length > 0) {
    throw new Error(
      [
        "these consumers take the package as a whole, so what the tarball must provide cannot be derived from them:",
        ...problems.map((problem) => `  ${problem}`),
        "Import the names you use instead.",
      ].join("\n"),
    );
  }
  return new Map(
    [...usage].map(([specifier, entry]) => [specifier, { values: [...entry.values].sort(), used: entry.used }]),
  );
}

// The value lists alone, for callers that only ask what the tarball must
// provide.
export function consumerValueImports(repoRoot) {
  return new Map([...consumerPackageUsage(repoRoot)].map(([specifier, entry]) => [specifier, entry.values]));
}
