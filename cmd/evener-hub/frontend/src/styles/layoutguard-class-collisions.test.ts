// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { definedClassNames } from "./cssClassTestUtils";

// layoutguard (scripts/layoutguard/run.mjs) concatenates a case's module
// stylesheets into one unscoped resolved.css: CSS Modules hashing never runs,
// so `.header` in one module and `.header` in another are the SAME selector
// there. Two modules a case loads together must therefore never define the
// same class name, or one silently restyles the other's elements and the
// case measures geometry production never has. global.css and tokens.css are
// global by design and never module-scoped, so they are left out. A
// composed-widget case legitimately loads a widget module together with the
// module it composes from (resolveComposes refuses a composes line whose
// target is not among the case's cssFiles), so the composed class names are
// shared between the two modules by design; such a case records each shared
// class and its reason in its own case.json's "allowlist" field, and a row
// whose class stops being shared, or whose reason is empty, fails so the
// record is removed when the composition changes rather than silently
// licensing a future collision.
const SRC_ROOT = dirname(dirname(fileURLToPath(import.meta.url))); // src/styles/.. = src
const CASES_DIR = join(SRC_ROOT, "../scripts/layoutguard/cases");
const GLOBAL_STYLESHEETS = new Set(["styles/global.css", "styles/tokens.css"]);

interface CaseJson {
  cssFiles: string[];
  allowlist?: Record<string, string>;
}

/** Every class name two or more of `files` define, as "class (fileA, fileB)". */
function sharedClassNames(files: ReadonlyMap<string, ReadonlySet<string>>): string[] {
  const definers = new Map<string, string[]>();
  for (const [file, classes] of files) {
    for (const name of classes) definers.set(name, [...(definers.get(name) ?? []), file]);
  }
  return [...definers]
    .filter(([, inFiles]) => inFiles.length > 1)
    .map(([name, inFiles]) => `.${name} (${inFiles.join(", ")})`)
    .sort();
}

test("sharedClassNames names a class two stylesheets both define, with both files", () => {
  const files = new Map([
    ["widgets/panescaffold/panescaffold.module.css", definedClassNames(".pane {} .header {}")],
    ["panes/x/x.module.css", definedClassNames(".header {} .row {}")],
  ]);
  expect(sharedClassNames(files)).toEqual([
    ".header (widgets/panescaffold/panescaffold.module.css, panes/x/x.module.css)",
  ]);
});

test("sharedClassNames is silent when every class is defined by one stylesheet", () => {
  const files = new Map([
    ["a.module.css", definedClassNames(".header { color: red; } .header:hover {}")],
    ["b.module.css", definedClassNames("/* .header */ .questionHeader {}")],
  ]);
  expect(sharedClassNames(files)).toEqual([]);
});

const CASE_NAMES = readdirSync(CASES_DIR, { withFileTypes: true })
  .filter((entry) => entry.isDirectory())
  .map((entry) => entry.name);

test("layoutguard has cases to check", () => {
  expect(CASE_NAMES.length).toBeGreaterThan(0);
});

test("no layoutguard case loads two module stylesheets that define the same class", () => {
  const collisions: string[] = [];
  for (const name of CASE_NAMES) {
    const caseJson = JSON.parse(readFileSync(join(CASES_DIR, name, "case.json"), "utf8")) as CaseJson;
    const files = new Map(
      caseJson.cssFiles
        .filter((rel) => !GLOBAL_STYLESHEETS.has(rel))
        .map((rel) => [rel, definedClassNames(readFileSync(join(SRC_ROOT, rel), "utf8"))] as const),
    );
    // sharedClassNames reports each shared class as "name (fileA, fileB)";
    // the bare name is the string before the first " (".
    const sharedBareNames = new Set(sharedClassNames(files).map((shared) => shared.slice(0, shared.indexOf(" ("))));
    const allowed: Record<string, string> = caseJson.allowlist ?? {};
    for (const bare of sharedBareNames) if (!allowed[bare]) collisions.push(`${name}: ${bare}`);
    for (const [bare, reason] of Object.entries(allowed)) {
      if (!sharedBareNames.has(bare))
        collisions.push(`${name}: allowlisted ${bare} is no longer shared - remove the stale row`);
      else if (!reason)
        collisions.push(
          `${name}: allowlisted ${bare} has no reason - every allowlisted class must state why the de-scoped collision is safe`,
        );
    }
  }
  expect(collisions).toEqual([]);
});
