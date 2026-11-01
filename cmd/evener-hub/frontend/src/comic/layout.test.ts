import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, test } from "vitest";
import type { ComicPersona } from "./cast";
import { type ComicRow, composeComicRows, splitComicText } from "./layout";
import type { ComicEntry, ComicItemEntry } from "./projection";

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

const WORKER: ComicPersona = { key: "worker", character: "xeno", name: "Worker", badge: "delegate" };
const HUMAN: ComicPersona = { key: "human", character: "anna", name: "Anna", badge: "human" };

function party(id: string, kind: ComicEntry["kind"], text: string, persona: ComicPersona): ComicItemEntry {
  return {
    id,
    sourceItemId: id,
    sourceIndex: 0,
    kind,
    speaker: kind === "dialogue" ? "human" : "assistant",
    sourceLabel: id,
    text,
    streaming: false,
    persona,
  };
}

const originals = [
  "alpha\t beta\n\nnext sentence.  After that!",
  "```ts\nconst answer = 42;\n```\n\nlast line",
  "cafe\u0301 👩🏽‍💻 and 👨‍👩‍👧‍👦 then 🇺🇸",
  "unbroken".repeat(35),
];

const rowBalloonIds = (rows: readonly ComicRow[]) =>
  rows.map((row) => row.panels.flatMap((panel) => panel.balloons.map((balloon) => balloon.slice.entryId)));

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
        rows
          .flatMap((row) => row.panels.flatMap((panel) => panel.balloons))
          .map(({ slice }) => original.slice(slice.start, slice.end))
          .join(""),
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
  expect(rows.map((row) => row.panels.flatMap((panel) => panel.balloons.map((balloon) => balloon.slice)))).toEqual([
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
    (_document, part, composition) => composition !== "packed" || part.input.length <= 6,
    416,
  );
  expect(rowBalloonIds(rows)).toEqual([["a", "b"], ["c"], ["d"], ["e"], ["long"], ["after"]]);
  expect(composeComicRows(entries, 760, () => true).every((row) => row.panels.length === 1)).toBe(true);
});

test("keeps one column below the packing boundary and packs two at it", () => {
  const entries = [entry("a", "dialogue", "Hello."), entry("b", "dialogue", "Hi.")];
  expect(composeComicRows(entries, 846, () => true, 416).map((row) => row.panels.length)).toEqual([1, 1]);
  expect(composeComicRows(entries, 847, () => true, 416).map((row) => row.panels.length)).toEqual([2]);
  expect(composeComicRows(entries.slice(0, 1), 1750, () => true, 416).map((row) => row.panels.length)).toEqual([1]);
  expect(composeComicRows(entries, 390, () => true, 416).map((row) => row.panels.length)).toEqual([1, 1]);
});

test("packs as many panel-sized panels as the width fits", () => {
  const entries = ["a", "b", "c", "d", "e"].map((id) => entry(id, "dialogue", `${id}.`));
  const rows = composeComicRows(entries, 1290, () => true, 416);
  expect(rowBalloonIds(rows)).toEqual([
    ["a", "b", "c"],
    ["d", "e"],
  ]);
  expect(rows.map((row) => row.columns)).toEqual([3, 3]);
  expect(rows.every((row) => row.unit === "panel")).toBe(true);
  const wide = composeComicRows(entries, 1750, () => true, 416);
  expect(wide.map((row) => row.panels.length)).toEqual([4, 1]);
  expect(wide.map((row) => row.columns)).toEqual([4, 1]);
  expect(wide[1]?.panels[0]?.balloons[0]?.composition).toBe("spotlight");
});

test("stacks consecutive same-party one-liners into one panel", () => {
  const entries = ["a", "b", "c"].map((id) => party(id, "dialogue", `${id}.`, WORKER));
  const rows = composeComicRows(entries, 1290, () => true, 416);
  expect(rows).toHaveLength(1);
  expect(rows[0]?.panels).toHaveLength(1);
  expect(rows[0]?.panels[0]?.balloons.map((balloon) => balloon.slice.entryId)).toEqual(["a", "b", "c"]);
  expect(rows[0]?.panels[0]?.balloons.map(({ slice }) => [slice.start, slice.end])).toEqual([
    [0, 2],
    [0, 2],
    [0, 2],
  ]);
  expect(rows[0]?.panels[0]?.balloons.every((balloon) => balloon.composition === "packed")).toBe(true);
  expect(rows[0]?.columns).toBe(1);
});

test("attributed captions stack, unattributed captions never stack", () => {
  const attributed = ["r1", "r2"].map((id) => party(id, "caption", `${id}.`, WORKER));
  const rows = composeComicRows(attributed, 1290, () => true, 416);
  expect(rows).toHaveLength(1);
  expect(rows[0]?.panels[0]?.balloons.map((balloon) => balloon.slice.entryId)).toEqual(["r1", "r2"]);
  const bare = [entry("c1", "caption", "One."), entry("c2", "caption", "Two.")];
  const bareRows = composeComicRows(bare, 1290, () => true, 416);
  expect(bareRows.every((row) => row.panels.length === 1)).toBe(true);
  expect(bareRows.every((row) => row.panels[0]?.balloons.length === 1)).toBe(true);
});

test("a long same-party run continues into a following panel of the same party", () => {
  const entries = Array.from({ length: 10 }, (_, index) => party(`u${index}`, "dialogue", `line ${index}.`, WORKER));
  const rows = composeComicRows(entries, 1290, () => true, 416);
  // The capped run and its continuation pack side by side as two panels of one row.
  expect(rows).toHaveLength(1);
  expect(rows[0]?.panels.map((panel) => panel.balloons.map((balloon) => balloon.slice.entryId))).toEqual([
    Array.from({ length: 8 }, (_, index) => `u${index}`),
    ["u8", "u9"],
  ]);
  expect(rows[0]?.columns).toBe(3);
});

test("different-party one-liners still pack side by side", () => {
  const entries = [
    party("a", "dialogue", "A.", HUMAN),
    party("b", "dialogue", "B.", WORKER),
    party("c", "dialogue", "C.", HUMAN),
  ];
  const rows = composeComicRows(entries, 1290, () => true, 416);
  expect(rows.map((row) => row.panels.map((panel) => panel.balloons.map((balloon) => balloon.slice.entryId)))).toEqual([
    [["a"], ["b"], ["c"]],
  ]);
});

test("a stack packs beside other panels without losing source ranges", () => {
  const entries = [
    party("a", "dialogue", "A1.", WORKER),
    party("b", "dialogue", "A2.", WORKER),
    party("c", "dialogue", "C.", HUMAN),
  ];
  const rows = composeComicRows(entries, 847, () => true, 416);
  expect(rows).toHaveLength(1);
  expect(rows[0]?.panels.map((panel) => panel.balloons.map((balloon) => balloon.slice.entryId))).toEqual([
    ["a", "b"],
    ["c"],
  ]);
});

test("packed slots measure at their own column width", () => {
  const slots: number[] = [];
  composeComicRows(
    [entry("a", "dialogue", "Hello."), entry("b", "dialogue", "Hi."), entry("c", "dialogue", "Hey.")],
    1290,
    (_document, _part, _composition, _kind, slot) => {
      slots.push(slot ?? 0);
      return true;
    },
    416,
  );
  expect(slots).toEqual([416, 416, 416]);
});

test("row columns stay bounded by the panel size token", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "./comic.module.css"), "utf8");
  expect(css).toContain("max-width: var(--comic-panel-max-width)");
  expect(css).not.toContain("repeat(2,");
});

test("a thought bubble paints one cloud, not a shadow duplicate beside it", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "./comic.module.css"), "utf8");
  const thought = /\.thought \{[^}]*\}/.exec(css)?.[0];
  expect(thought).toBeDefined();
  // The cloud keeps its own outline; a box-shadow would paint a second,
  // offset balloon silhouette that reads as a duplicated message bubble.
  expect(thought).not.toContain("box-shadow");
  expect(thought).toContain("border-radius");
});

test("a continuation is wide and a spotlight is settled short dialogue only", () => {
  const entries = [
    entry("first", "dialogue", "x".repeat(85)),
    entry("short", "dialogue", "OK"),
    entry("stream", "dialogue", "Working"),
  ];
  entries[2] = { ...entries[2], streaming: true } as ComicEntry;
  const rows = composeComicRows(entries, 1200, (_document, part) => part.input.length <= 30);
  expect(
    rows.map((row) =>
      row.panels.flatMap((panel) => panel.balloons).map((balloon) => [balloon.slice.entryId, balloon.composition]),
    ),
  ).toEqual([
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
  expect(rowBalloonIds(rows)).toEqual(entries.map((value) => [value.id]));
  expect(
    rows
      .flatMap((row) => row.panels.flatMap((panel) => panel.balloons))
      .find((balloon) => balloon.slice.entryId === mixed.id)?.composition,
  ).toBe("wide");
});

test("a short image-bearing dialogue never becomes a spotlight", () => {
  const mixed: ComicEntry = {
    ...entry("mixed", "dialogue", "Look."),
    images: [{ src: "data:image/png;base64,iVBORw0KGgo=", name: "photo.png" }],
  };
  expect(composeComicRows([mixed], 1200, () => true)[0]?.panels[0]?.balloons[0]?.composition).toBe("wide");
});

test("single-prose eligibility gates compact fit calls without limiting pair length", () => {
  for (const value of [
    entry("empty", "dialogue", ""),
    entry("thought", "thought", "x"),
    { ...entry("stream", "dialogue", "x"), streaming: true },
    { ...entry("image", "dialogue", "x"), images: [{ src: "fixture.png", name: "fixture.png" }] },
    entry("insert", "dialogue", "```txt\nx\n```"),
    entry("mixed", "dialogue", "x\n\n```txt\ny\n```"),
    entry("long", "dialogue", "x".repeat(81)),
  ]) {
    const calls: string[] = [];
    composeComicRows([value], 1200, (_document, _part, composition) => {
      calls.push(composition);
      return true;
    });
    expect(calls.filter((composition) => composition === "packed" || composition === "spotlight")).toEqual([]);
  }
  const calls: string[] = [];
  const fit = (_document: unknown, _part: unknown, composition: string) => {
    calls.push(composition);
    return true;
  };
  expect(
    composeComicRows([entry("limit", "dialogue", "x".repeat(80))], 1200, fit)[0]?.panels[0]?.balloons[0]?.composition,
  ).toBe("spotlight");
  expect(calls.every((composition) => composition === "spotlight")).toBe(true);
  calls.length = 0;
  const rows = composeComicRows(
    [entry("long", "dialogue", "x".repeat(81)), entry("next", "dialogue", "y")],
    1200,
    fit,
    416,
  );
  expect(rows.map((row) => row.panels.length)).toEqual([2]);
  expect(calls).toEqual(["packed", "packed"]);
});

test("formatted continuations break between blocks, not inside emphasis or references", () => {
  const source = "# Heading\n\n**A long intact emphasis**\n\n[link][ref]\n\n[ref]: https://example.com";
  const rows = composeComicRows(
    [entry("formatted", "dialogue", source)],
    390,
    (_document, part) => part.input.length < 25,
  );
  const balloons = rows.flatMap((row) => row.panels.flatMap((panel) => panel.balloons));
  expect(balloons).toHaveLength(3);
  expect(balloons.map(({ slice }) => source.slice(slice.start, slice.end)).join("")).toBe(source);
  expect(balloons.map(({ slice }) => slice.start)).toEqual([0, 11, 39]);
  expect(balloons.map(({ part }) => part.input.includes("[ref]: https://example.com"))).toEqual([true, true, true]);
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
  expect(rows[0]?.panels.flatMap((panel) => panel.balloons).map((balloon) => balloon.composition)).toEqual(["insert"]);
  expect(calls).not.toContain("code");
  expect(rows[0]?.panels[0]?.balloons[0]?.slice).toEqual({ entryId: "code", start: 0, end: source.length });
});
