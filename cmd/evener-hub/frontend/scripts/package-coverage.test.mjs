import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { compiledModules, describeMissingCoverage, hasRuntimeExport } from "./package-coverage.mjs";

const dir = path.resolve("/repo/appwire-client/typescript");

test("hasRuntimeExport sees functions, values, classes, enums and value re-exports", () => {
  for (const source of [
    "export function f() {}\n",
    "export const x = 1;\n",
    "export class C {}\n",
    "export enum E {\n  A = 1,\n}\n",
    'export { a } from "./other";\n',
    'export * from "./other";\n',
    "export default 1;\n",
    "export async function f() {}\n",
  ]) {
    assert.equal(hasRuntimeExport(source), true, source);
  }
});

test("hasRuntimeExport treats a declaration-only module as emitting nothing", () => {
  const source = [
    'import type { AppwireClient } from "./client";',
    "export interface AppwireClientLike {",
    '  connect: AppwireClient["connect"];',
    "}",
    'export type ConnectionState = "idle" | "open";',
    'export type { AppwireClient } from "./client";',
    "",
  ].join("\n");
  assert.equal(hasRuntimeExport(source), false);
});

test("compiledModules reads the build's own file list and keeps only TypeScript", () => {
  const config = JSON.stringify({ files: ["index.ts", "reducer.ts", "README.md"] });
  assert.deepEqual(compiledModules(config), ["index.ts", "reducer.ts"]);
});

test("compiledModules tolerates a build config with no files key", () => {
  assert.deepEqual(compiledModules(JSON.stringify({ compilerOptions: {} })), []);
});

test("describeMissingCoverage is silent when every module is scored", () => {
  const expected = ["index.ts", "reducer.ts"];
  const reported = [path.join(dir, "index.ts"), path.join(dir, "reducer.ts"), "/elsewhere/app.ts"];
  assert.equal(describeMissingCoverage(expected, reported, dir), "");
});

test("describeMissingCoverage names each module the report never mentions", () => {
  const message = describeMissingCoverage(["index.ts", "scratchUntested.ts"], [path.join(dir, "index.ts")], dir);
  assert.match(message, /1 AppWire package module\(s\) compile but never appear/);
  assert.match(message, /scratchUntested\.ts/);
});

test("describeMissingCoverage fails an empty expectation rather than passing on nothing", () => {
  assert.match(describeMissingCoverage([], [], dir), /measuring nothing/);
});

// The package's declaration-only modules. The guard excuses them through
// hasRuntimeExport, not through this list; the list pins which modules are
// expected to emit nothing, so a module that loses its runtime surface by
// accident turns this test red instead of quietly leaving the report.
const DECLARATION_ONLY_MODULES = new Set(["clientLike.ts", "modelCatalogTypes.ts"]);

test("the real build config lists the package's modules, so the expectation is not empty", () => {
  const real = fileURLToPath(new URL("../../../../appwire-client/typescript", import.meta.url));
  const modules = compiledModules(readFileSync(path.join(real, "tsconfig.build.json"), "utf8"));
  assert(modules.includes("index.ts"));
  assert(modules.includes("sharedNotesAvailability.ts"));
  for (const file of DECLARATION_ONLY_MODULES) assert(modules.includes(file), `${file} is not in the build`);
  assert(
    modules.every(
      (file) => hasRuntimeExport(readFileSync(path.join(real, file), "utf8")) || DECLARATION_ONLY_MODULES.has(file),
    ),
  );
});
