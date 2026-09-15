// packageValuesIn reads two different things with one reader: what the repo's
// consumers take from this package, and what resolve-check.mjs says they take.
// Both readings decide whether `make test-api-package` passes, so the shapes a
// hand-written regex used to miss are the ones worth pinning.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "vitest";
import { consumerPackageUsage, describeResolveCheckDrift, packageValuesIn, parse } from "./consumer-value-imports.mjs";

const ROOT = "@evener/appwire-client";
const DOC_CONTENT = "@evener/appwire-client/docContent";

function valuesIn(source) {
  return packageValuesIn(parse("fixture.ts", source), "fixture.ts", []);
}

function problemsFor(source) {
  const problems = [];
  packageValuesIn(parse("fixture.ts", source), "fixture.ts", problems);
  return problems;
}

test("collects every statement naming one specifier, not just the first", () => {
  const found = valuesIn(
    [
      'import { errorText } from "@evener/appwire-client";',
      'import { formatElapsed, splitMandate } from "@evener/appwire-client";',
      "",
    ].join("\n"),
  );
  expect([...found.get(ROOT)].sort()).toEqual(["errorText", "formatElapsed", "splitMandate"]);
});

test("reads either quote style", () => {
  const found = valuesIn("import { errorText } from '@evener/appwire-client';\n");
  expect([...found.get(ROOT)]).toEqual(["errorText"]);
});

test("keeps the value half when a consumer splits its type imports off", () => {
  const found = valuesIn(
    [
      'import type { ThreadModel } from "@evener/appwire-client";',
      'import { hydrateThread, type TurnModel } from "@evener/appwire-client";',
      "",
    ].join("\n"),
  );
  expect([...found.get(ROOT)]).toEqual(["hydrateThread"]);
});

test("a re-export is a consumer taking a value", () => {
  const found = valuesIn(
    [
      'export { graftContinuationTree } from "@evener/appwire-client";',
      'export type { ActivityTree } from "@evener/appwire-client";',
      'export * from "@evener/appwire-client";',
      "",
    ].join("\n"),
  );
  expect([...found.get(ROOT)]).toEqual(["graftContinuationTree"]);
});

test("an alias reports the name the package exports, not the local one", () => {
  const found = valuesIn('import { docImageURL as atSubpath } from "@evener/appwire-client/docContent";\n');
  expect([...found.get(DOC_CONTENT)]).toEqual(["docImageURL"]);
});

test("specifiers this package does not publish are not collected", () => {
  const found = valuesIn(
    [
      'import { FakeClient } from "@evener/appwire-client/testing/fakeClient";',
      'import { readFileSync } from "node:fs";',
      "",
    ].join("\n"),
  );
  expect([...found.get(ROOT)]).toEqual([]);
  expect([...found.get(DOC_CONTENT)]).toEqual([]);
});

test.each([
  ["import-namespace", `import * as everything from "${ROOT}";\nvoid everything;\n`],
  ["import-default", `import theDefault from "${ROOT}";\nvoid theDefault;\n`],
  ["import-side-effect", `import "${ROOT}";\n`],
  ["export-star-from", `export * from "${ROOT}";\n`],
  ["dynamic-import", `const loaded = await import("${ROOT}");\nvoid loaded;\n`],
  ["require", `const loaded = require("${ROOT}");\nvoid loaded;\n`],
  ["mock-call", `vi.mock("${ROOT}", () => ({}));\n`],
])("%s of the package is reported, not skipped", (kind, source) => {
  // This derivation says what the tarball must provide. It cannot answer that
  // for a module taken whole, and answering nothing looked identical to a file
  // that imports nothing.
  expect(problemsFor(source)).toEqual([`fixture.ts: ${kind} of ${ROOT} names no binding this check can account for`]);
  expect([...valuesIn(source).get(ROOT)]).toEqual([]);
});

test("the two forms that name what they take are still accepted", () => {
  expect(problemsFor(`import { errorText } from "${ROOT}";\nvoid errorText;\n`)).toEqual([]);
  expect(problemsFor(`export { errorText } from "${ROOT}";\n`)).toEqual([]);
  expect(problemsFor(`import type { ThreadModel } from "${ROOT}";\nexport type A = ThreadModel;\n`)).toEqual([]);
});

test("a namespace re-export of the package is accounted for, not refused", () => {
  // `export * as ns from "pkg"` takes the module, not a value out of it: there
  // is nothing to add to the list and nothing this derivation cannot answer.
  // Refusing it failed a re-export that resolves against the tarball perfectly.
  expect(problemsFor(`export * as everything from "${ROOT}";\n`)).toEqual([]);
  expect([...valuesIn(`export * as everything from "${ROOT}";\n`).get(ROOT)]).toEqual([]);
  // A bare `export *` still is refused: it names nothing at all.
  expect(problemsFor(`export * from "${ROOT}";\n`)).toEqual([
    `fixture.ts: export-star-from of ${ROOT} names no binding this check can account for`,
  ]);
});

// A consumer tree, as consumerPackageUsage walks one.
function consumerTree(files) {
  const root = mkdtempSync(path.join(os.tmpdir(), "evener-consumer-usage-"));
  for (const [relative, contents] of Object.entries(files)) {
    const full = path.join(root, relative);
    mkdirSync(path.dirname(full), { recursive: true });
    writeFileSync(full, contents);
  }
  for (const tree of ["mobile-native", "mobile/src", "cmd/evener-hub/frontend/src"]) {
    mkdirSync(path.join(root, tree), { recursive: true });
  }
  return root;
}

test("a consumer that only re-exports the module names no value but does use it", () => {
  const root = consumerTree({
    "mobile/src/state.ts": `export * as everything from "${ROOT}";\n`,
  });
  try {
    const usage = consumerPackageUsage(root);
    expect(usage.get(ROOT)).toEqual({ values: [], used: true });
    expect(usage.get(DOC_CONTENT)).toEqual({ values: [], used: false });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a value a JavaScript consumer imports is collected like a TypeScript one's", () => {
  // Metro and Vite resolve .js beside .ts, so a value only a .js file imports
  // is still one the tarball has to provide. Sweeping .ts/.tsx/.mts alone left
  // it out of the derivation, and resolve-check.mjs would not be held to it.
  const root = consumerTree({
    "mobile-native/src/legacyScreen.js": `import { errorText } from "${ROOT}";\nerrorText();\n`,
  });
  try {
    expect(consumerPackageUsage(root).get(ROOT)).toEqual({ values: ["errorText"], used: true });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a consumer whose only use is type-only names no value but does use it", () => {
  const root = consumerTree({
    "mobile/src/state.ts": `import type { ThreadModel } from "${ROOT}";\nexport type A = ThreadModel;\n`,
  });
  try {
    expect(consumerPackageUsage(root).get(ROOT)).toEqual({ values: [], used: true });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a specifier no consumer takes a value from qualifies as long as the fixture loads it", () => {
  const usage = new Map([
    [ROOT, { values: ["errorText"], used: true }],
    [DOC_CONTENT, { values: [], used: true }],
  ]);
  const loaded = parse(
    "resolve-check.mjs",
    `import { errorText } from "${ROOT}";\nimport * as doc from "${DOC_CONTENT}";\nvoid errorText;\nvoid doc;\n`,
  );
  expect(describeResolveCheckDrift(loaded, "resolve-check.mjs", usage)).toEqual("");

  // ... but it has to load it somehow, or running the fixture proves nothing.
  const missing = parse("resolve-check.mjs", `import { errorText } from "${ROOT}";\nvoid errorText;\n`);
  expect(describeResolveCheckDrift(missing, "resolve-check.mjs", usage)).toMatch(
    /does not load .*docContent at runtime/,
  );
});

test("a specifier consumers DO name values from still has to name every one", () => {
  const usage = new Map([
    [ROOT, { values: ["errorText", "formatElapsed"], used: true }],
    [DOC_CONTENT, { values: [], used: true }],
  ]);
  const short = parse(
    "resolve-check.mjs",
    `import { errorText } from "${ROOT}";\nimport * as doc from "${DOC_CONTENT}";\nvoid errorText;\nvoid doc;\n`,
  );
  expect(describeResolveCheckDrift(short, "resolve-check.mjs", usage)).toMatch(/have drifted/);

  const extra = parse(
    "resolve-check.mjs",
    `import { errorText } from "${ROOT}";\nimport { readDocFile } from "${DOC_CONTENT}";\nvoid errorText;\nvoid readDocFile;\n`,
  );
  expect(describeResolveCheckDrift(extra, "resolve-check.mjs", usage)).toMatch(/no consumer takes a value from/);
});

test("an inline-type import proves no resolution", () => {
  // `import { type X } from "pkg"` is erased exactly as `import type` is, and
  // the statement-level flag does not say so -- a fixture resting on one
  // would claim a resolution that never happens.
  const usage = new Map([
    [ROOT, { values: ["errorText"], used: true }],
    [DOC_CONTENT, { values: [], used: true }],
  ]);
  const erased = parse(
    "resolve-check.mjs",
    `import { errorText } from "${ROOT}";\nimport { type DocFileContent } from "${DOC_CONTENT}";\nvoid errorText;\nexport type A = DocFileContent;\n`,
  );
  expect(describeResolveCheckDrift(erased, "resolve-check.mjs", usage)).toMatch(
    /does not load .*docContent at runtime/,
  );

  const loaded = parse(
    "resolve-check.mjs",
    `import { errorText } from "${ROOT}";\nimport * as doc from "${DOC_CONTENT}";\nvoid errorText;\nvoid doc;\n`,
  );
  expect(describeResolveCheckDrift(loaded, "resolve-check.mjs", usage)).toEqual("");
});

test("a fixture that takes a specifier as a whole module is reported, not swallowed", () => {
  const usage = new Map([
    [ROOT, { values: [], used: true }],
    [DOC_CONTENT, { values: [], used: true }],
  ]);
  const whole = parse(
    "resolve-check.mjs",
    `const everything = require("${ROOT}");\nimport * as doc from "${DOC_CONTENT}";\nvoid everything;\nvoid doc;\n`,
  );
  expect(describeResolveCheckDrift(whole, "resolve-check.mjs", usage)).toMatch(
    /require of @evener\/appwire-client names no binding this check can account for/,
  );
});
