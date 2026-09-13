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
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const packageDir = path.resolve(frontend, "../../../appwire-client/typescript");

// Same shape as Vitest's default test glob, applied to the package tree.
const isTestFile = (name) => /\.(test|spec)\.[cm]?[jt]sx?$/.test(name);

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

// The package is a standalone library: nothing in it may reach back into the
// app. Two test files did, and moving them out is only half the fix - this is
// the half that keeps it fixed.
export function describeAppImports(files, read, dir) {
  const offenders = [];
  for (const file of files) {
    for (const line of read(file).split("\n")) {
      if (/from\s+["'][^"']*cmd\/evener-hub\//.test(line)) offenders.push(`${path.relative(dir, file)}: ${line.trim()}`);
    }
  }
  if (offenders.length === 0) return "";
  return [
    "the AppWire package must not import from the app:",
    ...offenders.map((line) => `  ${line}`),
    "Move the file into cmd/evener-hub/frontend/src/, or restate the type it needs locally.",
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
  // Compare on the path relative to the package, not on the absolute string:
  // the reporter and this process can reach the same file by different
  // prefixes (a symlinked worktree, /var vs /private/var), and an inequality
  // there would read as "never executed".
  const key = (name) => path.relative(dir, path.resolve(name));
  const ran = report.testResults?.some((result) => key(result.name) === key(file)) ?? false;
  if (!ran) return `${path.basename(file)} was not executed - the JSON report names ${(report.testResults ?? []).length} other file(s)`;
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
  const reaching = describeAppImports(sourceFilesOnDisk(packageDir), readFile, packageDir);
  if (reaching) {
    console.error(reaching);
    process.exit(1);
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
    console.error(`running ${path.relative(packageDir, proof)} failed (exit ${ran.status}):\n${ran.stdout ?? ""}${ran.stderr ?? ""}`);
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
    `the AppWire package imports nothing from the app; vitest collects all ${onDisk.length} of its test files, and ${path.relative(packageDir, proof)} executes and passes`,
  );
}
