import { render } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { ComicMarkdown, comicMarkdownPoint } from "./ComicMarkdown";
import { projectComicMarkdown } from "./markdown";

function formatted(source: string, live = false) {
  const document = projectComicMarkdown(source, live);
  return document.parts.map((part) => <ComicMarkdown key={part.start} document={document} part={part} />);
}

// jsdom has no glyph geometry. Record the REAL Range endpoints at the platform boundary.
function ranges(doc: Document) {
  const create = doc.createRange.bind(doc);
  let last: Range | undefined;
  const spy = vi.spyOn(doc, "createRange").mockImplementation(() => {
    const range = create();
    range.getBoundingClientRect = () => {
      last = range;
      return new DOMRect(0, 0, 1, 1);
    };
    return range;
  });
  return {
    spy,
    get last() {
      return last;
    },
  };
}

test("source offsets identify exact DOM nodes and positions, including repeated nested labels", () => {
  const source = "# **same [same](https://x.test) same**\r\n\r\n&amp; \\* 😀\tZ";
  const { container } = render(formatted(source));
  const probe = ranges(container.ownerDocument);
  const strong = container.querySelector("strong");
  const link = container.querySelector("a");
  const paragraph = container.querySelector("p");
  const expected = [
    [4, strong?.firstChild, 0],
    [7, strong?.firstChild, 3],
    [10, link?.firstChild, 0],
    [13, link?.firstChild, 3],
    [32, strong?.lastChild, 1],
    [35, strong?.lastChild, 4],
    [42, paragraph?.firstChild, 0],
    [49, paragraph?.firstChild, 2],
    [51, paragraph?.firstChild, 4],
    [54, paragraph?.firstChild, 7],
    // Hidden heading / emphasis delimiters, link destination and entity spelling.
    [0, strong?.firstChild, 0],
    [9, link?.firstChild, 0],
    [21, link?.firstChild, 3],
    [44, paragraph?.firstChild, 0],
  ] as const;
  try {
    for (const [offset, node, local] of expected) {
      expect(node).toBeTruthy();
      comicMarkdownPoint(container, offset);
      expect(probe.last?.startContainer, `source ${offset}`).toBe(node);
      expect(probe.last?.startOffset, `source ${offset}`).toBe(local);
    }
    // Reverse oracle: these DOM text positions correspond to independent original UTF-16 positions.
    for (const [sourceOffset, node, local] of expected.slice(0, 10)) {
      expect(node?.textContent?.codePointAt(local)).toBe(source.codePointAt(sourceOffset));
    }
  } finally {
    probe.spy.mockRestore();
  }
});

test("whole-document references resolve on both sides of a code insert without rendering definitions", () => {
  const source = "[same][r]\n\n```txt\nsame\n```\n\n[same][r]\n\n[r]: https://example.com\n";
  const { container } = render(formatted(source));
  expect([...container.querySelectorAll("a")].map((a) => a.getAttribute("href"))).toEqual([
    "https://example.com",
    "https://example.com",
  ]);
  expect(container.querySelectorAll("p")).toHaveLength(2);
  expect([...container.querySelectorAll("p")].map((p) => p.textContent)).toEqual(["same", "same"]);
});

test("a table nested in a list stays semantic and resolves references defined after the list", () => {
  const source =
    "- before\n\n  | A | B |\n  | - | - |\n  | [one][r] | two |\n\n  after\n\n[r]: https://example.com/table";
  const doc = projectComicMarkdown(source, false);
  expect(doc.parts.map((part) => part.kind)).toEqual(["prose", "table", "prose"]);
  expect(doc.parts.map((part) => source.slice(part.start, part.end)).join("")).toBe(source);
  const { container } = render(formatted(source));
  expect(container.querySelectorAll("ul li table")).toHaveLength(1);
  expect(container.querySelector("td a")?.getAttribute("href")).toBe("https://example.com/table");
  expect(container.querySelectorAll("[data-comic-markdown='prose'] ul li")).toHaveLength(2);
});

test("nested list and quote prose keeps its containers around character-free block boundaries", () => {
  const source = "> - before\n>\n>   ```ts\n>   same\n>   ```\n>\n>   **after**\n";
  const { container } = render(formatted(source));
  expect(container.querySelectorAll("blockquote ul li")).toHaveLength(2);
  expect(container.querySelector("pre code")?.textContent).toBe("same");
  expect(container.querySelector("blockquote strong")?.textContent).toBe("after");
  const codeRoot = container.querySelector<HTMLElement>("[data-comic-markdown='code']");
  if (!codeRoot) throw new Error("missing code");
  const probe = ranges(container.ownerDocument);
  try {
    comicMarkdownPoint(codeRoot, 29);
    expect(probe.last?.startContainer.textContent).toBe("same");
    expect(probe.last?.startOffset).toBe(2);
  } finally {
    probe.spy.mockRestore();
  }
});

test("safe shared rendering escapes authored HTML and strips unsafe destinations while preserving selectable text", () => {
  const html = "<img src=x onerror=alert(1)>";
  const source = `${html}\n\n[safe](https://example.com) [unsafe](javascript:alert%281%29)`;
  const { container } = render(formatted(source));
  expect(container.querySelectorAll("img, script, [onerror]")).toHaveLength(0);
  expect(container.textContent).toContain(html);
  expect([...container.querySelectorAll("a")].map((a) => a.getAttribute("href"))).toEqual([
    "https://example.com",
    null,
  ]);
  const selection = container.ownerDocument.getSelection();
  const range = container.ownerDocument.createRange();
  range.selectNodeContents(container);
  selection?.removeAllRanges();
  selection?.addRange(range);
  expect(selection?.toString()).toBe(container.textContent);
  selection?.removeAllRanges();
});

test("live fence and table prefixes settle and document edits replace prior markup", () => {
  const rendered = render(formatted("```ts\n  x", true));
  expect(rendered.container.querySelector("code")?.textContent).toBe("  x");
  rendered.rerender(formatted("```ts\n  x\n```\n\n| A | B |\n| - |", true));
  expect(rendered.container.querySelectorAll("code")).toHaveLength(1);
  rendered.rerender(formatted("```ts\n  x\n```\n\n| A | B |\n| - | - |\n| 1 | 2 |", false));
  expect(rendered.container.querySelectorAll("table")).toHaveLength(1);
  expect(rendered.container.querySelectorAll("code")).toHaveLength(1);
  rendered.rerender(formatted("# Replaced\n\n**new**", false));
  expect(rendered.container.querySelectorAll("table, code")).toHaveLength(0);
  expect(rendered.container.querySelectorAll("h1, strong")).toHaveLength(2);
});

test("an unchanged formatted rerender maps to connected glyphs rather than replaced widget text", () => {
  const source = "literal fixture *text*";
  const document = projectComicMarkdown(source, false);
  const part = document.parts[0];
  if (!part) throw new Error("missing part");
  const view = () => <ComicMarkdown document={document} part={part} />;
  const rendered = render(view());
  const probe = ranges(rendered.container.ownerDocument);
  try {
    comicMarkdownPoint(rendered.container, 7);
    expect(rendered.container.contains(probe.last?.startContainer ?? null)).toBe(true);
    rendered.rerender(view());
    comicMarkdownPoint(rendered.container, 7);
    expect(rendered.container.contains(probe.last?.startContainer ?? null)).toBe(true);
  } finally {
    probe.spy.mockRestore();
  }
});

test("table and reference-definition offsets resolve in the element's owning document", () => {
  const frame = document.createElement("iframe");
  document.body.append(frame);
  const foreign = frame.contentDocument;
  if (!foreign) throw new Error("missing frame document");
  const host = foreign.createElement("div");
  foreign.body.append(host);
  const source = "| A | B |\r\n| - | - |\r\n| &amp; | 😀 |\r\n";
  const rendered = render(formatted(source), { container: host });
  const probe = ranges(foreign);
  const globalRange = vi.spyOn(document, "createRange");
  try {
    const cells = host.querySelectorAll("th, td");
    for (const [offset, cell, local] of [
      [2, 0, 0],
      [6, 1, 0],
      [24, 2, 0],
      [32, 3, 0],
    ] as const) {
      comicMarkdownPoint(host, offset);
      expect(probe.last?.startContainer).toBe(cells[cell]?.firstChild);
      expect(probe.last?.startOffset).toBe(local);
      expect(probe.last?.startContainer.ownerDocument).toBe(foreign);
    }
    expect(globalRange).not.toHaveBeenCalled();
    rendered.rerender(formatted("[same][r]\n\n[r]: /destination"));
    comicMarkdownPoint(host, 18);
    expect(probe.last?.startContainer).toBe(host.querySelector("a")?.firstChild);
    expect(probe.last?.startOffset).toBe(3);
  } finally {
    globalRange.mockRestore();
    probe.spy.mockRestore();
    rendered.unmount();
    frame.remove();
  }
});
