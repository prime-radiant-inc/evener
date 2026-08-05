// Boot-time pane registration keeps a persisted layout intact across a reload.
//
// AppShell eagerly registers every pane type that can appear in a restored
// dockview layout. welcome/session/settings/spawn always could; doc and
// transcript are the wave-8 additions - they otherwise register only lazily,
// the first time a producer imports their opener (openDoc.ts / paneActions.ts).
// A layout persisted with one of those panes open would, on the next page
// load, reach DockHost's boot-time restoreLayout() BEFORE any such producer
// ran, find the pane type unregistered, and - because readPanelParams() throws
// on an unregistered type and restoreLayout()'s catch clears the whole api -
// discard the user's ENTIRE saved workspace, not just the one pane
// (workspace.ts:123-129,208-230; the sibling case is workspace.test.ts's
// "valid PaneTypeId but isn't registered" test, which proves the discard).
//
// Importing AppShell here runs exactly those boot-time side-effect
// registrations - the same module evaluation production performs - and the
// FakeDockviewApi (the workspace.test.ts unit double) drives restoreLayout
// against a layout that contains a doc pane AND a transcript pane.
//
// Mutation net: deleting either `import "../panes/doc"` or
// `import "../panes/transcript"` from AppShell.tsx leaves that pane type
// unregistered at boot, so restoreLayout discards the layout - and this test's
// restoreLayout()===true / cleared===false / both-panes-present assertions all
// fail. (Verified both ways during the wave-8 fix round.)
import type { DockviewApi } from "dockview-core";
import { beforeEach, describe, expect, test } from "vitest";
import { paneFor } from "./paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";
import "./AppShell"; // side effect: the boot-time pane-type registrations under test

// A minimal DockviewApi double covering only what workspace.ts touches
// (fromJSON/panels/activePanel/clear) - the same unit-test seam as
// workspace.test.ts, duplicated here per this project's no-shared-test-utils
// convention (see AppShell.test.tsx's own MemoryStorage note on why helpers
// are duplicated rather than shared).
class FakeDockviewApi {
  panels: Array<{ id: string; params: unknown }> = [];
  activePanel: { id: string } | undefined = undefined;
  cleared = false;
  serialized: unknown = { fake: "layout" };
  fromJSONBehavior: (data: unknown) => void = () => {};

  toJSON(): unknown {
    return this.serialized;
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

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

describe("boot-time registration lets a persisted layout with lazy panes restore", () => {
  // doc and transcript are the original wave-8 lazy registrations this file
  // exists to protect (see the header's mutation net); the session panels are
  // the later additions. One fixture carries all of them so dropping ANY
  // boot-time registration from AppShell discards the layout and fails here.
  test("session panels, doc, and transcript all round-trip through layoutJSON and restore with raw-ref titles before hydration", () => {
    const fake = new FakeDockviewApi();
    const saved = {
      grid: { root: { type: "branch", data: [] } },
      panels: {
        p1: { id: "p1", contentComponent: "default", params: { paneType: "session", paneParams: { ref: "ref_a" } } },
        p2: {
          id: "p2",
          contentComponent: "default",
          params: { paneType: "sessionTasks", paneParams: { ref: "ref_a" } },
        },
        p3: {
          id: "p3",
          contentComponent: "default",
          params: { paneType: "sessionActivity", paneParams: { ref: "ref_a" } },
        },
        p4: {
          id: "p4",
          contentComponent: "default",
          params: { paneType: "sessionDetails", paneParams: { ref: "ref_a" } },
        },
        p5: {
          id: "p5",
          contentComponent: "default",
          params: { paneType: "doc", paneParams: { ref: "ref_a", path: "notes.md" } },
        },
        p6: { id: "p6", contentComponent: "default", params: { paneType: "transcript", paneParams: { ref: "ref_b" } } },
      },
      activeGroup: "group-2",
    };
    fake.serialized = saved;
    fake.fromJSONBehavior = () => {
      fake.panels = [
        { id: "p1", params: { paneType: "session", paneParams: { ref: "ref_a" } } },
        { id: "p2", params: { paneType: "sessionTasks", paneParams: { ref: "ref_a" } } },
        { id: "p3", params: { paneType: "sessionActivity", paneParams: { ref: "ref_a" } } },
        { id: "p4", params: { paneType: "sessionDetails", paneParams: { ref: "ref_a" } } },
        { id: "p5", params: { paneType: "doc", paneParams: { ref: "ref_a", path: "notes.md" } } },
        { id: "p6", params: { paneType: "transcript", paneParams: { ref: "ref_b" } } },
      ];
      fake.activePanel = { id: "p4" };
    };
    registerDockviewApi(asDockviewApi(fake));

    expect(workspaceStore.getState().layoutJSON()).toBe(saved);
    const ok = workspaceStore.getState().restoreLayout(workspaceStore.getState().layoutJSON());

    expect(ok).toBe(true);
    expect(fake.cleared).toBe(false);
    expect(workspaceStore.getState().panes).toEqual([
      // slot is re-derived from the restored grid order (workspace.ts's
      // restoreLayout): the first panel is the top-left/main one.
      { id: "p1", type: "session", params: { ref: "ref_a" }, slot: "main" },
      { id: "p2", type: "sessionTasks", params: { ref: "ref_a" }, slot: "secondary" },
      { id: "p3", type: "sessionActivity", params: { ref: "ref_a" }, slot: "secondary" },
      { id: "p4", type: "sessionDetails", params: { ref: "ref_a" }, slot: "secondary" },
      { id: "p5", type: "doc", params: { ref: "ref_a", path: "notes.md" }, slot: "secondary" },
      { id: "p6", type: "transcript", params: { ref: "ref_b" }, slot: "secondary" },
    ]);
    expect(workspaceStore.getState().focusedPaneId).toBe("p4");
    expect(
      workspaceStore
        .getState()
        .panes.slice(1, 4)
        .map((pane) => paneFor(pane.type).title(pane.params, {})),
    ).toEqual(["Tasks · ref_a", "Activity · ref_a", "Details · ref_a"]);
  });
});
