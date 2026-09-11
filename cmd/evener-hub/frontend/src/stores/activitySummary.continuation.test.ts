import { beforeEach, expect, test, vi } from "vitest";
import type { ActivityTree } from "../protocol/activityData";
import { activityPanelStore, retainedActivityTree } from "./activityPanel";
import { activitySummaryStore } from "./activitySummary";

const ref = "ref_pages";
const nodeID = "session:pages";

function tree(ids: string[]): ActivityTree {
  return {
    revision: 1,
    root: {
      kind: "session",
      sessionId: "pages",
      ref,
      label: "Pages",
      aggregate: "working",
      counts: { active: ids.length, failed: 0, completed: 0, complete: true },
      branch: {},
      entries: ids.map((jobId) => ({
        kind: "shell",
        job: {
          jobId,
          ownerSessionId: "pages",
          ownerRef: ref,
          type: "shell",
          status: "running",
          terminal: false,
          background: true,
          hasOutput: false,
          description: jobId,
          startedAt: "2026-09-10T00:00:00Z",
          outputBytes: 0,
        },
      })),
    },
  };
}

function rootSettled(): Promise<void> {
  if (!activitySummaryStore.getState().entries.get(ref)?.loading) return Promise.resolve();
  return new Promise((resolve) => {
    const unsubscribe = activitySummaryStore.subscribe((state) => {
      if (!state.entries.get(ref)?.loading) {
        unsubscribe();
        resolve();
      }
    });
  });
}

function heldRoot() {
  let resolve!: (value: ActivityTree) => void;
  const response = new Promise<ActivityTree>((done) => {
    resolve = done;
  });
  activitySummaryStore.getState().refreshRoot(ref, 20, () => response);
  return async () => {
    const settled = rootSettled();
    resolve(tree(["first"]));
    await settled;
  };
}

beforeEach(() => {
  activitySummaryStore.getState().resetForTests();
  activityPanelStore.getState().resetForTests();
  const summary = activitySummaryStore.getState().beginRootFetch(ref, 10);
  if (summary === null) throw new Error("initial fetch rejected");
  activitySummaryStore.getState().publishRootFetch(ref, summary, tree(["first"]).root.counts);
  const panel = activityPanelStore.getState().beginFetch(ref);
  activityPanelStore.getState().publishFetch(ref, panel, { kind: "ready", tree: tree(["first"]) });
});

test.each([true, false])("successful continuation stays fresh when old root settles first: %s", async (rootFirst) => {
  const settleRoot = heldRoot();
  const page = activityPanelStore.getState().beginFetch(ref, { nodeID });
  if (rootFirst) await settleRoot();
  activityPanelStore.getState().publishFetch(ref, page, { kind: "ready", tree: tree(["second"]) });
  if (!rootFirst) await settleRoot();
  const backgroundFetch = vi.fn(async () => tree(["first"]));
  expect(activitySummaryStore.getState().refreshRoot(ref, 20, backgroundFetch)).toBeNull();
  expect(backgroundFetch).not.toHaveBeenCalled();
  expect(retainedActivityTree(activityPanelStore.getState().entries.get(ref))?.root.entries).toHaveLength(2);
  expect(activitySummaryStore.getState().entries.get(ref)?.counts?.active).toBe(2);
});

test.each([true, false])(
  "successful continuation retry restores freshness when old root settles first: %s",
  async (rootFirst) => {
    const settleRoot = heldRoot();
    const page = activityPanelStore.getState().beginFetch(ref, { nodeID });
    if (rootFirst) await settleRoot();
    activityPanelStore
      .getState()
      .publishFetch(ref, page, { kind: "continuation-failed", nodeID, message: "unavailable" });
    expect(activitySummaryStore.getState().entries.get(ref)?.lastFetchedBump).toBeUndefined();
    const retry = activityPanelStore.getState().beginFetch(ref, { nodeID });
    activityPanelStore.getState().publishFetch(ref, retry, { kind: "ready", tree: tree(["second"]) });
    if (!rootFirst) await settleRoot();
    const backgroundFetch = vi.fn(async () => tree(["first"]));
    expect(activitySummaryStore.getState().refreshRoot(ref, 20, backgroundFetch)).toBeNull();
    expect(backgroundFetch).not.toHaveBeenCalled();
    expect(retainedActivityTree(activityPanelStore.getState().entries.get(ref))?.root.entries).toHaveLength(2);
    expect(activityPanelStore.getState().entries.get(ref)?.continuationFailures[nodeID]).toBeUndefined();
  },
);

test("a continuation failure cannot queue an older bump behind the in-flight root", async () => {
  const settleRoot = heldRoot();
  const page = activityPanelStore.getState().beginFetch(ref, { nodeID });
  activityPanelStore
    .getState()
    .publishFetch(ref, page, { kind: "continuation-failed", nodeID, message: "unavailable" });
  const olderFetch = vi.fn(async () => tree(["older"]));
  activitySummaryStore.getState().refreshRoot(ref, 15, olderFetch);
  expect(activitySummaryStore.getState().entries.get(ref)?.pendingBump).toBeUndefined();
  await settleRoot();
  expect(olderFetch).not.toHaveBeenCalled();
});

test("an older continuation failure cannot invalidate a newer root generation", () => {
  const page = activityPanelStore.getState().beginFetch(ref, { nodeID });
  const newer = activitySummaryStore.getState().beginRootFetch(ref, 30);
  activityPanelStore
    .getState()
    .publishFetch(ref, page, { kind: "continuation-failed", nodeID, message: "unavailable" });
  expect(activitySummaryStore.getState().entries.get(ref)).toMatchObject({
    requestID: newer,
    loading: true,
    lastFetchedBump: 30,
  });
});

test("a failed continuation queues a same-bump replacement while the old root is still loading", async () => {
  const settleRoot = heldRoot();
  const page = activityPanelStore.getState().beginFetch(ref, { nodeID });
  activityPanelStore
    .getState()
    .publishFetch(ref, page, { kind: "continuation-failed", nodeID, message: "unavailable" });
  let finishReplacement!: (value: ActivityTree) => void;
  const replacement = new Promise<ActivityTree>((resolve) => {
    finishReplacement = resolve;
  });
  const fetch = vi.fn(() => replacement);
  activitySummaryStore.getState().refreshRoot(ref, 20, fetch);
  await settleRoot();
  expect(fetch).toHaveBeenCalledOnce();
  const settled = rootSettled();
  finishReplacement(tree(["first", "second", "third"]));
  await settled;
  expect(activitySummaryStore.getState().entries.get(ref)).toMatchObject({
    lastFetchedBump: 20,
    loading: false,
    counts: { active: 3 },
  });
});

test("a continuation failure keeps an already published root fresh", () => {
  const page = activityPanelStore.getState().beginFetch(ref, { nodeID });
  activityPanelStore
    .getState()
    .publishFetch(ref, page, { kind: "continuation-failed", nodeID, message: "unavailable" });
  const backgroundFetch = vi.fn(async () => tree(["first"]));
  expect(activitySummaryStore.getState().refreshRoot(ref, 10, backgroundFetch)).toBeNull();
  expect(backgroundFetch).not.toHaveBeenCalled();
  expect(activityPanelStore.getState().entries.get(ref)?.continuationFailures[nodeID]).toBe("unavailable");
});

test("a later continuation failure keeps successful pages fresh while the old root settles", async () => {
  const settleRoot = heldRoot();
  const page = activityPanelStore.getState().beginFetch(ref, { nodeID });
  activityPanelStore.getState().publishFetch(ref, page, { kind: "ready", tree: tree(["second"]) });
  const nextPage = activityPanelStore.getState().beginFetch(ref, { nodeID });
  activityPanelStore
    .getState()
    .publishFetch(ref, nextPage, { kind: "continuation-failed", nodeID, message: "unavailable" });
  await settleRoot();
  const backgroundFetch = vi.fn(async () => tree(["first"]));
  expect(activitySummaryStore.getState().refreshRoot(ref, 20, backgroundFetch)).toBeNull();
  expect(backgroundFetch).not.toHaveBeenCalled();
  expect(retainedActivityTree(activityPanelStore.getState().entries.get(ref))?.root.entries).toHaveLength(2);
});

test("a queued root refresh waits for the continuation it would otherwise discard", async () => {
  const settleRoot = heldRoot();
  const queued = vi.fn(async () => tree(["first", "second", "third"]));
  expect(activitySummaryStore.getState().refreshRoot(ref, 30, queued)).toBeNull();
  const page = activityPanelStore.getState().beginFetch(ref, { nodeID });
  await settleRoot();
  expect(queued).not.toHaveBeenCalled();
  activityPanelStore.getState().publishFetch(ref, page, { kind: "ready", tree: tree(["second"]) });
  expect(retainedActivityTree(activityPanelStore.getState().entries.get(ref))?.root.entries).toHaveLength(2);
  expect(queued).toHaveBeenCalledOnce();
  await rootSettled();
  expect(activitySummaryStore.getState().entries.get(ref)).toMatchObject({
    lastFetchedBump: 30,
    loading: false,
    counts: { active: 3 },
  });
});

test("a jobs bump with no root in flight waits for the continuation it would otherwise discard", async () => {
  const page = activityPanelStore.getState().beginFetch(ref, { nodeID });
  const bumped = vi.fn(async () => tree(["first", "second", "third"]));
  const requestID = activitySummaryStore.getState().refreshRoot(ref, 30, bumped);
  expect(bumped).not.toHaveBeenCalled();
  expect(requestID).toBeNull();
  activityPanelStore.getState().publishFetch(ref, page, { kind: "ready", tree: tree(["second"]) });
  expect(retainedActivityTree(activityPanelStore.getState().entries.get(ref))?.root.entries).toHaveLength(2);
  expect(bumped).toHaveBeenCalledOnce();
  await rootSettled();
  expect(activitySummaryStore.getState().entries.get(ref)).toMatchObject({
    lastFetchedBump: 30,
    loading: false,
    counts: { active: 3 },
  });
});
