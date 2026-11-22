import {
  type ItemModel,
  makeTranscriptDisplayConfig,
  type SearchResponse,
  type ThreadModel,
  type TurnModel,
  WarningCodeContextBudget,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import { expect, test } from "vitest";
import { ALL_CAPABILITIES } from "../../threadTestUtils";
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
function turn(id: string, items: ItemModel[], status: TurnModel["status"] = "completed"): TurnModel {
  return { id, status, items };
}
function modelWithTurns(turns: TurnModel[]): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic/claude",
    model: "anthropic/claude",
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns,
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    lastFrameAt: 0,
    capabilities: ALL_CAPABILITIES,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    contextAssumed: false,
    compactionThreshold: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
  };
}

// The palette reads the session's active display config to know which calls
// and thoughts project as searchable rows; message text is not level-gated.
const VIEW = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" });
const TOOLS = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
const CHAT = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });

test("findInSessionMatches finds case-insensitive hits and labels each with a 1-based turn number", () => {
  const model = modelWithTurns([
    turn("t1", [item("i1", "Hello world"), item("i2", "tool output")]),
    turn("t2", [item("i3", "Goodbye WORLD")]),
  ]);
  const hits = findInSessionMatches(model, "world", VIEW);
  expect(hits.map((h) => ({ turn: h.turn, itemId: h.itemId }))).toEqual([
    { turn: 1, itemId: "i1" },
    { turn: 2, itemId: "i3" },
  ]);
});

test("findInSessionMatches returns [] for an empty query and never matches empty-text items", () => {
  const model = modelWithTurns([turn("t1", [item("i1", ""), item("i2", "content")])]);
  expect(findInSessionMatches(model, "", VIEW)).toEqual([]);
  expect(findInSessionMatches(model, "zzz", VIEW)).toEqual([]);
});

test("findInSessionMatches normalizes runs of whitespace before matching", () => {
  const model = modelWithTurns([turn("t1", [item("i1", "multi\n\n   line")])]);
  const hits = findInSessionMatches(model, "multi line", VIEW);
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
  const hits = findInSessionMatches(model, "frobnitz", VIEW);
  expect(hits).toHaveLength(1);
  expect(hits[0]?.turn).toBe(1);
});

test("findInSessionMatches scans tool error text", () => {
  const model = modelWithTurns([turn("t1", [itemWith("i1", { type: "tool", error: "frobnitz not permitted" })])]);
  expect(findInSessionMatches(model, "frobnitz", VIEW)).toHaveLength(1);
});

test("findInSessionMatches scans reasoning summaries (joined chunks)", () => {
  const model = modelWithTurns([
    turn("t1", [itemWith("i1", { type: "reasoning", reasoningSummaries: [["thinking about ", "frobnitz"]] })]),
  ]);
  expect(findInSessionMatches(model, "frobnitz", VIEW)).toHaveLength(1);
});

test("findInSessionMatches reads a thought's summaries as rendered Markdown", () => {
  const model = modelWithTurns([
    turn("t1", [itemWith("think", { type: "reasoning", reasoningSummaries: [["the **frobnitz** plan"]] })]),
  ]);
  expect(findInSessionMatches(model, "frobnitz plan", VIEW).map((hit) => hit.itemId)).toEqual(["think"]);
  // The summary's formatting marks are not words on the page.
  expect(findInSessionMatches(model, "**frobnitz**", VIEW)).toEqual([]);
});

test("findInSessionMatches does not scan a thought's raw trace", () => {
  const model = modelWithTurns([
    turn("t1", [
      itemWith("think", { type: "reasoning", text: "raw frobnitz trace", reasoningSummaries: [["a plan"]] }),
    ]),
  ]);
  expect(findInSessionMatches(model, "raw frobnitz", VIEW)).toEqual([]);
});

test("findInSessionMatches reads a streaming thought's unclosed Markdown live", () => {
  const live = modelWithTurns([
    turn(
      "t1",
      [
        itemWith("think", {
          type: "reasoning",
          status: "inProgress",
          reasoningSummaries: [["check **the assumptions"]],
        }),
      ],
      "inProgress",
    ),
  ]);
  expect(findInSessionMatches(live, "check the assumptions", VIEW).map((hit) => hit.itemId)).toEqual(["think"]);
  // Settled text shows the unbalanced marker literally.
  const settled = modelWithTurns([
    turn("t1", [itemWith("think", { type: "reasoning", reasoningSummaries: [["check **the assumptions"]] })]),
  ]);
  expect(findInSessionMatches(settled, "check the assumptions", VIEW)).toEqual([]);
});

// A thought reads live only while it is the turn's tail: once a reply starts in
// the same in-progress turn, ThinkBlock renders the thought settled.
test("findInSessionMatches reads a thought a later item overtook as settled", () => {
  const thought = itemWith("think", {
    type: "reasoning",
    status: "inProgress",
    reasoningSummaries: [["check **the assumptions"]],
  });
  const reply = itemWith("reply", { type: "agentMessage", text: "Answering now", status: "inProgress" });
  const overtaken = modelWithTurns([turn("t1", [thought, reply], "inProgress")]);
  expect(findInSessionMatches(overtaken, "check the assumptions", VIEW)).toEqual([]);
  expect(findInSessionMatches(overtaken, "check **the assumptions", VIEW).map((hit) => hit.itemId)).toEqual(["think"]);
  // Still the tail, so it reads live.
  const tail = modelWithTurns([turn("t1", [thought], "inProgress")]);
  expect(findInSessionMatches(tail, "check the assumptions", VIEW).map((hit) => hit.itemId)).toEqual(["think"]);
});

// Chat shows no reasoning row, so none of a thought's summary is text Find
// could land on there.
test("findInSessionMatches offers no thought summary the view hides", () => {
  const settled = modelWithTurns([
    turn("t1", [itemWith("think", { type: "reasoning", reasoningSummaries: [["check **the assumptions**"]] })]),
  ]);
  expect(findInSessionMatches(settled, "check the assumptions", CHAT)).toEqual([]);
  // The tools view renders the thought, so its disclosure can open on the hit.
  expect(findInSessionMatches(settled, "check the assumptions", VIEW).map((hit) => hit.itemId)).toEqual(["think"]);

  const live = modelWithTurns([
    turn(
      "t1",
      [itemWith("think", { type: "reasoning", status: "inProgress", reasoningSummaries: [["live thought needle"]] })],
      "inProgress",
    ),
  ]);
  expect(findInSessionMatches(live, "live thought needle", CHAT)).toEqual([]);
  expect(findInSessionMatches(live, "live thought needle", VIEW).map((hit) => hit.itemId)).toEqual(["think"]);

  // A failed thought is reasoning too: Chat hides its row and offers no hit.
  const failed = modelWithTurns([
    turn("t1", [itemWith("think", { type: "reasoning", status: "failed", reasoningSummaries: [["the plan"]] })]),
  ]);
  expect(findInSessionMatches(failed, "thought failed", CHAT)).toEqual([]);
});

test("findInSessionMatches scans an action's rationale but not a system notice's description", () => {
  const model = modelWithTurns([
    turn("t1", [
      itemWith("call", { type: "commandExecution", toolName: "shell", description: "grep for frobnitz" }),
      itemWith("note", { type: "systemMessage", description: "frobnicate the frobnitz" }),
    ]),
  ]);
  expect(findInSessionMatches(model, "frobnitz", VIEW).map((hit) => hit.itemId)).toEqual(["call"]);
});

test("findInSessionMatches scans an action's rationale", () => {
  const model = modelWithTurns([
    turn("t1", [itemWith("call", { type: "commandExecution", toolName: "shell", description: "grep for frobnitz" })]),
  ]);
  expect(findInSessionMatches(model, "frobnitz", VIEW).map((hit) => hit.itemId)).toEqual(["call"]);
});

test("findInSessionMatches reads a rationale only where the view shows an action row", () => {
  const model = modelWithTurns([
    turn("t1", [itemWith("call", { type: "commandExecution", toolName: "shell", description: "grep for frobnitz" })]),
  ]);
  // Tools projects the call as a plain tool row, so no row shows this text and
  // Find could not land on the hit the palette would offer.
  expect(findInSessionMatches(model, "frobnitz", TOOLS)).toEqual([]);
  expect(findInSessionMatches(model, "frobnitz", VIEW).map((hit) => hit.itemId)).toEqual(["call"]);
});

test("findInSessionMatches does not read the rationale of a suppressed action row", () => {
  const model = modelWithTurns([
    turn("t1", [
      itemWith("call", {
        type: "commandExecution",
        toolName: "task_list",
        argumentsJSON: JSON.stringify({ action: "view" }),
        description: "view the frobnitz list",
      }),
    ]),
  ]);
  expect(findInSessionMatches(model, "frobnitz", VIEW)).toEqual([]);
});

test("findInSessionMatches reads a delegate's brief only where the row shows it", () => {
  const shown = "alpha-brief";
  const clippedOff = "clippedneedle";
  const prompt = `${shown} ${"pad ".repeat(40)}${clippedOff}`;
  const model = modelWithTurns([
    turn("t1", [
      itemWith("call", {
        type: "commandExecution",
        toolName: "delegate",
        argumentsJSON: JSON.stringify({ prompt }),
      }),
    ]),
  ]);
  expect(findInSessionMatches(model, shown, VIEW).map((hit) => hit.itemId)).toEqual(["call"]);
  // The intent line clips the brief, so words past the preview are not on
  // screen for a result to land on.
  expect(findInSessionMatches(model, clippedOff, VIEW)).toEqual([]);
});

test("a match in both text and output yields two hits sharing the turn label", () => {
  const model = modelWithTurns([turn("t1", [itemWith("i1", { text: "frobnitz here", output: "frobnitz there" })])]);
  const hits = findInSessionMatches(model, "frobnitz", VIEW);
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

// "İ" lower-cases to "i" plus a combining dot, so an offset into a lower-cased
// copy is not an offset into the text a snippet or a highlight is cut from.
test("a hit after a character its lower case makes longer still marks the match", () => {
  const text = "İ needle";
  const hits = findInSessionMatches(modelWithTurns([turn("t1", [item("i1", text)])]), "needle", VIEW);
  expect(hits).toHaveLength(1);
  expect(hits[0]?.snippet.filter((part) => part.mark)).toEqual([{ text: "needle", mark: true }]);
  expect(highlightParts(text, "needle").filter((part) => part.mark)).toEqual([{ text: "needle", mark: true }]);
});

test("a reply is searched as it reads, the way the session's Find reads it", () => {
  const reply: ItemModel = { id: "i1", turnId: "t", type: "agentMessage", text: "the build **fail**ed" };
  const model = { turns: [{ id: "t", status: "completed", items: [reply] }] } as unknown as ThreadModel;
  expect(findInSessionMatches(model, "build failed", VIEW)).toHaveLength(1);
  expect(findInSessionMatches(model, "**fail**", VIEW)).toHaveLength(0);
});

// The palette reads the same projected words Find lands on: a notice's quiet
// line, a warning's block, a scaffold's rendered Markdown. It never offers a
// raw `item.text` the row does not show.
const SHOWN = makeTranscriptDisplayConfig(
  { kind: "preset", level: "tools" },
  { systemEvents: true, promptEvents: true, roundTimings: true },
);
const FULL_WARNINGS = makeTranscriptDisplayConfig({ kind: "preset", level: "full" }, { systemEvents: true });

test("findInSessionMatches offers a visible system notice only under the config that shows it", () => {
  const model = modelWithTurns([
    turn("t1", [
      itemWith("n", {
        type: "systemMessage",
        eventKind: "context_compaction",
        text: "model switched to opus",
      }),
    ]),
  ]);
  // The default advanced settings hide routine system events.
  expect(findInSessionMatches(model, "opus", VIEW)).toEqual([]);
  expect(findInSessionMatches(model, "opus", SHOWN).map((h) => h.itemId)).toEqual(["n"]);
});

test("findInSessionMatches offers a warning's block words and hides one that renders nothing", () => {
  const shown = modelWithTurns([
    turn("t1", [itemWith("w", { type: "warning", text: "disk almost full", warning: { title: "Evener error" } })]),
  ]);
  expect(findInSessionMatches(shown, "Evener error", CHAT).map((h) => h.itemId)).toEqual(["w"]);
  expect(findInSessionMatches(shown, "disk almost full", CHAT).map((h) => h.itemId)).toEqual(["w"]);

  // warningWords is null, so WarningItem renders null and Find has no row.
  const empty = modelWithTurns([turn("t1", [itemWith("x", { type: "warning", text: "", warning: {} })])]);
  expect(findInSessionMatches(empty, "warning", CHAT)).toEqual([]);

  // An informational warning shows only at Full, and its hint is on screen.
  const quiet = modelWithTurns([
    turn("t1", [
      itemWith("q", {
        type: "warning",
        text: "context usage is high",
        warning: { code: WarningCodeContextBudget, hint: "no action needed" },
      }),
    ]),
  ]);
  expect(findInSessionMatches(quiet, "no action needed", SHOWN)).toEqual([]);
  expect(findInSessionMatches(quiet, "no action needed", FULL_WARNINGS).map((h) => h.itemId)).toEqual(["q"]);
});

test("findInSessionMatches reads a scaffold as its rendered Markdown, never the raw source", () => {
  const model = modelWithTurns([
    turn("t1", [itemWith("p", { type: "systemMessage", eventKind: "system_prompt", text: "You are **bold** here" })]),
  ]);
  expect(findInSessionMatches(model, "bold here", SHOWN).map((h) => h.itemId)).toEqual(["p"]);
  expect(findInSessionMatches(model, "**bold**", SHOWN)).toEqual([]);
});

test("findInSessionMatches reads round timings as their headline, not the raw dump", () => {
  const model = modelWithTurns([
    turn("t1", [
      itemWith("r", {
        type: "systemMessage",
        eventKind: "round_timings",
        text: "Round 2 total=6.411312958s llm=4.935822084s",
        raw: { roundTimings: { round: 2, total_round_ns: 6_411_312_958, llm_call_ns: 4_935_822_084 } },
      }),
    ]),
  ]);
  expect(findInSessionMatches(model, "Round 2", SHOWN).map((h) => h.itemId)).toEqual(["r"]);
  expect(findInSessionMatches(model, "total=6.411", SHOWN)).toEqual([]);
});

// A steer's and a memory refresh's words are the ones their row shows. The
// palette offers only those, so every row it lists is one Find can land on.
function steerItem(id: string, text: string, fields: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "t", type: "steering", text, ...fields };
}

function memoryRefreshItem(id: string, text: string, content: string): ItemModel {
  return {
    id,
    turnId: "t",
    type: "systemMessage",
    eventKind: "memory-context",
    text,
    raw: { memoryContext: { scope: "personal", state: "current", truncated: false, content } },
  };
}

test("findInSessionMatches offers a steer the human typed, a human note and a memory refresh", () => {
  const model = modelWithTurns([
    turn("t1", [
      steerItem("s1", "rethink the frobnitz", { source: "user" }),
      steerItem("s2", "whiteboard: frobnitz plan", { source: "user", steeringKind: "human-note" }),
      memoryRefreshItem("m1", "recorded text", "- frobnitz index"),
    ]),
  ]);
  expect(findInSessionMatches(model, "frobnitz", TOOLS).map((hit) => hit.itemId)).toEqual(["s1", "s2", "m1"]);
});

test("findInSessionMatches offers only what a steer's row shows", () => {
  const model = modelWithTurns([
    turn("t1", [
      // Renders nothing: the tasks surfaces own it.
      steerItem("sup", "the frobnitz task", { steeringKind: "task-list" }),
      // The reminder wrapper is stripped on screen.
      steerItem("wrapped", "<SYSTEM-REMINDER>frobnitz nudge</SYSTEM-REMINDER>", { steeringKind: "nudge" }),
    ]),
  ]);
  expect(findInSessionMatches(model, "frobnitz", TOOLS).map((hit) => hit.itemId)).toEqual(["wrapped"]);
  expect(findInSessionMatches(model, "SYSTEM-REMINDER", TOOLS)).toEqual([]);
});

test("findInSessionMatches reads a memory refresh's index, never the folded source", () => {
  const model = modelWithTurns([turn("t1", [memoryRefreshItem("m1", "sourceFrobnitz", "- index frobnitz")])]);
  expect(findInSessionMatches(model, "index frobnitz", TOOLS).map((hit) => hit.itemId)).toEqual(["m1"]);
  expect(findInSessionMatches(model, "sourceFrobnitz", TOOLS)).toEqual([]);
});

// A steer's notification cards are offered by the words they show, never by
// the markup behind them, so every row the palette lists is one Find lands on.
test("findInSessionMatches reads a steer's notification card as its words, not its markup", () => {
  const card = notificationWireItem("delegate-reported") as unknown as ItemModel;
  const model = modelWithTurns([turn("t1", [{ ...card, id: "steer", turnId: "t1" }])]);
  expect(findInSessionMatches(model, "the settle pass now waits for the drain", VIEW).map((hit) => hit.itemId)).toEqual(
    ["steer"],
  );
  // The verbatim frame's attributes are not on the card.
  expect(findInSessionMatches(model, 'delegate_id="dlg_1"', VIEW)).toEqual([]);
  expect(findInSessionMatches(model, '"outcome":"completed"', VIEW)).toEqual([]);
});

test("findInSessionMatches reads a job card's rendered fields and excerpt, not its frame", () => {
  const text =
    '<job-notification job_id="job_7" event="completed" job_type="shell" status="completed" reason="exit_zero" intent="Run the agent tests">\nJob job_7 completed.\nexcerpt:\nok  \t12.3s\n</job-notification>';
  const model = modelWithTurns([turn("t1", [itemWith("steer", { type: "steering", text })])]);
  // The head's intent and the excerpt are on screen.
  expect(findInSessionMatches(model, "Run the agent tests", VIEW).map((hit) => hit.itemId)).toEqual(["steer"]);
  expect(findInSessionMatches(model, "12.3s", VIEW).map((hit) => hit.itemId)).toEqual(["steer"]);
  // The frame's own words are not.
  expect(findInSessionMatches(model, 'event="completed"', VIEW)).toEqual([]);
});
