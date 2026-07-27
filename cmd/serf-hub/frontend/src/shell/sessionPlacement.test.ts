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
  // An owner-promotion implementation that moves only the child, or promotes
  // the child itself, leaves the unrelated main pane in place and violates
  // the route's owner/main invariant.
  test("promotes local:owner to main, keeps local:child secondary and focused, and removes unrelated main", () => {
    const workspace = workspaceStore.getState();

    const unrelated = workspace.openPane("session", { ref: "local:unrelated" });
    const owner = workspace.openPane("session", { ref: "local:owner" });
    const child = workspace.openPane("session", { ref: "local:child" });

    expect(workspaceStore.getState().focusedPaneId).toBe(child);
    expect(workspaceStore.getState().mainPane()?.id).toBe(unrelated);

    openNestedSessionWithOwner("local:child", "local:owner");

    const panes = workspaceStore.getState().panes;
    const ownerMain = panes.find((pane) => sessionRefOf(pane) === "local:owner");
    const ownerSecondary = panes.find((pane) => sessionRefOf(pane) === "local:owner" && pane.slot === "secondary");
    const childPane = panes.find((pane) => sessionRefOf(pane) === "local:child");
    const unrelatedPane = panes.find((pane) => sessionRefOf(pane) === "local:unrelated");

    expect(panes.filter((pane) => sessionRefOf(pane) === "local:owner")).toHaveLength(1);
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
