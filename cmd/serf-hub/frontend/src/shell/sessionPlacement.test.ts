import { beforeAll, beforeEach, describe, expect, test } from "vitest";
import { openNestedSessionWithOwner } from "./sessionPlacement";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

beforeAll(async () => {
  await import("../panes/session");
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

function sessionRefOf(pane: { params: unknown }): string | null {
  const params = pane.params as { ref?: unknown };
  return typeof params?.ref === "string" ? params.ref : null;
}

describe("openNestedSessionWithOwner", () => {
  test("promotes a secondary owner to main and keeps nested child focused", () => {
    const workspace = workspaceStore.getState();

    const unrelated = workspace.openPane("session", { ref: "unrelated" });
    const owner = workspace.openPane("session", { ref: "owner" });
    const child = workspace.openPane("session", { ref: "child" });

    expect(workspaceStore.getState().focusedPaneId).toBe(child);
    expect(workspaceStore.getState().mainPane()?.id).toBe(unrelated);

    openNestedSessionWithOwner("child", "owner");

    const panes = workspaceStore.getState().panes;
    const ownerMain = panes.find((pane) => sessionRefOf(pane) === "owner");
    const ownerSecondary = panes.find((pane) => sessionRefOf(pane) === "owner" && pane.slot === "secondary");
    const childPane = panes.find((pane) => sessionRefOf(pane) === "child");
    const unrelatedPane = panes.find((pane) => sessionRefOf(pane) === "unrelated");

    expect(panes.filter((pane) => sessionRefOf(pane) === "owner")).toHaveLength(1);
    expect(ownerMain).not.toBeUndefined();
    expect(ownerMain?.slot).toBe("main");
    expect(ownerSecondary).toBeUndefined();
    expect(ownerMain?.id).not.toBe(owner);
    expect(childPane?.slot).toBe("secondary");
    expect(childPane?.id).toBe(child);
    expect(workspaceStore.getState().mainPane()?.id).toBe(ownerMain?.id);
    expect(workspaceStore.getState().focusedPaneId).toBe(child);
    expect(unrelatedPane).toBeUndefined();
  });
});
