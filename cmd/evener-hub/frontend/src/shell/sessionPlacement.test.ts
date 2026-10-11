import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { openNestedSessionWithOwner, openSessionByRef, openTopLevelSession } from "./sessionPlacement";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

beforeAll(async () => {
  await import("../panes/session");
  await import("../panes/transcript");
  await import("../panes/zoom");
  await import("../panes/sessionPanels");
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

afterEach(() => {
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

test("session links request live routing instead of reusing read-only transcripts", () => {
  window.history.replaceState({}, "", "/s/local%3Aroot");
  openTopLevelSession("local:root");
  const root = workspaceStore.getState().mainPane();
  const child = workspaceStore
    .getState()
    .openPane("transcript", { ref: "local:child", parentRef: "local:root" }, { slot: "secondary" });
  workspaceStore
    .getState()
    .openPane("transcript", { ref: "local:grandchild", parentRef: "local:child" }, { slot: "secondary" });
  openSessionByRef("local:child");
  expect(workspaceStore.getState().focusedPaneId).not.toBe(child);
  expect(window.location.pathname).toBe("/s/local%3Achild");
  openSessionByRef("local:root");
  expect(workspaceStore.getState().focusedPaneId).toBe(root?.id);
  expect(workspaceStore.getState().panes).toHaveLength(3);
});

test("an unopened parent still requests normal routing without inventing placement", () => {
  window.history.replaceState({}, "", "/s/local%3Aroot");
  openTopLevelSession("local:root");
  const child = workspaceStore
    .getState()
    .openPane("transcript", { ref: "local:child", parentRef: "local:unloaded" }, { slot: "secondary" });
  openSessionByRef("local:unloaded");
  expect(window.location.pathname).toBe("/s/local%3Aunloaded");
  expect(workspaceStore.getState().focusedPaneId).toBe(child);
  expect(workspaceStore.getState().panes).toHaveLength(2);
});

// The main pane zoomed on a session is a sessionZoom record in the SAME slot:
// panes/zoom/actions.ts retypes the source pane in place, so this is the
// exact record shape a saved layout restores (and the guard scenario that
// reloads one).
const cascadeParams = (sourceRef: string) => ({
  ref: "local:zoom-leaf",
  source: { type: "session", params: { ref: sourceRef } },
  edges: [{ ownerRef: sourceRef, childRef: "local:zoom-leaf", delegateId: "delegate-zoom" }],
});

function promoteMainToCascade(sourceRef: string): string {
  // The first pane openPane places takes the main slot, so this is the plain
  // session pane the zoom system retypes in place.
  workspaceStore.getState().openPane("session", { ref: sourceRef });
  const main = workspaceStore.getState().mainPane();
  if (!main) throw new Error("no main pane to promote");
  if (!workspaceStore.getState().retypePane(main, "sessionZoom", cascadeParams(sourceRef))) {
    throw new Error("cascade promotion failed");
  }
  return main.id;
}

describe("openTopLevelSession against a cascade main", () => {
  test("opening the session the cascade zooms on preserves the cascade and its neighbors", () => {
    const cascadeId = promoteMainToCascade("local:root");
    workspaceStore.getState().openPane("sessionTasks", { ref: "local:root" }, { slot: "secondary" });
    // A plain session pane for the same ref is the duplicate replacePrimary's
    // matching arm would have removed; the cascade owns the main slot.
    workspaceStore.getState().openPane("session", { ref: "local:root" }, { slot: "secondary" });

    openTopLevelSession("local:root");

    const state = workspaceStore.getState();
    expect(state.mainPane()?.id).toBe(cascadeId);
    expect(state.mainPane()?.type).toBe("sessionZoom");
    expect(state.panes.filter((pane) => pane.type === "session" && sessionRefOf(pane) === "local:root")).toHaveLength(
      0,
    );
    expect(state.panes.some((pane) => pane.type === "sessionTasks")).toBe(true);
    // Closing the focused duplicate nulls focus; the preserved placement must
    // not steal focus the way replacePrimary's arm always does - the saved
    // focus is part of the restored work.
    expect(state.focusedPaneId).toBeNull();
  });

  test("a nested route keeps a cascade main on the owner and opens the child beside it", () => {
    const cascadeId = promoteMainToCascade("local:owner");

    openNestedSessionWithOwner("local:child", "local:owner");

    const state = workspaceStore.getState();
    expect(state.mainPane()?.id).toBe(cascadeId);
    expect(state.mainPane()?.type).toBe("sessionZoom");
    const child = state.panes.find((pane) => pane.type === "session" && sessionRefOf(pane) === "local:child");
    expect(child?.slot).toBe("secondary");
    expect(state.focusedPaneId).toBe(child?.id);
  });

  test("opening a different session still replaces a cascade main", () => {
    promoteMainToCascade("local:root");

    openTopLevelSession("local:other");

    const state = workspaceStore.getState();
    expect(state.panes).toHaveLength(1);
    expect(state.mainPane()).toMatchObject({ type: "session", params: { ref: "local:other" } });
  });
});

test.each([false, true])(
  "a new inspection main cannot replace the real route editor with nested route=%s",
  (nested) => {
    const id = promoteMainToCascade("local:root");
    const main = workspaceStore.getState().mainPane();
    if (!main) throw new Error("Missing inspection main fixture");
    workspaceStore
      .getState()
      .retypePane(main, "sessionZoom", { ...cascadeParams("local:root"), inspection: { origin: null } });
    workspaceStore.getState().openPane("session", { ref: "local:root" }, { slot: "secondary" });
    if (nested) openNestedSessionWithOwner("local:child", "local:root");
    else openTopLevelSession("local:root");
    const state = workspaceStore.getState();
    expect(state.mainPane()).toMatchObject({ type: "session", slot: "main", params: { ref: "local:root" } });
    expect(state.panes.some((pane) => pane.id === id)).toBe(false);
    expect(state.panes.filter((pane) => pane.type === "session" && sessionRefOf(pane) === "local:root")).toHaveLength(
      1,
    );
    if (nested) {
      const child = state.panes.find((pane) => pane.type === "session" && sessionRefOf(pane) === "local:child");
      expect(child?.slot).toBe("secondary");
      expect(state.focusedPaneId).toBe(child?.id);
    } else expect(state.focusedPaneId).toBe(state.mainPane()?.id);
  },
);
