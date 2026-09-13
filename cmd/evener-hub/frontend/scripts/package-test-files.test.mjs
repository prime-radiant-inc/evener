import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  collectedUnder,
  describeDifference,
  describeProofRun,
  pickProofFile,
  testFilesOnDisk,
} from "./package-test-files.mjs";

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
  // fileURLToPath, not URL.pathname: pathname is percent-encoded, so a
  // checkout path containing a space names a directory that is not there.
  const real = fileURLToPath(new URL("../../../../appwire-client/typescript", import.meta.url));
  const found = testFilesOnDisk(real);
  assert(found.length > 0);
  assert(found.every((file) => /\.(test|spec)\.[cm]?[jt]sx?$/.test(file)));
  assert(found.some((file) => file.endsWith(path.join("testing", "fakeClient.test.ts"))));
  assert(!found.some((file) => file.endsWith("reducer.ts")));
});

test("pickProofFile takes the first test that imports vitest", () => {
  const files = [path.join(dir, "a.test.ts"), path.join(dir, "b.test.ts"), path.join(dir, "c.test.ts")];
  const sources = {
    [files[0]]: 'import { thing } from "./thing";\n',
    [files[1]]: 'import { expect, test } from "vitest";\n',
    [files[2]]: 'import { test } from "vitest";\n',
  };
  assert.equal(pickProofFile(files, (file) => sources[file]), files[1]);
});

test("pickProofFile returns nothing when no test imports vitest", () => {
  assert.equal(pickProofFile([path.join(dir, "a.test.ts")], () => "export const x = 1;\n"), "");
});

test("describeProofRun is silent on a passing run of the named file", () => {
  const file = path.join(dir, "a.test.ts");
  const report = { numTotalTests: 3, numFailedTests: 0, testResults: [{ name: file }] };
  assert.equal(describeProofRun(report, file), "");
});

test("describeProofRun rejects a report that never ran the file", () => {
  const file = path.join(dir, "a.test.ts");
  const report = { numTotalTests: 3, numFailedTests: 0, testResults: [{ name: path.join(dir, "other.test.ts") }] };
  assert.match(describeProofRun(report, file), /was not executed/);
});

test("describeProofRun rejects a collected file that executed no tests", () => {
  const file = path.join(dir, "a.test.ts");
  assert.match(describeProofRun({ numTotalTests: 0, numFailedTests: 0, testResults: [{ name: file }] }, file), /ran no tests/);
});

test("describeProofRun reports failing tests", () => {
  const file = path.join(dir, "a.test.ts");
  assert.match(describeProofRun({ numTotalTests: 3, numFailedTests: 2, testResults: [{ name: file }] }, file), /2 failing test/);
});
