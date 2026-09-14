// package-test-files.mjs — fail the frontend gate if the AppWire package's
// test files stop being collected.
//
// The package lives at appwire-client/typescript/, outside this app's Vitest
// root, so it runs only because vite.config.ts's `test.include` names it
// explicitly. Drop or mistype that entry and every one of those files silently
// leaves the run: `make test-web` still reports a green suite, just a smaller
// one, and nothing else in the tree pins the suite's file count. This compares
// what Vitest collects under the package against what is on disk there, and
// then runs one of them for real: collection alone would not notice the
// package's `vitest` / `react` / `@testing-library/react` imports failing to
// resolve from outside the Vitest root, which is a transform-time error a
// collected-but-never-executed file hides.
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import ts from "typescript";
import { moduleSpecifierSites, parseSource } from "../../../../scripts/sdk/module-specifiers.mjs";
import { resolveSourceFile } from "../../../../scripts/sdk/resolve-source.mjs";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const packageDir = path.resolve(frontend, "../../../appwire-client/typescript");

// Same shape as Vitest's default test glob, applied to the package tree.
const isTestFile = (name) => /\.(test|spec)\.[cm]?[jt]sx?$/.test(name);

// Vitest resolves this one itself; it is the only bare specifier a package
// file may name without an alias.
const SELF_RESOLVING = new Set(["vitest"]);

// Every source file under the package, test or not - the no-app-import sweep
// has to see the whole tree, not only its tests.
export function sourceFilesOnDisk(dir) {
  const found = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === "node_modules" || entry.name === "dist") continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) found.push(...sourceFilesOnDisk(full));
    else if (/\.[cm]?[jt]sx?$/.test(entry.name)) found.push(full);
  }
  return found.sort();
}

// Every form that names a module, not only `from "..."`: a side-effect
// `import "..."`, a `require("...")` and a dynamic `import("...")` all reach
// into the app just as effectively. The leading keyword is what separates an
// import from a comment that happens to mention an app path.
const APP_IMPORT = /(?:\bfrom|\bimport|\brequire)\s*\(?\s*["'][^"']*cmd\/evener-hub\//;

// A package module that reads from disk has to resolve the path against its own
// location, because the package is consumed from wherever the consumer happens
// to run and the process's working directory is not its own. The rule is scoped
// to lines that actually build a filesystem path - an ordinary `../sibling`
// import is not one - and a file that says `import.meta.url` has already
// answered the question. testing/hubWireFixtures.ts read
// `join("..", "testdata", "authwire", "responses.json")` against the frontend's
// CWD; it lives in the app now, which is the other way to satisfy this.
const FS_PATH_CALL =
  /\b(?:join|resolve|readFileSync|readFile|readdirSync|readdir|existsSync|createReadStream|statSync|stat|openSync|open)\s*\(/;
const MODULE_SPECIFIER = /^\s*(?:import\b|export\b[^=]*\bfrom\b|.*\brequire\s*\()/;
const RELATIVE_PATH_LITERAL = /["'](?:\.{1,2}["'/]|[^"']*\btestdata\b)/;
// `import fs from "node:fs"`, `require("fs")` and `await import("node:fs")`
// all reach the filesystem; matching only the first let the other two through.
const FS_MODULE = /(?:\bfrom|\bimport|\brequire)\s*\(?\s*["'](?:node:)?fs(?:\/promises)?["']/;

export function describeCwdRelativeReads(files, read, dir) {
  const offenders = [];
  for (const file of files) {
    const source = read(file);
    if (!FS_MODULE.test(source)) continue;
    if (source.includes("import.meta.url")) continue;
    source.split("\n").forEach((line, index) => {
      if (MODULE_SPECIFIER.test(line)) return;
      if (!FS_PATH_CALL.test(line)) return;
      if (!RELATIVE_PATH_LITERAL.test(line)) return;
      offenders.push(`${path.relative(dir, file)}:${index + 1}: ${line.trim()}`);
    });
  }
  if (offenders.length === 0) return "";
  return [
    "the AppWire package must not read a path resolved against the working directory:",
    ...offenders.map((line) => `  ${line}`),
    "Resolve it with fileURLToPath(new URL(..., import.meta.url)), or move the file into the app.",
  ].join("\n");
}

// The package is a standalone library: nothing in it may reach back into the
// app. Two test files did, and moving them out is only half the fix - this is
// the half that keeps it fixed.
export function describeAppImports(files, read, dir) {
  const offenders = [];
  for (const file of files) {
    for (const line of read(file).split("\n")) {
      if (APP_IMPORT.test(line)) offenders.push(`${path.relative(dir, file)}: ${line.trim()}`);
    }
  }
  if (offenders.length === 0) return "";
  return [
    "the AppWire package must not import from the app:",
    ...offenders.map((line) => `  ${line}`),
    "Move the file into cmd/evener-hub/frontend/src/, or restate the type it needs locally.",
  ].join("\n");
}

// The keys of vite.config.ts's `resolve.alias`, read off its AST rather than
// matched: this is the list the package's bare imports are held against, and a
// regex that missed an entry would fail the gate over an alias that is there.
export function aliasKeysFrom(configText) {
  const source = ts.createSourceFile("vite.config.ts", configText, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const keys = new Set();
  const visit = (node) => {
    if (
      ts.isPropertyAssignment(node) &&
      !ts.isComputedPropertyName(node.name) &&
      node.name.text === "alias" &&
      ts.isObjectLiteralExpression(node.initializer)
    ) {
      for (const property of node.initializer.properties) {
        if (!ts.isPropertyAssignment(property)) continue;
        const name = property.name;
        if (ts.isIdentifier(name) || ts.isStringLiteral(name)) keys.add(name.text);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return keys;
}

// Every module this file names, by any form: a vi.mock or a require reaches a
// dependency exactly as an import does, and an alias it needs is needed just
// as much.
function moduleSpecifiersIn(file, text) {
  return moduleSpecifierSites(ts, parseSource(ts, file, text)).map((site) => site.text);
}

// The package's own files are TypeScript, plus the .mjs tooling its tests
// reach. Extension probing and the directory/index.ts rule are the rewriter's,
// shared rather than restated: an `existsSync` that says yes to a directory
// returns the directory, and the caller reads it as a file.
export const resolvePackageImport = (from, specifier) =>
  resolveSourceFile(from, specifier, [".ts", ".tsx", ".mjs", ".js"]);

// Every bare specifier reachable from the package's test files, mapped to the
// package-relative files that name it. The walk stops at the first bare
// specifier: what is inside node_modules is not this gate's business, and the
// package's own scripts that Vitest never loads (qualify-package.mjs and its
// `ws`) are not reachable from a test, so they are not held to this rule.
export function reachableBareImports(testFiles, read, resolveRelative, dir) {
  const walked = new Set();
  const bare = new Map();
  const walk = (file) => {
    if (walked.has(file)) return;
    walked.add(file);
    for (const specifier of moduleSpecifiersIn(file, read(file))) {
      if (specifier.startsWith("node:")) continue;
      if (specifier.startsWith(".")) {
        const resolved = resolveRelative(file, specifier);
        if (resolved) walk(resolved);
        continue;
      }
      if (!bare.has(specifier)) bare.set(specifier, []);
      bare.get(specifier).push(path.relative(dir, file));
    }
  };
  for (const file of testFiles) walk(file);
  return bare;
}

// The alias Vite would use for a specifier: the longest key that is the
// specifier itself or a path prefix of it. Null when nothing covers it.
export function longestAliasPrefix(specifier, aliasKeys) {
  let longest = null;
  for (const key of aliasKeys) {
    if (specifier !== key && !specifier.startsWith(`${key}/`)) continue;
    if (longest === null || key.length > longest.length) longest = key;
  }
  return longest;
}

// A bare specifier with no alias resolves from the importer, which is inside
// the package. That works on any machine where `npm ci --prefix
// appwire-client/typescript` has run for make test-api-package, and fails in
// CI's web job, which installs only this app - a green local gate over an
// import CI cannot resolve. This is the check that stops the two diverging.
export function describeUnaliasedImports(bare, aliasKeys) {
  const offenders = [];
  for (const [specifier, files] of [...bare].sort()) {
    if (SELF_RESOLVING.has(specifier)) continue;
    // Longest matching prefix, the way Vite picks an alias: a package root
    // entry does not stand in for the subpath entry beside it, and reading
    // only the first two segments called the testing specifier satisfied by
    // the root alias, which maps at index.ts and cannot serve it.
    if (longestAliasPrefix(specifier, aliasKeys) !== null) continue;
    offenders.push(`${specifier}, imported by ${files.join(", ")}`);
  }
  if (offenders.length === 0) return "";
  return [
    "the AppWire package's test graph names bare specifiers that vite.config.ts does not alias:",
    ...offenders.map((line) => `  ${line}`),
    "Vite resolves those from the package directory, which has node_modules only where",
    "`npm ci --prefix appwire-client/typescript` has run - not in CI's web job. Add an alias",
    "in cmd/evener-hub/frontend/vite.config.ts pointing at this app's copy.",
  ].join("\n");
}

export function testFilesOnDisk(dir) {
  const found = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === "node_modules" || entry.name === "dist") continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) found.push(...testFilesOnDisk(full));
    else if (isTestFile(entry.name)) found.push(full);
  }
  return found.sort();
}

// Vitest prints one collected file per line, relative to its root.
export function collectedUnder(listOutput, root, dir) {
  return listOutput
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => path.resolve(root, line))
    .filter((file) => file.startsWith(`${dir}${path.sep}`))
    .sort();
}

// The proof file is the first package test that imports `vitest` - executing
// one is what turns "collected" into "its bare imports really do resolve".
export function pickProofFile(files, read) {
  return files.find((file) => /(^|\n)\s*import\b[^;]*?from\s+["']vitest["']/.test(read(file))) ?? "";
}

// The JSON reporter's own summary, checked rather than trusted: a run that
// collected the file and executed nothing reports numTotalTests 0 and would
// otherwise read as a pass.
export function describeProofRun(report, file, dir) {
  // Compare on the real path relative to the package, not on the absolute
  // string: the reporter and this process can reach the same file by different
  // prefixes (a symlinked worktree, /var against /private/var on macOS), and an
  // inequality there would read as "never executed". realpathSync throws for a
  // path that does not exist, which for this comparison is the same answer as
  // "a different file", so fall back to the unresolved form.
  // A path that does not exist has no real path, and for this comparison that
  // is the same answer as "a different file". Only that case falls back: any
  // other error is a bug here and has to be loud, because a silent fallback
  // turns a broken comparison into a wrong verdict.
  const real = (name) => {
    try {
      return realpathSync(name);
    } catch (err) {
      if (err?.code !== "ENOENT" && err?.code !== "ENOTDIR") throw err;
      return path.resolve(name);
    }
  };
  const key = (name) => path.relative(real(dir), real(name));
  const ran = report.testResults?.some((result) => key(result.name) === key(file)) ?? false;
  if (!ran)
    return `${path.basename(file)} was not executed - the JSON report names ${(report.testResults ?? []).length} other file(s)`;
  if ((report.numTotalTests ?? 0) === 0) return `${path.basename(file)} ran no tests`;
  if ((report.numFailedTests ?? 0) > 0) return `${path.basename(file)} had ${report.numFailedTests} failing test(s)`;
  return "";
}

// Returns "" when the two agree, and otherwise the whole story: an empty disk
// listing is a failure too, because a check that measures nothing passes.
export function describeDifference(onDisk, collected, dir) {
  if (onDisk.length === 0) {
    return `no test files on disk under ${dir} - this check is measuring nothing`;
  }
  const missing = onDisk.filter((file) => !collected.includes(file));
  const extra = collected.filter((file) => !onDisk.includes(file));
  if (missing.length === 0 && extra.length === 0) return "";
  const lines = [`Vitest collects ${collected.length} of the package's ${onDisk.length} test files.`];
  for (const file of missing) lines.push(`  not collected: ${path.relative(dir, file)}`);
  for (const file of extra) lines.push(`  collected but not on disk: ${path.relative(dir, file)}`);
  lines.push("Fix vite.config.ts's test.include - it is the only thing that reaches outside the Vitest root.");
  return lines.join("\n");
}

// pathToFileURL, not a hand-built `file://` string: import.meta.url is
// percent-encoded, so a checkout path containing a space compares unequal and
// the whole check would skip in silence.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  // The LOCAL binary, never `npx`: with node_modules missing or stale, npx
  // falls back to the npm cache or the registry and a default gate must make
  // no network request. scripts/web/web-preflight.sh guards the same hazard
  // by asking ./node_modules/.bin/tsc for its version rather than a bare or
  // npx `tsc`, which resolves to the unrelated tsc@2.0.4 package.
  const vitestBin = path.join(frontend, "node_modules", ".bin", "vitest");
  if (!existsSync(vitestBin)) {
    console.error(`${vitestBin} is missing - run npm ci in cmd/evener-hub/frontend`);
    process.exit(1);
  }
  const listed = spawnSync(vitestBin, ["list", "--filesOnly"], { cwd: frontend, encoding: "utf8" });
  if (listed.status !== 0) {
    console.error(`vitest list failed (exit ${listed.status}):\n${listed.stderr ?? ""}`);
    process.exit(1);
  }
  const readFile = (file) => readFileSync(file, "utf8");
  const packageSources = sourceFilesOnDisk(packageDir);
  for (const problem of [
    describeAppImports(packageSources, readFile, packageDir),
    describeCwdRelativeReads(packageSources, readFile, packageDir),
    describeUnaliasedImports(
      reachableBareImports(testFilesOnDisk(packageDir), readFile, resolvePackageImport, packageDir),
      aliasKeysFrom(readFile(path.join(frontend, "vite.config.ts"))),
    ),
  ]) {
    if (problem) {
      console.error(problem);
      process.exit(1);
    }
  }

  const onDisk = testFilesOnDisk(packageDir);
  const problem = describeDifference(onDisk, collectedUnder(listed.stdout, frontend, packageDir), packageDir);
  if (problem) {
    console.error(problem);
    process.exit(1);
  }

  const proof = pickProofFile(onDisk, readFile);
  if (!proof) {
    console.error("no package test file imports vitest - this check is measuring nothing");
    process.exit(1);
  }
  const reportFile = path.join(mkdtempSync(path.join(os.tmpdir(), "appwire-proof-")), "report.json");
  const ran = spawnSync(vitestBin, ["run", "--reporter=json", `--outputFile=${reportFile}`, proof], {
    cwd: frontend,
    encoding: "utf8",
  });
  if (ran.status !== 0 || !existsSync(reportFile)) {
    console.error(
      `running ${path.relative(packageDir, proof)} failed (exit ${ran.status}):\n${ran.stdout ?? ""}${ran.stderr ?? ""}`,
    );
    process.exit(1);
  }
  const report = JSON.parse(readFileSync(reportFile, "utf8"));
  rmSync(path.dirname(reportFile), { recursive: true, force: true });
  const trouble = describeProofRun(report, proof, packageDir);
  if (trouble) {
    console.error(trouble);
    process.exit(1);
  }
  console.log(
    `the AppWire package imports nothing from the app and reads nothing through the working directory; vitest collects all ${onDisk.length} of its test files, and ${path.relative(packageDir, proof)} executes and passes`,
  );
}
