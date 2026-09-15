// The shared module-specifier reader, one case per form it claims to read.
//
// It lives under the frontend's scripts/ for the reason the rewriter's test
// does: this is the only runner in the repo with the node_modules these tools
// parse with.
import assert from "node:assert/strict";
import test from "node:test";
import ts from "typescript";
import { isLoadedAtRuntime, moduleSpecifierSites, parseSource } from "../../../../scripts/sdk/module-specifiers.mjs";

const SPECIFIER = "../../appwire-client/typescript/errors";

function sitesFor(source) {
  return moduleSpecifierSites(ts, parseSource(ts, "fixture.ts", source));
}

// Every form the reader claims to recognize, one case each. @@SPEC@@ stands in
// for the specifier so the same source can be checked for the kind it reads.
const forms = [
  { name: "static named import", kind: "import-named", source: 'import { errorText } from "@@SPEC@@";\nvoid errorText;\n' },
  { name: "static named import, single-quoted", kind: "import-named", source: "import { errorText } from '@@SPEC@@';\nvoid errorText;\n" },
  { name: "static named import, specifier on the next line", kind: "import-named", source: 'import { errorText } from\n\t"@@SPEC@@";\nvoid errorText;\n' },
  { name: "namespace import", kind: "import-namespace", source: 'import * as everything from "@@SPEC@@";\nvoid everything;\n' },
  { name: "default import", kind: "import-default", source: 'import theDefault from "@@SPEC@@";\nvoid theDefault;\n' },
  { name: "side-effect import", kind: "import-side-effect", source: 'import "@@SPEC@@";\n' },
  { name: "type-only import", kind: "import-named", source: 'import type { WireError } from "@@SPEC@@";\nexport type Alias = WireError;\n' },
  { name: "named re-export", kind: "export-from", source: 'export { errorText } from "@@SPEC@@";\n' },
  { name: "star re-export", kind: "export-star-from", source: 'export * from "@@SPEC@@";\n' },
  { name: "dynamic import with a backtick specifier", kind: "dynamic-import", source: "const loaded = await import(`@@SPEC@@`);\nvoid loaded;\n" },
  { name: "require call", kind: "require", source: 'const loaded = require("@@SPEC@@");\nvoid loaded;\n' },
  { name: "vi.mock with the specifier on the next line", kind: "mock-call", source: 'vi.mock(\n\t"@@SPEC@@",\n\t() => ({}),\n);\n' },
  { name: "vi.importActual call", kind: "mock-call", source: 'const actual = await vi.importActual("@@SPEC@@");\nvoid actual;\n' },
  { name: "inline import type node", kind: "import-named", source: 'export type Alias = import("@@SPEC@@").WireError;\n' },
  { name: "TypeScript import-equals require", kind: "require-equals", source: 'import loaded = require("@@SPEC@@");\nvoid loaded;\n' },
  { name: "side-effect import with no space before the specifier", kind: "import-side-effect", source: 'import"@@SPEC@@";\n' },
  { name: "named import with no whitespace at all", kind: "import-named", source: 'import{errorText}from"@@SPEC@@";\nvoid errorText;\n' },
  { name: "named re-export with no whitespace at all", kind: "export-from", source: 'export{errorText}from"@@SPEC@@";\n' },
  { name: "namespace re-export", kind: "export-namespace-from", source: 'export * as everything from "@@SPEC@@";\n' },
];

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

test("a .jsx file with a specifier inside its JSX still yields it", () => {
  // Parsed as TS, the `<View>` opens a type assertion, not an element, and the
  // dynamic import nested in the markup is lost -- the whole subtree is. A
  // .jsx file is JSX, and React Native writes JSX in .js too, so both parse as
  // JSX and the lazy load surfaces.
  const source = `export const Screen = () => <View>{import("${SPECIFIER}")}</View>;\n`;
  for (const name of ["screen.jsx", "screen.js"]) {
    const sites = moduleSpecifierSites(ts, parseSource(ts, name, source));
    assert.equal(sites.length, 1, `${name}: expected one site, got ${sites.map((site) => site.kind).join(", ")}`);
    assert.equal(sites[0].kind, "dynamic-import");
    assert.equal(sites[0].text, SPECIFIER);
  }
});
