import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../protocol/model";
import { buildSnippet, fetchSearch, findInSessionMatches, highlightParts } from "./search";

// --- fetchSearch: REST GET /api/search?q= (no appwire `search` method
// exists - parity-m6-surfaces.md §2.3, verified). Wire shape confirmed
// against cmd/serf-hub/web_api.go handleApiSearch + web_types.go
// searchResponse: {live, past} of {id,title,project,state,age}, and Go
// encodes an empty slice as JSON null.

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "Error",
    json: () => Promise.resolve(body),
  } as Response;
}

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

test("fetchSearch GETs /api/search with the URL-encoded query and same-origin credentials", async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ live: [], past: [] }));
  await fetchSearch("a b/c");
  expect(fetchMock).toHaveBeenCalledWith("/api/search?q=a%20b%2Fc", { credentials: "same-origin" });
});

test("fetchSearch returns the live and past rows verbatim", async () => {
  const body = {
    live: [{ id: "local:a", title: "Live one", project: "proj", state: "active", age: "now" }],
    past: [{ id: "p1", title: "Past one", project: "old", state: "ended", age: "2h" }],
  };
  fetchMock.mockResolvedValueOnce(jsonResponse(body));
  const resp = await fetchSearch("one");
  expect(resp).toEqual(body);
});

test("fetchSearch normalizes Go's null empty-slice encoding to []", async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({ live: null, past: null }));
  const resp = await fetchSearch("nothing");
  expect(resp).toEqual({ live: [], past: [] });
});

test("fetchSearch rejects on a non-ok response", async () => {
  fetchMock.mockResolvedValueOnce(jsonResponse({}, 500));
  await expect(fetchSearch("x")).rejects.toThrow();
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
