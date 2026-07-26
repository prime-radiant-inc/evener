// @vitest-environment node

import { readFileSync } from "node:fs";
import { expect, test } from "vitest";

// tsconfig.json sets `incremental: true` with a `.tsbuildinfo` cache, which is
// right for an editor and for `typecheck:fast` but wrong for a gate. `tsc
// --noEmit` reads that cache, and a stale entry has already reported an error
// for a property that was not in the file - an immediate re-run, with nothing
// changed in between, exited 0 (kata af3e).
//
// A gate that reports an error a re-run erases is worse than a slow one: it
// teaches you to re-run instead of reading, and the next time it is a real
// break you will re-run past that too. Measured cost of correctness here: 0.64s
// warm vs 2.45s cold, against a gate whose test suite alone is ~50s.
//
// These assert the scripts the gate actually invokes, because the difference is
// invisible in the output - both print nothing on success.
// A bare relative path, resolved against vitest's root - which is this
// directory. The two obvious alternatives are both unavailable here: `new
// URL(..., import.meta.url)` throws under jsdom because import.meta.url is not
// a file: URL (and vitest reports an import-time throw as "no tests" rather
// than a failure, so it fails silently), and node:path/process are untyped in
// this tsconfig, which has no @types/node.
const scripts = (JSON.parse(readFileSync("package.json", "utf8")) as { scripts: Record<string, string> }).scripts;

test.each(["typecheck", "build"])("the %s script type-checks without the incremental cache", (name) => {
  const script = scripts[name];
  expect(script, `package.json has no "${name}" script`).toBeDefined();
  expect(script).toContain("tsc --noEmit");
  expect(
    script,
    `"${name}" must pass --incremental false: tsc --noEmit reads .tsbuildinfo and a stale entry makes this gate lie (kata af3e)`,
  ).toContain("--incremental false");
});

test("the warm-cache typecheck is still available, under a name that says it is the fast one", () => {
  expect(scripts["typecheck:fast"]).toBe("tsc --noEmit");
});
