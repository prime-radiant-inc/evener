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
    const tree = makeTree();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load).toEqual({ kind: "ready", tree });
    expect(entry?.established).toBe(true);
    expect(entry?.pending).toBeUndefined();
  });

  test("publishFetch with ended result retains the tree", () => {
    resetActivityPanelStoreForTests();
    const tree = makeTree();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree });
    const req2 = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req2, { kind: "ended" });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load).toEqual({ kind: "ended", tree });
    expect(entry?.pending).toBeUndefined();
  });

  test("publishFetch with failed result shows error", () => {
    resetActivityPanelStoreForTests();
    const error = { headline: "Network error", sentence: "network error", detail: "connection lost" };
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "failed", error });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load).toEqual({ kind: "failed", error });
    expect(entry?.pending).toBeUndefined();
  });

  test("publishFetch with failed result retains existing tree as stale", () => {
    resetActivityPanelStoreForTests();
    const tree = makeTree();
    const error = { headline: "Reconnect failed", sentence: "reconnect failed", detail: "timeout" };
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree });
    const req2 = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req2, { kind: "failed", error });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load).toEqual({ kind: "ready", tree, staleError: error });
    expect(entry?.pending).toBeUndefined();
  });

  test("publishFetch with unsupported result sets unsupported load", () => {
    resetActivityPanelStoreForTests();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "unsupported" });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load).toEqual({ kind: "unsupported" });
    expect(entry?.pending).toBeUndefined();
  });

  test("continuation fetch with ready result grafts the tree", () => {
    resetActivityPanelStoreForTests();
    const current = makeTreeWithDelegate();
    current.root.entries.push({
      kind: "shell",
      job: {
        jobId: "job_sibling",
        ownerSessionId: "sess_a",
        ownerRef: "ref_a",
        type: "shell",
        status: "completed",
        terminal: true,
        background: false,
        hasOutput: true,
        description: "unaffected sibling",
        startedAt: "2026-01-01T00:00:00Z",
        outputBytes: 17,
      },
    });
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree: current });

    // Start a continuation fetch
    const contReq = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "delegate:dlg_1" });

    // A continuation patch preserves the root-to-target path and replaces the
    // targeted delegate with its newer projection.
    const patch = makeTreeWithDelegate();
    patch.revision = 2;
    const patchDelegate = patch.root.entries[0];
    if (patchDelegate?.kind !== "delegate" || !patchDelegate.delegate.child) {
      throw new Error("continuation fixture is missing its delegate child");
    }
    patchDelegate.delegate.projectionRevision = 2;
    patchDelegate.delegate.child.entries = [
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
    ];

    activityPanelStore.getState().publishFetch("ref_a", contReq, { kind: "ready", tree: patch });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load.kind).toBe("ready");
    if (entry?.load.kind === "ready") {
      const delegate = entry.load.tree.root.entries[0];
      expect(entry.load.tree.revision).toBe(2);
      expect(delegate?.kind).toBe("delegate");
      if (delegate?.kind === "delegate") {
        expect(delegate.delegate.projectionRevision).toBe(2);
        expect(delegate.delegate.child?.entries).toEqual([
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
        ]);
      }
      expect(entry.load.tree.root.entries).toContainEqual({
        kind: "shell",
        job: {
          jobId: "job_sibling",
          ownerSessionId: "sess_a",
          ownerRef: "ref_a",
          type: "shell",
          status: "completed",
          terminal: true,
          background: false,
          hasOutput: true,
          description: "unaffected sibling",
          startedAt: "2026-01-01T00:00:00Z",
          outputBytes: 17,
        },
      });
    }
    expect(entry?.continuationLoadingID).toBeUndefined();
    expect(entry?.pending).toBeUndefined();
  });

  test("continuation fetch with failed result records failure", () => {
    resetActivityPanelStoreForTests();
    const tree = makeTreeWithDelegate();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree });
    const readyEntry = activityPanelStore.getState().entries.get("ref_a");
    if (readyEntry?.load.kind !== "ready") throw new Error("fixture did not establish a ready activity tree");
    const readyLoad = readyEntry.load;
    const readyTree = readyLoad.tree;

    const contReq = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "delegate:dlg_1" });
    activityPanelStore.getState().publishFetch("ref_a", contReq, {
      kind: "continuation-failed",
      nodeID: "delegate:dlg_1",
      message: "Couldn't load more retained activity for this branch.",
    });

    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.established).toBe(true);
    expect(entry?.load).toBe(readyLoad);
    expect(entry?.load).toEqual({ kind: "ready", tree: readyTree });
    if (entry?.load.kind === "ready") expect(entry.load.tree).toBe(readyTree);
    expect(entry?.continuationFailures).toEqual({
      "delegate:dlg_1": "Couldn't load more retained activity for this branch.",
    });
    expect(entry?.continuationLoadingID).toBeUndefined();
    expect(entry?.pending).toBeUndefined();
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
    const before = activityPanelStore.getState().entries.get("ref_a");
    // Publish with old requestID — should be ignored
    activityPanelStore.getState().publishFetch("ref_a", req1, { kind: "ready", tree: makeTree() });
    // The entry should still be in loading state (req2 is the active one)
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry).toBe(before);
    expect(entry).toMatchObject({ requestID: req2, load: { kind: "loading" }, pending: { kind: "root" } });
  });
});
