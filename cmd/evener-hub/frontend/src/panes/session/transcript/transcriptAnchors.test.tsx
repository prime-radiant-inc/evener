import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import { makeTranscriptDisplayConfig } from "../../../transcriptDisplay/config";
import type { ProjectedEntry, ProjectedTurn } from "../../../transcriptDisplay/projector";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
// Registers the tool descriptors (fsTools' read_file is fold: "quiet") the
// same way the real session pane does - through TurnBlock's side-effect
// import of ./tools.
import "./TurnBlock";
import {
  TranscriptBody,
  type TranscriptTurnRow,
  transcriptAnchorEntriesForRows,
  transcriptRunDisclosureIdsForRows,
} from "./TranscriptBody";

beforeEach(resetDisclosureStoreForTests);
afterEach(cleanup);

// description: a call with a stated intent projects as an ordinary item
// entry at the tool-call levels; an intent-less call projects as a
// "critical" entry (projector.ts's decisionFor), which never folds.
function toolItem(id: string, toolName: string, status = "completed"): ItemModel {
  return {
    id,
    turnId: "t1",
    type: "commandExecution",
    text: "",
    toolName,
    status,
    description: `Look at ${id}`,
    output: "ok",
  } as ItemModel;
}

function turnRow(items: ItemModel[], status: string): TranscriptTurnRow {
  const source: TurnModel = { id: "t1", status, items } as TurnModel;
  const entries: ProjectedEntry[] = items.map((item, sourceIndex) => ({
    kind: "item",
    id: item.id,
    turnId: "t1",
    sourceIndex,
    item,
    isMessage: false,
  }));
  const turn: ProjectedTurn = { id: "t1", source, entries, visibleItems: items };
  return { kind: "turn", id: "t1", turn, sourceTurnIndex: 0, showTurnSeparator: true };
}

// roborev on PR #947: TurnBlock renders a folded run under ONE anchor
// (run:<first entry>), so the anchor registry must advertise that anchor -
// not the three entry ids no element carries while the run is closed - or a
// restore after a remount looks for an id that is not in the DOM.
test("a settled run of quiet tool calls registers one anchor under the run id", () => {
  const items = [toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "glob")];
  const anchors = transcriptAnchorEntriesForRows([turnRow(items, "completed")]);
  // members: the ids the run stands in for, so a focus or scroll position
  // captured on the second or third call still resolves to the run.
  expect(anchors).toEqual([{ id: "run:a", sourceIndex: 0, index: 0, isMessage: false, members: ["a", "b", "c"] }]);
});

test("a live turn registers every entry, matching the rows TurnBlock renders while the agent works", () => {
  const items = [toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "glob")];
  const anchors = transcriptAnchorEntriesForRows([turnRow(items, "inProgress")]);
  expect(anchors.map((anchor) => anchor.id)).toEqual(["a", "b", "c"]);
});

test("a tool with no fold policy keeps its own anchor and breaks the run", () => {
  const items = [
    toolItem("a", "read_file"),
    toolItem("b", "mcp_deploy"),
    toolItem("c", "read_file"),
    toolItem("d", "glob"),
  ];
  const anchors = transcriptAnchorEntriesForRows([turnRow(items, "completed")]);
  expect(anchors.map((anchor) => anchor.id)).toEqual(["a", "b", "c", "d"]);
});

// roborev on PR #947 (round five): the Full-view baseline clears stale closed
// choices only for ids in the eligible inventory, and the projector's inventory
// knows source item ids, not the run:<first> ids ToolRunGroup mints.
test("a settled run's disclosure id joins the Full-baseline inventory; a live turn adds none", () => {
  const items = [toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "glob")];
  expect(transcriptRunDisclosureIdsForRows([turnRow(items, "completed")])).toEqual(["run:a"]);
  expect(transcriptRunDisclosureIdsForRows([turnRow(items, "inProgress")])).toEqual([]);
});

// The behaviour that inventory buys: close a folded run in Full, leave Full,
// come back - Full's "everything open" baseline reopens it.
test("re-entering Full view reopens a run the reader closed there", () => {
  const items = [
    { id: "u", turnId: "t1", type: "userMessage", text: "look around", status: "completed" },
    toolItem("a", "read_file"),
    toolItem("b", "read_file"),
    toolItem("c", "glob"),
  ] as ItemModel[];
  const model = {
    ref: "preview:runs",
    threadId: "thread_runs",
    name: "Runs",
    status: { type: "idle" },
    modelProvider: "preview",
    model: "preview-model",
    askPending: false,
    pendingEscalations: [],
    turns: [{ id: "t1", status: "completed", items }],
  } as unknown as ThreadModel;
  const preset = (level: "tools" | "full") => makeTranscriptDisplayConfig({ kind: "preset", level });
  const view = (level: "tools" | "full") => (
    <TranscriptBody model={model} config={preset(level)} surface="preview" disclosureScope="preview:runs" />
  );
  const { rerender } = render(view("full"));
  const run = () => screen.getByTestId("tool-run") as HTMLDetailsElement;
  expect(run().open).toBe(true);
  const summary = run().querySelector("summary");
  if (summary === null) throw new Error("the run rendered no summary");
  fireEvent.click(summary);
  expect(run().open).toBe(false);
  rerender(view("tools"));
  rerender(view("full"));
  expect(run().open).toBe(true);
});
