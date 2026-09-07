import { expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import type { ProjectedEntry, ProjectedTurn } from "../../../transcriptDisplay/projector";
// Registers the tool descriptors (fsTools' read_file is fold: "quiet") the
// same way the real session pane does - through TurnBlock's side-effect
// import of ./tools.
import "./TurnBlock";
import { type TranscriptTurnRow, transcriptAnchorEntriesForRows } from "./TranscriptBody";

function toolItem(id: string, toolName: string, status = "completed"): ItemModel {
  return { id, turnId: "t1", type: "commandExecution", text: "", toolName, status } as ItemModel;
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
