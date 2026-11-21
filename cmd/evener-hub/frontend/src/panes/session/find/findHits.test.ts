import {
  type ItemModel,
  makeTranscriptDisplayConfig,
  type ProjectedEntry,
  type ProjectedTurn,
  projectThread,
  type ThreadModel,
  type TurnModel,
} from "@evener/appwire-client";
import { act } from "@testing-library/react";
import { beforeEach, expect, test } from "vitest";
import { makeTranscriptPreviewModel } from "../../../transcriptDisplay/previewFixture";
import { type TranscriptBodyRow, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import "../transcript/tools";
import { noteMermaidRender, resetMermaidRenderStateForTests } from "../../../widgets/mermaid/renderState";
import { findableItemIds, findHits, indexOfMatch, isOverlay } from "./findHits";
import { messageAsShown } from "./messageText";

type ItemEntry = Extract<ProjectedEntry, { kind: "item" }>;
type IntentEntry = Extract<ProjectedEntry, { kind: "intent" }>;

function message(id: string, text: string, type = "agentMessage"): ItemEntry {
  return {
    kind: "item",
    id,
    turnId: "t",
    sourceIndex: 0,
    isMessage: true,
    item: { id, turnId: "t", type, text, status: "completed" } as ItemModel,
  };
}

function reasoningEntry(
  id: string,
  summaries: string[][],
  kind: "thinking" | "item" = "thinking",
  live = false,
): ProjectedEntry {
  const item = {
    id,
    turnId: "t",
    type: "reasoning",
    text: "",
    status: live ? "inProgress" : "completed",
    reasoningSummaries: summaries,
  } as ItemModel;
  return kind === "thinking"
    ? { kind, id, turnId: "t", sourceIndex: 0, item, live }
    : { kind: "item", id, turnId: "t", sourceIndex: 0, item, isMessage: false };
}

function redactedReasoning(id: string, summary: string, summaries: string[][], text = ""): ProjectedEntry {
  return {
    kind: "critical",
    id,
    turnId: "t",
    sourceIndex: 0,
    sourceItemId: id,
    item: {
      id,
      turnId: "t",
      type: "reasoning",
      text,
      status: "failed",
      reasoningSummaries: summaries,
    } as ItemModel,
    summary,
    redacted: true,
  };
}

function intentEntry(id: string, rationale: string): IntentEntry {
  return {
    kind: "intent",
    id: `intent:${id}`,
    turnId: "t",
    sourceIndex: 0,
    sourceItemId: id,
    rationale,
    failed: false,
    item: {
      id,
      turnId: "t",
      type: "commandExecution",
      text: "",
      toolName: "shell",
      description: rationale,
      status: "completed",
    } as ItemModel,
  };
}

/** The projector's shape for an action row with no stated rationale: its
 * rationale is the neutral fallback, and the row itself shows the descriptor's
 * own summary. */
function rationaleLessAction(id: string, toolName: string, argumentsJson: string): IntentEntry {
  return {
    kind: "intent",
    id: `intent:${id}`,
    turnId: "t",
    sourceIndex: 0,
    sourceItemId: id,
    rationale: "Action summary unavailable",
    failed: false,
    item: {
      id,
      turnId: "t",
      type: "commandExecution",
      text: "",
      toolName,
      argumentsJson,
      status: "completed",
    } as ItemModel,
  };
}

function suppressedAction(id: string, rationale: string, argumentsJson: string): IntentEntry {
  return {
    kind: "intent",
    id: `intent:${id}`,
    turnId: "t",
    sourceIndex: 0,
    sourceItemId: id,
    rationale,
    failed: false,
    item: {
      id,
      turnId: "t",
      type: "commandExecution",
      text: "",
      toolName: "task_list",
      argumentsJson,
      description: rationale,
      status: "completed",
    } as ItemModel,
  };
}

/** A tool call rendered on its own row (the tool-call levels project every
 * non-interaction call this way), with the fields each case needs. */
function toolCallEntry(id: string, fields: Partial<ItemModel> = {}): ProjectedEntry {
  return {
    kind: "item",
    id,
    turnId: "t",
    sourceIndex: 0,
    isMessage: false,
    item: {
      id,
      turnId: "t",
      type: "commandExecution",
      text: "",
      toolName: "shell",
      status: "completed",
      ...fields,
    } as ItemModel,
  };
}

/** A delegate call with no stated description: its intent line is the brief
 * the projector carries in the arguments, clipped to the row's preview. */
function delegateAction(id: string, prompt: string): IntentEntry {
  return {
    kind: "intent",
    id: `intent:${id}`,
    turnId: "t",
    sourceIndex: 0,
    sourceItemId: id,
    rationale: "Action summary unavailable",
    failed: false,
    item: {
      id,
      turnId: "t",
      type: "commandExecution",
      text: "",
      toolName: "delegate",
      argumentsJSON: JSON.stringify({ prompt }),
      status: "completed",
    } as ItemModel,
  };
}

function turnRow(id: string, entries: ProjectedEntry[], status = "completed"): TranscriptBodyRow {
  // The source turn knows the items it projected, which is how the live rule
  // tells a thought still at the turn's tail from one a later item overtook.
  const source = { id, status, items: entries.map((entry) => entry.item) } as unknown as TurnModel;
  const turn: ProjectedTurn = { id, source, entries, visibleItems: [] };
  return { kind: "turn", id, turn, sourceTurnIndex: 0, showTurnSeparator: true };
}

const TOOLS = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
const CHAT = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
const COLLAPSED_INTENT = makeTranscriptDisplayConfig({
  kind: "custom",
  toolIntent: true,
  toolCalls: false,
  reasoning: false,
  expandByDefault: false,
  thinkingOpenByDefault: false,
});

test("hits are the messages holding the words, oldest first, each on its row", () => {
  const rows = [
    turnRow("t1", [message("u1", "Where is the Needle?", "userMessage"), message("a1", "Nowhere yet.")]),
    turnRow("t2", [message("u2", "Look again", "userMessage"), message("a2", "Found the\nneedle here.")]),
  ];
  expect(findHits(rows, "  the  NEEDLE ").map((hit) => [hit.key, hit.itemId, hit.rowIndex])).toEqual([
    ["u1", "u1", 0],
    ["a2", "a2", 1],
  ]);
  expect(findHits(rows, "   ")).toEqual([]);
  expect(findHits(rows, "absent")).toEqual([]);
});

test("a hit goes by the entry's display key, which outlives the item's id", () => {
  const streamed = { ...message("item_9", "the needle"), displayKey: "round:3" };
  expect(findHits([turnRow("t", [streamed])], "needle")).toEqual([{ key: "round:3", itemId: "item_9", rowIndex: 0 }]);
});

test("a reply is searched as it reads, not as its Markdown is written", () => {
  const reply =
    'The build **fail**ed in `main.go`, see [the log](https://example.test/log).\n\n- first point\n- second point\n\n```\nraw **stars** stay\n```\n\n<div class="fake-root">nested</div>';
  const rows = [turnRow("t", [message("a", reply), message("u", "keep my **stars**", "userMessage")])];
  const keys = (query: string) => findHits(rows, query).map((hit) => hit.key);
  expect(keys("build failed in main.go, see the log")).toEqual(["a"]);
  // Formatting marks are not words, and a link's address is not on the page.
  expect(keys("**fail**")).toEqual([]);
  expect(keys("example.test")).toEqual([]);
  // Separate list items are separate words.
  expect(keys("first point second point")).toEqual(["a"]);
  expect(keys("pointsecond")).toEqual([]);
  // Code, authored HTML and a prompt are shown as written.
  expect(keys("raw **stars** stay")).toEqual(["a"]);
  expect(keys("fake-root")).toEqual(["a"]);
  expect(keys("**stars**")).toEqual(["a", "u"]);
});

test("an in-progress reply searches auto-closed Markdown as its live row reads", () => {
  const live = (id: string, source: string): ItemEntry => {
    const entry = message(id, "");
    return { ...entry, item: { ...entry.item, text: "", pendingText: [source], status: "inProgress" } };
  };
  const rows = [turnRow("t", [live("bold", "the answer is **bo"), live("code", "type `needle")])];
  expect(findHits(rows, "is bo").map((hit) => hit.key)).toEqual(["bold"]);
  expect(findHits(rows, "**bo")).toEqual([]);
  expect(findHits(rows, "type needle").map((hit) => hit.key)).toEqual(["code"]);
  expect(findHits(rows, "`needle")).toEqual([]);
  // A settled reply does not get an invented closing marker.
  expect(messageAsShown({ ...message("settled", "the answer is **bo").item })).toContain("**bo");
});

// The invariant the rest of Find leans on: a counted match is one Find can
// land on with its words on screen. A thought's summaries, an action's
// rationale and a tool call's own words are readable once their disclosures
// open, and so are a steer's visible text and a human note's divider body;
// notices and warnings are not searched by this slice.
test("messages, thought summaries, action rationales, tool calls and steers are searched", () => {
  const items = [
    { id: "u", type: "userMessage", text: "find the needle" },
    { id: "think", type: "reasoning", text: "needle", reasoningSummaries: [["a needle thought"]] },
    {
      id: "call",
      type: "commandExecution",
      text: "",
      toolName: "shell",
      argumentsJSON: '{"command":"grep needle"}',
      output: "needle",
    },
    { id: "steer", type: "steering", text: "needle from the parent", source: "parent" },
    { id: "note", type: "steering", text: "needle note", source: "user", steeringKind: "human-note" },
    { id: "notice", type: "systemMessage", text: "needle event" },
    { id: "warn", type: "warning", text: "needle warning" },
    { id: "a", type: "agentMessage", text: "No needle to report." },
  ].map((item) => ({ turnId: "t1", status: "completed", ...item })) as ItemModel[];
  const model = { ref: "r", turns: [{ id: "t1", status: "completed", items }] } as unknown as ThreadModel;
  for (const level of ["chat", "tools", "full"] as const) {
    const config = makeTranscriptDisplayConfig({ kind: "preset", level });
    const rows = transcriptRowsForProjection(projectThread(model, config));
    // Chat hides a settled thought and projects no tool row; the other levels
    // show the thought's summary, and the call's summary and output hold the
    // words. A parent's steer and a human note show at every level.
    const expected = level === "chat" ? ["u", "steer", "note", "a"] : ["u", "think", "call", "steer", "note", "a"];
    expect(findHits(rows, "needle", { config }).map((hit) => hit.itemId)).toEqual(expected);
    expect([...findableItemIds(model, config, "needle")].sort()).toEqual([...expected].sort());
  }
  for (const item of items) {
    expect(messageAsShown(item) !== null).toBe(item.id === "u" || item.id === "a");
  }
});

test("a thought is searched by its rendered summary and its hit opens the thought", () => {
  const rows = [turnRow("t", [reasoningEntry("r1", [["the **needle** sits here"]])])];
  expect(findHits(rows, "needle sits here", { config: TOOLS })).toEqual([
    { key: "r1", itemId: "r1", rowIndex: 0, opens: ["r1"] },
  ]);
  // The summary's own formatting marks are not words on the page.
  expect(findHits(rows, "**needle**", { config: TOOLS })).toEqual([]);
});

// A view that hides thinking traces shows only a placeholder, so the thought's
// summary is not a match there (the chat preset).
test("a thought is not searched where the chat view hides thinking traces", () => {
  const rows = [turnRow("t", [reasoningEntry("r1", [["a hidden needle"]])])];
  expect(findHits(rows, "needle", { config: CHAT })).toEqual([]);
  expect(findHits(rows, "needle", { config: TOOLS }).map((hit) => hit.key)).toEqual(["r1"]);
});

// Reasoning rendered with its content flag on is an item entry rather than a
// compact trace; it is read the same way and opens the same disclosure.
test("a full-verbosity thought is searched by its summary", () => {
  const full = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const rows = [turnRow("t", [reasoningEntry("r1", [["a needle thought"]], "item")])];
  expect(findHits(rows, "needle thought", { config: full })).toEqual([
    { key: "r1", itemId: "r1", rowIndex: 0, opens: ["r1"] },
  ]);
});

// A redacted critical thought shows the projector's neutral label verbatim
// and can unfold its Markdown summaries. Its raw `text` never renders, so it
// is not searched.
test("a redacted thought is searched by its rendered label and summaries", () => {
  const rows = [
    turnRow("t", [
      redactedReasoning("r1", "Thought failed in main.go", [["the *needle* body"]], "raw needle that never renders"),
    ]),
  ];
  const keys = (query: string) => findHits(rows, query, { config: TOOLS }).map((hit) => hit.key);
  expect(keys("failed in main.go")).toEqual(["r1"]);
  expect(keys("**failed**")).toEqual([]);
  expect(keys("needle body")).toEqual(["r1"]);
  expect(keys("raw needle")).toEqual([]);
});

test("an action is searched by its rationale and the hit opens its collapsed group", () => {
  const rows = [turnRow("t", [intentEntry("c1", "list the repo"), intentEntry("c2", "grep for the needle")])];
  expect(findHits(rows, "grep for the needle", { config: COLLAPSED_INTENT })).toEqual([
    { key: "intent:c2", itemId: "c2", rowIndex: 0, opens: ["intent-group:intent:c1"] },
  ]);
  // A cross-turn action group names its own row id.
  const entries: IntentEntry[] = [intentEntry("c1", "list the repo"), intentEntry("c2", "grep for the needle")];
  const group: TranscriptBodyRow = {
    kind: "intentGroup",
    id: "intent-group:intent:c1",
    entries,
    sourceTurnIndexes: [0],
    sourceTurnIds: ["t"],
  };
  expect(findHits([group], "needle", { config: COLLAPSED_INTENT }).map((hit) => hit.opens)).toEqual([
    ["intent-group:intent:c1"],
  ]);
});

test("an action row with no stated rationale is not searched by the projector's fallback", () => {
  const rows = [turnRow("t", [rationaleLessAction("c1", "read_file", '{"file_path":"agent/tree.go"}')])];
  // The opened group shows read_file's own summary, never this neutral text.
  expect(findHits(rows, "action", { config: COLLAPSED_INTENT })).toEqual([]);
  expect(findHits(rows, "unavailable", { config: COLLAPSED_INTENT })).toEqual([]);
});

test("a suppressed action row is not searched, because it renders nothing", () => {
  const rows = [turnRow("t", [suppressedAction("c1", "view the task list", '{"action":"view"}')])];
  expect(findHits(rows, "view the task list", { config: COLLAPSED_INTENT })).toEqual([]);
});

// A tool call is searched by the three texts its row can paint: the intent
// line (always on screen), the summary line (behind its own disclosure), and
// the body - output, error, and the arguments a body renders.
test("a tool call is searched where its row shows each text, and its hit opens what hides it", () => {
  const rows = [
    turnRow("t", [
      toolCallEntry("c1", {
        description: "list the sources",
        argumentsJSON: '{"command":"ls src"}',
        output: "frobnitz.txt",
      }),
    ]),
  ];
  const hit = (query: string) => findHits(rows, query, { config: TOOLS }).map((found) => [found.itemId, found.opens]);
  // The intent line is on screen at every level.
  expect(hit("list the sources")).toEqual([["c1", undefined]]);
  // The summary line quotes the command; landing opens that line.
  expect(hit("ls src")).toEqual([["c1", ["summary:c1"]]]);
  // Output sits behind the body, which the landing opens.
  expect(hit("frobnitz.txt")).toEqual([["c1", ["c1"]]]);
  // The text Find cannot land on is not counted at all.
  expect(hit("command")).toEqual([]);
});

test("a tool call inside a folded run opens the run before the call's own body", () => {
  const rows = [
    turnRow("t", [
      toolCallEntry("c1", { argumentsJSON: '{"command":"echo one"}', output: "one" }),
      toolCallEntry("c2", { argumentsJSON: '{"command":"echo two"}', output: "the needle" }),
      toolCallEntry("c3", { argumentsJSON: '{"command":"echo three"}', output: "three" }),
    ]),
  ];
  expect(findHits(rows, "needle", { config: TOOLS })).toEqual([
    { key: "c2", itemId: "c2", rowIndex: 0, opens: ["run:c1", "c2"] },
  ]);
});

test("coloured tool output is searched with its terminal controls interpreted", () => {
  const rows = [
    turnRow("t", [
      toolCallEntry("c1", {
        argumentsJSON: '{"command":"make"}',
        output: "error \u001b[31mcode\u001b[0m here",
      }),
    ]),
  ];
  // The escape sequence sits between the two words, so only the interpreted
  // text holds the phrase.
  expect(findHits(rows, "error code here", { config: TOOLS }).map((hit) => hit.itemId)).toEqual(["c1"]);
});

// The argument values are words a body renders (the generic arguments block);
// the JSON around them - keys, braces, quotes - is not.
test("argument values are searched where the row renders its arguments, and no JSON syntax is", () => {
  const rows = [
    turnRow("t", [
      toolCallEntry("c1", {
        toolName: "github: create_issue",
        argumentsJSON: JSON.stringify({ title: "Add needle support", labels: ["bug"] }),
      }),
    ]),
  ];
  const keys = (query: string) => findHits(rows, query, { config: TOOLS }).map((hit) => hit.itemId);
  expect(keys("needle support")).toEqual(["c1"]);
  expect(keys("labels")).toEqual([]);
  expect(keys('{"title"')).toEqual([]);
});

test("a suppressed tool call is not searched, because it renders nothing", () => {
  const rows = [
    turnRow("t", [
      toolCallEntry("c1", { toolName: "task_list", argumentsJSON: '{"action":"view"}', output: "needle" }),
    ]),
  ];
  expect(findHits(rows, "needle", { config: TOOLS })).toEqual([]);
});

// A delegate with no stated description shows its brief, clipped to the intent
// line's preview width: the row's own words are the only text Find may count.
test("a delegate's clipped brief is searched where the intent line shows it", () => {
  const shown = "alpha-brief";
  const clippedOff = "clippedneedle";
  const prompt = `${shown} ${"pad ".repeat(40)}${clippedOff}`;
  const rows = [turnRow("t", [delegateAction("c1", prompt)])];

  expect(findHits(rows, shown, { config: COLLAPSED_INTENT }).map((hit) => [hit.itemId, hit.opens])).toEqual([
    ["c1", ["intent-group:intent:c1"]],
  ]);
  // Past the preview the row shows an ellipsis, so the clipped-off words are
  // not text a match could land on.
  expect(findHits(rows, clippedOff, { config: COLLAPSED_INTENT })).toEqual([]);
});

test("a streaming thought's unclosed Markdown is read as its live row shows it", () => {
  const live = [turnRow("t", [reasoningEntry("r1", [["check **the assumptions"]], "thinking", true)], "inProgress")];
  expect(findHits(live, "check the assumptions", { config: TOOLS }).map((hit) => hit.key)).toEqual(["r1"]);
  // A settled thought shows the unbalanced marker literally, so the phrase
  // without it is not on screen.
  const settled = [turnRow("t", [reasoningEntry("r1", [["check **the assumptions"]])])];
  expect(findHits(settled, "check the assumptions", { config: TOOLS })).toEqual([]);
});

// The wire never completes a reasoning item, so a thought reads live only
// while it is still the turn's tail, as ThinkBlock decides it: once a reply
// starts in the same in-progress turn, the thought renders settled.
test("a thought a later item has overtaken is read settled, not live", () => {
  const thought = reasoningEntry("r1", [["check **the assumptions"]], "thinking", true);
  const reply = message("a1", "Answering now");
  const streaming: ProjectedEntry = { ...reply, item: { ...reply.item, status: "inProgress" } };
  const overtaken = [turnRow("t", [thought, streaming], "inProgress")];
  expect(findHits(overtaken, "check the assumptions", { config: TOOLS })).toEqual([]);
  expect(findHits(overtaken, "check **the assumptions", { config: TOOLS }).map((hit) => hit.key)).toEqual(["r1"]);
  // While the thought is still the turn's tail it reads live.
  const tail = [turnRow("t", [thought], "inProgress")];
  expect(findHits(tail, "check the assumptions", { config: TOOLS }).map((hit) => hit.key)).toEqual(["r1"]);
});

test("a diagram's source is not searched once the reply shows the diagram instead", () => {
  const fence = "```";
  const drawn = message("drawn", `The flow:\n\n${fence}mermaid\nflowchart TD\n  A --> B\n${fence}\n\nDone.`);
  // An unfinished fence still shows its source, as any code block does.
  const streaming = message("streaming", `The flow:\n\n${fence}mermaid\nflowchart TD\n  A --> B`);
  const rows = [turnRow("t", [drawn, streaming])];
  expect(findHits(rows, "flowchart TD").map((hit) => hit.key)).toEqual(["streaming"]);
  expect(findHits(rows, "the flow").map((hit) => hit.key)).toEqual(["drawn", "streaming"]);
});

test("a live Mermaid fence remains searchable until its real closing fence arrives", () => {
  const source = "```mermaid\nflowchart TD\n  A --> B";
  const live = (id: string, text: string): ItemEntry => {
    const entry = message(id, "");
    return { ...entry, item: { ...entry.item, text: "", pendingText: [text], status: "inProgress" } };
  };
  const rows = [turnRow("t", [live("open", source), live("closed", `${source}\n\`\`\``)])];
  expect(findHits(rows, "flowchart TD").map((hit) => hit.key)).toEqual(["open"]);
});

// A fenced block's row shows its language in a header above the code, so the
// words there are on screen and Find must be able to land on them. A reader
// that skipped the header would count fewer matches than the page shows.
test("a fenced block's language label is on screen and searched", () => {
  const fence = "```";
  const reply = `Try this:\n\n${fence}go\nfunc main() {}\n${fence}\n\nThat is all.`;
  const rows = [turnRow("t", [message("a", reply)])];
  expect(findHits(rows, "go").map((hit) => hit.key)).toEqual(["a"]);
  expect(findHits(rows, "func main").map((hit) => hit.key)).toEqual(["a"]);
  // The label is its own word, so it does not run on into the code.
  expect(findHits(rows, "gofunc")).toEqual([]);
});

test("a character reference in a reply is searched as the character it shows", () => {
  const reply = "The R&amp;D report costs &#36;5 &lt;today&gt;.\n\n`a &amp; b` stays code, and \\&amp; stays escaped.";
  const rows = [turnRow("t", [message("a", reply), message("u", "typed &amp; kept", "userMessage")])];
  const keys = (query: string) => findHits(rows, query).map((hit) => hit.key);
  expect(keys("R&D report costs $5 <today>")).toEqual(["a"]);
  // The reference's own letters are not on the page.
  expect(keys("R&amp;D")).toEqual([]);
  expect(keys("report costs &#36;")).toEqual([]);
  // Code, an escaped reference and a prompt show theirs as written.
  expect(keys("a &amp; b")).toEqual(["a"]);
  expect(keys("&amp; stays escaped")).toEqual(["a"]);
  expect(keys("typed &amp; kept")).toEqual(["u"]);
  // An unterminated reference stays literal in the rendered reply.
  const malformed = [turnRow("t", [message("unfinished", "costs &#36")])];
  expect(findHits(malformed, "$")).toEqual([]);
  expect(findHits(malformed, "&#36").map((hit) => hit.key)).toEqual(["unfinished"]);
});

test("the current match is found again by its message, whatever key that message goes by", () => {
  const reply = (id: string, text: string, fields: Partial<ItemModel> = {}, displayKey?: string): ItemEntry => {
    const entry = message(id, text);
    return { ...entry, displayKey, item: { ...entry.item, ...fields } };
  };
  const hitsOf = (...entries: ItemEntry[]) => findHits([turnRow("t", entries)], "needle");

  // The same item under a new key: an earlier reply of its round took the round's key.
  const renamed = hitsOf(reply("first", "needle one", {}, "round:r1"), reply("second", "needle two"));
  expect(indexOfMatch(renamed, { key: "round:r1", itemId: "second" })).toBe(1);
  // The key now belongs to a different reply and the chosen one is not loaded.
  const taken = hitsOf(reply("second", "needle two", {}, "round:r1"));
  expect(indexOfMatch(taken, { key: "round:r1", itemId: "first" })).toBe(-1);

  // A streamed reply being recorded keeps its key and takes a new item.
  const recorded = hitsOf(reply("m1", "needle streamed", {}, "round:r2"));
  expect(indexOfMatch(recorded, { key: "round:r2", itemId: "stream:r2/0:agentMessage" })).toBe(0);

  // An overlay id names live state no refresh can bring back, unlike a
  // recorded item id, whatever it looks like.
  expect(isOverlay("stream:r2/0:agentMessage")).toBe(true);
  expect(isOverlay("preview:c1")).toBe(true);
  expect(isOverlay("m1")).toBe(false);
  // A stream of several text runs is recorded as several messages, and only
  // the first keeps the round's key. A match in a later run is found by its round.
  const laterRun = hitsOf(reply("m3", "needle in the second run", { roundId: "r2" }));
  expect(indexOfMatch(laterRun, { key: "round:r2", itemId: "stream:r2/0:agentMessage", roundId: "r2" })).toBe(0);
  const otherRound = hitsOf(reply("m4", "needle elsewhere", { roundId: "r9" }));
  expect(indexOfMatch(otherRound, { key: "round:r2", itemId: "stream:r2/0:agentMessage", roundId: "r2" })).toBe(-1);
  // A communicate preview being recorded takes a new item and a new key; its call says which.
  const delivered = hitsOf(
    reply("other", "needle other", { callId: "c0" }),
    reply("m2", "needle said", { callId: "c1" }),
  );
  expect(indexOfMatch(delivered, { key: "preview:c1", itemId: "preview:c1" })).toBe(1);
  // While the preview is still the loaded item it is found as itself.
  const previewing = hitsOf(reply("preview:c1", "needle said", { callId: "c1" }));
  expect(indexOfMatch(previewing, { key: "preview:c1", itemId: "preview:c1" })).toBe(0);
  // A preview whose message is not loaded is not some other call's message.
  expect(
    indexOfMatch(hitsOf(reply("other", "needle other", { callId: "c0" })), { key: "preview:c1", itemId: "preview:c1" }),
  ).toBe(-1);
});

beforeEach(() => {
  resetMermaidRenderStateForTests();
});

// A diagram that fails to render falls back to a code block holding its
// source, so its words are on screen and Find has to count them.
test("a failed diagram's source becomes searchable when it falls back to code", () => {
  const fence = "```";
  const source = "flowchart TD\n  A --> secretNode";
  const reply = `The flow:\n\n${fence}mermaid\n${source}\n${fence}\n\nDone.`;
  const rows = [turnRow("t", [message("drawn", reply)])];
  expect(findHits(rows, "secretNode")).toEqual([]);
  act(() => noteMermaidRender(source, "error"));
  expect(findHits(rows, "secretNode").map((hit) => hit.key)).toEqual(["drawn"]);
  // A diagram that starts rendering again stops being a code block.
  act(() => noteMermaidRender(source, "ok"));
  expect(findHits(rows, "secretNode")).toEqual([]);
});

// The fallback shows the same language header a mermaid fenced block does, so
// "mermaid" is a word on screen and Find has to land on it.
test("a failed diagram's language header becomes searchable too", () => {
  const fence = "```";
  const source = "flowchart TD\n  A --> secretNode";
  const reply = `The flow:\n\n${fence}mermaid\n${source}\n${fence}\n\nDone.`;
  const rows = [turnRow("t", [message("drawn", reply)])];
  expect(findHits(rows, "mermaid")).toEqual([]);
  act(() => noteMermaidRender(source, "error"));
  expect(findHits(rows, "mermaid").map((hit) => hit.key)).toEqual(["drawn"]);
  act(() => noteMermaidRender(source, "ok"));
  expect(findHits(rows, "mermaid")).toEqual([]);
});

// A thought's summary is Markdown too, so a diagram inside one falls back and
// makes its source words visible the same way - and the thought's cached text
// has to be read again after that change.
test("a thought's failed diagram source becomes searchable when it falls back to code", () => {
  const fence = "```";
  const source = "flowchart TD\n  A --> thoughtNode";
  const summary = `The flow:\n\n${fence}mermaid\n${source}\n${fence}\n\nDone.`;
  const rows = [turnRow("t", [reasoningEntry("r1", [[summary]])])];
  // First read warms the thought's cache while the diagram still renders.
  expect(findHits(rows, "thoughtNode", { config: TOOLS })).toEqual([]);
  act(() => noteMermaidRender(source, "error"));
  expect(findHits(rows, "thoughtNode", { config: TOOLS }).map((hit) => hit.key)).toEqual(["r1"]);
  act(() => noteMermaidRender(source, "ok"));
  expect(findHits(rows, "thoughtNode", { config: TOOLS })).toEqual([]);
});

// A steer and a memory refresh fold their words behind a divider keyed by the
// session ref plus the row's own id (SteeringDivider), not by the view's
// disclosure scope, so a hit names those ids for Find to open.
function projectedRows(items: ItemModel[]): readonly TranscriptBodyRow[] {
  const model = {
    ...makeTranscriptPreviewModel(),
    ref: "ref_a",
    turns: [{ id: "t1", status: "completed", items } as TurnModel],
  };
  return transcriptRowsForProjection(projectThread(model, TOOLS));
}

function steerItem(id: string, fields: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "t1", type: "steering", text: "", status: "completed", ...fields };
}

test("a steer's hit names the session-scoped divider that folds its words", () => {
  const rows = projectedRows([steerItem("s1", { text: "the brass widget" })]);
  expect(findHits(rows, "brass", { config: TOOLS }).map((hit) => [hit.itemId, hit.opensSession])).toEqual([
    ["s1", ["s1"]],
  ]);
  // A steer the human typed is a prompt bubble with no fold to open.
  const prompt = projectedRows([steerItem("s2", { text: "the brass widget", source: "user" })]);
  expect(findHits(prompt, "brass", { config: TOOLS }).map((hit) => hit.opensSession)).toEqual([undefined]);
});

test("a suppressed task steer is not a hit at all", () => {
  const rows = projectedRows([steerItem("sup", { text: "the brass widget", steeringKind: "task-list" })]);
  expect(findHits(rows, "brass", { config: TOOLS })).toEqual([]);
});

test("a steer's interstitial text span opens only its own divider", () => {
  const card = `<job-notification job_id="j5" event="failed" job_type="shell" status="failed" reason="bad" output_bytes="1" exit_code="3">
Job job_5 failed.
</job-notification>`;
  const rows = projectedRows([steerItem("mix", { text: `first brass\n\n${card}\n\nsecond brass` })]);
  expect(findHits(rows, "second brass", { config: TOOLS }).map((hit) => hit.opensSession)).toEqual([["mix:2"]]);
});

function memoryItem(id: string, text: string, content: string): ItemModel {
  return {
    id,
    turnId: "t1",
    type: "systemMessage",
    eventKind: "memory-context",
    text,
    status: "completed",
    raw: { memoryContext: { scope: "personal", state: "current", truncated: false, content } },
  };
}

test("a memory refresh is searched by its index, never by its folded source", () => {
  const rows = projectedRows([memoryItem("m1", "sourceOnlyWord", "- brass widget")]);
  expect(findHits(rows, "brass widget", { config: TOOLS }).map((hit) => [hit.itemId, hit.opensSession])).toEqual([
    ["m1", ["m1"]],
  ]);
  expect(findHits(rows, "sourceOnlyWord", { config: TOOLS })).toEqual([]);
});

test("a memory refresh's failed diagram source is counted once it falls back", () => {
  const fence = "```";
  const source = "flowchart TD\n  A --> memoryNode";
  const rows = projectedRows([memoryItem("m1", "recorded", `The index:\n\n${fence}mermaid\n${source}\n${fence}\n`)]);
  expect(findHits(rows, "memoryNode", { config: TOOLS })).toEqual([]);
  act(() => noteMermaidRender(source, "error"));
  expect(findHits(rows, "memoryNode", { config: TOOLS }).map((hit) => hit.itemId)).toEqual(["m1"]);
  act(() => noteMermaidRender(source, "ok"));
  expect(findHits(rows, "memoryNode", { config: TOOLS })).toEqual([]);
});
