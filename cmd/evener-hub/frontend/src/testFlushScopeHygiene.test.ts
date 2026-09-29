// @vitest-environment node

// Issue #2599: a flush called inside an outer act() cannot settle the work its
// own rounds' effects start. React 19 flushes its queue only when the outermost
// async act() exits (react.development.js: only `prevActScopeDepth === 0`
// flushes), so a nested flush's rounds schedule effects that wait for the outer
// act, and durable work they start registers after the flush has returned. The
// rule for callers is therefore: flush after the outer act, not inside it.
//
// This pins that rule where it is cheap to check: no test file calls the flush
// directly inside an `act(async ...)`. The check parses each file with the
// TypeScript compiler, so comments, strings, regular-expression literals and
// JSX text cannot confuse it, and a self-check proves the detector is not
// vacuous. A call reached through a helper (for example the seeding helpers in
// Session.test.tsx and heldSteerTestUtils.ts, or a helper wrapped in an outer
// act) is out of this scan's reach; the suite's console guard still fails any
// update an unsettled flush drops outside act.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import {
  type CallExpression,
  createSourceFile,
  forEachChild,
  isArrowFunction,
  isCallExpression,
  isIdentifier,
  isPropertyAccessExpression,
  type Node,
  ScriptKind,
  ScriptTarget,
  SyntaxKind,
} from "typescript";
import { expect, test } from "vitest";

const SELF = fileURLToPath(import.meta.url);
const SRC = dirname(SELF); // cmd/evener-hub/frontend/src
const SKIPPED_DIRS = new Set(["node_modules", "dist"]);
const TEST_FILE = /\.(?:test|spec)\.(?:c|m)?[jt]sx?$/;
const FLUSH = "flushPendingTurnsProjectionForTests";

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

// `act(async () => ...)`: the Testing Library act whose callback is an async
// arrow. A bare `act(() => ...)` runs its callback synchronously and flushes at
// its own exit, so only the async form holds effects for an outer act.
function isAsyncActCall(node: CallExpression): boolean {
  if (calleeName(node.expression) !== "act") return false;
  const callback = node.arguments[0];
  return (
    callback !== undefined &&
    isArrowFunction(callback) &&
    callback.modifiers?.some((modifier) => modifier.kind === SyntaxKind.AsyncKeyword) === true
  );
}

// True when the file holds a direct flush call inside an `act(async ...)`.
function hasNestedFlush(path: string, text: string): boolean {
  const kind = path.endsWith("x") ? ScriptKind.TSX : ScriptKind.TS;
  const source = createSourceFile(path, text, ScriptTarget.Latest, true, kind);
  let nested = false;
  const visit = (node: Node, insideAct: boolean): void => {
    if (nested) return;
    if (insideAct && isCallExpression(node) && calleeName(node.expression) === FLUSH) {
      nested = true;
      return;
    }
    const childInsideAct = insideAct || (isCallExpression(node) && isAsyncActCall(node));
    forEachChild(node, (child) => visit(child, childInsideAct));
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
  expect(hasNestedFlush(file, nested)).toBe(true);
  expect(hasNestedFlush(file, outermost)).toBe(false);
  expect(hasNestedFlush(file, commented)).toBe(false);
  expect(hasNestedFlush(file, regexBeforeOutermost)).toBe(false);
  expect(hasNestedFlush(file, regexBeforeNested)).toBe(true);
  expect(hasNestedFlush(file, jsxProse)).toBe(false);
});

test("no test file calls the projection flush directly inside an act(async ...)", () => {
  const offenders = testFilesUnder(SRC)
    .filter((path) => path !== SELF && hasNestedFlush(path, readFileSync(path, "utf8")))
    .map((path) => relative(SRC, path));
  expect(offenders).toEqual([]);
});
