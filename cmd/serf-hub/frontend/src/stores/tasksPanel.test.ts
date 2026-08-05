import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { schedulePanelStoreEviction } from "./panelStoreEviction";
import { resetTasksPanelStoreForTests, tasksPanelStore } from "./tasksPanel";

const failure = { headline: "Couldn't load tasks", sentence: "Couldn't load tasks: broken pipe" };

describe("tasksPanelStore", () => {
  beforeEach(() => resetTasksPanelStoreForTests());
  afterEach(() => resetTasksPanelStoreForTests());

  test("retains rows when a refresh fails", () => {
    const first = tasksPanelStore.getState().beginFetch("ref_a");
    tasksPanelStore.getState().publishFetch("ref_a", first, { kind: "rows", rows: [] });
    const refresh = tasksPanelStore.getState().beginFetch("ref_a");
    tasksPanelStore.getState().publishFetch("ref_a", refresh, { kind: "failure", failure });
    expect(tasksPanelStore.getState().entries.get("ref_a")).toMatchObject({ rows: [], failure, loading: false });
  });

  test("retains rows when the daemon disappears", () => {
    const first = tasksPanelStore.getState().beginFetch("ref_a");
    tasksPanelStore.getState().publishFetch("ref_a", first, { kind: "rows", rows: [] });
    const refresh = tasksPanelStore.getState().beginFetch("ref_a");
    tasksPanelStore.getState().publishFetch("ref_a", refresh, { kind: "daemon-gone" });
    expect(tasksPanelStore.getState().entries.get("ref_a")).toMatchObject({ rows: [], daemonGone: true });
  });

  test("publishes a completion after the initiating reader is gone", () => {
    const fetchID = tasksPanelStore.getState().beginFetch("ref_a");
    tasksPanelStore.getState().publishFetch("ref_a", fetchID, { kind: "rows", rows: [] });
    expect(tasksPanelStore.getState().entries.get("ref_a")?.rows).toEqual([]);
  });

  test("ignores an older overlapping completion", () => {
    const first = tasksPanelStore.getState().beginFetch("ref_a");
    const second = tasksPanelStore.getState().beginFetch("ref_a");
    tasksPanelStore.getState().publishFetch("ref_a", first, { kind: "rows", rows: [] });
    expect(tasksPanelStore.getState().entries.get("ref_a")?.loading).toBe(true);
    tasksPanelStore.getState().publishFetch("ref_a", second, { kind: "rows", rows: [] });
    expect(tasksPanelStore.getState().entries.get("ref_a")?.loading).toBe(false);
  });

  test("publishes a deferred completion after a reader is replaced", async () => {
    let resolve!: (rows: []) => void;
    const completion = new Promise<[]>((done) => {
      resolve = done;
    });
    const request = tasksPanelStore.getState().beginFetch("ref_a");
    const publish = completion.then((rows) => {
      tasksPanelStore.getState().publishFetch("ref_a", request, { kind: "rows", rows });
    });
    await Promise.resolve();
    resolve([]);
    await publish;
    expect(tasksPanelStore.getState().entries.get("ref_a")?.rows).toEqual([]);
  });

  test("a completion from before eviction cannot publish into a recreated entry", async () => {
    resetWorkspaceStoreForTests();
    const stale = tasksPanelStore.getState().beginFetch("ref_a");
    schedulePanelStoreEviction();
    await Promise.resolve();
    expect(tasksPanelStore.getState().entries.has("ref_a")).toBe(false);

    const fresh = tasksPanelStore.getState().beginFetch("ref_a");
    expect(fresh).not.toBe(stale);
    tasksPanelStore.getState().publishFetch("ref_a", stale, { kind: "rows", rows: [] });
    expect(tasksPanelStore.getState().entries.get("ref_a")).toMatchObject({ rows: null, loading: true });
  });
});
