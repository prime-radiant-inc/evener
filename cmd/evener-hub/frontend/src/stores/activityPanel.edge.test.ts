// Edge cases for activityPanel.ts that close remaining uncovered lines:
// - retainedActivityTree with undefined entry (line 96)
// - publishFetch continuation paths (lines 314-317, 327)
// - applyFetchResult continuation-failed (lines 371, 380)

import { describe, expect, test } from "vitest";
import type { ActivityTree } from "../panes/session/chrome/activityData";
import { activityPanelStore, resetActivityPanelStoreForTests, retainedActivityTree } from "./activityPanel";

function makeTree(revision = 1): ActivityTree {
  return {
    revision,
    root: {
      kind: "session",
      sessionId: "sess_a",
      ref: "ref_a",
      label: "A",
      aggregate: "running",
      counts: { active: 0, failed: 0, completed: 0, complete: true },
      entries: [],
      branch: {},
    },
  };
}

function makeTreeWithDelegate(): ActivityTree {
  return {
    revision: 1,
    root: {
      kind: "session",
      sessionId: "sess_a",
      ref: "ref_a",
      label: "A",
      aggregate: "running",
      counts: { active: 1, failed: 0, completed: 0, complete: true },
      entries: [
        {
          kind: "delegate",
          delegate: {
            delegateId: "dlg_1",
            childSessionId: "sess_child",
            childRef: "ref_child",
            branch: {},
            projectionRevision: 1,
            child: {
              kind: "session",
              sessionId: "sess_child",
              ref: "ref_child",
              label: "Child",
              aggregate: "running",
              counts: { active: 0, failed: 0, completed: 0, complete: true },
              entries: [],
              branch: {},
            },
          },
        },
      ],
      branch: {},
    },
  };
}

describe("retainedActivityTree", () => {
  test("returns undefined for undefined entry", () => {
    expect(retainedActivityTree(undefined)).toBeUndefined();
  });
});

describe("activityPanelStore continuation paths", () => {
  test("beginFetch + publishFetch ready stores the tree", () => {
    resetActivityPanelStoreForTests();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree: makeTree() });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry).toBeTruthy();
    expect(entry?.load.kind).toBe("ready");
  });

  test("beginFetch + publishFetch with delegate tree stores it", () => {
    resetActivityPanelStoreForTests();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree: makeTreeWithDelegate() });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry).toBeTruthy();
    expect(entry?.load.kind).toBe("ready");
    if (entry?.load.kind === "ready") {
      expect(entry.load.tree.root.entries).toHaveLength(1);
    }
  });

  test("publishFetch with ended result retains the tree", () => {
    resetActivityPanelStoreForTests();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree: makeTree() });
    const req2 = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req2, { kind: "ended" });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load.kind).toBe("ended");
  });

  test("publishFetch with failed result shows error", () => {
    resetActivityPanelStoreForTests();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, {
      kind: "failed",
      error: { headline: "Network error", sentence: "network error", detail: "connection lost" },
    });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load.kind).toBe("failed");
  });

  test("publishFetch with failed result retains existing tree as stale", () => {
    resetActivityPanelStoreForTests();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree: makeTree() });
    const req2 = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req2, {
      kind: "failed",
      error: { headline: "Reconnect failed", sentence: "reconnect failed", detail: "timeout" },
    });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load.kind).toBe("ready");
    if (entry?.load.kind === "ready") {
      expect(entry.load.staleError?.sentence).toBe("reconnect failed");
    }
  });

  test("publishFetch with unsupported result sets unsupported load", () => {
    resetActivityPanelStoreForTests();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "unsupported" });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load.kind).toBe("unsupported");
  });

  test("continuation fetch with ready result grafts the tree", () => {
    resetActivityPanelStoreForTests();
    // First establish the tree
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree: makeTreeWithDelegate() });

    // Start a continuation fetch
    const contReq = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "delegate:dlg_1" });

    // Build a continuation patch for the child session
    const patch: ActivityTree = {
      revision: 2,
      root: {
        kind: "session",
        sessionId: "sess_child",
        ref: "ref_child",
        label: "Child",
        aggregate: "running",
        counts: { active: 0, failed: 0, completed: 0, complete: true },
        entries: [
          {
            kind: "shell",
            job: {
              jobId: "job_1",
              ownerSessionId: "sess_child",
              ownerRef: "ref_child",
              type: "shell",
              status: "running",
              terminal: false,
              background: false,
              hasOutput: false,
              description: "test job",
              startedAt: "2026-01-01T00:00:00Z",
              outputBytes: 0,
            },
          },
        ],
        branch: {},
      },
    };

    activityPanelStore.getState().publishFetch("ref_a", contReq, { kind: "ready", tree: patch });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load.kind).toBe("ready");
    expect(entry?.continuationLoadingID).toBeUndefined();
  });

  test("continuation fetch with failed result records failure", () => {
    resetActivityPanelStoreForTests();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree: makeTreeWithDelegate() });

    const contReq = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "delegate:dlg_1" });
    activityPanelStore.getState().publishFetch("ref_a", contReq, {
      kind: "continuation-failed",
      nodeID: "delegate:dlg_1",
      message: "Couldn't load more retained activity for this branch.",
    });

    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.continuationFailures["delegate:dlg_1"]).toBe("Couldn't load more retained activity for this branch.");
    expect(entry?.continuationLoadingID).toBeUndefined();
  });

  test("setExpanded and setSelected update disclosure", () => {
    resetActivityPanelStoreForTests();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree: makeTree() });
    activityPanelStore.getState().setExpanded("ref_a", ["session:sess_a"]);
    activityPanelStore.getState().setSelected("ref_a", "session:sess_a");
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.disclosure.expandedIDs).toContain("session:sess_a");
    expect(entry?.disclosure.selectedID).toBe("session:sess_a");
  });

  test("stale requestID is ignored", () => {
    resetActivityPanelStoreForTests();
    const req1 = activityPanelStore.getState().beginFetch("ref_a");
    const req2 = activityPanelStore.getState().beginFetch("ref_a");
    // Publish with old requestID — should be ignored
    activityPanelStore.getState().publishFetch("ref_a", req1, { kind: "ready", tree: makeTree() });
    // The entry should still be in loading state (req2 is the active one)
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.requestID).toBe(req2);
  });
});
