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

test("a namespace or default import of the package is reported, not skipped", () => {
  // This derivation says what the tarball must provide. It cannot answer that
  // for a module taken whole, and answering nothing looked identical to a file
  // that imports nothing.
  expect(problemsFor(`import * as everything from "${ROOT}";\nvoid everything;\n`)).toEqual([
    "fixture.ts: import-namespace of @evener/appwire-client names no binding this check can account for",
  ]);
  expect(problemsFor(`import theDefault from "${ROOT}";\nvoid theDefault;\n`)).toEqual([
    "fixture.ts: import-default of @evener/appwire-client names no binding this check can account for",
  ]);
  expect(problemsFor(`import { errorText } from "${ROOT}";\nvoid errorText;\n`)).toEqual([]);
});
