// @vitest-environment node
import { expect, test } from "vitest";
import { applyNotification, chunkViewBackingForTests } from "../../../../protocol/reducer";
import { buildFloodChunks, hydrateFloodModel } from "../../../../protocol/testing/tokenFlood";
import type { AnyNotification } from "../../../../protocol/types.gen";
import {
  formatThoughtDuration,
  joinedReasoningParagraphs,
  lastMeaningfulThoughtLine,
  segmentReasoningTrace,
  thoughtDurationMs,
} from "./reasoningFormat";

// --- joinedReasoningParagraphs ---------------------------------------------
// reasoningSummaries is string[][] - per summaryIndex chunk lists
// (protocol/model.ts). Each index joins to one paragraph; empty (or
// all-whitespace) paragraphs are dropped, matching the wave-4 scope's
// "empty thoughts removed".

test("undefined summaries yields no paragraphs", () => {
  expect(joinedReasoningParagraphs(undefined)).toEqual([]);
});

test("an empty summaries array yields no paragraphs", () => {
  expect(joinedReasoningParagraphs([])).toEqual([]);
});

test("joins each summaryIndex's chunks into one paragraph", () => {
  expect(
    joinedReasoningParagraphs([
      ["Hel", "lo "],
      ["wor", "ld"],
    ]),
  ).toEqual(["Hello ", "world"]);
});

test("drops a paragraph whose chunks join to an empty string", () => {
  expect(joinedReasoningParagraphs([["real content"], [], ["more"]])).toEqual(["real content", "more"]);
});

test("drops a paragraph whose chunks join to all-whitespace", () => {
  expect(joinedReasoningParagraphs([["real"], ["  ", "\n"]])).toEqual(["real"]);
});

test("all paragraphs empty yields an empty array (the whole thought is empty)", () => {
  expect(joinedReasoningParagraphs([[], ["   "]])).toEqual([]);
});

// --- joinedReasoningParagraphs over LIVE chunk views -------------------------
// The reducer accumulates reasoning deltas into brand-carrying chunk views
// (protocol/reducer.ts's appendChunk/appendReasoningDelta), so a streaming
// think block hands this function view arrays, not plain ones. These pin the
// contract the O(1) rerouting relies on: joining live views must return text
// IDENTICAL to joining the same chunks as plain arrays, in every summary slot.

// Folds a reasoning item up through `counts.length` summary indices, each fed
// its own list of deltas, exactly the way the live wire would (item/started
// then one item/reasoning/summaryTextDelta per chunk). Returns the model so
// callers can read the item's reasoningSummaries straight out of the fold.
function foldLiveReasoning(deltas: string[][]): { summaries: string[][] | undefined } {
  let model = hydrateFloodModel("ref_t");
  const threadId = "thr_ref_t";
  const ref = "ref_t";
  const turnId = "turn_1";
  const itemId = "item_r";
  model = applyNotification(
    model,
    {
      method: "turn/started",
      params: { threadId, ref, turn: { id: turnId, status: "inProgress", itemsView: "" } },
    } as AnyNotification,
    1001,
  );
  model = applyNotification(
    model,
    {
      method: "item/started",
      params: { threadId, ref, turnId, item: { type: "reasoning", id: itemId, turnId, status: "inProgress" } },
    } as AnyNotification,
    1002,
  );
  let now = 1003;
  for (const [summaryIndex, chunks] of deltas.entries()) {
    for (const delta of chunks) {
      now += 1;
      model = applyNotification(
        model,
        {
          method: "item/reasoning/summaryTextDelta",
          params: { threadId, ref, turnId, itemId, summaryIndex, delta },
        } as AnyNotification,
        now,
      );
    }
  }
  // Locate the reasoning item the fold produced (turn 0, item 0).
  const turn = model.turns[0];
  const item = turn?.items[0];
  return { summaries: item?.reasoningSummaries };
}

test("joinedReasoningParagraphs over live chunk views returns the same paragraphs as plain-array joins", () => {
  // Fixture: deterministic chunk lists in the live wire's observed size range
  // (2..40 chars, the token-flood harness's own bounds).
  const deltas = [buildFloodChunks(12, 7), buildFloodChunks(9, 11), buildFloodChunks(1, 13)];
  const { summaries } = foldLiveReasoning(deltas);
  expect(summaries).toBeDefined();
  // The fold really produced views, not plain arrays (otherwise this test
  // would silently stop exercising the cache path it exists to pin).
  for (const chunks of summaries ?? []) {
    expect(chunkViewBackingForTests(chunks ?? [])).toBeDefined();
  }
  // The paragraphs joined from views are IDENTICAL to plain-array joins of
  // the same chunk contents, summary slot by summary slot.
  const expected = (summaries ?? []).map((chunks) => (chunks ?? []).join(""));
  expect(joinedReasoningParagraphs(summaries)).toEqual(expected);
});

test("joinedReasoningParagraphs drops empty live-view paragraphs exactly like plain ones", () => {
  // Second summary joins to whitespace-only, so only the first survives -
  // the same empty-thoughts rule the plain-array tests above pin, held to the
  // view path. Plain counterpart: the same chunk CONTENTS as plain arrays.
  const deltas = [buildFloodChunks(5, 3), ["  ", "\n", "\t"]];
  const { summaries } = foldLiveReasoning(deltas);
  expect(summaries).toBeDefined();
  const plain = (summaries ?? []).map((chunks) => [...(chunks ?? [])]);
  expect(joinedReasoningParagraphs(plain)).toEqual(joinedReasoningParagraphs(summaries));
  expect(joinedReasoningParagraphs(summaries)).toHaveLength(1);
});

// --- thoughtDurationMs -------------------------------------------------------
// Computes from REAL timestamps only - never a client wall clock. The wire
// pair (ItemModel.startedAt/completedAt) wins when present; otherwise falls
// back to the client-observed arrival pair (ItemModel.observedStartedAt/
// observedCompletedAt, stamped by the reducer - see model.ts's own comment).
// Both absent (hydrated/historical items) yields no duration.

test("undefined startedAt/completedAt yields no duration", () => {
  expect(thoughtDurationMs(undefined, undefined)).toBeUndefined();
  expect(thoughtDurationMs("2026-01-01T00:00:00.000Z", undefined)).toBeUndefined();
  expect(thoughtDurationMs(undefined, "2026-01-01T00:00:00.000Z")).toBeUndefined();
});

test("an unparseable timestamp yields no duration rather than a garbage number", () => {
  expect(thoughtDurationMs("not a date", "2026-01-01T00:00:00.000Z")).toBeUndefined();
});

test("preserves actual elapsed milliseconds from two real ISO timestamps", () => {
  expect(thoughtDurationMs("2026-01-01T00:00:00.000Z", "2026-01-01T00:00:04.400Z")).toBe(4_400);
  expect(thoughtDurationMs("2026-01-01T00:00:00.000Z", "2026-01-01T00:00:04.600Z")).toBe(4_600);
});

test("rejects impossible calendar timestamps while accepting leap days and offsets", () => {
  expect(thoughtDurationMs("2026-02-30T00:00:00.000Z", "2026-03-03T00:00:00.000Z")).toBeUndefined();
  expect(thoughtDurationMs("2026-13-01T00:00:00.000Z", "2026-01-02T00:00:00.000Z")).toBeUndefined();
  expect(thoughtDurationMs("2024-02-29T00:00:00.000Z", "2024-03-01T00:00:00.000Z")).toBe(86_400_000);
  expect(thoughtDurationMs("2026-01-01T00:00:00.000-05:00", "2026-01-01T01:00:00.000-05:00")).toBe(3_600_000);
  expect(thoughtDurationMs("2026-01-01T00:00:00.12Z", "2026-01-01T00:00:01.123456Z")).toBe(1_003);
});

test("a sub-second elapsed span stays sub-second", () => {
  expect(thoughtDurationMs("2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.200Z")).toBe(200);
});

test("neither wire nor observed pair present yields no duration", () => {
  expect(thoughtDurationMs(undefined, undefined, undefined, undefined)).toBeUndefined();
});

test("falls back to the observed pair when the wire pair is absent", () => {
  expect(thoughtDurationMs(undefined, undefined, "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:04.400Z")).toBe(4_400);
});

test("the wire pair wins when both the wire and observed pairs are present", () => {
  expect(
    thoughtDurationMs(
      "2026-01-01T00:00:00.000Z",
      "2026-01-01T00:00:10.000Z",
      "2026-01-01T00:00:00.000Z",
      "2026-01-01T00:00:02.000Z",
    ),
  ).toBe(10_000);
});

test("a backwards timestamp pair is unavailable rather than a fabricated positive duration", () => {
  expect(thoughtDurationMs("2026-01-01T00:00:02.000Z", "2026-01-01T00:00:01.000Z")).toBeUndefined();
});

test("the exact timestamp boundary remains an honest zero", () => {
  expect(thoughtDurationMs("2026-01-01T00:00:01.000Z", "2026-01-01T00:00:01.000Z")).toBe(0);
});

test("formatThoughtDuration uses milliseconds and sensible second tiers", () => {
  expect(formatThoughtDuration(0)).toBe("0ms");
  expect(formatThoughtDuration(250)).toBe("250ms");
  expect(formatThoughtDuration(1_500)).toBe("1.5s");
  expect(formatThoughtDuration(10_000)).toBe("10s");
});

test("lastMeaningfulThoughtLine chooses the final nonblank line and trims it", () => {
  expect(lastMeaningfulThoughtLine(["first line\nsecond line\n  ", "\nthird line"], 80)).toBe("third line");
});

test("lastMeaningfulThoughtLine clips only when the final line exceeds the bound", () => {
  expect(lastMeaningfulThoughtLine(["first", "a very long final line"], 11)).toBe("a very long…");
  expect(lastMeaningfulThoughtLine(["first", "short"], 11)).toBe("short");
});

test("lastMeaningfulThoughtLine clips without splitting Unicode code points", () => {
  expect(lastMeaningfulThoughtLine([`a${"🙂".repeat(10)}`], 5)).toBe("a🙂🙂🙂🙂…");
});

test("lastMeaningfulThoughtLine removes only common Markdown decoration from the plain preview", () => {
  expect(lastMeaningfulThoughtLine(["## plan", "- **ship** `it`"], 80)).toBe("ship it");
});

test("lastMeaningfulThoughtLine extracts readable Markdown without damaging identifiers", () => {
  expect(lastMeaningfulThoughtLine(["__bold__ _em_"], 80)).toBe("bold em");
  expect(lastMeaningfulThoughtLine(["![diagram](https://x/image_(1).png)"], 80)).toBe("diagram");
  expect(lastMeaningfulThoughtLine(["[docs](https://x/path_(1)/docs)"], 80)).toBe("docs");
  expect(lastMeaningfulThoughtLine(["> > - [ ] **task**"], 80)).toBe("task");
  expect(lastMeaningfulThoughtLine(["> ## heading"], 80)).toBe("heading");
  expect(lastMeaningfulThoughtLine(["## heading `code` ordinary_path_name"], 80)).toBe(
    "heading code ordinary_path_name",
  );
});

test("lastMeaningfulThoughtLine flattens Markdown inside image alt text", () => {
  expect(lastMeaningfulThoughtLine(["![**diagram**](https://x)"], 80)).toBe("diagram");
});

test("formatThoughtDuration chooses the seconds tier before rounding", () => {
  expect(formatThoughtDuration(9_999)).toBe("10.0s");
  expect(formatThoughtDuration(10_000)).toBe("10s");
});

// --- segmentReasoningTrace ---------------------------------------------------
// The expanded trace's step sections, built ONLY from structure the text
// already carries - markdown headings, or (absent any heading) the paragraph
// breaks joinedReasoningParagraphs already produced. No structure at all
// stays one section, same as today's undivided render.

test("no paragraphs yields no sections", () => {
  expect(segmentReasoningTrace([])).toEqual([]);
});

test("a single paragraph with no heading has no internal boundary - stays one section", () => {
  expect(segmentReasoningTrace(["just one plain thought, no structure"])).toEqual([
    "just one plain thought, no structure",
  ]);
});

test("multiple paragraphs with no heading anywhere: gated on heading structure only, so they stay one joined section (falls through to the single-document render)", () => {
  expect(segmentReasoningTrace(["first thought", "second thought", "third thought"])).toEqual([
    "first thought\n\nsecond thought\n\nthird thought",
  ]);
});

test("a heading starting the very first paragraph: everything up to the next heading is one section, no empty preamble", () => {
  expect(segmentReasoningTrace(["## Plan\n\nfigure it out"])).toEqual(["## Plan\n\nfigure it out"]);
});

test("text before the first heading becomes its own leading section", () => {
  expect(segmentReasoningTrace(["intro text", "## Plan\n\nfigure it out"])).toEqual([
    "intro text",
    "## Plan\n\nfigure it out",
  ]);
});

test("multiple headings each start a new section, running up to the next heading", () => {
  expect(
    segmentReasoningTrace(["## Survey\n\n- check a\n- check b", "## Decide\n\nship it", "## Verify\n\nrun the tests"]),
  ).toEqual(["## Survey\n\n- check a\n- check b", "## Decide\n\nship it", "## Verify\n\nrun the tests"]);
});

test("heading structure wins over paragraph structure when both are present", () => {
  // Two summaryIndex paragraphs, but only ONE heading inside the second: the
  // heading boundary governs, not the paragraph break, so the first
  // paragraph merges into the leading (headingless) section.
  expect(segmentReasoningTrace(["intro", "middle", "## Plan\n\nfinish it"])).toEqual([
    "intro\n\nmiddle",
    "## Plan\n\nfinish it",
  ]);
});

test("a section starting with an indented code block keeps its indentation (joinTokenRaw strips only leading newlines, never leading whitespace)", () => {
  const paragraphs = ["    indented\n    code block", "## Heading\n\ntext"];
  expect(segmentReasoningTrace(paragraphs)).toEqual(["    indented\n    code block", "## Heading\n\ntext"]);
});

test("a section reconstructed from heading structure reproduces the exact source markdown, not a rewritten form", () => {
  const paragraphs = ["setup notes", "## Steps\n\n1. one\n2. two\n\nsome **bold** trailing prose"];
  expect(segmentReasoningTrace(paragraphs)).toEqual([
    "setup notes",
    "## Steps\n\n1. one\n2. two\n\nsome **bold** trailing prose",
  ]);
});
