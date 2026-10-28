import type { DockviewApi } from "dockview-core";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { createAttachmentOperations } from "../panes/session/composer/attachments/useAttachments";
import { readRecoverySelection, readRefComposerSeat, writeRecoverySelection } from "../panes/session/composer/draft";
import { retainedTranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import type { MutationRecoveryRecord } from "../stores/mutationOutbox";
import { captureAttentionPlacement, presentAttentionTarget, restoreAttentionPlacement } from "./attentionPlacement";
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
afterEach(() => {
  resetWorkspaceStoreForTests();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function rootPane(): OpenPaneRecord {
  return { id: "session-1", type: "session", params: { ref: "root" }, slot: "main" };
}

function recoveryRow(id: string, text: string): MutationRecoveryRecord {
  const input = [{ type: "text" as const, text }];
  return {
    version: 1,
    clientMutationId: id,
    intentSequence: 1,
    createdAt: 1,
    targetRef: "root",
    method: "turn/start",
    payload: { ref: "root", input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
    composerText: text,
    recoveryKind: "rejected",
    state: "submitting",
    attempted: false,
  } as MutationRecoveryRecord;
}

test("attention substitution retains the source lifetime", async () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const source = conversationPaneLifetime(pane);
  const composer = source.composer;
  if (!composer) throw new Error("missing composer");
  composer.editText("/skill /command");
  composer.editSkillNames(["skill"]);
  composer.updateSelectionMeta({
    text: "/skill /command",
    skillNames: ["skill"],
    commandNames: ["command"],
    mentions: [
      { kind: "skill", name: "skill", offset: 0 },
      { kind: "command", name: "command", offset: 7 },
    ],
  });
  const view = retainedTranscriptReadView(source, "root", "session");
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing fixture origin");

  let complete: BlobCallback | undefined;
  vi.stubGlobal(
    "Image",
    class {
      width = 2;
      height = 3;
      onload: (() => void) | null = null;
      set src(_value: string) {
        this.onload?.();
      }
    },
  );
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:attention-image");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({
    drawImage() {},
  } as unknown as CanvasRenderingContext2D);
  vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((callback) => {
    complete = callback;
  });
  const rejected: string[] = [];
  createAttachmentOperations(composer.editor, composer.attachments).ingestFiles(
    [new File([new Uint8Array([1])], "a.png", { type: "image/png" })],
    (reason) => rejected.push(reason),
  );
  expect(composer.attachments.getState().items[0]?.pending).toBe(true);
  const draft = composer.getSnapshot();
  const destination = presentAttentionTarget("local:b");
  expect(destination?.pane).not.toBe(origin.pane);
  expect(workspaceStore.getState().panes).toContain(origin.pane);
  expect(origin.pane.slot).toBe("parked");
  expect(source.alive).toBe(true);
  const settled = new Promise<void>((resolve) => {
    const unsubscribe = composer.attachments.subscribe((state) => {
      if (state.items[0]?.pending === false) {
        unsubscribe();
        resolve();
      }
    });
  });
  if (!complete) throw new Error("encoder did not reach browser canvas");
  complete(new Blob([new Uint8Array([9, 8, 7])], { type: "image/png" }));
  await settled;
  expect(rejected).toEqual([]);
  expect(composer.attachments.getState().items[0]).toMatchObject({ data: "CQgH", width: 2, height: 3, pending: false });
  expect(composer.getSnapshot()).toEqual(draft);
  expect(restoreAttentionPlacement(origin)).toBe(true);
  expect(conversationPaneLifetime(origin.pane)).toBe(source);
  expect(source.composer).toBe(composer);
  expect(retainedTranscriptReadView(source, "root", "session")).toBe(view);
  expect(origin.readViewId).toBe(view.id);
  workspaceStore.getState().closePane(origin.pane.id);
  expect(source.alive).toBe(false);
  expect(restoreAttentionPlacement(origin)).toBe(false);
});

test("close retires parked readers without resurrection", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const origin = captureAttentionPlacement();
  if (!origin) throw new Error("missing fixture origin");
  const source = conversationPaneLifetime(pane);
  const reader = retainedTranscriptReadView(source, "root", "session");
  presentAttentionTarget("local:b");
  expect(source.alive).toBe(true);
  workspaceStore.getState().closePane(pane.id);
  expect(source.alive).toBe(false);
  expect(reader.alive).toBe(false);
  const replacement = rootPane();
  workspaceStore.setState({ panes: [...workspaceStore.getState().panes, replacement] });
  expect(restoreAttentionPlacement(origin)).toBe(false);
  expect(conversationPaneLifetime(replacement).serial).not.toBe(source.serial);
});

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

test("a resumed identity carries the pane's selected recovery owner", () => {
  const pane = rootPane();
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its source");
  const peer = recoveryRow("peer-row", "older peer work");
  const preserved = recoveryRow("preserved-row", "local [image 1]");
  composer.setActiveRecoveryId(preserved);
  expect(readRecoverySelection("root")?.clientMutationId).toBe(preserved.clientMutationId);

  lifetime.adoptResumedSource("resumed");
  const resumed = lifetime.composer;
  if (!resumed) throw new Error("resumed lifetime lacks its source");
  expect(readRecoverySelection("resumed")?.clientMutationId).toBe(preserved.clientMutationId);
  expect(readRecoverySelection("resumed")?.clientMutationId).not.toBe(peer.clientMutationId);
  lifetime.dispose();
});

test("a resumed identity keeps a newer destination selection", () => {
  const pane = rootPane();
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its source");
  writeRecoverySelection("resumed", "destination-row");
  composer.setActiveRecoveryId(recoveryRow("preserved-row", "local [image 1]"));

  lifetime.adoptResumedSource("resumed");
  expect(readRecoverySelection("resumed")?.clientMutationId).toBe("destination-row");
  lifetime.dispose();
});

test("a resumed source seat survives a later mobile route reconstruction", () => {
  const pane = rootPane();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const lifetime = conversationPaneLifetime(pane);
  const composer = lifetime.composer;
  if (!composer) throw new Error("session lifetime lacks its source");
  const preserved = recoveryRow("resumed-seat-row", "local resumed work");
  composer.setActiveRecoveryId(preserved);
  const seat = lifetime.composerSeatId;

  lifetime.adoptResumedSource("local:mobile-resumed");
  expect(readRefComposerSeat("local:mobile-resumed")).toBe(seat);

  // A reload reconstructs the resumed route under a fresh pane id. Route
  // placement must find this tab's seat through the conversation mapping.
  resetWorkspaceStoreForTests();
  const reloadedId = workspaceStore.getState().openPane("session", { ref: "local:mobile-resumed" });
  const reloaded = workspaceStore.getState().panes.find((record) => record.id === reloadedId);
  if (!reloaded?.composerSeatId) throw new Error("reloaded conversation lacks its seat");
  expect(reloaded.composerSeatId).toBe(seat);
  expect(readRecoverySelection("local:mobile-resumed")?.clientMutationId).toBe(preserved.clientMutationId);
});
