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
import type { DockviewApi } from "dockview-core";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import {
  browserDraftKey,
  getBrowserDraftRepository,
  resetBrowserDraftRepositoryForTests,
} from "../stores/browserDraftRepository";
import { resetThreadsStoreForTests } from "../stores/threads";
import { conversationPaneLifetime } from "./paneLifetime";
import { paneFor } from "./paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";
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
  globalThis.indexedDB = new IDBFactory();
  resetBrowserDraftRepositoryForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
});
afterEach(() => {
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  resetBrowserDraftRepositoryForTests();
});

test("a same-ID restored layout hydrates the full durable draft without persisting or reusing its source identity", async () => {
  const ref = "restore-structured";
  const key = browserDraftKey(ref);
  const repository = getBrowserDraftRepository();
  const value = {
    text: "🙂 /same /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command" as const, name: "same", offset: 3 },
      { kind: "skill" as const, name: "same", offset: 9 },
    ],
  };
  expect(await repository.seedDraft(key, value)).toMatchObject({ state: "committed" });
  const before = await repository.load(key);
  expect(before).toMatchObject(value);
  const old = { id: "same-pane", type: "session" as const, slot: "main" as const, params: { ref } };
  workspaceStore.setState({ panes: [old], focusedPaneId: old.id });
  const lifetime = conversationPaneLifetime(old);
  const source = lifetime.composer;
  if (!source) throw new Error("Missing original composer source");
  await source.ready();
  expect(source.getSnapshot()).toMatchObject(value);
  const saved = {
    grid: { root: { type: "branch", data: [] } },
    panels: {
      "same-pane": { id: old.id, contentComponent: "default", params: { paneType: "session", paneParams: { ref } } },
    },
  };
  expect(JSON.stringify(saved)).not.toContain(source.sourceId);
  resetWorkspaceStoreForTests();
  const fake = new FakeDockviewApi();
  fake.serialized = saved;
  fake.fromJSONBehavior = () => {
    fake.panels = [{ id: old.id, params: { paneType: "session", paneParams: { ref } } }];
    fake.activePanel = { id: old.id };
  };
  registerDockviewApi(asDockviewApi(fake));
  expect(workspaceStore.getState().restoreLayout(saved)).toBe(true);
  const restored = workspaceStore.getState().panes[0];
  if (!restored) throw new Error("Missing restored pane");
  expect(restored).toEqual(old);
  expect(restored).not.toBe(old);
  const replacement = conversationPaneLifetime(restored);
  const replacementSource = replacement.composer;
  if (!replacementSource) throw new Error("Missing restored composer source");
  await replacementSource.ready();
  await Promise.all([source.whenSettled(), replacementSource.whenSettled()]);
  expect(lifetime.alive).toBe(false);
  expect(replacement.serial).not.toBe(lifetime.serial);
  expect(replacementSource.sourceId).not.toBe(source.sourceId);
  expect(replacementSource.getSnapshot()).toMatchObject(value);
  expect(await repository.load(key)).toEqual(before);
  expect(restored).not.toHaveProperty("sourceId");
  expect(restored.params).not.toHaveProperty("sourceId");
  expect(workspaceStore.getState().layoutJSON()).toBe(saved);
  expect(JSON.stringify(workspaceStore.getState().layoutJSON())).not.toContain(replacementSource.sourceId);
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
          params: { paneType: "doc", paneParams: { session: "ref_a", path: "notes.md", kind: "file" } },
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
        { id: "p5", params: { paneType: "doc", paneParams: { session: "ref_a", path: "notes.md", kind: "file" } } },
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
      { id: "p4", type: "sessionDetails", params: { ref: "ref_a" }, slot: "secondary" },
      { id: "p5", type: "doc", params: { session: "ref_a", path: "notes.md", kind: "file" }, slot: "secondary" },
      { id: "p6", type: "transcript", params: { ref: "ref_b" }, slot: "secondary" },
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

// A persisted doc panel may carry an explicit opening origin (Task 4). The
// origin-less shape is the narrowly approved older form and stays readable; a
// MALFORMED origin must be refused, not silently downgraded to that older form.
// These cases drive the REAL restoreLayout -> readPanelParams -> doc.parseParams
// path (no direct parseParams call), so a missing doc.parseParams shows up as an
// actual panel that wrongly survived restore.
describe("a persisted doc panel's opening origin is preserved or refused on restore", () => {
  function restoreDocPanel(paneParams: unknown) {
    const fake = new FakeDockviewApi();
    const saved = {
      grid: { root: { type: "branch", data: [] } },
      panels: { p1: { id: "p1", contentComponent: "default", params: { paneType: "doc", paneParams } } },
    };
    fake.serialized = saved;
    fake.fromJSONBehavior = () => {
      fake.panels = [{ id: "p1", params: { paneType: "doc", paneParams } }];
      fake.activePanel = { id: "p1" };
    };
    registerDockviewApi(asDockviewApi(fake));
    const ok = workspaceStore.getState().restoreLayout(workspaceStore.getState().layoutJSON());
    return { fake, ok };
  }

  test("the approved older shape without origin restores unchanged", () => {
    const params = { session: "ref_a", path: "notes.md", kind: "file" };
    const { fake, ok } = restoreDocPanel(params);
    expect(ok).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([{ id: "p1", type: "doc", params, slot: "main" }]);
    expect(fake.panels.map((panel) => panel.id)).toEqual(["p1"]);
  });

  test("a valid browser-origin restores with its exact origin intact", () => {
    const params = {
      session: "ref_a",
      path: "notes.md",
      kind: "file",
      origin: { kind: "browser-origin", paneId: "owner-pane", compositionOwnerId: "owner-A" },
    };
    const { fake, ok } = restoreDocPanel(params);
    expect(ok).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([{ id: "p1", type: "doc", params, slot: "main" }]);
    expect(fake.panels.map((panel) => panel.id)).toEqual(["p1"]);
  });

  test("a valid browser-readonly-session origin restores with its exact source pane", () => {
    const params = {
      session: "ref_a",
      path: "notes.md",
      kind: "file",
      origin: { kind: "browser-readonly-session", sourcePaneId: "cascade-pane" },
    };
    const { fake, ok } = restoreDocPanel(params);
    expect(ok).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([{ id: "p1", type: "doc", params, slot: "main" }]);
    expect(fake.panels.map((panel) => panel.id)).toEqual(["p1"]);
  });

  test("a malformed origin is refused: the panel is skipped, not read as older origin-less data", () => {
    const { fake, ok } = restoreDocPanel({
      session: "ref_a",
      path: "notes.md",
      kind: "file",
      origin: { kind: "bogus" },
    });
    expect(ok).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([]);
    expect(fake.panels).toEqual([]);
  });

  test("a browser-origin missing its compositionOwnerId is refused", () => {
    const { fake, ok } = restoreDocPanel({
      session: "ref_a",
      path: "notes.md",
      kind: "file",
      origin: { kind: "browser-origin", paneId: "owner-pane" },
    });
    expect(ok).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([]);
    expect(fake.panels).toEqual([]);
  });

  test("a browser-readonly-session with an empty sourcePaneId is refused", () => {
    const { fake, ok } = restoreDocPanel({
      session: "ref_a",
      path: "notes.md",
      kind: "file",
      origin: { kind: "browser-readonly-session", sourcePaneId: "" },
    });
    expect(ok).toBe(true);
    expect(workspaceStore.getState().panes).toEqual([]);
    expect(fake.panels).toEqual([]);
  });
});

// The dedupe contract the origin change must not break: two openings of the SAME
// session/path under DIFFERENT origins are two distinct destinations, while
// identical params still focus the existing record. Driven through the real
// workspaceStore.openPane the doc producer uses.
test("the same file from two owners stays two distinct doc records, while identical params dedupe", () => {
  const base = { session: "ref_a", path: "notes.md", kind: "file" };
  const originA = { kind: "browser-origin", paneId: "pane-A", compositionOwnerId: "owner-A" };
  const originB = { kind: "browser-origin", paneId: "pane-B", compositionOwnerId: "owner-B" };
  const idA = workspaceStore.getState().openPane("doc", { ...base, origin: originA });
  const idB = workspaceStore.getState().openPane("doc", { ...base, origin: originB });
  expect(idB).not.toBe(idA);
  expect(workspaceStore.getState().panes.filter((pane) => pane.type === "doc")).toHaveLength(2);
  expect(workspaceStore.getState().openPane("doc", { ...base, origin: originA })).toBe(idA);
  expect(workspaceStore.getState().panes.filter((pane) => pane.type === "doc")).toHaveLength(2);
});
