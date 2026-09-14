// packageValuesIn reads two different things with one reader: what the repo's
// consumers take from this package, and what resolve-check.mjs says they take.
// Both readings decide whether `make test-api-package` passes, so the shapes a
// hand-written regex used to miss are the ones worth pinning.
import { expect, test } from "vitest";
import { packageValuesIn, parse } from "./consumer-value-imports.mjs";

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
