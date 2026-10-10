import type { DockviewApi } from "dockview-core";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { conversationPaneLifetime, disposeConversationPaneLifetime } from "./paneLifetime";
import { registerPaneForTests } from "./paneRegistry";
import { type OpenPaneRecord, registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

beforeAll(() => {
  registerPaneForTests({
    id: "session",
    title: () => "Session",
    component: lazy(() => new Promise(() => {})),
  });
});
beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetWorkspaceStoreForTests();
});
afterEach(() => resetWorkspaceStoreForTests());

function rootPane(): OpenPaneRecord {
  return { id: "session-1", type: "session", params: { ref: "root" }, slot: "main" };
}

test("the same committed pane retains its source owner across focus changes", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  composer?.editor.write("keep this draft", 15);
  workspaceStore.setState({ focusedPaneId: null });
  expect(conversationPaneLifetime(pane)).toBe(lifetime);
  expect(conversationPaneLifetime(pane).composer).toBe(composer);
  expect(composer?.getSnapshot().text).toBe("keep this draft");
  expect(lifetime.alive).toBe(true);
});

test("close followed by ID reuse invalidates the old source generation", () => {
  const oldRootPane = rootPane();
  const old = conversationPaneLifetime(oldRootPane);
  const composer = old.composer;
  if (!composer) throw new Error("session lifetime lacks its source");
  const generation = composer.attachments.getState().generationRef.current;
  disposeConversationPaneLifetime(oldRootPane);
  const replacement = conversationPaneLifetime(rootPane());
  expect(replacement.serial).not.toBe(old.serial);
  expect(old.alive).toBe(false);
  expect(replacement.composer?.attachments.getState().items).toEqual([]);
  expect(composer.attachments.getState().generationRef.current).toBeGreaterThan(generation);
  expect(conversationPaneLifetime(oldRootPane).alive).toBe(false);
  replacement.dispose();
});

test.each(["close", "replacePrimary", "reset", "same-ID replacement"] as const)(
  "%s ends only removed pane lifetimes",
  (action) => {
    const pane = rootPane();
    const other: OpenPaneRecord = {
      id: "transcript-2",
      type: "transcript",
      params: { ref: "child" },
      slot: "secondary",
    };
    workspaceStore.setState({ panes: [pane, other], focusedPaneId: pane.id });
    const lifetime = conversationPaneLifetime(pane);
    const otherLifetime = conversationPaneLifetime(other);
    void lifetime.composer;
    expect(otherLifetime.composer).toBeNull();
    if (action === "close") workspaceStore.getState().closePane(pane.id);
    else if (action === "replacePrimary") workspaceStore.getState().replacePrimary("session", { ref: "replacement" });
    else if (action === "reset") resetWorkspaceStoreForTests();
    else workspaceStore.setState({ panes: [rootPane(), other] });
    expect(lifetime.alive).toBe(false);
    expect(otherLifetime.alive).toBe(action === "close" || action === "same-ID replacement");
  },
);

test.each([false, true])("layout restore success %s disposes only committed replacements", (success) => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  composer?.editor.write("keep this draft", 15);
  // Dockview is the external layout boundary, workspace and source owners run unchanged.
  const api = {
    panels: [{ id: pane.id, params: { paneType: "session", paneParams: { ref: "root" } } }],
    activePanel: { id: pane.id },
    fromJSON() {
      if (!success) throw new Error("dockview: invalid layout");
    },
    clear() {},
  };
  registerDockviewApi(api as unknown as DockviewApi);
  expect(workspaceStore.getState().restoreLayout({})).toBe(success);
  expect(lifetime.alive).toBe(!success);
  const restored = workspaceStore.getState().panes[0];
  expect(restored).toBeDefined();
  if (!restored) throw new Error("layout restore discarded the source pane");
  expect(workspaceStore.getState().focusedPaneId).toBe("session-1");
  if (success) {
    expect(restored).not.toBe(pane);
    expect(conversationPaneLifetime(restored).serial).not.toBe(lifetime.serial);
  } else {
    expect(restored).toBe(pane);
    expect(conversationPaneLifetime(restored).composer).toBe(composer);
    expect(composer?.getSnapshot().text).toBe("keep this draft");
  }
});
