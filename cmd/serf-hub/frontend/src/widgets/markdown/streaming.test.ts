// closeOpenMarkdown is a PURE function (no DOM), so these are plain unit
// tests. The Markdown widget's `live` prop integration is covered in
// markdown.test.tsx; the two live call sites (AgentMessageItem, ThinkBlock)
// cover it end-to-end in their own test files.

import { expect, test } from "vitest";
import { closeOpenMarkdown } from "./streaming";

// --- already-balanced sources pass through untouched -------------------------

test("leaves a source with no markdown markers unchanged", () => {
  expect(closeOpenMarkdown("plain prose, nothing to close")).toBe("plain prose, nothing to close");
});

test("leaves balanced emphasis, code, and fences unchanged", () => {
  const source = "**bold** and *italic* and `code` and ~~gone~~\n\n```js\nconst x = 1;\n```";
  expect(closeOpenMarkdown(source)).toBe(source);
});

// --- inline emphasis / code at the stream tail -------------------------------

test("closes an unterminated ** at the end of the stream", () => {
  expect(closeOpenMarkdown("the answer is **bo")).toBe("the answer is **bo**");
});

test("closes an unterminated * at the end of the stream", () => {
  expect(closeOpenMarkdown("an *ital")).toBe("an *ital*");
});

test("closes an unterminated ~~ at the end of the stream", () => {
  expect(closeOpenMarkdown("~~str")).toBe("~~str~~");
});

test("closes an unterminated inline code span at the end of the stream", () => {
  expect(closeOpenMarkdown("run `serf bui")).toBe("run `serf bui`");
});

test("a double-backtick code span closes with a matching double backtick", () => {
  expect(closeOpenMarkdown("``code with ` tick")).toBe("``code with ` tick``");
});

test("closes nested opens in reverse order so the result parses as nested emphasis", () => {
  // "**a *b" opened strong then em; the em closes first.
  expect(closeOpenMarkdown("**a *b")).toBe("**a *b***");
});

test("an opener closed mid-stream is not closed again", () => {
  expect(closeOpenMarkdown("**bold** trailing")).toBe("**bold** trailing");
});

// --- markers that are NOT opens must not be closed ---------------------------

test("a bullet list marker is not emphasis and is left alone", () => {
  expect(closeOpenMarkdown("* first item\n* second")).toBe("* first item\n* second");
});

test("a lone * surrounded by whitespace (math, a footnote star) is not emphasis", () => {
  expect(closeOpenMarkdown("2 * 3 and 4 * 5")).toBe("2 * 3 and 4 * 5");
});

test("an escaped \\* is literal text, never an opener", () => {
  expect(closeOpenMarkdown("escaped \\* star")).toBe("escaped \\* star");
});

test("markers inside a closed code span do not count", () => {
  expect(closeOpenMarkdown("`a ** b` trailing")).toBe("`a ** b` trailing");
});

test("markers inside the OPEN code span at the tail do not count - the span closes first", () => {
  expect(closeOpenMarkdown("`a ** b")).toBe("`a ** b`");
});

// --- block boundaries ---------------------------------------------------------

test("emphasis left open across a blank line is dead (markdown emphasis cannot span blocks) - nothing closes", () => {
  expect(closeOpenMarkdown("**abandoned\n\nnew paragraph")).toBe("**abandoned\n\nnew paragraph");
});

test("emphasis spanning a soft-wrapped line inside one paragraph still closes", () => {
  expect(closeOpenMarkdown("**bold\ntext")).toBe("**bold\ntext**");
});

// --- fenced code blocks --------------------------------------------------------

test("an unterminated fenced code block gets its closing fence on a new line", () => {
  expect(closeOpenMarkdown("```js\nconst x = 1;")).toBe("```js\nconst x = 1;\n```");
});

test("an unterminated tilde fence closes with tildes", () => {
  expect(closeOpenMarkdown("~~~\ncode")).toBe("~~~\ncode\n~~~");
});

test("markers inside an open fence are code, not markdown - only the fence closes", () => {
  expect(closeOpenMarkdown("```\n**not bold**")).toBe("```\n**not bold**\n```");
});

test("emphasis opened before a fence began died at the block boundary - only the fence closes", () => {
  expect(closeOpenMarkdown("**before\n```\ncode")).toBe("**before\n```\ncode\n```");
});

test("a fence closed mid-stream is not closed again", () => {
  expect(closeOpenMarkdown("```\ncode\n```\nafter **bo")).toBe("```\ncode\n```\nafter **bo**");
});

test("a closing fence with a non-whitespace suffix remains code content", () => {
  const source = "```\n````js\ncode";
  expect(closeOpenMarkdown(source)).toBe(`${source}\n\`\`\``);
});

test("a fence indented four spaces is not treated as a fenced block", () => {
  const source = "    ```\ncode";
  expect(closeOpenMarkdown(source)).toBe(source);
});

test("an abandoned emphasis opener does not close across a heading", () => {
  const source = "**abandoned\n# heading";
  expect(closeOpenMarkdown(source)).toBe(source);
});

test("an abandoned emphasis opener does not close across a list", () => {
  const source = "**abandoned\n- item";
  expect(closeOpenMarkdown(source)).toBe(source);
});

test("indented continuation text stays in the active paragraph", () => {
  const source = "**bold\n    continuation";
  expect(closeOpenMarkdown(source)).toBe(`${source}**`);
});

test("emphasis continues across consecutive blockquote lines", () => {
  const source = "> **bold\n> continuation";
  expect(closeOpenMarkdown(source)).toBe(`${source}**`);
});

test("a lazy blockquote continuation keeps the quoted paragraph active", () => {
  const source = "> **bold\ncontinuation\n> end";
  expect(closeOpenMarkdown(source)).toBe(`${source}**`);
});

test.each(["> child", "- child", "code"])("an indented nested %s block ends the parent list paragraph", (nested) => {
  const source = `- **parent\n    ${nested}`;
  expect(closeOpenMarkdown(source)).toBe(source);
});

test("a quoted fence clears emphasis and closes only with the same quote depth", () => {
  const source = "> **abandoned\n> ```\n> **code**\n> ```";
  expect(closeOpenMarkdown(source)).toBe(source);
});

test("an open quoted fence receives a quoted closing fence", () => {
  const source = "> ```\n> code";
  expect(closeOpenMarkdown(source)).toBe(`${source}\n> \`\`\``);
});

test("spaced nested quote markers do not leak emphasis into the parent quote", () => {
  const source = ">   > **nested\n> parent";
  expect(closeOpenMarkdown(source)).toBe(source);
});

test("an abandoned emphasis opener does not close across a setext heading underline", () => {
  const source = "**abandoned\n===";
  expect(closeOpenMarkdown(source)).toBe(source);
});

// --- documented non-goals ------------------------------------------------------

test("underscore emphasis is NEVER auto-closed (snake_case would false-positive constantly)", () => {
  expect(closeOpenMarkdown("some _under")).toBe("some _under");
});
