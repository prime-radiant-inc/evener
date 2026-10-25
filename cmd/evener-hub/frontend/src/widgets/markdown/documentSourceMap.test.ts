import { render } from "@testing-library/react";
import { type Token, Tokenizer } from "marked";
import { createElement } from "react";
import { afterEach, expect, test } from "vitest";
import { FIRST, RICH_RANGES, RICH_SOURCE, SECOND, SOURCE } from "../../panes/doc/testing/annotationFixtures";
import { DocumentMarkdown } from "./DocumentMarkdown";
import {
  type BoundDocumentMap,
  type DocumentTextRun,
  lexAgainstSourceForTests,
  parseDocument,
  provenanceProofUnitsForTests,
  renderDocument,
  resetProvenanceProofUnitsForTests,
} from "./documentSourceMap";

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
function expectExactLine(run: DocumentTextRun, source: string, line: string) {
  const lineStart = source.indexOf(line);
  const lineIndex = run.sourceSpans.findIndex((span) => span.start === lineStart);
  expect(lineIndex).toBeGreaterThanOrEqual(0);
  expect(run.renderedText.slice(lineIndex, lineIndex + line.length)).toBe(line);
  expect(run.sourceSpans[lineIndex + line.length - 1]?.end).toBe(lineStart + line.length);
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

// A heavily formatted ~200KB document: rendering and mapping must stay
// linear-time. The tokenizer probes every block against the remaining text,
// so slicing that tail's span storage per probe was quadratic - this
// document's render measured 20.5s before slices became offsets into shared
// storage. The timing ratchet's per-test ceiling (testing-budget.json) guards
// the bound; the assertions here pin the mapping's exactness at scale.
test("maps a large heavily formatted document with exact coordinates", () => {
  const blocks: string[] = [];
  for (let i = 0; i < 2000; i++) {
    blocks.push(
      `Paragraph ${i}: **bold ${i}** and *em* with \`code ${i}\` plus [link ${i}](https://example.test/${i}) tail.`,
    );
  }
  const source = `# Large document\n\n${blocks.join("\n\n")}\n\n## Tail\n`;
  const { map, segments } = renderDocument(parseDocument(source), {
    displayedSnapshotId: "snap-large",
    mappingId: "map-large",
  });
  expect(segments.length).toBeGreaterThan(0);
  expect(map.textRuns.length).toBeGreaterThan(4000);
  const bold = map.textRuns.find((run) => run.renderedText === "bold 1000");
  if (!bold) throw new Error("missing middle bold run");
  const boldFirst = bold.sourceSpans[0];
  const boldLast = bold.sourceSpans.at(-1);
  if (!boldFirst || !boldLast) throw new Error("missing bold run spans");
  expect(source.slice(boldFirst.start, boldLast.end)).toBe("bold 1000");
  const tail = map.textRuns.find((run) => run.renderedText === "Tail");
  if (!tail) throw new Error("missing tail heading run");
  const tailFirst = tail.sourceSpans[0];
  const tailLast = tail.sourceSpans.at(-1);
  if (!tailFirst || !tailLast) throw new Error("missing tail heading spans");
  expect(source.slice(tailFirst.start, tailLast.end)).toBe("Tail");
});

// A list item's first line near the 512 KiB document limit runs expandTabs
// piece-per-character, and the transformations assembled those pieces by
// spreading them into concat: past the engine's function-call argument limit
// the spread itself throws while rendering a document that is within the
// size limit (Chrome throws at smaller pieces than Node; the assembly must
// never depend on that limit).
test("maps a giant single-line list item with a tab within the size limit", () => {
  const source = `- ${"a".repeat(500_000)}\tb\n`;
  const { map, segments } = renderDocument(parseDocument(source), {
    displayedSnapshotId: "snap-giant",
    mappingId: "map-giant",
  });
  expect(segments.length).toBeGreaterThan(0);
  const run = map.textRuns.find((entry) => entry.renderedText.includes("a"));
  if (!run) throw new Error("missing giant list item run");
  const first = run.sourceSpans[0];
  const last = run.sourceSpans.at(-1);
  if (!first || !last) throw new Error("missing giant run spans");
  // The item's own text begins after the "- " bullet, and the expanded tab's
  // rendered spaces still map onto the tab's original source units.
  expect(first.start).toBe(2);
  expect(run.renderedText.startsWith("aaaaa")).toBe(true);
  expect(source.slice(first.start, last.end)).toBe(`${"a".repeat(500_000)}\tb`);
});

// A long list item with thousands of lazy continuation lines: every
// continuation check needs only the FIRST line's provenance, but splitting
// the ENTIRE remaining document into lines per check was quadratic - the
// list tokenizer runs synchronously during React rendering, so ordinary
// documents within the 512 KiB limit could stall the browser. (Inline
// markup stays on two anchor lines only: marked's per-paragraph strong
// matching is itself quadratic in delimiter count, which would measure a
// different defect.) The timing ratchet's per-test ceiling
// (testing-budget.json) guards the bound; the assertions here pin the
// mapping's exactness at scale.
test("maps a long list continuation with exact coordinates", () => {
  const lines: string[] = [];
  for (let i = 0; i < 3000; i++) {
    lines.push(`Line ${i} with lazy continuation text.`);
  }
  lines[0] = "Item start with a **bold anchor** head.";
  lines[1500] = "Line 1500 with a **bold 1500** inline.";
  const source = `# List continuation\n\n- ${lines.join("\n")}\n\n## Tail\n`;
  const { map, segments } = renderDocument(parseDocument(source), {
    displayedSnapshotId: "snap-list",
    mappingId: "map-list",
  });
  expect(segments.length).toBeGreaterThan(0);
  const bold = map.textRuns.find((run) => run.renderedText === "bold 1500");
  if (!bold) throw new Error("missing middle bold run");
  const boldFirst = bold.sourceSpans[0];
  const boldLast = bold.sourceSpans.at(-1);
  if (!boldFirst || !boldLast) throw new Error("missing bold run spans");
  expect(source.slice(boldFirst.start, boldLast.end)).toBe("bold 1500");
  const mid = map.textRuns.find((run) => run.renderedText.includes("Line 750 with lazy"));
  if (!mid) throw new Error("missing mid-document text run");
  // The run is the merged item paragraph, so check one interior line's exact
  // source position rather than the whole run's width.
  expectExactLine(mid, source, "Line 750 with lazy continuation text.");
  const tail = map.textRuns.find((run) => run.renderedText === "Tail");
  if (!tail) throw new Error("missing tail heading run");
  const tailFirst = tail.sourceSpans[0];
  const tailLast = tail.sourceSpans.at(-1);
  if (!tailFirst || !tailLast) throw new Error("missing tail heading spans");
  expect(source.slice(tailFirst.start, tailLast.end)).toBe("Tail");
});

// A long list item with thousands of INDENTED continuation lines: the dedent
// slice starts its spans where the accumulated trace ends at the raw line's
// end, so the pieces are never contiguous and every continuation
// re-materialized the ENTIRE accumulated span array - quadratic span copying
// during the list tokenizer's synchronous render. The pieces now accumulate
// and materialize once per item; the ratchet's per-test ceiling guards the
// bound, and the assertions pin the mapping's exactness at scale.
test("maps a long indented list continuation with exact coordinates", () => {
  const lines: string[] = [];
  for (let i = 1; i < 3000; i += 1) {
    lines.push(`    Line ${i} with indented continuation text.`);
  }
  const source = `# Indented continuation\n\n- Item start.\n${lines.join("\n")}\n\n## Tail\n`;
  const { map, segments } = renderDocument(parseDocument(source), {
    displayedSnapshotId: "snap-ind",
    mappingId: "map-ind",
  });
  expect(segments.length).toBeGreaterThan(0);
  const mid = map.textRuns.find((run) => run.renderedText.includes("Line 1500 with indented"));
  if (!mid) throw new Error("missing mid-document text run");
  const midFirst = mid.sourceSpans[0];
  const midLast = mid.sourceSpans.at(-1);
  if (!midFirst || !midLast) throw new Error("missing mid run spans");
  expect(source.slice(midFirst.start, midLast.end)).toContain("Line 1500 with indented continuation text.");
  const tail = map.textRuns.find((run) => run.renderedText === "Tail");
  if (!tail) throw new Error("missing tail heading run");
  const tailFirst = tail.sourceSpans[0];
  const tailLast = tail.sourceSpans.at(-1);
  if (!tailFirst || !tailLast) throw new Error("missing tail heading spans");
  expect(source.slice(tailFirst.start, tailLast.end)).toBe("Tail");
});

// The provenance proof must stay linear in a single frame: the frame's own
// text is compared once, then every token proves its own raw at token size.
// Counting the units of each kind makes a return to the old per-dispatch
// full-frame comparisons a deterministic failure, not a wall-clock race
// (#4579). The 4000-line shape compared 8.1 billion characters before the
// proof moved; this 500-line shape already exceeds the bound below by 412x on
// the old implementation, so the ratchet stays cheap.
test("proves a frame once and each token at token size", () => {
  const lines: string[] = [];
  for (let i = 0; i < 500; i++) lines.push(`Line ${i} with lazy continuation text.`);
  lines[250] = "Line 250 with a **bold 250** inline.";
  const source = `# Proof shape\n\n- ${lines.join("\n")}\n\n## Tail\n`;
  resetProvenanceProofUnitsForTests();
  const parse = parseDocument(source);
  const units = provenanceProofUnitsForTests();
  // Frames are bounded by the document's blocks, not its tokens: a handful of
  // frame texts add up to a small multiple of the source.
  expect(units.fullFrameUnits).toBeLessThan(source.length * 16);
  // Every byte of every frame's text is still verified: tokens account for at
  // least the source's own units (nested frames verify their text again).
  expect(units.tokenUnits).toBeGreaterThanOrEqual(source.length);
  expect(units.tokenUnits).toBeLessThan(source.length * 16);
  const bold = parse.textRuns.find((run) => run.renderedText === "bold 250");
  if (!bold) throw new Error("missing middle bold run");
  const first = bold.sourceSpans[0];
  const last = bold.sourceSpans.at(-1);
  if (!first || !last) throw new Error("missing middle bold run spans");
  expect(source.slice(first.start, last.end)).toBe("bold 250");
});

test("refuses a deliberately corrupted frame remainder", () => {
  expect(() => lexAgainstSourceForTests("same", "same and more")).toThrow();
  expect(() => lexAgainstSourceForTests("same", "sAme")).toThrow();
  expect(() => lexAgainstSourceForTests("same", "same")).not.toThrow();
});

// The per-token proof, not only the frame-entry compare, must refuse a token
// whose consumed raw diverges at the same length. Patch Marked's own tokenizer
// boundary (the pinned dependency) and restore it afterwards.
test("refuses a token whose raw differs from the frame trace", () => {
  const tokenizer = Tokenizer.prototype as unknown as { text(src: string): Token | undefined };
  const original = tokenizer.text;
  tokenizer.text = function (this: unknown, src: string) {
    const token = original.call(this, src);
    if (token && token.raw.length > 0) token.raw = `${token.raw.slice(0, -1)}${token.raw.endsWith("a") ? "b" : "a"}`;
    return token;
  };
  try {
    expect(() => parseDocument("- same\n")).toThrow();
  } finally {
    tokenizer.text = original;
  }
});

// Marked's blockquote tokenizer appends a synthetic newline for a trailing
// empty continuation group, so its token raw is one unit longer than the
// source and blockTokens' substring clamps. The frame cursor must track what
// Marked consumed, not the synthetic raw length (#4579 review).
test("maps a blockquote whose token raw appends a synthetic newline", () => {
  const source = "> - a\nb";
  const { container, map } = documentFor(source);
  expect(map.capture(selectText(container, "b"))).toEqual({
    state: "mapped",
    sourceRange: { start: 6, end: 7 },
    selectedRenderedText: "b",
  });
});

test.each([
  ["> 1. a\nb", 7, 8],
  ["> * a\nb", 6, 7],
] as const)("maps a quote-nested list's lazy continuation in %j", (source, start, end) => {
  const { container, map } = documentFor(source);
  expect(map.capture(selectText(container, "b"))).toEqual({
    state: "mapped",
    sourceRange: { start, end },
    selectedRenderedText: "b",
  });
});

test.each(["> - a\n b", "> - a\n  b", "> - a\n    b", "> - a\nb\nc", "> - a\nb\n\n> - c\nd"])(
  "renders %j without a frame-consumption mismatch",
  (source) => {
    expect(() => parseDocument(source)).not.toThrow();
  },
);

test("maps a list item at end of input without a trailing newline", () => {
  const source = "- first\nsecond";
  const { container, map } = documentFor(source);
  expect(map.capture(selectText(container, "second"))).toEqual({
    state: "mapped",
    sourceRange: { start: 8, end: 14 },
    selectedRenderedText: "second",
  });
});

// A long quoted paragraph is one blockquote frame whose inner paragraph is a
// single inline frame. Moving the proof to frame entry plus token raw must
// keep both coordinates exact.
test("maps a long quoted paragraph with exact coordinates", () => {
  const lines: string[] = [];
  for (let i = 0; i < 2000; i++) lines.push(`> Line ${i} with quoted continuation text.`);
  lines[1000] = "> Line 1000 with a **bold 1000** inline.";
  const source = `# Quote continuation\n\n${lines.join("\n")}\n\n## Tail\n`;
  const { map } = renderDocument(parseDocument(source), { displayedSnapshotId: "snap-quote", mappingId: "map-quote" });
  const bold = map.textRuns.find((run) => run.renderedText === "bold 1000");
  if (!bold) throw new Error("missing quote bold run");
  const boldFirst = bold.sourceSpans[0];
  const boldLast = bold.sourceSpans.at(-1);
  if (!boldFirst || !boldLast) throw new Error("missing quote bold run spans");
  expect(source.slice(boldFirst.start, boldLast.end)).toBe("bold 1000");
  const mid = map.textRuns.find((run) => run.renderedText.includes("Line 500 with quoted"));
  if (!mid) throw new Error("missing quote mid run");
  // The source range spans the dropped "> " prefixes between rendered lines,
  // so locate the line by its own first source unit and check its exact run.
  expectExactLine(mid, source, "Line 500 with quoted continuation text.");
});

// Table cells are separate inline frames nested under one table token; their
// coordinates must survive the frame cursor.
test("maps a long table's cells with exact coordinates", () => {
  const rows = ["| a | b | c |", "| --- | --- | --- |"];
  for (let i = 0; i < 200; i++) rows.push(`| cell ${i}a | **cell ${i}b** | \`cell ${i}c\` |`);
  const source = `# Table\n\n${rows.join("\n")}\n\n## Tail\n`;
  const { map } = renderDocument(parseDocument(source), { displayedSnapshotId: "snap-table", mappingId: "map-table" });
  const bold = map.textRuns.find((run) => run.renderedText === "cell 100b");
  if (!bold) throw new Error("missing table cell run");
  const boldFirst = bold.sourceSpans[0];
  const boldLast = bold.sourceSpans.at(-1);
  if (!boldFirst || !boldLast) throw new Error("missing table cell spans");
  expect(source.slice(boldFirst.start, boldLast.end)).toBe("cell 100b");
  const code = map.textRuns.find((run) => run.renderedText === "cell 100c");
  if (!code) throw new Error("missing table code run");
  const codeFirst = code.sourceSpans[0];
  const codeLast = code.sourceSpans.at(-1);
  if (!codeFirst || !codeLast) throw new Error("missing table code spans");
  expect(source.slice(codeFirst.start, codeLast.end)).toBe("cell 100c");
});

// String.replace interprets $$ and $& in a replacement string; authored code
// must reach the DOM and the provenance map literally.
test("renders and maps literal dollar replacement sequences in code", () => {
  const span = documentFor("pay `$$` now");
  expect(span.container.textContent).toContain("$$");
  expect(span.map.capture(selectText(span.container, "$$"))).toEqual({
    state: "mapped",
    sourceRange: { start: 5, end: 7 },
    selectedRenderedText: "$$",
  });
  const block = documentFor("```\na$$b $&\n```");
  expect(block.container.textContent).toContain("a$$b $&");
  expect(block.map.capture(selectText(block.container, "a$$b $&"))).toEqual({
    state: "mapped",
    sourceRange: { start: 4, end: 11 },
    selectedRenderedText: "a$$b $&",
  });
});

test("a partial selection of a multi-character transformed spelling is refused instead of expanded", () => {
  const entity = documentFor("a &fjlig; b");
  // The full rendered spelling maps and reconstructs exactly.
  expect(entity.container.textContent).toContain("fj");
  const full = entity.map.capture(selectText(entity.container, "fj"));
  expect(full).toMatchObject({ state: "mapped", selectedRenderedText: "fj" });
  if (full.state !== "mapped") throw new Error("the full spelling should map");
  expect(
    entity.map
      .rangesFor(full.sourceRange)
      .map((range) => range.toString())
      .join(""),
  ).toBe("fj");
  // Selecting only its first character must not save a source range that
  // reconstructs BOTH characters - a highlight beyond what was selected. The
  // boundary cannot round-trip, so the capture refuses it.
  const partial = entity.map.capture(selectText(entity.container, "f"));
  expect(partial).toEqual({ state: "unmapped", reason: "transformed" });
});
