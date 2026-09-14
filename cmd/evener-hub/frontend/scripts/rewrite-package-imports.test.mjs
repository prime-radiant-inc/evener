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

// A minimal repo-shaped tree: the package with one root export and one
// docContent export, plus whatever consumer files the case needs.
function fixture(files) {
  const root = mkdtempSync(path.join(os.tmpdir(), "evener-rewrite-imports-"));
  const written = {
    "appwire-client/typescript/index.ts":
      'export { errorText } from "./errors";\nexport { alpha, beta, Thing } from "./model";\n',
    "appwire-client/typescript/errors.ts": "export function errorText() {\n  return '';\n}\nexport function internalOnly() {}\n",
    // Thing is a class so one import can take it as a type and another as a
    // value, which is how the same local name arrives spelled two ways.
    "appwire-client/typescript/model.ts": "export class Thing {}\nexport function alpha() {}\nexport function beta() {}\n",
    "appwire-client/typescript/docContent.ts": "export function docImageURL() {\n  return '';\n}\n",
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

test("mobile-native/scripts keeps its relative imports", () => {
  const carved = 'import { errorText } from "../../appwire-client/typescript/errors";\nerrorText();\n';
  const root = fixture({ "mobile-native/scripts/check-hub.mts": carved });
  try {
    assert.equal(rewrite(root).status, 0);
    assert.equal(readFileSync(path.join(root, "mobile-native/scripts/check-hub.mts"), "utf8"), carved);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("--check reports what is left and writes nothing", () => {
  const before = 'import { errorText } from "../../appwire-client/typescript/errors";\nerrorText();\n';
  const root = fixture({ "mobile/src/state.ts": before });
  try {
    let status = 0;
    let output = "";
    try {
      output = execFileSync(process.execPath, [script, "--root", root, "--check"], { encoding: "utf8" });
    } catch (error) {
      status = error.status;
      output = `${error.stdout ?? ""}${error.stderr ?? ""}`;
    }
    assert.equal(status, 1);
    assert.match(output, /still name a path/);
    assert.equal(readFileSync(path.join(root, "mobile/src/state.ts"), "utf8"), before);
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
