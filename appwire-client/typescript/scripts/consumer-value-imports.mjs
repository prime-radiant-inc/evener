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

// The package root's runtime values and exported types, read off index.ts so
// the qualification surface cannot drift from what the entry point exports.
// index.ts keeps values in `export {}` and types in `export type {}`, so the
// statement's (or member's) type-only flag is the split. A `export type * from`
// names no binding here -- those types come from the file it re-exports -- and
// is not enumerable from index.ts alone, exactly as the hand-written surface
// never listed them.
export function rootSurface(indexSource) {
  const values = new Set();
  const types = new Set();
  for (const site of moduleSpecifierSites(ts, indexSource)) {
    if (site.kind !== "export-from") continue;
    for (const binding of site.bindings) (site.typeOnly || binding.typeOnly ? types : values).add(binding.imported);
  }
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
