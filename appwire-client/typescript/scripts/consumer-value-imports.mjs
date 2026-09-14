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
import { extname, join, relative } from "node:path";
import ts from "typescript";
import { isRuntimeSite, moduleSpecifierSites, parseSource } from "../../../scripts/sdk/module-specifiers.mjs";

export const PACKAGE_SPECIFIERS = ["@evener/appwire-client", "@evener/appwire-client/docContent"];

const SOURCE_EXTENSIONS = [".ts", ".tsx", ".mts"];

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
// A site that names the package without naming a binding -- a namespace or
// default import, a bare require, a whole-module dynamic import -- is a
// refusal, not a skip: this derivation's whole job is to say what the tarball
// must provide, and it cannot answer that for a module taken as a whole.
export function packageValuesIn(source, file, problems) {
  const bySpecifier = new Map(PACKAGE_SPECIFIERS.map((specifier) => [specifier, new Set()]));
  for (const site of moduleSpecifierSites(ts, source)) {
    const names = bySpecifier.get(site.text);
    if (!names) continue;
    if (!isRuntimeSite(site)) continue;
    if (site.kind === "import-namespace" || site.kind === "import-default") {
      problems?.push(`${file}: ${site.kind} of ${site.text} names no binding this check can account for`);
      continue;
    }
    for (const binding of site.bindings) {
      if (!binding.typeOnly) names.add(binding.imported);
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
  const problems = [];
  for (const tree of CONSUMER_TREES) {
    for (const file of sources(join(repoRoot, tree))) {
      const found = packageValuesIn(parse(file, readFileSync(file, "utf8")), relative(repoRoot, file), problems);
      for (const [specifier, names] of found) {
        for (const name of names) bySpecifier.get(specifier).add(name);
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
  return new Map([...bySpecifier].map(([specifier, names]) => [specifier, [...names].sort()]));
}
