import { buildEntityView, hydrateThread, parseActivityTree } from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { subagentOutcomesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { expect, test } from "vitest";
import { composeComicFlow, findComicFlowAnchorRow } from "./comicRows";
import { projectComicDelegates } from "./delegates";
import type { ComicReaderSnapshot, ComicSubchatState } from "./useComicSubchats";

const model = hydrateThread({ thread: wireThread("local:root") }, "local:root", 0);
const entities = buildEntityView({
  sessionRef: model.ref,
  tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
  turns: [],
  stale: false,
  ended: false,
});
const strip = projectComicDelegates(model, entities)[0]!;
const activity = { snapshot: null, entities, strips: [], context: {}, loadMore: async () => {} };
const history = {
  model,
  hasOlder: false,
  loadingOlder: false,
  olderError: null,
  loadOlder: async () => {},
  loadOlderReportingError() {},
  cancelOlder() {},
};
const entry = (id: string, sourceIndex: number) => ({
  id,
  sourceItemId: id,
  sourceIndex,
  kind: "dialogue" as const,
  speaker: "assistant" as const,
  sourceLabel: "fixture",
  text: id,
  streaming: false,
});
const root: ComicReaderSnapshot = {
  ref: model.ref,
  path: [],
  entries: [
    entry("before", 0),
    entry("after", 2),
    {
      id: "itemless",
      sourceTurnId: "failed-turn",
      kind: "caption",
      speaker: "source",
      sourceLabel: "fixture",
      text: "ERROR",
      streaming: false,
    },
  ],
  history,
  activity: { ...activity, strips: [{ ...strip, source: { itemId: "receipt", sourceIndex: 1 } }] },
};
const child: ComicReaderSnapshot = {
  ref: strip.childRef,
  path: [strip.key],
  entries: [entry("before", 0)],
  history,
  activity,
};
const state: ComicSubchatState = {
  readers: new Map([[JSON.stringify(child.path), child]]),
  expandedPaths: [child.path],
  expand() {},
  collapse() {},
};
test("flattens causal child rows and isolates itemless errors and unplaced activity", () => {
  const rows = composeComicFlow(root, state, 390, () => true);
  const sources = rows.flatMap((row) =>
    row.kind === "panels" ? row.row.panels.map((p) => [row.sessionRef, p.slice.entryId]) : [],
  );
  expect(sources).toEqual([
    [root.ref, "before"],
    [child.ref, "before"],
    [root.ref, "after"],
    [root.ref, "itemless"],
  ]);
  expect(rows.filter((r) => r.kind === "strip")).toHaveLength(1);
  expect(rows.findIndex((r) => r.kind === "turn-errors-group")).toBeLessThan(
    rows.findIndex((r) => r.kind === "panels" && r.row.panels[0]?.slice.entryId === "itemless"),
  );
  expect(rows.filter((r) => r.kind === "history")).toHaveLength(2);
  const unplaced = composeComicFlow(
    { ...root, activity: { ...root.activity, strips: [strip] } },
    state,
    320,
    () => true,
  );
  expect(unplaced.filter((r) => r.kind === "unplaced-group")).toHaveLength(1);
  expect(unplaced.every((r) => r.kind !== "panels" || r.row.panels.length === 1)).toBe(true);
});
test("matches complete path and session before entry offsets and keeps end target on its reader", () => {
  const rows = composeComicFlow(root, state, 390, () => true);
  const index = findComicFlowAnchorRow(rows, {
    mode: "away",
    sessionRef: child.ref,
    path: child.path,
    entryId: "before",
    offset: 2,
    viewportOffset: 0,
  });
  expect(index).toBeDefined();
  expect(rows[index!]?.sessionRef).toBe(child.ref);
  expect(
    findComicFlowAnchorRow(rows, {
      mode: "away",
      sessionRef: root.ref,
      path: child.path,
      entryId: "before",
      offset: 2,
      viewportOffset: 0,
    }),
  ).toBeUndefined();
  const end = findComicFlowAnchorRow(rows, { mode: "end", sessionRef: root.ref, path: [] });
  expect(rows[end!]?.sessionRef).toBe(root.ref);
});
