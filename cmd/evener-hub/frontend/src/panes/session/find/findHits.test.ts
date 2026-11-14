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
import { findableItemIds, findHits } from "./findHits";
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
