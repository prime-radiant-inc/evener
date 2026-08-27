// @vitest-environment node
import { expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { SearchResponse } from "../../protocol/types.gen";
import { buildSnippet, fetchSearch, findInSessionMatches, highlightParts } from "./search";

// --- fetchSearch: typed AppWire request ---

test("fetchSearch sends the query through evener/search", async () => {
  const client = new FakeClient();
  const response: SearchResponse = { live: [], past: [] };
  client.on("evener/search", (params) => {
    expect(params).toEqual({ query: "a b/c" });
    return response;
  });

  await expect(fetchSearch("a b/c", client)).resolves.toEqual(response);
  expect(client.calls).toEqual([{ method: "evener/search", params: { query: "a b/c" } }]);
});

test("fetchSearch returns typed live and past rows, including qualified refs", async () => {
  const client = new FakeClient();
  const response: SearchResponse = {
    live: [{ id: "bare", ref: "local:qualified", title: "Live one", project: "proj", state: "active", age: "now" }],
    past: [{ id: "p1", ref: "local:p1", title: "Past one", project: "old", state: "ended", age: "2h" }],
  };
  client.on("evener/search", () => response);

  await expect(fetchSearch("one", client)).resolves.toEqual(response);
});

test("fetchSearch propagates an AppWire failure", async () => {
  const client = new FakeClient();
  client.on("evener/search", () => {
    throw new Error("search failed");
  });

  await expect(fetchSearch("x", client)).rejects.toThrow("search failed");
});

// --- findInSessionMatches: scans the focused ThreadModel (turns -> items ->
// text), NOT the DOM (the transcript is virtualized) - the plan-resolved
// port of search.js:961-982's #conversation scan.

function item(id: string, text: string): ItemModel {
  return { id, turnId: "t", type: "message", text };
}
function turn(id: string, items: ItemModel[]): TurnModel {
  return { id, status: "completed", items };
}
function modelWithTurns(turns: TurnModel[]): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic/claude",
    model: "anthropic/claude",
    askPending: false,
    pendingEscalations: [],
    turns,
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    lastFrameAt: 0,
    capabilities: {
      send: true,
      steer: true,
      interrupt: true,
      compact: true,
      clear: true,
      forkFromTurn: true,
      shutdown: true,
      changeModel: true,
      queue: true,
      goal: true,
      rename: true,
    },
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
  };
}

test("findInSessionMatches finds case-insensitive hits and labels each with a 1-based turn number", () => {
  const model = modelWithTurns([
    turn("t1", [item("i1", "Hello world"), item("i2", "tool output")]),
    turn("t2", [item("i3", "Goodbye WORLD")]),
  ]);
  const hits = findInSessionMatches(model, "world");
  expect(hits.map((h) => ({ turn: h.turn, itemId: h.itemId }))).toEqual([
    { turn: 1, itemId: "i1" },
    { turn: 2, itemId: "i3" },
  ]);
});

test("findInSessionMatches returns [] for an empty query and never matches empty-text items", () => {
  const model = modelWithTurns([turn("t1", [item("i1", ""), item("i2", "content")])]);
  expect(findInSessionMatches(model, "")).toEqual([]);
  expect(findInSessionMatches(model, "zzz")).toEqual([]);
});

test("findInSessionMatches normalizes runs of whitespace before matching", () => {
  const model = modelWithTurns([turn("t1", [item("i1", "multi\n\n   line")])]);
  const hits = findInSessionMatches(model, "multi line");
  expect(hits).toHaveLength(1);
});

// The transcript renders more than an item's settled `text`: tool output, tool
// error text, and live reasoning summaries are all visible, so search must
// find them too (reviewer adjudication I2).
function itemWith(id: string, fields: Partial<ItemModel>): ItemModel {
  return { id, turnId: "t", type: "message", text: "", ...fields };
}

test("findInSessionMatches scans tool output", () => {
  const model = modelWithTurns([turn("t1", [itemWith("i1", { type: "tool", output: "result: frobnitz found" })])]);
  const hits = findInSessionMatches(model, "frobnitz");
  expect(hits).toHaveLength(1);
  expect(hits[0]?.turn).toBe(1);
});

test("findInSessionMatches scans tool error text", () => {
  const model = modelWithTurns([turn("t1", [itemWith("i1", { type: "tool", error: "frobnitz not permitted" })])]);
  expect(findInSessionMatches(model, "frobnitz")).toHaveLength(1);
});

test("findInSessionMatches scans reasoning summaries (joined chunks)", () => {
  const model = modelWithTurns([
    turn("t1", [itemWith("i1", { type: "reasoning", reasoningSummaries: [["thinking about ", "frobnitz"]] })]),
  ]);
  expect(findInSessionMatches(model, "frobnitz")).toHaveLength(1);
});

test("a match in both text and output yields two hits sharing the turn label", () => {
  const model = modelWithTurns([turn("t1", [itemWith("i1", { text: "frobnitz here", output: "frobnitz there" })])]);
  const hits = findInSessionMatches(model, "frobnitz");
  expect(hits).toHaveLength(2);
  expect(hits.every((h) => h.turn === 1)).toBe(true);
});

// --- buildSnippet / highlightParts: ~40 chars of context per side, leading/
// trailing ellipsis only when truncated, <mark> around the match
// (search.js:984-992, 994-1003). Structured parts (not HTML strings) so the
// React overlay renders <mark> without dangerouslySetInnerHTML.

test("buildSnippet marks the match and omits ellipses when nothing is truncated", () => {
  const text = "the quick brown fox jumps over the lazy dog";
  expect(buildSnippet(text, text.indexOf("fox"), 3)).toEqual([
    { text: "the quick brown ", mark: false },
    { text: "fox", mark: true },
    { text: " jumps over the lazy dog", mark: false },
  ]);
});

test("buildSnippet clamps to ~40 chars/side and adds ellipses on both truncated ends", () => {
  const text = `${"x".repeat(50)}MATCH${"y".repeat(50)}`;
  const parts = buildSnippet(text, 50, 5);
  expect(parts).toEqual([
    { text: `…${"x".repeat(40)}`, mark: false },
    { text: "MATCH", mark: true },
    { text: `${"y".repeat(40)}…`, mark: false },
  ]);
});

test("highlightParts wraps the first case-insensitive match", () => {
  expect(highlightParts("Switch Model", "model")).toEqual([
    { text: "Switch ", mark: false },
    { text: "Model", mark: true },
    { text: "", mark: false },
  ]);
});

test("highlightParts returns a single unmarked part when there is no match or no query", () => {
  expect(highlightParts("Switch model", "")).toEqual([{ text: "Switch model", mark: false }]);
  expect(highlightParts("Switch model", "zzz")).toEqual([{ text: "Switch model", mark: false }]);
});
