// package-coverage.mjs — fail the coverage run if a package module is absent
// from the report rather than scored.
//
// vite.config.ts names the whole source tree in coverage.include on purpose:
// Vitest reports only the files a test actually loaded, so a module with no
// test at all would be ABSENT rather than the 0% it is. That enumeration stops
// at the Vitest root, and the AppWire package sits outside it:
// coverage.allowExternal brings in the package files a test DOES load, but
// nothing brings in one that no test touches. Vitest 4 removed coverage.all,
// and an include glob - relative or absolute - does not reach past the root
// either (both measured). So the floor is enforced by this check instead: every
// module the package's own build compiles, and that emits any runtime code, has
// to appear as a file entry in coverage-summary.json.
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const packageDir = path.resolve(frontend, "../../../appwire-client/typescript");

// A declaration-only module emits no statements, so v8 has nothing to score and
// its absence is correct. Everything else - a function, a value, a class, an
// enum, a value re-export - runs.
export function hasRuntimeExport(source) {
  return source
    .split("\n")
    .some((line) =>
      /^export\s+(?!type\b|interface\b)(?:default\b|async\b|function\b|const\b|let\b|var\b|class\b|enum\b|\{|\*)/.test(
        line,
      ),
    );
}

// The build's own file list is the definition of "a module this package ships
// the code for", so a module added to the build and to no test is exactly what
// this catches.
export function compiledModules(buildConfigText) {
  return (JSON.parse(buildConfigText).files ?? []).filter((file) => file.endsWith(".ts"));
}

export function describeMissingCoverage(expected, reported, dir) {
  const have = new Set(reported.map((file) => path.relative(dir, path.resolve(file))));
  const missing = expected.filter((file) => !have.has(file));
  if (expected.length === 0) return "no compiled package modules found - this check is measuring nothing";
  if (missing.length === 0) return "";
  return [
    `${missing.length} AppWire package module(s) compile but never appear in the coverage report:`,
    ...missing.map((file) => `  ${file}`),
    "An absent module scores as nothing rather than as 0%. Give it a test, or exclude it deliberately in vite.config.ts.",
  ].join("\n");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const summaryPath = path.join(frontend, "coverage", "coverage-summary.json");
  let summary;
  try {
    summary = JSON.parse(readFileSync(summaryPath, "utf8"));
  } catch (err) {
    console.error(`${summaryPath} is unreadable (${err.message}) - run vitest with --coverage first`);
    process.exit(1);
  }
  const expected = compiledModules(readFileSync(path.join(packageDir, "tsconfig.build.json"), "utf8")).filter((file) =>
    hasRuntimeExport(readFileSync(path.join(packageDir, file), "utf8")),
  );
  const reported = Object.keys(summary).filter((key) => key !== "total");
  const problem = describeMissingCoverage(expected, reported, packageDir);
  if (problem) {
    console.error(problem);
    process.exit(1);
  }
  console.log(`all ${expected.length} compiled AppWire package modules are scored in the coverage report`);
}
