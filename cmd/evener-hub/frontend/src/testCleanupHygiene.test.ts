// @vitest-environment node

// The shared setup (testSetup.ts) unmounts every rendered tree after each test
// through onTestFinished, so a test file registering its own `afterEach(cleanup)`
// is redundant. It is also worse than redundant: a file-level hook runs before
// the setup's teardown, so a tree that throws as it unmounts used to skip the
// setup's own teardown (#2604 moved the teardown into onTestFinished). This
// pins that no test file reintroduces the registration.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SRC = dirname(fileURLToPath(import.meta.url));

function testFilesUnder(dir: string): string[] {
  const files: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...testFilesUnder(path));
    } else if (/\.test\.tsx?$/.test(entry.name)) {
      files.push(path);
    }
  }
  return files.sort();
}

// A file-level registration of Testing Library's cleanup, in either spelling the
// suite used. Anchored to a whole line, with same-line spacing only, so prose
// mentioning cleanup() is not matched and a call split across lines is not read
// as a file-level hook.
const FILE_LEVEL_CLEANUP = /^[ \t]*afterEach\((?:cleanup|\(\)[ \t]*=>[ \t]*cleanup\(\))\);[ \t]*$/m;

test("no test file registers its own afterEach(cleanup)", () => {
  const offenders = testFilesUnder(SRC)
    .filter((path) => FILE_LEVEL_CLEANUP.test(readFileSync(path, "utf8")))
    .map((path) => path.slice(SRC.length + 1));
  expect(offenders).toEqual([]);
});
