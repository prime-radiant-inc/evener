import { describe, expect, test } from "vitest";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { activityPanelStore, resetActivityPanelStoreForTests } from "./activityPanel";
import { schedulePanelStoreEviction } from "./panelStoreEviction";

function tree(revision = 1) {
  return {
    revision,
    root: {
      kind: "session" as const,
      sessionId: "sess_a",
      ref: "ref_a",
      label: "A",
      aggregate: "running",
      counts: { active: 1, failed: 0, completed: 0, complete: true },
      entries: [],
      branch: {},
    },
  };
}

describe("activityPanelStore", () => {
  test("retains disclosure selection and expansion across root refresh", () => {
    resetActivityPanelStoreForTests();
    const first = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", first, { kind: "ready", tree: tree() });
    activityPanelStore.getState().setExpanded("ref_a", ["session:sess_a"]);
    activityPanelStore.getState().setSelected("ref_a", "session:sess_a");
    const refresh = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", refresh, { kind: "ready", tree: tree(2) });
    expect(activityPanelStore.getState().entries.get("ref_a")?.disclosure).toMatchObject({
      expandedIDs: ["session:sess_a"],
      selectedID: "session:sess_a",
    });
  });

  test("grafts a continuation without replacing root counts", () => {
    resetActivityPanelStoreForTests();
    const first = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", first, { kind: "ready", tree: tree() });
    const continuation = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    const patch = tree(2);
    patch.root.counts = { active: 99, failed: 99, completed: 99, complete: false };
    activityPanelStore.getState().publishFetch("ref_a", continuation, { kind: "ready", tree: patch });
    expect(activityPanelStore.getState().entries.get("ref_a")?.load).toMatchObject({
      kind: "ready",
      tree: { root: { counts: { active: 1, failed: 0, completed: 0, complete: true } } },
    });
  });

  test("retains a continuation failure and clears it after retry", () => {
    resetActivityPanelStoreForTests();
    const first = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", first, { kind: "ready", tree: tree() });
    const continuation = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().publishFetch("ref_a", continuation, {
      kind: "continuation-failed",
      nodeID: "session:sess_a",
      message: "branch failed",
    });
    expect(activityPanelStore.getState().entries.get("ref_a")?.continuationFailures).toMatchObject({
      "session:sess_a": "branch failed",
    });
    const retry = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().publishFetch("ref_a", retry, { kind: "ready", tree: tree(2) });
    expect(activityPanelStore.getState().entries.get("ref_a")?.continuationFailures).toEqual({});
  });

  test("preserves selected and expanded descendants through a graft and consumer remount", () => {
    resetActivityPanelStoreForTests();
    const first = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", first, { kind: "ready", tree: tree() });
    activityPanelStore.getState().setSelected("ref_a", "session:sess_a");
    activityPanelStore.getState().setExpanded("ref_a", ["session:sess_a"]);
    const continuation = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().publishFetch("ref_a", continuation, { kind: "ready", tree: tree(2) });
    const remounted = activityPanelStore.getState().entries.get("ref_a");
    expect(remounted?.disclosure.selectedID).toBe("session:sess_a");
    expect(remounted?.disclosure.expandedIDs).toEqual(["session:sess_a"]);
    expect(remounted?.load.kind).toBe("ready");
  });

  test("publishes a root completion after the initiating reader is gone", () => {
    resetActivityPanelStoreForTests();
    const request = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", request, { kind: "ready", tree: tree() });
    expect(activityPanelStore.getState().entries.get("ref_a")?.load.kind).toBe("ready");
  });

  test("toggleFold flips fold membership per session ref", () => {
    resetActivityPanelStoreForTests();
    activityPanelStore.getState().toggleFold("ref_a", "session:s1:inactive-fold");
    expect(activityPanelStore.getState().entries.get("ref_a")?.collapsedFoldIDs).toEqual(["session:s1:inactive-fold"]);
    activityPanelStore.getState().toggleFold("ref_a", "session:s1:inactive-fold");
    expect(activityPanelStore.getState().entries.get("ref_a")?.collapsedFoldIDs).toEqual([]);
    // Independent per ref:
    activityPanelStore.getState().toggleFold("ref_a", "fold:1");
    activityPanelStore.getState().toggleFold("ref_b", "fold:2");
    expect(activityPanelStore.getState().entries.get("ref_a")?.collapsedFoldIDs).toEqual(["fold:1"]);
    expect(activityPanelStore.getState().entries.get("ref_b")?.collapsedFoldIDs).toEqual(["fold:2"]);
  });

  test("a completion from before eviction cannot publish into a recreated entry", async () => {
    resetActivityPanelStoreForTests();
    resetWorkspaceStoreForTests();
    const stale = activityPanelStore.getState().beginFetch("ref_a");
    schedulePanelStoreEviction();
    await Promise.resolve();
    expect(activityPanelStore.getState().entries.has("ref_a")).toBe(false);

    const fresh = activityPanelStore.getState().beginFetch("ref_a");
    expect(fresh).not.toBe(stale);
    activityPanelStore.getState().publishFetch("ref_a", stale, { kind: "ready", tree: tree(9) });
    expect(activityPanelStore.getState().entries.get("ref_a")?.load.kind).toBe("loading");
  });
});
