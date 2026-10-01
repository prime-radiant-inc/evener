// @vitest-environment node
import { expect, test } from "vitest";
import { activityNodeID } from "./activityData";
import {
  activityDelegateState,
  buildActivityRows,
  buildWatchRows,
  indexActivityEntities,
  watchDeliveryInstants,
  watchFacts,
} from "./activityRows";
import { buildEntityView, findEntityView } from "./entityView";
import { projectSessionActivity } from "./sessionActivityPresentation";
import { type SessionActivitySnapshot, SessionActivityStore } from "./sessionActivityStore";
import {
  activityClient,
  activityContext,
  activityRef,
  delegateFixture,
  jobFixture,
  summaryFixture,
} from "./sessionActivityTestUtils";
import { subagentOutcomesDelegatesResponse } from "./testing/subagentWireFixtures";
import type { SessionDelegate, SessionWatch } from "./types.gen";
import { watchArmedLabel, watchGloss } from "./watchText";

function snapshot(delegates: SessionDelegate[] = []): SessionActivitySnapshot {
  const state = new SessionActivityStore(activityClient(), activityRef).getSnapshot();
  return {
    ...state,
    context: activityContext(),
    summary: summaryFixture(),
    delegates: { ...state.delegates, rows: delegates, complete: true, context: activityContext() },
    jobs: { ...state.jobs, complete: true, context: activityContext() },
  };
}
function delegate(id: string, ownerRef: string, childRef: string, parentDelegateId?: string): SessionDelegate {
  return { ...delegateFixture(), delegateId: id, ownerRef, childRef, parentDelegateId };
}
function watch(receiverRef: string, state: SessionWatch["state"] = "unknown"): SessionWatch {
  return {
    ownerRef: "remote:producer",
    receiverRef,
    state,
    watch: {
      id: "watch-1",
      source: "job-1",
      target: "job-2",
      sendTo: receiverRef,
      note: "note-sentinel",
      cadence: [{ kind: "every", seconds: 60, derivedNextFireAt: "2026-09-30T12:01:00Z" }],
      deliveries: 2,
      deliveryTimes: ["2026-09-30T12:00:00Z", "invalid", "2026-09-30T11:59:00Z"],
      createdAt: "2026-09-30T11:58:00Z",
      active: false,
      endReason: "reason-sentinel",
    },
  };
}

test("stable active delegate projection remains visible and preserves its raw action target", () => {
  const active = {
    ...delegateFixture("active_raw"),
    lifecycle: "running",
    phase: "running",
    status: "running",
    terminal: false,
    childRef: "remote:opaque-child-target",
  };
  const projected = projectSessionActivity(snapshot([active]));
  if (!projected.tree) throw new Error("missing loaded projection");
  const rows = buildActivityRows(projected.tree, new Set());
  expect(rows.map((row) => row.kind)).toEqual(["delegate"]);
  const row = rows[0];
  if (row?.kind !== "delegate") throw new Error("missing active stable delegate row");
  expect(activityDelegateState(row.delegate)).toEqual({ active: true, failed: false, status: "running" });
  expect(row.live).toBe(true);
  expect(row.defaultDetailOpen).toBe(true);
  const entities = buildEntityView({
    sessionRef: activityRef,
    tree: projected.tree,
    turns: [],
    stale: false,
    ended: false,
  });
  const entity = findEntityView(entities, "delegate", active.delegateId, active.ownerRef);
  if (entity?.kind !== "delegate") throw new Error("missing active delegate action");
  expect(entity.logicalId).toBe("active_raw");
  expect(entity.open).toEqual({ ref: active.childRef, parentRef: active.ownerRef });
});

test("retained failed stable delegate projection keeps failure in its fold and action target", () => {
  const failed = {
    ...delegateFixture("failed_raw"),
    lifecycle: "idle",
    phase: "idle",
    status: "idle",
    terminal: true,
    outcome: "failed",
    error: "failure-sentinel",
    childRef: "remote:retained-child-target",
  };
  const state = snapshot([failed]);
  state.context = { ...activityContext(), availability: "retained" };
  const projected = projectSessionActivity(state);
  if (!projected.tree) throw new Error("missing retained projection");
  const closed = buildActivityRows(projected.tree, new Set());
  expect(closed).toHaveLength(1);
  const fold = closed[0];
  if (fold?.kind !== "fold") throw new Error("missing inactive fold");
  expect([fold.inactiveCount, fold.failedCount]).toEqual([1, 1]);
  const expanded = buildActivityRows(projected.tree, new Set([fold.id]));
  const row = expanded.find((row) => row.kind === "delegate");
  if (row?.kind !== "delegate") throw new Error("missing disclosed retained delegate");
  expect(activityDelegateState(row.delegate)).toEqual({ active: false, failed: true, status: "failed" });
  expect(row.live).toBe(false);
  expect(row.defaultDetailOpen).toBe(false);
  expect(row.delegate.error).toBe(failed.error);
  const entities = buildEntityView({
    sessionRef: activityRef,
    tree: projected.tree,
    turns: [],
    stale: true,
    ended: false,
  });
  const entity = findEntityView(entities, "delegate", failed.delegateId, failed.ownerRef);
  if (entity?.kind !== "delegate") throw new Error("missing retained delegate action");
  expect(entity.logicalId).toBe("failed_raw");
  expect(entity.open).toEqual({ ref: failed.childRef, parentRef: failed.ownerRef });
});

test("loaded orphan descendants survive until an out-of-order parent page arrives", () => {
  const child = delegate("child", "remote:child", "remote:leaf", "parent");
  const first = projectSessionActivity(snapshot([child]));
  expect(first.tree?.root.entries[0]).toMatchObject({ kind: "delegate", delegate: { delegateId: "child" } });
  expect(first.complete).toBe(false);
  const parent = delegate("parent", activityRef, "remote:child");
  const second = projectSessionActivity(snapshot([child, parent]));
  expect(second.tree?.root.entries).toHaveLength(1);
  expect(second.tree?.root.entries[0]).toMatchObject({
    delegate: { delegateId: "parent", child: { entries: [{ kind: "delegate", delegate: { delegateId: "child" } }] } },
  });
  expect(second.complete).toBe(true);
  expect(second.tree?.root.entries[0]).not.toHaveProperty("delegate.childSessionId");
});
test("source-qualified identities keep equal logical IDs and unrelated forks separate", () => {
  const rows = [
    delegate("same", activityRef, "host-a:child"),
    delegate("same", activityRef, "host-b:child"),
    delegate("nested", "host-a:child", "host-a:leaf", "same"),
    delegate("nested", "host-b:unrelated", "host-b:leaf", "same"),
  ];
  const projected = projectSessionActivity(snapshot(rows));
  const tree = projected.tree;
  expect(tree).not.toBeNull();
  if (!tree) throw new Error("missing loaded projection");
  const index = indexActivityEntities(tree);
  expect(index.size).toBe(4);
  expect(index.get(`delegate:${JSON.stringify(["host-a:child", "same"])}`)?.kind).toBe("delegate");
  expect(index.get(`delegate:${JSON.stringify(["host-b:child", "same"])}`)?.kind).toBe("delegate");
  expect(tree.root.entries).toHaveLength(3);
  expect(activityNodeID(tree.root)).toBe(`session:${activityRef}`);
});

test("deep descendants remain connected when all parent pages arrive in reverse order", () => {
  const rows = Array.from({ length: 40 }, (_, level) =>
    delegate(
      `d${level}`,
      level === 0 ? activityRef : `remote:child-${level - 1}`,
      `remote:child-${level}`,
      level === 0 ? undefined : `d${level - 1}`,
    ),
  );
  const projected = projectSessionActivity(snapshot(rows.reverse()));
  if (!projected.tree) throw new Error("missing loaded projection");
  expect(indexActivityEntities(projected.tree).size).toBe(40);
  expect(projected.tree.root.entries).toHaveLength(1);
  expect(projected.complete).toBe(true);
});

test("cyclic retained lineage remains visible as incomplete evidence", () => {
  const projected = projectSessionActivity(
    snapshot([delegate("a", "remote:b", "remote:a", "b"), delegate("b", "remote:a", "remote:b", "a")]),
  );
  if (!projected.tree) throw new Error("missing loaded projection");
  expect(indexActivityEntities(projected.tree).size).toBe(2);
  expect(projected.tree.root.entries).toHaveLength(2);
  expect(projected.complete).toBe(false);
});
test("shell jobs attach to their actual owner and preserve output/action targets", () => {
  const state = snapshot([delegate("parent", activityRef, "remote:child")]);
  state.jobs.rows = [
    {
      ...jobFixture("same"),
      ownerRef: "remote:child",
      ownerSessionId: "child-id",
      transcriptRef: "remote:output",
      hasOutput: true,
    },
    { ...jobFixture("same"), ownerRef: activityRef },
  ];
  const projected = projectSessionActivity(state);
  if (!projected.tree) throw new Error("missing loaded projection");
  const rows = buildActivityRows(projected.tree, new Set());
  expect(rows.filter((row) => row.kind === "job").map((row) => [row.parentRef, row.transcriptRef, row.id])).toEqual([
    ["remote:child", "remote:output", `job:${JSON.stringify(["remote:child", "same"])}`],
    [activityRef, undefined, `job:${JSON.stringify([activityRef, "same"])}`],
  ]);
  expect(projected.tree.root.entries[0]).toMatchObject({ delegate: { child: { sessionId: "child-id" } } });
  expect(projected.tree.root.entries[0]).toMatchObject({
    delegate: { ownerSessionId: "session", rootSessionId: "session", childSessionId: "child-id" },
  });
});
test("retained unavailable runtime preserves optional failure/model/usage/worktree facts", () => {
  const row = {
    ...delegate("parent", activityRef, "remote:child"),
    terminal: true,
    status: "failed",
    outcome: "failed",
    reason: "reason-sentinel",
    error: "error-sentinel",
    resumable: true,
    model: "model-sentinel",
    usage: { inputTokens: 10, outputTokens: 20 },
    worktree: { path: "/work", branch: "b", headSha: "h", ahead: 1, dirty: true },
  };
  const state = snapshot([row]);
  state.context = { ...activityContext(), availability: "retained" };
  const projected = projectSessionActivity(state);
  expect(projected.context?.availability).toBe("retained");
  expect(projected.tree?.root.entries[0]).toMatchObject({
    delegate: {
      terminal: true,
      outcome: "failed",
      reason: "reason-sentinel",
      error: "error-sentinel",
      resumable: true,
      model: "model-sentinel",
      usage: row.usage,
      worktree: row.worktree,
    },
  });
});
test("partial loaded evidence and unknown summary counts stay independent", () => {
  const state = snapshot([delegate("parent", activityRef, "remote:child")]);
  state.delegates.complete = false;
  state.delegates.pending = true;
  state.delegates.issues = [{ ref: "remote:child", code: "unavailable" }];
  const projected = projectSessionActivity(state);
  expect(projected.summary).toBe(state.summary);
  expect(projected.summary?.delegates).toMatchObject({ known: false });
  expect(projected.complete).toBe(false);
  expect(projected.pending).toBe(true);
  expect(projected.tree?.root.counts.complete).toBe(false);
  expect(projected.issues).toEqual(state.delegates.issues);
  expect(projectSessionActivity(new SessionActivityStore(activityClient(), activityRef).getSnapshot()).tree).toBeNull();
});

test("partial optional usage stays partial without fabricating token counters", () => {
  const row = { ...delegate("parent", activityRef, "remote:child"), usage: { totalTokens: 30 } };
  const projected = projectSessionActivity(snapshot([row]));
  const entry = projected.tree?.root.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing delegate");
  expect(entry.delegate.usage).toEqual({ totalTokens: 30 });
  expect(entry.delegate.usage).not.toHaveProperty("inputTokens");
  expect(entry.delegate.usage).not.toHaveProperty("outputTokens");
});

test("unresolved shell and delegate placement preserves original RPC ownership", () => {
  const state = snapshot([delegate("orphan", "remote:missing", "remote:orphan", "unloaded")]);
  state.jobs.rows = [{ ...jobFixture(), ownerRef: "remote:missing" }];
  const projected = projectSessionActivity(state);
  if (!projected.tree) throw new Error("missing loaded projection");
  expect([...indexActivityEntities(projected.tree).values()].map((row) => row.parentRef)).toEqual([
    "remote:missing",
    "remote:missing",
  ]);
  expect(projected.complete).toBe(false);
});
test("watch rows retain receiver identity, domain fields and explicit unknown state", () => {
  const first = watch("host-a:receiver"),
    second = watch("host-b:receiver", "ended");
  const rows = buildWatchRows([first, second]);
  expect(rows.map(({ id }) => id)).toEqual([
    `watch:${JSON.stringify([first.receiverRef, first.watch.id])}`,
    `watch:${JSON.stringify([second.receiverRef, second.watch.id])}`,
  ]);
  expect(rows[0]?.watch).toBe(first);
  expect(watchArmedLabel(first.state)).toBe("unknown");
  expect(watchGloss(first)).toContain("unknown");
  expect(watchDeliveryInstants(first)).toEqual([
    Date.parse("2026-09-30T11:59:00Z"),
    Date.parse("2026-09-30T12:00:00Z"),
  ]);
  expect(watchFacts(first, Date.parse("2026-09-30T12:00:00Z"))).toContain("unknown");
  const state = snapshot();
  state.watches.rows = [first, second];
  expect(projectSessionActivity(state).watches).toBe(state.watches.rows);
});

test("recorded delegate names survive domain projection", () => {
  const recorded = subagentOutcomesDelegatesResponse();
  const state = snapshot(recorded.delegates);
  const projected = projectSessionActivity({
    ...state,
    context: recorded.context,
    delegates: { ...state.delegates, context: recorded.context },
  });
  if (!projected.tree) throw new Error("missing domain activity");
  const entities = indexActivityEntities(projected.tree);
  const row = [...entities.values()].find(
    (row) => row.kind === "delegate" && row.delegate.delegateId === "dlg_reported",
  );
  const named = row?.kind === "delegate" ? row.delegate : undefined;
  expect(named?.name).toBe("reported-delegate");
  expect(named?.task).toBe("Fix race in tree settle");
  expect(named?.runGeneration).toBe(1);
  expect(named?.reportPreview).toContain("Fixed the race");
});
