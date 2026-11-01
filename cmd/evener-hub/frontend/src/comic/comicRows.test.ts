import { buildEntityView, hydrateThread, parseActivityTree } from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { subagentOutcomesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { expect, test } from "vitest";
import { composeComicFlow, findComicFlowAnchorRow } from "./comicRows";
import { projectComicDelegates } from "./delegates";
import type { ComicReaderSnapshot, ComicSubchatState } from "./useComicSubchats";

const model = hydrateThread({ thread: wireThread("local:root", { sessionId: "root" }) }, "local:root", 0);
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
  olderPageKey: undefined,
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
const update = (id: string, sourceIndex: number, target: { delegateId?: string; childRef?: string }) => ({
  ...entry(id, sourceIndex),
  kind: "caption" as const,
  speaker: "source" as const,
  sourceLabel: `${strip.persona.name} · ${strip.persona.badge}`,
  delegateUpdate: target,
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
test("flattens causal child rows and isolates itemless errors", () => {
  const rows = composeComicFlow(root, state, 390, () => true);
  expect(
    rows
      .filter((row) => row.kind === "history" || row.kind === "panels" || row.kind === "strip")
      .map((row) =>
        row.kind === "panels"
          ? [row.sessionRef, row.row.panels.flatMap((panel) => panel.balloons)[0]?.slice.entryId]
          : [row.sessionRef, row.kind],
      ),
  ).toEqual([
    [root.ref, "history"],
    [root.ref, "before"],
    [root.ref, "strip"],
    [child.ref, "history"],
    [child.ref, "before"],
    [root.ref, "after"],
    [root.ref, "itemless"],
  ]);
  const sources = rows.flatMap((row) =>
    row.kind === "panels"
      ? row.row.panels.flatMap((panel) => panel.balloons).map((balloon) => [row.sessionRef, balloon.slice.entryId])
      : [],
  );
  expect(sources).toEqual([
    [root.ref, "before"],
    [child.ref, "before"],
    [root.ref, "after"],
    [root.ref, "itemless"],
  ]);
  expect(rows.filter((r) => r.kind === "strip")).toHaveLength(1);
  expect(rows.findIndex((r) => r.kind === "turn-errors-group")).toBeLessThan(
    rows.findIndex(
      (r) => r.kind === "panels" && r.row.panels.flatMap((panel) => panel.balloons)[0]?.slice.entryId === "itemless",
    ),
  );
  expect(rows.filter((r) => r.kind === "history")).toHaveLength(2);
});

const folded = () => [
  entry("before", 0),
  update("mid-update", 2, { childRef: strip.childRef }),
  update("late-update", 3, { delegateId: strip.delegateId }),
  entry("after", 4),
];
const collapsed = { ...state, readers: new Map() as ComicSubchatState["readers"], expandedPaths: [] };
const entryIds = (rows: ReturnType<typeof composeComicFlow>) =>
  rows.flatMap((row) =>
    row.kind === "panels"
      ? row.row.panels.flatMap((panel) => panel.balloons.map((balloon) => balloon.slice.entryId))
      : [],
  );
const stripSequence = (rows: ReturnType<typeof composeComicFlow>) =>
  rows
    .filter((row) => row.kind === "panels" || row.kind === "strip")
    .map((row) =>
      row.kind === "strip"
        ? row.strip.key
        : row.row.panels.flatMap((panel) => panel.balloons.map((balloon) => balloon.slice.entryId)).join(","),
    );

test("an unlaunched strip anchors inline at its earliest folded update and folds the updates away", () => {
  const rows = composeComicFlow(
    { ...root, entries: folded(), activity: { ...activity, strips: [strip] } },
    collapsed,
    390,
    () => true,
  );
  expect(stripSequence(rows)).toEqual(["before", strip.key, "after"]);
  expect(entryIds(rows)).toEqual(["before", "after"]);
  expect(rows.find((row) => row.kind === "strip")?.updateCount).toBe(2);
});

test("an update-anchored strip keeps its row identity when the launch loads earlier", () => {
  const unlaunched = composeComicFlow(
    { ...root, entries: folded(), activity: { ...activity, strips: [strip] } },
    collapsed,
    390,
    () => true,
  );
  const launched = composeComicFlow(
    {
      ...root,
      entries: folded(),
      activity: { ...activity, strips: [{ ...strip, source: { itemId: "receipt", sourceIndex: 1 } }] },
    },
    collapsed,
    390,
    () => true,
  );
  expect(stripSequence(launched)).toEqual(["before", strip.key, "after"]);
  expect(launched.find((row) => row.kind === "strip")?.id).toBe(unlaunched.find((row) => row.kind === "strip")?.id);
  expect(launched.find((row) => row.kind === "strip")?.updateCount).toBe(2);
});

test("a strip with neither a loaded launch nor loaded updates renders no strip row", () => {
  const rows = composeComicFlow({ ...root, activity: { ...activity, strips: [strip] } }, collapsed, 390, () => true);
  expect(rows.filter((row) => row.kind === "strip")).toHaveLength(0);
});

test("update captions without a matching strip still render as panels", () => {
  const rows = composeComicFlow(
    {
      ...root,
      entries: [
        entry("before", 0),
        update("orphan-update", 2, { delegateId: "dlg_unknown", childRef: "local:elsewhere" }),
        entry("after", 4),
      ],
      activity: { ...activity, strips: [strip] },
    },
    collapsed,
    390,
    () => true,
  );
  expect(rows.filter((row) => row.kind === "strip")).toHaveLength(0);
  expect(entryIds(rows)).toEqual(["before", "orphan-update", "after"]);
});
test("flow rows carry the packed column count from the width formula", () => {
  const packed: ComicReaderSnapshot = {
    ...root,
    entries: ["a", "b", "c", "d"].map((id, index) => entry(id, index)),
    activity: { ...activity, strips: [] },
  };
  const rows = composeComicFlow(packed, { ...state, readers: new Map(), expandedPaths: [] }, 1290, () => true, 416);
  const panelsRows = rows.filter((row) => row.kind === "panels");
  expect(
    panelsRows.map((row) =>
      row.kind === "panels" ? row.row.panels.flatMap((panel) => panel.balloons.map((b) => b.slice.entryId)) : [],
    ),
  ).toEqual([["a", "b", "c"], ["d"]]);
  expect(panelsRows.map((row) => (row.kind === "panels" ? row.row.columns : 0))).toEqual([3, 1]);
  expect(panelsRows.every((row) => row.kind !== "panels" || row.row.unit === "panel")).toBe(true);
});

test("a stacked panel preserves reading anchors for every balloon", () => {
  const stacked: ComicReaderSnapshot = {
    ...root,
    entries: ["a", "b", "c"].map((id, index) => ({
      ...entry(id, index),
      persona: { key: "worker", character: "xeno", name: "Worker", badge: "delegate" },
      speaker: "assistant",
    })),
    activity: { ...activity, strips: [] },
  };
  const rows = composeComicFlow(stacked, { ...state, readers: new Map(), expandedPaths: [] }, 1290, () => true, 416);
  expect(rows.filter((row) => row.kind === "panels")).toHaveLength(1);
  for (const id of ["a", "b", "c"]) {
    expect(
      findComicFlowAnchorRow(rows, {
        mode: "away",
        sessionRef: stacked.ref,
        path: [],
        entryId: id,
        offset: 0,
        viewportOffset: 0,
      }),
    ).toBeDefined();
  }
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
