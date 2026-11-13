import type { ItemModel, ProjectedEntry, ProjectedTurn, TurnModel } from "@evener/appwire-client";
import { expect, test } from "vitest";
import type { TranscriptBodyRow } from "../transcript/TranscriptBody";
import "../transcript/tools";
import { findHits } from "./findHits";

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

function tool(id: string, fields: Partial<ItemModel> = {}): ItemEntry {
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
      toolName: "read_file",
      status: "completed",
      ...fields,
    } as ItemModel,
  };
}

function intent(id: string, rationale: string): IntentEntry {
  return {
    kind: "intent",
    id: `intent:${id}`,
    turnId: "t",
    sourceIndex: 0,
    sourceItemId: id,
    rationale,
    failed: false,
    item: tool(id).item,
  };
}

function turnRow(id: string, entries: ProjectedEntry[], status: TurnModel["status"] = "completed"): TranscriptBodyRow {
  const source = { id, status, items: [] } as unknown as TurnModel;
  const turn: ProjectedTurn = { id, source, entries, visibleItems: [] };
  return { kind: "turn", id, turn, sourceTurnIndex: 0, showTurnSeparator: true };
}

test("hits are the entries holding the words, oldest first, each on its row", () => {
  const rows = [
    turnRow("t1", [message("u1", "Where is the Needle?", "userMessage"), message("a1", "Nowhere yet.")]),
    turnRow("t2", [message("u2", "Look again"), message("a2", "Found the\nneedle here.")]),
  ];
  expect(findHits(rows, "  the  NEEDLE ").map((hit) => [hit.key, hit.rowIndex])).toEqual([
    ["u1", 0],
    ["a2", 1],
  ]);
  expect(findHits(rows, "   ")).toEqual([]);
  expect(findHits(rows, "absent")).toEqual([]);
});

test("a message has nothing to open; a lone tool call opens its own body", () => {
  const rows = [turnRow("t1", [message("a1", "needle"), tool("x", { output: "a needle in the output" })])];
  const [inMessage, inTool] = findHits(rows, "needle");
  expect(inMessage?.opens).toEqual([]);
  expect(inTool?.opens).toEqual(["x"]);
  expect(inTool?.itemId).toBe("x");
});

test("a call folded into a run opens the run first, then its own body", () => {
  const run = [tool("a"), tool("b", { argumentsJSON: JSON.stringify({ path: "/src/needle.ts" }) }), tool("c")];
  const rows = [turnRow("t1", [message("u", "go", "userMessage"), ...run, message("done", "ok")])];
  const hits = findHits(rows, "needle");
  expect(hits.map((hit) => hit.key)).toEqual(["b"]);
  expect(hits[0]?.opens).toEqual(["run:a", "b"]);
});

test("a running turn's calls are not folded, so there is no run to open", () => {
  const run = [tool("a"), tool("b", { output: "needle" }), tool("c")];
  const rows = [turnRow("t1", run, "inProgress")];
  expect(findHits(rows, "needle")[0]?.opens).toEqual(["b"]);
});

test("an action inside a group opens the group, whether the group is a row or sits inside a turn", () => {
  const entries = [intent("p", "Read the config"), intent("q", "Search for the needle")];
  const crossTurn: TranscriptBodyRow = {
    kind: "intentGroup",
    id: "intent-group:intent:p",
    entries,
    sourceTurnIndexes: [0, 1],
    sourceTurnIds: ["t1", "t2"],
  };
  const inTurn = turnRow("t3", [message("u", "go", "userMessage"), ...entries, message("a", "done")]);
  const hits = findHits([crossTurn, inTurn], "needle");
  expect(hits.map((hit) => [hit.key, hit.itemId, hit.rowIndex])).toEqual([
    ["intent:q", "q", 0],
    ["intent:q", "q", 1],
  ]);
  for (const hit of hits) expect(hit.opens[0]).toBe("intent-group:intent:p");
});

test("a redacted thought is found by what it shows, never by its hidden text", () => {
  const thought = { id: "r", turnId: "t", type: "reasoning", text: "secret needle", status: "completed" } as ItemModel;
  const critical = (redacted: boolean): ProjectedEntry => ({
    kind: "critical",
    id: "r",
    turnId: "t",
    sourceIndex: 0,
    item: thought,
    summary: "Thinking failed",
    redacted,
  });
  expect(findHits([turnRow("t1", [critical(true)])], "needle")).toEqual([]);
  expect(findHits([turnRow("t1", [critical(true)])], "thinking failed")).toHaveLength(1);
  expect(findHits([turnRow("t1", [critical(false)])], "needle")).toHaveLength(1);
});
