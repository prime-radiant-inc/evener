// @vitest-environment node

// Issue #2599: a flush called inside an outer act() cannot settle the work its
// own rounds' effects start. React 19 flushes its queue only when the outermost
// async act() exits (react.development.js: only `prevActScopeDepth === 0`
// flushes), so a nested flush's rounds schedule effects that wait for the outer
// act, and durable work they start registers after the flush has returned. The
// rule for callers is therefore: flush after the outer act, not inside it.
//
// This pins that rule where it is cheap to check: no test file calls the flush
// directly inside an `act(...)`. The check parses each file with the TypeScript
// compiler, so comments, strings, regular-expression literals and JSX text
// cannot confuse it, and a self-check proves the detector is not vacuous.
//
// A call reached through a helper (for example the seeding helpers in
// Session.test.tsx and heldSteerTestUtils.ts, or a helper wrapped in an outer
// act) is out of this scan's reach, and the console guard does not cover it: the
// dropped work lands inside the still-open act at its exit, so no "not wrapped
// in act" warning fires. A caller of such a helper must flush after the outer
// act itself; the HeldSteer tests follow their seed with waitFor for that.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import {
  type CallExpression,
  createSourceFile,
  forEachChild,
  isCallExpression,
  isIdentifier,
  isImportDeclaration,
  isImportSpecifier,
  isNamedImports,
  isPropertyAccessExpression,
  type Node,
  ScriptKind,
  ScriptTarget,
} from "typescript";
import { expect, test } from "vitest";

const SELF = fileURLToPath(import.meta.url);
const SRC = dirname(SELF); // cmd/evener-hub/frontend/src
const SKIPPED_DIRS = new Set(["node_modules", "dist"]);
const TEST_FILE = /\.(?:test|spec)\.(?:c|m)?[jt]sx?$/;
const FLUSH = "flushPendingTurnsProjectionForTests";
const ACT = "act";

function testFilesUnder(dir: string): string[] {
  const files: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name.startsWith(".") || SKIPPED_DIRS.has(entry.name)) continue;
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...testFilesUnder(path));
    } else if (TEST_FILE.test(entry.name)) {
      files.push(path);
    }
  }
  return files.sort();
}

function calleeName(node: Node): string | undefined {
  if (isIdentifier(node)) return node.text;
  if (isPropertyAccessExpression(node)) return node.name.text;
  return undefined;
}

// Every local name `importedName` is imported under, including aliases
// (`import { flushPendingTurnsProjectionForTests as flush }`), plus the bare
// name for a direct or global use.
function importedLocalNames(source: Node, importedName: string): Set<string> {
  const names = new Set([importedName]);
  forEachChild(source, (child) => {
    if (!isImportDeclaration(child)) return;
    const bindings = child.importClause?.namedBindings;
    if (bindings === undefined || !isNamedImports(bindings)) return;
    for (const specifier of bindings.elements) {
      if (!isImportSpecifier(specifier)) continue;
      const imported = specifier.propertyName ?? specifier.name;
      if (isIdentifier(imported) && imported.text === importedName) names.add(specifier.name.text);
    }
  });
  return names;
}

// A call to `act(...)`, in any spelling: React's act raises the act scope for
// its callback whether the callback is synchronous or not, so a flush reached
// inside any act(...) is nested and cannot flush the queue at its own exit.
function isActCall(node: CallExpression, actNames: Set<string>): boolean {
  return actNames.has(calleeName(node.expression) ?? "");
}

// True when the file holds a direct flush call inside an `act(...)`. A flush
// passed straight to act (`act(flushPendingTurnsProjectionForTests)`) is the
// same nesting with no call expression to see, so it counts too.
function hasNestedFlush(path: string, text: string): boolean {
  const kind = path.endsWith("x") ? ScriptKind.TSX : ScriptKind.TS;
  const source = createSourceFile(path, text, ScriptTarget.Latest, true, kind);
  const actNames = importedLocalNames(source, ACT);
  const flushNames = importedLocalNames(source, FLUSH);
  const isFlushCall = (node: CallExpression): boolean => flushNames.has(calleeName(node.expression) ?? "");
  let nested = false;
  const visit = (node: Node, insideAct: boolean): void => {
    if (nested) return;
    if (insideAct && isCallExpression(node) && isFlushCall(node)) {
      nested = true;
      return;
    }
    if (isCallExpression(node) && isActCall(node, actNames)) {
      if (node.arguments.some((argument) => isIdentifier(argument) && flushNames.has(argument.text))) {
        nested = true;
        return;
      }
      forEachChild(node, (child) => visit(child, true));
      return;
    }
    forEachChild(node, (child) => visit(child, insideAct));
  };
  visit(source, false);
  return nested;
}

test("the detector uses the parser, so literals and prose cannot fool it", () => {
  const file = "example.test.tsx";
  const nested = "await act(async () => {\n  await flushPendingTurnsProjectionForTests();\n});";
  const outermost =
    "await act(async () => {\n  await refreshPendingTurnsProjection(ref);\n});\nawait flushPendingTurnsProjectionForTests();";
  // A comment, a regex literal carrying an apostrophe, and JSX prose each hold
  // characters that a hand-rolled mask could read as a string.
  const commented =
    "// call flushPendingTurnsProjectionForTests() outside act\nawait flushPendingTurnsProjectionForTests();";
  const regexBeforeOutermost =
    "const m = /hasn't started yet/.test(s);\nawait act(async () => {});\nawait flushPendingTurnsProjectionForTests();";
  const regexBeforeNested =
    "const m = /hasn't started yet/.test(s);\nawait act(async () => {\n  await flushPendingTurnsProjectionForTests();\n});";
  const jsxProse =
    "const node = <p>this file's body</p>;\nawait act(async () => {});\nawait flushPendingTurnsProjectionForTests();";
  // Every spelling of the nesting: function expression, parenthesized callback,
  // a synchronous act, a flush call passed straight to act, and each of the two
  // names imported under an alias.
  const functionExpression = "await act(async function () {\n  await flushPendingTurnsProjectionForTests();\n});";
  const parenthesized = "await act((async () => {\n  await flushPendingTurnsProjectionForTests();\n}));";
  const syncAct = "await act(() => flushPendingTurnsProjectionForTests());";
  const flushAsCallback = "await act(flushPendingTurnsProjectionForTests);";
  const aliasedAct =
    'import { act as rtlAct } from "@testing-library/react";\nawait rtlAct(async () => {\n  await flushPendingTurnsProjectionForTests();\n});';
  const aliasedActOutermost =
    'import { act as rtlAct } from "@testing-library/react";\nawait rtlAct(async () => {});\nawait flushPendingTurnsProjectionForTests();';
  const aliasedFlush =
    'import { flushPendingTurnsProjectionForTests as flush } from "./flush";\nawait act(async () => {\n  await flush();\n});';
  expect(hasNestedFlush(file, nested)).toBe(true);
  expect(hasNestedFlush(file, outermost)).toBe(false);
  expect(hasNestedFlush(file, commented)).toBe(false);
  expect(hasNestedFlush(file, regexBeforeOutermost)).toBe(false);
  expect(hasNestedFlush(file, regexBeforeNested)).toBe(true);
  expect(hasNestedFlush(file, jsxProse)).toBe(false);
  expect(hasNestedFlush(file, functionExpression)).toBe(true);
  expect(hasNestedFlush(file, parenthesized)).toBe(true);
  expect(hasNestedFlush(file, syncAct)).toBe(true);
  expect(hasNestedFlush(file, flushAsCallback)).toBe(true);
  expect(hasNestedFlush(file, aliasedAct)).toBe(true);
  expect(hasNestedFlush(file, aliasedActOutermost)).toBe(false);
  expect(hasNestedFlush(file, aliasedFlush)).toBe(true);
});

test("no test file calls the projection flush directly inside an act(...)", () => {
  const offenders = testFilesUnder(SRC)
    .filter((path) => path !== SELF && hasNestedFlush(path, readFileSync(path, "utf8")))
    .map((path) => relative(SRC, path));
  expect(offenders).toEqual([]);
});
