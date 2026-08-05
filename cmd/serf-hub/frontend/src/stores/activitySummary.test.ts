import { describe, expect, test } from "vitest";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { activityPanelStore } from "./activityPanel";
import { activitySummaryStore, resetActivitySummaryStoreForTests } from "./activitySummary";
import { schedulePanelStoreEviction } from "./panelStoreEviction";

describe("activitySummaryStore", () => {
  test("uses the established-attempt gate and complete-count badge data", () => {
    resetActivitySummaryStoreForTests();
    expect(activitySummaryStore.getState().entries.has("ref_a")).toBe(false);
    const request = activitySummaryStore.getState().beginRootFetch("ref_a", 1);
    expect(request).toBe(1);
    expect(activitySummaryStore.getState().entries.get("ref_a")).toMatchObject({
      established: true,
      loading: true,
      lastFetchedBump: 1,
    });
    activitySummaryStore.getState().publishRootFetch("ref_a", request as number, {
      active: 2,
      failed: 0,
      completed: 1,
      complete: false,
    });
    expect(activitySummaryStore.getState().entries.get("ref_a")?.counts?.complete).toBe(false);
    expect(activitySummaryStore.getState().beginRootFetch("ref_a", 2)).toBe(2);
  });

  test("suppresses duplicate requests while one root fetch is in flight", () => {
    resetActivitySummaryStoreForTests();
    expect(activitySummaryStore.getState().beginRootFetch("ref_a", 1)).toBe(1);
    expect(activitySummaryStore.getState().beginRootFetch("ref_a", 2)).toBeNull();
    activitySummaryStore.getState().failRootFetch("ref_a", 1);
    expect(activitySummaryStore.getState().entries.get("ref_a")).toMatchObject({
      established: true,
      loading: false,
      lastFetchedBump: 1,
    });
    expect(activitySummaryStore.getState().beginRootFetch("ref_a", 1)).toBeNull();
    expect(activitySummaryStore.getState().beginRootFetch("ref_a", 2)).toBe(2);
  });

  test("tracks mounted bodies and allows background refresh after they leave", () => {
    resetActivitySummaryStoreForTests();
    activitySummaryStore.getState().mountBody("ref_a");
    activitySummaryStore.getState().mountBody("ref_a");
    expect(activitySummaryStore.getState().entries.get("ref_a")?.mountedBodies).toBe(2);
    activitySummaryStore.getState().unmountBody("ref_a");
    activitySummaryStore.getState().unmountBody("ref_a");
    expect(activitySummaryStore.getState().entries.get("ref_a")?.mountedBodies).toBe(0);
    expect(activitySummaryStore.getState().beginRootFetch("ref_a", 3)).toBe(1);
  });

  test("publishes only the newest overlapping root result", () => {
    resetActivitySummaryStoreForTests();
    const first = activitySummaryStore.getState().beginRootFetch("ref_a", 1);
    activitySummaryStore.getState().failRootFetch("ref_a", first as number);
    const second = activitySummaryStore.getState().beginRootFetch("ref_a", 2);
    activitySummaryStore.getState().publishRootFetch("ref_a", first as number, {
      active: 9,
      failed: 0,
      completed: 0,
      complete: true,
    });
    expect(activitySummaryStore.getState().entries.get("ref_a")?.counts).toBeUndefined();
    activitySummaryStore.getState().publishRootFetch("ref_a", second as number, {
      active: 2,
      failed: 0,
      completed: 0,
      complete: true,
    });
    expect(activitySummaryStore.getState().entries.get("ref_a")?.counts?.active).toBe(2);
  });

  test("publishes a deferred root completion after the body unmounts", async () => {
    resetActivitySummaryStoreForTests();
    let resolve!: (value: unknown) => void;
    const deferred = new Promise<unknown>((done) => {
      resolve = done;
    });
    const request = activitySummaryStore.getState().refreshRoot("ref_a", 1, async () => deferred);
    activitySummaryStore.getState().unmountBody("ref_a");
    resolve({
      revision: 1,
      root: {
        kind: "session",
        sessionId: "sess_a",
        ref: "ref_a",
        label: "A",
        aggregate: "running",
        counts: { active: 4, failed: 0, completed: 0, complete: true },
        entries: [],
        branch: {},
      },
    });
    await deferred;
    await Promise.resolve();
    await Promise.resolve();
    expect(request).toBe(1);
    expect(activitySummaryStore.getState().entries.get("ref_a")?.counts?.active).toBe(4);
  });

  test("publishes root counts to both stores without letting a continuation change the badge", async () => {
    resetActivitySummaryStoreForTests();
    activityPanelStore.getState().resetForTests();
    const root = {
      revision: 1,
      root: {
        sessionId: "sess_a",
        ref: "ref_a",
        label: "A",
        aggregate: "running",
        counts: { active: 4, failed: 0, completed: 1, complete: true },
        entries: [],
        branch: {},
      },
    };
    const request = activitySummaryStore.getState().refreshRoot("ref_a", 1, async () => root);
    await Promise.resolve();
    await Promise.resolve();

    expect(request).toBe(1);
    expect(activitySummaryStore.getState().entries.get("ref_a")?.counts).toEqual(root.root.counts);
    expect(activityPanelStore.getState().entries.get("ref_a")?.load).toMatchObject({
      kind: "ready",
      tree: { root: { counts: root.root.counts } },
    });

    const continuationRequest = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:child" });
    activityPanelStore.getState().publishFetch("ref_a", continuationRequest, {
      kind: "ready",
      tree: {
        revision: 2,
        root: {
          kind: "session",
          ...root.root,
          counts: { active: 99, failed: 0, completed: 0, complete: true },
        },
      },
    });

    expect(activitySummaryStore.getState().entries.get("ref_a")?.counts).toEqual(root.root.counts);
  });

  test("re-fetches the newest bump that arrived while a root fetch was loading", async () => {
    resetActivitySummaryStoreForTests();
    const pendingFetches: Array<(value: unknown) => void> = [];
    const fetch = () =>
      new Promise<unknown>((resolve) => {
        pendingFetches.push(resolve);
      });
    const rootWith = (active: number) => ({
      revision: 1,
      root: {
        kind: "session",
        sessionId: "sess_a",
        ref: "ref_a",
        label: "A",
        aggregate: "running",
        counts: { active, failed: 0, completed: 0, complete: true },
        entries: [],
        branch: {},
      },
    });

    expect(activitySummaryStore.getState().refreshRoot("ref_a", 1, fetch)).toBe(1);
    // Bump 2 arrives while request 1 is still in flight: dropped today.
    expect(activitySummaryStore.getState().refreshRoot("ref_a", 2, fetch)).toBeNull();

    pendingFetches[0]?.(rootWith(4));
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    // Completion of request 1 must re-issue a fetch for the queued bump 2.
    expect(pendingFetches.length).toBe(2);
    pendingFetches[1]?.(rootWith(7));
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(activitySummaryStore.getState().entries.get("ref_a")).toMatchObject({
      loading: false,
      lastFetchedBump: 2,
    });
    expect(activitySummaryStore.getState().entries.get("ref_a")?.counts?.active).toBe(7);
  });

  test("a completion from before eviction cannot publish into a recreated entry", async () => {
    resetActivitySummaryStoreForTests();
    resetWorkspaceStoreForTests();
    const stale = activitySummaryStore.getState().beginRootFetch("ref_a", 1);
    schedulePanelStoreEviction();
    await Promise.resolve();
    expect(activitySummaryStore.getState().entries.has("ref_a")).toBe(false);

    const fresh = activitySummaryStore.getState().beginRootFetch("ref_a", 2);
    expect(fresh).not.toBe(stale);
    activitySummaryStore.getState().publishRootFetch("ref_a", stale as number, {
      active: 9,
      failed: 0,
      completed: 0,
      complete: true,
    });
    expect(activitySummaryStore.getState().entries.get("ref_a")?.counts).toBeUndefined();
    expect(activitySummaryStore.getState().entries.get("ref_a")?.loading).toBe(true);
  });
});
