import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import { collectedUnder, describeDifference, testFilesOnDisk } from "./package-test-files.mjs";

const root = path.resolve("/repo/cmd/evener-hub/frontend");
const dir = path.resolve("/repo/appwire-client/typescript");

test("collectedUnder keeps only the package's files and resolves them against the vitest root", () => {
  const output = ["src/App.test.tsx", "../../../appwire-client/typescript/errors.test.ts", "", "  scripts/x.test.mjs  "].join("\n");
  assert.deepEqual(collectedUnder(output, root, dir), [path.join(dir, "errors.test.ts")]);
});

test("collectedUnder does not match a sibling directory with the same prefix", () => {
  const output = "../../../appwire-client/typescript-notes/errors.test.ts";
  assert.deepEqual(collectedUnder(output, root, dir), []);
});

test("describeDifference is silent when the two agree", () => {
  const files = [path.join(dir, "a.test.ts"), path.join(dir, "b.test.ts")];
  assert.equal(describeDifference(files, files, dir), "");
});

test("describeDifference names each file vitest failed to collect", () => {
  const onDisk = [path.join(dir, "a.test.ts"), path.join(dir, "b.test.ts")];
  const message = describeDifference(onDisk, [path.join(dir, "a.test.ts")], dir);
  assert.match(message, /collects 1 of the package's 2 test files/);
  assert.match(message, /not collected: b\.test\.ts/);
});

test("describeDifference reports a collected file that is not on disk", () => {
  const message = describeDifference([path.join(dir, "a.test.ts")], [path.join(dir, "a.test.ts"), path.join(dir, "gone.test.ts")], dir);
  assert.match(message, /collected but not on disk: gone\.test\.ts/);
});

test("describeDifference fails an empty disk listing rather than passing on nothing", () => {
  assert.match(describeDifference([], [], dir), /measuring nothing/);
});

test("testFilesOnDisk finds the package's own test files and skips its sources", () => {
  const real = path.resolve(new URL("../../../../appwire-client/typescript", import.meta.url).pathname);
  const found = testFilesOnDisk(real);
  assert(found.length > 0);
  assert(found.every((file) => /\.(test|spec)\.[cm]?[jt]sx?$/.test(file)));
  assert(found.some((file) => file.endsWith(path.join("testing", "fakeClient.test.ts"))));
  assert(!found.some((file) => file.endsWith("reducer.ts")));
});
