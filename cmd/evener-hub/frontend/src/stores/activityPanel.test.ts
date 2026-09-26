// @vitest-environment node

import type { ActivityTree } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { activityPanelStore, resetActivityPanelStoreForTests, retainedActivityTree } from "./activityPanel";
import { activitySummaryStore, initActivitySummary, resetActivitySummaryStoreForTests } from "./activitySummary";
import { linkFakeActivitySummary } from "./activitySummaryLinkTestUtils";
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
  test("a second branch cannot replace the continuation already in flight", () => {
    resetActivityPanelStoreForTests();
    initActivitySummary();
    resetActivitySummaryStoreForTests();
    const root = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", root, { kind: "ready", tree: tree() });
    const first = activityPanelStore.getState().beginContinuationFetch("ref_a", "session:sess_a");
    if (first === null) throw new Error("the first page was refused");
    const second = activityPanelStore.getState().beginContinuationFetch("ref_a", "delegate:dlg_other");
    activityPanelStore.getState().publishFetch("ref_a", first, { kind: "ready", tree: tree() });
    expect(activityPanelStore.getState().entries.get("ref_a")?.load).toMatchObject({
      kind: "ready",
      tree: { revision: 1 },
    });
    expect(second).toBeNull();
  });

  test("stable delegate selection survives reconnect and keeps the child transcript target", () => {
    resetActivityPanelStoreForTests();
    initActivitySummary();
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
    initActivitySummary();
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

  test("derives a continuation summary from the merged entries", () => {
    resetActivityPanelStoreForTests();
    initActivitySummary();
    const first = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", first, { kind: "ready", tree: tree() });
    const continuation = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    const patch = tree();
    patch.root.counts = { active: 99, failed: 99, completed: 99, complete: false };
    activityPanelStore.getState().publishFetch("ref_a", continuation, { kind: "ready", tree: patch });
    expect(activityPanelStore.getState().entries.get("ref_a")?.load).toMatchObject({
      kind: "ready",
      tree: { root: { aggregate: "idle", counts: { active: 0, failed: 0, completed: 0, complete: true } } },
    });
  });

  test.each([false, true])("continuation summary preserves a newer root request: %s", (newerRoot) => {
    resetActivityPanelStoreForTests();
    initActivitySummary();
    resetActivitySummaryStoreForTests();
    const original = tree();
    const summaryRequest = activitySummaryStore.getState().beginRootFetch("ref_a", 1);
    if (summaryRequest === null) throw new Error("initial summary request was not admitted");
    activitySummaryStore.getState().publishRootFetch("ref_a", summaryRequest, original.root.counts);
    const rootRequest = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", rootRequest, { kind: "ready", tree: original });

    const continuation = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    const expectedGeneration = newerRoot
      ? activitySummaryStore.getState().beginRootFetch("ref_a", 2, true)
      : summaryRequest;
    // Deliver the already-requested page only after the optional newer root request.
    activityPanelStore.getState().publishFetch("ref_a", continuation, { kind: "ready", tree: tree() });

    expect(activitySummaryStore.getState().entries.get("ref_a")).toMatchObject({
      requestID: expectedGeneration,
      loading: newerRoot,
      counts: newerRoot ? original.root.counts : { active: 0, failed: 0, completed: 0, complete: true },
    });
  });

  test("retains a continuation failure and clears it after retry", () => {
    resetActivityPanelStoreForTests();
    initActivitySummary();
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
    initActivitySummary();
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
    initActivitySummary();
    const request = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", request, { kind: "ready", tree: tree() });
    expect(activityPanelStore.getState().entries.get("ref_a")?.load.kind).toBe("ready");
  });

  test("toggleFold flips fold membership per session ref", () => {
    resetActivityPanelStoreForTests();
    initActivitySummary();
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
    initActivitySummary();
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

// Edge cases for activityPanel.ts that close remaining uncovered lines:
// - retainedActivityTree with undefined entry (line 96)
// - publishFetch continuation paths (lines 314-317, 327)
// - applyFetchResult continuation-failed (lines 371, 380)

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

// This suite drives the panel without importing ./activitySummary, so each
// test supplies the summary side the continuation protocol requires.
describe("activityPanelStore continuation paths", () => {
  test("beginFetch + publishFetch ready stores the tree", () => {
    resetActivityPanelStoreForTests();
    linkFakeActivitySummary();
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
    linkFakeActivitySummary();
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
    linkFakeActivitySummary();
    const error = { headline: "Network error", sentence: "network error", detail: "connection lost" };
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "failed", error });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load).toEqual({ kind: "failed", error });
    expect(entry?.pending).toBeUndefined();
  });

  test("publishFetch with failed result retains existing tree as stale", () => {
    resetActivityPanelStoreForTests();
    linkFakeActivitySummary();
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
    linkFakeActivitySummary();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "unsupported" });
    const entry = activityPanelStore.getState().entries.get("ref_a");
    expect(entry?.load).toEqual({ kind: "unsupported" });
    expect(entry?.pending).toBeUndefined();
  });

  test("continuation fetch with ready result grafts the tree", () => {
    resetActivityPanelStoreForTests();
    linkFakeActivitySummary();
    const current = makeTreeWithDelegate();
    // The retained tree and its continuation page share a revision: a page from
    // a different revision is discarded by the consumer, not grafted.
    current.revision = 2;
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
    linkFakeActivitySummary();
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

  test("a discarded continuation clears loading without recording a failure", () => {
    resetActivityPanelStoreForTests();
    linkFakeActivitySummary();
    const tree = makeTreeWithDelegate();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree });

    const contReq = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "delegate:dlg_1" });
    activityPanelStore.getState().publishFetch("ref_a", contReq, {
      kind: "continuation-discarded",
      nodeID: "delegate:dlg_1",
    });

    const entry = activityPanelStore.getState().entries.get("ref_a");
    // The retained tree is untouched and the page leaves no failure behind: the
    // consumer refetches the root instead.
    expect(entry?.load).toEqual({ kind: "ready", tree });
    expect(entry?.continuationFailures).toEqual({});
    expect(entry?.continuationLoadingID).toBeUndefined();
    expect(entry?.pending).toBeUndefined();
  });

  test("a mismatched continuation page is discarded, not recorded as a merge", () => {
    resetActivityPanelStoreForTests();
    const { settled } = linkFakeActivitySummary();
    const tree = makeTreeWithDelegate();
    const req = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", req, { kind: "ready", tree });

    const contReq = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "delegate:dlg_1" });
    const page = makeTreeWithDelegate();
    page.revision = tree.revision + 1;
    activityPanelStore.getState().publishFetch("ref_a", contReq, { kind: "ready", tree: page });

    const entry = activityPanelStore.getState().entries.get("ref_a");
    // The retained tree is untouched and the page leaves no failure or counts
    // claim behind: the caller refetches the root instead.
    expect(entry?.load).toEqual({ kind: "ready", tree });
    expect(entry?.continuationFailures).toEqual({});
    expect(entry?.continuationLoadingID).toBeUndefined();
    expect(entry?.pending).toBeUndefined();
    expect(settled).toHaveBeenCalledWith("ref_a", { summaryRequestID: undefined, debt: undefined });
  });

  test("setExpanded and setSelected update disclosure", () => {
    resetActivityPanelStoreForTests();
    linkFakeActivitySummary();
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
    linkFakeActivitySummary();
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

// The ActivitySummaryLink seam, driven directly with a fake summary side.

function linkWithGeneration(generation: number | undefined) {
  return linkFakeActivitySummary(generation).settled;
}

function publishRoot(): void {
  const root = activityPanelStore.getState().beginFetch("ref_a");
  activityPanelStore.getState().publishFetch("ref_a", root, { kind: "ready", tree: tree() });
}

describe("the summary link", () => {
  test("a merged page settles with the generation it began under and the merged counts", () => {
    resetActivityPanelStoreForTests();
    const settled = linkWithGeneration(7);
    publishRoot();
    const page = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    expect(settled).not.toHaveBeenCalled();
    activityPanelStore.getState().publishFetch("ref_a", page, { kind: "ready", tree: tree() });
    const merged = activityPanelStore.getState().entries.get("ref_a")?.load;
    if (merged?.kind !== "ready") throw new Error("expected the page to merge");
    expect(settled).toHaveBeenCalledTimes(1);
    expect(settled).toHaveBeenCalledWith("ref_a", {
      summaryRequestID: 7,
      debt: { kind: "counts", counts: merged.tree.root.counts },
    });
  });

  test("a failed page settles as a failure debt", () => {
    resetActivityPanelStoreForTests();
    const settled = linkWithGeneration(7);
    publishRoot();
    const page = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().publishFetch("ref_a", page, {
      kind: "continuation-failed",
      nodeID: "session:sess_a",
      message: "branch failed",
    });
    expect(settled).toHaveBeenCalledWith("ref_a", { summaryRequestID: 7, debt: { kind: "failure" } });
  });

  test("a page begun with no summary entry still settles, so a queued root can be issued", () => {
    resetActivityPanelStoreForTests();
    const settled = linkWithGeneration(undefined);
    publishRoot();
    const page = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().publishFetch("ref_a", page, { kind: "ready", tree: tree() });
    expect(settled).toHaveBeenCalledWith("ref_a", {
      summaryRequestID: undefined,
      debt: { kind: "counts", counts: expect.anything() },
    });
  });

  test("a continuation with no summary registered fails loudly", () => {
    resetActivityPanelStoreForTests();
    const { unlink } = linkFakeActivitySummary(7);
    publishRoot();
    unlink();
    expect(() => activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" })).toThrow(
      /no summary link registered/,
    );
    expect(activityPanelStore.getState().entries.get("ref_a")?.pending).toBeUndefined();
  });

  test("a continuation publish with no summary registered throws with the page still pending", () => {
    resetActivityPanelStoreForTests();
    const { unlink } = linkFakeActivitySummary(7);
    publishRoot();
    const page = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    unlink();
    expect(() => activityPanelStore.getState().publishFetch("ref_a", page, { kind: "ready", tree: tree(2) })).toThrow(
      /no summary link registered/,
    );
    expect(activityPanelStore.getState().entries.get("ref_a")?.pending).toMatchObject({
      kind: "continuation",
      nodeID: "session:sess_a",
    });
  });

  test("resetting the store drops the link, and the init puts it back", () => {
    resetActivityPanelStoreForTests();
    initActivitySummary();
    publishRoot();
    resetActivityPanelStoreForTests();
    publishRoot();
    expect(() => activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" })).toThrow(
      /no summary link registered/,
    );
    initActivitySummary();
    expect(() => activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" })).not.toThrow();
  });

  test("a dropped stale page settles nothing", () => {
    resetActivityPanelStoreForTests();
    const settled = linkWithGeneration(7);
    publishRoot();
    const stale = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", stale, { kind: "ready", tree: tree(2) });
    expect(settled).not.toHaveBeenCalled();
  });
});
