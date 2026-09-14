// The scripts/*.mts resolution walk, held against a tree built for the case
// that is easiest to stop short on: a chain of `export * from` re-exports.
// Those name no binding, so a walk that follows only sites carrying bindings
// reports success having never reached the module at the end.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { resolveSourceFile } from "../../scripts/sdk/resolve-source.mjs";
import { walkScriptImports } from "../scripts/check-script-imports.mjs";

// The walk's own resolver is tsx's patched CommonJS one, which is not
// installed here. This stands in for it so these cases exercise which sites
// the walk follows; what tsx resolves is what the gate answers on every run.
const resolve = (from: string, specifier: string) => {
  if (!specifier.startsWith(".")) return createRequire(from).resolve(specifier);
  const resolved = resolveSourceFile(from, specifier, [".ts", ".mts"]);
  if (!resolved) throw new Error(`Cannot find module '${specifier}'`);
  return resolved;
};

function tree(files: Record<string, string>) {
  const root = mkdtempSync(path.join(os.tmpdir(), "evener-script-imports-"));
  for (const [relative, contents] of Object.entries(files)) {
    const full = path.join(root, relative);
    mkdirSync(path.dirname(full), { recursive: true });
    writeFileSync(full, contents);
  }
  return root;
}

describe("walkScriptImports", () => {
  it("follows an export * chain to the module at the end", () => {
    const root = tree({
      "scripts/tool.mts": 'export * from "../src/hub";\n',
      "src/hub.ts": 'export * from "./deep";\n',
      "src/deep.ts": 'export const marker = 1;\n',
    });
    try {
      const { failures, reached } = walkScriptImports([path.join(root, "scripts/tool.mts")], root, resolve);
      expect(failures).toEqual([]);
      expect(reached.map((file) => path.relative(root, file)).sort()).toEqual([
        "scripts/tool.mts",
        "src/deep.ts",
        "src/hub.ts",
      ]);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("reports a specifier reached only through that chain when it does not resolve", () => {
    const root = tree({
      "scripts/tool.mts": 'export * from "../src/hub";\n',
      "src/hub.ts": 'export { nothing } from "@evener/not-installed";\n',
    });
    try {
      const { failures } = walkScriptImports([path.join(root, "scripts/tool.mts")], root, resolve);
      expect(failures.map((failure) => `${failure.file}:${failure.specifier}`)).toEqual([
        "src/hub.ts:@evener/not-installed",
      ]);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("does not follow a type-only import, which is erased before anything runs", () => {
    const root = tree({
      "scripts/tool.mts": 'import type { Gone } from "@evener/not-installed";\nexport type Alias = Gone;\n',
    });
    try {
      expect(walkScriptImports([path.join(root, "scripts/tool.mts")], root, resolve).failures).toEqual([]);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
