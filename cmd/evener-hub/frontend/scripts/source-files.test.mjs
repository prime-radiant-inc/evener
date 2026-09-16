// Tests for ../../../../scripts/sdk/source-files.mjs, the shared file walk.
//
// It lives here for the reason the rewriter's test does: this is the runner
// with the node_modules these tools parse with. The walk swallows a missing
// directory (a fixture omits a tree) but must not swallow a permission error,
// which would read as an empty tree and pass a check that should fault.
import assert from "node:assert/strict";
import test from "node:test";
import { sourceFiles } from "../../../../scripts/sdk/source-files.mjs";

const fault = (code) => {
  const error = new Error(code);
  error.code = code;
  return error;
};

const throwing = (code) => () => {
  throw fault(code);
};

test("a missing directory yields an empty list", () => {
  assert.deepEqual(sourceFiles("/nowhere", { readdir: throwing("ENOENT") }), []);
  assert.deepEqual(sourceFiles("/afile", { readdir: throwing("ENOTDIR") }), []);
});

test("a permission error is rethrown, not read as an empty tree", () => {
  assert.throws(() => sourceFiles("/locked", { readdir: throwing("EACCES") }), /EACCES/);
});

test("it returns the source files a directory holds, skipping the rest", () => {
  const entry = (name, dir = false) => ({ name, isDirectory: () => dir, isFile: () => !dir });
  const tree = {
    "/pkg": [entry("index.ts"), entry("notes.md"), entry("sub", true), entry("node_modules", true)],
    "/pkg/sub": [entry("child.tsx"), entry("child.test.ts")],
  };
  const readdir = (dir) => tree[dir] ?? [];
  assert.deepEqual(sourceFiles("/pkg", { readdir }).sort(), ["/pkg/index.ts", "/pkg/sub/child.test.ts", "/pkg/sub/child.tsx"]);
  assert.deepEqual(sourceFiles("/pkg", { readdir, keep: (name) => !name.includes(".test.") }).sort(), [
    "/pkg/index.ts",
    "/pkg/sub/child.tsx",
  ]);
});
