// package-test-files.mjs — fail the frontend gate if the AppWire package's
// test files stop being collected.
//
// The package lives at appwire-client/typescript/, outside this app's Vitest
// root, so it runs only because vite.config.ts's `test.include` names it
// explicitly. Drop or mistype that entry and every one of those files silently
// leaves the run: `make test-web` still reports a green suite, just a smaller
// one, and nothing else in the tree pins the suite's file count. This compares
// what Vitest collects under the package against what is on disk there.
import { spawnSync } from "node:child_process";
import { readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const packageDir = path.resolve(frontend, "../../../appwire-client/typescript");

// Same shape as Vitest's default test glob, applied to the package tree.
const isTestFile = (name) => /\.(test|spec)\.[cm]?[jt]sx?$/.test(name);

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

if (import.meta.url === `file://${process.argv[1]}`) {
  const listed = spawnSync("npx", ["vitest", "list", "--filesOnly"], { cwd: frontend, encoding: "utf8" });
  if (listed.status !== 0) {
    console.error(`vitest list failed (exit ${listed.status}):\n${listed.stderr ?? ""}`);
    process.exit(1);
  }
  const onDisk = testFilesOnDisk(packageDir);
  const problem = describeDifference(onDisk, collectedUnder(listed.stdout, frontend, packageDir), packageDir);
  if (problem) {
    console.error(problem);
    process.exit(1);
  }
  console.log(`vitest collects all ${onDisk.length} AppWire package test files`);
}
