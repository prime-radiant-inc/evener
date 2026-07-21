import { afterEach, test, expect } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render } from "@testing-library/react";
import { requireClass } from "../internal/requireClass";
import { DiffBlock } from "./index";
import rawStyles from "./diffblock.module.css";

afterEach(cleanup);

const styles = {
  add: requireClass(rawStyles.add, "diffblock.module.css", "add"),
  del: requireClass(rawStyles.del, "diffblock.module.css", "del"),
  header: requireClass(rawStyles.header, "diffblock.module.css", "header"),
  context: requireClass(rawStyles.context, "diffblock.module.css", "context"),
  meta: requireClass(rawStyles.meta, "diffblock.module.css", "meta"),
  line: requireClass(rawStyles.line, "diffblock.module.css", "line"),
  content: requireClass(rawStyles.content, "diffblock.module.css", "content"),
};

function lineElements(container: HTMLElement): Element[] {
  return Array.from(container.querySelectorAll(`.${styles.line}`));
}

// A line's marker glyph (+/-/space) and its stripped content are separate
// text nodes; .line's own textContent would concatenate both, so content
// assertions read the dedicated .content child instead.
function contentTextOf(line: Element): string | null {
  return line.querySelector(`.${styles.content}`)?.textContent ?? null;
}

const SIMPLE_DIFF = [
  "diff --git a/greet.go b/greet.go",
  "index abc1234..def5678 100644",
  "--- a/greet.go",
  "+++ b/greet.go",
  "@@ -1,3 +1,3 @@",
  " package main",
  "-func greet() string {",
  "+func greet(name string) string {",
  " \treturn \"hi\"",
].join("\n");

test("renders one line per input line", () => {
  const { container } = render(<DiffBlock unified={SIMPLE_DIFF} />);
  expect(lineElements(container)).toHaveLength(9);
});

test("classifies diff --git and index lines as header", () => {
  const { container } = render(<DiffBlock unified={SIMPLE_DIFF} />);
  const lines = lineElements(container);
  expect(lines[0]!.classList.contains(styles.header)).toBe(true);
  expect(contentTextOf(lines[0]!)).toBe("diff --git a/greet.go b/greet.go");
  expect(lines[1]!.classList.contains(styles.header)).toBe(true);
});

test("classifies the ---/+++ file header pair as header, keeping the full raw text", () => {
  const { container } = render(<DiffBlock unified={SIMPLE_DIFF} />);
  const lines = lineElements(container);
  expect(lines[2]!.classList.contains(styles.header)).toBe(true);
  expect(contentTextOf(lines[2]!)).toBe("--- a/greet.go");
  expect(lines[3]!.classList.contains(styles.header)).toBe(true);
  expect(contentTextOf(lines[3]!)).toBe("+++ b/greet.go");
});

test("classifies the @@ hunk header as header", () => {
  const { container } = render(<DiffBlock unified={SIMPLE_DIFF} />);
  const lines = lineElements(container);
  expect(lines[4]!.classList.contains(styles.header)).toBe(true);
  expect(contentTextOf(lines[4]!)).toBe("@@ -1,3 +1,3 @@");
});

test("classifies a space-prefixed line as context, stripping the marker", () => {
  const { container } = render(<DiffBlock unified={SIMPLE_DIFF} />);
  const lines = lineElements(container);
  expect(lines[5]!.classList.contains(styles.context)).toBe(true);
  expect(contentTextOf(lines[5]!)).toBe("package main");
});

test("classifies a --prefixed line inside a hunk as a deletion, stripping the marker", () => {
  const { container } = render(<DiffBlock unified={SIMPLE_DIFF} />);
  const lines = lineElements(container);
  expect(lines[6]!.classList.contains(styles.del)).toBe(true);
  expect(contentTextOf(lines[6]!)).toBe("func greet() string {");
});

test("classifies a +-prefixed line inside a hunk as an addition, stripping the marker", () => {
  const { container } = render(<DiffBlock unified={SIMPLE_DIFF} />);
  const lines = lineElements(container);
  expect(lines[7]!.classList.contains(styles.add)).toBe(true);
  expect(contentTextOf(lines[7]!)).toBe("func greet(name string) string {");
});

test("preserves a tab character in the stripped content", () => {
  const { container } = render(<DiffBlock unified={SIMPLE_DIFF} />);
  const lines = lineElements(container);
  expect(contentTextOf(lines[8]!)).toBe('\treturn "hi"');
});

// The classic unified-diff ambiguity: once inside a hunk (past the first
// @@), a deleted/added line that itself starts with "---"/"+++" must NOT be
// mistaken for a second file-header pair - only content before the first
// @@ of a file block can be a --- / +++ header.
test("a hunk line that itself starts with --- is a deletion, not a second header", () => {
  const diff = ["--- a/f", "+++ b/f", "@@ -1,2 +1,2 @@", "-normal line", "---divider---"].join(
    "\n",
  );
  const { container } = render(<DiffBlock unified={diff} />);
  const lines = lineElements(container);
  expect(lines[4]!.classList.contains(styles.del)).toBe(true);
  expect(contentTextOf(lines[4]!)).toBe("--divider---");
});

test("a hunk line that itself starts with +++ is an addition, not a second header", () => {
  const diff = ["--- a/f", "+++ b/f", "@@ -1,2 +1,2 @@", "-old", "+++new-marker-in-content"].join(
    "\n",
  );
  const { container } = render(<DiffBlock unified={diff} />);
  const lines = lineElements(container);
  expect(lines[4]!.classList.contains(styles.add)).toBe(true);
  expect(contentTextOf(lines[4]!)).toBe("++new-marker-in-content");
});

test("a second diff --git resets header detection for the next file in a multi-file diff", () => {
  const diff = [
    "diff --git a/one.go b/one.go",
    "--- a/one.go",
    "+++ b/one.go",
    "@@ -1 +1 @@",
    "-old one",
    "+new one",
    "diff --git a/two.go b/two.go",
    "--- a/two.go",
    "+++ b/two.go",
    "@@ -1 +1 @@",
    "-old two",
    "+new two",
  ].join("\n");
  const { container } = render(<DiffBlock unified={diff} />);
  const lines = lineElements(container);
  // the second file's own --- / +++ pair (indices 7,8) must read as headers
  // again, not as deletion/addition just because a hunk was already seen
  expect(lines[6]!.classList.contains(styles.header)).toBe(true); // diff --git two
  expect(lines[7]!.classList.contains(styles.header)).toBe(true); // --- a/two.go
  expect(lines[8]!.classList.contains(styles.header)).toBe(true); // +++ b/two.go
  expect(lines[9]!.classList.contains(styles.header)).toBe(true); // @@ ... @@
  expect(lines[10]!.classList.contains(styles.del)).toBe(true);
  expect(lines[11]!.classList.contains(styles.add)).toBe(true);
});

test("renders nothing for an empty diff", () => {
  const { container } = render(<DiffBlock unified="" />);
  expect(lineElements(container)).toHaveLength(0);
});

test("a trailing newline does not produce a spurious empty final line", () => {
  const { container } = render(<DiffBlock unified={"@@ -1 +1 @@\n-a\n+b\n"} />);
  expect(lineElements(container)).toHaveLength(3);
});

test("a genuinely blank context line (no marker at all) still renders as context", () => {
  // 4 lines: the hunk header, " context", a genuinely empty line, " more".
  const { container } = render(<DiffBlock unified={"@@ -1,2 +1,2 @@\n context\n\n more"} />);
  const lines = lineElements(container);
  expect(lines).toHaveLength(4);
  expect(lines[2]!.classList.contains(styles.context)).toBe(true);
  expect(contentTextOf(lines[2]!)).toBe("");
});

test("strips a trailing \\r from every line for CRLF-style diffs", () => {
  const diff = ["--- a/f\r", "+++ b/f\r", "@@ -1,2 +1,2 @@\r", "-old\r", "+new\r", " same\r"].join(
    "\n",
  );
  const { container } = render(<DiffBlock unified={diff} />);
  const lines = lineElements(container);
  // The exact-match "---"/"+++" header check would itself fail to
  // recognize a CRLF line ("---\r" !== "---") if stripping happened only
  // on already-classified content instead of before classification -
  // this fixture uses the " a/f" trailer form, so it isn't exercising
  // that exact-match path, but every content assertion below still
  // proves no \r survived into what's rendered.
  expect(contentTextOf(lines[0]!)).toBe("--- a/f");
  expect(contentTextOf(lines[1]!)).toBe("+++ b/f");
  expect(contentTextOf(lines[2]!)).toBe("@@ -1,2 +1,2 @@");
  expect(contentTextOf(lines[3]!)).toBe("old");
  expect(contentTextOf(lines[4]!)).toBe("new");
  expect(contentTextOf(lines[5]!)).toBe("same");
});

test("strips a trailing \\r before the ---/+++ exact-match check, not just from already-classified content", () => {
  // The bare "---"/"+++" form matches via an exact `raw === "---"` check,
  // which would fail on an un-stripped "---\r" and misclassify the header
  // as a deletion instead (content "--" after stripping just the leading
  // "-").
  const diff = ["---\r", "+++\r", "@@ -1 +1 @@\r", "-old\r", "+new\r"].join("\n");
  const { container } = render(<DiffBlock unified={diff} />);
  const lines = lineElements(container);
  expect(lines[0]!.classList.contains(styles.header)).toBe(true);
  expect(contentTextOf(lines[0]!)).toBe("---");
  expect(lines[1]!.classList.contains(styles.header)).toBe(true);
  expect(contentTextOf(lines[1]!)).toBe("+++");
});

test('renders a "\\ No newline at end of file" marker with the meta tone, not context', () => {
  const diff = ["@@ -1 +1 @@", "-old", "+new", "\\ No newline at end of file"].join("\n");
  const { container } = render(<DiffBlock unified={diff} />);
  const lines = lineElements(container);
  expect(lines[3]!.classList.contains(styles.meta)).toBe(true);
  expect(lines[3]!.classList.contains(styles.context)).toBe(false);
  expect(contentTextOf(lines[3]!)).toBe("\\ No newline at end of file");
});

test("declares no external diff-parsing dependency (no import beyond React)", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const source = readFileSync(join(here, "index.tsx"), "utf8");
  const importLines = source.split("\n").filter((line) => line.trim().startsWith("import "));
  for (const line of importLines) {
    expect(line).toMatch(/^import .* from "(react|\.\.?\/)/);
  }
});
