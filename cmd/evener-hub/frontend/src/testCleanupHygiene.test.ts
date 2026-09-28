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

const SELF = fileURLToPath(import.meta.url);
const SRC = dirname(SELF);

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

// A file-level registration of Testing Library's cleanup: `afterEach(cleanup)`
// or the arrow spelling, with the whitespace, trailing comma, and optional
// semicolon a formatter may leave. It matches only an afterEach whose whole
// callback is the cleanup call, so a hook that does other work first — like
// testSetup.teardown.test.tsx's simulated throwing cleanup — is not an
// offender. This file itself is skipped: it names the forbidden pattern.
const PER_FILE_CLEANUP = /afterEach\(\s*(?:cleanup|\(\s*\)\s*=>\s*cleanup\(\)\s*,?)\s*\)\s*;?/;

test("no test file registers its own afterEach(cleanup)", () => {
  const offenders = testFilesUnder(SRC)
    .filter((path) => path !== SELF && PER_FILE_CLEANUP.test(readFileSync(path, "utf8")))
    .map((path) => path.slice(SRC.length + 1));
  expect(offenders).toEqual([]);
});
