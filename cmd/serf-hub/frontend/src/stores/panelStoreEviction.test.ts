import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { activityPanelStore } from "./activityPanel";
import { activitySummaryStore } from "./activitySummary";
import { resetPanelStoreEvictionForTests } from "./panelStoreEviction";
import { tasksPanelStore } from "./tasksPanel";

async function settle(): Promise<void> {
  await Promise.resolve();
}

describe("panel store eviction", () => {
  beforeEach(() => {
    resetWorkspaceStoreForTests();
    tasksPanelStore.getState().resetForTests();
    activityPanelStore.getState().resetForTests();
    resetPanelStoreEvictionForTests();
  });
  afterEach(() => {
    resetWorkspaceStoreForTests();
    tasksPanelStore.getState().resetForTests();
    activityPanelStore.getState().resetForTests();
  });

  test("retains entries while a session pane references the ref", async () => {
    tasksPanelStore.getState().setRows("ref_a", []);
    workspaceStore.setState({
      panes: [{ id: "session_a", type: "session", params: { ref: "ref_a" }, slot: "main" }],
      focusedPaneId: "session_a",
    });
    await settle();
    expect(tasksPanelStore.getState().entries.has("ref_a")).toBe(true);
  });

  test("retains Activity and summary entries while a panel references the ref", async () => {
    const request = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", request, {
      kind: "ready",
      tree: {
        revision: 1,
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
      },
    });
    const summaryRequest = activitySummaryStore.getState().beginRootFetch("ref_a", 1);
    activitySummaryStore.getState().publishRootFetch("ref_a", summaryRequest as number, {
      active: 1,
      failed: 0,
      completed: 0,
      complete: true,
    });
    workspaceStore.setState({
      panes: [{ id: "activity_a", type: "sessionActivity", params: { ref: "ref_a" }, slot: "secondary" }],
      focusedPaneId: "activity_a",
    });
    await settle();
    expect(activityPanelStore.getState().entries.has("ref_a")).toBe(true);
    expect(activitySummaryStore.getState().entries.has("ref_a")).toBe(true);
  });

  test("evicts Activity and summary entries after their last pane closes", async () => {
    const request = activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", request, {
      kind: "ready",
      tree: {
        revision: 1,
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
      },
    });
    const summaryRequest = activitySummaryStore.getState().beginRootFetch("ref_a", 1);
    activitySummaryStore.getState().publishRootFetch("ref_a", summaryRequest as number, {
      active: 1,
      failed: 0,
      completed: 0,
      complete: true,
    });
    workspaceStore.setState({
      panes: [{ id: "activity_a", type: "sessionActivity", params: { ref: "ref_a" }, slot: "secondary" }],
      focusedPaneId: "activity_a",
    });
    workspaceStore.getState().closePane("activity_a");
    await settle();
    expect(activityPanelStore.getState().entries.has("ref_a")).toBe(false);
    expect(activitySummaryStore.getState().entries.has("ref_a")).toBe(false);
  });

  test("retains an open backgrounded panel without a thread claim", async () => {
    tasksPanelStore.getState().setRows("ref_a", []);
    workspaceStore.setState({
      panes: [{ id: "tasks_a", type: "sessionTasks", params: { ref: "ref_a" }, slot: "secondary" }],
      focusedPaneId: "tasks_a",
    });
    await settle();
    expect(tasksPanelStore.getState().entries.has("ref_a")).toBe(true);
  });

  test("evicts after the last pane closes", async () => {
    tasksPanelStore.getState().setRows("ref_a", []);
    workspaceStore.setState({
      panes: [
        { id: "session_a", type: "session", params: { ref: "ref_a" }, slot: "main" },
        { id: "tasks_a", type: "sessionTasks", params: { ref: "ref_a" }, slot: "secondary" },
      ],
      focusedPaneId: "session_a",
    });
    workspaceStore.getState().closePane("session_a");
    await settle();
    expect(tasksPanelStore.getState().entries.has("ref_a")).toBe(true);
    workspaceStore.getState().closePane("tasks_a");
    await settle();
    expect(tasksPanelStore.getState().entries.has("ref_a")).toBe(false);
  });

  test("does not evict during a synchronous restore sequence", async () => {
    tasksPanelStore.getState().setRows("ref_a", []);
    const pane = { id: "session_a", type: "session" as const, params: { ref: "ref_a" }, slot: "main" as const };
    workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
    workspaceStore.setState({ panes: [], focusedPaneId: null });
    workspaceStore.setState({ panes: [{ ...pane, id: "restored" }], focusedPaneId: "restored" });
    await settle();
    expect(tasksPanelStore.getState().entries.has("ref_a")).toBe(true);
  });
});
