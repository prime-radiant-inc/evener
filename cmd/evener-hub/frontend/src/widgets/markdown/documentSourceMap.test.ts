import { render } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, expect, test } from "vitest";
import { FIRST, RICH_RANGES, RICH_SOURCE, SECOND, SOURCE } from "../../panes/doc/testing/annotationFixtures";
import { DocumentMarkdown } from "./DocumentMarkdown";
import type { BoundDocumentMap } from "./documentSourceMap";

afterEach(() => window.getSelection()?.removeAllRanges());
function documentFor(source: string) {
  let bound: BoundDocumentMap | null = null;
  const view = render(
    createElement(DocumentMarkdown, {
      source,
      identity: { displayedSnapshotId: "s", mappingId: "m" },
      onMap: (value) => {
        bound = value;
      },
    }),
  );
  if (!bound) throw new Error("document not bound");
  return { ...view, map: bound as BoundDocumentMap };
}
// Quote searches are only test input construction, never the production mapper.
function selectText(container: HTMLElement, text: string, occurrence = 0) {
  const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  let total = "";
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    nodes.push(n as Text);
    total += n.textContent;
  }
  let start = -1;
  for (let i = 0; i <= occurrence; i++) start = total.indexOf(text, start + 1);
  if (start < 0) throw new Error(`rendered text unavailable: ${JSON.stringify(text)} in ${JSON.stringify(total)}`);
  let offset = 0;
  const range = document.createRange();
  let started = false;
  for (const node of nodes) {
    const end = offset + node.length;
    if (!started && start < end) {
      range.setStart(node, start - offset);
      started = true;
    }
    if (start + text.length <= end && started) {
      range.setEnd(node, start + text.length - offset);
      break;
    }
    offset = end;
  }
  const selection = window.getSelection();
  if (!selection) throw new Error("selection unavailable");
  selection.removeAllRanges();
  selection.addRange(range);
  return selection;
}
// A mapper that rebases normalized input, searches duplicates or ignores decode
// spellings fails these independent literal original-coordinate expectations.
test.each([
  ["same same.", "same", 1, 5, 9],
  ["same\n\nsame\n", "same", 1, 6, 10],
  ["A **same** and same.", "same", 0, 4, 8],
  ["A **same** and same.", "same and same", 0, 4, 19],
  ["[go &amp; now](x) \\*ok\\*", "& now", 0, 4, 13],
  ["[go &amp; now](x) \\*ok\\*", "*ok*", 0, 18, 24],
  ["\ufeff# H\r\n\r\n\t😀e\u0301\r\n", "😀e\u0301", 0, 9, 13],
  ["\ufeff# H\r\n\r\n\t😀e\u0301\r\n", "😀e\u0301\n", 0, 9, 15],
  ["```js\r\n\t😀x\r\nz\r\n```", "\t😀x\nz", 0, 7, 14],
  ["| H | H |\n| --- | --- |\n| a\\|b | 😀 |\n", "a|b", 0, 26, 30],
  ["` a\r\nb `", "a b", 0, 2, 6],
  ["a\r\nb", "\n", 0, 1, 3],
  ["a\rb", "\n", 0, 1, 2],
  ["before **partial** after", "art", 0, 10, 13],
  ["&amp;\\*&copy;&#x1f600;", "&*©😀", 0, 0, 22],
  ["- one\n\n> two\n", "one", 0, 2, 5],
  ["> same\n>\n> - **same**\n>   - same\n", "same", 2, 28, 32],
  ["- one\n\n  same\n\n- same\n", "same", 1, 17, 21],
  ["- [x] same\n- [ ] same\n", "same", 1, 17, 21],
  ["  ```js\n  same\n  ```", "same", 0, 10, 14],
  ["> - same\nlazy\n", "lazy", 0, 9, 13],
  ["# same ###\n", "same", 0, 2, 6],
  ["same\n===\n", "same", 0, 0, 4],
  ["<script>x()</script>", "<script>", 0, 0, 8],
  ["<code>&amp;</code>", "&", 0, 6, 11],
  ["<code>&copy</code>", "©", 0, 6, 11],
  ["a <b>same</b> z", "same", 0, 5, 9],
] as const)("maps literal %j / %j", (source, text, occurrence, start, end) => {
  const { container, map } = documentFor(source);
  const selection = selectText(container, text, occurrence);
  expect(map.capture(selection)).toEqual({ state: "mapped", sourceRange: { start, end }, selectedRenderedText: text });
  const ranges = map.rangesFor({ start, end });
  expect(ranges.map((r) => r.toString()).join("")).toBe(text);
});

test.each([
  ["> same\nlazy\n", "lazy", 7, 11],
  ["> same\nlazy\n", "same\nlazy", 2, 11],
  ["> same\nlazy\n", "\n", 6, 7],
  ["> same\r\nlazy\r\n", "lazy", 8, 12],
  ["> same\r\nlazy\r\n", "same\nlazy", 2, 12],
  ["> same\r\nlazy\r\n", "\n", 6, 8],
] as const)("maps lazy quote literal %j / %j", (source, text, start, end) => {
  const { container, map } = documentFor(source);
  const paragraph = container.querySelector<HTMLParagraphElement>("blockquote p");
  if (!paragraph) throw new Error("missing quote paragraph");
  expect(map.capture(selectText(paragraph, text))).toEqual({
    state: "mapped",
    sourceRange: { start, end },
    selectedRenderedText: text,
  });
  expect(
    map
      .rangesFor({ start, end })
      .map((range) => range.toString())
      .join(""),
  ).toBe(text);
});

test("keeps both independent SOURCE occurrences", () => {
  const { container, map } = documentFor(SOURCE);
  expect(map.capture(selectText(container, "same"))).toMatchObject({ state: "mapped", sourceRange: FIRST });
  expect(map.capture(selectText(container, "same", 1))).toMatchObject({ state: "mapped", sourceRange: SECOND });
});
test("keeps rich CRLF coordinates across five actual rendered blocks", () => {
  const { container, map } = documentFor(RICH_SOURCE);
  for (const [i, range] of RICH_RANGES.entries())
    expect(map.capture(selectText(container, "same", i))).toMatchObject({ state: "mapped", sourceRange: range });
  expect(map.capture(selectText(container, "link"))).toMatchObject({
    state: "mapped",
    sourceRange: { start: 26, end: 30 },
  });
  expect(map.capture(selectText(container, "&"))).toMatchObject({
    state: "mapped",
    sourceRange: { start: 54, end: 59 },
  });
  expect(map.capture(selectText(container, "😀"))).toMatchObject({
    state: "mapped",
    sourceRange: { start: 60, end: 62 },
  });
  const first = map.rangesFor({ start: 18, end: 22 })[0];
  const last = map.rangesFor({ start: 73, end: 77 })[0];
  if (!first || !last) throw new Error("missing fragments");
  const range = document.createRange();
  range.setStart(first.startContainer, first.startOffset);
  range.setEnd(last.endContainer, last.endOffset);
  const s = window.getSelection();
  if (!s) throw new Error("missing selection");
  s.removeAllRanges();
  s.addRange(range);
  expect(map.capture(s)).toEqual({
    state: "mapped",
    sourceRange: { start: 18, end: 77 },
    selectedRenderedText: s.toString(),
  });
});
test("preserves the actual list-to-quote Selection string", () => {
  const { container, map } = documentFor("- one\n\n> two\n");
  const li = container.querySelector("li");
  const p = container.querySelector("blockquote p");
  if (!li || !p) throw new Error("missing blocks");
  const r = document.createRange();
  r.setStart(li, 0);
  r.setEnd(p, p.childNodes.length);
  const s = window.getSelection();
  if (!s) throw new Error("no selection");
  s.removeAllRanges();
  s.addRange(r);
  expect(map.capture(s)).toEqual({
    state: "mapped",
    sourceRange: { start: 2, end: 12 },
    selectedRenderedText: s.toString(),
  });
});
test("rejects surrogate splits and gives indentation no visible fragment", () => {
  const { container, map } = documentFor("\ufeff# H\r\n\r\n\t😀e\u0301\r\n");
  const s = selectText(container, "😀");
  const r = s.getRangeAt(0);
  r.setEnd(r.startContainer, r.startOffset + 1);
  s.removeAllRanges();
  s.addRange(r);
  expect(map.capture(s)).toEqual({ state: "unmapped", reason: "unverified" });
  expect(map.rangesFor({ start: 8, end: 9 })).toEqual([]);
  expect(map.rangesFor({ start: 9, end: 10 })).toEqual([]);
});
test("rejects mutated or removed verified text rather than trusting attributes", () => {
  const { container, map } = documentFor("same");
  const s = selectText(container, "same");
  const text = s.anchorNode;
  if (!text) throw new Error("no text");
  text.textContent = "xxxx";
  expect(map.capture(selectText(container, "xxxx"))).toEqual({ state: "unmapped", reason: "unverified" });
  expect(map.rangesFor({ start: 0, end: 4 })).toEqual([]);
  text.parentNode?.removeChild(text);
  expect(map.rangesFor({ start: 0, end: 4 })).toEqual([]);
});
test("rejects outside, empty and disposed selections", () => {
  const { container, map } = documentFor("same");
  const outside = document.createTextNode("other");
  document.body.append(outside);
  const r = document.createRange();
  r.selectNodeContents(outside);
  const s = window.getSelection();
  if (!s) throw new Error("no selection");
  s.removeAllRanges();
  s.addRange(r);
  expect(map.capture(s)).toEqual({ state: "unmapped", reason: "outside" });
  outside.remove();
  s.removeAllRanges();
  expect(map.capture(s).state).toBe("unmapped");
  selectText(container, "same");
  map.dispose();
  expect(map.capture(s)).toEqual({ state: "unmapped", reason: "stale" });
  expect(map.rangesFor({ start: 0, end: 4 })).toEqual([]);
});
