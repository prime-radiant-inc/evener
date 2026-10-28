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
  readRecoverySelection,
  readRefComposerSeat,
  writeComposerSeat,
  writeRecoverySelection,
  writeRefComposerSeat,
} from "../panes/session/composer/draft";
import { retainedConversationComposer } from "../panes/session/composer/retainedComposer";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { setMutationClientIdentityForTests } from "../stores/mutationClientIdentity";
import { conversationPaneLifetime } from "./paneLifetime";
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
    writeRecoverySelection("ref_leaf", "preserved-local");
    // Another tab saves a different seat and pick for the same conversation
    // under its own identity. Its pick key is invisible to this tab.
    setMutationClientIdentityForTests("tab-b");
    writeComposerSeat("p1", "ref_leaf", "tab-b-seat");
    writeRecoverySelection("ref_leaf", "other-tab-row");
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
    expect(readRecoverySelection("ref_leaf")?.clientMutationId).toBe("preserved-local");
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});
// Layouts are shared across tabs. Another tab can drill the same cascade one
// level deeper and save, so this tab restores its saved pane id showing a leaf
// it never saved. The saved seat is still this cascade's: continuity runs
// through the source path, not the exact leaf, so the unchanged ancestor
// columns keep their saved selections instead of falling back to a fresh
// seat and older rejected rows.
test("a cascade restore keeps this tab's seat when the layout drilled deeper", () => {
  installLocalStorage(new MemoryStorage());
  try {
    setMutationClientIdentityForTests("tab-a");
    writeComposerSeat("p1", "ref_child", "tab-a-seat");
    writeRecoverySelection("ref_owner", "owner-row");
    writeRecoverySelection("ref_child", "child-row");

    restorePanels({
      p1: {
        id: "p1",
        contentComponent: "default",
        params: {
          paneType: "sessionZoom",
          paneParams: {
            ref: "ref_grandchild",
            source: { type: "session", params: { ref: "ref_owner" } },
            edges: [
              { ownerRef: "ref_owner", childRef: "ref_child", delegateId: "delegate-1" },
              { ownerRef: "ref_child", childRef: "ref_grandchild", delegateId: "delegate-2" },
            ],
          },
        },
      },
    });

    const pane = workspaceStore.getState().panes.find((record) => record.id === "p1");
    expect(pane?.type).toBe("sessionZoom");
    expect(pane?.composerSeatId).toBe("tab-a-seat");
    expect(readRecoverySelection("ref_owner")?.clientMutationId).toBe("owner-row");
    expect(readRecoverySelection("ref_child")?.clientMutationId).toBe("child-row");
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});
// This tab held two conversations on one cascade seat; another tab saved the
// shared layout with them as two separate panes. Restoring claims the shared
// seat for the first pane, so the second must have that seat's saved
// selection migrated to its own distinct seat - or a reload of the second
// conversation restores an older rejected row instead of its preserved work.
test("a restore resolving a seat collision keeps the saved selection readable", () => {
  installLocalStorage(new MemoryStorage());
  try {
    setMutationClientIdentityForTests("tab-a");
    writeRefComposerSeat("ref_parent", "seat-s");
    writeRefComposerSeat("ref_child", "seat-s");
    writeRecoverySelection("ref_child", "child-row");
    writeComposerSeat("p1", "ref_parent", "seat-s");

    restorePanels({
      p1: {
        id: "p1",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "ref_parent" } },
      },
      p2: {
        id: "p2",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "ref_child" } },
      },
    });

    const parent = workspaceStore.getState().panes.find((record) => record.id === "p1");
    expect(parent?.composerSeatId).toBe("seat-s");
    const child = workspaceStore.getState().panes.find((record) => record.id === "p2");
    if (!child?.composerSeatId) throw new Error("child lacks its seat");
    expect(child.composerSeatId).not.toBe("seat-s");
    expect(readRecoverySelection("ref_child")?.clientMutationId).toBe("child-row");
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});
// A restored cascade colliding onto a replacement seat must carry EVERY
// column's stored selection with it, not only its leaf's: the ancestor
// columns read the replacement seat, and their preserved work lives under
// the seat that was claimed first. A reload of a non-leaf column then
// restores its own selected work instead of an older rejected peer row.
test("a seat-collision restore keeps every cascade column's pick readable", () => {
  installLocalStorage(new MemoryStorage());
  try {
    setMutationClientIdentityForTests("tab-a");
    // One cascade seat held the shared ancestor's and the leaf's selections;
    // another pane saved on this tab claims that seat first.
    writeRefComposerSeat("ref_shared", "seat-s");
    writeRefComposerSeat("ref_leaf", "seat-s");
    writeRecoverySelection("ref_shared", "shared-row");
    writeRecoverySelection("ref_leaf", "leaf-row");
    writeComposerSeat("p1", "ref_parent", "seat-s");
    writeComposerSeat("p2", "ref_leaf", "seat-s");

    restorePanels({
      p1: {
        id: "p1",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "ref_parent" } },
      },
      p2: {
        id: "p2",
        contentComponent: "default",
        params: {
          paneType: "sessionZoom",
          paneParams: {
            ref: "ref_leaf",
            source: { type: "session", params: { ref: "ref_shared" } },
            edges: [{ ownerRef: "ref_shared", childRef: "ref_leaf", delegateId: "delegate-shared-leaf" }],
          },
        },
      },
    });

    const parent = workspaceStore.getState().panes.find((record) => record.id === "p1");
    expect(parent?.composerSeatId).toBe("seat-s");
    const cascade = workspaceStore.getState().panes.find((record) => record.id === "p2");
    if (!cascade?.composerSeatId) throw new Error("cascade lacks its seat");
    expect(cascade.composerSeatId).not.toBe("seat-s");

    // The non-leaf column restores its own selected work under the
    // replacement seat, exactly like the leaf does.
    const sharedColumn = retainedConversationComposer(conversationPaneLifetime(cascade), "ref_shared");
    if (!sharedColumn) throw new Error("shared column lacks a composer");
    expect(readRecoverySelection("ref_shared")?.clientMutationId).toBe("shared-row");
    expect(readRecoverySelection("ref_leaf")?.clientMutationId).toBe("leaf-row");
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});
// Design A contract: a cold restore needs no ancestry to keep a column's
// pick restorable. The ancestor's selection is the conversation's own, so
// when authoritative ancestry later reveals the column, its composer reads
// its pick without any migration having run.
test("a cold restore keeps an ancestry-only ancestor's pick restorable", () => {
  installLocalStorage(new MemoryStorage());
  try {
    setMutationClientIdentityForTests("tab-a");
    // The ancestor is supplied only by authoritative ancestry: the recorded
    // edge segment is empty and names neither it nor the source chain.
    const ancestorRef = "local:cold-ancestor";
    const leafRef = "local:cold-leaf";
    writeRefComposerSeat(ancestorRef, "seat-s");
    writeRefComposerSeat(leafRef, "seat-s");
    writeRecoverySelection(ancestorRef, "cold-ancestor-row");
    writeRecoverySelection(leafRef, "cold-leaf-row");
    // Another pane saved on this tab claims the shared seat first, so the
    // cascade restores onto a replacement seat.
    writeComposerSeat("p0", "local:cold-other", "seat-s");
    writeComposerSeat("p1", leafRef, "seat-s");

    restorePanels({
      p0: {
        id: "p0",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "local:cold-other" } },
      },
      p1: {
        id: "p1",
        contentComponent: "default",
        params: {
          paneType: "sessionZoom",
          paneParams: {
            ref: leafRef,
            source: { type: "session", params: { ref: "local:cold-source" } },
            edges: [],
          },
        },
      },
    });

    // Cold load: no client, no ancestry. The cascade restored onto a
    // replacement seat, and no migration ran for the ancestor. When ancestry
    // later reveals the ancestor column, its composer still reads its own
    // pick without any migration having run.
    const cascade = workspaceStore.getState().panes.find((record) => record.id === "p1");
    if (!cascade?.composerSeatId) throw new Error("cascade lacks its seat");
    expect(cascade.composerSeatId).not.toBe("seat-s");
    const column = retainedConversationComposer(conversationPaneLifetime(cascade), ancestorRef);
    if (!column) throw new Error("ancestor column lacks a composer");
    expect(readRecoverySelection(ancestorRef)?.clientMutationId).toBe("cold-ancestor-row");
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

// AppShell can reconstruct a routed conversation before restoring a saved
// desktop layout whose pane id belongs to a different conversation. The route
// placement must not overwrite that saved pane-seat mapping.
test("a route placement before restore keeps the saved pane seat", () => {
  installLocalStorage(new MemoryStorage());
  try {
    setMutationClientIdentityForTests("tab-a");
    // The previous page saved conversation A on pane_session_1, with its seat
    // and selected recovery row.
    writeComposerSeat("pane_session_1", "local:a", "saved-a-seat");
    writeRecoverySelection("local:a", "a-row");

    // Cold startup reconstructs route B first; pane ids restarted, so B lands
    // on A's saved id.
    const routedId = workspaceStore.getState().openPane("session", { ref: "local:b" });
    expect(routedId).toBe("pane_session_1");

    restorePanels({
      pane_session_1: {
        id: "pane_session_1",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "local:a" } },
      },
    });

    const restored = workspaceStore.getState().panes.find((pane) => pane.id === "pane_session_1");
    expect(restored?.composerSeatId).toBe("saved-a-seat");
    expect(readRecoverySelection("local:a")?.clientMutationId).toBe("a-row");
    // B's route placement never touched A's conversation pick.
    expect(readRecoverySelection("local:b")).toBeUndefined();
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});

// Another tab can save a different conversation under this pane id. This tab
// still remembers that id as conversation A, but the restored pane is B; the
// pane mapping must be validated against B rather than preferred blindly.
test("shared-layout restore ignores a pane seat saved for another conversation", () => {
  installLocalStorage(new MemoryStorage());
  try {
    setMutationClientIdentityForTests("tab-a");
    // This tab remembers p1 as conversation A.
    writeComposerSeat("p1", "ref_a", "seat-a");
    writeRecoverySelection("ref_a", "a-row");
    // This tab's correct mapping for conversation B.
    writeRefComposerSeat("ref_b", "seat-b");
    writeRecoverySelection("ref_b", "b-row");

    // Another tab saved conversation B under the same pane id.
    restorePanels({
      p1: {
        id: "p1",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "ref_b" } },
      },
    });

    const restored = workspaceStore.getState().panes.find((pane) => pane.id === "p1");
    expect(restored?.composerSeatId).toBe("seat-b");
    expect(readRecoverySelection("ref_b")?.clientMutationId).toBe("b-row");
    // Conversation A's saved pick is not overwritten by the B restore.
    expect(readRecoverySelection("ref_a")?.clientMutationId).toBe("a-row");
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});

// A restored layout can list an unfamiliar pane before a known one for the
// same conversation (a session and a cascade saved by another tab). The known
// pane's validated mapping must be reserved before the unfamiliar pane can
// consume the ref fallback.
test("restore reserves known pane seats before ref fallbacks", () => {
  installLocalStorage(new MemoryStorage());
  try {
    setMutationClientIdentityForTests("tab-a");
    writeComposerSeat("p_known", "ref_a", "seat-known");
    writeRecoverySelection("ref_a", "known-row");

    restorePanels({
      p_unknown: {
        id: "p_unknown",
        contentComponent: "default",
        // A cascade for the same conversation (plain session duplicates are
        // collapsed by the legacy reconciliation).
        params: {
          paneType: "sessionZoom",
          paneParams: { ref: "ref_a", source: { type: "session", params: { ref: "local:root" } }, edges: [] },
        },
      },
      p_known: {
        id: "p_known",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "ref_a" } },
      },
    });

    const panes = workspaceStore.getState().panes;
    const known = panes.find((pane) => pane.id === "p_known");
    const unknown = panes.find((pane) => pane.id === "p_unknown");
    expect(known?.composerSeatId).toBe("seat-known");
    expect(unknown?.composerSeatId).not.toBe("seat-known");
    expect(readRecoverySelection("ref_a")?.clientMutationId).toBe("known-row");
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});

// A restored layout can hold a session pane and a cascade for one
// conversation. Each pane keeps its own seat, but the conversation mapping -
// what a mobile route reconstruction reads after the reload - must follow the
// restored focus, not whichever same-ref pane the panel iteration wrote last.
test("restore reconciles a shared conversation mapping from the focused pane", () => {
  installLocalStorage(new MemoryStorage());
  try {
    setMutationClientIdentityForTests("tab-a");
    writeComposerSeat("p_session", "ref_shared", "session-seat");
    writeComposerSeat("p_cascade", "ref_shared", "cascade-seat");
    // One pick per conversation: the focused session pane's row.
    writeRecoverySelection("ref_shared", "session-row");

    restorePanels({
      p_session: {
        id: "p_session",
        contentComponent: "default",
        params: { paneType: "session", paneParams: { ref: "ref_shared" } },
      },
      p_cascade: {
        id: "p_cascade",
        contentComponent: "default",
        params: {
          paneType: "sessionZoom",
          paneParams: { ref: "ref_shared", source: { type: "session", params: { ref: "local:root" } }, edges: [] },
        },
      },
    });
    // restorePanels focuses the first panel: the session pane, whose
    // iteration-time write the cascade's would have overwritten.
    expect(workspaceStore.getState().focusedPaneId).toBe("p_session");
    expect(readRefComposerSeat("ref_shared")).toBe("session-seat");

    // A mobile route reconstruction after the restore restores the focused
    // pane's selected row, not the inactive cascade's.
    resetWorkspaceStoreForTests();
    const reloadedId = workspaceStore.getState().openPane("session", { ref: "ref_shared" });
    const reloaded = workspaceStore.getState().panes.find((pane) => pane.id === reloadedId);
    if (!reloaded?.composerSeatId) throw new Error("reloaded route lacks its seat");
    expect(reloaded.composerSeatId).toBe("session-seat");
    expect(readRecoverySelection("ref_shared")?.clientMutationId).toBe("session-row");
  } finally {
    setMutationClientIdentityForTests(undefined);
  }
});
