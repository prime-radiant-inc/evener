import {
  type ItemModel,
  makeTranscriptDisplayConfig,
  type ProjectedEntry,
  type ProjectedTurn,
  steeringNotificationFragments,
  type ThreadModel,
  type TurnModel,
} from "@evener/appwire-client";
import { expect, test } from "vitest";
import { notificationDisclosureId } from "../transcript/messages/NotificationCard";
import type { TranscriptBodyRow } from "../transcript/TranscriptBody";
import "../transcript/tools";
import { findableItemIds, findHits } from "./findHits";

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
  // A call's request sits behind its own summary disclosure, its result
  // behind the body.
  expect(inTool?.opens).toEqual(["x", "summary:x"]);
  expect(inTool?.itemId).toBe("x");
});

test("a call folded into a run opens the run first, then its own body", () => {
  const run = [tool("a"), tool("b", { argumentsJSON: JSON.stringify({ path: "/src/needle.ts" }) }), tool("c")];
  const rows = [turnRow("t1", [message("u", "go", "userMessage"), ...run, message("done", "ok")])];
  const hits = findHits(rows, "needle");
  expect(hits.map((hit) => hit.key)).toEqual(["b"]);
  expect(hits[0]?.opens).toEqual(["run:a", "b", "summary:b"]);
});

test("a running turn's calls are not folded, so there is no run to open", () => {
  const run = [tool("a"), tool("b", { output: "needle" }), tool("c")];
  const rows = [turnRow("t1", run, "inProgress")];
  expect(findHits(rows, "needle")[0]?.opens).toEqual(["b", "summary:b"]);
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

test("a redacted thought is found by what it can show: its label and its summaries, never its raw text", () => {
  const thought = {
    id: "r",
    turnId: "t",
    type: "reasoning",
    text: "secret needle",
    reasoningSummaries: [["Weighing the ", "haystack"]],
    status: "completed",
  } as ItemModel;
  const critical = (redacted: boolean): ProjectedEntry => ({
    kind: "critical",
    id: "r",
    turnId: "t",
    sourceIndex: 0,
    item: thought,
    summary: "Thinking failed",
    redacted,
  });
  const found = (redacted: boolean, query: string) => findHits([turnRow("t1", [critical(redacted)])], query).length;
  expect(found(true, "needle")).toBe(0);
  expect(found(true, "thinking failed")).toBe(1);
  // The summaries unfold inside the redacted thought, so they are findable.
  expect(found(true, "weighing the haystack")).toBe(1);
  // Shown in full, a thought still renders its summaries and nothing else.
  expect(found(false, "weighing the haystack")).toBe(1);
  expect(found(false, "needle")).toBe(0);
});

test("only what the current verbosity shows can be found in a thread", () => {
  const model = {
    ref: "r",
    turns: [
      {
        id: "t1",
        status: "completed",
        items: [
          { id: "u", turnId: "t1", type: "userMessage", text: "find the quokka", status: "completed" },
          {
            id: "c",
            turnId: "t1",
            type: "reasoning",
            text: "Where would a quokka be?",
            reasoningSummaries: [["Where would a quokka be?"]],
            status: "completed",
          },
          { id: "a", turnId: "t1", type: "agentMessage", text: "No quokka to report.", status: "completed" },
        ],
      },
    ],
  } as unknown as ThreadModel;
  const shown = (level: "chat" | "full") =>
    [...findableItemIds(model, makeTranscriptDisplayConfig({ kind: "preset", level }), "quokka")].sort();
  expect(shown("full")).toEqual(["a", "c", "u"]);
  // Chat verbosity leaves settled thoughts out, so the thought is not a destination.
  expect(shown("chat")).toEqual(["a", "u"]);
});

test("a call whose row the transcript never renders is not a match", () => {
  const view = tool("v", {
    toolName: "task_list",
    argumentsJSON: JSON.stringify({ action: "view" }),
    output: "1. find the quokka",
  });
  const rows = [turnRow("t1", [message("u", "quokka?", "userMessage"), view])];
  expect(findHits(rows, "quokka").map((found) => found.key)).toEqual(["u"]);
});

test("a steer or a memory refresh names the divider its words sit behind", () => {
  const steer = (id: string, text: string, fields: Partial<ItemModel> = {}): ItemEntry => {
    const entry = message(id, text, "steering");
    return { ...entry, isMessage: false, item: { ...entry.item, ...fields } };
  };
  const delivered =
    'before the needle\n<job-notification job_id="job_1" event="completed">Done.</job-notification>\nafter it';
  const rows = [
    turnRow("t", [
      steer("whole", "a needle from above", { source: "parent" }),
      steer("cut", delivered),
      steer("memory", "needle remembered", { type: "systemMessage", eventKind: "memory-context" }),
      tool("call", { output: "needle" }),
    ]),
  ];
  const opened = findHits(rows, "needle").map((hit) => [hit.itemId, hit.sessionOpens]);
  // Only the piece of a cut-up steer that holds the words is opened.
  expect(opened).toEqual([
    ["whole", ["whole"]],
    ["cut", ["cut:0"]],
    ["memory", ["memory"]],
    ["call", []],
  ]);
});

test("a reply is searched as it reads, not as its Markdown is written", () => {
  const reply =
    "The build **fail**ed in `main.go`, see [the log](https://example.test/log).\n\n- first point\n- second point\n\n```\nraw **stars** stay\n```";
  const rows = [turnRow("t", [message("a", reply), message("u", "keep my **stars**", "userMessage")])];
  const keys = (query: string) => findHits(rows, query).map((hit) => hit.key);
  expect(keys("build failed in main.go, see the log")).toEqual(["a"]);
  // Formatting marks are not words, and a link's address is not on the page.
  expect(keys("**fail**")).toEqual([]);
  expect(keys("example.test")).toEqual([]);
  // Separate list items are separate words.
  expect(keys("first point second point")).toEqual(["a"]);
  expect(keys("pointsecond")).toEqual([]);
  // Code, and a message shown as typed, keep their marks.
  expect(keys("raw **stars** stay")).toEqual(["a"]);
  expect(keys("**stars**")).toEqual(["a", "u"]);
});

test("an entry its renderer leaves blank is not a match", () => {
  const steer = (id: string, fields: Partial<ItemModel>): ItemEntry => {
    const entry = message(id, "the needle task", "steering");
    return { ...entry, isMessage: false, item: { ...entry.item, ...fields } };
  };
  const thought = {
    id: "th",
    turnId: "t",
    type: "reasoning",
    text: "",
    reasoningSummaries: [["a needle thought"]],
    status: "inProgress",
  } as ItemModel;
  const thinking: ProjectedEntry = {
    kind: "thinking",
    live: true,
    id: "th",
    turnId: "t",
    sourceIndex: 0,
    item: thought,
  };
  const rows = [
    turnRow("t", [
      steer("task", { steeringKind: "current-task" }),
      steer("note", { steeringKind: "human-note" }),
      thinking,
    ]),
  ];
  const keys = (level: "chat" | "tools") =>
    findHits(rows, "needle", { config: makeTranscriptDisplayConfig({ kind: "preset", level }) }).map((hit) => hit.key);
  // The daemon's task bookkeeping never renders; a thought unfolds everywhere but Chat.
  expect(keys("tools")).toEqual(["note", "th"]);
  expect(keys("chat")).toEqual(["note"]);
});

test("words inside a notification card open that card", () => {
  const text =
    'before\n<job-notification job_id="job_1" event="completed">The needle finished.</job-notification>\nafter';
  const entry = message("cut", text, "steering");
  const rows = [turnRow("t", [{ ...entry, isMessage: false }])];
  const pieces = steeringNotificationFragments(text) ?? [];
  const at = pieces.findIndex((piece) => piece.kind === "notification");
  const piece = pieces[at];
  if (piece?.kind !== "notification") throw new Error("fixture holds no notification");
  const [found] = findHits(rows, "needle finished", { sessionRef: "s" });
  expect(found?.opens).toContain(notificationDisclosureId(piece.notification, "s", `cut:${at}`));
  expect(found?.sessionOpens).toEqual([]);
});

test("a notice folded into a group lands on the group and opens it", () => {
  const notice = (id: string, text: string) => message(id, text, "systemMessage");
  const entries = [notice("n1", "first event"), notice("n2", "second event"), notice("n3", "third needle event")];
  const row = turnRow("t", entries);
  if (row.kind !== "turn") throw new Error("not a turn row");
  row.turn.source.items = entries.map((entry) => entry.item);
  const [found] = findHits([row], "needle");
  expect(found?.key).toBe("n3");
  // The group renders under its first member, behind that member's disclosure.
  expect(found?.anchor).toBe("n1");
  expect(found?.opens).toContain("n1");
  // Two notices do not group: each is its own line.
  const pair = turnRow("t2", entries.slice(1));
  if (pair.kind !== "turn") throw new Error("not a turn row");
  pair.turn.source.items = entries.slice(1).map((entry) => entry.item);
  expect(findHits([pair], "needle")[0]?.anchor).toBeUndefined();
});
