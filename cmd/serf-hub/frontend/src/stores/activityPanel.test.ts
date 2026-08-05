import { describe, expect, test } from "vitest";
import { activityPanelStore, resetActivityPanelStoreForTests } from "./activityPanel";

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

  test("publishes a root completion after the initiating reader is gone", () => {
    resetActivityPanelStoreForTests();
    const request = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", request, { kind: "ready", tree: tree() });
    expect(activityPanelStore.getState().entries.get("ref_a")?.load.kind).toBe("ready");
  });
});
