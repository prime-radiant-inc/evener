// @vitest-environment node

import type { DockviewApi } from "dockview-core";
import { lazy } from "react";
import { afterAll, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { type PaneDescriptor, type PaneProps, registerPaneForTests } from "./paneRegistry";
import {
  cancelPaneFocus,
  consumePaneFocus,
  isPaneOpen,
  type OpenPaneRecord,
  registerDockviewApi,
  requestPaneFocus,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "./workspace";

// Fixture pane types, registered once for the whole file. "settings" is the
// file's singleton fixture, "doc" its non-singleton fixture - both are real
// PaneTypeId values (the union is locked/closed, so fixtures must be drawn
// from it), but neither is the REAL settings/doc pane (those don't exist
// yet); "transcript" is deliberately left UNREGISTERED, as this file's
// stand-in for "a syntactically valid PaneTypeId that isn't actually
// registered" (restoreLayout's own-registration-check tests below).
function fixtureDescriptor<P>(
  id: PaneDescriptor<P>["id"],
  overrides: Partial<PaneDescriptor<P>> = {},
): PaneDescriptor<P> {
  return {
    id,
    title: () => `title for ${id}`,
    component: lazy(() => new Promise<{ default: React.ComponentType<PaneProps<P>> }>(() => {})),
    ...overrides,
  };
}

// paneRegistry.ts is a shared module singleton, not fresh per test file -
// the afterAll below restores whatever each of these ids resolved to before
// this file ran, so a later file sharing the same module registry never
// inherits these never-resolving fixtures in place of the real panes.
const restorePaneFixtures: Array<() => void> = [];

beforeAll(() => {
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("settings", { singleton: true })));
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("doc")));
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("session")));
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("spawn", { singleton: true })));
  // "welcome" is a real PaneTypeId and the main slot's empty state, so the
  // placement tests below open one. openPane has ALWAYS resolved its type
  // through paneFor (that is where an unregistered type throws), so this is a
  // missing fixture registration, not the store reaching somewhere new.
  restorePaneFixtures.push(registerPaneForTests(fixtureDescriptor("welcome", { singleton: true })));
});

afterAll(() => {
  for (const restore of restorePaneFixtures) restore();
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

describe("openPane", () => {
  test("opens a new pane, adding it to panes with a fresh id", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a", path: "x" });
    const panes = workspaceStore.getState().panes;
    expect(panes).toEqual([{ id, type: "doc", params: { ref: "a", path: "x" }, slot: "main" }]);
  });

  test("defaults params to {} when omitted", () => {
    const id = workspaceStore.getState().openPane("doc");
    expect(workspaceStore.getState().panes.find((p) => p.id === id)?.params).toEqual({});
  });

  test("focuses the newly-opened pane", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a" });
    expect(workspaceStore.getState().focusedPaneId).toBe(id);
  });

  test("keepExistingFocus leaves focus alone when the pane is already open", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    const second = workspaceStore.getState().openPane("doc", { ref: "b" });
    expect(workspaceStore.getState().focusedPaneId).toBe(second);

    const again = workspaceStore.getState().openPane("doc", { ref: "a" }, { keepExistingFocus: true });

    expect(again).toBe(first); // still resolves to (and returns) the existing pane
    expect(workspaceStore.getState().focusedPaneId).toBe(second); // focus untouched
  });

  test("keepExistingFocus still focuses a pane it had to create", () => {
    workspaceStore.getState().openPane("doc", { ref: "a" });
    const fresh = workspaceStore.getState().openPane("doc", { ref: "b" }, { keepExistingFocus: true });
    expect(workspaceStore.getState().focusedPaneId).toBe(fresh);
  });

  test("keepExistingFocus still updates an already-open singleton's params without focusing it", () => {
    workspaceStore.getState().openPane("settings", { section: "appearance" });
    const other = workspaceStore.getState().openPane("doc", { ref: "a" });

    const settings = workspaceStore
      .getState()
      .openPane("settings", { section: "credentials" }, { keepExistingFocus: true });

    expect(workspaceStore.getState().panes.find((p) => p.id === settings)?.params).toEqual({ section: "credentials" });
    expect(workspaceStore.getState().focusedPaneId).toBe(other);
  });

  // The one placement rule (openPane's own, not any caller's): the main slot
  // holds exactly one pane; every later open stacks in the secondary group.
  test("the first pane takes the main slot", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    expect(workspaceStore.getState().mainPane()?.id).toBe(first);
  });

  // Welcome is the main slot's empty state, not a peer: it exists to say
  // "nothing is open here". The first real pane therefore takes the main slot
  // FROM it rather than opening beside it - otherwise navigating from "/" to a
  // session leaves half the workspace showing "No session open" while the
  // session itself sits in the secondary group (observed in a real browser).
  test("the first real pane displaces a welcome placeholder from the main slot", () => {
    const welcome = workspaceStore.getState().openPane("welcome");
    const session = workspaceStore.getState().openPane("doc", { ref: "a" });

    expect(workspaceStore.getState().mainPane()?.id).toBe(session);
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual([session]);
    expect(workspaceStore.getState().panes.map((p) => p.id)).not.toContain(welcome);
  });

  test("welcome is NOT displaced once a real pane already holds the main slot", () => {
    const main = workspaceStore.getState().openPane("doc", { ref: "a" });
    const welcome = workspaceStore.getState().openPane("welcome"); // secondary, oddly, but the user asked for it

    expect(workspaceStore.getState().mainPane()?.id).toBe(main);
    expect(workspaceStore.getState().panes.map((p) => p.id)).toContain(welcome);
  });

  test("every pane after the first goes to the secondary slot", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    const second = workspaceStore.getState().openPane("doc", { ref: "b" });
    const third = workspaceStore.getState().openPane("doc", { ref: "c" });

    expect(workspaceStore.getState().panes.map((p) => p.slot)).toEqual(["main", "secondary", "secondary"]);
    expect(workspaceStore.getState().mainPane()?.id).toBe(first);
    expect([second, third]).not.toContain(workspaceStore.getState().mainPane()?.id);
  });

  // The one exception to "main takes whatever comes first": a caller that
  // knows its pane must never be the primary one. The store sees only a type
  // and a {ref}, so it cannot tell a subagent from a top-level session - the
  // rail can, and says so here. See docs/web-ui/specs/
  // 2026-07-26-subagent-opens-beside-main.md §A.
  describe("slot: 'secondary' preference", () => {
    test("keeps a pane out of the main slot even when main is empty", () => {
      const beside = workspaceStore.getState().openPane("doc", { ref: "a" }, { slot: "secondary" });
      expect(workspaceStore.getState().mainPane()).toBeNull();
      expect(workspaceStore.getState().panes.find((p) => p.id === beside)?.slot).toBe("secondary");
    });

    // Welcome is displaced by a pane that TAKES main. A pane that refuses main
    // has nothing to displace, so the placeholder must survive - otherwise the
    // workspace is left with a secondary pane and an empty left half.
    test("does not displace a welcome placeholder", () => {
      const welcome = workspaceStore.getState().openPane("welcome");
      workspaceStore.getState().openPane("doc", { ref: "a" }, { slot: "secondary" });
      expect(workspaceStore.getState().mainPane()?.id).toBe(welcome);
    });

    test("still goes secondary when main is already occupied", () => {
      const main = workspaceStore.getState().openPane("doc", { ref: "a" });
      const beside = workspaceStore.getState().openPane("doc", { ref: "b" }, { slot: "secondary" });
      expect(workspaceStore.getState().mainPane()?.id).toBe(main);
      expect(workspaceStore.getState().panes.find((p) => p.id === beside)?.slot).toBe("secondary");
    });

    test("omitting it leaves the default placement exactly as it was", () => {
      const first = workspaceStore.getState().openPane("doc", { ref: "a" });
      const second = workspaceStore.getState().openPane("doc", { ref: "b" });
      expect(workspaceStore.getState().panes.map((p) => p.slot)).toEqual(["main", "secondary"]);
      expect(workspaceStore.getState().mainPane()?.id).toBe(first);
      expect(second).not.toBe(first);
    });

    // Reopening an already-open pane resolves to that pane; the preference is
    // a placement rule for a pane being CREATED, and slot is assign-once.
    test("does not move a pane that is already open", () => {
      const main = workspaceStore.getState().openPane("doc", { ref: "a" });
      const again = workspaceStore.getState().openPane("doc", { ref: "a" }, { slot: "secondary" });
      expect(again).toBe(main);
      expect(workspaceStore.getState().mainPane()?.id).toBe(main);
    });
  });

  test("closing the main pane empties the main slot, and the next open refills it", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().openPane("doc", { ref: "b" }); // secondary

    workspaceStore.getState().closePane(first);

    expect(workspaceStore.getState().mainPane()).toBeNull(); // no promotion from the right
    const relaunched = workspaceStore.getState().openPane("welcome");
    expect(workspaceStore.getState().mainPane()?.id).toBe(relaunched);
  });

  test("mainPane is null for an empty workspace", () => {
    expect(workspaceStore.getState().mainPane()).toBeNull();
  });

  test("two non-singleton panes with different params are both opened", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    const second = workspaceStore.getState().openPane("doc", { ref: "b" });
    expect(first).not.toBe(second);
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual([first, second]);
  });

  test("reopening a non-singleton pane with identical params focuses the existing one instead of duplicating", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().openPane("doc", { ref: "b" }); // a second, different pane
    const again = workspaceStore.getState().openPane("doc", { ref: "a" });

    expect(again).toBe(first);
    expect(workspaceStore.getState().panes).toHaveLength(2);
    expect(workspaceStore.getState().focusedPaneId).toBe(first);
  });

  test("reopening a singleton pane type focuses the existing instance instead of creating a second", () => {
    const first = workspaceStore.getState().openPane("settings", { section: "appearance" });
    const again = workspaceStore.getState().openPane("settings", { section: "appearance" });

    expect(again).toBe(first);
    expect(workspaceStore.getState().panes).toHaveLength(1);
  });

  test("reopening a singleton pane type with different params updates the existing pane's params in place", () => {
    const first = workspaceStore.getState().openPane("settings", { section: "appearance" });
    const again = workspaceStore.getState().openPane("settings", { section: "credentials" });

    expect(again).toBe(first);
    expect(workspaceStore.getState().panes).toEqual([
      { id: first, type: "settings", params: { section: "credentials" }, slot: "main" },
    ]);
    expect(workspaceStore.getState().focusedPaneId).toBe(first);
  });

  test("throws for an unregistered pane type (mirrors paneFor's own contract)", () => {
    expect(() => workspaceStore.getState().openPane("transcript", {})).toThrow(/transcript/);
  });
});

describe("togglePane and pending focus", () => {
  test("opens secondary panes, closes them, and distinguishes refs", () => {
    const first = workspaceStore.getState().togglePane("session", { ref: "ref_a" });
    expect(first.opened).toBe(true);
    expect(workspaceStore.getState().panes).toMatchObject([{ type: "session", slot: "secondary" }]);
    expect(workspaceStore.getState().focusedPaneId).toBe(first.paneId);
    expect(workspaceStore.getState().togglePane("session", { ref: "ref_a" })).toEqual({
      paneId: first.paneId,
      opened: false,
    });
    expect(workspaceStore.getState().focusedPaneId).toBeNull();

    const third = workspaceStore.getState().togglePane("session", { ref: "ref_a" });
    expect(third.paneId).not.toBe(first.paneId);
    expect(workspaceStore.getState().panes).toMatchObject([{ type: "session", slot: "secondary" }]);
    expect(isPaneOpen(workspaceStore.getState(), "session", { ref: "ref_a" })).toBe(true);
    expect(isPaneOpen(workspaceStore.getState(), "session", { ref: "ref_b" })).toBe(false);
  });

  test("consumes pending focus once and supports cancellation", () => {
    requestPaneFocus("pane_session_1");
    expect(consumePaneFocus("pane_session_1")).toBe(true);
    expect(consumePaneFocus("pane_session_1")).toBe(false);

    requestPaneFocus("pane_session_2");
    cancelPaneFocus("pane_session_2");
    expect(consumePaneFocus("pane_session_2")).toBe(false);

    const id = workspaceStore.getState().openPane("session", { ref: "ordinary" });
    expect(consumePaneFocus(id)).toBe(false);
  });
});

describe("replacePrimary", () => {
  // A replacement that only opens the requested pane leaves old secondary
  // panes behind; this literal workspace is the contract that catches that
  // additive-placement break.
  test("replaces main and clears every secondary pane when the primary identity changes", () => {
    const workspace = workspaceStore.getState();
    workspace.openPane("doc", { ref: "secondary-a" });
    workspace.openPane("doc", { ref: "secondary-b" });

    const sessionId = workspace.replacePrimary("session", { ref: "local:session-b" });

    expect(workspaceStore.getState().panes).toEqual([
      { id: sessionId, type: "session", params: { ref: "local:session-b" }, slot: "main" },
    ]);
    expect(workspaceStore.getState().focusedPaneId).toBe(sessionId);
  });

  // A replacement that always rebuilds the workspace clears useful secondary
  // panes even when the requested primary is already the current identity.
  test("preserves secondary panes when the requested primary identity is already main", () => {
    const workspace = workspaceStore.getState();
    const mainId = workspace.replacePrimary("session", { ref: "local:session-a" });
    const secondaryId = workspace.openPane("doc", { ref: "secondary" });

    const repeatedId = workspace.replacePrimary("session", { ref: "local:session-a" });

    expect(repeatedId).toBe(mainId);
    expect(workspaceStore.getState().panes).toEqual([
      { id: mainId, type: "session", params: { ref: "local:session-a" }, slot: "main" },
      { id: secondaryId, type: "doc", params: { ref: "secondary" }, slot: "secondary" },
    ]);
  });

  // Settings is a singleton whose section changes without changing its
  // primary identity; a replacement that closes/reopens it loses the pane id
  // and its secondary neighbors instead of updating the existing main pane.
  test("updates a singleton settings section in place while preserving secondary panes", () => {
    const workspace = workspaceStore.getState();
    const settingsId = workspace.replacePrimary("settings", { section: "general" });
    const secondaryId = workspace.openPane("doc", { ref: "secondary" });

    const updatedId = workspace.replacePrimary("settings", { section: "credentials" });

    expect(updatedId).toBe(settingsId);
    expect(workspaceStore.getState().panes).toEqual([
      { id: settingsId, type: "settings", params: { section: "credentials" }, slot: "main" },
      { id: secondaryId, type: "doc", params: { ref: "secondary" }, slot: "secondary" },
    ]);
  });

  // A session's primary identity is READ FROM the params it is called with,
  // never spelled beside them: with two arguments there is no pair that can
  // disagree, so no caller can ask replacePrimary to keep a live pane while
  // handing it a different ref (kata z44z). That mismatch is what would
  // re-point a mounted pane - same pane id, new ref - and neither host
  // remounts a pane whose id it already has, so the previous session's job
  // list and badge count would stay on screen (katas pcx5/tmyw rest on this).
  // Written in the two-argument form on purpose: re-introducing a separate
  // identity argument breaks the typecheck here.
  test("a session's primary identity is the ref in its own params", () => {
    const workspace = workspaceStore.getState();
    const mainId = workspace.replacePrimary("session", { ref: "local:session-a" });

    expect(workspace.replacePrimary("session", { ref: "local:session-a" })).toBe(mainId);

    const switched = workspace.replacePrimary("session", { ref: "local:session-b" });
    expect(switched).not.toBe(mainId);
    expect(workspaceStore.getState().mainPane()?.params).toEqual({ ref: "local:session-b" });
  });

  test("notifies subscribers once for a primary replacement", () => {
    const snapshots: Array<ReadonlyArray<OpenPaneRecord>> = [];
    const unsubscribe = workspaceStore.subscribe((state) => snapshots.push(state.panes));

    workspaceStore.getState().replacePrimary("spawn", {});

    unsubscribe();
    expect(snapshots).toHaveLength(1);
    expect(snapshots[0]).toEqual(workspaceStore.getState().panes);
  });
});

describe("closePane", () => {
  test("removes the pane from panes", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().closePane(id);
    expect(workspaceStore.getState().panes).toEqual([]);
  });

  test("clears focusedPaneId when closing the focused pane", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().closePane(id);
    expect(workspaceStore.getState().focusedPaneId).toBeNull();
  });

  test("leaves focusedPaneId untouched when closing a different, non-focused pane", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    const second = workspaceStore.getState().openPane("doc", { ref: "b" }); // now focused
    workspaceStore.getState().closePane(first);
    expect(workspaceStore.getState().focusedPaneId).toBe(second);
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual([second]);
  });

  test("is a no-op for an unknown paneId", () => {
    const id = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().closePane("not-a-real-id");
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual([id]);
    expect(workspaceStore.getState().focusedPaneId).toBe(id);
  });
});

describe("focusPane", () => {
  test("sets focusedPaneId to an existing pane's id", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().openPane("doc", { ref: "b" }); // focus moves here
    workspaceStore.getState().focusPane(first);
    expect(workspaceStore.getState().focusedPaneId).toBe(first);
  });

  test("is a no-op for an unknown paneId", () => {
    const first = workspaceStore.getState().openPane("doc", { ref: "a" });
    workspaceStore.getState().focusPane("not-a-real-id");
    expect(workspaceStore.getState().focusedPaneId).toBe(first);
  });
});

// A minimal DockviewApi double covering only what workspace.ts itself
// touches (toJSON/fromJSON/panels/activePanel/clear). This file tests
// workspace.ts's OWN logic - dedup/focus bookkeeping and how it delegates
// persistence - in isolation from the real dockview library; DockHost's own
// tests (DockHost.test.tsx) exercise real dockview end to end, per this
// task's testing guidance ("mock only what jsdom can't do" is about THAT
// boundary, not this one - a fake api double here is an ordinary unit-test
// seam for a store that would otherwise need a full dockview mount just to
// test a dedup guard).
class FakeDockviewApi {
  panels: Array<{ id: string; params: unknown }> = [];
  activePanel: { id: string } | undefined = undefined;
  cleared = false;
  toJSONResult: unknown = { fake: "layout" };
  fromJSONBehavior: (data: unknown) => void = () => {};

  toJSON(): unknown {
    return this.toJSONResult;
  }

  fromJSON(data: unknown): void {
    this.fromJSONBehavior(data);
  }

  clear(): void {
    this.cleared = true;
    this.panels = [];
    this.activePanel = undefined;
  }
}

function asDockviewApi(fake: FakeDockviewApi): DockviewApi {
  return fake as unknown as DockviewApi;
}

describe("layoutJSON / restoreLayout (against a fake DockviewApi)", () => {
  test("layoutJSON returns null when no DockviewApi is registered", () => {
    expect(workspaceStore.getState().layoutJSON()).toBeNull();
  });

  test("restoreLayout returns false when no DockviewApi is registered", () => {
    expect(workspaceStore.getState().restoreLayout({ anything: true })).toBe(false);
  });

  test("layoutJSON delegates to the registered api's toJSON()", () => {
    const fake = new FakeDockviewApi();
    fake.toJSONResult = { grid: "serialized" };
    registerDockviewApi(asDockviewApi(fake));

    expect(workspaceStore.getState().layoutJSON()).toEqual({ grid: "serialized" });
  });

  test("registering null clears the api (layoutJSON goes back to null)", () => {
    const fake = new FakeDockviewApi();
    registerDockviewApi(asDockviewApi(fake));
    registerDockviewApi(null);

    expect(workspaceStore.getState().layoutJSON()).toBeNull();
  });

  test("restoreLayout calls fromJSON and, on success, rebuilds panes/focusedPaneId from the api's panels/activePanel", () => {
    const fake = new FakeDockviewApi();
    let receivedData: unknown;
    fake.fromJSONBehavior = (data) => {
      receivedData = data;
      fake.panels = [
        { id: "p1", params: { paneType: "settings", paneParams: { section: "appearance" } } },
        { id: "p2", params: { paneType: "doc", paneParams: { ref: "a", path: "x" } } },
      ];
      fake.activePanel = { id: "p2" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({ the: "json" });

    expect(ok).toBe(true);
    expect(receivedData).toEqual({ the: "json" });
    expect(workspaceStore.getState().panes).toEqual([
      // Slots are re-derived from the restored grid order: the first panel is
      // the top-left (main) one, everything after it is in the group to its right.
      { id: "p1", type: "settings", params: { section: "appearance" }, slot: "main" },
      { id: "p2", type: "doc", params: { ref: "a", path: "x" }, slot: "secondary" },
    ]);
    expect(workspaceStore.getState().focusedPaneId).toBe("p2");
  });

  test("restoreLayout falls back to the first panel as focused when the api reports no active panel", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [{ id: "p1", params: { paneType: "doc", paneParams: {} } }];
      fake.activePanel = undefined;
    };
    registerDockviewApi(asDockviewApi(fake));

    workspaceStore.getState().restoreLayout({});

    expect(workspaceStore.getState().focusedPaneId).toBe("p1");
  });

  test("restoreLayout sets focusedPaneId to null when the restored layout has no panels at all", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [];
      fake.activePanel = undefined;
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({});

    expect(ok).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([]);
    expect(workspaceStore.getState().focusedPaneId).toBeNull();
  });

  test("restoreLayout returns false and clears the api when fromJSON throws (structurally invalid data)", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      throw new Error("dockview: root must be of type branch");
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({ nonsense: true });

    expect(ok).toBe(false);
    expect(fake.cleared).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([]);
    expect(workspaceStore.getState().focusedPaneId).toBeNull();
  });

  test("restoreLayout returns false and clears the api when a restored panel's paneType isn't a real PaneTypeId", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [{ id: "p1", params: { paneType: "not-a-real-type", paneParams: {} } }];
      fake.activePanel = { id: "p1" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({});

    expect(ok).toBe(false);
    expect(fake.cleared).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([]);
  });

  test("restoreLayout returns false and clears the api when a restored panel's paneType is a valid PaneTypeId but isn't registered", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      // "transcript" is a real PaneTypeId (see the file header) but this
      // test file never registers it - simulates a layout saved by a later
      // build (once transcript panes ship) loaded by an older one that
      // hasn't shipped them yet.
      fake.panels = [{ id: "p1", params: { paneType: "transcript", paneParams: { ref: "a" } } }];
      fake.activePanel = { id: "p1" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({});

    expect(ok).toBe(false);
    expect(fake.cleared).toBe(true);
  });

  test("restoreLayout returns false when a restored panel has no params at all", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [{ id: "p1", params: undefined }];
      fake.activePanel = { id: "p1" };
    };
    registerDockviewApi(asDockviewApi(fake));

    expect(workspaceStore.getState().restoreLayout({})).toBe(false);
    expect(fake.cleared).toBe(true);
  });

  // Restored ids come from a PREVIOUS page load's own independently-
  // numbered counter (nextPaneSeq resets to 0 on every fresh load - see
  // openPane's own "fresh id" tests above) - a freshly-minted id after a
  // restore can otherwise collide with one just restored, silently
  // duplicating an id in `panes` (see workspace.ts's bumpPastRestoredIds
  // for the full failure mode this guards against, live-reproduced via
  // DockHost.test.tsx's own save/restore/re-open round trip).
  test("restoreLayout bumps the pane-id counter past every restored id, so the next openPane() can't collide with one", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [
        { id: "pane_doc_1", params: { paneType: "doc", paneParams: { ref: "a" } } },
        { id: "pane_doc_5", params: { paneType: "doc", paneParams: { ref: "b" } } },
      ];
      fake.activePanel = { id: "pane_doc_5" };
    };
    registerDockviewApi(asDockviewApi(fake));

    workspaceStore.getState().restoreLayout({});
    const freshId = workspaceStore.getState().openPane("doc", { ref: "fresh" });

    expect(freshId).not.toBe("pane_doc_1");
    expect(freshId).not.toBe("pane_doc_5");
    expect(workspaceStore.getState().panes.map((p) => p.id)).toEqual(["pane_doc_1", "pane_doc_5", freshId]);
  });

  test("restoreLayout leaves the counter untouched when the restored layout has no panels at all", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [];
      fake.activePanel = undefined;
    };
    registerDockviewApi(asDockviewApi(fake));

    workspaceStore.getState().restoreLayout({});
    const freshId = workspaceStore.getState().openPane("doc", { ref: "fresh" });

    expect(freshId).toBe("pane_doc_1"); // the ordinary first-ever id, nothing to bump past
  });
});
