import {
  type ItemModel,
  makeTranscriptDisplayConfig,
  type ProjectedEntry,
  type ProjectedTurn,
  projectThread,
  type ThreadModel,
  type TurnModel,
} from "@evener/appwire-client";
import { expect, test } from "vitest";
import { type TranscriptBodyRow, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import "../transcript/tools";
import { findableItemIds, findHits, indexOfMatch } from "./findHits";
import { messageAsShown } from "./messageText";

type ItemEntry = Extract<ProjectedEntry, { kind: "item" }>;

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

function turnRow(id: string, entries: ProjectedEntry[]): TranscriptBodyRow {
  const source = { id, status: "completed", items: [] } as unknown as TurnModel;
  const turn: ProjectedTurn = { id, source, entries, visibleItems: [] };
  return { kind: "turn", id, turn, sourceTurnIndex: 0, showTurnSeparator: true };
}

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

// The invariant the rest of Find leans on: a counted match is one Find can
// land on with its words on screen. Only a message's own text is ever read,
// so nothing folded, cut short, suppressed or behind a disclosure can count.
test("only the conversation's messages are searched", () => {
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
    expect(findHits(rows, "needle").map((hit) => hit.itemId)).toEqual(["u", "a"]);
    expect([...findableItemIds(model, config, "needle")].sort()).toEqual(["a", "u"]);
  }
  for (const item of items) {
    expect(messageAsShown(item) !== null).toBe(item.id === "u" || item.id === "a");
  }
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
