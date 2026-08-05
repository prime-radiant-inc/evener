import { beforeAll, beforeEach, describe, expect, test } from "vitest";
import { openNestedSessionWithOwner, openTopLevelSession } from "./sessionPlacement";
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

describe("openTopLevelSession", () => {
  // A session pane's ref is fixed for the life of its pane id: switching the
  // main slot to a different session opens a NEW pane and drops the old one,
  // never re-points the open one at another ref. Both hosts instantiate a
  // pane per pane id (mobile keys StackedPane by it; dockview gives each
  // panel its own React tree), so this is what guarantees a session switch
  // REMOUNTS the pane rather than re-rendering it with new params.
  //
  // The session chrome depends on that remount and nothing else: Session
  // passes its ref straight down to SessionChrome, which passes it to
  // ActivityPanel/TasksPanel, none of them keyed - and those panels hold the
  // fetched list, the trigger's badge count, and the "which bump did I last
  // fetch for" marker in component-local state that only a fresh mount
  // clears. A re-pointed pane would leave the previous session's panel state on
  // screen, and its badge count there until the next open (katas pcx5/tmyw:
  // premise checked here, since it is this rule that makes it unreachable).
  //
  // replacePrimary CAN update a main pane's params in place - that is how a
  // singleton settings section changes without losing the pane. A session
  // never takes that path because the store matches a session pane on the ref
  // in the very params that would replace it (kata z44z), so an in-place
  // update cannot change a ref. This pins the consequence at the caller a
  // route actually reaches.
  test("switching to a different ref opens a NEW pane instead of re-pointing the open one", () => {
    openTopLevelSession("local:session-a");
    const first = workspaceStore.getState().mainPane();
    expect(sessionRefOf(first ?? { params: {} })).toBe("local:session-a");

    openTopLevelSession("local:session-b");

    const second = workspaceStore.getState().mainPane();
    expect(sessionRefOf(second ?? { params: {} })).toBe("local:session-b");
    expect(second?.id).not.toBe(first?.id);
    // The old pane is gone entirely, not merely displaced: nothing keeps it
    // mounted anywhere.
    expect(workspaceStore.getState().panes.some((pane) => pane.id === first?.id)).toBe(false);
  });
});

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
    expect(workspaceStore.getState().mainPane()?.id).toBe(ownerMain?.id);
    expect(workspaceStore.getState().focusedPaneId).toBe(childPane?.id);
    expect(unrelatedPane).toBeUndefined();
  });
});
