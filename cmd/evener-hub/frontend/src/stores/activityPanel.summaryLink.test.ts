// The ActivitySummaryLink seam, driven directly with a fake summary side.
import type { ActivityTree } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { activityPanelStore, resetActivityPanelStoreForTests } from "./activityPanel";
import { initActivitySummary } from "./activitySummary";
import { linkFakeActivitySummary } from "./activitySummaryLinkTestUtils";

function tree(revision = 1): ActivityTree {
  return {
    revision,
    root: {
      kind: "session",
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
