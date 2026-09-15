import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  aliasesFrom,
  collectedUnder,
  describeAppImports,
  describeCwdRelativeReads,
  describeDifference,
  describeProofRun,
  firstAliasMatch,
  describeAliasOrder,
  describeUnaliasedImports,
  pickProofFile,
  reachableBareImports,
  resolvePackageImport,
  sourceFilesOnDisk,
  testFilesOnDisk,
} from "./package-test-files.mjs";

const root = path.resolve("/repo/cmd/evener-hub/frontend");
const dir = path.resolve("/repo/appwire-client/typescript");

test("collectedUnder keeps only the package's files and resolves them against the vitest root", () => {
  const output = [
    "src/App.test.tsx",
    "../../../appwire-client/typescript/errors.test.ts",
    "",
    "  scripts/x.test.mjs  ",
  ].join("\n");
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
  const message = describeDifference(
    [path.join(dir, "a.test.ts")],
    [path.join(dir, "a.test.ts"), path.join(dir, "gone.test.ts")],
    dir,
  );
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
  assert.equal(
    pickProofFile(files, (file) => sources[file]),
    files[1],
  );
});

test("pickProofFile returns nothing when no test imports vitest", () => {
  assert.equal(
    pickProofFile([path.join(dir, "a.test.ts")], () => "export const x = 1;\n"),
    "",
  );
});

test("describeProofRun is silent on a passing run of the named file", () => {
  const file = path.join(dir, "a.test.ts");
  const report = { numTotalTests: 3, numFailedTests: 0, testResults: [{ name: file }] };
  assert.equal(describeProofRun(report, file, dir), "");
});

test("describeProofRun rejects a report that never ran the file", () => {
  const file = path.join(dir, "a.test.ts");
  const report = { numTotalTests: 3, numFailedTests: 0, testResults: [{ name: path.join(dir, "other.test.ts") }] };
  assert.match(describeProofRun(report, file, dir), /was not executed/);
});

test("describeProofRun rejects a collected file that executed no tests", () => {
  const file = path.join(dir, "a.test.ts");
  assert.match(
    describeProofRun({ numTotalTests: 0, numFailedTests: 0, testResults: [{ name: file }] }, file, dir),
    /ran no tests/,
  );
});

test("describeProofRun reports failing tests", () => {
  const file = path.join(dir, "a.test.ts");
  assert.match(
    describeProofRun({ numTotalTests: 3, numFailedTests: 2, testResults: [{ name: file }] }, file, dir),
    /2 failing test/,
  );
});

test("describeProofRun matches a file the reporter named through a different prefix", () => {
  const file = path.join(dir, "a.test.ts");
  const viaSymlink = path.join(dir, "sub", "..", "a.test.ts");
  assert.equal(
    describeProofRun({ numTotalTests: 1, numFailedTests: 0, testResults: [{ name: viaSymlink }] }, file, dir),
    "",
  );
});

test("describeAppImports is silent when the package keeps to itself", () => {
  const files = [path.join(dir, "reducer.ts")];
  const sources = { [files[0]]: 'import { x } from "./model";\n' };
  assert.equal(
    describeAppImports(files, (file) => sources[file], dir),
    "",
  );
});

test("describeAppImports names every line that reaches into the app", () => {
  const files = [path.join(dir, "a.test.ts"), path.join(dir, "b.ts")];
  const sources = {
    [files[0]]: 'import Session from "../../cmd/evener-hub/frontend/src/panes/session/Session";\n',
    [files[1]]: 'import type { T } from "../../cmd/evener-hub/frontend/src/shell/palette/commands";\n',
  };
  const message = describeAppImports(files, (file) => sources[file], dir);
  assert.match(message, /must not import from the app/);
  assert.match(message, /a\.test\.ts: imports .*panes\/session\/Session/);
  assert.match(message, /b\.ts: imports .*shell\/palette\/commands/);
});

test("describeAppImports catches a side-effect import, a require and a dynamic import", () => {
  const files = [path.join(dir, "a.ts"), path.join(dir, "b.ts"), path.join(dir, "c.ts"), path.join(dir, "d.ts")];
  const sources = {
    [files[0]]: 'import "../../cmd/evener-hub/frontend/src/testSetup";\n',
    [files[1]]: 'const s = require("../../cmd/evener-hub/frontend/src/stores/threads");\n',
    [files[2]]: 'const m = await import("../../cmd/evener-hub/frontend/src/shell/clientContext");\n',
    [files[3]]: 'export { x } from "../../cmd/evener-hub/frontend/src/panes/session/Session";\n',
  };
  const message = describeAppImports(files, (file) => sources[file], dir);
  assert.match(message, /a\.ts: imports .*testSetup/);
  assert.match(message, /b\.ts: imports .*stores\/threads/);
  assert.match(message, /c\.ts: imports .*shell\/clientContext/);
  assert.match(message, /d\.ts: imports .*panes\/session\/Session/);
});

test("describeAppImports does not fire on a comment that merely names the app path", () => {
  const files = [path.join(dir, "a.ts")];
  const sources = {
    [files[0]]: [
      "// mirrors cmd/evener-hub/frontend/src/stores/threads.ts",
      '// the web side imports this as "cmd/evener-hub/frontend/src/x"',
      'const label = "cmd/evener-hub/frontend/src/panes/session";',
      "",
    ].join("\n"),
  };
  assert.equal(
    describeAppImports(files, (file) => sources[file], dir),
    "",
  );
});

test("sourceFilesOnDisk sees the package's non-test sources, not only its tests", () => {
  const real = fileURLToPath(new URL("../../../../appwire-client/typescript", import.meta.url));
  const found = sourceFilesOnDisk(real);
  assert(found.some((file) => file.endsWith("reducer.ts")));
  assert(found.some((file) => file.endsWith("reducer.test.ts")));
  assert(!found.some((file) => file.includes(`${path.sep}node_modules${path.sep}`)));
});

test("describeCwdRelativeReads names the working-directory read hubWireFixtures.ts used to carry", () => {
  const file = path.join(dir, "testing", "hubWireFixtures.ts");
  const source = [
    'import { readFileSync } from "node:fs";',
    'import { join } from "node:path";',
    'const FIXTURE_PATH = join("..", "testdata", "authwire", "responses.json");',
    "",
  ].join("\n");
  const message = describeCwdRelativeReads([file], () => source, dir);
  assert.match(message, /must not read a path resolved against the working directory/);
  assert.match(message, /hubWireFixtures\.ts:3: const FIXTURE_PATH/);
});

test("describeCwdRelativeReads accepts a read resolved against the module's own URL", () => {
  const file = path.join(dir, "a.ts");
  const source = [
    'import { readFileSync } from "node:fs";',
    'import { fileURLToPath } from "node:url";',
    'const p = fileURLToPath(new URL("../testdata/x.json", import.meta.url));',
    "",
  ].join("\n");
  assert.equal(
    describeCwdRelativeReads([file], () => source, dir),
    "",
  );
});

test("describeCwdRelativeReads ignores ordinary relative imports beside an absolute read", () => {
  const file = path.join(dir, "a.ts");
  const source = [
    'import { readFileSync } from "node:fs";',
    'import { join } from "node:path";',
    'import type { ThreadModel } from "../model";',
    'export * from "../testdata/shim";',
    'const legacy = require("../fixtures/old");',
    'const text = readFileSync("/absolute/path/x.json", "utf8");',
    "",
  ].join("\n");
  assert.equal(
    describeCwdRelativeReads([file], () => source, dir),
    "",
  );
});

test("describeCwdRelativeReads ignores a module that never touches the filesystem", () => {
  const file = path.join(dir, "a.ts");
  const source = 'const rel = "../testdata/x.json";\n';
  assert.equal(
    describeCwdRelativeReads([file], () => source, dir),
    "",
  );
});

test("describeCwdRelativeReads covers node:fs/promises too", () => {
  const file = path.join(dir, "a.ts");
  const source =
    'import { readFile } from "node:fs/promises";\nconst text = await readFile("../testdata/x.json", "utf8");\n';
  assert.match(
    describeCwdRelativeReads([file], () => source, dir),
    /a\.ts:2/,
  );
});

test("describeCwdRelativeReads ignores a path literal that no filesystem call reads", () => {
  const file = path.join(dir, "a.ts");
  const source = 'import { readFileSync } from "node:fs";\nconst label = "../testdata/x.json";\n';
  assert.equal(
    describeCwdRelativeReads([file], () => source, dir),
    "",
  );
});

test("describeCwdRelativeReads sees fs reached through require and dynamic import", () => {
  const file = path.join(dir, "a.ts");
  for (const header of [
    'const fs = require("fs");',
    'const fs = require("node:fs");',
    'const fs = await import("node:fs/promises");',
    'import fs from "fs";',
  ]) {
    const source = `${header}\nconst text = fs.readFileSync(join("..", "testdata", "x.json"), "utf8");\n`;
    assert.match(
      describeCwdRelativeReads([file], () => source, dir),
      /a\.ts:2/,
      header,
    );
  }
});

test("describeCwdRelativeReads also catches a ./-prefixed literal", () => {
  const file = path.join(dir, "a.ts");
  const source = 'import { readFileSync } from "node:fs";\nconst text = readFileSync("./responses.json", "utf8");\n';
  assert.match(
    describeCwdRelativeReads([file], () => source, dir),
    /a\.ts:2/,
  );
});

test("describeProofRun matches when one side reaches the file through a symlink", (t) => {
  const scratch = mkdtempSync(path.join(os.tmpdir(), "appwire-realpath-"));
  t.after(() => rmSync(scratch, { recursive: true, force: true }));
  const realDir = path.join(scratch, "real");
  mkdirSync(realDir);
  const realFile = path.join(realDir, "a.test.ts");
  writeFileSync(realFile, "// fixture\n");
  const linkDir = path.join(scratch, "link");
  symlinkSync(realDir, linkDir);
  const viaLink = path.join(linkDir, "a.test.ts");
  const report = { numTotalTests: 1, numFailedTests: 0, testResults: [{ name: viaLink }] };
  assert.equal(describeProofRun(report, realFile, realDir), "");
  assert.equal(describeProofRun({ ...report, testResults: [{ name: realFile }] }, viaLink, linkDir), "");
});

test("aliasesFrom reads every key of the resolve.alias block, and what it serves", () => {
  const config = [
    "export default defineConfig({",
    "  resolve: {",
    '    alias: {',
    '      "@evener/appwire-client/docContent": path.join(dir, "docContent.ts"),',
    '      react: path.join(__dirname, "node_modules", "react"),',
    "      // a comment between entries",
    '      typescript: path.join(__dirname, "node_modules", "typescript"),',
    "    },",
    "  },",
    "});",
  ].join("\n");
  const aliases = aliasesFrom(config);
  assert.deepEqual([...aliases.keys()].sort(), ["@evener/appwire-client/docContent", "react", "typescript"]);
  // A file target answers its own specifier and nothing below it; a directory
  // target answers both.
  assert.equal(aliases.get("@evener/appwire-client/docContent").servesSubpaths, false);
  assert.equal(aliases.get("react").servesSubpaths, true);
  assert.equal(aliases.get("typescript").servesSubpaths, true);
});

test("the package root does not stand in for a specifier below it", () => {
  // The root alias points at index.ts. Vite would build
  // index.ts/testing/fakeClient, which is not a path, so accepting the root
  // as a prefix match said an unresolvable import was aliased.
  const subpath = new Map([["@evener/appwire-client/testing/fakeClient", ["a.test.ts"]]]);
  assert.match(describeUnaliasedImports(subpath, new Map([["@evener/appwire-client", { servesSubpaths: false }]])), /does not alias/);
  assert.equal(describeUnaliasedImports(subpath, new Map([["@evener/appwire-client/testing", { servesSubpaths: true }]])), "");
  assert.equal(describeUnaliasedImports(subpath, new Map([["@evener/appwire-client/testing/fakeClient", { servesSubpaths: false }]])), "");
});

test("with the config's targets in hand, a directory alias serves subpaths and a file alias does not", () => {
  const subpath = new Map([["@evener/appwire-client/testing/fakeClient", ["a.test.ts"]]]);
  const asFile = new Map([["@evener/appwire-client", { servesSubpaths: false }]]);
  const asDirectory = new Map([["@evener/appwire-client", { servesSubpaths: true }]]);
  assert.match(describeUnaliasedImports(subpath, asFile), /does not alias/);
  assert.equal(describeUnaliasedImports(subpath, asDirectory), "");
});

test("reachableBareImports follows relative imports and stops at bare ones", () => {
  const files = {
    "/pkg/a.test.ts": 'import { helper } from "./helper";\nimport { expect } from "vitest";\n',
    "/pkg/helper.ts": 'import ts from "typescript";\nimport { readFileSync } from "node:fs";\n',
    // Not reachable from any test: the runner's own tooling.
    "/pkg/scripts/qualify.mjs": 'import { WebSocketServer } from "ws";\n',
  };
  const bare = reachableBareImports(
    ["/pkg/a.test.ts"],
    (file) => files[file],
    (from, specifier) => (specifier === "./helper" ? "/pkg/helper.ts" : null),
    "/pkg",
  );
  assert.deepEqual([...bare.keys()].sort(), ["typescript", "vitest"]);
  assert.deepEqual(bare.get("typescript"), ["helper.ts"]);
});

test("describeUnaliasedImports excuses vitest and anything the config aliases", () => {
  const bare = new Map([
    ["vitest", ["a.test.ts"]],
    ["react", ["b.test.tsx"]],
    ["@testing-library/react", ["b.test.tsx"]],
  ]);
  assert.equal(describeUnaliasedImports(bare, new Map([["react", { servesSubpaths: true }], ["@testing-library/react", { servesSubpaths: true }]])), "");
});

test("describeUnaliasedImports names the specifier and the file that imports it", () => {
  const bare = new Map([["typescript", ["scripts/consumer-value-imports.mjs"]]]);
  const problem = describeUnaliasedImports(bare, new Map([["react", { servesSubpaths: true }]]));
  assert.match(problem, /typescript, imported by scripts\/consumer-value-imports\.mjs/);
  assert.match(problem, /CI's web job/);
});

test("describeUnaliasedImports matches a subpath against a package alias that serves one", () => {
  const bare = new Map([["@testing-library/react/pure", ["b.test.tsx"]]]);
  assert.equal(describeUnaliasedImports(bare, new Map([["@testing-library/react", { servesSubpaths: true }]])), "");
});

test("resolvePackageImport answers a directory import with its index, not the directory", () => {
  const root = mkdtempSync(path.join(os.tmpdir(), "evener-package-imports-"));
  try {
    mkdirSync(path.join(root, "helpers"));
    writeFileSync(path.join(root, "helpers", "index.ts"), 'import ts from "typescript";\n');
    writeFileSync(path.join(root, "a.test.ts"), 'import { helper } from "./helpers";\n');
    assert.equal(resolvePackageImport(path.join(root, "a.test.ts"), "./helpers"), path.join(root, "helpers", "index.ts"));

    // The crash this guards: the walk reads whatever the resolver hands back.
    const bare = reachableBareImports(
      [path.join(root, "a.test.ts")],
      (file) => readFileSync(file, "utf8"),
      resolvePackageImport,
      root,
    );
    assert.deepEqual([...bare.keys()], ["typescript"]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("resolvePackageImport probes index for every extension it was asked about", () => {
  const root = mkdtempSync(path.join(os.tmpdir(), "evener-package-index-"));
  try {
    mkdirSync(path.join(root, "tools"));
    writeFileSync(path.join(root, "tools", "index.mjs"), "export const x = 1;\n");
    assert.equal(resolvePackageImport(path.join(root, "a.test.ts"), "./tools"), path.join(root, "tools", "index.mjs"));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a subpath alias satisfies its own specifier without the root being aliased too", () => {
  // Reading only the first two segments looked for "@evener/appwire-client",
  // so a tree aliasing just the testing subpath reported a false offender.
  const bare = new Map([["@evener/appwire-client/testing/fakeClient", ["a.test.ts"]]]);
  assert.equal(describeUnaliasedImports(bare, new Map([["@evener/appwire-client/testing", { servesSubpaths: true }]])), "");
  assert.match(describeUnaliasedImports(bare, new Map([["@evener/appwire-client/other", { servesSubpaths: true }]])), /does not alias/);
});

test("an erased import does not demand an alias", () => {
  // `import type X from "pkg"` is gone before Vite resolves anything, so a
  // package test that only names a dependency that way needs no alias entry.
  const files = {
    "/pkg/a.test.ts": 'import type { Program } from "typescript";\nexport type P = Program;\n',
  };
  const bare = reachableBareImports(["/pkg/a.test.ts"], (file) => files[file], () => null, "/pkg");
  assert.deepEqual([...bare.keys()], []);
});

test("an import that mixes a value member with a type one still demands its alias", () => {
  const files = {
    "/pkg/a.test.ts": 'import ts, { type Program } from "typescript";\nvoid ts;\nexport type P = Program;\n',
  };
  const bare = reachableBareImports(["/pkg/a.test.ts"], (file) => files[file], () => null, "/pkg");
  assert.deepEqual([...bare.keys()], ["typescript"]);
});

test("the walk does not follow an erased relative import either", () => {
  const files = {
    "/pkg/a.test.ts": 'import type { Helper } from "./helper";\nexport type H = Helper;\n',
    "/pkg/helper.ts": 'import ts from "typescript";\nvoid ts;\n',
  };
  const bare = reachableBareImports(
    ["/pkg/a.test.ts"],
    (file) => files[file],
    (_from, specifier) => (specifier === "./helper" ? "/pkg/helper.ts" : null),
    "/pkg",
  );
  assert.deepEqual([...bare.keys()], []);
});

test("the alias Vite uses is the first that matches, in config order", () => {
  const ordered = new Map([
    ["@evener/appwire-client/docContent", { servesSubpaths: false }],
    ["@evener/appwire-client/testing", { servesSubpaths: true }],
    ["@evener/appwire-client", { servesSubpaths: false }],
  ]);
  assert.equal(firstAliasMatch("@evener/appwire-client/docContent", ordered), "@evener/appwire-client/docContent");
  assert.equal(firstAliasMatch("@evener/appwire-client/testing/fakeClient", ordered), "@evener/appwire-client/testing");
  assert.equal(firstAliasMatch("@evener/appwire-client", ordered), "@evener/appwire-client");
});

test("a general key placed before a specific one is refused by name", () => {
  const reordered = new Map([
    ["@evener/appwire-client", { servesSubpaths: true }],
    ["@evener/appwire-client/testing", { servesSubpaths: true }],
  ]);
  const problem = describeAliasOrder(reordered);
  assert.match(problem, /@evener\/appwire-client precedes @evener\/appwire-client\/testing, which it swallows/);
  assert.match(problem, /takes the FIRST matching alias/);
  // And with that root entry serving subpaths, first-match really does answer
  // the testing specifier with the root -- which is the harm the order rule
  // exists to prevent.
  assert.equal(firstAliasMatch("@evener/appwire-client/testing/fakeClient", reordered), "@evener/appwire-client");
});

test("the real config is ordered specific before general", () => {
  const config = readFileSync(
    path.resolve(fileURLToPath(import.meta.url), "../../vite.config.ts"),
    "utf8",
  );
  assert.equal(describeAliasOrder(aliasesFrom(config)), "");
});
