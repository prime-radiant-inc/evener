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
  fromJSONBehavior: (data: unknown) => void = () => {};

  toJSON(): unknown {
    return { fake: "layout" };
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
  test("a saved layout containing a doc pane and a transcript pane survives restore intact", () => {
    const fake = new FakeDockviewApi();
    fake.fromJSONBehavior = () => {
      fake.panels = [
        { id: "p1", params: { paneType: "doc", paneParams: { ref: "a", path: "notes.md" } } },
        { id: "p2", params: { paneType: "transcript", paneParams: { ref: "b" } } },
      ];
      fake.activePanel = { id: "p2" };
    };
    registerDockviewApi(asDockviewApi(fake));

    const ok = workspaceStore.getState().restoreLayout({ real: "saved layout" });

    expect(ok).toBe(true);
    expect(fake.cleared).toBe(false);
    expect(workspaceStore.getState().panes).toEqual([
      // slot is re-derived from the restored grid order (workspace.ts's
      // restoreLayout): the first panel is the top-left/main one.
      { id: "p1", type: "doc", params: { ref: "a", path: "notes.md" }, slot: "main" },
      { id: "p2", type: "transcript", params: { ref: "b" }, slot: "secondary" },
    ]);
    expect(workspaceStore.getState().focusedPaneId).toBe("p2");
  });
});
