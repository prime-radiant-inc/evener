// @vitest-environment node

// Issue #2599: a flush called inside an outer act() cannot settle the work its
// own rounds' effects start. React 19 flushes its queue only when the outermost
// async act() exits (react.development.js: only `prevActScopeDepth === 0`
// flushes), so a nested flush's rounds schedule effects that wait for the outer
// act, and durable work they start registers after the flush has returned. The
// rule for callers is therefore: flush after the outer act, not inside it.
//
// This pins that rule where it is cheap to check: no test file calls the flush
// directly inside an `act(async ...)`. The scan runs on a comment- and
// string-stripped copy, so the prose above and the patterns below do not match
// themselves, and a self-check proves the detector is not vacuous. A call
// reached through a helper (for example the seeding helpers in Session.test.tsx
// and heldSteerTestUtils.ts, or a helper wrapped in an outer act) is out of this
// scan's reach; the suite's console guard still fails any update an unsettled
// flush drops outside act.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SELF = fileURLToPath(import.meta.url);
const SRC = dirname(SELF); // cmd/evener-hub/frontend/src
const SKIPPED_DIRS = new Set(["node_modules", "dist"]);
const TEST_FILE = /\.(?:test|spec)\.(?:c|m)?[jt]sx?$/;
const FLUSH_CALL = /\bflushPendingTurnsProjectionForTests\s*\(/g;
const ACT_ASYNC = /\bact\s*\(\s*async\b/g;

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

// True where text[i] is code: outside comments and string/template literals.
// Newlines stay code so line counts and brace matching are unaffected.
function codeMask(text: string): boolean[] {
  const mask = new Array<boolean>(text.length).fill(true);
  const blank = (from: number, to: number) => {
    for (let k = from; k < to; k += 1) {
      if (text[k] !== "\n") mask[k] = false;
    }
  };
  let i = 0;
  while (i < text.length) {
    const ch = text[i];
    if (ch === "/" && text[i + 1] === "/") {
      const j = text.indexOf("\n", i);
      const end = j === -1 ? text.length : j;
      blank(i, end);
      i = end;
    } else if (ch === "/" && text[i + 1] === "*") {
      const j = text.indexOf("*/", i + 2);
      const end = j === -1 ? text.length : j + 2;
      blank(i, end);
      i = end;
    } else if (ch === '"' || ch === "'" || ch === "`") {
      let j = i + 1;
      while (j < text.length) {
        if (text[j] === "\\") {
          j += 2;
        } else if (text[j] === ch) {
          j += 1;
          break;
        } else {
          j += 1;
        }
      }
      blank(i, j);
      i = j;
    } else {
      i += 1;
    }
  }
  return mask;
}

function closeParen(text: string, mask: boolean[], open: number): number {
  let depth = 0;
  for (let i = open; i < text.length; i += 1) {
    if (!mask[i]) continue;
    if (text[i] === "(") depth += 1;
    else if (text[i] === ")") {
      depth -= 1;
      if (depth === 0) return i;
    }
  }
  return -1;
}

// True when the text holds a direct flush call inside an `act(async ...)`.
function hasNestedFlush(text: string): boolean {
  const mask = codeMask(text);
  const acts: Array<[number, number]> = [];
  for (const match of text.matchAll(ACT_ASYNC)) {
    const at = match.index;
    if (at === undefined || !mask[at]) continue;
    const end = closeParen(text, mask, text.indexOf("(", at));
    if (end !== -1) acts.push([at, end]);
  }
  for (const match of text.matchAll(FLUSH_CALL)) {
    const at = match.index;
    if (at === undefined || !mask[at]) continue;
    if (acts.some(([open, end]) => open < at && at < end)) return true;
  }
  return false;
}

test("the detector sees a nested flush and ignores an outermost one", () => {
  const nested = "await act(async () => {\n  await flushPendingTurnsProjectionForTests();\n});";
  const outermost =
    "await act(async () => {\n  await refreshPendingTurnsProjection(ref);\n});\nawait flushPendingTurnsProjectionForTests();";
  // A mention in prose is not a call.
  const inProse =
    "// call flushPendingTurnsProjectionForTests() outside act\nawait flushPendingTurnsProjectionForTests();";
  expect(hasNestedFlush(nested)).toBe(true);
  expect(hasNestedFlush(outermost)).toBe(false);
  expect(hasNestedFlush(inProse)).toBe(false);
});

test("no test file calls the projection flush directly inside an act(async ...)", () => {
  const offenders = testFilesUnder(SRC)
    .filter((path) => path !== SELF && hasNestedFlush(readFileSync(path, "utf8")))
    .map((path) => relative(SRC, path));
  expect(offenders).toEqual([]);
});
