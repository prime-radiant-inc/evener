import { describe, expect, test } from "vitest";
import { activitySummaryStore, resetActivitySummaryStoreForTests } from "./activitySummary";

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
});
