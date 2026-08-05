import { afterEach, beforeEach, describe, expect, test } from "vitest";
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
});
