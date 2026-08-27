import { describe, expect, test } from "vitest";
import { type ActivityEntry, type ActivityTree, activityNodeID } from "../panes/session/chrome/activityData";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { activityPanelStore, resetActivityPanelStoreForTests } from "./activityPanel";
import { schedulePanelStoreEviction } from "./panelStoreEviction";

function tree(revision = 1): ActivityTree {
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

function shell(jobId: string): ActivityEntry {
  return {
    kind: "shell",
    job: {
      jobId,
      ownerSessionId: "sess_a",
      ownerRef: "ref_a",
      type: "shell",
      status: "completed",
      terminal: true,
      background: false,
      hasOutput: false,
      description: jobId,
      startedAt: "2026-01-01T00:00:00Z",
      outputBytes: 0,
    },
  };
}

function rootPage(jobId: string, continuation?: string): ActivityTree {
  const page = tree();
  page.root.entries = [shell(jobId)];
  page.root.branch = continuation ? { truncated: true, continuation } : {};
  return page;
}

describe("activityPanelStore", () => {
  test("stable delegate selection survives reconnect and keeps the child transcript target", () => {
    resetActivityPanelStoreForTests();
    const stableTree = (revision: number, status: string): ActivityTree => {
      const running = status === "running";
      return {
        revision,
        root: {
          kind: "session",
          sessionId: "sess_a",
          ref: "ref_a",
          label: "A",
          aggregate: "running",
          counts: { active: status === "running" ? 1 : 0, failed: 0, completed: 0, complete: true },
          entries: [
            {
              kind: "delegate",
              delegate: {
                delegateId: "dlg_stable",
                ownerSessionId: "sess_a",
                rootSessionId: "sess_a",
                childSessionId: "sess_child",
                childRef: "local:sess_child",
                transcriptRef: "local:sess_child",
                type: "delegate",
                lifecycle: running ? "running" : "idle",
                phase: running ? "running" : "idle",
                status: running ? "running" : "idle",
                outcome: running ? undefined : status,
                projectionRevision: revision,
                terminal: !running,
                resumable: true,
                originTurnId: "turn_1",
                parentWatchGranted: true,
                diagnostics: ["observer armed"],
                branch: {},
              },
            },
          ],
          branch: {},
        },
      } as unknown as ActivityTree;
    };

    const first = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", first, { kind: "ready", tree: stableTree(1, "running") });
    activityPanelStore.getState().setSelected("ref_a", "delegate:dlg_stable");

    const reconnect = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", reconnect, {
      kind: "ready",
      tree: stableTree(2, "completed"),
    });

    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.disclosure.selectedID).toBe("delegate:dlg_stable");
    if (entry?.load.kind !== "ready") throw new Error("expected ready activity tree");
    const delegate = entry.load.tree.root.entries[0];
    expect(delegate?.kind).toBe("delegate");
    if (delegate?.kind !== "delegate") throw new Error("expected stable delegate entry");
    expect(delegate.delegate.childRef).toBe("local:sess_child");
  });

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
    const patch = tree();
    patch.root.counts = { active: 99, failed: 99, completed: 99, complete: false };
    activityPanelStore.getState().publishFetch("ref_a", continuation, { kind: "ready", tree: patch });
    expect(activityPanelStore.getState().entries.get("ref_a")?.load).toMatchObject({
      kind: "ready",
      tree: { root: { counts: { active: 1, failed: 0, completed: 0, complete: true } } },
    });
  });

  test("appends same-revision root continuation deltas exactly once until the branch completes", () => {
    resetActivityPanelStoreForTests();
    const first = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore
      .getState()
      .publishFetch("ref_a", first, { kind: "ready", tree: rootPage("job_page_1", "page-2") });

    const second = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore
      .getState()
      .publishFetch("ref_a", second, { kind: "ready", tree: rootPage("job_page_2", "page-3") });

    let entry = activityPanelStore.getState().entries.get("ref_a");
    if (entry?.load.kind !== "ready") throw new Error("expected ready activity tree after page two");
    expect(entry.load.tree.root.entries.map((activity) => activityNodeID(activity))).toEqual([
      "job:job_page_1",
      "job:job_page_2",
    ]);
    expect(entry.load.tree.root.branch.continuation).toBe("page-3");

    const third = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().publishFetch("ref_a", third, { kind: "ready", tree: rootPage("job_page_3") });

    entry = activityPanelStore.getState().entries.get("ref_a");
    if (entry?.load.kind !== "ready") throw new Error("expected ready activity tree after page three");
    expect(entry.load.tree.root.entries.map((activity) => activityNodeID(activity))).toEqual([
      "job:job_page_1",
      "job:job_page_2",
      "job:job_page_3",
    ]);
    expect(entry.load.tree.root.branch.continuation).toBeUndefined();
  });

  test("rejects a continuation from another revision and requests a root restart", () => {
    resetActivityPanelStoreForTests();
    const retained = tree(1);
    retained.root.branch = { truncated: true, continuation: "page-2", error: "retained branch error" };
    const first = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", first, { kind: "ready", tree: retained });
    const continuation = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });

    const restartRoot = activityPanelStore
      .getState()
      .publishFetch("ref_a", continuation, { kind: "ready", tree: tree(2) });

    expect(restartRoot).toBe(true);
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load).toMatchObject({
      kind: "ready",
      tree: {
        revision: 1,
        root: {
          branch: { truncated: true, continuation: "page-2", error: "retained branch error" },
        },
      },
    });
    expect(entry?.continuationLoadingID).toBeUndefined();
    expect(entry?.pending).toBeUndefined();
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
    activityPanelStore.getState().publishFetch("ref_a", retry, { kind: "ready", tree: tree() });
    expect(activityPanelStore.getState().entries.get("ref_a")?.continuationFailures).toEqual({});
  });

  test("preserves selected and expanded descendants through a graft and consumer remount", () => {
    resetActivityPanelStoreForTests();
    const first = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", first, { kind: "ready", tree: tree() });
    activityPanelStore.getState().setSelected("ref_a", "session:sess_a");
    activityPanelStore.getState().setExpanded("ref_a", ["session:sess_a"]);
    const continuation = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().publishFetch("ref_a", continuation, { kind: "ready", tree: tree() });
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
    expect(activityPanelStore.getState().entries.get("ref_a")?.expandedFoldIDs).toEqual(["session:s1:inactive-fold"]);
    activityPanelStore.getState().toggleFold("ref_a", "session:s1:inactive-fold");
    expect(activityPanelStore.getState().entries.get("ref_a")?.expandedFoldIDs).toEqual([]);
    // Independent per ref:
    activityPanelStore.getState().toggleFold("ref_a", "fold:1");
    activityPanelStore.getState().toggleFold("ref_b", "fold:2");
    expect(activityPanelStore.getState().entries.get("ref_a")?.expandedFoldIDs).toEqual(["fold:1"]);
    expect(activityPanelStore.getState().entries.get("ref_b")?.expandedFoldIDs).toEqual(["fold:2"]);
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
