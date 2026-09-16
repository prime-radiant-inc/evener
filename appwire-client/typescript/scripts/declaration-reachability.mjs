// Which shipped modules an installed consumer can reach. A module is reachable
// when it is a published specifier's own entry, or when an entry re-exports it
// -- directly, or through another module's re-exports. Each re-export is
// resolved relative to the declaration file that carries it: a barrel written
// beside its siblings spells `export * from "./codec"` inside
// dist/state/navigation/index.d.ts, which names dist/state/navigation/codec,
// not a "./codec" at the dist root, and a barrel behind a barrel reaches what
// that one reaches. A plain import inside a declaration is not a re-export: a
// module only imported for its types is no more reachable to a consumer than
// one nobody names.
import { readFileSync } from "node:fs";
import path from "node:path";
import ts from "typescript";
import { moduleSpecifierSites, parseSource, walkImportGraph } from "../../../scripts/sdk/module-specifiers.mjs";
import { resolveSourceFile } from "../../../scripts/sdk/resolve-source.mjs";

const RE_EXPORT_KINDS = new Set(["export-from", "export-star-from", "export-namespace-from"]);

function reExportedSpecifiers(file, readFile) {
  return moduleSpecifierSites(ts, parseSource(ts, file, readFile(file)))
    .filter((site) => RE_EXPORT_KINDS.has(site.kind))
    .map((site) => site.text);
}

// `entryDeclarations` are the declaration files the published specifiers name,
// as absolute paths under `distDir`. Returns the module names reached, spelled
// as tsconfig.build.json's `files` spells them: dist-relative, posix, without
// the .d.ts suffix.
export function reachableModules(entryDeclarations, distDir, readFile = (file) => readFileSync(file, "utf8")) {
  const reached = walkImportGraph(
    entryDeclarations,
    (file) => reExportedSpecifiers(file, readFile),
    (file, specifier) => (specifier.startsWith(".") ? resolveSourceFile(file, specifier, [".d.ts"]) : null),
  );
  return new Set(
    [...reached].map((file) =>
      path
        .relative(distDir, file)
        .split(path.sep)
        .join("/")
        .replace(/\.d\.ts$/, ""),
    ),
  );
}
