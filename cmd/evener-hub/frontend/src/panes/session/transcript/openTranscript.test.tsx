import { keyID } from "@evener/appwire-client/state/navigation";
import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, expect, test } from "vitest";
import { captureAttentionPlacement, presentAttentionTarget } from "../../../shell/attentionPlacement";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import {
  conversationOpenParentRef,
  recordConversationOpenParent,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "../../../shell/workspace";
import { navigationStore } from "../../../stores/navigation/store";
import { conversationContextIncludes, OpenTranscriptButton, openTranscript } from "./openTranscript";

beforeAll(async () => {
  await import("../");
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  navigationStore.setState({ resources: new Map() });
});

function transcriptPanes(ref: string) {
  return workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "transcript" && (pane.params as { ref?: unknown }).ref === ref);
}

function sessionPane(ref: string) {
  return workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === ref);
}

// A location the navigation store has already fetched: the only source that
// can prove a nested session's top-level owner.
function seedLocation(ref: string, topLevelRef: string) {
  const key = { kind: "location", ref } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: {
      generation_id: "generation_test",
      revision: 1,
      ref,
      top_level_ref: topLevelRef,
      top_level: ref === topLevelRef,
    },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "etag",
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  });
  navigationStore.setState({ resources });
}

test("opens a session child as a conversation pane beside its origin", () => {
  workspaceStore.getState().openPane("session", { ref: "local:owner" });
  openTranscript("local:child", "local:owner");

  const child = sessionPane("local:child");
  expect(child).toBeDefined();
  expect(child?.slot).toBe("secondary");
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);
  expect(transcriptPanes("local:child")).toHaveLength(0);
});

test("reuses the open conversation pane for the same child instead of duplicating", () => {
  openTranscript("local:child", "local:owner-a");
  const first = sessionPane("local:child");
  expect(first).toBeDefined();

  openTranscript("local:child", "local:owner-b");

  const state = workspaceStore.getState();
  expect(
    state.panes.filter((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child"),
  ).toHaveLength(1);
  expect(sessionPane("local:child")?.id).toBe(first?.id);
  expect(state.focusedPaneId).toBe(first?.id);
  expect(transcriptPanes("local:child")).toHaveLength(0);
});

test("does not promote an owner from a stale navigation location during a reconnect", () => {
  // Seed a stale location: the store retains the previous payload during a
  // reconnect or generation change, but its top_level_ref may be obsolete.
  const key = { kind: "location", ref: "local:nested" } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: {
      generation_id: "generation_old",
      revision: 1,
      ref: "local:nested",
      top_level_ref: "local:stale-owner",
      top_level: false,
    },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "etag",
    loading: false,
    stale: true,
    error: null,
    generationID: "generation_old",
  });
  navigationStore.setState({ resources });

  const workspace = workspaceStore.getState();
  const mainId = workspace.replacePrimary("session", { ref: "local:owner" });
  const nestedId = workspace.openPane("session", { ref: "local:nested" });
  workspace.focusPane(nestedId);

  openTranscript("local:child", "local:nested");

  // The stale location's top_level_ref is not trusted: the retained main
  // session is preserved and the child's conversation opens beside it.
  const state = workspaceStore.getState();
  expect(state.mainPane()?.id).toBe(mainId);
  expect(state.panes.some((pane) => pane.id === nestedId)).toBe(true);
  expect(sessionPane("local:stale-owner")).toBeUndefined();
  const child = sessionPane("local:child");
  expect(child).toBeDefined();
  expect(child?.slot).toBe("secondary");
  expect(transcriptPanes("local:child")).toHaveLength(0);
});

test("preserves a restored workspace when the conversation owner cannot be proven", () => {
  const workspace = workspaceStore.getState();
  const mainId = workspace.replacePrimary("session", { ref: "local:owner" });
  const nestedId = workspace.openPane("session", { ref: "local:nested" });
  const unrelatedId = workspace.openPane("transcript", { ref: "job:job-unrelated", parentRef: "local:owner" });
  workspace.focusPane(nestedId);

  openTranscript("local:child", "local:nested");

  const state = workspaceStore.getState();
  // No location is loaded, so the nested parent cannot be proven to be the
  // owner. Replacing the primary with the unproven guess would discard the
  // restored main session and every secondary pane.
  expect(state.mainPane()?.id).toBe(mainId);
  expect(state.mainPane()?.slot).toBe("main");
  expect(state.panes.some((pane) => pane.id === nestedId)).toBe(true);
  expect(state.panes.some((pane) => pane.id === unrelatedId)).toBe(true);
  expect(sessionPane("local:nested")?.id).toBe(nestedId);
  const child = sessionPane("local:child");
  expect(child).toBeDefined();
  expect(child?.slot).toBe("secondary");
  expect(state.focusedPaneId).toBe(child?.id);
});

test("promotes the proven top-level owner from a loaded navigation location", () => {
  seedLocation("local:nested", "local:owner");
  const workspace = workspaceStore.getState();
  workspace.replacePrimary("session", { ref: "local:elsewhere" });
  const nestedId = workspace.openPane("session", { ref: "local:nested" });
  workspace.focusPane(nestedId);

  openTranscript("local:child", "local:nested");

  // Ownership is proven: the top-level owner moves into main, never the
  // immediate nested parent. The promotion replaces the whole pane set, so
  // the nested session pane yields to the opened child conversation.
  expect(sessionPane("local:owner")?.slot).toBe("main");
  expect(sessionPane("local:nested")).toBeUndefined();
  const child = sessionPane("local:child");
  expect(child).toBeDefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(child?.id);
});

test("keeps the retained main session when the parent's conversation chain reaches it", () => {
  // The parent's ancestry runs through the RECORDED conversation parent
  // edges: the retained main session is an ancestor of the immediate parent,
  // so ownership is already satisfied and nothing may be promoted. The
  // parent's location names a different canonical owner (a continuation);
  // trusting it would replace the retained workspace and discard every
  // secondary pane.
  const workspace = workspaceStore.getState();
  const rootId = workspace.replacePrimary("session", { ref: "local:root" });
  const midId = workspace.openPane("session", { ref: "local:mid" });
  const sideId = workspace.openPane("session", { ref: "local:side" });
  const mid = workspaceStore.getState().panes.find((pane) => pane.id === midId);
  if (!mid) throw new Error("mid pane missing");
  recordConversationOpenParent(mid, "local:root");
  seedLocation("local:mid", "local:other");
  workspace.focusPane(midId);

  openTranscript("local:child", "local:mid");

  const state = workspaceStore.getState();
  expect(state.mainPane()?.id).toBe(rootId);
  expect(state.panes.some((pane) => pane.id === midId)).toBe(true);
  expect(state.panes.some((pane) => pane.id === sideId)).toBe(true);
  expect(sessionPane("local:other")).toBeUndefined();
  const child = sessionPane("local:child");
  expect(child).toBeDefined();
  expect(child?.slot).toBe("secondary");
});

test("keeps a job ref on the job-log transcript pane with its owner context", () => {
  workspaceStore.getState().openPane("session", { ref: "local:owner" });
  openTranscript("job:job-1", "local:owner");

  const first = transcriptPanes("job:job-1");
  expect(first).toHaveLength(1);
  expect(first[0]?.params).toEqual({ ref: "job:job-1", parentRef: "local:owner" });
  expect(workspaceStore.getState().focusedPaneId).toBe(first[0]?.id);

  // Reopening the same job log focuses the exact pane without a duplicate.
  openTranscript("job:job-1", "local:owner");
  expect(transcriptPanes("job:job-1")).toHaveLength(1);
  expect(workspaceStore.getState().focusedPaneId).toBe(first[0]?.id);
});

test("session opener prefers a presented duplicate and leaves parked parent metadata unchanged", () => {
  const parkedId = workspaceStore.getState().openPane("session", { ref: "local:child" });
  const parked = sessionPane("local:child");
  if (!parked) throw new Error("missing source session");
  recordConversationOpenParent(parked, "local:old-parent");
  const entry = captureAttentionPlacement();
  if (!entry) throw new Error("missing attention source");
  presentAttentionTarget("local:away");
  const presentedId = workspaceStore.getState().openPane("session", { ref: "local:child" });
  const presented = workspaceStore.getState().panes.find((pane) => pane.id === presentedId);
  if (!presented) throw new Error("missing presented duplicate");

  openTranscript("local:child", "local:new-parent");

  expect(workspaceStore.getState().focusedPaneId).toBe(presentedId);
  expect(conversationOpenParentRef(presented)).toBe("local:new-parent");
  expect(conversationOpenParentRef(parked)).toBe("local:old-parent");
  expect(workspaceStore.getState().panes).toContain(entry.pane);
  expect(parkedId).not.toBe(presentedId);
});

test("session opener creates a presented pane when only a parked match exists", () => {
  const parkedId = workspaceStore.getState().openPane("session", { ref: "local:child" });
  const parked = sessionPane("local:child");
  if (!parked) throw new Error("missing source session");
  recordConversationOpenParent(parked, "local:old-parent");
  const lifetime = conversationPaneLifetime(parked);
  const entry = captureAttentionPlacement();
  if (!entry) throw new Error("missing attention source");
  presentAttentionTarget("local:away");

  openTranscript("local:child", "local:new-parent");

  const opened = workspaceStore
    .getState()
    .panes.find(
      (pane) =>
        pane.type === "session" && (pane.params as { ref?: unknown }).ref === "local:child" && pane.slot !== "parked",
    );
  if (!opened) throw new Error("missing fresh presented session");
  expect(opened.id).not.toBe(parkedId);
  expect(workspaceStore.getState().focusedPaneId).toBe(opened.id);
  expect(conversationOpenParentRef(opened)).toBe("local:new-parent");
  expect(conversationOpenParentRef(parked)).toBe("local:old-parent");
  expect(conversationPaneLifetime(parked)).toBe(lifetime);
  expect(lifetime.alive).toBe(true);
  expect(workspaceStore.getState().panes).toContain(entry.pane);
});

test("job opener reuses presented duplicate without closing parked variants", () => {
  const parkedId = workspaceStore.getState().openPane("transcript", { ref: "job:job-1", parentRef: "local:old" });
  const parked = workspaceStore.getState().panes.find((pane) => pane.id === parkedId);
  if (!parked) throw new Error("missing job transcript");
  const lifetime = conversationPaneLifetime(parked);
  const entry = captureAttentionPlacement();
  if (!entry) throw new Error("missing attention source");
  presentAttentionTarget("local:away");
  const presentedId = workspaceStore.getState().openPane("transcript", { ref: "job:job-1", parentRef: "local:new" });

  openTranscript("job:job-1", "local:new");

  expect(workspaceStore.getState().focusedPaneId).toBe(presentedId);
  expect(transcriptPanes("job:job-1").map((pane) => pane.id)).toContain(parkedId);
  expect(workspaceStore.getState().panes.find((pane) => pane.id === parkedId)).toBe(parked);
  expect(conversationPaneLifetime(parked)).toBe(lifetime);
  expect(lifetime.alive).toBe(true);
  expect(workspaceStore.getState().panes).toContain(entry.pane);
});

test("job opener creates a fresh presented transcript when only parked variants exist", () => {
  const parkedId = workspaceStore.getState().openPane("transcript", { ref: "job:job-1", parentRef: "local:old" });
  const parked = workspaceStore.getState().panes.find((pane) => pane.id === parkedId);
  if (!parked) throw new Error("missing job transcript");
  const lifetime = conversationPaneLifetime(parked);
  const entry = captureAttentionPlacement();
  if (!entry) throw new Error("missing attention source");
  presentAttentionTarget("local:away");

  openTranscript("job:job-1", "local:new");

  const opened = transcriptPanes("job:job-1").find((pane) => pane.slot !== "parked");
  if (!opened) throw new Error("missing fresh presented job transcript");
  expect(opened.id).not.toBe(parkedId);
  expect(opened.params).toEqual({ ref: "job:job-1", parentRef: "local:new" });
  expect(transcriptPanes("job:job-1").map((pane) => pane.id)).toContain(parkedId);
  expect(conversationPaneLifetime(parked)).toBe(lifetime);
  expect(lifetime.alive).toBe(true);
  expect(workspaceStore.getState().panes).toContain(entry.pane);
});

test("parent context lookup prefers a presented same-ref holder over retained context", () => {
  const parkedId = workspaceStore.getState().openPane("session", { ref: "local:parent" });
  const parked = sessionPane("local:parent");
  if (!parked) throw new Error("missing parked parent context");
  recordConversationOpenParent(parked, "local:old-root");
  const entry = captureAttentionPlacement();
  if (!entry) throw new Error("missing attention source");
  presentAttentionTarget("local:away");
  const presentedId = workspaceStore.getState().openPane("session", { ref: "local:parent" });
  const presented = workspaceStore.getState().panes.find((pane) => pane.id === presentedId);
  if (!presented) throw new Error("missing presented parent duplicate");
  recordConversationOpenParent(presented, "local:new-root");

  expect(conversationContextIncludes("local:parent", "local:new-root")).toBe(true);
  expect(conversationContextIncludes("local:parent", "local:old-root")).toBe(false);

  openTranscript("local:child", "local:parent");

  expect(sessionPane("local:child")?.slot).toBe("secondary");
  expect(workspaceStore.getState().panes.find((pane) => pane.id === parkedId)).toBe(parked);
  expect(workspaceStore.getState().panes).toContain(entry.pane);
  expect(presented.id).toBe(presentedId);
});

test("the row button names the conversation it opens for a session ref", () => {
  render(<OpenTranscriptButton transcriptRef="local:child" parentRef="local:owner" />);
  expect(screen.getByRole("button", { name: "Open conversation" })).toBeTruthy();

  render(<OpenTranscriptButton transcriptRef="job:job-1" parentRef="local:owner" label="Open transcript" />);
  expect(screen.getByRole("button", { name: "Open transcript" })).toBeTruthy();
});
