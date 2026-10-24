// Boot-time pane registration keeps a persisted layout intact across a reload.
//
// AppShell eagerly registers every pane type that can appear in a restored
// dockview layout. welcome/session/settings/spawn always could; doc and
// transcript are the wave-8 additions - they otherwise register only lazily,
// the first time a producer imports their opener (openDoc.ts / paneActions.ts).
// A layout persisted with one of those panes open would, on the next page
// load, reach DockHost's boot-time restoreLayout() BEFORE any such producer
// ran and find the pane type unregistered - restoreLayout now SKIPS such a
// panel (removing its orphaned dockview panel from the live api
// synchronously, including on this double) rather than discarding the
// layout, but a skipped pane is still a pane the user lost
// (workspace.ts:readPanelParams; the sibling
// partial-recovery cases are workspace.test.ts's "skips a panel whose
// paneType..." tests, which prove the skip).
//
// Importing AppShell here runs exactly those boot-time side-effect
// registrations - the same module evaluation production performs - and the
// FakeDockviewApi (the workspace.test.ts unit double) drives restoreLayout
// against a layout that contains a doc pane AND a transcript pane.
//
// Mutation net: deleting either `import "../panes/doc"` or
// `import "../panes/transcript"` from AppShell.tsx leaves that pane type
// unregistered at boot, so restoreLayout skips it and this test's
// both-panes-present assertions fail (restoreLayout()===true /
// cleared===false still hold - the skip, including its synchronous
// removePanel, is graceful on this double, which is exactly why the
// pane-presence assertions are the ones that must carry the net).
import { bindFilePath } from "@evener/appwire-client/docContent";
import type { DockviewApi } from "dockview-core";
import { beforeEach, describe, expect, test } from "vitest";
import { openDocBeside } from "../panes/doc/openDoc";
import {
  composerSelectionScope,
  readRecoverySelection,
  writeComposerSeat,
  writeRecoverySelection,
} from "../panes/session/composer/draft";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { setMutationClientIdentityForTests } from "../stores/mutationClientIdentity";
import { paneFor } from "./paneRegistry";
import {
  conversationOpenParentRef,
  documentPaneState,
  registerDockviewApi,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "./workspace";
import "./AppShell"; // side effect: the boot-time pane-type registrations under test

// A minimal DockviewApi double covering only what workspace.ts touches
// (fromJSON/panels/activePanel/removePanel/clear) - the same unit-test seam
// as workspace.test.ts, duplicated here.
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

  removePanel(panel: { id: string }): void {
    this.panels = this.panels.filter((p) => p.id !== panel.id);
    if (this.activePanel?.id === panel.id) this.activePanel = undefined;
  }
}

function asDockviewApi(fake: FakeDockviewApi): DockviewApi {
  return fake as unknown as DockviewApi;
}

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

describe("boot-time registration lets a persisted layout with lazy panes restore", () => {
  test("cold restore with reused IDs restores saved focus but never a previous exact document owner or binding", () => {
    const session = "local:02wMz5TxvEMoJEDTDGOTil";
    const sourceId = workspaceStore.getState().openPane("session", { ref: session });
    const reference = bindFilePath("/work/A/notes.md", "/work/A");
    if (!reference) throw new Error("reference did not bind");
    openDocBeside({ session, reference, sourcePaneId: sourceId });
    openDocBeside({ session, reference, sourcePaneId: sourceId });
    const oldPanes = workspaceStore.getState().panes;
    const oldDocument = oldPanes.find((pane) => pane.type === "doc");
    if (!oldDocument) throw new Error("document did not open");
    expect(documentPaneState(oldDocument)?.reopen).toBe(1);
    const saved = JSON.parse(
      JSON.stringify(
        oldPanes.map((pane) => ({
          id: pane.id,
          params: { paneType: pane.type, paneParams: pane.params },
        })),
      ),
    );
    const fake = new FakeDockviewApi();
    registerDockviewApi(asDockviewApi(fake));
    registerDockviewApi(null);
    resetWorkspaceStoreForTests();
    fake.fromJSONBehavior = () => {
      fake.panels = saved;
      fake.activePanel = { id: oldDocument.id };
    };
    registerDockviewApi(asDockviewApi(fake));

    expect(workspaceStore.getState().restoreLayout({})).toBe(true);

    const restored = workspaceStore.getState().panes.find((pane) => pane.id === oldDocument.id);
    if (!restored) throw new Error("document did not restore");
    expect(restored).not.toBe(oldDocument);
    expect(documentPaneState(oldDocument)).toBeUndefined();
    expect(documentPaneState(restored)).toBeUndefined();
    expect(workspaceStore.getState().focusedPaneId).toBe(oldDocument.id);
    expect(workspaceStore.getState().openPane("doc", { session, path: "new.md", kind: "file" })).not.toBe(
      oldDocument.id,
    );
  });

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
          params: { paneType: "doc", paneParams: { session: "ref_a", path: "notes.md", kind: "file" } },
        },
        p6: {
          id: "p6",
          contentComponent: "default",
          params: { paneType: "transcript", paneParams: { ref: "job:job-1", parentRef: "ref_a" } },
        },
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
        { id: "p5", params: { paneType: "doc", paneParams: { session: "ref_a", path: "notes.md", kind: "file" } } },
        { id: "p6", params: { paneType: "transcript", paneParams: { ref: "job:job-1", parentRef: "ref_a" } } },
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
      { id: "p1", type: "session", params: { ref: "ref_a" }, slot: "main", composerSeatId: expect.any(String) },
      { id: "p2", type: "sessionTasks", params: { ref: "ref_a" }, slot: "secondary" },
      { id: "p4", type: "sessionDetails", params: { ref: "ref_a" }, slot: "secondary" },
      { id: "p5", type: "doc", params: { session: "ref_a", path: "notes.md", kind: "file" }, slot: "secondary" },
      { id: "p6", type: "transcript", params: { ref: "job:job-1", parentRef: "ref_a" }, slot: "secondary" },
    ]);
    expect(fake.panels.map((pane) => pane.id)).not.toContain("p3");
    expect(workspaceStore.getState().focusedPaneId).toBe("p4");
    expect(
      workspaceStore
        .getState()
        .panes.slice(1, 3)
        .map((pane) => paneFor(pane.type).title(pane.params, {})),
    ).toEqual(["Tasks · ref_a", "Details · ref_a"]);
  });
});

test("a saved session-ref transcript pane restores as the live conversation pane", () => {
  const fake = new FakeDockviewApi();
  registerDockviewApi(asDockviewApi(fake));
  const saved = {
    grid: {
      root: { type: "branch", data: [{ type: "leaf", data: { views: ["p1", "p6", "p7"] } }] },
      activeGroup: "group-1",
    },
    panels: {
      p1: { id: "p1", contentComponent: "default", params: { paneType: "session", paneParams: { ref: "ref_a" } } },
      p6: {
        id: "p6",
        contentComponent: "default",
        params: { paneType: "transcript", paneParams: { ref: "ref_b", parentRef: "ref_a" } },
      },
      p7: {
        id: "p7",
        contentComponent: "default",
        params: { paneType: "transcript", paneParams: { ref: "job:job-2", parentRef: "ref_a" } },
      },
    },
    activeGroup: "group-1",
  };
  fake.serialized = saved;
  fake.fromJSONBehavior = () => {
    fake.panels = [
      { id: "p1", params: { paneType: "session", paneParams: { ref: "ref_a" } } },
      { id: "p6", params: { paneType: "transcript", paneParams: { ref: "ref_b", parentRef: "ref_a" } } },
      { id: "p7", params: { paneType: "transcript", paneParams: { ref: "job:job-2", parentRef: "ref_a" } } },
    ];
    fake.activePanel = { id: "p1" };
  };

  const ok = workspaceStore.getState().restoreLayout(saved);

  expect(ok).toBe(true);
  expect(workspaceStore.getState().panes).toEqual([
    { id: "p1", type: "session", params: { ref: "ref_a" }, slot: "main", composerSeatId: expect.any(String) },
    { id: "p6", type: "session", params: { ref: "ref_b" }, slot: "secondary", composerSeatId: expect.any(String) },
    { id: "p7", type: "transcript", params: { ref: "job:job-2", parentRef: "ref_a" }, slot: "secondary" },
  ]);
});

test("a layout holding both a session pane and a legacy transcript pane for one conversation restores it once", () => {
  const fake = new FakeDockviewApi();
  const saved = {
    grid: {
      root: { type: "branch", data: [{ type: "leaf", data: { views: ["p1", "p2", "p3"] } }] },
      activeGroup: "group-1",
    },
    panels: {
      p1: {
        id: "p1",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "local:owner" } },
      },
      p2: {
        id: "p2",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "local:child" } },
      },
      p3: {
        id: "p3",
        contentComponent: "default",
        params: { paneType: "transcript", paneParams: { ref: "local:child", parentRef: "local:owner" } },
      },
    },
    activeGroup: "group-1",
  };
  fake.serialized = saved;
  // Model dockview itself: the panels it builds come from the JSON the
  // migration hands it, so dropping a panel has to happen before fromJSON
  // (the live api would otherwise hold the duplicate too).
  fake.fromJSONBehavior = (data) => {
    const layout = data as { panels?: Record<string, { params?: unknown }> };
    fake.panels = Object.entries(layout.panels ?? {}).map(([id, panel]) => ({ id, params: panel.params }));
    fake.activePanel = { id: "p1" };
  };
  registerDockviewApi(asDockviewApi(fake));

  expect(workspaceStore.getState().restoreLayout(saved)).toBe(true);

  // One conversation, one pane: route reconciliation requires exactly one
  // child pane, so the legacy read-only pane must not survive beside it.
  expect(
    workspaceStore.getState().panes.map((pane) => ({ id: pane.id, ref: (pane.params as { ref?: string }).ref })),
  ).toEqual([
    { id: "p1", ref: "local:owner" },
    { id: "p2", ref: "local:child" },
  ]);
  expect(fake.panels.map((panel) => panel.id)).toEqual(["p1", "p2"]);
  // The dropped pane's open-parent edge moves to the survivor, or the
  // conversation's Back walk is lost with the duplicate.
  const child = workspaceStore.getState().panes.find((pane) => pane.id === "p2");
  if (!child) throw new Error("child pane missing");
  expect(conversationOpenParentRef(child)).toBe("local:owner");
});

test("a removed legacy duplicate keeps the saved conversation focused", () => {
  const fake = new FakeDockviewApi();
  const saved = {
    grid: {
      root: { type: "branch", data: [{ type: "leaf", data: { views: ["p1", "p2", "p3", "p4"] } }] },
      activeGroup: "group-1",
    },
    panels: {
      p1: {
        id: "p1",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "local:owner" } },
      },
      p2: {
        id: "p2",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "local:child" } },
      },
      p3: {
        id: "p3",
        contentComponent: "default",
        params: { paneType: "transcript", paneParams: { ref: "local:child", parentRef: "local:owner" } },
      },
      p4: {
        id: "p4",
        contentComponent: "default",
        params: { paneType: "doc", paneParams: { session: "local:owner", path: "notes.md", kind: "file" } },
      },
    },
    activeGroup: "group-1",
  };
  fake.serialized = saved;
  fake.fromJSONBehavior = (data) => {
    const layout = data as { panels?: Record<string, { params?: unknown }> };
    fake.panels = Object.entries(layout.panels ?? {}).map(([id, panel]) => ({ id, params: panel.params }));
    // The saved focus was the legacy transcript pane the reconciliation drops.
    fake.activePanel = { id: "p3" };
  };
  registerDockviewApi(asDockviewApi(fake));

  expect(workspaceStore.getState().restoreLayout(saved)).toBe(true);

  // Focus follows the conversation that pane became, not whatever unrelated
  // panel the removal left selected.
  expect(workspaceStore.getState().focusedPaneId).toBe("p2");
});

function restorePanels(panels: Record<string, unknown>): void {
  const fake = new FakeDockviewApi();
  fake.fromJSONBehavior = (data) => {
    const layout = data as { panels?: Record<string, { params?: unknown }> };
    fake.panels = Object.entries(layout.panels ?? {}).map(([id, panel]) => ({ id, params: panel.params }));
    const first = Object.keys(layout.panels ?? {})[0];
    fake.activePanel = first === undefined ? undefined : { id: first };
  };
  registerDockviewApi(asDockviewApi(fake));
  expect(workspaceStore.getState().restoreLayout({ panels })).toBe(true);
}

// A sessionZoom has no own routed ref on the shared layout seat path. Restoring
// it must use this tab's persisted cascade seat, not one another tab saved.
test("a sessionZoom restore keeps this tab's seat when another tab saved another", () => {
  installLocalStorage(new MemoryStorage());
  try {
    setMutationClientIdentityForTests("tab-a");
    writeComposerSeat("p1", "ref_leaf", "tab-a-seat");
    writeRecoverySelection("ref_leaf", composerSelectionScope("tab-a-seat"), "preserved-local");
    // Another tab saves a different seat for the same conversation under its
    // own identity. It is invisible to this tab.
    setMutationClientIdentityForTests("tab-b");
    writeComposerSeat("p1", "ref_leaf", "tab-b-seat");
    writeRecoverySelection("ref_leaf", composerSelectionScope("tab-b-seat"), "other-tab-row");
    setMutationClientIdentityForTests("tab-a");

    restorePanels({
      p1: {
        id: "p1",
        contentComponent: "default",
        params: {
          paneType: "sessionZoom",
          paneParams: {
            ref: "ref_leaf",
            source: { type: "session", params: { ref: "local:root" } },
            edges: [],
          },
        },
      },
    });

    const pane = workspaceStore.getState().panes.find((record) => record.id === "p1");
    expect(pane?.composerSeatId).toBe("tab-a-seat");
    expect(readRecoverySelection("ref_leaf", composerSelectionScope("tab-a-seat"))).toBe("preserved-local");
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});

test("restoring two panes for one conversation allocates distinct seats", () => {
  installLocalStorage(new MemoryStorage());
  restorePanels({
    p1: {
      id: "p1",
      contentComponent: "default",
      params: { paneType: "session", paneParams: { ref: "ref_a" } },
    },
    p2: {
      id: "p2",
      contentComponent: "default",
      // A cascade for the same conversation is a distinct pane: the legacy
      // duplicate reconciliation only collapses two plain session panes.
      params: {
        paneType: "sessionZoom",
        paneParams: { ref: "ref_a", source: { type: "session", params: { ref: "local:root" } }, edges: [] },
      },
    },
  });
  const seats = workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "session" || pane.type === "sessionZoom")
    .map((pane) => pane.composerSeatId);
  expect(seats.every((seat) => typeof seat === "string" && seat !== "")).toBe(true);
  expect(new Set(seats).size).toBe(2);
});
