// @vitest-environment node

import { expect, test } from "vitest";
import {
  type ActivityDelegate,
  type ActivityEntry,
  type ActivityJob,
  type ActivityTree,
  activityNodeID,
} from "./activityData";
import { buildActivityRows } from "./activityRows";
import {
  buildEntityView,
  type DelegateEntityView,
  entityOpenTarget,
  findEntityView,
  watchFoldKey,
  watchItems,
} from "./entityView";
import type { ItemModel, TurnModel } from "./model";
import type { EvenerDelegateInfo } from "./types.gen";

function job(jobId: string): ActivityJob {
  return {
    jobId,
    ownerSessionId: "s",
    ownerRef: "local:s",
    type: "shell",
    status: "completed",
    transcriptRef: `job:${jobId}`,
    terminal: true,
    background: false,
    hasOutput: true,
    description: "test job",
    startedAt: "2026-09-13T20:00:00Z",
    endedAt: "2026-09-13T20:00:01Z",
    outputBytes: 12,
  };
}

function delegate(delegateId: string, projectionRevision?: number): ActivityDelegate {
  return {
    runGeneration: 1,
    delegateId,
    ownerSessionId: "s",
    rootSessionId: "s",
    childSessionId: `${delegateId}-child`,
    childRef: `local:tree-${delegateId}`,
    type: "delegate",
    lifecycle: "ended",
    phase: "ended",
    status: "completed",
    projectionRevision,
    terminal: true,
    resumable: true,
    branch: {},
  };
}

function treeWithEntries(entries: ActivityEntry[]): ActivityTree {
  return {
    revision: 1,
    root: {
      kind: "session",
      sessionId: "s",
      ref: "local:s",
      label: "root",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: entries.length, complete: true },
      entries,
      branch: {},
    },
  };
}

function treeWithJob(jobId: string): ActivityTree {
  return treeWithEntries([{ kind: "shell", job: job(jobId) }]);
}

function treeWithDelegate(delegateId: string, projectionRevision?: number): ActivityTree {
  return treeWithEntries([{ kind: "delegate", delegate: delegate(delegateId, projectionRevision) }]);
}

function liveDelegate(
  delegateId: string,
  projectionRevision: number,
  transcriptRef = `local:live-${delegateId}`,
): EvenerDelegateInfo {
  return {
    runGeneration: 1,
    delegateId,
    ownerSessionId: "s",
    rootSessionId: "s",
    childSessionId: `${delegateId}-child`,
    transcriptRef,
    type: "delegate",
    lifecycle: "running",
    phase: "running",
    status: "running",
    resumable: true,
    needsAttention: false,
    projectionRevision,
  };
}

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return {
    id: "watch-item",
    turnId: "turn-1",
    position: { entry: 2, item: 1 },
    type: "commandExecution",
    text: "",
    toolName: "job_watch",
    argumentsJSON: '{"job_id":"job_x"}',
    output: '{"watch_id":"watch_x"}',
    raw: { watch_id: "watch_x", watching: true, source: "job_x" },
    status: "completed",
    ...overrides,
  };
}

function turn(items: ItemModel[]): TurnModel {
  return { id: "turn-1", status: "completed", items };
}

function delegateSource(entity: DelegateEntityView): "tree" | "live" {
  return entity.row ? "tree" : "live";
}

test("job uses the row transcriptRef and parentRef", () => {
  const view = buildEntityView({
    sessionRef: "local:s",
    tree: treeWithJob("job_x"),
    turns: [],
    stale: false,
    ended: false,
  });
  const entity = findEntityView(view, "job", "job_x", "local:s");

  expect(entity).toMatchObject({ kind: "job", logicalId: "job_x" });
  expect(entityOpenTarget(entity!)).toEqual({ ref: "job:job_x", parentRef: "local:s" });
});

test("a job without a transcriptRef uses its verified row id and parentRef", () => {
  const tree = treeWithJob("job_fallback");
  const entry = tree.root.entries[0];
  if (entry?.kind !== "shell") throw new Error("expected shell fixture");
  entry.job.transcriptRef = undefined;

  const entity = buildEntityView({
    sessionRef: "local:s",
    tree,
    turns: [],
    stale: false,
    ended: false,
  }).get('job:["local:s","job_fallback"]');

  expect(entityOpenTarget(entity!)).toEqual({ ref: "job:job_fallback", parentRef: "local:s" });
});

test("live-only delegate cards and navigates from delegates[]", () => {
  const stable = liveDelegate("dlg_x", 3, "local:child");
  const view = buildEntityView({
    sessionRef: "local:s",
    delegates: [stable],
    turns: [],
    stale: false,
    ended: false,
  });
  const entity = findEntityView(view, "delegate", "dlg_x", "local:s");

  expect(entity).toMatchObject({ kind: "delegate", logicalId: "dlg_x", stable });
  expect(entityOpenTarget(entity!)).toEqual({ ref: "local:child", parentRef: "local:s" });
});

test("live update and retained row reconcile by qualified identity with one action target", () => {
  const retained = treeWithDelegate("same", 3);
  const live = { ...liveDelegate("same", 4, "local:tree-same"), runGeneration: 2 };
  const view = buildEntityView({
    sessionRef: "local:s",
    tree: retained,
    delegates: [live],
    turns: [],
    stale: false,
    ended: false,
  });
  expect(view.size).toBe(1);
  const entity = view.get('delegate:["local:tree-same","same"]');
  expect(entity).toMatchObject({ kind: "delegate", logicalId: "same", stable: live });
  expect(entity && entityOpenTarget(entity)).toEqual({ ref: "local:tree-same", parentRef: "local:s" });
});

test("transcript lookup selects raw IDs only within their authoritative owner", () => {
  const first = { ...job("same"), ownerRef: "host-a:s" },
    second = { ...job("same"), ownerRef: "host-b:s" };
  const view = buildEntityView({
    sessionRef: "local:s",
    tree: treeWithEntries([
      { kind: "shell", job: first },
      { kind: "shell", job: second },
    ]),
    turns: [],
    stale: false,
    ended: false,
  });
  expect(view.size).toBe(2);
  expect(findEntityView(view, "job", "same", "host-a:s")?.ownerRef).toBe("host-a:s");
  expect(findEntityView(view, "job", "same", "host-b:s")?.ownerRef).toBe("host-b:s");
  expect(findEntityView(view, "job", "same", "local:s")).toBeUndefined();
});

test("closed failed job folds retain equal logical IDs and authoritative output owners", () => {
  const tree = treeWithEntries([
    {
      kind: "shell",
      job: { ...job("same"), ownerRef: "host-a:s", status: "command_exited_nonzero", outcome: "failure" },
    },
    { kind: "shell", job: { ...job("same"), ownerRef: "host-b:s", status: "completed", outcome: "success" } },
  ]);
  expect(buildActivityRows(tree, new Set())).toEqual([expect.objectContaining({ kind: "fold", inactiveCount: 2 })]);
  const view = buildEntityView({ sessionRef: "local:s", tree, turns: [], stale: false, ended: false });
  expect(view.size).toBe(2);
  expect(findEntityView(view, "job", "same", "host-a:s")).toMatchObject({
    row: { defaultDetailOpen: false, job: { status: "command_exited_nonzero", outcome: "failure" } },
    open: { ref: "job:same", parentRef: "host-a:s" },
  });
  expect(findEntityView(view, "job", "same", "host-b:s")).toMatchObject({
    open: { ref: "job:same", parentRef: "host-b:s" },
  });
  expect(findEntityView(view, "job", "same", "local:s")).toBeUndefined();
});

test.each([
  ["higher live revision", 5, 7, "live"],
  ["higher tree revision", 9, 7, "tree"],
  ["equal revisions", 7, 7, "live"],
  ["tree revision absent", undefined, 7, "live"],
] as const)("delegate selection uses the %s branch", (_case, treeRevision, liveRevision, expected) => {
  const entity = buildEntityView({
    sessionRef: "local:s",
    tree: treeWithDelegate("dlg_shared", treeRevision),
    delegates: [
      { ...liveDelegate("dlg_shared", liveRevision, "local:tree-dlg_shared"), terminal: true, status: "completed" },
    ],
    turns: [],
    stale: false,
    ended: false,
  }).get('delegate:["local:tree-dlg_shared","dlg_shared"]');

  if (entity?.kind !== "delegate") throw new Error("expected delegate entity");
  expect(delegateSource(entity)).toBe(expected);
  expect(entityOpenTarget(entity)).toEqual({
    ref: "local:tree-dlg_shared",
    parentRef: "local:s",
  });
});

test("tree delegate remains selected when there is no live record", () => {
  const entity = buildEntityView({
    sessionRef: "local:s",
    tree: treeWithDelegate("dlg_tree", 4),
    turns: [],
    stale: false,
    ended: false,
  }).get('delegate:["local:tree-dlg_tree","dlg_tree"]');

  if (entity?.kind !== "delegate") throw new Error("expected delegate entity");
  expect(delegateSource(entity)).toBe("tree");
  expect(entityOpenTarget(entity)).toEqual({ ref: "local:tree-dlg_tree", parentRef: "local:s" });
});

test("stale and ended propagate to every activity entity", () => {
  const view = buildEntityView({
    sessionRef: "local:s",
    tree: treeWithEntries([
      { kind: "shell", job: job("job_x") },
      { kind: "delegate", delegate: delegate("dlg_x", 2) },
    ]),
    delegates: [liveDelegate("dlg_live", 1)],
    turns: [],
    stale: true,
    ended: true,
  });

  expect([...view.values()]).toHaveLength(3);
  for (const entity of view.values()) {
    expect(entity).toMatchObject({ stale: true, ended: true });
  }
});

test("watch items flatten turns and filter only job_watch calls", () => {
  const first = item({ id: "first", turnId: "turn-1" });
  const prose = item({ id: "prose", toolName: undefined, type: "agentMessage", text: "hello" });
  const second = item({ id: "second", turnId: "turn-2" });

  expect(watchItems([turn([first, prose]), { id: "turn-2", status: "completed", items: [second] }])).toEqual([
    first,
    second,
  ]);
});

test("watch entities are last-known and have no open target", () => {
  const entity = buildEntityView({
    sessionRef: "local:s",
    turns: [turn([item()])],
    stale: false,
    ended: false,
  }).get('watch:["local:s","watch_x"]');

  expect(entity).toMatchObject({ kind: "watch", logicalId: "watch_x", lastKnown: true, stale: true, ended: false });
  expect(entityOpenTarget(entity!)).toBeUndefined();
});

test("watchFoldKey ignores prose-only turn replacements", () => {
  const watch = item();
  const before = [turn([item({ id: "prose", toolName: undefined, type: "agentMessage", text: "first" }), watch])];
  const after = [
    turn([item({ id: "replacement", toolName: undefined, type: "agentMessage", text: "second" }), { ...watch }]),
  ];

  expect(watchFoldKey(after)).toBe(watchFoldKey(before));
});

test("watchFoldKey changes for raw summary enrichment and is stable for equal raw content", () => {
  const raw = { watch_id: "watch_x", watching: true, deliveries: 1 };
  const before = item({ raw });
  const equal = item({ raw: { ...raw } });
  const enriched = item({ raw: { ...raw, deliveries: 2 } });

  expect(watchFoldKey([turn([equal])])).toBe(watchFoldKey([turn([before])]));
  expect(watchFoldKey([turn([enriched])])).not.toBe(watchFoldKey([turn([before])]));
});

test.each([
  ["position", { position: { entry: 2, item: 2 } }],
  ["arguments", { argumentsJSON: '{"job_id":"job_y"}' }],
  ["output with unchanged length", { output: '{"watch_id":"watch_y"}' }],
  ["completion state", { status: "inProgress" }],
] satisfies ReadonlyArray<readonly [string, Partial<ItemModel>]>)(
  "watchFoldKey changes when watch %s changes",
  (_field, change) => {
    const before = item({ error: "failed" });
    const after = item({ error: "failed", ...change });

    expect(watchFoldKey([turn([after])])).not.toBe(watchFoldKey([turn([before])]));
  },
);

test("watchFoldKey changes when equal-length error content changes", () => {
  const beforeError = "denied";
  const afterError = "failed";
  expect(afterError).toHaveLength(beforeError.length);

  const before = item({ error: beforeError });
  const after = item({ error: afterError });
  expect(watchFoldKey([turn([after])])).not.toBe(watchFoldKey([turn([before])]));
});

test("watchFoldKey is order-canonical for positioned watch items", () => {
  // The fold orders snapshots by transcript position, so the key must not depend
  // on the order the caller happened to hand the items over in: a page arriving
  // out of order would otherwise rebuild the view for identical content.
  const earlier = item({ id: "watch-earlier", position: { entry: 1, item: 0 } });
  const later = item({ id: "watch-later", position: { entry: 4, item: 2 } });

  expect(watchFoldKey([turn([later]), turn([earlier])])).toBe(watchFoldKey([turn([earlier]), turn([later])]));
});

test("watchFoldKey keeps caller order when items carry no transcript position", () => {
  // Unpositioned items have no canonical order, so caller order still decides —
  // the same rule the fold follows.
  const first = item({ id: "unpositioned-first", position: undefined, output: '{"watch_id":"watch_a"}' });
  const second = item({ id: "unpositioned-second", position: undefined, output: '{"watch_id":"watch_b"}' });

  expect(watchFoldKey([turn([first]), turn([second])])).not.toBe(watchFoldKey([turn([second]), turn([first])]));
});

test("watchFoldKey is order-canonical across a mixed positioned and positionless list", () => {
  // A positionless item sorts after the positioned ones, so a mixed list has one
  // canonical order whatever order the caller handed the items over in. Without
  // that rule the comparator is non-transitive across the boundary and the key
  // stays caller-order-dependent here, which is the case the positioned-only
  // test above cannot catch.
  const positioned = item({ id: "watch-positioned", position: { entry: 2, item: 1 } });
  const positionless = item({ id: "watch-positionless", position: undefined, output: '{"watch_id":"watch_b"}' });

  expect(watchFoldKey([turn([positionless]), turn([positioned])])).toBe(
    watchFoldKey([turn([positioned]), turn([positionless])]),
  );
});

test.each([3, 4])("keeps immutable delegate names through status overlay revision %s", (revision) => {
  const row = { ...delegate("same", 3), name: "Compact observer", reportPreview: "Previous run report" };
  const stable = { ...liveDelegate("same", revision, row.childRef), runGeneration: 2 };
  const entity = findEntityView(
    buildEntityView({
      sessionRef: "local:s",
      tree: treeWithEntries([{ kind: "delegate", delegate: row }]),
      delegates: [stable],
      turns: [],
      stale: false,
      ended: false,
    }),
    "delegate",
    "same",
    "local:s",
  );
  if (entity?.kind !== "delegate") throw new Error("expected delegate entity");
  expect(entity.name).toBe("Compact observer");
  expect(entity.stable).toBe(stable);
  expect(entity.row).toBeUndefined();
});

test.each(["owner", "delegate", "child"])("does not graft a compact name across a different %s", (identity) => {
  const row = { ...delegate("same", 100), runGeneration: 9, name: "Other identity" };
  const stable = liveDelegate(
    identity === "delegate" ? "different" : "same",
    4,
    identity === "child" ? "local:different" : row.childRef,
  );
  if (identity === "owner") row.ownerRef = "local:another";
  const entity = buildEntityView({
    sessionRef: "local:s",
    tree: treeWithEntries([{ kind: "delegate", delegate: row }]),
    delegates: [stable],
    turns: [],
    stale: false,
    ended: false,
  }).get(activityNodeID({ kind: "delegate", delegateId: stable.delegateId, childRef: stable.transcriptRef }));
  if (entity?.kind !== "delegate") throw new Error("expected delegate entity");
  expect(entity.name).toBeUndefined();
  expect(entity.stable).toBe(stable);
});

test.each([
  [2, true, 1, false, "tree"],
  [2, true, 2, false, "tree"],
  [2, false, 2, true, "live"],
  [2, true, 3, false, "live"],
  [2, true, 0, false, "tree"],
  [0, true, 2, false, "live"],
  [0, true, 0, false, "live"],
] as const)(
  "delegate generation %s/%s versus diagnostics %s/%s selects %s",
  (rowGeneration, rowTerminal, stableGeneration, stableTerminal, expected) => {
    const row = {
      ...delegate("ordered"),
      runGeneration: rowGeneration,
      terminal: rowTerminal,
      reportPreview: "current report",
    };
    const stable = {
      ...liveDelegate("ordered", 100, row.childRef),
      runGeneration: stableGeneration,
      terminal: stableTerminal,
    };
    const entity = findEntityView(
      buildEntityView({
        sessionRef: "local:s",
        tree: treeWithEntries([{ kind: "delegate", delegate: row }]),
        delegates: [stable],
        turns: [],
        stale: false,
        ended: false,
      }),
      "delegate",
      row.delegateId,
      "local:s",
    );
    if (entity?.kind !== "delegate") throw new Error("expected owned delegate");
    expect(delegateSource(entity)).toBe(expected);
    if (expected === "tree") expect(entity.row?.delegate).toBe(row);
    else {
      expect(entity.stable).toBe(stable);
      expect(entity).not.toHaveProperty("row");
    }
  },
);
