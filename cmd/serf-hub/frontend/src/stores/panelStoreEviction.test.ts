import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { activityPanelStore } from "./activityPanel";
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
