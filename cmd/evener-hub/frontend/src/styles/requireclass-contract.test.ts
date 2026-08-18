// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

// Every CSS Modules import in this codebase (`import styles from
// "./x.module.css"`) is typed as an index signature under this project's
// `noUncheckedIndexedAccess`, so `styles.foo` is `string | undefined` and
// tsc can never catch a class that's referenced but never defined - that's
// exactly why requireClass(styles.foo, ...) exists (see
// widgets/internal/requireClass.ts): it turns the gap into a loud throw AT
// RUNTIME. But vite.config.ts's `test` block never sets `test.css`, which
// defaults to `false` - CSS Modules are left unprocessed under vitest, so
// `styles.foo` resolves to a defined (truthy) stub for ANY property name,
// real or not, and requireClass's own guard never fires in the one run
// that would otherwise catch it. A widget shipped exactly this gap once
// (radiogroup.module.css was missing the `optionLabel` class that
// widgets/radiogroup/index.tsx referenced via requireClass - see
// f50b5606e) and only a live dev server browsing to /dev/widgets caught
// it, because that's the one path where requireClass actually evaluates
// against the REAL stylesheet.
//
// This test closes that gap statically: it reads every *.module.css file
// straight off disk with node:fs (bypassing vite's transform entirely -
// the same reason token-contract.test.ts does this, see its own top
// comment and types: src/styles/node-fs-shim.d.ts), parses each one's real
// class list, and checks every `styles.foo`-shaped reference in every
// consumer file under src/ actually resolves to a class that's there.
const SRC_ROOT = dirname(dirname(fileURLToPath(import.meta.url))); // src/styles/.. = src

function walkFiles(dir: string, isMatch: (name: string) => boolean, found: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) walkFiles(full, isMatch, found);
    else if (entry.isFile() && isMatch(entry.name)) found.push(full);
  }
  return found;
}

// --- mechanism 1: what classes does a stylesheet actually define? -------
//
// A class is "defined" only when it appears in SELECTOR position, never
// inside a declaration's value - scoped this way so a future declaration
// value that happens to contain a literal dot followed by a letter (e.g.
// `url("./sprite.svg")`, a file extension) can never be mistaken for a
// class and mask a genuinely missing one (see the poison test below that
// pins exactly this). Comments are stripped first, identical to
// token-contract.test.ts's own COMMENT_RE, so a class-shaped mention
// inside a comment doesn't count as a definition either.
const COMMENT_RE = /\/\*[\s\S]*?\*\//g;

// A selector or at-rule prelude is the text between the start of the file
// or a `{`/`}`, and the next `{`. The boundary MUST be a lookbehind rather
// than a consumed leading character: that lets two preludes share the same
// single brace as their boundary, which is exactly the shape of
// `@media (...) { .foo { ... } }` - the text right after @media's own `{`
// starts a second, nested prelude, " .foo ". A version that consumes the
// boundary character as part of the match cannot do this (the first match
// already used that `{` up) and silently drops every rule nested one level
// inside an at-rule - which is exactly where every real @media in this
// codebase's CSS modules puts its rules (dialog, tooltip, menu, statusdot,
// streamingtext). Verified both ways by hand before picking this one; the
// poison test below pins the @media case directly.
const PRELUDE_RE = /(?<=^|[{}])([^{}]*)\{/g;

// Class tokens within a prelude: identifier-shaped only, the same shape as
// a JS property name - every class in this codebase's CSS modules is
// camelCase with no hyphens (swept by hand across all 87 files while
// building this test; a hyphenated class couldn't be reached via
// `styles.foo` dot-access anyway). Requiring a letter/underscore
// immediately after the dot excludes decimals in media conditions
// (`min-width: 43.75em`) with no extra casing needed.
const CLASS_TOKEN_RE = /\.([A-Za-z_][A-Za-z0-9_]*)/g;

function definedClassNames(cssText: string): Set<string> {
  const stripped = cssText.replace(COMMENT_RE, " ");
  const names = new Set<string>();
  for (const prelude of stripped.matchAll(PRELUDE_RE)) {
    for (const token of prelude[1]!.matchAll(CLASS_TOKEN_RE)) names.add(token[1]!);
  }
  return names;
}

const CSS_MODULE_PATHS = walkFiles(SRC_ROOT, (name) => name.endsWith(".module.css"));

const CSS_CLASSES = new Map<string, Set<string>>();
for (const absPath of CSS_MODULE_PATHS) {
  CSS_CLASSES.set(relative(SRC_ROOT, absPath), definedClassNames(readFileSync(absPath, "utf8")));
}

test("definedClassNames finds a plain class selector", () => {
  expect(definedClassNames(".foo { color: red; }")).toEqual(new Set(["foo"]));
});

test("definedClassNames finds every class in a compound + descendant selector", () => {
  expect(definedClassNames('.option[aria-checked="true"] .dotInner { background: blue; }')).toEqual(
    new Set(["option", "dotInner"]),
  );
});

test("definedClassNames finds every class in a comma-grouped selector", () => {
  expect(definedClassNames(".a, .b { color: red; }")).toEqual(new Set(["a", "b"]));
});

test("definedClassNames finds a class nested one level inside @media - the shape every real @media in this codebase uses", () => {
  expect(
    definedClassNames("@media (prefers-reduced-motion: reduce) {\n  .dialogVariant {\n    animation: none;\n  }\n}"),
  ).toEqual(new Set(["dialogVariant"]));
});

test("definedClassNames does not mistake a decimal media condition for a class", () => {
  expect(definedClassNames("@media (min-width: 43.75em) { .wide { display: flex; } }")).toEqual(new Set(["wide"]));
});

test("definedClassNames ignores a class-shaped mention inside a comment", () => {
  expect(definedClassNames("/* .fake */ .real { color: red; }")).toEqual(new Set(["real"]));
});

test("definedClassNames does not see into a declaration value, so a future url() can't smuggle in a fake class", () => {
  expect(definedClassNames('.icon { background: url("./sprite.svg") no-repeat; }')).toEqual(new Set(["icon"]));
});

// --- mechanism 2: which identifiers does a source file bind to a stylesheet? ---
//
// Only a plain default import binds an identifier to a real CSS Modules
// export object anywhere in this codebase - verified: no `import * as`, no
// named re-export, no dynamic `import()` of a .module.css under src - so
// this one shape is all resolving "which stylesheet does this identifier
// mean, in this file" ever needs.
const IMPORT_RE = /^import\s+([A-Za-z_][A-Za-z0-9_]*)\s+from\s+["']([^"']+\.module\.css)["'];?\s*$/gm;

function parseCssImports(sourceText: string): Array<{ ident: string; importPath: string }> {
  const imports: Array<{ ident: string; importPath: string }> = [];
  for (const match of sourceText.matchAll(IMPORT_RE)) {
    imports.push({ ident: match[1]!, importPath: match[2]! });
  }
  return imports;
}

test("parseCssImports extracts a same-directory default import", () => {
  expect(parseCssImports('import styles from "./radiogroup.module.css";')).toEqual([
    { ident: "styles", importPath: "./radiogroup.module.css" },
  ]);
});

test("parseCssImports extracts a parent-relative, differently-named import", () => {
  expect(parseCssImports('import buttonStyles from "../button/button.module.css";')).toEqual([
    { ident: "buttonStyles", importPath: "../button/button.module.css" },
  ]);
});

test("parseCssImports finds every css-module import in a file that has several", () => {
  const text = [
    'import codeblockStyles from "../codeblock/codeblock.module.css";',
    'import styles from "./markdown.module.css";',
  ].join("\n");
  expect(parseCssImports(text)).toEqual([
    { ident: "codeblockStyles", importPath: "../codeblock/codeblock.module.css" },
    { ident: "styles", importPath: "./markdown.module.css" },
  ]);
});

test("parseCssImports ignores a plain, non-CSS import", () => {
  expect(parseCssImports('import { useState } from "react";')).toEqual([]);
});

// --- mechanism 2b: what import-shaped .module.css mentions got missed? ---
//
// IMPORT_RE only recognizes ONE shape: a plain default import, alone on
// its line, nothing trailing but an optional `;`. A namespace import, a
// type-only import, a re-export, or an otherwise-normal import with a
// same-line trailing comment all still mention a CSS module but don't
// match that shape - so parseCssImports silently produces NO binding for
// any of them. Left unchecked, a file whose ONLY css import takes one of
// these shapes gets `bindings.size === 0` in the production loop below
// and is skipped ENTIRELY: no test generated, no failure, no signal at
// all that its classes were never checked - a stray trailing comment
// added to an import line would quietly turn this whole contract off for
// that one file.
//
// This scan is deliberately loose - anything starting with the bare
// keyword `import` or `export` that mentions ".module.css" later on the
// same line - rather than trying to enumerate every unsupported shape.
// Anchoring to "the TRIMMED line starts with the keyword" (real ES
// import/export declarations are always top-level; nesting one inside a
// block is a syntax error) is what keeps this from tripping over its own
// subject matter: a comment mentioning "import" and ".module.css" in
// prose never starts the line with the bare keyword (it starts with
// `//`/`/*`/`*`), and every fixture string in this file's own tests below
// is either indented inside a test() body or starts with `const`/a quote
// character, not literally `import`/`export` - both excluded
// automatically, with no special case needed for "this is my own file."
// Anything this flags that IMPORT_RE also parsed is excluded via
// parsedLines, so a normal import stays silent.
const LOOSE_IMPORT_LINE_RE = /^(?:import|export)\b.*\.module\.css/;

function unparsedModuleCssImportLines(sourceText: string): string[] {
  const parsedLines = new Set<string>();
  for (const match of sourceText.matchAll(IMPORT_RE)) parsedLines.add(match[0].trim());

  const offenders: string[] = [];
  for (const rawLine of sourceText.split("\n")) {
    if (rawLine !== rawLine.trimStart()) continue; // real import/export declarations are always column-0
    const line = rawLine.trim();
    if (LOOSE_IMPORT_LINE_RE.test(line) && !parsedLines.has(line)) offenders.push(line);
  }
  return offenders;
}

test("unparsedModuleCssImportLines is silent for a normal, fully-parsed import", () => {
  expect(unparsedModuleCssImportLines('import styles from "./radiogroup.module.css";')).toEqual([]);
});

test("unparsedModuleCssImportLines flags a trailing same-line comment on an otherwise-normal import", () => {
  const line = 'import styles from "./radiogroup.module.css"; // eslint-disable-line no-unused-vars';
  expect(unparsedModuleCssImportLines(line)).toEqual([line]);
});

test("unparsedModuleCssImportLines flags a namespace import", () => {
  const line = 'import * as styles from "./radiogroup.module.css";';
  expect(unparsedModuleCssImportLines(line)).toEqual([line]);
});

test("unparsedModuleCssImportLines flags a type-only import", () => {
  const line = 'import type styles from "./radiogroup.module.css";';
  expect(unparsedModuleCssImportLines(line)).toEqual([line]);
});

test("unparsedModuleCssImportLines flags a re-export", () => {
  const line = 'export { default as styles } from "./radiogroup.module.css";';
  expect(unparsedModuleCssImportLines(line)).toEqual([line]);
});

test("unparsedModuleCssImportLines finds only the offending line when a file mixes a good import and a bad one", () => {
  const badLine = 'import rawStyles from "./card.module.css"; // trailing comment breaks the strict shape';
  const sourceText = ['import styles from "./markdown.module.css";', badLine].join("\n");
  expect(unparsedModuleCssImportLines(sourceText)).toEqual([badLine]);
});

test("unparsedModuleCssImportLines does not flag prose or fixture text that merely mentions both words", () => {
  const notAnImport = [
    "// A comment mentioning an import and a .module.css path is not code.",
    "  const line = 'import styles from \"./x.module.css\";'; // indented fixture text",
  ].join("\n");
  expect(unparsedModuleCssImportLines(notAnImport)).toEqual([]);
});

// --- mechanism 3: which classes does a source file actually reference? ---
//
// Every `<ident>.<name>` access where <ident> is one of THIS file's own
// css-module bindings - covers both the mandated `requireClass(styles.foo,
// ...)` call shape and a bare `styles.foo` member access (JSX className,
// template literal, a clsx(...) argument) with the same scan: a CSS
// Modules default export has no legitimate use other than reading a class
// off it, so once an identifier is known (per file) to be bound to a
// stylesheet, every dotted access on it is a class reference. Scoping by
// the file's OWN actual import bindings - never a hardcoded name like
// "styles" - matters concretely here: several *.test.tsx files import the
// real module as `rawStyles` and then build their own, differently-keyed
// local object literally called `styles` (see badge.test.tsx) to give
// semantic test names to tone values - that local `styles.info`-shaped
// access is not a stylesheet reference at all, and keying strictly off
// each file's real import identifiers leaves it alone automatically
// instead of needing a special case (see the poison test below). This is
// scope-blind by design, not real lexical-scope analysis: it follows
// today's rawStyles-rebinding convention specifically, by matching an
// import-bound name anywhere in the file's text - a hypothetical NESTED
// shadow of the same bound name would still be attributed to the
// stylesheet. That's the safe direction: it can only over-include a
// reference to check, never silently drop one.
const MEMBER_ACCESS_RE = /\b([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\b/g;

function parseClassReferences(
  sourceText: string,
  bindings: ReadonlyMap<string, string>,
): Array<{ ident: string; name: string; cssKey: string }> {
  const refs: Array<{ ident: string; name: string; cssKey: string }> = [];
  for (const match of sourceText.matchAll(MEMBER_ACCESS_RE)) {
    const ident = match[1]!;
    const cssKey = bindings.get(ident);
    if (cssKey !== undefined) refs.push({ ident, name: match[2]!, cssKey });
  }
  return refs;
}

test("parseClassReferences follows a requireClass call on a bound identifier", () => {
  const bindings = new Map([["styles", "widgets/radiogroup/radiogroup.module.css"]]);
  const text = 'requireClass(styles.optionLabel, "radiogroup.module.css", "optionLabel")';
  expect(parseClassReferences(text, bindings)).toEqual([
    { ident: "styles", name: "optionLabel", cssKey: "widgets/radiogroup/radiogroup.module.css" },
  ]);
});

test("parseClassReferences follows a bare member access too, not just requireClass calls", () => {
  const bindings = new Map([["styles", "dev/gallery-sections/radiogroup.module.css"]]);
  expect(parseClassReferences("<div className={styles.row}>", bindings)).toEqual([
    { ident: "styles", name: "row", cssKey: "dev/gallery-sections/radiogroup.module.css" },
  ]);
});

test("parseClassReferences only follows a file's own bound identifier, not a same-named local shadow", () => {
  const bindings = new Map([["rawStyles", "widgets/badge/badge.module.css"]]);
  const sourceText = [
    'import rawStyles from "./badge.module.css";',
    "const styles = {",
    '  neutral: requireClass(rawStyles.neutral, "badge.module.css", "neutral"),',
    "};",
    "styles.neutral; // local rebind, not a stylesheet reference - see badge.test.tsx",
  ].join("\n");
  expect(parseClassReferences(sourceText, bindings)).toEqual([
    { ident: "rawStyles", name: "neutral", cssKey: "widgets/badge/badge.module.css" },
  ]);
});

test("parseClassReferences ignores a dotted access on an identifier this file never bound", () => {
  expect(parseClassReferences("Object.keys(config).length", new Map([["styles", "x/x.module.css"]]))).toEqual([]);
});

// --- mechanism 4: put it together - every reference must resolve -------
function missingClassMessages(
  sourceRel: string,
  sourceText: string,
  bindings: ReadonlyMap<string, string>,
  classesByKey: ReadonlyMap<string, ReadonlySet<string>>,
): string[] {
  const unparsed = unparsedModuleCssImportLines(sourceText);
  if (unparsed.length > 0) {
    throw new Error(
      `requireclass-contract: ${sourceRel} has an import-shaped line mentioning a CSS module that this test's parser did not bind: ${unparsed.map((line) => JSON.stringify(line)).join(", ")} - likely a trailing comment, "import * as", "import type", or a re-export shape parseCssImports doesn't understand; fix the import's shape (or extend parseCssImports) so this file's classes actually get checked instead of silently skipped.`,
    );
  }

  const messages: string[] = [];
  for (const { ident, name, cssKey } of parseClassReferences(sourceText, bindings)) {
    const classes = classesByKey.get(cssKey);
    if (!classes) {
      throw new Error(
        `requireclass-contract: no parsed CSS module for "${cssKey}" (imported by ${sourceRel} as ${ident}) - this test's own walk is missing a file.`,
      );
    }
    if (!classes.has(name)) {
      messages.push(
        `${cssKey} is missing the "${name}" class, referenced from ${sourceRel} as ${ident}.${name} - add the class to ${cssKey} or remove the reference.`,
      );
    }
  }
  return messages;
}

// Pins the ORIGINAL radiogroup bug (f50b5606e) exactly: index.tsx's CLASS
// table ran `optionLabel` through requireClass, but radiogroup.module.css
// never defined it - the same real class list the widget's other classes
// use, minus the one that went missing.
test("catches the original radiogroup bug shape: requireClass referencing a class the stylesheet never defines", () => {
  const sourceText = [
    'import { requireClass } from "../internal/requireClass";',
    'import styles from "./radiogroup.module.css";',
    "",
    "const CLASS = {",
    '  dotInner: requireClass(styles.dotInner, "radiogroup.module.css", "dotInner"),',
    '  optionLabel: requireClass(styles.optionLabel, "radiogroup.module.css", "optionLabel"),',
    "};",
  ].join("\n");
  const bindings = new Map([["styles", "widgets/radiogroup/radiogroup.module.css"]]);
  const classesByKey = new Map([
    ["widgets/radiogroup/radiogroup.module.css", new Set(["root", "legend", "options", "option", "dot", "dotInner"])],
  ]);
  expect(missingClassMessages("widgets/radiogroup/index.tsx", sourceText, bindings, classesByKey)).toEqual([
    'widgets/radiogroup/radiogroup.module.css is missing the "optionLabel" class, referenced from widgets/radiogroup/index.tsx as styles.optionLabel - add the class to widgets/radiogroup/radiogroup.module.css or remove the reference.',
  ]);
});

test("passes once the referenced class is restored, mirroring the real fix", () => {
  const sourceText = 'requireClass(styles.optionLabel, "radiogroup.module.css", "optionLabel")';
  const bindings = new Map([["styles", "widgets/radiogroup/radiogroup.module.css"]]);
  const classesByKey = new Map([["widgets/radiogroup/radiogroup.module.css", new Set(["optionLabel"])]]);
  expect(missingClassMessages("widgets/radiogroup/index.tsx", sourceText, bindings, classesByKey)).toEqual([]);
});

test("missingClassMessages throws when a reference resolves to a CSS module this test never parsed", () => {
  const bindings = new Map([["styles", "widgets/ghost/ghost.module.css"]]);
  const sourceText = 'requireClass(styles.foo, "ghost.module.css", "foo")';
  const classesByKey = new Map(); // deliberately empty - "widgets/ghost/ghost.module.css" was never walked
  expect(() => missingClassMessages("widgets/ghost/index.tsx", sourceText, bindings, classesByKey)).toThrow(
    /no parsed CSS module/,
  );
});

// Pins the "no silent skip" fix: a trailing same-line comment breaks
// IMPORT_RE's strict shape (see the unparsedModuleCssImportLines poison
// tests above), so parseCssImports finds no binding for this import at
// all - bindings ends up empty here exactly as bindingsFor would produce
// it for this real file. Without the check this pins,
// missingClassMessages would happily return [] (parseClassReferences has
// no bound identifier to check "styles.optionLabel" against), and the
// production loop's own `bindings.size === 0` branch would never even
// generate a test for this file - a real bug living behind that import
// would never be reported by ANY test, not even a failing one.
test("missingClassMessages throws on an unparsed import-shaped line instead of silently reporting nothing missing", () => {
  const sourceText = [
    'import styles from "./radiogroup.module.css"; // eslint-disable-line no-unused-vars',
    "",
    "styles.optionLabel;",
  ].join("\n");
  expect(() => missingClassMessages("widgets/radiogroup/index.tsx", sourceText, new Map(), CSS_CLASSES)).toThrow(
    /import-shaped line/,
  );
});

// --- the actual contract: run it against every real file under src -----
const SOURCE_PATHS = walkFiles(SRC_ROOT, (name) => name.endsWith(".ts") || name.endsWith(".tsx"));

function bindingsFor(sourceDir: string, sourceText: string): Map<string, string> {
  const bindings = new Map<string, string>();
  for (const { ident, importPath } of parseCssImports(sourceText)) {
    bindings.set(ident, relative(SRC_ROOT, join(sourceDir, importPath)));
  }
  return bindings;
}

for (const absPath of SOURCE_PATHS) {
  const sourceRel = relative(SRC_ROOT, absPath);
  const sourceText = readFileSync(absPath, "utf8");
  const bindings = bindingsFor(dirname(absPath), sourceText);
  // Skip only when there's truly nothing to check: no bindings AND no
  // import-shaped line that should have produced one. A file whose ONLY
  // css import took an unrecognized shape must still get a test - one
  // that throws via missingClassMessages's own check above - rather than
  // being silently skipped just because bindings.size ended up at 0.
  if (bindings.size === 0 && unparsedModuleCssImportLines(sourceText).length === 0) continue;

  test(`${sourceRel} only references classes that exist in its CSS module(s)`, () => {
    expect(missingClassMessages(sourceRel, sourceText, bindings, CSS_CLASSES)).toEqual([]);
  });
}
