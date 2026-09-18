// Tests for ../../../../scripts/sdk/rewrite-package-imports.mjs, the SDK
// migration's import rewriter.
//
// It lives here, not beside the script, because it is the only test runner in
// the repo that has what the script needs: the rewriter parses with TypeScript
// out of this app's node_modules, and the root Go suite's CI lane installs no
// dependency tree at all. `npm test` here already runs `node --test
// scripts/*.test.mjs`, so this file needs no new gate wiring.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = path.resolve(fileURLToPath(import.meta.url), "../../../../../scripts/sdk/rewrite-package-imports.mjs");

// A minimal repo-shaped tree: the package with one root export, one
// docContent export and two state subpaths (one in the object form, one as a
// string shorthand naming its runtime target), plus whatever consumer files the
// case needs. The exports map is what the rewriter reads its subpaths from, so
// the fixture carries one exactly as the real package does.
function fixture(files) {
  const root = mkdtempSync(path.join(os.tmpdir(), "evener-rewrite-imports-"));
  const written = {
    "appwire-client/typescript/package.json": JSON.stringify({
      name: "@evener/appwire-client",
      exports: {
        ".": { types: "./dist/index.d.ts" },
        "./docContent": { types: "./dist/docContent.d.ts" },
        "./state/navigation": { types: "./dist/state/navigation/index.d.ts" },
        "./state/mutation": "./dist/state/mutation/index.js",
      },
    }),
    "appwire-client/typescript/index.ts":
      'export { errorText } from "./errors";\nexport { alpha, beta, Thing } from "./model";\n',
    "appwire-client/typescript/errors.ts":
      "export function errorText() {\n  return '';\n}\nexport function internalOnly() {}\n",
    // Thing is a class so one import can take it as a type and another as a
    // value, which is how the same local name arrives spelled two ways.
    "appwire-client/typescript/model.ts":
      "export class Thing {}\nexport function alpha() {}\nexport function beta() {}\n",
    "appwire-client/typescript/docContent.ts": "export function docImageURL() {\n  return '';\n}\n",
    "appwire-client/typescript/state/navigation/index.ts": "export function parseRoute() {\n  return '';\n}\n",
    "appwire-client/typescript/state/mutation/index.ts": "export function enqueue() {\n  return '';\n}\n",
    "appwire-client/typescript/testing/fakeClient.ts": "export class FakeClient {}\n",
    ...files,
  };
  for (const [relative, contents] of Object.entries(written)) {
    const full = path.join(root, relative);
    mkdirSync(path.dirname(full), { recursive: true });
    writeFileSync(full, contents);
  }
  return root;
}

function rewrite(root) {
  try {
    return { status: 0, output: execFileSync(process.execPath, [script, "--root", root], { encoding: "utf8" }) };
  } catch (error) {
    return { status: error.status, output: `${error.stdout ?? ""}${error.stderr ?? ""}` };
  }
}

test("rewrites a relative import of the package onto the package name", () => {
  const root = fixture({
    "mobile/src/state.ts": 'import { errorText } from "../../appwire-client/typescript/errors";\nerrorText();\n',
  });
  try {
    assert.equal(rewrite(root).status, 0);
    assert.match(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), /from "@evener\/appwire-client"/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a symbol the package does not publish leaves every file untouched", () => {
  // The refusal is in the LAST tree the sweep walks, behind a file in the
  // first that rewrites cleanly. Writing as it went left that first file
  // migrated and the run reporting "nothing was written".
  const clean = 'import { errorText } from "../../../../appwire-client/typescript/errors";\nerrorText();\n';
  const refused = 'import { internalOnly } from "../../appwire-client/typescript/errors";\ninternalOnly();\n';
  const root = fixture({
    "cmd/evener-hub/frontend/src/app.ts": clean,
    "mobile/src/state.ts": refused,
  });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 2);
    assert.match(run.output, /internalOnly/);
    assert.match(run.output, /nothing was written/);
    // This one resolves and would have been rewritten; it is the file a
    // write-as-you-go sweep left migrated behind the refusal.
    assert.equal(readFileSync(path.join(root, "cmd/evener-hub/frontend/src/app.ts"), "utf8"), clean);
    assert.equal(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), refused);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("the mobile-native/scripts tools are rewritten like every other consumer", () => {
  // They were carved out while the premise held that tsx reads no tsconfig
  // paths. It reads mobile-native/tsconfig.json, which simply had none.
  const root = fixture({
    "mobile-native/scripts/check-hub.mts":
      'import { errorText } from "../../appwire-client/typescript/errors";\nerrorText();\n',
  });
  try {
    assert.equal(rewrite(root).status, 0);
    assert.match(
      readFileSync(path.join(root, "mobile-native/scripts/check-hub.mts"), "utf8"),
      /from "@evener\/appwire-client"/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});


test("an inline type member and a value member of the same export merge to the value form", () => {
  // Two statements that both land on the package root, naming one export two
  // ways. Keyed by rendered text these survived as `{ type Thing, Thing }` --
  // a duplicate identifier the tool itself introduced.
  const root = fixture({
    "mobile/src/state.ts": [
      'import { type Thing } from "../../appwire-client/typescript/model";',
      'import { Thing } from "../../appwire-client/typescript/model";',
      "export const made: Thing = new Thing();",
      "",
    ].join("\n"),
  });
  try {
    assert.equal(rewrite(root).status, 0);
    const merged = readFileSync(path.join(root, "mobile/src/state.ts"), "utf8");
    assert.match(merged, /^import \{ Thing \} from "@evener\/appwire-client";$/m);
    assert.doesNotMatch(merged, /type Thing/);
    assert.equal(merged.match(/@evener\/appwire-client/g).length, 1);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("two exports behind one local alias are refused, not merged", () => {
  const before = [
    'import { alpha as shared } from "../../appwire-client/typescript/model";',
    'import { beta as shared } from "../../appwire-client/typescript/model";',
    "shared();",
    "",
  ].join("\n");
  const root = fixture({ "mobile/src/state.ts": before });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 2);
    assert.match(run.output, /both imported as shared/);
    assert.match(run.output, /nothing was written/);
    assert.equal(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), before);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a backtick specifier is rewritten like a quoted one", () => {
  // A specifier with nothing to interpolate is a different node kind carrying
  // the same string, and a reader that only knows string literals skips it in
  // silence -- leaving a path import behind the rewrite it was meant to make.
  const root = fixture({
    "mobile/src/state.ts": "const doc = await import(`../../appwire-client/typescript/docContent`);\nvoid doc;\n",
  });
  try {
    assert.equal(rewrite(root).status, 0);
    assert.match(
      readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"),
      /import\("@evener\/appwire-client\/docContent"\)/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a namespace import of a module the package does not publish is refused", () => {
  const before = 'import * as errors from "../../appwire-client/typescript/errors";\nvoid errors;\n';
  const root = fixture({ "mobile/src/state.ts": before });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 2);
    assert.match(run.output, /import-namespace of "errors"/);
    assert.equal(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), before);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a namespace import of a published subpath is rewritten", () => {
  const root = fixture({
    "mobile/src/state.ts": 'import * as doc from "../../appwire-client/typescript/docContent";\nvoid doc;\n',
  });
  try {
    assert.equal(rewrite(root).status, 0);
    assert.match(
      readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"),
      /from "@evener\/appwire-client\/docContent"/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a state subpath added to the exports map is rewritten without a code edit", () => {
  // A symbol only a nested subpath publishes maps onto that subpath, since the
  // subpaths are the package's exports map rather than a list in the rewriter.
  const root = fixture({
    "mobile/src/route.ts":
      'import { parseRoute } from "../../appwire-client/typescript/state/navigation";\nparseRoute();\n',
  });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 0, run.output);
    assert.match(
      readFileSync(path.join(root, "mobile/src/route.ts"), "utf8"),
      /from "@evener\/appwire-client\/state\/navigation"/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a whole-module import of a published state subpath is rewritten to that subpath", () => {
  const root = fixture({
    "mobile/src/route.ts": 'import * as nav from "../../appwire-client/typescript/state/navigation";\nvoid nav;\n',
  });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 0, run.output);
    assert.match(
      readFileSync(path.join(root, "mobile/src/route.ts"), "utf8"),
      /from "@evener\/appwire-client\/state\/navigation"/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a subpath the exports map gives as a string shorthand is rewritten too", () => {
  // The shorthand names the runtime target (`./dist/state/mutation/index.js`),
  // not a `.d.ts`, so normalizing it as a declaration path left the module id
  // as "state/mutation/index.js", the subpath read as unpublished, and the run
  // refused. Both forms have to yield the same module id.
  const root = fixture({
    "mobile/src/outbox.ts":
      'import { enqueue } from "../../appwire-client/typescript/state/mutation";\nenqueue();\n',
  });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 0, run.output);
    assert.match(
      readFileSync(path.join(root, "mobile/src/outbox.ts"), "utf8"),
      /from "@evener\/appwire-client\/state\/mutation"/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("an import-equals require of the package is refused like any other whole-module site", () => {
  const before = 'import errors = require("../../appwire-client/typescript/errors");\nvoid errors;\n';
  const root = fixture({ "mobile/src/state.ts": before });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 2);
    assert.match(run.output, /require-equals of "errors"/);
    assert.equal(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), before);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a whole-module site naming the package root is rewritten to the package name", () => {
  // Both spellings resolve to index.ts, and the root is published: refusing
  // them said otherwise.
  for (const specifier of ["../../appwire-client/typescript", "../../appwire-client/typescript/index"]) {
    const root = fixture({ "mobile/src/state.ts": `import * as pkg from "${specifier}";\nvoid pkg;\n` });
    try {
      const run = rewrite(root);
      assert.equal(run.status, 0, `${specifier}: ${run.output}`);
      assert.match(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), /from "@evener\/appwire-client";/);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  }
});

test("a whole-module site naming a module the package does not publish is still refused", () => {
  const before = 'import * as errors from "../../appwire-client/typescript/errors";\nvoid errors;\n';
  const root = fixture({ "mobile/src/state.ts": before });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 2);
    assert.match(run.output, /import-namespace of "errors"/);
    assert.equal(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), before);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("an empty named import binds nothing, so it is refused", () => {
  const before = 'import {} from "../../appwire-client/typescript/errors";\n';
  const root = fixture({ "mobile/src/state.ts": before });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 2);
    assert.match(run.output, /empty named import of "errors"/);
    assert.equal(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), before);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("export * as ns publishes the namespace, not the module's members", () => {
  // Recursing into it said the root publishes alpha, beta and Thing, none of
  // which a consumer can import from the root -- only `ns` can.
  const index = 'export { errorText } from "./errors";\nexport * as ns from "./model";\n';

  const named = fixture({
    "appwire-client/typescript/index.ts": index,
    "mobile/src/state.ts": 'import { alpha } from "../../appwire-client/typescript/model";\nalpha();\n',
  });
  try {
    const run = rewrite(named);
    assert.equal(run.status, 2);
    assert.match(run.output, /exports alpha, which @evener\/appwire-client does not publish/);
  } finally {
    rmSync(named, { recursive: true, force: true });
  }

  const namespace = fixture({
    "appwire-client/typescript/index.ts": index,
    "mobile/src/state.ts": 'import { ns } from "../../appwire-client/typescript/index";\nvoid ns;\n',
  });
  try {
    assert.equal(rewrite(namespace).status, 0);
    assert.match(
      readFileSync(path.join(namespace, "mobile/src/state.ts"), "utf8"),
      /import \{ ns \} from "@evener\/appwire-client";/,
    );
  } finally {
    rmSync(namespace, { recursive: true, force: true });
  }
});

test("a side-effect import of a published module is migrated, not refused", () => {
  const root = fixture({
    "mobile/src/state.ts": [
      'import "../../appwire-client/typescript/index";',
      'import "../../appwire-client/typescript/docContent";',
      "",
    ].join("\n"),
  });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 0, run.output);
    const after = readFileSync(path.join(root, "mobile/src/state.ts"), "utf8");
    assert.match(after, /^import "@evener\/appwire-client";$/m);
    assert.match(after, /^import "@evener\/appwire-client\/docContent";$/m);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a side-effect import of a module the package does not publish is still refused", () => {
  const root = fixture({ "mobile/src/state.ts": 'import "../../appwire-client/typescript/errors";\n' });
  try {
    const run = rewrite(root);
    assert.equal(run.status, 2);
    assert.match(run.output, /import-side-effect of "errors"/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a comment above a removed duplicate goes with it", () => {
  const root = fixture({
    "mobile/src/state.ts": [
      'import { errorText } from "../../appwire-client/typescript/errors";',
      "// about the second import, which is about to be merged away",
      'import { alpha } from "../../appwire-client/typescript/model";',
      "// about the code below, which stays",
      "export const used = [errorText, alpha];",
      "",
    ].join("\n"),
  });
  try {
    assert.equal(rewrite(root).status, 0);
    const after = readFileSync(path.join(root, "mobile/src/state.ts"), "utf8");
    assert.doesNotMatch(after, /about the second import/);
    assert.match(after, /\/\/ about the code below, which stays\nexport const used/);
    assert.equal(after.match(/@evener\/appwire-client/g).length, 1);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a trailing comment on the statement above a removed duplicate survives", () => {
  const root = fixture({
    "mobile/src/state.ts": [
      'import { errorText } from "../../appwire-client/typescript/errors"; // keep me',
      'import { alpha } from "../../appwire-client/typescript/model";',
      "export const used = [errorText, alpha];",
      "",
    ].join("\n"),
  });
  try {
    assert.equal(rewrite(root).status, 0);
    const after = readFileSync(path.join(root, "mobile/src/state.ts"), "utf8");
    assert.match(after, /\/\/ keep me/);
    assert.equal(after.match(/@evener\/appwire-client/g).length, 1);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

// The bundlers resolve JavaScript beside TypeScript, so a .js consumer reaches
// the package by the same relative path a .ts one does. The sweep walked only
// the TypeScript extensions, which left such a file behind on every run.
test("a JavaScript consumer is swept like a TypeScript one", () => {
  const root = fixture({
    "mobile-native/src/legacyScreen.js":
      'import { errorText } from "../../appwire-client/typescript/errors";\nerrorText();\n',
  });
  try {
    assert.equal(rewrite(root).status, 0);
    assert.match(
      readFileSync(path.join(root, "mobile-native/src/legacyScreen.js"), "utf8"),
      /from "@evener\/appwire-client"/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a testing import is validated against the testing module's exports, like the root", () => {
  const root = fixture({
    "mobile/src/state.ts":
      'import { FakeClient } from "../../appwire-client/typescript/testing/fakeClient";\nnew FakeClient();\n',
  });
  try {
    assert.equal(rewrite(root).status, 0);
    assert.match(
      readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"),
      /from "@evener\/appwire-client\/testing\/fakeClient"/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a symbol the testing module does not publish is reported, not rewritten", () => {
  const root = fixture({
    "mobile/src/state.ts":
      'import { FakeClientz } from "../../appwire-client/typescript/testing/fakeClient";\nnew FakeClientz();\n',
  });
  try {
    const result = rewrite(root);
    assert.equal(result.status, 2);
    assert.match(result.output, /FakeClientz/);
    assert.match(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), /typescript\/testing\/fakeClient/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
