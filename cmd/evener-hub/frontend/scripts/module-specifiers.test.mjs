// The shared module-specifier reader, one case per form it claims to read.
//
// The form list is a checked-in file, not a literal here, because the grep
// gate's audit test builds its fixtures from the same list: the two readers
// answer the same question in different languages, and this is what stops them
// drifting apart form by form, which is how they drifted in the first place.
//
// It lives under the frontend's scripts/ for the reason the rewriter's test
// does: this is the only runner in the repo with the node_modules these tools
// parse with.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { isLoadedAtRuntime, moduleSpecifierSites, parseSource } from "../../../../scripts/sdk/module-specifiers.mjs";

const sdkDir = path.resolve(fileURLToPath(import.meta.url), "../../../../../scripts/sdk");
const forms = JSON.parse(readFileSync(path.join(sdkDir, "module-specifier-forms.json"), "utf8"));

const SPECIFIER = "../../appwire-client/typescript/errors";

function sitesFor(source) {
  return moduleSpecifierSites(ts, parseSource(ts, "fixture.ts", source));
}

test("the form list is not empty and every entry is complete", () => {
  assert.ok(forms.length > 0);
  for (const form of forms) {
    assert.ok(form.name && form.kind && form.source.includes("@@SPEC@@"), `incomplete form: ${form.name}`);
  }
});

for (const form of forms) {
  test(`reads ${form.name} as ${form.kind}`, () => {
    const sites = sitesFor(form.source.replaceAll("@@SPEC@@", SPECIFIER));
    assert.equal(sites.length, 1, `expected exactly one site, got ${sites.map((site) => site.kind).join(", ")}`);
    assert.equal(sites[0].kind, form.kind);
    assert.equal(sites[0].text, SPECIFIER);
  });
}

test("named members carry their imported name, local name and type-only flag", () => {
  const [site] = sitesFor(`import { a, b as c, type d } from "${SPECIFIER}";\nvoid a;\nvoid c;\n`);
  assert.deepEqual(site.bindings, [
    { imported: "a", local: "a", typeOnly: false },
    { imported: "b", local: "c", typeOnly: false },
    { imported: "d", local: "d", typeOnly: true },
  ]);
});

test("a default import with named members beside it is reported as the default", () => {
  const [site] = sitesFor(`import theDefault, { errorText } from "${SPECIFIER}";\nvoid theDefault;\nvoid errorText;\n`);
  assert.equal(site.kind, "import-default");
  assert.deepEqual(site.bindings, [
    { imported: "default", local: "theDefault", typeOnly: false },
    { imported: "errorText", local: "errorText", typeOnly: false },
  ]);
});

test("a default binding keeps an import loaded even when every named member is a type", () => {
  const [mixed] = sitesFor(`import theDefault, { type WireError } from "${SPECIFIER}";\nvoid theDefault;\nexport type A = WireError;\n`);
  assert.equal(isLoadedAtRuntime(mixed), true);

  const [erased] = sitesFor(`import type theDefault from "${SPECIFIER}";\nexport type A = theDefault;\n`);
  assert.equal(isLoadedAtRuntime(erased), false);
});

test("an import-equals yields one site, not a nested require as well", () => {
  // `import X = require("./x")` parses its reference as an
  // ExternalModuleReference, not a call, so the require branch never sees it.
  const sites = sitesFor(`import loaded = require("${SPECIFIER}");\nvoid loaded;\n`);
  assert.deepEqual(
    sites.map((site) => site.kind),
    ["require-equals"],
  );
});

test("a statement-level type-only import is marked erased", () => {
  const [typed] = sitesFor(`import type { WireError } from "${SPECIFIER}";\nexport type A = WireError;\n`);
  assert.equal(typed.typeOnly, true);
  assert.equal(isLoadedAtRuntime(typed), false);
  const [value] = sitesFor(`import { errorText } from "${SPECIFIER}";\nvoid errorText;\n`);
  assert.equal(isLoadedAtRuntime(value), true);
});

test("a star re-export is loaded at run time even though it names no binding", () => {
  // A graph walk has to follow it; a "which values must the package provide"
  // question cannot use it. Those are different questions, and conflating
  // them stopped the native script walk one hop short of the package.
  const [site] = sitesFor(`export * from "${SPECIFIER}";\n`);
  assert.equal(isLoadedAtRuntime(site), true);
  assert.deepEqual(site.bindings, []);
});

test("a call that is not an import, a require or a mock is not a specifier site", () => {
  assert.deepEqual(sitesFor(`describe("${SPECIFIER}", () => {});\n`), []);
  assert.deepEqual(sitesFor(`expect(x).toThrow("${SPECIFIER}");\n`), []);
});

test("every site points at the literal node, so a caller can rewrite it in place", () => {
  const source = `import { errorText } from "${SPECIFIER}";\nvoid errorText;\n`;
  const [site] = sitesFor(source);
  assert.equal(source.slice(site.node.getStart(site.node.getSourceFile()), site.node.getEnd()), `"${SPECIFIER}"`);
});

test("an import whose every member is inline-type is erased, mixed is not", () => {
  // The statement carries no type-only flag; only the members say so.
  const [allType] = sitesFor(`import { type WireError } from "${SPECIFIER}";\nexport type A = WireError;\n`);
  assert.equal(allType.typeOnly, false);
  assert.equal(isLoadedAtRuntime(allType), false);

  const [mixed] = sitesFor(`import { errorText, type WireError } from "${SPECIFIER}";\nvoid errorText;\nexport type A = WireError;\n`);
  assert.equal(isLoadedAtRuntime(mixed), true);

  const [reexport] = sitesFor(`export { type WireError } from "${SPECIFIER}";\n`);
  assert.equal(isLoadedAtRuntime(reexport), false);
});

test("a site that names no binding is loaded whatever it does with what it finds", () => {
  for (const source of [`export * from "${SPECIFIER}";\n`, `import "${SPECIFIER}";\n`, `require("${SPECIFIER}");\n`]) {
    const [site] = sitesFor(source);
    assert.equal(isLoadedAtRuntime(site), true, source);
  }
});
