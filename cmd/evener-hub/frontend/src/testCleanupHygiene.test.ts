// @vitest-environment node

// The shared setup (testSetup.ts) unmounts every rendered tree after each test
// through onTestFinished, so a test file whose whole afterEach exists to call
// Testing Library's cleanup() is redundant. It is also worse than redundant: a
// file-level hook runs before the setup's teardown, so a tree that throws as it
// unmounts used to skip the setup's own teardown (#2604 moved the teardown into
// onTestFinished). This pins that no test file reintroduces such a hook, in any
// of the spellings the suite used: `afterEach(cleanup)`, `afterEach(() =>
// cleanup())`, and `afterEach(() => { cleanup(); })`. A hook that also does
// other work is not this pattern and is left alone.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SELF = fileURLToPath(import.meta.url);
const SRC = dirname(SELF); // cmd/evener-hub/frontend/src
const REPO_ROOT = join(SRC, "..", "..", "..", "..");
// vite.config.ts's test.include runs the AppWire package's tests under the same
// setupFiles, so a registration there is as redundant as one in src.
const ROOTS = [SRC, join(REPO_ROOT, "appwire-client", "typescript")];

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

// An afterEach whose whole callback is the cleanup call, in any spelling. The
// block branch allows only whitespace and an optional trailing comma between the
// braces, so a hook with any other statement does not match.
const PER_FILE_CLEANUP =
  /afterEach\(\s*(?:cleanup\s*|\(\s*\)\s*=>\s*(?:cleanup\(\)\s*|{\s*cleanup\(\)\s*;?\s*,?\s*})\s*)\s*\)\s*;?/;

// Comments carry prose about cleanup() (and this file names the pattern), so the
// match runs on a comment-stripped copy.
function withoutComments(text: string): string {
  return text.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/[^\n]*/g, "$1");
}

function label(path: string): string {
  const fromSrc = relative(SRC, path);
  if (!fromSrc.startsWith("..")) return join("src", fromSrc);
  return relative(REPO_ROOT, path);
}

test("no test file registers an afterEach whose only call is cleanup()", () => {
  const offenders = ROOTS.flatMap(testFilesUnder)
    .filter((path) => path !== SELF && PER_FILE_CLEANUP.test(withoutComments(readFileSync(path, "utf8"))))
    .map(label);
  expect(offenders).toEqual([]);
});
