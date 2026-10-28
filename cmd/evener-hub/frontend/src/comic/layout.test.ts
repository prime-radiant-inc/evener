import { describe, expect, test } from "vitest";
import { composeComicRows, splitComicText } from "./layout";
import type { ComicEntry } from "./projection";

function entry(id: string, kind: ComicEntry["kind"], text: string): ComicEntry {
  return {
    id,
    sourceItemId: id,
    sourceIndex: 0,
    kind,
    speaker: kind === "dialogue" ? "human" : "source",
    sourceLabel: id,
    text,
    streaming: false,
  };
}

const originals = [
  "alpha\t beta\n\nnext sentence.  After that!",
  "```ts\nconst answer = 42;\n```\n\nlast line",
  "cafe\u0301 👩🏽‍💻 and 👨‍👩‍👧‍👦 then 🇺🇸",
  "unbroken".repeat(35),
];

describe.each(["dialogue", "thought"] as const)("%s partition", (kind) => {
  test("reconstructs every source character at grapheme boundaries", () => {
    for (const original of originals) {
      const parts = splitComicText(original, (candidate) => candidate.length <= 13);
      expect(parts.map((p) => original.slice(p.start, p.end)).join("")).toBe(original);
      expect(parts.length).toBeGreaterThan(1);
      const boundaries = new Set([
        0,
        ...Array.from(
          new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(original),
          (s) => s.index + s.segment.length,
        ),
      ]);
      for (const [index, part] of parts.entries()) {
        expect(part.start).toBe(index === 0 ? 0 : parts[index - 1]?.end);
        expect(part.end).toBeGreaterThan(part.start);
        expect(boundaries.has(part.start)).toBe(true);
        expect(boundaries.has(part.end)).toBe(true);
      }
      const rows = composeComicRows(
        [entry(`item-${kind}`, kind, original)],
        760,
        (_document, part) => part.input.length <= 13,
      );
      expect(rows.every((row) => row.panels.length === 1)).toBe(true);
      expect(
        rows.flatMap((row) => row.panels.map(({ slice }) => original.slice(slice.start, slice.end))).join(""),
      ).toBe(original);
    }
  });

  test("leaves an oversized single grapheme intact to expand the panel", () => {
    const original = "👨‍👩‍👧‍👦";
    expect(splitComicText(original, () => false)).toEqual([{ start: 0, end: original.length }]);
  });
});

test("does not invent words for an empty image or content-free thought", () => {
  const entries = [entry("image", "images", ""), entry("thought", "thought", "")];
  const rows = composeComicRows(entries, 1200, () => true);
  expect(rows.map((row) => row.panels.map((panel) => panel.slice))).toEqual([
    [{ entryId: "image", start: 0, end: 0 }],
    [{ entryId: "thought", start: 0, end: 0 }],
  ]);
});

test("pairs only adjacent single-slice dialogue within a strip", () => {
  const entries = [
    entry("a", "dialogue", "Hello."),
    entry("b", "dialogue", "Hi."),
    entry("c", "thought", "Thinking."),
    entry("d", "dialogue", "Later."),
    entry("e", "dialogue", "Finish."),
    entry("long", "dialogue", "abcdefghij"),
    entry("after", "dialogue", "Yes."),
  ];
  const rows = composeComicRows(
    entries,
    1100,
    (_document, part, composition) => composition !== "paired" || part.input.length <= 6,
  );
  expect(rows.map((row) => row.panels.map((panel) => panel.slice.entryId))).toEqual([
    ["a", "b"],
    ["c"],
    ["d"],
    ["e"],
    ["long"],
    ["after"],
  ]);
  expect(composeComicRows(entries, 760, () => true).every((row) => row.panels.length === 1)).toBe(true);
});

test("pairs two short dialogues above the one-column boundary and keeps a lone dialogue single", () => {
  const entries = [entry("a", "dialogue", "Hello."), entry("b", "dialogue", "Hi.")];
  expect(composeComicRows(entries, 761, () => true).map((row) => row.panels.length)).toEqual([2]);
  expect(composeComicRows(entries.slice(0, 1), 1360, () => true).map((row) => row.panels.length)).toEqual([1]);
  expect(composeComicRows(entries, 390, () => true).map((row) => row.panels.length)).toEqual([1, 1]);
});

test("a continuation is wide and a spotlight is settled short dialogue only", () => {
  const entries = [
    entry("first", "dialogue", "x".repeat(85)),
    entry("short", "dialogue", "OK"),
    entry("stream", "dialogue", "Working"),
  ];
  entries[2] = { ...entries[2], streaming: true } as ComicEntry;
  const rows = composeComicRows(entries, 1200, (_document, part) => part.input.length <= 30);
  expect(rows.map((row) => row.panels.map((p) => [p.slice.entryId, p.composition]))).toEqual([
    [["first", "wide"]],
    [["first", "wide"]],
    [["first", "wide"]],
    [["short", "spotlight"]],
    [["stream", "wide"]],
  ]);
});

test.each(["first", "second"] as const)("image-bearing dialogue in %s pair position stays wide", (position) => {
  const mixed: ComicEntry = {
    ...entry("mixed", "dialogue", "Look at this."),
    images: [{ src: "data:image/png;base64,iVBORw0KGgo=", name: "photo.png" }],
  };
  const plain = entry("plain", "dialogue", "Thanks.");
  const entries = position === "first" ? [mixed, plain] : [plain, mixed];
  const rows = composeComicRows(entries, 1200, () => true);
  expect(rows.map((row) => row.panels.map((panel) => panel.slice.entryId))).toEqual(entries.map((value) => [value.id]));
  expect(rows.flatMap((row) => row.panels).find((panel) => panel.slice.entryId === mixed.id)?.composition).toBe("wide");
});

test("a short image-bearing dialogue never becomes a spotlight", () => {
  const mixed: ComicEntry = {
    ...entry("mixed", "dialogue", "Look."),
    images: [{ src: "data:image/png;base64,iVBORw0KGgo=", name: "photo.png" }],
  };
  expect(composeComicRows([mixed], 1200, () => true)[0]?.panels[0]?.composition).toBe("wide");
});

test("formatted continuations break between blocks, not inside emphasis or references", () => {
  const source = "# Heading\n\n**A long intact emphasis**\n\n[link][ref]\n\n[ref]: https://example.com";
  const rows = composeComicRows(
    [entry("formatted", "dialogue", source)],
    390,
    (_document, part) => part.input.length < 25,
  );
  const panels = rows.flatMap((row) => row.panels);
  expect(panels).toHaveLength(3);
  expect(panels.map(({ slice }) => source.slice(slice.start, slice.end)).join("")).toBe(source);
  expect(panels.map(({ slice }) => slice.start)).toEqual([0, 11, 39]);
  expect(panels.map(({ part }) => part.input.includes("[ref]: https://example.com"))).toEqual([true, true, true]);
});

test("technical blocks bypass balloon budgets even in a thought and never pair", () => {
  const source = `\`\`\`ts\n${"X".repeat(400)}\n\`\`\``;
  const calls: string[] = [];
  const rows = composeComicRows(
    [entry("code", "thought", source), entry("next", "dialogue", "Next")],
    1200,
    (_document, part) => {
      calls.push(part.kind);
      return false;
    },
  );
  expect(rows[0]?.panels.map((panel) => panel.composition)).toEqual(["insert"]);
  expect(calls).not.toContain("code");
  expect(rows[0]?.panels[0]?.slice).toEqual({ entryId: "code", start: 0, end: source.length });
});
