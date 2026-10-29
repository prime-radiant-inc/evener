import assert from "node:assert/strict";
import { test } from "node:test";
import * as guard from "./comicguard/run.mjs";

const transition = {
  width: 850,
  availableWidth: 835,
  rootFontSize: 16,
  readerLeft: 15,
  readerRight: 850,
  single: { panel: 416, probe: 416, left: 15, right: 431 },
  paired: { rowId: "real-pair-1", panel: 410, probe: 410, left: 15, firstRight: 425, secondLeft: 440, right: 850 },
};

test("rejects a paired panel checked against a reused single probe", () => {
  assert.throws(
    () => guard.verifyPanelWidths({ ...transition, paired: { ...transition.paired, probe: 416 } }, "transition"),
    { name: "Error" },
  );
});

test("accepts independently measured single and real paired geometry at the transition width", () => {
  assert.doesNotThrow(() => guard.verifyPanelWidths(transition, "transition", true));
  assert.throws(() => guard.verifyPanelWidths({ ...transition, paired: null }, "transition", true), { name: "Error" });
});

test("requires the canonical error at mounted width and mode checkpoints", () => {
  const id = "provider-item:turn-error";
  assert.doesNotThrow(() =>
    guard.verifyCanonicalErrorMounted({ mounted: 3, parts: [{ entryId: id }], errorGroups: [] }, id, "width-390"),
  );
  assert.throws(
    () =>
      guard.verifyCanonicalErrorMounted(
        { mounted: 3, parts: [{ entryId: "another-item" }], errorGroups: [] },
        id,
        "plain-mode",
      ),
    { name: "Error" },
  );
});

test("rejects a duplicate source range owned by a different virtual row", () => {
  const covered = new Map();
  const first = { rowId: "row-a", entryId: "source", start: 0, end: 4, text: "abcd" };
  const duplicate = { ...first, rowId: "row-b" };
  assert.doesNotThrow(() => guard.collectSourceSlices(covered, [first], "abcd"));
  assert.doesNotThrow(() => guard.collectSourceSlices(covered, [first], "abcd"));
  assert.throws(() => guard.collectSourceSlices(covered, [duplicate], "abcd"), { name: "Error" });
  assert.equal(covered.size, 1);
});

test("formatted fixture coverage retains raw UTF16 ranges while checking rendered glyphs", () => {
  const source = "A *word* and `code` 😀.";
  const part = {
    rowId: "formatted",
    entryId: "source",
    start: 0,
    end: source.length,
    formatted: true,
    text: "A word and code 😀.\n",
  };
  const covered = new Map();
  assert.doesNotThrow(() => guard.collectSourceSlices(covered, [part], source));
  assert.throws(() => guard.collectSourceSlices(new Map(), [{ ...part, text: source }], source), Error);
  assert.throws(() => guard.collectSourceSlices(covered, [{ ...part, rowId: "duplicate" }], source), Error);
});

test("independent inline fixture oracle pins visible UTF16 offsets, not Markdown delimiters", () => {
  const source = "😀 *word* and `code`";
  const glyphs = guard.inlineFixtureGlyphs(source);
  assert.equal(glyphs.map((glyph) => glyph.char).join(""), "😀 word and code");
  assert.equal(glyphs.find((glyph) => glyph.char === "w").source, 4);
  assert.equal(glyphs.find((glyph) => glyph.char === "c").source, 15);
});

const inserts = () => ({
  width: 390,
  rootFontSize: 16,
  availableWidth: 360,
  overflow: 0,
  kinds: ["prose", "code", "table", "prose"],
  semantics: {
    heading: true,
    strong: true,
    emphasis: true,
    quote: true,
    listStart: 7,
    inlineCode: true,
    safeLink: true,
    unsafeLinks: 0,
  },
  code: {
    text: "  alpha\n\tbeta  ",
    width: 360,
    height: 200,
    clientHeight: 200,
    scrollHeight: 200,
    scrollWidth: 900,
    clientWidth: 330,
    scrollLeft: 400,
    stage: false,
    size: 14,
  },
  table: {
    cells: ["one", "two"],
    width: 360,
    height: 200,
    clientHeight: 200,
    scrollHeight: 200,
    scrollWidth: 900,
    clientWidth: 330,
    scrollLeft: 400,
    stage: false,
    size: 18,
  },
});
const insertExpected = { code: "  alpha\n\tbeta  ", cells: ["one", "two"] };
test("formatted boundaries reject shifted or duplicated original source ranges", () => {
  const source = "before\n\n```text\n  alpha\n\tbeta  \n```\n\n| one | two |\n| --- | --- |\n\nafter";
  const code = source.indexOf("```text");
  const table = source.indexOf("| one");
  const after = source.indexOf("after");
  const value = {
    ...inserts(),
    parts: [
      { start: 0, end: code, kind: "prose" },
      { start: code, end: table, kind: "code" },
      { start: table, end: after, kind: "table" },
      { start: after, end: source.length, kind: "prose" },
    ],
  };
  const expected = { ...insertExpected, source, tableStart: table, proseStart: after };
  assert.doesNotThrow(() => guard.verifyFormattedObservation(value, expected));
  for (const mutation of [(v) => v.parts[1].start++, (v) => (v.parts[2].start = code), (v) => v.parts[3].end--]) {
    const bad = structuredClone(value);
    mutation(bad);
    assert.throws(() => guard.verifyFormattedObservation(bad, expected), Error);
  }
});
test("accepts complete semantic inserts with usable horizontal scrolling", () => {
  assert.doesNotThrow(() => guard.verifyFormattedObservation(inserts(), insertExpected));
});
for (const [name, mutate] of [
  ["wrong source order", (v) => v.kinds.reverse()],
  ["literal Markdown", (v) => (v.semantics.strong = false)],
  ["absent leading heading", (v) => (v.semantics.heading = false)],
  ["unsafe link", (v) => (v.semantics.unsafeLinks = 1)],
  ["lost ordered-list start", (v) => (v.semantics.listStart = 1)],
  ["code whitespace loss", (v) => (v.code.text = v.code.text.trim())],
  ["table cell omission", (v) => v.table.cells.pop()],
  ["character stage", (v) => (v.code.stage = true)],
  ["stretched insert", (v) => (v.code.width = 641)],
  ["nested vertical reader", (v) => (v.code.scrollHeight = 400)],
  ["unreadable horizontal overflow", (v) => (v.table.scrollLeft = 0)],
  ["shrunk code", (v) => (v.code.size = 8)],
  ["page overflow", (v) => (v.overflow = 10)],
])
  test(`formatted oracle rejects ${name}`, () => {
    const value = inserts();
    mutate(value);
    assert.throws(() => guard.verifyFormattedObservation(value, insertExpected), Error);
  });

test("stream oracle requires one stable source and completed semantic blocks", () => {
  const value = {
    ids: ["source", "source", "source"],
    partialCode: "  COMIC_STREAM_CODE",
    code: "  COMIC_STREAM_CODE\n\tCOMIC_STREAM_END  ",
    cells: ["key", "value", "COMIC_STREAM_CELL", "settled"],
    after: true,
    literalFence: false,
  };
  assert.doesNotThrow(() => guard.verifyFormattedStream(value));
  for (const mutation of [
    (v) => (v.ids[2] = "replacement"),
    (v) => (v.partialCode = ""),
    (v) => v.cells.pop(),
    (v) => (v.literalFence = true),
  ]) {
    const bad = structuredClone(value);
    mutation(bad);
    assert.throws(() => guard.verifyFormattedStream(bad), Error);
  }
});

test("collapse oracle requires retirement without losing the mounted sibling", () => {
  const value = {
    collapsed: "a",
    sibling: "b",
    mountedCollapsed: true,
    siblingExpanded: true,
    retired: ["a"],
    reread: [],
  };
  assert.doesNotThrow(() => guard.verifyBranchRetirement(value));
  for (const mutation of [
    (v) => (v.retired = []),
    (v) => v.retired.push("b"),
    (v) => (v.siblingExpanded = false),
    (v) => (v.mountedCollapsed = false),
    (v) => v.reread.push("b"),
  ]) {
    const bad = structuredClone(value);
    mutation(bad);
    assert.throws(() => guard.verifyBranchRetirement(bad), Error);
  }
});
