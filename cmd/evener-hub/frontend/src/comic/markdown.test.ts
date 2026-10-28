import { expect, test } from "vitest";
import { projectComicMarkdown } from "./markdown";

const source = "**before**\n\n```ts\n  a\n\tb\n```\n\n| A | B |\n| - | - |\n| 1 | 2 |\n\nafter";

test("parts retain every original UTF-16 unit in prose, code, table, prose order", () => {
  const document = projectComicMarkdown(source, false);
  expect(document.source).toBe(source);
  expect(document.parts.map((part) => part.kind)).toEqual(["prose", "code", "table", "prose"]);
  expect(document.parts.map((part) => source.slice(part.start, part.end)).join("")).toBe(source);
  expect(document.parts[1]?.code).toBe("  a\n\tb");
  expect(document.parts[1]?.language).toBe("ts");
});

test("normalization maps nested labels, repeated text, entities, escapes and astral text to original offsets", () => {
  const raw = "# **same [same](https://x.test) same**\r\n\r\n&amp; \\* 😀\tZ";
  const document = projectComicMarkdown(raw, false);
  const runs = document.parts.flatMap((part) => part.visible);
  expect(runs.map((run) => run.text).join("")).toBe("same same same& * 😀\tZ");
  expect(runs.flatMap((run) => run.offsets)).toEqual([
    4, 5, 6, 7, 8, 10, 11, 12, 13, 31, 32, 33, 34, 35, 42, 47, 49, 50, 51, 52, 53, 54,
  ]);
});

test("nested list and quote containers split around inserts while retaining source coverage", () => {
  const raw = "> - before\n>\n>   ```ts\n>   same\n>   ```\n>\n>   **after**\n";
  const document = projectComicMarkdown(raw, false);
  expect(document.parts.map((part) => part.kind)).toEqual(["prose", "code", "prose"]);
  expect(document.parts.map((part) => raw.slice(part.start, part.end)).join("")).toBe(raw);
  expect(document.parts[1]?.code).toBe("same");
  expect(document.parts[1]?.visible[0]?.offsets).toEqual([27, 28, 29, 30]);
  expect(document.parts[2]?.visible[0]?.offsets).toEqual([48, 49, 50, 51, 52]);
});

test("code-only and language-tagged diagram fences never produce empty prose or diagrams", () => {
  for (const language of ["", "ts", "mermaid"]) {
    const raw = `\n\n\`\`\`${language}\nA --> B\n\`\`\`\n\n`;
    const document = projectComicMarkdown(raw, false);
    expect(document.parts.map((part) => part.kind)).toEqual(["code"]);
    expect(document.parts[0]?.start).toBe(0);
    expect(document.parts[0]?.end).toBe(raw.length);
    expect(document.parts[0]?.code).toBe("A --> B");
  }
});

test("every live prefix partitions its own input and settles without stale parsing", () => {
  for (let end = 0; end <= source.length; end++) {
    const raw = source.slice(0, end);
    const document = projectComicMarkdown(raw, true);
    expect(document.parts.map((part) => raw.slice(part.start, part.end)).join("")).toBe(raw);
  }
  const changed = source.replace("```ts", "```python").replace("  a", "  altered");
  const document = projectComicMarkdown(changed, false);
  expect(document.parts[1]?.language).toBe("python");
  expect(document.parts[1]?.code).toBe("  altered\n\tb");
});

test("task-list checkbox syntax never consumes the following visible text offsets", () => {
  const raw = "- [x] one\n- **two**\n";
  const runs = projectComicMarkdown(raw, false).parts.flatMap((part) => part.visible);
  expect(runs.map((run) => run.text).join("")).toBe("onetwo");
  expect(runs.flatMap((run) => run.offsets)).toEqual([6, 7, 8, 14, 15, 16]);
});

test("table cell maps retain CRLF, entities and astral UTF-16 identity", () => {
  const raw = "| A | B |\r\n| - | - |\r\n| &amp; | 😀 |\r\n";
  const runs = projectComicMarkdown(raw, false).parts.flatMap((part) => part.visible);
  expect(runs.map((run) => run.text).join("")).toBe("AB&😀");
  expect(runs.flatMap((run) => run.offsets)).toEqual([2, 6, 24, 32, 33]);
});

test("tab-prefixed quotes and indented code retain their visible source positions", () => {
  const quoted = ">\t**same**\n>\t[same](/same)\n";
  const runs = projectComicMarkdown(quoted, false).parts.flatMap((part) => part.visible);
  expect(runs.flatMap((run) => run.offsets)).toEqual([4, 5, 6, 7, 10, 14, 15, 16, 17]);
  const code = projectComicMarkdown("    a\n\t b\n", false).parts[0];
  expect(code?.code).toBe("a\n b\n");
  expect(code?.visible[0]?.offsets).toEqual([4, 5, 7, 8, 9]);
});
