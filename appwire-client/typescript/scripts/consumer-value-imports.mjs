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
import { readdirSync, readFileSync } from "node:fs";
import { extname, join, relative } from "node:path";
import ts from "typescript";
import { moduleSpecifierSites, parseSource } from "../../../scripts/sdk/module-specifiers.mjs";
import { CONSUMER_TREES, isTestFile, SKIPPED_DIRS, SOURCE_EXTENSIONS } from "../../../scripts/sdk/source-files.mjs";

export const PACKAGE_SPECIFIERS = ["@evener/appwire-client", "@evener/appwire-client/docContent"];

export function parse(file, text) {
  return parseSource(ts, file, text);
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

export function packageValuesIn(source, file, problems) {
  const bySpecifier = new Map(PACKAGE_SPECIFIERS.map((specifier) => [specifier, new Set()]));
  for (const site of moduleSpecifierSites(ts, source)) {
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

// Tests are not shipped and are not consumers of the tarball, which the
// isTestFile filter below handles. Directory names are not the place to say so:
// skipping every directory called `testing` also skipped the app's own
// src/stores/testing and src/panes/session/testing, which are ordinary source
// that happens to serve tests -- and the package's own testing/ tree is not
// under any of these directories to begin with.
function sources(dir, found = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!SKIPPED_DIRS.has(entry.name)) sources(full, found);
    } else if (entry.isFile() && SOURCE_EXTENSIONS.includes(extname(entry.name)) && !isTestFile(entry.name)) {
      found.push(full);
    }
  }
  return found;
}

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
    for (const file of sources(join(repoRoot, tree))) {
      const source = parse(file, readFileSync(file, "utf8"));
      for (const site of moduleSpecifierSites(ts, source)) {
        const entry = usage.get(site.text);
        if (entry) entry.used = true;
      }
      const found = packageValuesIn(source, relative(repoRoot, file), problems);
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
